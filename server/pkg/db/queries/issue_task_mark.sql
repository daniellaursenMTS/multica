-- Tasks under Inbox (Matas fork, MATA-229).
--
-- A member's task list holds the issues they have to act on. An issue is a
-- task when the member marked it by hand (issue_task_mark.state = 'task'), or
-- when one of the automatic rules below holds and the member has not dismissed
-- it since the last inbox notification about it:
--
--   assigned  — assigned to the member and open (todo, in progress, in review
--               or blocked);
--   awaiting  — created by the member, assigned to an agent, and waiting on a
--               person (in review or blocked);
--   mentioned — the member has an unarchived inbox mention on an issue that is
--               not closed.
--
-- Status rules read the status category, so a custom status behaves like the
-- built-in it belongs to.

-- name: ListMemberTasks :many
WITH candidate AS (
    SELECT m.issue_id FROM issue_task_mark m
    WHERE m.workspace_id = @workspace_id AND m.user_id = @user_id AND m.state = 'task'
    UNION
    SELECT i.id FROM issue i
    WHERE i.workspace_id = @workspace_id AND i.assignee_type = 'member' AND i.assignee_id = @user_id
    UNION
    SELECT i.id FROM issue i
    WHERE i.workspace_id = @workspace_id AND i.creator_type = 'member' AND i.creator_id = @user_id
      AND i.assignee_type = 'agent'
    UNION
    SELECT n.issue_id FROM inbox_item n
    WHERE n.workspace_id = @workspace_id AND n.recipient_type = 'member' AND n.recipient_id = @user_id
      AND n.type = 'mentioned' AND n.archived = FALSE AND n.issue_id IS NOT NULL
),
classified AS (
    SELECT
        i.id,
        COALESCE(m.state = 'task', FALSE)::bool AS marked,
        -- assignee_type is NULL on unassigned issues; COALESCE keeps the
        -- flags strictly boolean.
        COALESCE(i.assignee_type = 'member' AND i.assignee_id = @user_id
            AND COALESCE(s.category, i.status) IN ('todo', 'in_progress', 'in_review', 'blocked'), FALSE)::bool AS assigned,
        COALESCE(i.creator_type = 'member' AND i.creator_id = @user_id AND i.assignee_type = 'agent'
            AND COALESCE(s.category, i.status) IN ('in_review', 'blocked'), FALSE)::bool AS awaiting,
        (COALESCE(s.category, i.status) NOT IN ('done', 'cancelled')
            AND EXISTS (
                SELECT 1 FROM inbox_item n
                WHERE n.workspace_id = i.workspace_id AND n.recipient_type = 'member'
                  AND n.recipient_id = @user_id AND n.issue_id = i.id
                  AND n.type = 'mentioned' AND n.archived = FALSE
            ))::bool AS mentioned,
        COALESCE(m.state = 'dismissed' AND NOT EXISTS (
            SELECT 1 FROM inbox_item n
            WHERE n.workspace_id = i.workspace_id AND n.recipient_type = 'member'
              AND n.recipient_id = @user_id AND n.issue_id = i.id
              AND n.created_at > m.updated_at
        ), FALSE)::bool AS dismissed,
        COALESCE(s.category, i.status)::text AS status_category
    FROM candidate c
    JOIN issue i ON i.id = c.issue_id AND i.workspace_id = @workspace_id
    LEFT JOIN issue_status s ON s.workspace_id = i.workspace_id AND s.key = i.status
    LEFT JOIN issue_task_mark m ON m.workspace_id = i.workspace_id AND m.user_id = @user_id AND m.issue_id = i.id
)
SELECT sqlc.embed(i), c.marked, c.assigned, c.awaiting, c.mentioned, c.status_category
FROM classified c
JOIN issue i ON i.id = c.id
WHERE c.marked OR ((c.assigned OR c.awaiting OR c.mentioned) AND NOT c.dismissed)
ORDER BY COALESCE(i.last_activity_at, i.updated_at) DESC, i.id DESC
LIMIT 500;

-- name: UpsertIssueTaskMark :one
INSERT INTO issue_task_mark (workspace_id, user_id, issue_id, state)
VALUES (@workspace_id, @user_id, @issue_id, @state)
ON CONFLICT (workspace_id, user_id, issue_id)
DO UPDATE SET state = EXCLUDED.state, updated_at = now()
RETURNING *;

-- name: DeleteIssueTaskMarksByIssue :exec
-- Issue delete cleanup: issue_task_mark has no foreign key to issue.
DELETE FROM issue_task_mark
WHERE workspace_id = @workspace_id AND issue_id = @issue_id;
