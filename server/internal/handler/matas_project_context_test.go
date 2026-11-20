package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestMatasNormalizeContext(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	good := `{"goals":["  Kortere svartid i kundeservice ", ""],"priorities":["Pilot i to butikker"],
		"milestones":[{"date":"2026-12-01","what":"Pilot live"}],"decision_rights":{"technical":"Technical Owner","steerco":""},
		"sources":[{"type":"teams","name":"Kanal: Pilot","url":"https://teams.microsoft.com/l/channel/x"}],
		"not_sources":[{"type":"teams","name":"Pilot (gammel)"}],"watch_for":["Estimater er ofte for optimistiske"],
		"terms":[{"term":"KS","meaning":"Kundeservice"}],"updated_at":"1999-01-01T00:00:00Z","updated_by":"someone@else.dk"}`
	out, err := matasNormalizeContext(good, false, "anna@matas.dk", now)
	if err != nil {
		t.Fatal(err)
	}
	var c matasContext
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatal(err)
	}
	// Trimmed, empty entries dropped, and who/when set by the server whatever the client sent.
	if len(c.Goals) != 1 || c.Goals[0] != "Kortere svartid i kundeservice" || len(c.DecisionRights) != 1 ||
		c.UpdatedAt != "2026-11-20T09:00:00Z" || c.UpdatedBy != "anna@matas.dk" || c.NotSources[0].URL != "" {
		t.Fatalf("normalised %s", out)
	}
	if strings.Contains(out, `<`) || !strings.Contains(out, `"milestones":[{"date":"2026-12-01"`) {
		t.Fatalf("stored form %s", out)
	}

	long := strings.Repeat("x", 201)
	many := func(n int, item string) string {
		return "[" + strings.TrimSuffix(strings.Repeat(item+",", n), ",") + "]"
	}
	for raw, want := range map[string]string{
		`{"goals":[]}`:                                                                       "1–3 goals",
		`{"goals":["a","b","c","d"]}`:                                                        "at most 3",
		`{"goals":["a"],"budget":1}`:                                                         "unknown field",
		`{"goals":["` + long + `"]}`:                                                         "at most 200",
		`{"goals":["a\nb"]}`:                                                                 "one line",
		`{"goals":["a"],"priorities":["a","b","c","d"]}`:                                     "priorities has 4",
		`{"goals":["a"],"milestones":[{"date":"1. dec","what":"x"}]}`:                        "YYYY-MM-DD",
		`{"goals":["a"],"milestones":[{"date":"2026-02-30","what":"x"}]}`:                    "YYYY-MM-DD",
		`{"goals":["a"],"milestones":[{"date":"2026-12-01","what":""}]}`:                     "what must not be empty",
		`{"goals":["a"],"decision_rights":{"marketing":"x"}}`:                                "not an area",
		`{"goals":["a"],"sources":[{"type":"mail","name":"x","url":"https://x"}]}`:           "type must be one of",
		`{"goals":["a"],"sources":[{"type":"repo","name":"x"}]}`:                             "url is required",
		`{"goals":["a"],"sources":[{"type":"repo","name":"x","url":"javascript:alert(1)"}]}`: "http(s) link",
		`{"goals":["a"],"terms":[{"term":"KS","meaning":""}]}`:                               "meaning must not be empty",
		`{"goals":["a"],"owner":"Anna"}`:                                                     "only for the portfolio",
		`{"goals":["a"],"terms":` + many(30, `{"term":"`+strings.Repeat("t", 60)+`","meaning":"`+strings.Repeat("m", 200)+`"}`) + `}`: "at most 4500",
		`[1]`: "JSON object",
	} {
		if _, err := matasNormalizeContext(raw, false, "a@b.dk", now); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.60s: got %v, want %q", raw, err, want)
		}
	}
	if out, err := matasNormalizeContext(`{"goals":["Én portefølje"],"owner":" Anna "}`, true, "a@b.dk", now); err != nil || !strings.Contains(out, `"owner":"Anna"`) {
		t.Fatalf("portfolio owner: %v %s", err, out)
	}
}

