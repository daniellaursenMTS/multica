package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Matas tools on the incoming MCP endpoint: projects and their diamonds, shared project
// knowledge, updates to a project hub, and tasks for agents. Everything runs as the member whose
// token calls it, with the same workspace checks as the REST API.
//
// What a project shares follows its owners' choice (kh_share on the intake issue): an approved
// summary is returned as it stands; a whole shared hub is answered by the Knowledge Hub agent,
// which only reads what owners shared. Raw updates and GDPR material are never returned.

const (
	matasKnowledgeAgent    = "Knowledge Hub"
	matasOrchestratorAgent = "Orchestrator"
)

var matasDateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func matasMCPTools() []any {
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	obj := func(required []string, props map[string]any) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": props}
	}
	ro := map[string]any{"readOnlyHint": true, "openWorldHint": false}
	rw := map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	return []any{
		map[string]any{"name": "multica_list_projects", "description": "List Matas projects: status, priority, dates, portfolio fields (WOF score etc.), the diamond's four roles with who holds them and whether each has updated recently, and what the owners share with Knowledge Hub.", "inputSchema": obj([]string{}, map[string]any{}), "annotations": ro},
		map[string]any{"name": "multica_list_agents", "description": "List the agents a task can be given to, with what each does.", "inputSchema": obj([]string{}, map[string]any{}), "annotations": ro},
		map[string]any{"name": "multica_project_summary", "description": "Get what a project's owners chose to share: their approved summary, or a note that the whole project hub is shared (then use multica_ask_project) or that nothing is shared.", "inputSchema": obj([]string{"project_id"}, map[string]any{"project_id": str("Project UUID from multica_list_projects.")}), "annotations": ro},
		map[string]any{"name": "multica_ask_project", "description": "Ask the Knowledge Hub agent a question. It answers from Matas' own sources and from projects only as far as their owners share them. Asynchronous: poll multica_get_issue with the returned id; the answer appears there as `answer` within a few minutes.", "inputSchema": obj([]string{"request_id", "question"}, map[string]any{"request_id": str("Stable UUID for this question; reuse it to retry."), "question": str("The question, 1–4000 characters."), "project_id": str("Optional project UUID the question is about.")}), "annotations": rw},
		map[string]any{"name": "multica_add_project_update", "description": "Send an update about a project (a status, decision, risk) from someone in its diamond. It is screened by the GDPR gate before anything reaches the project hub, exactly like the project page's Add update.", "inputSchema": obj([]string{"request_id", "project_id", "speaker", "text"}, map[string]any{"request_id": str("Stable UUID for this update; reuse it to retry."), "project_id": str("Project UUID; the project must have a diamond."), "speaker": str("Who gave the update, as named in the project's diamond."), "text": str("The update, 1–20000 characters."), "date": str("Day the update was given, YYYY-MM-DD; default today.")}), "annotations": rw},
		map[string]any{"name": "multica_create_task", "description": "Create a task in a project and give it to an agent (see multica_list_agents). Poll multica_get_issue for status; the agent's latest reply appears as `answer`.", "inputSchema": obj([]string{"request_id", "project_id", "title", "agent"}, map[string]any{"request_id": str("Stable UUID for this task; reuse it to retry."), "project_id": str("Project UUID."), "title": str("Task title, 1–200 characters."), "description": str("What to do, the background and what done looks like."), "agent": str("Agent name or UUID."), "start": map[string]any{"type": "boolean", "description": "Start the agent now (default true). False parks the task in backlog."}}), "annotations": rw},
	}
}

// matasIntakes returns each project's intake issue metadata (pf_intake), keyed by project.
func (h *Handler) matasIntakes(r *http.Request, ws pgtype.UUID) (map[string]map[string]json.RawMessage, error) {
	rows, err := h.DB.Query(r.Context(), `SELECT project_id, metadata FROM issue WHERE workspace_id = $1 AND project_id IS NOT NULL AND metadata @> '{"pf_intake": true}'::jsonb ORDER BY created_at`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]json.RawMessage{}
	for rows.Next() {
		var pid pgtype.UUID
		var raw []byte
		if err := rows.Scan(&pid, &raw); err != nil {
			return nil, err
		}
		m := map[string]json.RawMessage{}
		_ = json.Unmarshal(raw, &m)
		out[util.UUIDToString(pid)] = m
	}
	return out, rows.Err()
}

func metaString(m map[string]json.RawMessage, key string) string {
	var s string
	if v, ok := m[key]; ok && json.Unmarshal(v, &s) == nil {
		return s
	}
	return ""
}

