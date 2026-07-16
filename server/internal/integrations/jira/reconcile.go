package jira

import (
	"context"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// reconcile.go owns the observe half of the cycle (AD-1/AD-2): bounded,
// ordered views of "what changed since the Cursor" on each side. Apply arms
// land in apply_in.go / apply_out.go; diff.go plans between them.

const (
	// observeOverlap widens the cursor window to absorb JQL's minute
	// truncation and clock skew; idempotent applies make the resulting
	// re-observations harmless (AD-2).
	observeOverlap = 2 * time.Minute

	// observePageCap bounds one observation pass (operational envelope:
	// 500 changed issues per cycle; excess continues next cycle, journaled).
	observePageCap = 500
)

// ObservedIssue is a Jira-side observation ready for diffing: raw side-local
// values (status id, label set, mapped raw fields) plus canonical Markdown
// text produced by the single canonicalizer (AD-5, AD-7).
type ObservedIssue struct {
	ID               string
	Key              string
	Summary          string
	DescriptionMD    string
	DescriptionLossy bool
	StatusID         string
	StatusName       string
	StatusCategory   string
	AssigneeKey      string
	Labels           []string
	Fields           map[string]string // mapped field id → raw JSON value (canonical string form)
	Updated          time.Time
}

// observeJira fetches the Connection's changed issues since the Cursor
// (full-project scan while the Cursor is unset — the first-enable import
// path, FR-10). The JQL window is relative and minute-coarse; the precise
// cut uses each issue's RFC3339 timestamp against cursor−overlap.
func observeJira(ctx context.Context, client *Client, conn db.JiraConnection, fieldIDs []string) (issues []ObservedIssue, truncated bool, err error) {
	sinceMinutes := 0
	var preciseCut time.Time
	if conn.JiraCursor.Valid {
		preciseCut = conn.JiraCursor.Time.Add(-observeOverlap)
		mins := int(time.Until(preciseCut).Minutes())
		if mins >= 0 {
			// Cursor is in the future relative to now (clock skew): fall back
			// to the smallest window.
			sinceMinutes = 1
		} else {
			sinceMinutes = -mins + 1
		}
	}

	raw, truncated, err := client.SearchUpdated(ctx, conn.ProjectKey, conn.JqlFilter, sinceMinutes, fieldIDs, observePageCap)
	if err != nil {
		return nil, false, err
	}
	for _, ri := range raw {
		if !preciseCut.IsZero() && !ri.Updated.IsZero() && ri.Updated.Before(preciseCut) {
			continue
		}
		md, lossy := ADFToMarkdown(ri.DescriptionADF)
		oi := ObservedIssue{
			ID:               ri.ID,
			Key:              ri.Key,
			Summary:          ri.Summary,
			DescriptionMD:    md,
			DescriptionLossy: lossy,
			StatusID:         ri.StatusID,
			StatusName:       ri.StatusName,
			StatusCategory:   ri.StatusCategory,
			AssigneeKey:      ri.AssigneeKey,
			Labels:           ri.Labels,
			Fields:           map[string]string{},
			Updated:          ri.Updated,
		}
		for k, v := range ri.Fields {
			oi.Fields[k] = string(v)
		}
		issues = append(issues, oi)
	}
	return issues, truncated, nil
}
