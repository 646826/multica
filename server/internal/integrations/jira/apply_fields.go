package jira

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// apply_fields.go — mapped custom-field value sync (FR-24). Values only:
// definitions are never created (FR-25). Inbound writes through the atomic
// single-key property setter; outbound folds into the coalesced field PUT.
// Unmappable values skip that field with a journal entry, never blocking the
// rest of the issue.

// applyInField writes a mapped Jira field value onto the Multica property
// (single-key atomic). external_field/property_id ride in act.Detail.
func (w *Worker) applyInField(ctx context.Context, conn db.JiraConnection, issueID pgtype.UUID, items *ItemsV1, act Action, cycleID pgtype.UUID) error {
	external, _ := act.Detail["external_field"].(string)
	propertyID, _ := act.Detail["property_id"].(string)
	if external == "" || propertyID == "" {
		return nil
	}
	propUUID, perr := parsePropertyUUID(propertyID)
	if perr != nil {
		return nil
	}
	// The property definition must exist (values only, never created).
	prop, gerr := w.Q.GetIssueProperty(ctx, db.GetIssuePropertyParams{ID: propUUID, WorkspaceID: conn.WorkspaceID})
	if pgx.ErrNoRows == gerr {
		_ = w.Journal.Record(ctx, conn, cycleID, JournalFieldSkipped, issueID, "", map[string]any{
			"property_id": propertyID, "reason": "property definition not found",
		})
		return nil
	} else if gerr != nil {
		return gerr
	}
	value, ok := JiraRawToProperty(json.RawMessage(act.Value), PropertyType(prop.Type))
	if !ok {
		_ = w.Journal.Record(ctx, conn, cycleID, JournalFieldSkipped, issueID, "", map[string]any{
			"external_field": external, "property_id": propertyID, "reason": "value not mappable to " + prop.Type,
		})
		return nil
	}
	if _, err := w.Q.SetIssuePropertyValue(ctx, db.SetIssuePropertyValueParams{
		ID: issueID, WorkspaceID: conn.WorkspaceID, Key: propertyID, Value: value,
	}); err != nil {
		return err
	}
	items.Fields[external] = ItemState{RemoteSHA: SHA(act.Value), LocalSHA: SHA(canonicalPropertyValue(value)), BreadcrumbFor: items.Fields[external].BreadcrumbFor}
	return nil
}

// canonicalPropertyValue normalizes a property JSON value for snapshot hashing.
func canonicalPropertyValue(v json.RawMessage) string {
	var decoded any
	if json.Unmarshal(v, &decoded) != nil {
		return string(v)
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		return string(v)
	}
	return string(out)
}

func parsePropertyUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	err := u.Scan(s)
	return u, err
}

// jiraFieldType resolves a Jira field's schema type from the live catalog.
// Unknown → "string" (safest text coercion). A per-connection cache is a
// future optimization; field writes are low-volume in v1.
func (w *Worker) jiraFieldType(ctx context.Context, conn db.JiraConnection, fieldID string) string {
	client, err := w.Svc.ClientFor(conn)
	if err != nil {
		return "string"
	}
	fields, err := client.ListFields(ctx)
	if err != nil {
		return "string"
	}
	for _, f := range fields {
		if f.ID == fieldID {
			if f.Schema.Type != "" {
				return f.Schema.Type
			}
			return "string"
		}
	}
	return "string"
}
