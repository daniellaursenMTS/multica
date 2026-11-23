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

func TestMatasHolderFor(t *testing.T) {
	meta := func(roles map[string]any, rights map[string]string, editors map[string][]string) map[string]json.RawMessage {
		spec, _ := json.Marshal(map[string]any{"roles": roles})
		ctx, _ := json.Marshal(map[string]any{"goals": []string{"x"}, "decision_rights": rights})
		ed, _ := json.Marshal(editors)
		m := map[string]json.RawMessage{}
		for k, v := range map[string]string{"dia_spec": string(spec), "ctx_spec": string(ctx), "ctx_editors": string(ed)} {
			m[k], _ = json.Marshal(v)
		}
		return m
	}
	roles := map[string]any{
		"bo":  map[string]any{"person": "Bent Ørsø", "email": "bent@matas.test"},
		"po":  map[string]any{"person": "Petra Holm"},
		"plo": map[string]any{"person": "Daniel Laursen"},
		"to":  map[string]any{"person": "Daniel Laursen"},
	}
	editors := map[string][]string{"po": {"PETRA@matas.test"}, "plo": {"daniel@matas.test"}, "to": {"daniel@matas.test"}}
	for _, tc := range []struct {
		area   string
		rights map[string]string
		role   string
		emails string
		reason string
	}{
		// The diamond role decides when the context names nobody.
		{"business", nil, "bo", "bent@matas.test", ""},
		{"product", nil, "po", "petra@matas.test", ""},
		// The context names someone outside the diamond: nobody can be linked (1000-3, B-004).
		{"business", map[string]string{"business": "Kristian Rossing"}, "", "", "Kristian Rossing er ikke koblet til en Multica-bruger."},
		// Named by full name, by first name, or by the role's name; non-ASCII names too.
		{"business", map[string]string{"business": "Bent Ørsø"}, "bo", "bent@matas.test", ""},
		{"technical", map[string]string{"technical": "Daniel"}, "to", "daniel@matas.test", ""},
		{"product", map[string]string{"product": "Product Owner (Petra)"}, "po", "petra@matas.test", ""},
		{"business", map[string]string{"business": "Daniel Laursen"}, "plo", "daniel@matas.test", ""},
		// A name that only contains another name is not that person.
		{"business", map[string]string{"business": "Bentsen"}, "", "", "Bentsen er ikke koblet til en Multica-bruger."},
		// Two people match: nobody.
		{"product", map[string]string{"product": "Petra og Bent"}, "", "", "«Petra og Bent» passer på flere personer i diamanten (Bent Ørsø, Petra Holm)."},
		// Steering committee only when the context names someone.
		{"steerco", nil, "", "", "Projektkonteksten udpeger ingen, der afgør for styregruppen."},
	} {
		h := matasHolderFor(tc.area, meta(roles, tc.rights, editors))
		if h.Role != tc.role || strings.Join(h.Emails, " ") != tc.emails || h.Reason != tc.reason {
			t.Errorf("%s %v: role %q emails %v reason %q", tc.area, tc.rights, h.Role, h.Emails, h.Reason)
		}
	}
	// A covering role gives no decision right.
	if h := matasHolderFor("business", meta(map[string]any{"bo": map[string]any{"person": nil}, "plo": map[string]any{"person": "Daniel"}}, nil, nil)); !strings.Contains(h.Reason, "Business Owner") {
		t.Errorf("covered role: %+v", h)
	}
	if h := matasHolderFor("business", map[string]json.RawMessage{}); h.Reason == "" {
		t.Errorf("no diamond: %+v", h)
	}
}