func (h *Handler) matasWorkspace(r *http.Request) (pgtype.UUID, pgtype.UUID, error) {
	ws, err := util.ParseUUID(h.resolveWorkspaceID(r))
	if err != nil {
		return ws, pgtype.UUID{}, errors.New("workspace_id is required")
	}
	member, err := util.ParseUUID(requestUserID(r))
	if err != nil {
		return ws, member, errors.New("authenticated member is required")
	}
	return ws, member, nil
}

func (h *Handler) matasProject(r *http.Request, ws pgtype.UUID, id string) (db.Project, error) {
	pid, err := util.ParseUUID(id)
	if err != nil {
		return db.Project{}, errors.New("project_id must be a UUID")
	}
	p, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: pid, WorkspaceID: ws})
	if err != nil {
		return db.Project{}, errors.New("project not found in this workspace")
	}
	return p, nil
}

func (h *Handler) mcpListProjects(r *http.Request, raw json.RawMessage) (any, error) {
	if err := decodeMCPArguments(raw, &struct{}{}); err != nil {
		return nil, err
	}
	ws, _, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	projects, err := h.Queries.ListProjects(r.Context(), db.ListProjectsParams{WorkspaceID: ws})
	if err != nil {
		return nil, errors.New("could not list projects")
	}
	intakes, err := h.matasIntakes(r, ws)
	if err != nil {
		return nil, errors.New("could not read project fields")
	}
	list := []any{}
	for _, p := range projects {
		id := util.UUIDToString(p.ID)
		item := map[string]any{"id": id, "title": p.Title, "status": p.Status, "priority": p.Priority, "description": p.Description.String}
		if p.DueDate.Valid {
			item["due_date"] = p.DueDate.Time.Format("2006-01-02")
		}
		if m, ok := intakes[id]; ok {
			fields := map[string]json.RawMessage{}
			for k, v := range m {
				if strings.HasPrefix(k, "pf_") && k != "pf_intake" {
					fields[strings.TrimPrefix(k, "pf_")] = v
				}
			}
			item["fields"] = fields
			var spec struct {
				Name     string                     `json:"name"`
				Archived bool                       `json:"archived"`
				Roles    map[string]json.RawMessage `json:"roles"`
			}
			if s := metaString(m, "dia_spec"); s != "" && json.Unmarshal([]byte(s), &spec) == nil && !spec.Archived {
				diamond := map[string]any{"name_in_updates": spec.Name, "roles": spec.Roles}
				if st := metaString(m, "dia_status"); st != "" {
					diamond["activity"] = json.RawMessage(st)
				}
				item["diamond"] = diamond
			}
			share := "none"
			var ks struct {
				Mode string `json:"mode"`
			}
			if s := metaString(m, "kh_share"); s != "" && json.Unmarshal([]byte(s), &ks) == nil && ks.Mode != "" {
				share = ks.Mode
			}
			item["shared_with_knowledge_hub"] = share
		}
		list = append(list, item)
	}
	return map[string]any{"projects": list}, nil
}

func (h *Handler) mcpListAgents(r *http.Request, raw json.RawMessage) (any, error) {
	if err := decodeMCPArguments(raw, &struct{}{}); err != nil {
		return nil, err
	}
	ws, _, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	agents, err := h.Queries.ListAgents(r.Context(), ws)
	if err != nil {
		return nil, errors.New("could not list agents")
	}
	list := []any{}
	for _, a := range agents {
		if a.ArchivedAt.Valid || !a.RuntimeID.Valid {
			continue
		}
		list = append(list, map[string]any{"id": util.UUIDToString(a.ID), "name": a.Name, "description": a.Description})
	}
	return map[string]any{"agents": list}, nil
}

