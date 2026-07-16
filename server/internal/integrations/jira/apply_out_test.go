package jira

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Story 3.2: outbound comments are intent-first exactly-once.

func TestOutboundCommentPostsOnceWithAttributionAndMarker(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	var posts atomic.Int64
	var lastBody atomic.Value
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[%s],"isLast":true}`,
			searchIssueWithCategory("GAME-30", now, "100", "new"))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-30/comment", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			lastBody.Store(string(body))
			posts.Add(1)
			w.Write([]byte(`{"id":"jc-100"}`))
			return
		}
		w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import cycle: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-30"})

	// An agent comments locally.
	agentID := testUUID()
	comment, err := q.CreateComment(ctx, db.CreateCommentParams{
		IssueID: link.IssueID, WorkspaceID: conn.WorkspaceID,
		AuthorType: "agent", AuthorID: agentID, Content: "I fixed the crash", Type: "comment",
	})
	if err != nil {
		t.Fatal(err)
	}

	c2, _ := q.GetJiraConnectionByID(ctx, conn.ID)
	if _, err := w.runCycle(ctx, c2); err != nil {
		t.Fatalf("outbound cycle: %v", err)
	}

	if posts.Load() != 1 {
		t.Fatalf("want exactly one POST, got %d", posts.Load())
	}
	body, _ := lastBody.Load().(string)
	if !strings.Contains(body, "(agent) via Multica") || !strings.Contains(body, "I fixed the crash") || !strings.Contains(body, "[mc-") {
		t.Fatalf("posted body must carry attribution + content + marker: %s", body)
	}

	clink, err := q.GetJiraCommentLinkByJiraID(ctx, db.GetJiraCommentLinkByJiraIDParams{ConnectionID: conn.ID, JiraCommentID: "jc-100"})
	if err != nil || clink.State != "ok" || clink.CommentID != comment.ID {
		t.Fatalf("outbound link not finalized: %v %+v", err, clink)
	}

	// Replay: nothing new to push.
	c3, _ := q.GetJiraConnectionByID(ctx, conn.ID)
	if _, err := w.runCycle(ctx, c3); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("replay must not re-post: %d", posts.Load())
	}
}

func TestOutboundCommentCrashWindowAdoptsInsteadOfRepost(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	var posts atomic.Int64
	var postedMarker atomic.Value
	postedMarker.Store("")
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[%s],"isLast":true}`,
			searchIssueWithCategory("GAME-31", now, "100", "new"))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-31/comment", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.Write([]byte(`{"id":"jc-201"}`))
			return
		}
		marker, _ := postedMarker.Load().(string)
		if marker == "" {
			w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
			return
		}
		// The interrupted POST landed: sync's own comment is observable with
		// the marker in its body.
		fmt.Fprintf(w, `{"comments":[
			{"id":"jc-999","author":{"accountId":"acc-bot","displayName":"Sync Bot"},
			 "body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"posted [%s]"}]}]},
			 "created":%q}
		],"startAt":0,"maxResults":100,"total":1}`, marker, now.Format(jiraTimeLayout))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	if err := q.UpdateJiraConnectionServiceAccount(ctx, db.UpdateJiraConnectionServiceAccountParams{
		ID: conn.ID, ServiceAccountID: "acc-bot",
	}); err != nil {
		t.Fatal(err)
	}
	conn, _ = q.GetJiraConnectionByID(ctx, conn.ID)

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-31"})

	// Simulate the crash window: the local comment exists, its intent row is
	// pending with a marker, and the Jira POST already landed (jc-999) — but
	// finalize never ran.
	comment, err := q.CreateComment(ctx, db.CreateCommentParams{
		IssueID: link.IssueID, WorkspaceID: conn.WorkspaceID,
		AuthorType: "member", AuthorID: testUUID(), Content: "went out before crash", Type: "comment",
	})
	if err != nil {
		t.Fatal(err)
	}
	marker := commentMarker(comment.ID)
	if _, err := q.ClaimJiraCommentLinkOutbound(ctx, db.ClaimJiraCommentLinkOutboundParams{
		ConnectionID: conn.ID, WorkspaceID: conn.WorkspaceID,
		IssueID: link.IssueID, CommentID: comment.ID, Marker: marker,
	}); err != nil {
		t.Fatal(err)
	}
	postedMarker.Store(marker)

	// Rewind so the issue re-enters the observed window (comments re-listed).
	c2, _ := q.GetJiraConnectionByID(ctx, conn.ID)
	_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
		ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-10 * time.Minute), Valid: true}, LocalCursor: c2.LocalCursor,
	})
	c2, _ = q.GetJiraConnectionByID(ctx, conn.ID)
	if _, err := w.runCycle(ctx, c2); err != nil {
		t.Fatalf("adopt cycle: %v", err)
	}

	if posts.Load() != 0 {
		t.Fatalf("adoption must not re-post: %d POSTs", posts.Load())
	}
	clink, err := q.GetJiraCommentLinkByJiraID(ctx, db.GetJiraCommentLinkByJiraIDParams{ConnectionID: conn.ID, JiraCommentID: "jc-999"})
	if err != nil || clink.State != "ok" || clink.CommentID != comment.ID {
		t.Fatalf("intent must be adopted onto the observed artifact: %v %+v", err, clink)
	}
}
