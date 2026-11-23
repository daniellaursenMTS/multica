package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestMatasBriefQuestions(t *testing.T) {
	// Nothing waits: three of the role's own questions.
	qs := matasBriefQuestions([]string{"bo"}, map[string][]matasBriefItem{}, "")
	if len(qs) != 3 || qs[0].Text != matasRoleQuestions["bo"][0] || qs[2].Kind != "role" {
		t.Fatalf("empty brief %+v", qs)
	}
	// Concrete questions first, decisions before risks, at most three; then two of the roles' own.
	waiting := map[string][]matasBriefItem{
		"decisions": {{ID: "1-1/B-001", Text: "Azure"}},
		"risks":     {{ID: "1-1/R-001", Text: "IT", Date: "2026-11-01"}, {ID: "1-1/R-002", Text: "Drift", Date: "2026-11-02"}},
		"questions": {{ID: "1-1/Q-001", Text: "Hvornår?"}},
	}
	qs = matasBriefQuestions([]string{"plo", "to"}, waiting, "")
	if len(qs) != 5 || qs[0].About != "1-1/B-001" || !strings.Contains(qs[0].Text, "styregruppen") || qs[1].About != "1-1/R-001" ||
		!strings.Contains(qs[1].Text, "1.11.2026") || qs[3].Text != matasRoleQuestions["plo"][0] || qs[4].Text != matasRoleQuestions["to"][0] {
		t.Fatalf("brief %+v", qs)
	}
}

func TestMatasSimilarTitles(t *testing.T) {
	titles := map[string]string{"a": "Multica-opsætning", "b": "Talk with Multica", "c": "Kundeservice", "d": "Multicas drift"}
	if got := matasSimilarTitles("Multica-opsætning", nil, "a", titles); strings.Join(got, "|") != "Talk with Multica" {
		t.Fatalf("similar %v", got)
	}
	if got := matasSimilarTitles("Kundeservice", []string{"KS"}, "c", titles); len(got) != 0 {
		t.Fatalf("no match %v", got)
	}
	if link, name, ok := matasRepoLink("git@github.com:daniellaursenMTS/hermes.git"); !ok || link != "https://github.com/daniellaursenMTS/hermes" || name != "daniellaursenMTS/hermes" {
		t.Fatalf("ssh repo %s %s", link, name)
	}
}

