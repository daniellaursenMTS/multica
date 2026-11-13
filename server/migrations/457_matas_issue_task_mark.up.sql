-- Tasks under Inbox (Matas fork, MATA-229): a member's own mark on an issue.
-- 'task' puts the issue on the member's task list by hand; 'dismissed' hides an
-- issue that the automatic task rules picked up, until a newer inbox
-- notification about it arrives. updated_at is when the state was last set.
CREATE TABLE IF NOT EXISTS issue_task_mark (
    workspace_id UUID NOT NULL,
    user_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('task', 'dismissed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