func (h *Handler) mcpProjectSummary(r *http.Request, raw json.RawMessage) (any, error) {
	var p struct {
		ProjectID string `json:"project_id"`
	}
	if err := decodeMCPArguments(raw, &p); err != nil {
		return nil, err
	}
	ws, _, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	project, err := h.matasProject(r, ws, p.ProjectID)
	if err != nil {
		return nil, err
	}
	intakes, err := h.matasIntakes(r, ws)
	if err != nil {
		return nil, errors.New("could not read project fields")
	}
	m := intakes[util.UUIDToString(project.ID)]
	var ks struct {
		Mode string `json:"mode"`
		At   string `json:"at"`
	}
	raw2 := metaString(m, "kh_share")
	_ = json.Unmarshal([]byte(raw2), &ks)
	out := map[string]any{"project": project.Title, "shared": "none"}
	// Only a choice the sync has applied counts: a pending or refused one shares nothing yet.
	if raw2 == "" || raw2 != metaString(m, "kh_synced") {
		out["note"] = "The owners have not shared this project with Knowledge Hub."
		return out, nil
	}
	out["shared"] = ks.Mode
	switch ks.Mode {
	case "summary":
		out["summary"] = metaString(m, "kh_summary")
		out["approved_at"] = ks.At
	case "full":
		out["note"] = "The whole project hub is shared. Use multica_ask_project with this project_id for answers from it."
	default:
		out["note"] = "The owners have not shared this project with Knowledge Hub."
	}
	return out, nil
}

// matasCreate creates one issue idempotently under request_id, assigned to an agent.
func (h *Handler) matasCreate(r *http.Request, requestID string, title, text string, project pgtype.UUID, agent db.Agent, status string, metadata map[string]json.RawMessage) (any, error) {
	rid, err := util.ParseUUID(requestID)
	if err != nil {
		return nil, errors.New("request_id must be a UUID")
	}
	ws, creator, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	if st, msg := h.validateAssigneePair(r.Context(), r, h.resolveWorkspaceID(r), pgtype.Text{String: "agent", Valid: true}, agent.ID); st != 0 {
		return nil, errors.New(msg)
	}
	canonical, _ := json.Marshal([]string{title, text, util.UUIDToString(agent.ID), util.UUIDToString(project), status})
	digest := sha256.Sum256(canonical)
	if metadata == nil {
		metadata = map[string]json.RawMessage{}
	}
	metadata["mcp_request_id"], _ = json.Marshal(util.UUIDToString(rid))
	fireAt := time.Now()
	result, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID: ws, Title: title, Description: pgtype.Text{String: text, Valid: true}, Status: status, Priority: "medium",
		AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: agent.ID, CreatorType: "member", CreatorID: creator,
		ProjectID: project, AllowDuplicate: true,
		Submission: &service.IssueSubmission{RequestID: rid, PayloadHash: hex.EncodeToString(digest[:]), Metadata: metadata},
	}, service.IssueCreateOpts{Platform: "mcp", AssignedAgentRunFireAt: fireAt,
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
		return nil, errors.New("Multica could not accept this; retry with the same request_id")
	}
	return h.mcpIssueReceipt(r, result.Issue), nil
}

func (h *Handler) matasAgent(r *http.Request, ws pgtype.UUID, ref string) (db.Agent, error) {
	agents, err := h.Queries.ListAgents(r.Context(), ws)
	if err != nil {
		return db.Agent{}, errors.New("could not list agents")
	}
	for _, a := range agents {
		if a.ArchivedAt.Valid || !a.RuntimeID.Valid {
			continue
		}
		if util.UUIDToString(a.ID) == ref || strings.EqualFold(a.Name, strings.TrimSpace(ref)) {
			return a, nil
		}
	}
	return db.Agent{}, fmt.Errorf("no active agent named %q; see multica_list_agents", ref)
}

func (h *Handler) mcpAskProject(r *http.Request, raw json.RawMessage) (any, error) {
	var p struct {
		RequestID string `json:"request_id"`
		Question  string `json:"question"`
		ProjectID string `json:"project_id"`
	}
	if err := decodeMCPArguments(raw, &p); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(p.Question)
	if q == "" || len([]rune(q)) > 4000 {
		return nil, errors.New("question must be 1–4000 characters")
	}
	ws, _, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	var project pgtype.UUID
	text := q
	if p.ProjectID != "" {
		pr, err := h.matasProject(r, ws, p.ProjectID)
		if err != nil {
			return nil, err
		}
		project = pr.ID
		text = "Spørgsmål om projektet «" + pr.Title + "»:\n\n" + q
	}
	agent, err := h.matasAgent(r, ws, matasKnowledgeAgent)
	if err != nil {
		return nil, err
	}
	title := "Spørgsmål: " + truncateRunes(q, 120)
	return h.matasCreate(r, p.RequestID, title, text+"\n\nSvar i én kommentar. Brug kun projekter, som ejerne har delt.\n", project, agent, "todo",
		map[string]json.RawMessage{"mcp_kind": json.RawMessage(`"ask"`)})
}

