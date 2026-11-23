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

func memoryPosts() []matasMemoryPost {
	return []matasMemoryPost{
		{ID: "1000-9/H-001", Type: "event", Text: "Behandlet: Hosting på Azure", SaidAt: "2026-11-20", Source: "MATA-300", Status: "recorded"},
		{ID: "1000-9/B-001", Type: "decision", Area: "technical", Topic: "hosting", Text: "Multica flytter til Matas' Azure.", SaidAt: "2026-11-20",
			Source: "MATA-300", Status: "superseded", SupersededBy: "1000-9/B-002"},
		{ID: "1000-9/F-001", Type: "fact", Text: "Installationen kører på én Mac.", SaidAt: "2026-11-20", Source: "MATA-300", Status: "recorded"},
		{ID: "1000-9/H-002", Type: "event", Text: "Behandlet: Ny hosting", SaidAt: "2026-12-01", Source: "MATA-310", Status: "recorded"},
		{ID: "1000-9/B-002", Type: "decision", Area: "technical", Topic: "hosting", Text: "Multica flytter til VibeHub i stedet for Azure.", SaidAt: "2026-12-01",
			Source: "MATA-310", Status: "confirmed", Supersedes: "1000-9/B-001", ConfirmedBy: "to", ConfirmedAt: "2026-12-02"},
		{ID: "1000-9/B-003", Type: "decision", Area: "business", Text: "Capex-andelen beregnes i kroner.", SaidAt: "2026-12-01",
			Source: "MATA-310", Status: "proposal", Review: "Forslag om at erstatte noget."},
		{ID: "1000-9/R-001", Type: "risk", Text: "IT-godkendelsen kan trække ud.", SaidAt: "2026-12-01", Source: "MATA-310", Status: "recorded",
			RiskNo: "R1", Likelihood: "middel", Impact: "høj", ScoredBy: "agent"},
	}
}

func TestMatasSearchPosts(t *testing.T) {
	ids := func(hits []matasMemoryHit) string {
		out := []string{}
		for _, h := range hits {
			out = append(out, strings.TrimPrefix(h.Post.ID, "1000-9/"))
		}
		return strings.Join(out, " ")
	}
	for _, tc := range []struct {
		q    matasMemoryQuery
		want string
	}{
		{matasMemoryQuery{}, "R-001 B-003 B-002 F-001"},                              // current: no events, nothing superseded
		{matasMemoryQuery{View: "log"}, "R-001 H-002 B-003 B-002 H-001 F-001 B-001"}, // log: every post
		{matasMemoryQuery{Query: "azure"}, "B-002"},
		{matasMemoryQuery{Query: "AZURE", View: "log", Type: "decision"}, "B-002 B-001"},
		{matasMemoryQuery{Type: "decision"}, "B-003 B-002"},
		{matasMemoryQuery{Status: "proposal"}, "B-003"},
		{matasMemoryQuery{Since: "2026-11-21", Type: "risk"}, "R-001"},
		{matasMemoryQuery{Until: "2026-11-20"}, "F-001"},
		{matasMemoryQuery{Query: "mac hosting"}, "B-002 F-001"},
		{matasMemoryQuery{Query: "intet her"}, ""},
	} {
		if got := ids(matasSearchPosts(memoryPosts(), tc.q)); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.q, got, tc.want)
		}
	}
}

func TestMatasBuildDecisionLog(t *testing.T) {
	log := matasBuildDecisionLog(memoryPosts())
	decisions := log["decisions"].([]matasDecisionEntry)
	if len(decisions) != 1 || decisions[0].ID != "1000-9/B-002" || len(decisions[0].Chain) != 1 || decisions[0].Chain[0].ID != "1000-9/B-001" {
		t.Fatalf("decisions %+v", decisions)
	}
	if p := log["proposals"].([]matasMemoryPost); len(p) != 1 || p[0].ID != "1000-9/B-003" {
		t.Fatalf("proposals %+v", p)
	}
	if r := log["reviews"].([]matasMemoryPost); len(r) != 1 || r[0].ID != "1000-9/B-003" {
		t.Fatalf("reviews %+v", r)
	}
	// A broken or circular chain stops instead of looping.
	loop := []matasMemoryPost{{ID: "a", Type: "decision", Status: "decision", Supersedes: "b"}, {ID: "b", Type: "decision", Status: "superseded", Supersedes: "a"}}
	if d := matasBuildDecisionLog(loop)["decisions"].([]matasDecisionEntry); len(d[0].Chain) != 50 {
		t.Fatalf("chain %d", len(d[0].Chain))
	}
}

