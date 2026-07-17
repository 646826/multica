package jira

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Reconcile worker: one serial cycle per Connection (AD-1/AD-4). The worker
// scans enabled Connections on a coarse tick and runs each Connection's cycle
// at its own cadence (±20% jitter). A cycle holds a dedicated pooled
// connection for its whole duration and takes a session advisory lock ON THAT
// CONNECTION — pool-shared session locks leak (rubric F3); if the lock is
// busy (another replica or an overlapping tick) the tick is skipped, never
// queued. The goroutine only exists when the deployment is configured
// (zero-footprint, NFR-4); with no enabled Connections it idles on the scan.

const (
	// scanInterval is the coarse tick that discovers due Connections; each
	// Connection then honors its own cycle_interval_seconds ± jitter.
	scanInterval = 5 * time.Second

	// advisoryNamespace disambiguates jira sync locks from other advisory
	// users (property.go uses its own constants; runtime rollup uses 4246).
	advisoryNamespace int64 = 0x6A697261 // "jira"
)

// Worker drives reconcile cycles for every enabled Connection.
type Worker struct {
	Pool    *pgxpool.Pool
	Q       *db.Queries
	Svc     *Service
	Issues  *service.IssueService
	Tasks   *service.TaskService
	Bus     *events.Bus
	Journal *Journal

	// nextRun tracks per-connection due times in-process (jittered cadence);
	// correctness does not depend on it — the advisory lock is the guard.
	nextRun map[[16]byte]time.Time

	// hooks for tests
	now   func() time.Time
	sleep func(d time.Duration)
}

func NewWorker(pool *pgxpool.Pool, q *db.Queries, svc *Service, issues *service.IssueService, tasks *service.TaskService, bus *events.Bus) *Worker {
	return &Worker{
		Pool:    pool,
		Q:       q,
		Svc:     svc,
		Issues:  issues,
		Tasks:   tasks,
		Bus:     bus,
		Journal: &Journal{Q: q},
		nextRun: map[[16]byte]time.Time{},
		now:     time.Now,
		sleep:   time.Sleep,
	}
}

// Run loops until ctx is done. Panics in a cycle are recovered per tick so a
// single bad Connection cannot kill the worker for the whole deployment.
func (w *Worker) Run(ctx context.Context) {
	slog.Info("jira: reconcile worker started")
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("jira: reconcile worker stopped")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

// tick runs due Connections serially (per-deployment worker; Connections are
// few in v1 — parallelism is a later knob, correctness first).
func (w *Worker) tick(ctx context.Context) {
	conns, err := w.Q.ListEnabledJiraConnections(ctx)
	if err != nil {
		slog.Error("jira: list enabled connections failed", "error", err)
		return
	}
	now := w.now()
	for _, conn := range conns {
		due, ok := w.nextRun[conn.ID.Bytes]
		if ok && now.Before(due) {
			continue
		}
		w.runCycleRecovered(ctx, conn)
		interval := time.Duration(conn.CycleIntervalSeconds) * time.Second
		jitter := time.Duration(rand.Int64N(int64(interval) / 5)) // ±20% one-sided
		w.nextRun[conn.ID.Bytes] = w.now().Add(interval + jitter - interval/10)
	}
}

// runCycleRecovered isolates panics per cycle (poison Connection isolation).
func (w *Worker) runCycleRecovered(ctx context.Context, conn db.JiraConnection) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("jira: cycle panic recovered", "connection_id", uuidStr(conn.ID), "panic", r)
		}
	}()
	if _, err := w.runCycle(ctx, conn); err != nil {
		slog.Error("jira: cycle failed", "connection_id", uuidStr(conn.ID), "error", err)
	}
}

// advisoryKey derives the per-Connection lock key from the UUID's first
// 8 bytes, namespaced so other advisory-lock users cannot collide.
func advisoryKey(id pgtype.UUID) int64 {
	return advisoryNamespace ^ int64(binary.BigEndian.Uint64(id.Bytes[0:8]))
}