func (h *Handler) mcpAddProjectUpdate(r *http.Request, raw json.RawMessage) (any, error) {
	var p struct {
		RequestID string `json:"request_id"`
		ProjectID string `json:"project_id"`
		Speaker   string `json:"speaker"`
		Text      string `json:"text"`
		Date      string `json:"date"`
	}
	if err := decodeMCPArguments(raw, &p); err != nil {
		return nil, err
	}
	speaker := strings.TrimSpace(p.Speaker)
	text := strings.TrimSpace(p.Text)
	if speaker == "" || len([]rune(speaker)) > 80 || strings.ContainsAny(speaker, "·\n") {
		return nil, errors.New("speaker must be a name of 1–80 characters")
	}
	if text == "" || len([]rune(text)) > 20000 {
		return nil, errors.New("text must be 1–20000 characters")
	}
	day := strings.TrimSpace(p.Date)
	if day == "" {
		day = time.Now().Format("2006-01-02")
	}
	if !matasDateRE.MatchString(day) {
		return nil, errors.New("date must be YYYY-MM-DD")
	}
	if d, err := time.Parse("2006-01-02", day); err != nil || d.After(time.Now().Add(24*time.Hour)) {
		return nil, errors.New("date must be a real day, not in the future")
	}
	ws, _, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	project, err := h.matasProject(r, ws, p.ProjectID)
	if err != nil {
		return nil, err
	}
	intakes, err := h.matasIntakes(r, ws)
	if err != nil {
		return nil, errors.New("could not read project fields")
	}
	var spec struct {
		Name     string `json:"name"`
		Archived bool   `json:"archived"`
	}
	if s := metaString(intakes[util.UUIDToString(project.ID)], "dia_spec"); s == "" || json.Unmarshal([]byte(s), &spec) != nil || spec.Archived || spec.Name == "" {
		return nil, errors.New("this project has no diamond, so updates have no hub to go to")
	}
	agent, err := h.matasAgent(r, ws, matasOrchestratorAgent)
	if err != nil {
		return nil, err
	}
	body := "Projekt: " + spec.Name + " · Taler: " + speaker + " · Dato: " + day + "\n\n" + text + "\n"
	meta := map[string]json.RawMessage{"intake_source": json.RawMessage(`"opus_mcp"`), "gdpr_review_only": json.RawMessage(`true`), "note_source": json.RawMessage(`"mcp"`)}
	meta["intake_request_id"], _ = json.Marshal(p.RequestID)
	return h.matasCreate(r, p.RequestID, "Opdatering: "+spec.Name+" — "+speaker+", "+day, body, project.ID, agent, "todo", meta)
}

func (h *Handler) mcpCreateTask(r *http.Request, raw json.RawMessage) (any, error) {
	var p struct {
		RequestID   string `json:"request_id"`
		ProjectID   string `json:"project_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Agent       string `json:"agent"`
		Start       *bool  `json:"start"`
	}
	if err := decodeMCPArguments(raw, &p); err != nil {
		return nil, err
	}
	title := strings.TrimSpace(p.Title)
	if title == "" || len([]rune(title)) > 200 || len(p.Description) > 64*1024 {
		return nil, errors.New("title must be 1–200 characters and description at most 64 KiB")
	}
	ws, _, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	project, err := h.matasProject(r, ws, p.ProjectID)
	if err != nil {
		return nil, err
	}
	agent, err := h.matasAgent(r, ws, p.Agent)
	if err != nil {
		return nil, err
	}
	if agent.Name == matasOrchestratorAgent {
		return nil, errors.New("give tasks to a working agent; project updates go through multica_add_project_update")
	}
	status := "todo"
	if p.Start != nil && !*p.Start {
		status = "backlog"
	}
	return h.matasCreate(r, p.RequestID, title, p.Description, project.ID, agent, status, map[string]json.RawMessage{"mcp_kind": json.RawMessage(`"task"`)})
}

// matasAnswer is the newest agent comment on an MCP question or task, for the receipt.
func (h *Handler) matasAnswer(r *http.Request, issue db.Issue) string {
	metadata := map[string]json.RawMessage{}
	_ = json.Unmarshal(issue.Metadata, &metadata)
	kind := metaString(metadata, "mcp_kind")
	if kind != "ask" && kind != "task" {
		return ""
	}
	comments, err := h.Queries.ListCommentsForIssue(r.Context(), db.ListCommentsForIssueParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Limit: 20})
	if err != nil {
		return ""
	}
	for i := len(comments) - 1; i >= 0; i-- {
		if comments[i].AuthorType == "agent" {
			return comments[i].Content
		}
	}
	return ""
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}