// callErr calls an MCP tool as a user and returns the error text, or fails if it succeeded.
func (f *memoryFixture) callErr(user, name string, args map[string]any) string {
	f.t.Helper()
	req := testutil.WithHeaders(testutil.JSONRequest("POST", "/api/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args}}), "Authorization", "Bearer mul_test", "X-User-ID", user, "X-Workspace-ID", testWorkspaceID)
	var response struct {
		Result map[string]any `json:"result"`
	}
	testutil.Call(f.t, testHandler.IncomingMCP, req).Want(200).JSON(&response)
	if response.Result["isError"] != true {
		f.t.Fatalf("%s succeeded: %v", name, response.Result["structuredContent"])
	}
	return fmt.Sprint(response.Result["content"])
}

func TestIncomingMCPInterviewAndContextDraft(t *testing.T) {
	f := newMemoryFixture(t)
	tag := f.tag
	po, poEmail := f.member("Petra")
	to, toEmail := f.member("Tobias")
	outsider, _ := f.member("Otto")
	admin, _ := f.member("Alma")
	dbfx.Exec(t, `UPDATE member SET role = 'admin' WHERE user_id = $1 AND workspace_id = $2`, admin, testWorkspaceID)

	now := time.Now()
	day := func(d int) string { return now.AddDate(0, 0, d).Format("2006-01-02") }
	num := "IV" + tag + "-1"
	post := func(id, kind, area, topic, text, said, status string) matasMemoryPost {
		return matasMemoryPost{ID: num + "/" + id, Type: kind, Area: area, Topic: topic, Text: text, SaidAt: said, Source: "MATA-1", Status: status, Rev: 1}
	}
	posts := []matasMemoryPost{
		post("B-001", "decision", "technical", "hosting", "Multica flytter til VibeHub.", day(-3), "proposal"),
		post("B-002", "decision", "business", "capex", "Capex-andelen beregnes i kroner.", day(-3), "proposal"),
		post("R-001", "risk", "", "it-godkendelse", "IT-godkendelsen kan trække ud.", day(-30), "recorded"),
		post("R-002", "risk", "", "hosting", "Hosting er ikke afklaret.", day(-30), "recorded"),
		post("F-001", "fact", "", "hosting", "VibeHub kan hoste Multica.", day(-2), "recorded"),
		post("R-003", "risk", "product", "brugere", "Brugerne kender ikke værktøjet.", day(-30), "recorded"),
		post("Q-001", "question", "", "n8n", "Hvornår får vi adgang til n8n?", day(-10), "recorded"),
	}
	roles := map[string]any{"bo": map[string]any{"person": "Bent"}, "po": map[string]any{"person": "Petra", "email": poEmail},
		"plo": map[string]any{"person": nil}, "to": map[string]any{"person": "Tobias", "email": toEmail}}
	ctx := fmt.Sprintf(`{"goals":["Mindre manuelt arbejde"],"milestones":[{"date":"%s","what":"Pilot live"},{"date":"%s","what":"Udrulning"}],"updated_at":"%s"}`,
		day(5), day(40), now.UTC().Format(time.RFC3339))
	alfa := f.project("Alfa", num, roles, posts, map[string]any{matasContextKey: ctx, "pf_clickup_url": "https://app.clickup.com/t/abc"})
	// The same product owner in another project: nothing from it may reach Alfa's brief.
	other := "IW" + tag + "-1"
	f.project("Beta", other, map[string]any{"po": map[string]any{"person": "Petra", "email": poEmail}},
		[]matasMemoryPost{{ID: other + "/R-001", Type: "risk", Text: "Beta-hemmelighed om leverandøren.", SaidAt: day(-30), Source: "MATA-2", Status: "recorded"}}, nil)
	dbfx.Issue(t, "Forslag til kontekst: Alfa — sources", testutil.Cols{"status": "in_review", "project_id": alfa, "number": f.number(),
		"assignee_type": "member", "assignee_id": po, "metadata": `{"ctx_proposal": true}`})

	ids := func(items any) string {
		out := []string{}
		for _, x := range items.([]any) {
			out = append(out, strings.TrimPrefix(fmt.Sprint(x.(map[string]any)["id"]), num+"/"))
		}
		return strings.Join(out, " ")
	}

	// The product owner: risks without update in no area or in product, the milestone within two
	// weeks, the open question and the context proposal assigned to her; no decision waits for her.
	brief := f.call(po, "multica_interview_brief", map[string]any{"project_id": num, "person": "petra"})
	waiting := brief["waiting"].(map[string]any)
	if ids(waiting["decisions"]) != "" || ids(waiting["risks"]) != "R-001 R-003" || ids(waiting["questions"]) != "Q-001" ||
		len(waiting["milestones"].([]any)) != 1 || len(waiting["proposals"].([]any)) != 1 {
		t.Fatalf("po waiting %v", waiting)
	}
	qs := brief["questions"].([]any)
	if len(qs) != 5 || qs[0].(map[string]any)["kind"] != "milestone" || qs[4].(map[string]any)["text"] != matasRoleQuestions["po"][1] {
		t.Fatalf("po questions %v", qs)
	}
	if send := brief["send"].(map[string]any); send["speaker"] != "Petra" || send["source"] != "interview" {
		t.Fatalf("send %v", send)
	}
	if b, _ := json.Marshal(brief); strings.Contains(string(b), "Beta-hemmelighed") || strings.Contains(string(b), other) {
		t.Fatalf("another project leaked into the brief: %s", b)
	}

	// The technical owner: the technical proposal only he may confirm, and no product risk.
	brief = f.call(to, "multica_interview_brief", map[string]any{"project_id": alfa})
	waiting = brief["waiting"].(map[string]any)
	if ids(waiting["decisions"]) != "B-001" || ids(waiting["risks"]) != "R-001" || len(waiting["proposals"].([]any)) != 0 {
		t.Fatalf("to waiting %v", waiting)
	}
	if q := brief["questions"].([]any)[0].(map[string]any); q["about"] != num+"/B-001" || !strings.Contains(fmt.Sprint(q["text"]), "styregruppen") {
		t.Fatalf("to first question %v", q)
	}

	// Without a role in the diamond, even as an admin: a clear refusal. And only for yourself.
	for _, user := range []string{outsider, admin} {
		if msg := f.callErr(user, "multica_interview_brief", map[string]any{"project_id": num}); !strings.Contains(msg, "no role in the diamond") || !strings.Contains(msg, "Product Owner: Petra") {
			t.Fatalf("refusal %q", msg)
		}
	}
	if msg := f.callErr(po, "multica_interview_brief", map[string]any{"project_id": num, "person": "Tobias"}); !strings.Contains(msg, "only prepared for yourself") {
		t.Fatalf("other person %q", msg)
	}

	// The context draft: each filled value has a source, the rest stays empty with a question.
	dbfx.Exec(t, `UPDATE project SET due_date = $2 WHERE id = $1`, alfa, day(60))
	dbfx.Exec(t, `INSERT INTO project_resource (project_id, workspace_id, resource_type, resource_ref) VALUES ($1, $2, 'github_repo', '{"url":"https://github.com/daniellaursenMTS/alfa.git"}')`, alfa, testWorkspaceID)
	draft := f.call(po, "multica_draft_project_context", map[string]any{"project_id": num})
	d := draft["draft"].(map[string]any)
	rights := d["decision_rights"].(map[string]any)
	if len(d["goals"].([]any)) != 0 || len(d["not_sources"].([]any)) != 0 || len(d["priorities"].([]any)) != 0 || len(d["terms"].([]any)) != 0 ||
		rights["business"] != "Bent (Business Owner)" || rights["technical"] != "Tobias (Technical Owner)" || rights["platform"] != nil || rights["steerco"] != nil {
		t.Fatalf("draft %v", d)
	}
	srcs := d["sources"].([]any)
	if len(srcs) != 2 || srcs[0].(map[string]any)["url"] != "https://app.clickup.com/t/abc" || srcs[1].(map[string]any)["url"] != "https://github.com/daniellaursenMTS/alfa" {
		t.Fatalf("draft sources %v", srcs)
	}
	if w := d["watch_for"].([]any); len(w) != 3 || len(d["milestones"].([]any)) != 1 {
		t.Fatalf("draft watch_for %v milestones %v", w, d["milestones"])
	}
	cited := map[string]bool{}
	for _, s := range draft["sources"].([]any) {
		cited[strings.SplitN(s.(map[string]any)["field"].(string), "[", 2)[0]] = s.(map[string]any)["source"] != ""
	}
	for _, field := range []string{"decision_rights.business", "decision_rights.product", "decision_rights.technical", "sources", "milestones", "watch_for"} {
		if !cited[field] {
			t.Errorf("no source for %s: %v", field, draft["sources"])
		}
	}
	if b, _ := json.Marshal(draft); strings.Contains(string(b), "Beta-hemmelighed") || !strings.Contains(string(b), "R-001") {
		t.Fatalf("draft cites %s", b)
	}
	questions := []string{}
	for _, q := range draft["questions"].([]any) {
		questions = append(questions, q.(map[string]any)["field"].(string))
	}
	if strings.Join(questions, " ") != "goals decision_rights not_sources" {
		t.Fatalf("draft questions %v", questions)
	}
	if msg := f.callErr(outsider, "multica_draft_project_context", map[string]any{"project_id": num}); !strings.Contains(msg, "only the diamond's owners") {
		t.Fatalf("outsider draft %q", msg)
	}
	// «Lav udkast» on the Kontekst tab reads the same draft over REST, with the same rule.
	for user, want := range map[string]int{po: 200, outsider: 403} {
		w := httptest.NewRecorder()
		testHandler.GetProjectContextDraft(w, withURLParams(newRequestAs(user, "GET", "/api/projects/"+alfa+"/context-draft", nil), "id", alfa))
		if w.Code != want || (want == 200 && !strings.Contains(w.Body.String(), "Tobias (Technical Owner)")) {
			t.Fatalf("REST draft %d %s", w.Code, w.Body.String())
		}
	}
	if msg := f.callErr(po, "multica_draft_project_context", map[string]any{"project_id": "portfolio"}); !strings.Contains(msg, "only workspace owners and admins") {
		t.Fatalf("member portfolio draft %q", msg)
	}

	// Saving: the owner as herself; never someone outside the diamond; never over a newer edit.
	current := draft["current"].(map[string]any)["updated_at"].(string)
	approved := map[string]any{"goals": []string{"Mindre manuelt arbejde i kundeservice"}, "decision_rights": rights, "sources": srcs}
	if msg := f.callErr(outsider, "multica_save_project_context", map[string]any{"project_id": num, "context": approved}); !strings.Contains(msg, "only the project's diamond owners") {
		t.Fatalf("outsider save %q", msg)
	}
	if msg := f.callErr(po, "multica_save_project_context", map[string]any{"project_id": num, "context": approved, "based_on_updated_at": "2020-01-01T00:00:00Z"}); !strings.Contains(msg, "changed after the draft") {
		t.Fatalf("stale save %q", msg)
	}
	if msg := f.callErr(po, "multica_save_project_context", map[string]any{"project_id": num, "context": map[string]any{"goals": []string{}}}); !strings.Contains(msg, "1–3 goals") {
		t.Fatalf("no goals %q", msg)
	}
	saved := f.call(po, "multica_save_project_context", map[string]any{"project_id": num, "context": approved, "based_on_updated_at": current})
	var stored matasContext
	var raw string
	dbfx.QueryRow(t, `SELECT metadata->>'ctx_spec' FROM issue WHERE project_id = $1 AND metadata @> '{"pf_intake": true}'::jsonb`, alfa).Scan(&raw)
	if err := json.Unmarshal([]byte(raw), &stored); err != nil || saved["saved"] != true || stored.Goals[0] != "Mindre manuelt arbejde i kundeservice" ||
		stored.UpdatedBy != poEmail || len(stored.Sources) != 2 {
		t.Fatalf("saved %v stored %s", saved, raw)
	}

	// The portfolio: an admin drafts it and saves it; what it holds then leaves the project's draft.
	var existing int
	dbfx.QueryRow(t, `SELECT count(*) FROM issue WHERE workspace_id = $1 AND metadata @> '{"portfolio_context": true}'::jsonb`, testWorkspaceID).Scan(&existing)
	if existing > 0 {
		t.Skip("the workspace already has a portfolio context issue")
	}
	dbfx.Cleanup(t, `DELETE FROM issue WHERE workspace_id = $1 AND metadata @> '{"portfolio_context": true}'::jsonb`, testWorkspaceID)
	pdraft := f.call(admin, "multica_draft_project_context", map[string]any{"project_id": "portfolio"})
	pd := pdraft["draft"].(map[string]any)
	if len(pd["terms"].([]any)) < len(matasCommonTerms) || len(pd["goals"].([]any)) != 0 || pdraft["current"] != nil {
		t.Fatalf("portfolio draft %v", pdraft)
	}
	for _, s := range pdraft["sources"].([]any) {
		if !strings.HasPrefix(s.(map[string]any)["source"].(string), matasDocs) && !strings.HasPrefix(s.(map[string]any)["source"].(string), "projektkonteksten i ") {
			t.Fatalf("portfolio source %v", s)
		}
	}
	if msg := f.callErr(po, "multica_save_project_context", map[string]any{"project_id": "portfolio", "context": map[string]any{"goals": []string{"x"}}}); !strings.Contains(msg, "owners and admins") {
		t.Fatalf("member portfolio save %q", msg)
	}
	pd["goals"] = []string{"Færre, større projekter"}
	pd["watch_for"] = []string{"IT-godkendelsen kan trække ud."}
	f.call(admin, "multica_save_project_context", map[string]any{"project_id": "portfolio", "context": pd, "based_on_updated_at": ""})
	draft = f.call(po, "multica_draft_project_context", map[string]any{"project_id": num})
	if w := draft["draft"].(map[string]any)["watch_for"].([]any); len(w) != 2 || len(draft["left_out"].([]any)) != 1 {
		t.Fatalf("portfolio item repeated: %v left out %v", w, draft["left_out"])
	}
}

func TestIncomingMCPToolList(t *testing.T) {
	var response struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	testutil.Call(t, testHandler.IncomingMCP, incomingRequest(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})).Want(200).JSON(&response)
	names := map[string]map[string]any{}
	for _, tool := range response.Result.Tools {
		names[tool["name"].(string)] = tool
	}
	for _, name := range []string{"multica_interview_brief", "multica_draft_project_context", "multica_save_project_context"} {
		if names[name] == nil {
			t.Fatalf("%s not listed", name)
		}
	}
	if names["multica_save_project_context"]["annotations"].(map[string]any)["destructiveHint"] != true {
		t.Fatal("saving a context must ask the client to confirm")
	}
}
