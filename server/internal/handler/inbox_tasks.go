package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Tasks under Inbox (Matas fork, MATA-229). The task list is the issues the
// member has to act on; which issues qualify is decided in ListMemberTasks
// (queries/issue_task_mark.sql). The member can also put an issue on the list
// by hand, or take one off it.

// Task reasons, in the order the client shows them.
const (
	taskReasonBlocked   = "blocked"   // created by the member, an agent is blocked on it
	taskReasonReview    = "review"    // created by the member, an agent delivered it for review
	taskReasonAssigned  = "assigned"  // assigned to the member and open
	taskReasonMentioned = "mentioned" // unarchived inbox mention
	taskReasonManual    = "manual"    // marked by the member
)

type TaskItemResponse struct {
	Issue   IssueResponse `json:"issue"`
	Reasons []string      `json:"reasons"`
	Marked  bool          `json:"marked"`
}

type TaskMarkResponse struct {
	IssueID string `json:"issue_id"`
	State   string `json:"state"`
}

func taskReasons(row db.ListMemberTasksRow) []string {
	reasons := make([]string, 0, 3)
	if row.Awaiting {
		if row.StatusCategory == "blocked" {
			reasons = append(reasons, taskReasonBlocked)
		} else {
			reasons = append(reasons, taskReasonReview)
		}
	}
	if row.Assigned {
		reasons = append(reasons, taskReasonAssigned)
	}
	if row.Mentioned {
		reasons = append(reasons, taskReasonMentioned)
	}
	if row.Marked {
		reasons = append(reasons, taskReasonManual)
	}
	return reasons
}

func (h *Handler) ListInboxTasks(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace id")
	if !ok {
		return
	}

	rows, err := h.Queries.ListMemberTasks(r.Context(), db.ListMemberTasksParams{
		WorkspaceID: wsUUID,
		UserID:      parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}

	prefix := h.getIssuePrefix(r.Context(), wsUUID)
	fill := h.newStatusCategoryFiller(r.Context(), wsUUID)
	resp := make([]TaskItemResponse, len(rows))
	for i, row := range rows {
		issue := issueToResponse(row.Issue, prefix)
		fill(&issue)
		resp[i] = TaskItemResponse{
			Issue:   issue,
			Reasons: taskReasons(row),
			Marked:  row.Marked,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// MarkInboxTask puts an issue on the member's task list by hand.
func (h *Handler) MarkInboxTask(w http.ResponseWriter, r *http.Request) {
	h.setInboxTaskMark(w, r, "task")
}

// RemoveInboxTask takes an issue off the member's task list. The mark becomes
// 'dismissed' rather than being deleted, so an issue that the automatic rules
// still pick up stays off the list until a newer notification about it arrives.
func (h *Handler) RemoveInboxTask(w http.ResponseWriter, r *http.Request) {
	h.setInboxTaskMark(w, r, "dismissed")
}

func (h *Handler) setInboxTaskMark(w http.ResponseWriter, r *http.Request, state string) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "issueId"))
	if !ok {
		return
	}
	mark, err := h.Queries.UpsertIssueTaskMark(r.Context(), db.UpsertIssueTaskMarkParams{
		WorkspaceID: issue.WorkspaceID,
		UserID:      parseUUID(requestUserID(r)),
		IssueID:     issue.ID,
		State:       state,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update task")
		return
	}
	writeJSON(w, http.StatusOK, TaskMarkResponse{
		IssueID: uuidToString(mark.IssueID),
		State:   mark.State,
	})
}
