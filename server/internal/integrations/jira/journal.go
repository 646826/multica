package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Journal kinds form a CLOSED registry (AD-14): every skip, guard, conflict,
// degradation, rule action and failure writes exactly one row with a kind
// from this enum, and failure kinds carry a paired recovery kind so Health
// can show "cleared". Adding a kind is a code change here — never an ad-hoc
// string at a call site. The journal is bounded-retention observability;
// correctness state lives on jira_link rows, never here (AD-3).

// JournalKind enumerates every event class the journal may record.
type JournalKind string

const (
	// Management.
	JournalConfigChanged JournalKind = "config_changed"
	// Cycle lifecycle.
	JournalCycleError     JournalKind = "cycle_error"
	JournalCycleRecovered JournalKind = "cycle_recovered"
	// Observation.
	JournalObserveTruncated JournalKind = "observe_truncated"
	// Policy skips (planner-emitted, SM-5 classes).
	JournalStatusUnmapped    JournalKind = "status_unmapped"
	JournalInboundSuppressed JournalKind = "inbound_suppressed"
	// Inbound import (AD-15 protocol).
	JournalImportAdopted      JournalKind = "import_adopted"
	JournalImportMarkerFailed JournalKind = "import_marker_failed"
	// Per-item transient failures (dirty ladder, AD-2) with recovery pairing.
	JournalItemDirty     JournalKind = "item_dirty"
	JournalItemRecovered JournalKind = "item_recovered"
	// Scope & identity lifecycle (FR-11/FR-14).
	JournalScopeDormant JournalKind = "scope_dormant"
	// Comments.
	JournalRestrictedCommentDropped JournalKind = "restricted_comment_dropped"
	JournalCommentAdopted           JournalKind = "comment_adopted"
	// Outbound status (FR-17) with recovery pairing.
	JournalTransitionUnreachable JournalKind = "transition_unreachable"
	JournalTransitionRecovered   JournalKind = "transition_recovered"
	// Outbound creation (FR-9, AD-15).
	JournalOutboundCreated  JournalKind = "outbound_created"
	JournalCreateRejected   JournalKind = "create_rejected"
	JournalLabelTransformed JournalKind = "label_transformed"
	JournalFieldSkipped     JournalKind = "field_skipped"
	// Agent tagging (FR-27) — application and guarded skips.
	JournalTagAssigned JournalKind = "tag_assigned"
	JournalTagSkipped  JournalKind = "tag_skipped"
	// Mention bridge (FR-29): a mention woke its agent / could not wake it. The
	// woken record carries the Jira author accountId so every agent run traces
	// back to the human who triggered it.
	JournalMentionWoken  JournalKind = "mention_woken"
	JournalMentionDenied JournalKind = "mention_denied"
	JournalScopeResumed  JournalKind = "scope_resumed"
	JournalLinkOrphaned  JournalKind = "link_orphaned"
)

// journalKinds is the registry; Record refuses kinds outside it.
var journalKinds = map[JournalKind]bool{
	JournalConfigChanged:            true,
	JournalCycleError:               true,
	JournalCycleRecovered:           true,
	JournalObserveTruncated:         true,
	JournalStatusUnmapped:           true,
	JournalInboundSuppressed:        true,
	JournalImportAdopted:            true,
	JournalImportMarkerFailed:       true,
	JournalItemDirty:                true,
	JournalItemRecovered:            true,
	JournalScopeDormant:             true,
	JournalRestrictedCommentDropped: true,
	JournalCommentAdopted:           true,
	JournalTransitionUnreachable:    true,
	JournalTransitionRecovered:      true,
	JournalOutboundCreated:          true,
	JournalCreateRejected:           true,
	JournalLabelTransformed:         true,
	JournalFieldSkipped:             true,
	JournalTagAssigned:              true,
	JournalTagSkipped:               true,
	JournalMentionWoken:             true,
	JournalMentionDenied:            true,
	JournalScopeResumed:             true,
	JournalLinkOrphaned:             true,
}

// Journal retention bounds (operational envelope): whichever prunes more.
const (
	journalMaxAge  = 30 * 24 * time.Hour
	journalMaxRows = 100_000
)

// Journal writes and prunes jira_journal rows.
type Journal struct {
	Q *db.Queries
}

// Record appends one journal row. Unknown kinds are a programming error and
// are refused loudly (the closed-registry guarantee).
func (j *Journal) Record(ctx context.Context, conn db.JiraConnection, cycleID pgtype.UUID, kind JournalKind, issueID pgtype.UUID, jiraKey string, detail map[string]any) error {
	if !journalKinds[kind] {
		return fmt.Errorf("jira journal: kind %q is not in the closed registry", kind)
	}
	payload, err := json.Marshal(detail)
	if err != nil || detail == nil {
		payload = []byte(`{}`)
	}
	_, err = j.Q.InsertJiraJournal(ctx, db.InsertJiraJournalParams{
		ConnectionID: conn.ID,
		WorkspaceID:  conn.WorkspaceID,
		CycleID:      cycleID,
		Kind:         string(kind),
		IssueID:      issueID,
		JiraKey:      jiraKey,
		Detail:       payload,
	})
	if err != nil {
		// Journal loss must never fail sync work — log and continue (AD-14:
		// observability, not correctness).
		slog.Error("jira: journal write failed", "connection_id", uuidStr(conn.ID), "kind", kind, "error", err)
	}
	return nil
}

// Prune applies both retention bounds for one Connection.
func (j *Journal) Prune(ctx context.Context, connID pgtype.UUID) {
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-journalMaxAge), Valid: true}
	if _, err := j.Q.PruneJiraJournalByAge(ctx, db.PruneJiraJournalByAgeParams{
		ConnectionID: connID, CreatedAt: cutoff,
	}); err != nil {
		slog.Error("jira: journal age prune failed", "connection_id", uuidStr(connID), "error", err)
	}
	if _, err := j.Q.PruneJiraJournalByCount(ctx, db.PruneJiraJournalByCountParams{
		ConnectionID: connID, Offset: journalMaxRows,
	}); err != nil {
		slog.Error("jira: journal count prune failed", "connection_id", uuidStr(connID), "error", err)
	}
}

func uuidStr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
