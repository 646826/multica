-- Native Jira sync: per-comment identity map (AD-5/AD-15). Inbound rows are
-- written in the same transaction as the mirrored Multica comment; outbound
-- rows follow intent-first (pending row + marker before the Jira POST, then
-- finalized with the Jira comment id) so a crash can adopt instead of
-- re-posting. A comment present here is never re-mirrored (echo immunity).

CREATE TABLE jira_comment_link (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    comment_id UUID,
    jira_comment_id TEXT NOT NULL DEFAULT '',
    origin TEXT NOT NULL CHECK (origin IN ('inbound', 'outbound')),
    marker TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'ok' CHECK (state IN ('pending', 'ok')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
