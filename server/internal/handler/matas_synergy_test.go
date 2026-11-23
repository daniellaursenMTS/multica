package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMatasIdeaTerms(t *testing.T) {
	if got := strings.Join(matasIdeaTerms("Kan vi hoste Multica på VibeHub, og hoste det for alle?"), " "); got != "hoste multica vibehub" {
		t.Errorf("terms %q", got)
	}
	for n, want := range map[int]int{0: 1, 1: 1, 4: 1, 5: 2, 8: 2, 9: 3, 30: 3} {
		if got := matasSynergyNeed(n); got != want {
			t.Errorf("need(%d) = %d, want %d", n, got, want)
		}
	}
	scope := []matasMemoryProject{{ID: "a", Number: "1000-3"}, {ID: "b", Number: "1000-30"}, {ID: "c"}}
	if p, ok := matasProjectInText("Hvad er besluttet i 1000-3?", scope); !ok || p.ID != "a" {
		t.Errorf("1000-3: %v %v", p.ID, ok)
	}
	if _, ok := matasProjectInText("Noget om 1000-31 og 21000-3", scope); ok {
		t.Error("a longer number is not the project")
	}
	if _, ok := matasProjectInText("1000-3 og 1000-30", scope); ok {
		t.Error("two projects named is no single project")
	}
}

func TestIncomingMCPSynergyCheck(t *testing.T) {
	f := newMemoryFixture(t)
	tag := f.tag
	po, poEmail := f.member("Petra")
	numA, numC := "SA"+tag+"-1", "SC"+tag+"-1"
	petra := map[string]any{"po": map[string]any{"person": "Petra", "email": poEmail}}
	others := map[string]any{"po": map[string]any{"person": "Ina"}}
	ctx, _ := json.Marshal(matasContext{Goals: []string{"Multica kører stabilt for hele porteføljen."}, DecisionRights: map[string]string{"business": "Bent (BO)"},
		Terms: []matasTerm{{Term: "Knowledge Hub", Meaning: "Agenten, der svarer ud fra projekternes hubs."}}})
	a := f.project("Alfa", numA, petra, memoryPosts(), map[string]any{matasContextKey: string(ctx)})
	secret := []matasMemoryPost{{ID: numC + "/B-001", Type: "decision", Text: "Hemmelig plan: VibeHub hosting af Multica.", SaidAt: "2026-11-20", Source: "MATA-9", Status: "decision"}}
	c := f.project("Gamma", numC, others, secret, nil)
	f.issue(a, "Flyt Multica til VibeHub "+tag, "done")
	f.issue(a, "Overvågning af Multica på VibeHub "+tag, "in_progress")
	f.issue(c, "Gammas hemmelige VibeHub-migrering "+tag, "todo")
	f.issue("", "VibeHub uden projekt "+tag, "todo")

	// An idea like something done before: each part has its items, the superseded decision and the
	// proposal are discarded, and the project not shared is named with whom to ask, nothing more.
	got := f.call(po, "multica_synergy_check", map[string]any{"idea": "Kan vi køre Multica på VibeHub? " + tag, "project_id": numA})
	texts := func(part string) string {
		out := []string{}
		for _, x := range got[part].([]any) {
			out = append(out, x.(map[string]any)["text"].(string))
		}
		return strings.Join(out, " | ")
	}
	if d := texts("done_before"); !strings.Contains(d, "Flyt Multica til VibeHub") || strings.Contains(d, "uden projekt") {
		t.Fatalf("done_before %q", d)
	}
	if p := texts("in_progress"); !strings.Contains(p, "Overvågning af Multica") || strings.Contains(p, "Gamma") {
		t.Fatalf("in_progress %q", p)
	}
	if d := texts("decided"); !strings.Contains(d, "VibeHub i stedet for Azure") || strings.Contains(d, "Hemmelig") {
		t.Fatalf("decided %q", d)
	}
	if rel := got["related_projects"].([]any); len(rel) != 1 {
		t.Fatalf("related %v", rel)
	}
	tr := trailOf(t, got)
	reasons := map[string]string{}
	for _, d := range tr.Discarded {
		if d.Reason == matasDiscardNotShared && !strings.Contains(d.Project["title"].(string), tag) {
			continue // a project of another test in the shared workspace
		}
		reasons[d.ID] = d.Reason
		if d.Reason == matasDiscardNotShared && (d.ID != "" || d.Matching != 2 || d.Project["number"] != numC) {
			t.Fatalf("not shared %+v", d)
		}
	}
	if reasons["1000-9/B-001"] != matasDiscardSuperseded || reasons[""] != matasDiscardNotShared {
		t.Fatalf("discarded %+v", tr.Discarded)
	}
	if tr.InterpretedAs["project"].(map[string]any)["number"] != numA || *tr.Reviewed.Issues < 2 || tr.Reviewed.Posts != len(memoryPosts()) ||
		len(tr.Used) < 4 || tr.Confidence != "medium" || got["confidence"] != "medium" || !strings.Contains(got["recommendation"].(string), "withheld") {
		t.Fatalf("trail %+v", tr)
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), "Hemmelig") || strings.Contains(string(b), "hemmelige") || strings.Contains(string(b), numC+"/") ||
		strings.Contains(string(b), "uden projekt") || !strings.Contains(string(b), "Product Owner: Ina") {
		t.Fatalf("leaks the project not shared or an issue without project: %s", b)
	}

	// A context term the idea names counts as one term.
	got = f.call(po, "multica_synergy_check", map[string]any{"idea": "Et nyt overblik til knowledge hub " + tag})
	if ct := got["interpreted_as"].(map[string]any)["context_terms"].([]any); len(ct) != 1 || ct[0] != "knowledge hub" {
		t.Fatalf("context terms %v", ct)
	}

	// An idea with nothing like it: empty parts, low confidence and whom to ask.
	got = f.call(po, "multica_synergy_check", map[string]any{"idea": "Kvantecomputer til lagerstyring zq" + tag})
	tr = trailOf(t, got)
	for _, part := range []string{"done_before", "in_progress", "decided", "withheld"} {
		if len(got[part].([]any)) != 0 {
			t.Fatalf("%s %v", part, got[part])
		}
	}
	if len(tr.Used) != 0 || tr.Confidence != "low" || len(tr.Missing) != 1 || !strings.Contains(tr.Recommendation, "Nothing similar") {
		t.Fatalf("empty trail %+v", tr)
	}
}
