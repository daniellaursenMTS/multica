package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Roles (MATA-286): the project lead, the reader (workspace role viewer) and admins who see every
// project's memory.

func TestMatasHolderForProjectLead(t *testing.T) {
	meta := func(rights map[string]string) map[string]json.RawMessage {
		spec, _ := json.Marshal(map[string]any{"roles": map[string]any{"po": map[string]any{"person": "Petra Holm", "email": "petra@matas.test"}},
			"project_lead": map[string]any{"person": "Lis Lund", "email": "lis@matas.test"}})
		ctx, _ := json.Marshal(map[string]any{"goals": []string{"x"}, "decision_rights": rights})
		ed, _ := json.Marshal(map[string][]string{"pl": {"LIS.L@matas.test"}})
		m := map[string]json.RawMessage{}
		for k, v := range map[string]string{"dia_spec": string(spec), "ctx_spec": string(ctx), "ctx_editors": string(ed)} {
			m[k], _ = json.Marshal(v)
		}
		return m
	}
	for _, tc := range []struct {
		rights map[string]string
		role   string
		emails string
	}{
		// Being project lead gives no decision right by itself.
		{nil, "po", "petra@matas.test"},
		// The context names the project lead, by name or by the title.
		{map[string]string{"product": "Lis"}, "pl", "lis.l@matas.test lis@matas.test"},
		{map[string]string{"product": "Projektlederen"}, "pl", "lis.l@matas.test lis@matas.test"},
	} {
		h := matasHolderFor("product", meta(tc.rights))
		if h.Role != tc.role || strings.Join(h.Emails, " ") != tc.emails || h.Reason != "" {
			t.Errorf("%v: role %q emails %v reason %q", tc.rights, h.Role, h.Emails, h.Reason)
		}
	}
}

func TestMatasOnlyStakeholders(t *testing.T) {
	base := `{"name":"X","roles":{"po":{"person":"Petra"}},"stakeholders":[{"person":"Sif","title":"Jura"}]}`
	for after, want := range map[string]bool{
		`{"roles":{"po":{"person":"Petra"}},"name":"X","stakeholders":[]}`:                 true,
		`{"name":"X","roles":{"po":{"person":"Petra"}}}`:                                   true,
		`{"name":"X","roles":{"po":{"person":"Otto"}},"stakeholders":[]}`:                  false,
		`{"name":"X","roles":{"po":{"person":"Petra"}},"project_lead":{"person":"Petra"}}`: false,
		`{"name":"X","roles":{"po":{"person":"Petra"}},"archived":true,"stakeholders":[]}`: false,
		`not json`: false,
	} {
		if got := matasOnlyStakeholders(base, after); got != want {
			t.Errorf("%s: %v", after, got)
		}
	}
}

