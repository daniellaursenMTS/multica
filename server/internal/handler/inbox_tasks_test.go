package handler

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func listInboxTasksForTest(t *testing.T, workspaceID string) map[string][]string {
	t.Helper()
	var items []TaskItemResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInboxTasks),
		inboxRequest(http.MethodGet, "/api/inbox/tasks", workspaceID)).
		Want(http.StatusOK).
		JSON(&items)
	byIssue := make(map[string][]string, len(items))
	for _, item := range items {
		byIssue[item.Issue.ID] = item.Reasons
	}
	return byIssue
}

func setInboxTaskForTest(t *testing.T, workspaceID, issueID string, handler http.HandlerFunc, method string) {
	t.Helper()
	req := testutil.WithURLParams(inboxRequest(method, "/api/inbox/tasks/"+issueID, workspaceID), "issueId", issueID)
	testutil.Call(t, inboxWorkspaceHandler(handler), req).Want(http.StatusOK)
}

func TestListInboxTasksRulesAndMarks(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Inbox tasks", "inbox-tasks-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	t.Cleanup(func() {
		dbfx.Pool.Exec(context.Background(), `DELETE FROM issue_task_mark WHERE workspace_id = $1`, workspaceID)
	})
	otherUserID := uuid.NewString()
	agentID := uuid.NewString()

	issue := func(title string, cols testutil.Cols) string {
		cols["workspace_id"] = workspaceID
		return dbfx.Issue(t, title, cols)
	}
	assignedOpen := issue("assigned open", testutil.Cols{"status": "in_progress", "assignee_type": "member", "assignee_id": testUserID})
	assignedBacklog := issue("assigned backlog", testutil.Cols{"status": "backlog", "assignee_type": "member", "assignee_id": testUserID})
	assignedDone := issue("assigned done", testutil.Cols{"status": "done", "assignee_type": "member", "assignee_id": testUserID})
	agentReview := issue("agent review", testutil.Cols{"status": "in_review", "assignee_type": "agent", "assignee_id": agentID})
	agentBlocked := issue("agent blocked", testutil.Cols{"status": "blocked", "assignee_type": "agent", "assignee_id": agentID})
	agentWorking := issue("agent working", testutil.Cols{"status": "in_progress", "assignee_type": "agent", "assignee_id": agentID})
	othersReview := issue("someone else's review", testutil.Cols{"status": "in_review", "assignee_type": "agent", "assignee_id": agentID, "creator_id": otherUserID})
	mentioned := issue("mentioned", testutil.Cols{"status": "todo", "creator_id": otherUserID})
	mentionArchived := issue("mention archived", testutil.Cols{"status": "todo", "creator_id": otherUserID})
	manualDone := issue("manual on done", testutil.Cols{"status": "done", "creator_id": otherUserID})

	mention := func(issueID string, archived bool) {
		dbfx.Insert(t, "inbox_item", testutil.Cols{
			"workspace_id":   workspaceID,
			"recipient_type": "member",
			"recipient_id":   testUserID,
			"type":           "mentioned",
			"severity":       "info",
			"issue_id":       issueID,
			"title":          "mention",
			"archived":       archived,
		})
	}
	mention(mentioned, false)
	mention(mentionArchived, true)
	setInboxTaskForTest(t, workspaceID, manualDone, testHandler.MarkInboxTask, http.MethodPut)

	got := listInboxTasksForTest(t, workspaceID)
	want := map[string][]string{
		assignedOpen: {taskReasonAssigned},
		agentReview:  {taskReasonReview},
		agentBlocked: {taskReasonBlocked},
		mentioned:    {taskReasonMentioned},
		manualDone:   {taskReasonManual},
	}
	for id, reasons := range want {
		if !slices.Equal(got[id], reasons) {
			t.Errorf("issue %s reasons = %v, want %v", id, got[id], reasons)
		}
	}
	for _, id := range []string{assignedBacklog, assignedDone, agentWorking, othersReview, mentionArchived} {
		if reasons, ok := got[id]; ok {
			t.Errorf("issue %s listed with %v, want it off the task list", id, reasons)
		}
	}
	if len(got) != len(want) {
		t.Errorf("task count = %d, want %d: %v", len(got), len(want), got)
	}

	// Removing a task hides an automatic one until a newer notification
	// about the issue arrives, and takes a manual one off for good.
	setInboxTaskForTest(t, workspaceID, agentReview, testHandler.RemoveInboxTask, http.MethodDelete)
	setInboxTaskForTest(t, workspaceID, manualDone, testHandler.RemoveInboxTask, http.MethodDelete)
	got = listInboxTasksForTest(t, workspaceID)
	if _, ok := got[agentReview]; ok {
		t.Errorf("dismissed task still listed")
	}
	if _, ok := got[manualDone]; ok {
		t.Errorf("removed manual task still listed")
	}

	dbfx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id":   workspaceID,
		"recipient_type": "member",
		"recipient_id":   testUserID,
		"type":           "new_comment",
		"severity":       "info",
		"issue_id":       agentReview,
		"title":          "new comment",
		"created_at":     time.Now().Add(time.Minute),
	})
	got = listInboxTasksForTest(t, workspaceID)
	if !slices.Equal(got[agentReview], []string{taskReasonReview}) {
		t.Errorf("dismissed task after a new notification = %v, want back as review", got[agentReview])
	}
}
