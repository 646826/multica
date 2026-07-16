package jira

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

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
	Journal *Journal

	// nextRun tracks per-connection due times in-process (jittered cadence);
	// correctness does not depend on it — the advisory lock is the guard.
	nextRun map[[16]byte]time.Time

	// hooks for tests
	now   func() time.Time
	sleep func(d time.Duration)
}

func NewWorker(pool *pgxpool.Pool, q *db.Queries, svc *Service, issues *service.IssueService) *Worker {
	return &Worker{
		Pool:    pool,
		Q:       q,
		Svc:     svc,
		Issues:  issues,
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
					return client.Requests(), err
				}
			}
		case lerr != nil:
			return client.Requests(), lerr
		case link.State == "pending":
			if err := w.importIssue(ctx, conn, sm, obs, cycleID); err != nil {
				return client.Requests(), err
			}
		case link.State == "ok":
			seenLinked = append(seenLinked, obs.ID)
			// Update path lands with Story 2.4; observation is recorded so
			// the orphan sweep never flags a live pair.
		default:
			// dormant/orphaned pairs are fully suspended (FR-11/FR-14).
		}
	}
	if len(seenLinked) > 0 {
		if err := w.Q.TouchJiraLinksSeen(ctx, db.TouchJiraLinksSeenParams{
			ConnectionID: conn.ID, Column2: seenLinked,
		}); err != nil {
			return client.Requests(), err
		}
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
	if !localCursor.Valid {
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