func TestMatasProjectContext(t *testing.T) {
	counter := func() int {
		var n int
		dbfx.QueryRow(t, `UPDATE workspace SET issue_counter = issue_counter + 1 WHERE id = $1 RETURNING issue_counter`, testWorkspaceID).Scan(&n)
		return n
	}
	tag := uuid.NewString()[:8]
	person := func(name, role string) (string, string) {
		email := fmt.Sprintf("ctx-%s-%s@matas.test", strings.ToLower(name), tag)
		id := dbfx.User(t, name, email)
		dbfx.Member(t, testWorkspaceID, id, role)
		return id, email
	}
	po, poEmail := person("Petra", "member") // product owner, e-mail on the role in dia_spec
	bo, boEmail := person("Bent", "member")  // business owner, linked by the roster only (ctx_editors)
	outsider, _ := person("Otto", "member")  // a member outside the diamond
	admin, _ := person("Alma", "admin")      // a workspace admin outside the diamond
	spec, _ := json.Marshal(map[string]any{"slug": "ctx-" + tag, "name": "Kontekst " + tag, "roles": map[string]any{
		"bo": map[string]any{"person": "Bent"}, "po": map[string]any{"person": "Petra", "email": poEmail},
		"plo": map[string]any{"person": nil}, "to": map[string]any{"person": nil}}})
	number := "CTX" + tag + "-1"
	meta, _ := json.Marshal(map[string]any{"pf_intake": true, "dia_spec": string(spec), "pf_number": number})
	project := dbfx.Project(t, "Kontekst "+tag)
	intake := dbfx.Issue(t, "Intake — Kontekst", testutil.Cols{"status": "done", "project_id": project, "number": counter(), "metadata": string(meta)})
	plain := dbfx.Issue(t, "Not an intake", testutil.Cols{"project_id": project, "number": counter()})
	dbfx.Cleanup(t, `DELETE FROM issue WHERE project_id = $1`, project)

	ctx := func(goal string) string {
		b, _ := json.Marshal(`{"goals":["` + goal + `"],"sources":[{"type":"clickup","name":"Liste","url":"https://app.clickup.com/x"}]}`)
		return string(b)
	}
	set := func(user, issue, key, value string, headers ...string) int {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParams(newRequestAs(user, "PUT", "/api/issues/"+issue+"/metadata/"+key, json.RawMessage(`{"value":`+value+`}`)), "id", issue, "key", key)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		testHandler.SetIssueMetadataKey(w, req)
		return w.Code
	}
	del := func(user, issue, key string) int {
		w := httptest.NewRecorder()
		testHandler.DeleteIssueMetadataKey(w, withURLParams(newRequestAs(user, "DELETE", "/", nil), "id", issue, "key", key))
		return w.Code
	}
	stored := func(issue string) matasContext {
		t.Helper()
		var raw string
		dbfx.QueryRow(t, `SELECT coalesce(metadata->>'ctx_spec', '') FROM issue WHERE id = $1`, issue).Scan(&raw)
		var c matasContext
		_ = json.Unmarshal([]byte(raw), &c)
		return c
	}

	// Who may edit a project's context: the workspace owner and admins, and the diamond's owners.
	for _, tc := range []struct {
		who  string
		user string
		want int
	}{
		{"owner", testUserID, 200}, {"admin", admin, 200}, {"po by role e-mail", po, 200},
		{"bo before the roster link", bo, 403}, {"outsider", outsider, 403},
	} {
		if got := set(tc.user, intake, "ctx_spec", ctx(tc.who)); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.who, got, tc.want)
		}
	}
	if c := stored(intake); c.Goals[0] != "po by role e-mail" || c.UpdatedBy != poEmail {
		t.Fatalf("stored %+v", c)
	}
	// The roster links Bent's login to BO; only an admin (the sync) may write that link.
	editors := fmt.Sprintf(`"{\"bo\":[\"%s\"]}"`, strings.ToUpper(boEmail))
	if got := set(po, intake, "ctx_editors", editors); got != 403 {
		t.Fatalf("po wrote ctx_editors: %d", got)
	}
	if got := set(testUserID, intake, "ctx_editors", editors); got != 200 {
		t.Fatalf("owner wrote ctx_editors: %d", got)
	}
	if got := set(bo, intake, "ctx_spec", ctx("bo")); got != 200 || stored(intake).UpdatedBy != boEmail {
		t.Fatalf("bo after the roster link: %d %+v", got, stored(intake))
	}
	// Agents propose; they never edit, whoever's token they run with.
	if got := set(testUserID, intake, "ctx_spec", ctx("agent"), "X-Actor-Source", "task_token", "X-Agent-ID", uuid.NewString()); got != 403 {
		t.Fatalf("agent edit: %d", got)
	}
	if got := set(po, intake, "ctx_spec", `"{\"goals\":[]}"`); got != 400 {
		t.Fatalf("invalid context: %d", got)
	}
	if got := set(testUserID, plain, "ctx_spec", ctx("x")); got != 400 {
		t.Fatalf("context on a plain issue: %d", got)
	}
	if got := del(outsider, intake, "ctx_spec"); got != 403 || stored(intake).Goals[0] != "bo" {
		t.Fatalf("outsider delete: %d", got)
	}

	// The portfolio context: only owners and admins mark its issue and edit it.
	portfolio := dbfx.Issue(t, "Porteføljekontekst", testutil.Cols{"status": "done", "number": counter()})
	if got := set(po, portfolio, "portfolio_context", "true"); got != 403 {
		t.Fatalf("member marked the portfolio: %d", got)
	}
	if got := set(admin, portfolio, "portfolio_context", "true"); got != 200 {
		t.Fatalf("admin marked the portfolio: %d", got)
	}
	portfolioCtx, _ := json.Marshal(`{"goals":["Færre, større projekter"],"priorities":["Kundeservice"],"owner":"Alma"}`)
	if got := set(po, portfolio, "ctx_spec", string(portfolioCtx)); got != 403 {
		t.Fatalf("diamond owner edited the portfolio: %d", got)
	}
	if got := set(admin, portfolio, "ctx_spec", string(portfolioCtx)); got != 200 {
		t.Fatalf("admin edited the portfolio: %d", got)
	}

	// MCP: the context by UUID, by project number and for the portfolio; a clear error otherwise.
	ok := func(result map[string]any) map[string]any {
		t.Helper()
		if result["isError"] == true {
			t.Fatalf("call failed: %v", result["content"])
		}
		return result["structuredContent"].(map[string]any)
	}
	for _, ref := range []string{project, " " + number + " "} {
		got := ok(incomingCall(t, "multica_project_context", map[string]any{"project_id": ref}))
		c, _ := json.Marshal(got["context"])
		if !strings.Contains(string(c), `"goals":["bo"]`) || got["stale"] != false || got["project"].(map[string]any)["number"] != number || got["diamond"] == nil {
			t.Fatalf("%s: %v", ref, got)
		}
	}
	if got := ok(incomingCall(t, "multica_project_context", map[string]any{"project_id": "Portfolio"})); !strings.Contains(fmt.Sprint(got["context"]), "Færre") {
		t.Fatalf("portfolio: %v", got)
	}
	for ref, want := range map[string]string{"CTX" + tag + "-9": "no project has project number", uuid.NewString(): "project not found", "Kontekst": "project UUID or a project number"} {
		if result := incomingCall(t, "multica_project_context", map[string]any{"project_id": ref}); result["isError"] != true || !strings.Contains(fmt.Sprint(result["content"]), want) {
			t.Errorf("%s: %v", ref, result["content"])
		}
	}
	// Not changed for 30 days: stale.
	old := stored(intake)
	old.UpdatedAt = time.Now().AddDate(0, 0, -31).UTC().Format(time.RFC3339)
	b, _ := json.Marshal(old)
	dbfx.Exec(t, `UPDATE issue SET metadata = jsonb_set(metadata, '{ctx_spec}', to_jsonb($2::text)) WHERE id = $1`, intake, string(b))
	if got := ok(incomingCall(t, "multica_project_context", map[string]any{"project_id": number})); got["stale"] != true || got["days_since_update"] != float64(31) {
		t.Fatalf("stale: %v", got)
	}

	// A proposal is a task in review for the product owner; the context itself is unchanged.
	proposal := map[string]any{"request_id": uuid.NewString(), "project_id": number,
		"field": "sources", "proposal": "Tilføj Teams-kanalen «Pilot drift».", "why": "Nævnt i tre opdateringer i november."}
	receipt := ok(incomingCall(t, "multica_propose_project_context", proposal))
	if again := ok(incomingCall(t, "multica_propose_project_context", proposal)); again["id"] != receipt["id"] {
		t.Fatalf("a retry made a second proposal: %v %v", again["id"], receipt["id"])
	}
	var status, assignee, kind, field string
	dbfx.QueryRow(t, `SELECT status, assignee_type || ':' || assignee_id::text, metadata->>'ctx_proposal', metadata->>'ctx_field' FROM issue WHERE id = $1`, receipt["id"]).Scan(&status, &assignee, &kind, &field)
	if status != "in_review" || assignee != "member:"+po || kind != "true" || field != "sources" {
		t.Fatalf("proposal %s %s %s %s", status, assignee, kind, field)
	}
	if result := incomingCall(t, "multica_propose_project_context", map[string]any{"request_id": uuid.NewString(), "project_id": number,
		"field": "budget", "proposal": "x", "why": "y"}); result["isError"] != true {
		t.Fatalf("unknown field accepted")
	}
	if stored(intake).Goals[0] != "bo" {
		t.Fatal("a proposal changed the context")
	}
}
