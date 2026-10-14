package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func incomingRequest(body any) *http.Request {
	return testutil.WithHeaders(testutil.JSONRequest("POST", "/api/mcp", body), "Authorization", "Bearer mul_test", "X-User-ID", testUserID, "X-Workspace-ID", testWorkspaceID)
}
func incomingCall(t *testing.T, name string, args any) map[string]any {
	t.Helper()
	req := incomingRequest(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	var response struct {
		Result map[string]any `json:"result"`
	}
	testutil.Call(t, testHandler.IncomingMCP, req).Want(200).JSON(&response)
	return response.Result
}
func TestIncomingMCPTransport(t *testing.T) {
	for _, tc := range []struct {
		name, method, body, header, value string
		status                            int
	}{
		{"init", "POST", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`, "", "", 200},
		{"notification", "POST", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "", "", 202},
		{"get", "GET", "", "", "", 405},
		{"cookie-only", "POST", `{}`, "Authorization", "", 401},
		{"machine", "POST", `{}`, "X-Actor-Source", "task_token", 401},
		{"bad-origin", "POST", `{}`, "Origin", "https://evil.example", 403},
		{"version", "POST", `{}`, "MCP-Protocol-Version", "unknown", 400},
		{"no-tool-notification", "POST", `{"jsonrpc":"2.0","method":"tools/call"}`, "", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := incomingRequest(tc.body)
			req.Method = tc.method
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			testutil.Call(t, testHandler.IncomingMCP, req).Want(tc.status)
		})
	}
	for _, body := range []string{`[]`, `{}`, `{"jsonrpc":"2.0","id":{},"method":"ping"}`, `{"jsonrpc":"2.0","id":1,"method":"ping"} {}`, `!`} {
		var response struct {
			Error any `json:"error"`
		}
		testutil.Call(t, testHandler.IncomingMCP, incomingRequest(body)).Want(200).JSON(&response)
		if response.Error == nil {
			t.Fatalf("accepted malformed request: %s", body)
		}
	}
}
func TestIncomingMCPSubmissionRetryAndRouting(t *testing.T) {
	runtimeID := dbfx.Runtime(t, "MCP intake fixture")
	agentID := dbfx.Agent(t, "Orchestrator", runtimeID)
	args := map[string]any{"request_id": uuid.NewString(), "title": "Synthetic MCP intake", "text": "Synthetic customer processing proposal.", "orchestrator_id": agentID}
	// Concurrent retries must commit one issue and exactly one task.
	results := make([]map[string]any, 4)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = incomingCall(t, "multica_submit_issue", args) }(i)
	}
	wg.Wait()
	var id string
	for _, result := range results {
		if result["isError"] == true {
			t.Fatalf("submission failed: %v", result)
		}
		receipt := result["structuredContent"].(map[string]any)
		if id == "" {
			id = receipt["id"].(string)
		} else if receipt["id"] != id {
			t.Fatal("retry created another issue")
		}
		if receipt["assignee_id"] != agentID {
			t.Fatal("did not assign Orchestrator")
		}
		metadata := receipt["metadata"].(map[string]any)
		if metadata["intake_source"] != "opus_mcp" {
			t.Fatal("missing intake metadata")
		}
	}
	// Issue created by the service needs explicit fixture cleanup registration.
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), "DELETE FROM agent_task_queue WHERE issue_id=$1", id)
		if err != nil {
			t.Error(err)
		}
		_, err = testPool.Exec(context.Background(), "DELETE FROM issue WHERE id=$1", id)
		if err != nil {
			t.Error(err)
		}
	})
	var count int
	var metadata []byte
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("queued %d tasks, want 1", count)
	}
	if err := testPool.QueryRow(context.Background(), "SELECT metadata FROM issue WHERE id=$1", id).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(metadata, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["gdpr_review_only"] != true {
		t.Fatal("missing review-only guard")
	}
	args["text"] = "Changed payload"
	if incomingCall(t, "multica_submit_issue", args)["isError"] != true {
		t.Fatal("reused key with changed content")
	}
	got := incomingCall(t, "multica_get_issue", map[string]any{"id": id})
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "Synthetic customer") {
		t.Fatal("receipt leaked transcript")
	}
	foreign := dbfx.Workspace(t, "MCP foreign", "mcp-foreign-"+uuid.NewString())
	req := incomingRequest(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "multica_get_issue", "arguments": map[string]any{"id": id}}})
	req.Header.Set("X-Workspace-ID", foreign)
	var denied struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	testutil.Call(t, testHandler.IncomingMCP, req).Want(200).JSON(&denied)
	if !denied.Result.IsError {
		t.Fatal("cross-workspace receipt leaked")
	}
}
func TestIncomingMCPRejectsInvalidArguments(t *testing.T) {
	agent := dbfx.Agent(t, "GDPR Compliance Gate", dbfx.Runtime(t, "Wrong target"))
	valid := map[string]any{"request_id": uuid.NewString(), "title": "Synthetic", "text": "hello", "orchestrator_id": agent}
	if incomingCall(t, "multica_submit_issue", valid)["isError"] != true {
		t.Fatal("bypassed Orchestrator")
	}
	for _, args := range []any{nil, map[string]any{"request_id": "bad"}, map[string]any{"id": uuid.NewString(), "extra": true}} {
		if incomingCall(t, "multica_get_issue", args)["isError"] != true {
			t.Fatal("accepted invalid input")
		}
	}
}

func TestMCPExtensionOriginAllowlist(t *testing.T) {
	id := "abcdefghijklmnopabcdefghijklmnop"
	for _, tc := range []struct {
		origin, allow string
		want          bool
	}{
		{"chrome-extension://" + id, id, true},
		{"chrome-extension://" + id, "", false},
		{"chrome-extension://" + id + "/", id, false},
		{"https://" + id, id, false},
		{"chrome-extension://" + id + ".evil", id, false},
		{"chrome-extension://" + id, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
	} {
		if got := allowedMCPExtensionOrigin(tc.origin, tc.allow); got != tc.want {
			t.Fatalf("origin %q: got %v", tc.origin, got)
		}
	}
}
