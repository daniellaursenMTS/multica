package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const incomingMCPVersion = "2025-11-25"

func supportedIncomingMCPVersion(v string) bool {
	return v == incomingMCPVersion || v == "2025-06-18" || v == "2025-03-26"
}

// IncomingMCP is a stateless Streamable HTTP server. It deliberately exposes
// only issue submission and receipt lookup. Authentication and workspace
// membership are provided by the same middleware as the REST issue API.
func (h *Handler) IncomingMCP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer mul_") || isMachineCredentialActor(r) {
		writeError(w, http.StatusUnauthorized, "a personal access token is required")
		return
	}
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && (h.cfg.AppURL == "" || origin != strings.TrimRight(h.cfg.AppURL, "/")) && !allowedMCPExtensionOrigin(origin, os.Getenv("MCP_ALLOWED_EXTENSION_IDS")) {
		writeError(w, http.StatusForbidden, "origin is not allowed")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 415, "application/json is required")
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !supportedIncomingMCPVersion(v) {
		writeError(w, 400, "unsupported MCP protocol version")
		return
	}
	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
	if err := decoder.Decode(&msg); err != nil {
		mcpRPCError(w, nil, -32700, "invalid JSON request")
		return
	}
	if decoder.Decode(new(any)) != io.EOF || msg.JSONRPC != "2.0" || msg.Method == "" {
		mcpRPCError(w, nil, -32600, "invalid JSON-RPC request")
		return
	}
	if len(msg.ID) == 0 {
		// A notification must never execute a tool or return a JSON-RPC response.
		if strings.HasPrefix(msg.Method, "notifications/") {
			w.WriteHeader(http.StatusAccepted)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
		return
	}
	var id any
	if json.Unmarshal(msg.ID, &id) != nil || id == nil {
		mcpRPCError(w, nil, -32600, "invalid request id")
		return
	}
	switch id.(type) {
	case string, float64:
	default:
		mcpRPCError(w, nil, -32600, "invalid request id")
		return
	}
	switch msg.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(msg.Params, &p) != nil || p.ProtocolVersion == "" {
			mcpRPCError(w, msg.ID, -32602, "protocolVersion is required")
			return
		}
		version := p.ProtocolVersion
		if !supportedIncomingMCPVersion(version) {
			version = incomingMCPVersion
		}
		mcpRPCResult(w, msg.ID, map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "matas-multica", "version": "1.1.0"}, "instructions": "Matas' Multica workspace: projects and their diamonds of leaders, what project owners share, updates to a project (screened by the GDPR gate), and tasks for agents. Questions and tasks are answered asynchronously: poll multica_get_issue. Submitting is not approval; a person decides when work is done."})
	case "ping":
		mcpRPCResult(w, msg.ID, map[string]any{})
	case "tools/list":
		mcpRPCResult(w, msg.ID, map[string]any{"tools": append(incomingMCPTools(), matasMCPTools()...)})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			mcpRPCError(w, msg.ID, -32602, "invalid tool call")
			return
		}
		var result any
		var err error
		switch p.Name {
		case "multica_submit_issue":
			result, err = h.mcpSubmitIssue(r, p.Arguments)
		case "multica_get_issue":
			result, err = h.mcpGetIssue(r, p.Arguments)
		case "multica_list_projects":
			result, err = h.mcpListProjects(r, p.Arguments)
		case "multica_list_agents":
			result, err = h.mcpListAgents(r, p.Arguments)
		case "multica_project_summary":
			result, err = h.mcpProjectSummary(r, p.Arguments)
		case "multica_ask_project":
			result, err = h.mcpAskProject(r, p.Arguments)
		case "multica_add_project_update":
			result, err = h.mcpAddProjectUpdate(r, p.Arguments)
		case "multica_create_task":
			result, err = h.mcpCreateTask(r, p.Arguments)
		default:
			mcpRPCError(w, msg.ID, -32602, "unknown tool")
			return
		}
		if err != nil {
			mcpRPCResult(w, msg.ID, map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": err.Error()}}})
			return
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			mcpRPCError(w, msg.ID, -32603, "could not encode receipt")
			return
		}
		mcpRPCResult(w, msg.ID, map[string]any{"content": []any{map[string]string{"type": "text", "text": string(encoded)}}, "structuredContent": result})
	default:
		mcpRPCError(w, msg.ID, -32601, "method not found")
	}
}

func mcpRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
func mcpRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}
func incomingMCPTools() []any {
	field := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	return []any{
		map[string]any{"name": "multica_submit_issue", "description": "Store selected text in Multica and start the Orchestrator. Reuse request_id for retries. The Orchestrator routes privacy matters to GDPR Compliance Gate.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"request_id", "title", "text", "orchestrator_id"}, "properties": map[string]any{"request_id": field("Stable UUID for this submission; changing content requires a new UUID."), "title": field("Issue title, 1–200 characters."), "text": field("Selected text, at most 64 KiB; treated as untrusted issue content."), "orchestrator_id": field("UUID of the workspace agent named Orchestrator.")}}, "annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}},
		map[string]any{"name": "multica_get_issue", "description": "Get an issue receipt and routing status without returning transcript content.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id"}, "properties": map[string]any{"id": field("Issue UUID from the submission receipt.")}}, "annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false}},
	}
}
func decodeMCPArguments(raw json.RawMessage, out any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("arguments must be an object")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("invalid tool arguments")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid tool arguments")
	}
	return nil
}
func (h *Handler) mcpSubmitIssue(r *http.Request, raw json.RawMessage) (any, error) {
	var p struct {
		RequestID      string `json:"request_id"`
		Title          string `json:"title"`
		Text           string `json:"text"`
		OrchestratorID string `json:"orchestrator_id"`
	}
	if err := decodeMCPArguments(raw, &p); err != nil {
		return nil, err
	}
	requestID, err := util.ParseUUID(p.RequestID)
	if err != nil {
		return nil, errors.New("request_id must be a UUID")
	}
	agentID, err := util.ParseUUID(p.OrchestratorID)
	if err != nil {
		return nil, errors.New("orchestrator_id must be a UUID")
	}
	workspace := h.resolveWorkspaceID(r)
	ws, err := util.ParseUUID(workspace)
	if err != nil {
		return nil, errors.New("workspace_id is required")
	}
	creator, err := util.ParseUUID(requestUserID(r))
	if err != nil {
		return nil, errors.New("authenticated member is required")
	}
	if len([]rune(strings.TrimSpace(p.Title))) == 0 || len([]rune(p.Title)) > 200 || strings.TrimSpace(p.Text) == "" || len(p.Text) > 64*1024 {
		return nil, errors.New("title must be 1–200 characters and text 1–65536 bytes")
	}
	if status, msg := h.validateAssigneePair(r.Context(), r, workspace, pgtype.Text{String: "agent", Valid: true}, agentID); status != 0 {
		return nil, errors.New(msg)
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: ws})
	if err != nil || agent.Name != "Orchestrator" || !agent.RuntimeID.Valid {
		return nil, errors.New("select the workspace Orchestrator with a connected runtime")
	}
	canonical, err := json.Marshal([]string{p.Title, p.Text, util.UUIDToString(agentID)})
	if err != nil {
		return nil, errors.New("invalid submission")
	}
	digest := sha256.Sum256(canonical)
	metadata := map[string]json.RawMessage{"intake_source": json.RawMessage(`"opus_mcp"`), "gdpr_review_only": json.RawMessage(`true`)}
	metadata["intake_request_id"], err = json.Marshal(util.UUIDToString(requestID))
	if err != nil {
		return nil, err
	}
	result, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID: ws, Title: p.Title, Description: pgtype.Text{String: p.Text, Valid: true}, Status: "todo", Priority: "medium",
		AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: agentID, CreatorType: "member", CreatorID: creator, AllowDuplicate: true,
		Submission: &service.IssueSubmission{RequestID: requestID, PayloadHash: hex.EncodeToString(digest[:]), Metadata: metadata},
	}, service.IssueCreateOpts{Platform: "mcp", AssignedAgentRunFireAt: time.Now(),
		BroadcastPayload: func(issue db.Issue, atts []db.Attachment, labels []db.IssueLabel) map[string]any {
			payload := issueToResponse(issue, h.getIssuePrefix(r.Context(), issue.WorkspaceID))
			h.fillStatusCategory(r.Context(), issue.WorkspaceID, &payload)
			payload.Attachments = []AttachmentResponse{}
			labelResponses := labelsToResponse(labels)
			payload.Labels = &labelResponses
			return map[string]any{"issue": payload}
		},
	})
	if errors.Is(err, service.ErrSubmissionConflict) {
		return nil, err
	}
	if err != nil {
		return nil, errors.New("Multica could not accept this issue; retry with the same request_id")
	}
	return h.mcpIssueReceipt(r, result.Issue), nil
}
func (h *Handler) mcpGetIssue(r *http.Request, raw json.RawMessage) (any, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeMCPArguments(raw, &p); err != nil {
		return nil, err
	}
	id, err := util.ParseUUID(p.ID)
	if err != nil {
		return nil, errors.New("id must be a UUID")
	}
	ws, err := util.ParseUUID(h.resolveWorkspaceID(r))
	if err != nil {
		return nil, errors.New("workspace_id is required")
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return nil, errors.New("issue not found in this workspace")
	}
	return h.mcpIssueReceipt(r, issue), nil
}
func (h *Handler) mcpIssueReceipt(r *http.Request, issue db.Issue) map[string]any {
	metadata := map[string]json.RawMessage{}
	if json.Unmarshal(issue.Metadata, &metadata) != nil {
		metadata = map[string]json.RawMessage{}
	}
	safe := map[string]json.RawMessage{}
	for _, key := range []string{"intake_source", "intake_request_id", "intake_route", "intake_route_reason", "gdpr_check", "mcp_kind", "hub_project"} {
		if v, ok := metadata[key]; ok {
			safe[key] = v
		}
	}
	assignee := ""
	if issue.AssigneeType.String == "agent" && issue.AssigneeID.Valid {
		if agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: issue.AssigneeID, WorkspaceID: issue.WorkspaceID}); err == nil {
			assignee = agent.Name
		}
	}
	receipt := map[string]any{"id": util.UUIDToString(issue.ID), "identifier": fmt.Sprintf("%s-%d", h.getIssuePrefix(r.Context(), issue.WorkspaceID), issue.Number), "status": issue.Status, "assignee_id": util.UUIDToString(issue.AssigneeID), "assignee": assignee, "metadata": safe, "updated_at": issue.UpdatedAt.Time}
	if answer := h.matasAnswer(r, issue); answer != "" {
		receipt["answer"] = answer
	}
	return receipt
}

// Extension callers still require a personal token and workspace membership.
// Only explicitly configured extension IDs may bypass the web-origin check.
func allowedMCPExtensionOrigin(origin, allowed string) bool {
	const prefix = "chrome-extension://"
	if !strings.HasPrefix(origin, prefix) {
		return false
	}
	id := strings.TrimPrefix(origin, prefix)
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if c < 'a' || c > 'p' {
			return false
		}
	}
	for _, candidate := range strings.Split(allowed, ",") {
		if id == strings.TrimSpace(candidate) {
			return true
		}
	}
	return false
}
