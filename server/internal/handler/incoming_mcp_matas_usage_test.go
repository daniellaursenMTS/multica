package handler

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func usageDays(pairs ...any) []matasUsageDay {
	var days []matasUsageDay
	for i := 0; i < len(pairs); i += 3 {
		tokens, usd := pairs[i+1].(float64), pairs[i+2].(float64)
		days = append(days, matasUsageDay{Date: pairs[i].(string), Tokens: &tokens, USD: &usd})
	}
	return days
}

func TestMatasMergeUsage(t *testing.T) {
	today := "2026-11-02"
	first, err := matasMergeUsage(nil, usageDays("2026-11-01", 100.0, 1.5, "2026-08-10", 50.0, 0.5), today)
	if err != nil {
		t.Fatal(err)
	}
	if first.DailyFrom != "2026-09-19" || first.Through != today {
		t.Fatalf("window %s–%s", first.DailyFrom, first.Through)
	}
	// A day older than the window still counts in its month, as compactUsage does.
	if _, kept := first.Days["2026-08-10"]; kept || first.Months["2026-08"] != [2]float64{50, 0.5} {
		t.Fatalf("old day: %v %v", first.Days, first.Months)
	}
	// The same day again replaces it; a new day is added.
	second, err := matasMergeUsage(&first, usageDays("2026-11-01", 40.0, 0.25, "2026-11-02", 10.0, 0.1), today)
	if err != nil {
		t.Fatal(err)
	}
	if second.Months["2026-11"] != [2]float64{50, 0.35} || second.Days["2026-11-01"] != [2]float64{40, 0.25} {
		t.Fatalf("replace: %v %v", second.Months, second.Days)
	}
	if first.Months["2026-11"] != [2]float64{100, 1.5} {
		t.Fatal("merge changed the stored entry")
	}
	// A stored month's day before the window is only known as part of the total.
	if _, err := matasMergeUsage(&second, usageDays("2026-08-11", 1.0, 0.0), today); err == nil || !strings.Contains(err.Error(), "2026-08-11") {
		t.Fatalf("replaced a day hidden in a month total: %v", err)
	}
	// An old month with nothing stored can still be filled in.
	third, err := matasMergeUsage(&second, usageDays("2026-07-03", 7.0, 0.07), today)
	if err != nil || third.Months["2026-07"] != [2]float64{7, 0.07} {
		t.Fatalf("backfill: %v %v", err, third.Months)
	}
	// Later, the window moves on and days fall out of the detail but stay in the months.
	later, err := matasMergeUsage(&third, usageDays("2026-12-20", 1.0, 0.01), "2026-12-20")
	if err != nil || len(later.Days) != 1 || later.Months["2026-11"] != [2]float64{50, 0.35} {
		t.Fatalf("window move: %v %v %v", err, later.Days, later.Months)
	}
}

func TestMatasUsageEntryEncodesLikeTheImporters(t *testing.T) {
	raw := `{"v":1,"id":"app-x","label":"X & <Y>","capex_id":null,"months":{},"days":{},"daily_from":"2026-09-19","through":"2026-11-02"}`
	var e matasUsageEntry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatal(err)
	}
	out, _ := e.encode()
	if !strings.Contains(string(out), `"capex_id":null`) || strings.Contains(string(out), "project_id") || !strings.Contains(string(out), "X & <Y>") {
		t.Fatalf("unallocated entry became %s", out)
	}
}

