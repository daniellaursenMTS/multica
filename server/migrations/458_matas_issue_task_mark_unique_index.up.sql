CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_task_mark_user_issue ON issue_task_mark (workspace_id, user_id, issue_id);