// runCycle executes one reconcile pass under the per-Connection lock.
// It reports whether the cycle actually ran (false = lock busy).
func (w *Worker) runCycle(ctx context.Context, conn db.JiraConnection) (bool, error) {
	pc, err := w.Pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer pc.Release()

	var locked bool
	if err := pc.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", advisoryKey(conn.ID)).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	defer func() {
		// Unlock on the SAME session that took the lock; Release() alone
		// would leak the session lock into the pool.
		_, _ = pc.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryKey(conn.ID))
	}()

	cycleID := pgtype.UUID{Bytes: randomUUIDBytes(), Valid: true}
	requests, err := w.cycleBody(ctx, conn, cycleID)
	w.recordHealth(ctx, conn, cycleID, requests, err)
	w.Journal.Prune(ctx, conn.ID)
	return true, err
}

// cycleBody is the observe→diff→plan→apply→record pass (AD-1). The Cursor
// advances only after every observed issue applied cleanly; a failing cycle
// keeps it so the whole window retries next cycle (idempotent claims make
// re-processing free).
func (w *Worker) cycleBody(ctx context.Context, conn db.JiraConnection, cycleID pgtype.UUID) (int64, error) {
	client, err := w.Svc.ClientFor(conn)
	if err != nil {
		return 0, err
	}

	fieldRows, err := ParseFieldMap(conn.FieldMap)
	if err != nil {
		return client.Requests(), err
	}
	var fieldIDs []string
	for _, r := range fieldRows {
		fieldIDs = append(fieldIDs, r.ExternalField)
	}
	sm, err := ParseStatusMap(conn.StatusMap)
	if err != nil {
		return client.Requests(), err
	}

	// Resolve outbound issue creations first (AD-15): finalize any pending
	// create (adopt-or-create) before the observe loop, so a Multica-origin
	// Jira issue is never seen as a new inbound issue mid-flight. The inbound
	// importer additionally skips marker-bearing issues (belt and suspenders).
	if err := w.syncOutboundCreates(ctx, conn, cycleID); err != nil {
		return client.Requests(), err
	}

	observed, truncated, err := observeJira(ctx, client, conn, fieldIDs)
	if err != nil {
		return client.Requests(), err
	}
	if truncated {
		_ = w.Journal.Record(ctx, conn, cycleID, JournalObserveTruncated, pgtype.UUID{}, "", map[string]any{
			"page_cap": observePageCap,
		})
	}

	var maxUpdated time.Time
	var seenLinked []string
	processed := map[[16]byte]bool{} // links already planned this cycle
	for _, obs := range observed {
		if obs.Updated.After(maxUpdated) {
			maxUpdated = obs.Updated
		}
		link, lerr := w.Q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{
			ConnectionID: conn.ID, JiraIssueID: obs.ID,
		})
		switch {
		case errors.Is(lerr, pgx.ErrNoRows):
			if conn.CreateFromJira {
				if err := w.importIssue(ctx, conn, sm, obs, cycleID); err != nil {
					// Poison isolation (AD-2/NFR-3): a failing import journals
					// and is skipped; its pending link makes the next cycle
					// retry, and the Cursor still advances past it.
					_ = w.Journal.Record(ctx, conn, cycleID, JournalItemDirty, pgtype.UUID{}, obs.Key, map[string]any{
						"error": redactError(err), "at": "import",
					})
				}
			}
		case lerr != nil:
			return client.Requests(), lerr
		case link.State == "pending":
			if err := w.importIssue(ctx, conn, sm, obs, cycleID); err != nil {
				_ = w.Journal.Record(ctx, conn, cycleID, JournalItemDirty, link.IssueID, obs.Key, map[string]any{
					"error": redactError(err), "at": "import_retry",
				})
			}
		case link.State == "ok":
			seenLinked = append(seenLinked, obs.ID)
			processed[link.ID.Bytes] = true
			w.updateIssue(ctx, conn, sm, fieldRows, link, &obs, cycleID)
			// Live pair: new comments wake mentioned agents (wakeEnabled=true).
			if cerr := w.mirrorComments(ctx, conn, link, cycleID, true); cerr != nil {
				_ = w.Journal.Record(ctx, conn, cycleID, JournalItemDirty, link.IssueID, obs.Key, map[string]any{
					"error": redactError(cerr), "at": "comments",
				})
			}
		case link.State == "dormant" || link.State == "orphaned":
			// Re-entering scope resumes the SAME Link (FR-11/FR-14) — the
			// pair picks up where it left off, no duplicate mirror.
			if err := w.Q.SetJiraLinkState(ctx, db.SetJiraLinkStateParams{ID: link.ID, State: "ok"}); err != nil {
				return client.Requests(), err
			}
			_ = w.Journal.Record(ctx, conn, cycleID, JournalScopeResumed, link.IssueID, obs.Key, map[string]any{
				"previous_state": link.State,
			})
			seenLinked = append(seenLinked, obs.ID)
			link.State = "ok"
			processed[link.ID.Bytes] = true
			w.updateIssue(ctx, conn, sm, fieldRows, link, &obs, cycleID)
		}
	}
	// Dirty rescan (AD-2): due retries re-enter the observe set with a fresh
	// single-issue fetch, so a poison item never pins the Cursor.
	dirty, derr := w.Q.ListDueDirtyJiraLinks(ctx, db.ListDueDirtyJiraLinksParams{
		ConnectionID: conn.ID, Limit: 50,
	})
	if derr != nil {
		return client.Requests(), derr
	}
	for _, link := range dirty {
		if link.State != "ok" {
			continue
		}
		remote, gerr := client.GetIssue(ctx, link.JiraIssueID, fieldIDs)
		if gerr != nil {
			_ = w.Journal.Record(ctx, conn, cycleID, JournalItemDirty, link.IssueID, link.JiraKey, map[string]any{
				"error": redactError(gerr), "at": "dirty_refresh",
			})
			if merr := w.Q.MarkJiraLinkDirty(ctx, db.MarkJiraLinkDirtyParams{
				ID:      link.ID,
				RetryAt: pgtype.Timestamptz{Time: nextRetryAt(w.now(), link.RetryCount), Valid: true},
			}); merr != nil {
				slog.Error("jira: re-mark dirty failed", "link_id", uuidStr(link.ID), "error", merr)
			}
			continue
		}
		md, lossy := ADFToMarkdown(remote.DescriptionADF)
		obs := ObservedIssue{
			ID: remote.ID, Key: remote.Key, Summary: remote.Summary,
			DescriptionMD: md, DescriptionLossy: lossy,
			StatusID: remote.StatusID, StatusName: remote.StatusName,
			StatusCategory: remote.StatusCategory, Labels: remote.Labels,
			Fields: map[string]string{}, Updated: remote.Updated,
		}
		for k, v := range remote.Fields {
			obs.Fields[k] = string(v)
		}
		processed[link.ID.Bytes] = true
		w.updateIssue(ctx, conn, sm, fieldRows, link, &obs, cycleID)
	}

	if len(seenLinked) > 0 {
		if err := w.Q.TouchJiraLinksSeen(ctx, db.TouchJiraLinksSeenParams{
			ConnectionID: conn.ID, Column2: seenLinked,
		}); err != nil {
			return client.Requests(), err
		}
	}

	// Local observation (AD-2, Multica side): pairs whose issue row changed
	// since the local Cursor run the same planner with Remote unset — the
	// outbound arms (transitions now; fields/labels with their stories) fire
	// from here.
	localCut := conn.LocalCursor
	if !localCut.Valid {
		localCut = pgtype.Timestamptz{Time: w.now().Add(-time.Hour), Valid: true}
	}
	localChanged, lerr := w.Q.ListLocallyChangedLinkedIssues(ctx, db.ListLocallyChangedLinkedIssuesParams{
		ConnectionID: conn.ID,
		UpdatedAt:    pgtype.Timestamptz{Time: localCut.Time.Add(-observeOverlap), Valid: true},
		Limit:        int32(observePageCap),
	})
	if lerr != nil {
		return client.Requests(), lerr
	}
	var maxLocal time.Time
	for _, row := range localChanged {
		if row.IssueUpdatedAt.Time.After(maxLocal) {
			maxLocal = row.IssueUpdatedAt.Time
		}
		if processed[row.ID.Bytes] {
			// Already planned this cycle from the Jira side (both-sided change);
			// the inbound plan considered the local values too — re-planning
			// would only burn a redundant GetIssue and double-count failures.
			continue
		}
		link := db.JiraLink{
			ID: row.ID, ConnectionID: row.ConnectionID, WorkspaceID: row.WorkspaceID,
			IssueID: row.IssueID, JiraIssueID: row.JiraIssueID, JiraKey: row.JiraKey,
			State: row.State, Items: row.Items, Dirty: row.Dirty,
			RetryAt: row.RetryAt, RetryCount: row.RetryCount, LastSeenAt: row.LastSeenAt,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}
		w.updateIssue(ctx, conn, sm, fieldRows, link, nil, cycleID)
	}

	if err := w.syncOutboundComments(ctx, conn, cycleID); err != nil {
		return client.Requests(), err
	}

	if err := w.sweepUnseenLinks(ctx, client, conn, cycleID); err != nil {
		return client.Requests(), err
	}

	// Record: the Cursor advances to the newest applied observation; an
	// empty first scan initializes it to now.
	jiraCursor := conn.JiraCursor
	if !maxUpdated.IsZero() {
		jiraCursor = pgtype.Timestamptz{Time: maxUpdated.UTC(), Valid: true}
	} else if !jiraCursor.Valid {
		jiraCursor = pgtype.Timestamptz{Time: w.now().UTC(), Valid: true}
	}
	localCursor := conn.LocalCursor
	if !maxLocal.IsZero() {
		localCursor = pgtype.Timestamptz{Time: maxLocal.UTC(), Valid: true}
	} else if !localCursor.Valid {
		localCursor = pgtype.Timestamptz{Time: w.now().UTC(), Valid: true}
	}
	if err := w.Q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
		ID: conn.ID, JiraCursor: jiraCursor, LocalCursor: localCursor,
	}); err != nil {
		return client.Requests(), err
	}
	return client.Requests(), nil
}

