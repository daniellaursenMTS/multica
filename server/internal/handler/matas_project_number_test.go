package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestMatasNextNumber(t *testing.T) {
	for _, tc := range []struct {
		last     int
		existing []string
		want     int
	}{
		{0, nil, 1},
		{3, nil, 4},
		{0, []string{"1004-2", "1004-12", "10045-40", "1000-30", "1004-x"}, 13},
		{20, []string{"1004-12"}, 21},
	} {
		if got := matasNextNumber("1004", tc.last, tc.existing); got != tc.want {
			t.Errorf("next(%d, %v) = %d, want %d", tc.last, tc.existing, got, tc.want)
		}
	}
	for _, s := range []string{"1004-1", "1045-12", "PoC.1-3"} {
		if !matasProjectNumberRE.MatchString(s) {
			t.Errorf("%s is a project number", s)
		}
	}
	for _, s := range []string{"1004", "1004-0", "1004-01", "-1", "1004 -1", uuid.NewString()} {
		if matasProjectNumberRE.MatchString(s) {
			t.Errorf("%s is not a project number", s)
		}
	}
}

func TestMatasProjectNumbers(t *testing.T) {
	// Issue numbers come from the workspace counter, which the issue service uses too.
	counter := func() int {
		var n int
		dbfx.QueryRow(t, `UPDATE workspace SET issue_counter = issue_counter + 1 WHERE id = $1 RETURNING issue_counter`, testWorkspaceID).Scan(&n)
		return n
	}
	register := dbfx.Issue(t, "Capex-register", testutil.Cols{"status": "done", "number": counter(),
		"metadata": `{"capex_register":true,"capex_catalog":"{\"items\":[{\"id\":\"cx-sm\",\"number\":\"1004\",\"description\":\"Social Media\",\"active\":true},{\"id\":\"cx-it\",\"number\":\"1000\",\"description\":\"Internal tools\",\"active\":true}],\"usd_dkk\":6.9}"}`})
	project := func(title, metadata string) (string, string) {
		p := dbfx.Project(t, title)
		return p, dbfx.Issue(t, "Intake — "+title, testutil.Cols{"status": "done", "project_id": p, "number": counter(), "metadata": metadata})
	}
	set := func(issue, key, value string) (int, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParams(newRequest("PUT", "/api/issues/"+issue+"/metadata/"+key, json.RawMessage(`{"value":`+value+`}`)), "id", issue, "key", key)
		testHandler.SetIssueMetadataKey(w, req)
		var resp struct {
			Metadata map[string]any `json:"metadata"`
		}
		_ = json.NewDecoder(w.Body).Decode(&resp)
		return w.Code, resp.Metadata
	}
	stored := func(issue string) map[string]any {
		t.Helper()
		var raw []byte
		dbfx.QueryRow(t, `SELECT metadata FROM issue WHERE id = $1`, issue).Scan(&raw)
		m := map[string]any{}
		_ = json.Unmarshal(raw, &m)
		return m
	}

	// The first project with a Capex starts its series; the response carries the number.
	alpha, alphaIntake := project("Alpha", `{"pf_intake":true,"dia_spec":"{\"name\":\"Alpha\"}"}`)
	if code, m := set(alphaIntake, "pf_capex", `"cx-sm"`); code != 200 || m["pf_number"] != "1004-1" {
		t.Fatalf("first project: %d %v", code, m)
	}

	// A project without a Capex has no number.
	_, plainIntake := project("Plain", `{"pf_intake":true}`)
	if _, m := set(plainIntake, "pf_status", `"open"`); m["pf_number"] != nil {
		t.Fatalf("project without Capex got %v", m["pf_number"])
	}

	// Projects created at the same time get different numbers.
	intakes := make([]string, 5)
	for i := range intakes {
		_, intakes[i] = project(fmt.Sprintf("Parallel %d", i), `{"pf_intake":true}`)
	}
	var wg sync.WaitGroup
	for _, issue := range intakes {
		wg.Add(1)
		go func(issue string) { defer wg.Done(); set(issue, "pf_capex", `"cx-sm"`) }(issue)
	}
	wg.Wait()
	var got []string
	for _, issue := range intakes {
		got = append(got, fmt.Sprint(stored(issue)["pf_number"]))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "1004-2,1004-3,1004-4,1004-5,1004-6" {
		t.Fatalf("concurrent numbers %v", got)
	}

	// The number stays when the project changes or loses its Capex.
	if _, m := set(alphaIntake, "pf_capex", `"cx-it"`); m["pf_number"] != "1004-1" {
		t.Fatalf("changed Capex renumbered: %v", m["pf_number"])
	}
	w := httptest.NewRecorder()
	testHandler.DeleteIssueMetadataKey(w, withURLParams(newRequest("DELETE", "/", nil), "id", alphaIntake, "key", "pf_capex"))
	if w.Code != 200 || stored(alphaIntake)["pf_number"] != "1004-1" {
		t.Fatalf("removed Capex lost the number: %d %v", w.Code, stored(alphaIntake))
	}
	set(alphaIntake, "pf_capex", `"cx-it"`)

	// A deleted project's number is not handed out again.
	var last string
	dbfx.QueryRow(t, `SELECT project_id FROM issue WHERE metadata->>'pf_number' = '1004-6'`).Scan(&last)
	dbfx.Exec(t, `DELETE FROM issue WHERE project_id = $1`, last)
	dbfx.Exec(t, `DELETE FROM project WHERE id = $1`, last)
	_, nextIntake := project("After delete", `{"pf_intake":true}`)
	if _, m := set(nextIntake, "pf_capex", `"cx-sm"`); m["pf_number"] != "1004-7" {
		t.Fatalf("after a deleted project: %v", m["pf_number"])
	}
	if reg := stored(register); reg["capex_number_last.1004"] != float64(7) || reg["capex_number_last.1000"] != nil {
		t.Fatalf("register counters %v", reg)
	}

	// A project that gets a Capex later takes the next number of that series.
	if _, m := set(plainIntake, "pf_capex", `"cx-it"`); m["pf_number"] != "1000-1" {
		t.Fatalf("later Capex: %v", m["pf_number"])
	}
	// So does an issue that becomes an intake after its Capex was set.
	_, lateIntake := project("Late intake", `{"pf_capex":"cx-it"}`)
	if _, m := set(lateIntake, "pf_intake", `true`); m["pf_number"] != "1000-2" {
		t.Fatalf("late intake: %v", m["pf_number"])
	}
	// A Capex that is not in the register gives no number, but the field is saved.
	_, unknownIntake := project("Unknown Capex", `{"pf_intake":true}`)
	if code, m := set(unknownIntake, "pf_capex", `"cx-none"`); code != 200 || m["pf_capex"] != "cx-none" || m["pf_number"] != nil {
		t.Fatalf("unknown Capex: %d %v", code, m)
	}

	// Only the server writes numbers and counters.
	for _, tc := range [][2]string{{plainIntake, "pf_number"}, {register, "capex_number_last.1004"}} {
		if code, _ := set(tc[0], tc[1], `"1004-99"`); code != http.StatusBadRequest {
			t.Errorf("set %s: %d", tc[1], code)
		}
		w := httptest.NewRecorder()
		testHandler.DeleteIssueMetadataKey(w, withURLParams(newRequest("DELETE", "/", nil), "id", tc[0], "key", tc[1]))
		if w.Code != http.StatusBadRequest {
			t.Errorf("delete %s: %d", tc[1], w.Code)
		}
	}
	if stored(plainIntake)["pf_number"] != "1000-1" || stored(register)["capex_number_last.1004"] != float64(7) {
		t.Fatal("a refused write changed a number")
	}

	// The MCP tools take a project number wherever they take a project UUID.
	storage := dbfx.Project(t, matasUsageProject)
	runtime := dbfx.Runtime(t, "Project number fixture")
	dbfx.Agent(t, matasKnowledgeAgent, runtime)
	dbfx.Agent(t, matasOrchestratorAgent, runtime)
	for _, p := range []string{alpha, storage} {
		dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id IN (SELECT id FROM issue WHERE project_id = $1)`, p)
		dbfx.Cleanup(t, `DELETE FROM issue WHERE project_id = $1`, p)
	}
	ok := func(result map[string]any) map[string]any {
		t.Helper()
		if result["isError"] == true {
			t.Fatalf("call failed: %v", result["content"])
		}
		return result["structuredContent"].(map[string]any)
	}
	issueOf := func(receipt map[string]any) (project, title, description string) {
		t.Helper()
		dbfx.QueryRow(t, `SELECT project_id::text, title, description FROM issue WHERE id = $1`, receipt["id"]).Scan(&project, &title, &description)
		return
	}
	today := matasUsageToday(time.Now())
	usage := ok(incomingCall(t, "multica_report_usage", map[string]any{"request_id": uuid.NewString(), "source": "Number app", "project_id": "1004-1",
		"days": []any{map[string]any{"date": today, "tokens": 10, "usd": 0.1}}}))
	if target := usage["target"].(map[string]any); target["project_id"] != alpha || target["number"] != "1004-1" {
		t.Fatalf("usage target %v", target)
	}
	if pid, _, text := issueOf(ok(incomingCall(t, "multica_ask_project", map[string]any{"request_id": uuid.NewString(), "question": "Hvad er status?", "project_id": "1004-1"}))); pid != alpha || !strings.Contains(text, "«[1004-1] Alpha»") {
		t.Fatalf("ask: %s %q", pid, text)
	}
	if pid, title, _ := issueOf(ok(incomingCall(t, "multica_add_project_update", map[string]any{"request_id": uuid.NewString(), "project_id": " 1004-1 ", "speaker": "Anna", "text": "Klar til test."}))); pid != alpha || !strings.HasPrefix(title, "Opdatering: [1004-1] Alpha — Anna") {
		t.Fatalf("update: %s %q", pid, title)
	}
	for ref, want := range map[string]string{"1004-99": "no project has project number 1004-99", "Alpha": "project UUID or a project number"} {
		result := incomingCall(t, "multica_add_project_update", map[string]any{"request_id": uuid.NewString(), "project_id": ref, "speaker": "Anna", "text": "x"})
		if result["isError"] != true || !strings.Contains(fmt.Sprint(result["content"]), want) {
			t.Errorf("%s: %v", ref, result["content"])
		}
	}
	listed := ok(incomingCall(t, "multica_list_projects", map[string]any{}))["projects"].([]any)
	numbers := map[string]any{}
	for _, p := range listed {
		item := p.(map[string]any)
		numbers[item["id"].(string)] = item["number"]
	}
	if numbers[alpha] != "1004-1" || numbers[storage] != nil {
		t.Fatalf("listed numbers %v", numbers)
	}
}