func TestMatasConfirmDecision(t *testing.T) {
	tag := uuid.NewString()[:8]
	counter := func() int {
		var n int
		dbfx.QueryRow(t, `UPDATE workspace SET issue_counter = issue_counter + 1 WHERE id = $1 RETURNING issue_counter`, testWorkspaceID).Scan(&n)
		return n
	}
	person := func(name, role string) (string, string) {
		email := fmt.Sprintf("conf-%s-%s@matas.test", strings.ToLower(name), tag)
		id := dbfx.User(t, name, email)
		dbfx.Member(t, testWorkspaceID, id, role)
		return id, email
	}
	bo, boEmail := person("Bent", "member")  // business owner, e-mail on the role
	po, poEmail := person("Petra", "member") // product owner, linked by the roster (ctx_editors)
	admin, _ := person("Alma", "admin")      // a workspace admin without the right
	outsider, _ := person("Otto", "member")
	spec, _ := json.Marshal(map[string]any{"slug": "conf-" + tag, "name": "Bekræft " + tag, "roles": map[string]any{
		"bo": map[string]any{"person": "Bent", "email": boEmail}, "po": map[string]any{"person": "Petra"},
		"plo": map[string]any{"person": nil}, "to": map[string]any{"person": nil}}})
	ctx, _ := json.Marshal(map[string]any{"goals": []string{"x"}, "decision_rights": map[string]string{"platform": "Kristian Rossing"}})
	editors, _ := json.Marshal(map[string][]string{"po": {poEmail}})
	number := "CF" + tag + "-1"
	meta, _ := json.Marshal(map[string]any{"pf_intake": true, "dia_spec": string(spec), "ctx_spec": string(ctx), "ctx_editors": string(editors), "pf_number": number})
	project := dbfx.Project(t, "Bekræft "+tag)
	intake := dbfx.Issue(t, "Intake — Bekræft", testutil.Cols{"status": "done", "project_id": project, "number": counter(), "metadata": string(meta)})
	review := dbfx.Issue(t, "Afklar hukommelse: "+number+" B-004", testutil.Cols{"status": "in_review", "project_id": project, "number": counter(),
		"metadata": fmt.Sprintf(`{"mem_review": "%s/B-004"}`, number)})
	dbfx.Cleanup(t, `DELETE FROM issue WHERE project_id = $1`, project)

	proposal := func(id, area string, rev int) matasMemoryPost {
		return matasMemoryPost{ID: number + "/" + id, Type: "decision", Area: area, Text: "Forslag " + id + ".", SaidAt: "2026-11-20",
			Source: "MATA-1", Status: "proposal", Rev: rev, Basis: "taleren har ikke retten"}
	}
	dir := t.TempDir()
	t.Setenv(matasMemoryDirEnv, dir)
	posts := []matasMemoryPost{
		proposal("B-001", "business", 1),
		{ID: number + "/B-002", Type: "decision", Area: "business", Text: "En beslutning.", SaidAt: "2026-11-20", Source: "MATA-1", Status: "decision", Rev: 1},
		proposal("B-003", "platform", 1),
		proposal("B-004", "product", 2),
	}
	view, _ := json.Marshal(matasMemoryView{Slug: "conf-" + tag, Number: number, Name: "Bekræft", MulticaProject: project, Posts: posts})
	if err := os.WriteFile(filepath.Join(dir, "conf.json"), view, 0o644); err != nil {
		t.Fatal(err)
	}

	confirm := func(user, post string, rev int, action string, headers ...string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParams(newRequestAs(user, "POST", "/api/projects/"+project+"/decision-log/confirm",
			map[string]any{"post": post, "rev": rev, "action": action}), "id", project)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		testHandler.ConfirmProjectDecision(w, req)
		return w.Code, w.Body.String()
	}
	stored := func(issue, key string) matasMemConfirm {
		t.Helper()
		var raw string
		dbfx.QueryRow(t, `SELECT coalesce(metadata->>$2, '') FROM issue WHERE id = $1`, issue, key).Scan(&raw)
		var c matasMemConfirm
		_ = json.Unmarshal([]byte(raw), &c)
		return c
	}
	agent := []string{"X-Actor-Source", "task_token", "X-Agent-ID", uuid.NewString()}

	for _, tc := range []struct {
		who     string
		user    string
		post    string
		rev     int
		headers []string
		want    int
		says    string
	}{
		{"agent with the holder's token", bo, "B-001", 1, agent, 403, "agents cannot"},
		{"workspace owner without the right", testUserID, "B-001", 1, nil, 403, "only Bent (Business Owner)"},
		{"admin without the right", admin, "B-001", 1, nil, 403, "only Bent"},
		{"product owner on a business proposal", po, "B-001", 1, nil, 403, "only Bent"},
		{"outsider", outsider, "B-001", 1, nil, 403, "only Bent"},
		{"holder on an old rev", bo, "B-001", 2, nil, 409, "rev 1, not 2"},
		{"holder on a decision", bo, "B-002", 1, nil, 409, "not a proposal"},
		{"a name not linked to a user", admin, "B-003", 1, nil, 403, "Kristian Rossing er ikke koblet til en Multica-bruger"},
		{"unknown post", bo, "B-099", 1, nil, 404, "no post"},
	} {
		if code, body := confirm(tc.user, tc.post, tc.rev, "confirm", tc.headers...); code != tc.want || !strings.Contains(body, tc.says) {
			t.Errorf("%s: %d %s", tc.who, code, body)
		}
	}
	if c := stored(intake, "mem_confirm.B-001"); c.Post != "" {
		t.Fatalf("a refused confirmation was stored: %+v", c)
	}

	// The holder confirms; without a review task it is stored on the intake.
	code, body := confirm(bo, number+"/B-001", 1, "confirm")
	if code != 200 {
		t.Fatalf("holder: %d %s", code, body)
	}
	if c := stored(intake, "mem_confirm.B-001"); c.Post != number+"/B-001" || c.Action != "confirm" || c.Rev != 1 || c.UserID != bo || c.Role != "bo" || c.Name != "Bent" || c.Email != boEmail || c.At == "" {
		t.Fatalf("stored %+v", c)
	}
	proposals := func(body []byte) map[string]matasProposalEntry {
		t.Helper()
		var log struct {
			Proposals []matasProposalEntry `json:"proposals"`
		}
		if err := json.Unmarshal(body, &log); err != nil {
			t.Fatal(err)
		}
		out := map[string]matasProposalEntry{}
		for _, p := range log.Proposals {
			out[matasLocalID(p.ID)] = p
		}
		return out
	}
	if p := proposals([]byte(body))["B-001"]; p.CanConfirm || p.Confirmation == nil || p.Confirmation.State != "pending" || p.Confirmation.Name != "Bent" {
		t.Fatalf("after confirming: %+v", p)
	}
	if code, body := confirm(bo, "B-001", 1, "decline"); code != 409 || !strings.Contains(body, "already answered") {
		t.Fatalf("second answer: %d %s", code, body)
	}

	// The product owner, linked by the roster, declines B-004: stored on its review task.
	if code, body := confirm(po, "B-004", 2, "decline"); code != 200 {
		t.Fatalf("decline: %d %s", code, body)
	}
	if c := stored(review, "mem_confirm.B-004"); c.Action != "decline" || c.Role != "po" || c.Rev != 2 {
		t.Fatalf("decline stored %+v", c)
	}
	if c := stored(intake, "mem_confirm.B-004"); c.Post != "" {
		t.Fatalf("decline also on the intake: %+v", c)
	}

	// Nobody writes or removes a confirmation through the metadata API, not even the owner.
	for _, method := range []string{"PUT", "DELETE"} {
		w := httptest.NewRecorder()
		req := withURLParams(newRequestAs(testUserID, method, "/api/issues/"+intake+"/metadata/mem_confirm.B-003", json.RawMessage(`{"value":"{}"}`)),
			"id", intake, "key", "mem_confirm.B-003")
		if method == "PUT" {
			testHandler.SetIssueMetadataKey(w, req)
		} else {
			testHandler.DeleteIssueMetadataKey(w, req)
		}
		if w.Code != 400 {
			t.Errorf("%s mem_confirm: %d", method, w.Code)
		}
	}

	// The tab: who holds the right, whether this member may answer, and why nobody can.
	w := httptest.NewRecorder()
	testHandler.GetProjectDecisionLog(w, withURLParams(newRequestAs(bo, "GET", "/api/projects/"+project+"/decision-log", nil), "id", project))
	byID := proposals(w.Body.Bytes())
	if p := byID["B-003"]; p.CanConfirm || p.Holder.Linked || p.Holder.Reason != "Kristian Rossing er ikke koblet til en Multica-bruger." {
		t.Errorf("B-003: %+v", p.Holder)
	}
	if p := byID["B-004"]; p.CanConfirm || p.Holder.Name != "Petra" || p.Confirmation == nil || p.Confirmation.Action != "decline" {
		t.Errorf("B-004 for Bent: %+v", p)
	}
	if strings.Contains(w.Body.String(), boEmail) {
		t.Errorf("the log shows e-mails: %s", w.Body.String())
	}

	// A confirmation on an older rev no longer counts, and the holder may answer again.
	posts[0].Rev = 2
	view, _ = json.Marshal(matasMemoryView{Slug: "conf-" + tag, Number: number, Name: "Bekræft", MulticaProject: project, Posts: posts})
	_ = os.WriteFile(filepath.Join(dir, "conf.json"), view, 0o644)
	w = httptest.NewRecorder()
	testHandler.GetProjectDecisionLog(w, withURLParams(newRequestAs(bo, "GET", "/api/projects/"+project+"/decision-log", nil), "id", project))
	if p := proposals(w.Body.Bytes())["B-001"]; !p.CanConfirm || p.Confirmation == nil || p.Confirmation.State != "stale" {
		t.Fatalf("stale: %+v", p)
	}
	if code, body := confirm(bo, "B-001", 2, "confirm"); code != 200 || stored(intake, "mem_confirm.B-001").Rev != 2 {
		t.Fatalf("again on rev 2: %d %s", code, body)
	}
}