// healthSnapshot is the wire shape stored in jira_connection.health.
type healthSnapshot struct {
	State             string `json:"state"` // ok|degraded|auth_expired|forbidden
	LastCycleAt       string `json:"last_cycle_at"`
	LastError         string `json:"last_error,omitempty"`
	RequestsLastCycle int64  `json:"requests_last_cycle,omitempty"`
}

// recordHealth classifies the cycle outcome (auth vs permission vs generic)
// and journals failures with their paired recovery events (AD-14, FR-4).
func (w *Worker) recordHealth(ctx context.Context, conn db.JiraConnection, cycleID pgtype.UUID, requests int64, cycleErr error) {
	snap := healthSnapshot{State: "ok", LastCycleAt: w.now().UTC().Format(time.RFC3339), RequestsLastCycle: requests}
	if cycleErr != nil {
		snap.State = "degraded"
		var apiErr *APIError
		if errors.As(cycleErr, &apiErr) {
			if apiErr.IsAuth() {
				snap.State = "auth_expired"
			} else if apiErr.IsForbidden() {
				snap.State = "forbidden"
			}
		}
		snap.LastError = redactError(cycleErr)
		_ = w.Journal.Record(ctx, conn, cycleID, JournalCycleError, pgtype.UUID{}, "", map[string]any{
			"state": snap.State, "error": snap.LastError,
		})
	} else if wasDegraded(conn.Health) {
		_ = w.Journal.Record(ctx, conn, cycleID, JournalCycleRecovered, pgtype.UUID{}, "", nil)
	}

	payload, err := json.Marshal(snap)
	if err != nil {
		payload = []byte(`{}`)
	}
	if err := w.Q.UpdateJiraConnectionHealth(ctx, db.UpdateJiraConnectionHealthParams{
		ID: conn.ID, Health: payload,
	}); err != nil {
		slog.Error("jira: health update failed", "connection_id", uuidStr(conn.ID), "error", err)
	}
}