func TestMatasProjectLeadAndViewer(t *testing.T) {
	tag := uuid.NewString()[:8]
	counter := func() int {
		var n int
		dbfx.QueryRow(t, `UPDATE workspace SET issue_counter = issue_counter + 1 WHERE id = $1 RETURNING issue_counter`, testWorkspaceID).Scan(&n)
		return n
	}
	person := func(name, role string) (string, string) {
		email := fmt.Sprintf("roles-%s-%s@matas.test", strings.ToLower(name), tag)
		id := dbfx.User(t, name, email)
		dbfx.Member(t, testWorkspaceID, id, role)
		return id, email
	}
	lead, leadEmail := person("Lis", "member") // the project lead
	po, poEmail := person("Petra", "member")   // product owner
	admin, _ := person("Alma", "admin")        // an admin without a role in the project
	outsider, _ := person("Otto", "member")    // a member without a role
	viewer, _ := person("Vera", "viewer")      // a reader
	spec := map[string]any{"slug": "roles-" + tag, "name": "Roller " + tag,
		"roles": map[string]any{"bo": map[string]any{"person": nil}, "po": map[string]any{"person": "Petra", "email": poEmail},
			"plo": map[string]any{"person": nil}, "to": map[string]any{"person": nil}},
		"stakeholders": []any{}, "project_lead": map[string]any{"person": "Lis", "email": leadEmail}}
	specJSON := func(change func(map[string]any)) string {
		c := map[string]any{}
		b, _ := json.Marshal(spec)
		_ = json.Unmarshal(b, &c)
		if change != nil {
			change(c)
		}
		out, _ := json.Marshal(c)
		value, _ := json.Marshal(string(out))
		return string(value)
	}
	ctxSpec, _ := json.Marshal(map[string]any{"goals": []string{"Mål"}, "decision_rights": map[string]string{"product": "Petra"},
		"sources": []any{map[string]any{"type": "repo", "name": "Kode", "url": "https://example.test/x"}}, "watch_for": []string{"Pas på"}})
	number := "RL" + tag + "-1"
	rawSpec, _ := json.Marshal(spec)
	meta, _ := json.Marshal(map[string]any{"pf_intake": true, "dia_spec": string(rawSpec), "ctx_spec": string(ctxSpec), "pf_number": number})
	project := dbfx.Project(t, "Roller "+tag)
	intake := dbfx.Issue(t, "Intake — Roller", testutil.Cols{"status": "done", "project_id": project, "number": counter(), "metadata": string(meta)})
	dbfx.Cleanup(t, `DELETE FROM issue WHERE project_id = $1`, project)

	set := func(user, key, value string) int {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParams(newRequestAs(user, "PUT", "/api/issues/"+intake+"/metadata/"+key, json.RawMessage(`{"value":`+value+`}`)), "id", intake, "key", key)
		testHandler.SetIssueMetadataKey(w, req)
		return w.Code
	}
	del := func(user, key string) int {
		w := httptest.NewRecorder()
		testHandler.DeleteIssueMetadataKey(w, withURLParams(newRequestAs(user, "DELETE", "/", nil), "id", intake, "key", key))
		return w.Code
	}
	withRole := func(role, who string) func(map[string]any) {
		return func(c map[string]any) { c["roles"].(map[string]any)[role] = map[string]any{"person": who} }
	}
	withStakeholder := func(c map[string]any) { c["stakeholders"] = []any{map[string]any{"person": "Sif", "title": "Jura"}} }
	withLead := func(who, email string) func(map[string]any) {
		return func(c map[string]any) { c["project_lead"] = map[string]any{"person": who, "email": email} }
	}

	// The project lead changes the diamond's roles and stakeholders; the product owner only the
	// stakeholders; only admins appoint, change or remove the project lead.
	for _, tc := range []struct {
		name   string
		user   string
		change func(map[string]any)
		want   int
	}{
		{"lead changes a role", lead, withRole("bo", "Bent"), 200},
		{"lead adds a stakeholder", lead, withStakeholder, 200},
		{"lead appoints another lead", lead, withLead("Otto", ""), 403},
		{"lead removes themselves", lead, func(c map[string]any) { delete(c, "project_lead") }, 403},
		{"po changes a role", po, withRole("to", "Tue"), 403},
		{"po removes a stakeholder", po, func(c map[string]any) { c["stakeholders"] = []any{} }, 200},
		{"outsider adds a stakeholder", outsider, withStakeholder, 403},
		{"viewer adds a stakeholder", viewer, withStakeholder, 403},
		{"admin changes the lead", admin, withLead("Lis", strings.ToUpper(leadEmail)), 200},
	} {
		if tc.want == 200 {
			// Each case starts from the stored diamond, so earlier cases do not mask later ones.
			dbfx.Exec(t, `UPDATE issue SET metadata = jsonb_set(metadata, '{dia_spec}', $2::jsonb) WHERE id = $1`, intake, specJSON(withStakeholder))
		}
		if got := set(tc.user, "dia_spec", specJSON(tc.change)); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
	dbfx.Exec(t, `UPDATE issue SET metadata = jsonb_set(metadata, '{dia_spec}', $2::jsonb) WHERE id = $1`, intake, specJSON(nil))
	if got := del(lead, "dia_spec"); got != 403 {
		t.Errorf("lead removed the diamond: %d", got)
	}
	// A new diamond that names a project lead is for admins; without one it stays open.
	freshProject := dbfx.Project(t, "Ny roller "+tag)
	fresh := dbfx.Issue(t, "Intake — ny", testutil.Cols{"status": "done", "project_id": freshProject, "number": counter(), "metadata": `{"pf_intake":true}`})
	dbfx.Cleanup(t, `DELETE FROM issue WHERE project_id = $1`, freshProject)
	setOn := func(user, issue, value string) int {
		w := httptest.NewRecorder()
		testHandler.SetIssueMetadataKey(w, withURLParams(newRequestAs(user, "PUT", "/", json.RawMessage(`{"value":`+value+`}`)), "id", issue, "key", "dia_spec"))
		return w.Code
	}
	if got := setOn(outsider, fresh, specJSON(nil)); got != 403 {
		t.Errorf("a member created a diamond with a project lead: %d", got)
	}
	if got := setOn(outsider, fresh, specJSON(func(c map[string]any) { delete(c, "project_lead") })); got != 200 {
		t.Errorf("a member could not create a diamond: %d", got)
	}

	// The project lead edits the context, decision rights included; the edit is logged with their e-mail.
	newCtx, _ := json.Marshal(`{"goals":["Ny"],"decision_rights":{"product":"Lis"}}`)
	if got := set(lead, "ctx_spec", string(newCtx)); got != 200 {
		t.Fatalf("lead edited the context: %d", got)
	}
	var updatedBy string
	dbfx.QueryRow(t, `SELECT (metadata->>'ctx_spec')::jsonb->>'updated_by' FROM issue WHERE id = $1`, intake).Scan(&updatedBy)
	if updatedBy != leadEmail {
		t.Fatalf("updated_by %q", updatedBy)
	}
	if got := set(viewer, "ctx_spec", string(newCtx)); got != 403 {
		t.Fatalf("viewer edited the context: %d", got)
	}
	dbfx.Exec(t, `UPDATE issue SET metadata = jsonb_set(metadata, '{ctx_spec}', to_jsonb($2::text)) WHERE id = $1`, intake, string(ctxSpec))

	// Memory: the lead and admins read it; a member and a viewer without a role get it withheld.
	dir := t.TempDir()
	t.Setenv(matasMemoryDirEnv, dir)
	posts := []matasMemoryPost{
		{ID: number + "/B-001", Type: "decision", Area: "product", Text: "Vi vælger leverandøren Nordlys.", SaidAt: "2026-11-20", Source: "MATA-1", Status: "proposal", Rev: 1},
		{ID: number + "/B-002", Type: "decision", Area: "product", Text: "Pilot i to butikker med leverandøren.", SaidAt: "2026-11-21", Source: "MATA-2", Status: "decision", Rev: 1},
	}
	view, _ := json.Marshal(matasMemoryView{Slug: "roles-" + tag, Number: number, Name: "Roller", MulticaProject: project, Posts: posts})
	if err := os.WriteFile(filepath.Join(dir, "roles.json"), view, 0o644); err != nil {
		t.Fatal(err)
	}
	call := func(user, tool string, args map[string]any) map[string]any {
		t.Helper()
		return incomingCallAs(t, user, tool, args)
	}
	content := func(result map[string]any) map[string]any {
		t.Helper()
		if result["isError"] == true {
			t.Fatalf("call failed: %v", result["content"])
		}
		return result["structuredContent"].(map[string]any)
	}
	for _, tc := range []struct {
		who    string
		user   string
		shared bool
	}{
		{"lead", lead, true}, {"admin without a role", admin, true}, {"owner", testUserID, true},
		{"member without a role", outsider, false}, {"viewer", viewer, false},
	} {
		log := content(call(tc.user, "multica_decision_log", map[string]any{"project_id": number}))
		if log["shared"] != tc.shared {
			t.Errorf("decision log for %s: shared %v", tc.who, log["shared"])
		}
		search := content(call(tc.user, "multica_search_memory", map[string]any{"project_id": number, "query": "leverandøren"}))
		posts, withheld := search["posts"].([]any), search["withheld"].([]any)
		if tc.shared != (len(posts) > 0 && len(withheld) == 0) {
			t.Errorf("search for %s: %d posts, %d withheld", tc.who, len(posts), len(withheld))
		}
		// The synergy check follows the same rule: content for those who read it, else withheld.
		synergy := content(call(tc.user, "multica_synergy_check", map[string]any{"idea": "Pilot med leverandøren i butikker", "project_id": number}))
		raw, _ := json.Marshal(synergy)
		if tc.shared != (strings.Contains(string(raw), "B-002") && len(synergy["withheld"].([]any)) == 0) {
			t.Errorf("synergy check for %s: %s", tc.who, raw)
		}
	}
	// Admins see everything but confirm only with their own right; the lead is not named for product.
	confirm := func(user string) int {
		w := httptest.NewRecorder()
		testHandler.ConfirmProjectDecision(w, withURLParams(newRequestAs(user, "POST", "/", map[string]any{"post": "B-001", "rev": 1, "action": "confirm"}), "id", project))
		return w.Code
	}
	if got := confirm(admin); got != 403 {
		t.Errorf("admin confirmed without the right: %d", got)
	}
	if got := confirm(lead); got != 403 {
		t.Errorf("lead confirmed without the right: %d", got)
	}

	// A viewer: only the read tools and Knowledge Hub; writing tools are refused in the dispatcher.
	for _, tool := range []string{"multica_add_project_update", "multica_create_task", "multica_propose_project_context", "multica_submit_issue", "multica_report_usage", "multica_list_agents"} {
		if result := call(viewer, tool, map[string]any{}); result["isError"] != true || !strings.Contains(fmt.Sprint(result["content"]), "readers") {
			t.Errorf("viewer called %s: %v", tool, result)
		}
	}
	listed := incomingListAs(t, viewer)
	if len(listed) != len(matasViewerTool) {
		t.Errorf("viewer sees %d tools: %v", len(listed), listed)
	}
	for _, name := range listed {
		if !matasViewerTool[name] {
			t.Errorf("viewer sees %s", name)
		}
	}
	// list_projects names people without their e-mails for a viewer.
	projects := content(call(viewer, "multica_list_projects", map[string]any{}))
	raw, _ := json.Marshal(projects)
	if strings.Contains(string(raw), poEmail) || strings.Contains(string(raw), leadEmail) || !strings.Contains(string(raw), `"project_lead":{"person":"Lis"}`) {
		t.Errorf("viewer's project list: %s", raw)
	}
	if raw, _ := json.Marshal(content(call(po, "multica_list_projects", map[string]any{}))); !strings.Contains(string(raw), leadEmail) {
		t.Errorf("member's project list has no project lead e-mail")
	}

	// project_context for a viewer follows what is shared: nothing, the summary, or the whole context.
	ctxFor := func(user string) map[string]any {
		return content(call(user, "multica_project_context", map[string]any{"project_id": number}))
	}
	hidden := func(got map[string]any) bool {
		raw, _ := json.Marshal(got)
		s := string(raw)
		return got["context"] == nil && !strings.Contains(s, "decision_rights") && !strings.Contains(s, "Pas på") && !strings.Contains(s, "example.test") && !strings.Contains(s, poEmail)
	}
	if got := ctxFor(viewer); got["shared"] != "none" || !hidden(got) {
		t.Errorf("viewer, not shared: %v", got)
	}
	if got := ctxFor(outsider); got["context"] == nil {
		t.Errorf("member without a role lost the context: %v", got)
	}
	share := func(mode string) {
		v, _ := json.Marshal(`{"mode":"` + mode + `","at":"2026-11-20"}`)
		dbfx.Exec(t, `UPDATE issue SET metadata = metadata || jsonb_build_object('kh_share', $2::jsonb, 'kh_synced', $2::jsonb, 'kh_summary', 'Godkendt resumé') WHERE id = $1`, intake, string(v))
	}
	share("summary")
	if got := ctxFor(viewer); got["shared"] != "summary" || got["summary"] != "Godkendt resumé" || !hidden(got) {
		t.Errorf("viewer, summary shared: %v", got)
	}
	share("full")
	got := ctxFor(viewer)
	c, _ := json.Marshal(got["context"])
	if !strings.Contains(string(c), "decision_rights") || strings.Contains(string(c), "updated_by") {
		t.Errorf("viewer, whole project shared: %s", c)
	}
	if log := content(call(viewer, "multica_decision_log", map[string]any{"project_id": number})); log["shared"] != true {
		t.Errorf("viewer, whole project shared: decision log %v", log["shared"])
	}

	// multica_get_issue: a viewer reads only issues they created themselves.
	own := dbfx.Issue(t, "Spørgsmål fra Vera", testutil.Cols{"project_id": project, "number": counter(), "creator_type": "member", "creator_id": viewer})
	other := dbfx.Issue(t, "Spørgsmål fra Otto", testutil.Cols{"project_id": project, "number": counter(), "creator_type": "member", "creator_id": outsider})
	if result := call(viewer, "multica_get_issue", map[string]any{"id": own}); result["isError"] == true {
		t.Errorf("viewer could not read own question: %v", result["content"])
	}
	if result := call(viewer, "multica_get_issue", map[string]any{"id": other}); result["isError"] != true {
		t.Errorf("viewer read someone else's issue")
	}
	if result := call(outsider, "multica_get_issue", map[string]any{"id": own}); result["isError"] == true {
		t.Errorf("member could not read an issue: %v", result["content"])
	}
}

func incomingCallAs(t *testing.T, user, name string, args any) map[string]any {
	t.Helper()
	req := incomingRequest(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	req.Header.Set("X-User-ID", user)
	var response struct {
		Result map[string]any `json:"result"`
	}
	testutil.Call(t, testHandler.IncomingMCP, req).Want(200).JSON(&response)
	return response.Result
}

func incomingListAs(t *testing.T, user string) []string {
	t.Helper()
	req := incomingRequest(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	req.Header.Set("X-User-ID", user)
	var response struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	testutil.Call(t, testHandler.IncomingMCP, req).Want(200).JSON(&response)
	out := []string{}
	for _, tool := range response.Result.Tools {
		out = append(out, tool.Name)
	}
	return out
}