func TestMatasUsageReportValidation(t *testing.T) {
	today := "2026-11-02"
	valid := func() matasUsageReport {
		return matasUsageReport{RequestID: uuid.NewString(), Source: " ChatGPT –  Marketing GPT ", CapexID: "c1", Days: usageDays("2026-11-01", 1.0, 0.1)}
	}
	p := valid()
	if err := p.validate(today); err != nil || p.Source != "ChatGPT – Marketing GPT" {
		t.Fatalf("valid report: %v %q", err, p.Source)
	}
	many := make([]any, 0, 3*367)
	start, _ := time.Parse("2006-01-02", "2025-10-01")
	for i := 0; i < 367; i++ {
		many = append(many, start.AddDate(0, 0, i).Format("2006-01-02"), 1.0, 0.0)
	}
	for name, change := range map[string]func(*matasUsageReport){
		"request id":    func(p *matasUsageReport) { p.RequestID = "x" },
		"no source":     func(p *matasUsageReport) { p.Source = "  " },
		"both targets":  func(p *matasUsageReport) { p.ProjectID = uuid.NewString() },
		"no target":     func(p *matasUsageReport) { p.CapexID = "" },
		"no days":       func(p *matasUsageReport) { p.Days = nil },
		"too many days": func(p *matasUsageReport) { p.Days = usageDays(many...) },
		"future":        func(p *matasUsageReport) { p.Days = usageDays("2026-11-03", 1.0, 0.1) },
		"not a day":     func(p *matasUsageReport) { p.Days = usageDays("2026-02-30", 1.0, 0.1) },
		"twice":         func(p *matasUsageReport) { p.Days = usageDays("2026-11-01", 1.0, 0.1, "2026-11-01", 2.0, 0.2) },
		"negative":      func(p *matasUsageReport) { p.Days = usageDays("2026-11-01", -1.0, 0.1) },
		"negative usd":  func(p *matasUsageReport) { p.Days = usageDays("2026-11-01", 1.0, -0.1) },
		"missing usd":   func(p *matasUsageReport) { p.Days[0].USD = nil },
	} {
		p := valid()
		change(&p)
		if p.validate(today) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestIncomingMCPReportUsage(t *testing.T) {
	storage := dbfx.Project(t, matasUsageProject)
	target := dbfx.Project(t, "Usage target project")
	// Take the register's number from the workspace counter, which the issue service uses too.
	var number int
	dbfx.QueryRow(t, `UPDATE workspace SET issue_counter = issue_counter + 1 WHERE id = $1 RETURNING issue_counter`, testWorkspaceID).Scan(&number)
	dbfx.Issue(t, "Capex-register", testutil.Cols{"status": "done",
		"number": number, "metadata": `{"capex_register":true,"capex_catalog":"{\"items\":[{\"id\":\"cx-a\",\"number\":\"1005\",\"description\":\"Analytics\",\"active\":true},{\"id\":\"cx-b\",\"number\":\"1045\",\"description\":\"Other\",\"active\":true},{\"id\":\"cx-old\",\"number\":\"0999\",\"description\":\"Closed\",\"active\":false}],\"usd_dkk\":6.9}"}`})
	dbfx.Cleanup(t, `DELETE FROM issue WHERE workspace_id = $1 AND title LIKE 'Capex-forbrug · app-%'`, testWorkspaceID)
	today := matasUsageToday(time.Now())
	day := func(back int) string {
		d, _ := time.Parse("2006-01-02", today)
		return d.AddDate(0, 0, -back).Format("2006-01-02")
	}
	report := func(args map[string]any) map[string]any {
		t.Helper()
		args["request_id"] = uuid.NewString()
		return incomingCall(t, "multica_report_usage", args)
	}
	receipt := func(result map[string]any) map[string]any {
		t.Helper()
		if result["isError"] == true {
			t.Fatalf("report failed: %v", result["content"])
		}
		return result["structuredContent"].(map[string]any)
	}
	stored := func(issueID string) (map[string]any, matasUsageEntry) {
		t.Helper()
		var raw []byte
		dbfx.QueryRow(t, `SELECT metadata FROM issue WHERE id = $1`, issueID).Scan(&raw)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		var e matasUsageEntry
		if err := json.Unmarshal([]byte(m["capex_usage_data"].(string)), &e); err != nil {
			t.Fatal(err)
		}
		return m, e
	}

	for name, args := range map[string]map[string]any{
		"unknown capex":   {"source": "App", "capex_id": "cx-none", "days": []any{map[string]any{"date": today, "tokens": 1, "usd": 1}}},
		"inactive capex":  {"source": "App", "capex_id": "cx-old", "days": []any{map[string]any{"date": today, "tokens": 1, "usd": 1}}},
		"capex number":    {"source": "App", "capex_id": "1005", "days": []any{map[string]any{"date": today, "tokens": 1, "usd": 1}}},
		"unknown project": {"source": "App", "project_id": uuid.NewString(), "days": []any{map[string]any{"date": today, "tokens": 1, "usd": 1}}},
		"foreign field":   {"source": "App", "capex_id": "cx-a", "token": "secret", "days": []any{map[string]any{"date": today, "tokens": 1, "usd": 1}}},
	} {
		if report(args)["isError"] != true {
			t.Errorf("%s: accepted", name)
		}
	}
	if text := report(map[string]any{"source": "App", "capex_id": "1005", "days": []any{map[string]any{"date": today, "tokens": 1, "usd": 1}}})["content"]; !strings.Contains(fmt.Sprint(text), "cx-a (1005 Analytics)") || strings.Contains(fmt.Sprint(text), "cx-old") {
		t.Fatalf("refusal does not list the active Capex ids: %v", text)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id = $1 AND title LIKE 'Capex-forbrug · app-%'`, testWorkspaceID); n != 0 {
		t.Fatalf("a refused report stored %d posts", n)
	}

	// Concurrent first reports make one post.
	results := make([]map[string]any, 4)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = report(map[string]any{"source": "ChatGPT – Marketing GPT", "capex_id": "cx-a", "days": []any{map[string]any{"date": day(1), "tokens": 1000, "usd": 2.5}}})
		}(i)
	}
	wg.Wait()
	first := receipt(results[0])
	for _, r := range results[1:] {
		if receipt(r)["issue_id"] != first["issue_id"] {
			t.Fatal("concurrent reports made two posts")
		}
	}
	issueID := first["issue_id"].(string)
	meta, entry := stored(issueID)
	if entry.Label != "ChatGPT – Marketing GPT" || entry.CapexID == nil || *entry.CapexID != "cx-a" || entry.Days[day(1)] != [2]float64{1000, 2.5} {
		t.Fatalf("stored %+v", entry)
	}
	if meta["capex_usage"] != true || meta["capex_usage_id"] != entry.ID || meta["capex_usage_reporter"] != testUserID || meta["capex_usage_reported_at"] == nil {
		t.Fatalf("metadata %v", meta)
	}
	if !strings.HasPrefix(entry.ID, "app-") || dbfx.Count(t, `SELECT count(*) FROM issue WHERE id = $1 AND project_id = $2 AND status = 'done'`, issueID, storage) != 1 {
		t.Fatal("post is not a completed issue in Multica-opsætning")
	}

	// The same day again replaces it; a new day is added.
	second := receipt(report(map[string]any{"source": "ChatGPT – Marketing GPT", "capex_id": "cx-a", "days": []any{
		map[string]any{"date": day(1), "tokens": 400, "usd": 1}, map[string]any{"date": day(0), "tokens": 100, "usd": 0.5}}}))
	if second["issue_id"] != issueID || second["created"] != false {
		t.Fatal("second report made a new post")
	}
	if totals := second["totals"].(map[string]any); totals["tokens"] != 500.0 || totals["usd"] != 1.5 {
		t.Fatalf("totals %v, want 500 tokens and 1.5 USD", totals)
	}

	// A person renames the post and moves it to another Capex; the app's next report keeps that.
	_, entry = stored(issueID)
	entry.Label, entry.CapexID = "Marketing GPT", ptr("cx-b")
	moved, _ := entry.encode()
	dbfx.Exec(t, `UPDATE issue SET metadata = jsonb_set(metadata, '{capex_usage_data}', to_jsonb($2::text)) WHERE id = $1`, issueID, string(moved))
	third := receipt(report(map[string]any{"source": "ChatGPT – Marketing GPT", "capex_id": "cx-a", "label": "Ny", "days": []any{map[string]any{"date": day(0), "tokens": 300, "usd": 0.75}}}))
	_, entry = stored(issueID)
	if third["issue_id"] != issueID || entry.Label != "Marketing GPT" || *entry.CapexID != "cx-b" || third["note"] == nil {
		t.Fatalf("lost a person's change: %+v %v", entry, third)
	}
	if target := third["target"].(map[string]any); target["number"] != "1045" {
		t.Fatalf("receipt target %v", target)
	}

	// Another target is another post; a project target names the project.
	other := receipt(report(map[string]any{"source": "ChatGPT – Marketing GPT", "project_id": target, "days": []any{map[string]any{"date": day(2), "tokens": 5, "usd": 0}}}))
	if other["issue_id"] == issueID || other["target"].(map[string]any)["title"] != "Usage target project" {
		t.Fatalf("project post %v", other)
	}
}