func wasDegraded(health []byte) bool {
	var snap healthSnapshot
	if json.Unmarshal(health, &snap) != nil {
		return false
	}
	return snap.State != "" && snap.State != "ok"
}

// redactError keeps messages human-useful without leaking credentials: the
// client never embeds tokens in errors, so bounding length suffices.
func redactError(err error) string {
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

func randomUUIDBytes() [16]byte {
	var b [16]byte
	u := rand.Uint64()
	binary.BigEndian.PutUint64(b[0:8], u)
	binary.BigEndian.PutUint64(b[8:16], rand.Uint64())
	// RFC 4122 v4 bits so the value round-trips as a UUID.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return b
}

// sweepUnseenLinks verifies Links that have not been observed for a prolonged
// window (FR-11/FR-14): deleted or moved-away issues become orphaned, issues
// filtered out by the JQL become dormant, and quiet-but-healthy pairs are
// re-touched. Bounded per cycle so the verification budget stays fixed.
const (
	unseenThreshold = 24 * time.Hour
	sweepBatch      = 20
)

func (w *Worker) sweepUnseenLinks(ctx context.Context, client *Client, conn db.JiraConnection, cycleID pgtype.UUID) error {
	cutoff := pgtype.Timestamptz{Time: w.now().Add(-unseenThreshold), Valid: true}
	links, err := w.Q.ListJiraLinksUnseenSince(ctx, db.ListJiraLinksUnseenSinceParams{
		ConnectionID: conn.ID, LastSeenAt: cutoff, Limit: sweepBatch,
	})
	if err != nil {
		return err
	}
	var healthy []string
	for _, link := range links {
		remote, gerr := client.GetIssue(ctx, link.JiraIssueID, nil)
		if gerr != nil {
			var apiErr *APIError
			if errors.As(gerr, &apiErr) && apiErr.Status == 404 {
				if serr := w.Q.SetJiraLinkState(ctx, db.SetJiraLinkStateParams{ID: link.ID, State: "orphaned"}); serr != nil {
					return serr
				}
				_ = w.Journal.Record(ctx, conn, cycleID, JournalLinkOrphaned, link.IssueID, link.JiraKey, map[string]any{
					"reason": "issue deleted or inaccessible",
				})
				continue
			}
			return gerr
		}
		// A key whose project prefix changed means a Jira project move.
		if !strings.HasPrefix(remote.Key, conn.ProjectKey+"-") {
			if serr := w.Q.SetJiraLinkState(ctx, db.SetJiraLinkStateParams{ID: link.ID, State: "orphaned"}); serr != nil {
				return serr
			}
			_ = w.Journal.Record(ctx, conn, cycleID, JournalLinkOrphaned, link.IssueID, link.JiraKey, map[string]any{
				"reason": "moved out of the connected project", "new_key": remote.Key,
			})
			continue
		}
		if conn.JqlFilter != "" {
			inScope, serr := w.issueMatchesScope(ctx, client, conn, link.JiraIssueID)
			if serr != nil {
				return serr
			}
			if !inScope {
				if uerr := w.Q.SetJiraLinkState(ctx, db.SetJiraLinkStateParams{ID: link.ID, State: "dormant"}); uerr != nil {
					return uerr
				}
				_ = w.Journal.Record(ctx, conn, cycleID, JournalScopeDormant, link.IssueID, link.JiraKey, nil)
				continue
			}
		}
		healthy = append(healthy, link.JiraIssueID)
	}
	if len(healthy) > 0 {
		return w.Q.TouchJiraLinksSeen(ctx, db.TouchJiraLinksSeenParams{ConnectionID: conn.ID, Column2: healthy})
	}
	return nil
}

// issueMatchesScope asks Jira whether one issue still matches the narrowed
// scope (JQL cannot be evaluated locally).
func (w *Worker) issueMatchesScope(ctx context.Context, client *Client, conn db.JiraConnection, jiraIssueID string) (bool, error) {
	jql := fmt.Sprintf("project = %q AND id = %s AND (%s)", conn.ProjectKey, jiraIssueID, conn.JqlFilter)
	found, _, err := client.SearchJQLIDs(ctx, jql, 1)
	if err != nil {
		return false, err
	}
	return len(found) > 0, nil
}
