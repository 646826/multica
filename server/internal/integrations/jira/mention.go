package jira

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// mention.go — the mention bridge (FR-29). Inbound: plain-text @AgentName in a
// Jira comment (typed by a human) is rewritten to a native Multica mention
// link so the standard comment-trigger machinery wakes the agent. Genuine
// Jira user-mention ADF nodes are already rendered as plain names by the ADF
// reader (no leading @), so a human teammate named like an agent can never be
// converted. Ambiguous or unknown @names stay plain text.

// plainMentionRe matches word-bounded @token candidates in mirrored text.
var plainMentionRe = regexp.MustCompile(`(^|[^\w@])@([A-Za-z0-9_-]{1,64})\b`)

// bridgeMentions rewrites matching @AgentName tokens into native mention links
// and returns the rewritten content plus the set of agent ids to wake. When
// the per-connection toggle is off it returns the content unchanged and no
// wakes.
func (w *Worker) bridgeMentions(ctx context.Context, conn db.JiraConnection, content string) (string, []pgtype.UUID, error) {
	if !conn.MentionBridgeEnabled {
		return content, nil, nil
	}
	agents, err := w.Q.ListAgents(ctx, conn.WorkspaceID)
	if err != nil {
		return content, nil, err
	}
	// Case-insensitive exact name → agent; ambiguous names (same name twice)
	// are excluded so an ambiguous @name is never auto-resolved.
	byName := map[string]db.Agent{}
	ambiguous := map[string]bool{}
	for _, a := range agents {
		key := strings.ToLower(a.Name)
		if _, seen := byName[key]; seen {
			ambiguous[key] = true
			continue
		}
		byName[key] = a
	}

	woken := map[pgtype.UUID]bool{}
	rewritten := plainMentionRe.ReplaceAllStringFunc(content, func(match string) string {
		m := plainMentionRe.FindStringSubmatch(match)
		prefix, name := m[1], m[2]
		key := strings.ToLower(name)
		agent, ok := byName[key]
		if !ok || ambiguous[key] {
			return match // unknown or ambiguous → leave as plain text
		}
		woken[agent.ID] = true
		return prefix + "[@" + agent.Name + "](mention://agent/" + uuidStr(agent.ID) + ")"
	})

	var ids []pgtype.UUID
	for id := range woken {
		ids = append(ids, id)
	}
	return rewritten, ids, nil
}