func TestMatasMemoryReadable(t *testing.T) {
	spec := `{"roles":{"po":{"person":"Petra","email":"Petra@Matas.dk"},"bo":{"person":"Bent"}}}`
	meta := func(kv ...string) map[string]json.RawMessage {
		m := map[string]json.RawMessage{"dia_spec": json.RawMessage(mustJSON(spec))}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = json.RawMessage(mustJSON(kv[i+1]))
		}
		return m
	}
	full, summary := `{"mode":"full","at":"x"}`, `{"mode":"summary","at":"x"}`
	for _, tc := range []struct {
		name  string
		m     map[string]json.RawMessage
		email string
		want  bool
	}{
		{"owner by role e-mail", meta(), "petra@matas.dk", true},
		{"owner by roster link", meta("ctx_editors", `{"bo":["bent@matas.dk"]}`), "bent@matas.dk", true},
		{"outsider", meta(), "otto@matas.dk", false},
		{"outsider, whole project shared", meta("kh_share", full, "kh_synced", full), "otto@matas.dk", true},
		{"outsider, share not applied yet", meta("kh_share", full), "otto@matas.dk", false},
		{"outsider, summary only", meta("kh_share", summary, "kh_synced", summary), "otto@matas.dk", false},
		{"archived diamond", map[string]json.RawMessage{"dia_spec": json.RawMessage(mustJSON(`{"archived":true,"roles":{"po":{"person":"Petra","email":"petra@matas.dk"}}}`))}, "petra@matas.dk", false},
	} {
		if got := matasMemoryReadable(tc.m, tc.email); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	if got := matasMemoryAsk(meta()); got != "Product Owner: Petra" {
		t.Errorf("ask %q", got)
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestIncomingMCPMemory(t *testing.T) {
	tag := uuid.NewString()[:8]
	counter := func() int {
		var n int
		dbfx.QueryRow(t, `UPDATE workspace SET issue_counter = issue_counter + 1 WHERE id = $1 RETURNING issue_counter`, testWorkspaceID).Scan(&n)
		return n
	}
	poEmail := fmt.Sprintf("mem-petra-%s@matas.test", tag)
	po := dbfx.User(t, "Petra", poEmail)
	dbfx.Member(t, testWorkspaceID, po, "member")
	outsider := dbfx.User(t, "Otto", fmt.Sprintf("mem-otto-%s@matas.test", tag))
	dbfx.Member(t, testWorkspaceID, outsider, "member")

	dir := t.TempDir()
	t.Setenv(matasMemoryDirEnv, dir)
	full, summary := `{"mode":"full","at":"2026-11-20"}`, `{"mode":"summary","at":"2026-11-20"}`
	project := func(name, number string, roles map[string]any, posts []matasMemoryPost, extra map[string]any) string {
		spec, _ := json.Marshal(map[string]any{"slug": strings.ToLower(name), "name": name, "roles": roles})
		meta := map[string]any{"pf_intake": true, "dia_spec": string(spec), "pf_number": number}
		for k, v := range extra {
			meta[k] = v
		}
		raw, _ := json.Marshal(meta)
		id := dbfx.Project(t, name+" "+tag)
		dbfx.Issue(t, "Intake — "+name, testutil.Cols{"status": "done", "project_id": id, "number": counter(), "metadata": string(raw)})
		dbfx.Cleanup(t, `DELETE FROM issue WHERE project_id = $1`, id)
		view, _ := json.Marshal(matasMemoryView{Slug: strings.ToLower(name), Number: number, Name: name, MulticaProject: id, Posts: posts})
		if err := os.WriteFile(filepath.Join(dir, strings.ToLower(name)+".json"), view, 0o644); err != nil {
			t.Fatal(err)
		}
		return id
	}
	post := func(prefix, id, kind, text string) matasMemoryPost {
		return matasMemoryPost{ID: prefix + "/" + id, Type: kind, Text: text, SaidAt: "2026-11-20", Source: "MATA-1", Status: "recorded"}
	}
	numA, numB, numC := "MA"+tag+"-1", "MB"+tag+"-1", "MC"+tag+"-1"
	petra := map[string]any{"po": map[string]any{"person": "Petra", "email": poEmail}}
	others := map[string]any{"po": map[string]any{"person": "Ina"}}
	a := project("Alfa", numA, petra, memoryPosts(), nil)
	project("Beta", numB, others, []matasMemoryPost{post(numB, "F-001", "fact", "Hosting på VibeHub virker.")},
		map[string]any{"kh_share": full, "kh_synced": full})
	project("Gamma", numC, others, []matasMemoryPost{post(numC, "F-001", "fact", "Hosting on-prem er afvist.")},
		map[string]any{"kh_share": summary, "kh_synced": summary})
	// A view for a project that is not in this workspace is ignored.
	stray, _ := json.Marshal(matasMemoryView{Slug: "stray", MulticaProject: uuid.NewString(), Posts: []matasMemoryPost{post("x", "F-001", "fact", "Hosting fremmed.")}})
	_ = os.WriteFile(filepath.Join(dir, "stray.json"), stray, 0o644)

	call := func(user, name string, args map[string]any) map[string]any {
		t.Helper()
		req := testutil.WithHeaders(testutil.JSONRequest("POST", "/api/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": args}}), "Authorization", "Bearer mul_test", "X-User-ID", user, "X-Workspace-ID", testWorkspaceID)
		var response struct {
			Result map[string]any `json:"result"`
		}
		testutil.Call(t, testHandler.IncomingMCP, req).Want(200).JSON(&response)
		if response.Result["isError"] == true {
			t.Fatalf("%s: %v", name, response.Result["content"])
		}
		return response.Result["structuredContent"].(map[string]any)
	}
	numbers := func(list any, key string) string {
		out := []string{}
		for _, x := range list.([]any) {
			p := x.(map[string]any)
			if key == "post" {
				out = append(out, p["post"].(map[string]any)["id"].(string))
			} else {
				out = append(out, p["project"].(map[string]any)["number"].(string))
			}
		}
		return strings.Join(out, " ")
	}
	mine := func(s string) string { // only this test's projects; the workspace may hold others
		out := []string{}
		for _, f := range strings.Fields(s) {
			if strings.Contains(f, tag) {
				out = append(out, f)
			}
		}
		return strings.Join(out, " ")
	}

	// Someone outside every diamond: the whole-shared project's post; the others are named, not quoted.
	got := call(outsider, "multica_search_memory", map[string]any{"query": "hosting"})
	if p := mine(numbers(got["posts"], "post")); p != numB+"/F-001" {
		t.Fatalf("outsider posts %q", p)
	}
	if w := mine(numbers(got["withheld"], "project")); w != numA+" "+numC {
		t.Fatalf("outsider withheld %q", w)
	}
	if b, _ := json.Marshal(got["withheld"]); strings.Contains(string(b), "Azure") || strings.Contains(string(b), "on-prem") || !strings.Contains(string(b), "Product Owner: Petra") {
		t.Fatalf("withheld leaks content or misses whom to ask: %s", b)
	}

	// The product owner of Alfa reads Alfa too, with the filters.
	got = call(po, "multica_search_memory", map[string]any{"project_id": numA, "type": "decision", "view": "log", "query": "azure"})
	if p := numbers(got["posts"], "post"); p != "1000-9/B-002 1000-9/B-001" {
		t.Fatalf("owner posts %q", p)
	}
	if got["posts_reviewed"] != float64(len(memoryPosts())) {
		t.Fatalf("reviewed %v", got["posts_reviewed"])
	}

	// The decision log follows the same rule.
	log := call(outsider, "multica_decision_log", map[string]any{"project_id": a})
	if log["shared"] != false || log["decisions"] != nil || log["ask"] != "Product Owner: Petra" {
		t.Fatalf("outsider log %v", log)
	}
	log = call(po, "multica_decision_log", map[string]any{"project_id": numA})
	decisions := log["decisions"].([]any)
	if len(decisions) != 1 || len(decisions[0].(map[string]any)["chain"].([]any)) != 1 {
		t.Fatalf("owner log %v", log)
	}

	// The project page reads it over REST.
	w := httptest.NewRecorder()
	testHandler.GetProjectDecisionLog(w, withURLParams(newRequestAs(po, "GET", "/api/projects/"+a+"/decision-log", nil), "id", a))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "VibeHub i stedet for Azure") {
		t.Fatalf("REST %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	testHandler.GetProjectDecisionLog(w, withURLParams(newRequestAs(outsider, "GET", "/api/projects/"+a+"/decision-log", nil), "id", a))
	if w.Code != 200 || strings.Contains(w.Body.String(), "Azure") {
		t.Fatalf("REST outsider %d %s", w.Code, w.Body.String())
	}

	for args, want := range map[string]map[string]any{
		"type must be":  {"type": "meeting"},
		"YYYY-MM-DD":    {"since": "1. dec"},
		"view must be":  {"view": "all"},
		"limit must be": {"limit": 500},
	} {
		req := testutil.WithHeaders(testutil.JSONRequest("POST", "/api/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "multica_search_memory", "arguments": want}}), "Authorization", "Bearer mul_test", "X-User-ID", po, "X-Workspace-ID", testWorkspaceID)
		var response struct {
			Result map[string]any `json:"result"`
		}
		testutil.Call(t, testHandler.IncomingMCP, req).Want(200).JSON(&response)
		if response.Result["isError"] != true || !strings.Contains(fmt.Sprint(response.Result["content"]), args) {
			t.Errorf("%v: %v", want, response.Result)
		}
	}
}
