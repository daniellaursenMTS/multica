package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Usage that apps (ChatGPT, Claude, Codex …) report for the Capex page. A post is a completed
// issue in Multica-opsætning carrying the v1 payload that capex-usage.ts and the importers'
// usage_format.py read and write: whole-history month totals, the last 45 days in detail.
// One post per app and target; a person's rename or move on the Capex page is kept.

const (
	matasUsageProject  = "Multica-opsætning"
	matasUsageDays     = 44 // days before today kept in detail, as compactUsage and compact()
	matasUsageMaxBytes = 6000
	matasUsageMaxCall  = 366
	matasUsageMaxToken = 1e12
	matasUsageMaxUSD   = 1e6
)

type matasUsageDay struct {
	Date   string   `json:"date"`
	Tokens *float64 `json:"tokens"`
	USD    *float64 `json:"usd"`
}

type matasUsageReport struct {
	RequestID string          `json:"request_id"`
	Source    string          `json:"source"`
	CapexID   string          `json:"capex_id"`
	ProjectID string          `json:"project_id"`
	Label     string          `json:"label"`
	Days      []matasUsageDay `json:"days"`
}

// matasUsageEntry is the v1 payload. CapexID and ProjectID are pointers so an unallocated entry
// keeps its explicit null, as the importers write it.
type matasUsageEntry struct {
	V         int                   `json:"v"`
	ID        string                `json:"id"`
	Label     string                `json:"label"`
	Note      string                `json:"note,omitempty"`
	CapexID   *string               `json:"capex_id,omitempty"`
	ProjectID *string               `json:"project_id,omitempty"`
	Months    map[string][2]float64 `json:"months"`
	Days      map[string][2]float64 `json:"days"`
	DailyFrom string                `json:"daily_from"`
	Through   string                `json:"through"`
	capexNull bool
}

// encode writes the payload as capex-usage.ts and usage_format.py do: no HTML escaping.
func (e matasUsageEntry) encode() ([]byte, error) {
	type plain matasUsageEntry
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(plain(e)); err != nil {
		return nil, err
	}
	out := bytes.TrimSpace(buf.Bytes())
	if !e.capexNull || e.CapexID != nil || e.ProjectID != nil {
		return out, nil
	}
	return append(out[:len(out)-1], []byte(`,"capex_id":null}`)...), nil
}

func (e *matasUsageEntry) UnmarshalJSON(raw []byte) error {
	type plain matasUsageEntry
	var p plain
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	p.capexNull = p.CapexID == nil && p.ProjectID == nil && string(keys["capex_id"]) == "null"
	*e = matasUsageEntry(p)
	return nil
}

func matasUsageTool() any {
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	day := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"date", "tokens", "usd"}, "properties": map[string]any{
		"date":   str("Day of the usage, YYYY-MM-DD in Danish time; not in the future."),
		"tokens": map[string]any{"type": "number", "minimum": 0, "description": "Tokens that day: new input + output (cached input that is read again is not counted)."},
		"usd":    map[string]any{"type": "number", "minimum": 0, "description": "Cost that day in USD."},
	}}
	return map[string]any{
		"name":        "multica_report_usage",
		"description": "Report an app's token usage and cost per day to a Capex number or a project. It appears as a usage post on the Capex page. One post per source and target: a day reported again replaces that day, new days are added. A name or Capex a person set on the Capex page is kept.",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"request_id", "source", "days"}, "properties": map[string]any{
			"request_id": str("Stable UUID for this report; reuse it to retry."),
			"source":     str("The app's name, 1–80 characters, e.g. «ChatGPT – Marketing GPT». Keep it the same between reports."),
			"capex_id":   str("Stable id of an active Capex number (not the number itself). Give this or project_id."),
			"project_id": str("Project UUID or project number (e.g. 1004-2) from multica_list_projects. Give this or capex_id."),
			"label":      str("Optional name the post is shown with; default the source. Only names a new post."),
			"days":       map[string]any{"type": "array", "minItems": 1, "maxItems": matasUsageMaxCall, "items": day},
		}},
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}
}

func matasUsageToday(now time.Time) string {
	if loc, err := time.LoadLocation("Europe/Copenhagen"); err == nil {
		now = now.In(loc)
	}
	return now.Format("2006-01-02")
}

func matasUsageRound(v float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 8, 64), 64)
	return r
}

// validate trims the report and checks everything that needs no database.
func (p *matasUsageReport) validate(today string) error {
	if _, err := util.ParseUUID(p.RequestID); err != nil {
		return errors.New("request_id must be a UUID")
	}
	p.Source = strings.Join(strings.Fields(p.Source), " ")
	if p.Source == "" || len([]rune(p.Source)) > 80 {
		return errors.New("source must be the app's name, 1–80 characters")
	}
	p.Label = strings.Join(strings.Fields(p.Label), " ")
	if len([]rune(p.Label)) > 120 {
		return errors.New("label must be at most 120 characters")
	}
	p.CapexID, p.ProjectID = strings.TrimSpace(p.CapexID), strings.TrimSpace(p.ProjectID)
	if (p.CapexID == "") == (p.ProjectID == "") {
		return errors.New("give exactly one of capex_id and project_id")
	}
	if len(p.Days) == 0 || len(p.Days) > matasUsageMaxCall {
		return fmt.Errorf("days must hold 1–%d days", matasUsageMaxCall)
	}
	seen := map[string]bool{}
	for _, d := range p.Days {
		if !matasDateRE.MatchString(d.Date) {
			return fmt.Errorf("date %q must be YYYY-MM-DD", d.Date)
		}
		if _, err := time.Parse("2006-01-02", d.Date); err != nil {
			return fmt.Errorf("date %s is not a real day", d.Date)
		}
		if d.Date > today {
			return fmt.Errorf("date %s is in the future", d.Date)
		}
		if seen[d.Date] {
			return fmt.Errorf("date %s is given twice", d.Date)
		}
		seen[d.Date] = true
		if d.Tokens == nil || d.USD == nil {
			return fmt.Errorf("date %s needs tokens and usd", d.Date)
		}
		if *d.Tokens < 0 || *d.USD < 0 || *d.Tokens > matasUsageMaxToken || *d.USD > matasUsageMaxUSD {
			return fmt.Errorf("date %s: tokens and usd must be between 0 and %.0f / %.0f", d.Date, float64(matasUsageMaxToken), float64(matasUsageMaxUSD))
		}
	}
	return nil
}

// matasUsageID is the post's stable id for one source and the target it was reported to.
// It starts with "app-", never with an importer's prefix, so the Capex page treats it as manual.
func matasUsageID(source, kind, target string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(source) + "\n" + kind + ":" + target))
	return "app-" + hex.EncodeToString(sum[:8])
}

// matasMergeUsage applies reported days to the stored entry (nil for a new post). A reported day
// replaces that day and months keep including their days. A stored day older than daily_from
// lives on only inside its month's total, so it cannot be replaced; such a day is refused unless
// its month has nothing stored yet.
func matasMergeUsage(stored *matasUsageEntry, days []matasUsageDay, today string) (matasUsageEntry, error) {
	e := matasUsageEntry{V: 1, Months: map[string][2]float64{}, Days: map[string][2]float64{}}
	if stored != nil {
		e = *stored
		e.Months, e.Days = map[string][2]float64{}, map[string][2]float64{}
		for k, v := range stored.Months {
			e.Months[k] = v
		}
		for k, v := range stored.Days {
			e.Days[k] = v
		}
	}
	var locked []string
	for _, d := range days {
		month := d.Date[:7]
		if _, known := e.Months[month]; stored != nil && known && d.Date < stored.DailyFrom {
			locked = append(locked, d.Date)
			continue
		}
		old, value := e.Days[d.Date], [2]float64{*d.Tokens, *d.USD}
		m := e.Months[month]
		for i := range m {
			m[i] = math.Max(0, m[i]-old[i]+value[i])
		}
		e.Months[month], e.Days[d.Date] = m, value
	}
	if len(locked) > 0 {
		sort.Strings(locked)
		return e, fmt.Errorf("these days are older than the post's daily detail (from %s) and already part of a month total, so they can no longer be replaced: %s", stored.DailyFrom, strings.Join(locked, ", "))
	}
	cutoff, _ := time.Parse("2006-01-02", today)
	e.DailyFrom = cutoff.AddDate(0, 0, -matasUsageDays).Format("2006-01-02")
	e.Through = today
	if stored != nil && stored.Through > today {
		e.Through = stored.Through
	}
	for k, v := range e.Days {
		if k < e.DailyFrom {
			delete(e.Days, k)
		} else {
			e.Days[k] = [2]float64{matasUsageRound(v[0]), matasUsageRound(v[1])}
		}
	}
	for k, v := range e.Months {
		e.Months[k] = [2]float64{matasUsageRound(v[0]), matasUsageRound(v[1])}
	}
	return e, nil
}

// matasCapexItem is the active Capex with this stable id in the workspace's Capex register.
func (h *Handler) matasCapexItem(r *http.Request, ws pgtype.UUID, id string) (map[string]any, error) {
	_, items, err := matasCapexRegister(r.Context(), h.DB, ws)
	if err != nil {
		return nil, err
	}
	var active []string
	for _, item := range items {
		if item["active"] == false {
			continue
		}
		if item["id"] == id {
			return item, nil
		}
		active = append(active, fmt.Sprintf("%v (%v %v)", item["id"], item["number"], item["description"]))
	}
	return nil, fmt.Errorf("capex_id %q is not an active Capex. Use the stable id, not the number; active: %s", id, strings.Join(active, "; "))
}

func (h *Handler) matasUsageProject(r *http.Request, ws pgtype.UUID) (pgtype.UUID, error) {
	projects, err := h.Queries.ListProjects(r.Context(), db.ListProjectsParams{WorkspaceID: ws})
	if err != nil {
		return pgtype.UUID{}, errors.New("could not list projects")
	}
	var found []pgtype.UUID
	for _, p := range projects {
		if p.Title == matasUsageProject {
			found = append(found, p.ID)
		}
	}
	if len(found) != 1 {
		return pgtype.UUID{}, fmt.Errorf("the project %s, which holds usage posts, is not unique in this workspace", matasUsageProject)
	}
	return found[0], nil
}

// mcpIssueBroadcast renders a created issue for the issue:created event, as the REST API does.
func (h *Handler) mcpIssueBroadcast(r *http.Request) func(db.Issue, []db.Attachment, []db.IssueLabel) map[string]any {
	return func(issue db.Issue, atts []db.Attachment, labels []db.IssueLabel) map[string]any {
		payload := issueToResponse(issue, h.getIssuePrefix(r.Context(), issue.WorkspaceID))
		h.fillStatusCategory(r.Context(), issue.WorkspaceID, &payload)
		payload.Attachments = []AttachmentResponse{}
		labelResponses := labelsToResponse(labels)
		payload.Labels = &labelResponses
		return map[string]any{"issue": payload}
	}
}

func (h *Handler) mcpReportUsage(r *http.Request, raw json.RawMessage) (any, error) {
	var p matasUsageReport
	if err := decodeMCPArguments(raw, &p); err != nil {
		return nil, err
	}
	now := time.Now()
	today := matasUsageToday(now)
	if err := p.validate(today); err != nil {
		return nil, err
	}
	ws, member, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	kind, target := "capex", p.CapexID
	if p.ProjectID != "" {
		project, err := h.matasProject(r, ws, p.ProjectID)
		if err != nil {
			return nil, err
		}
		kind, target = "project", util.UUIDToString(project.ID)
	} else if _, err := h.matasCapexItem(r, ws, p.CapexID); err != nil {
		return nil, err
	}
	storage, err := h.matasUsageProject(r, ws)
	if err != nil {
		return nil, err
	}
	id := matasUsageID(p.Source, kind, target)
	title := "Capex-forbrug · " + id

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return nil, errors.New("Multica could not accept this; retry with the same request_id")
	}
	defer tx.Rollback(r.Context())
	// Serializes reports for the same post, including the create below on its own connection.
	if _, err := tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "capex_usage/"+util.UUIDToString(ws)+"/"+id); err != nil {
		return nil, errors.New("Multica could not accept this; retry with the same request_id")
	}
	// Like usage_store.upsert: find the post by its id, or an incomplete create by its title.
	byID, _ := json.Marshal(map[string]string{"capex_usage_id": id})
	rows, err := tx.Query(r.Context(), `SELECT id, metadata FROM issue WHERE workspace_id = $1 AND (metadata @> $2::jsonb OR (project_id = $3 AND title = $4)) FOR UPDATE`, ws, byID, storage, title)
	if err != nil {
		return nil, errors.New("could not read usage posts")
	}
	type post struct {
		id       pgtype.UUID
		metadata map[string]json.RawMessage
	}
	var found []post
	for rows.Next() {
		var pid pgtype.UUID
		var rawMeta []byte
		if err := rows.Scan(&pid, &rawMeta); err != nil {
			rows.Close()
			return nil, errors.New("could not read usage posts")
		}
		m := map[string]json.RawMessage{}
		_ = json.Unmarshal(rawMeta, &m)
		found = append(found, post{pid, m})
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, errors.New("could not read usage posts")
	}
	if len(found) > 1 {
		return nil, fmt.Errorf("usage post %s exists twice; a person must remove one on the Capex page", id)
	}

	var stored *matasUsageEntry
	var existing map[string]json.RawMessage
	if len(found) == 1 {
		existing = found[0].metadata
		if s := metaString(existing, "capex_usage_data"); s != "" {
			var e matasUsageEntry
			if json.Unmarshal([]byte(s), &e) != nil || e.V != 1 || e.ID != id || e.Months == nil || e.Days == nil {
				return nil, fmt.Errorf("usage post %s could not be read; nothing was changed", id)
			}
			stored = &e
		}
	}
	entry, err := matasMergeUsage(stored, p.Days, today)
	if err != nil {
		return nil, err
	}
	entry.ID = id
	if stored == nil {
		entry.Label = p.Label
		if entry.Label == "" {
			entry.Label = p.Source
		}
		if kind == "project" {
			entry.ProjectID = &target
		} else {
			entry.CapexID = &target
		}
	} // else the merge kept the stored label, note and allocation, which a person may have changed.

	data, err := entry.encode()
	if err != nil {
		return nil, errors.New("could not encode the usage post")
	}
	values := map[string]any{
		"capex_usage": true, "capex_usage_id": id, "capex_usage_data": string(data),
		"capex_usage_source": p.Source, "capex_usage_reporter": util.UUIDToString(member),
		"capex_usage_reported_at": now.UTC().Format(time.RFC3339), "capex_usage_request_id": p.RequestID,
	}
	metadata := map[string]json.RawMessage{}
	combined := map[string]json.RawMessage{}
	for k, v := range existing {
		combined[k] = v
	}
	for k, v := range values {
		metadata[k], _ = json.Marshal(v)
		combined[k] = metadata[k]
	}
	if size, _ := json.Marshal(combined); len(size) > matasUsageMaxBytes {
		return nil, fmt.Errorf("the post's metadata would be %d bytes; the limit is %d. Nothing was changed", len(size), matasUsageMaxBytes)
	}

	created := len(found) == 0
	issueID := pgtype.UUID{}
	if created {
		result, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
			WorkspaceID: ws, Title: title, Status: "done", Priority: "none", CreatorType: "member", CreatorID: member,
			Description: pgtype.Text{String: "Forbrug meldt af appen «" + p.Source + "» via MCP. Vises på Capex-siden; ret navn og Capex dér, ikke i metadata.", Valid: true},
			ProjectID:   storage, AllowDuplicate: true,
		}, service.IssueCreateOpts{Platform: "mcp", BroadcastPayload: h.mcpIssueBroadcast(r)})
		if err != nil {
			return nil, errors.New("Multica could not accept this; retry with the same request_id")
		}
		issueID = result.Issue.ID
	} else {
		issueID = found[0].id
	}
	// The marker is written last, so readers never see an incomplete payload; a create that
	// stops before it is found again by its title.
	qtx := h.Queries.WithTx(tx)
	var updated db.Issue
	for _, k := range []string{"capex_usage_id", "capex_usage_data", "capex_usage_source", "capex_usage_reporter", "capex_usage_reported_at", "capex_usage_request_id", "capex_usage"} {
		updated, err = qtx.SetIssueMetadataKey(r.Context(), db.SetIssueMetadataKeyParams{ID: issueID, WorkspaceID: ws, Key: k, Value: metadata[k]})
		if err != nil {
			return nil, errors.New("Multica could not accept this; retry with the same request_id")
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		return nil, errors.New("Multica could not accept this; retry with the same request_id")
	}
	workspaceID, userID := util.UUIDToString(ws), util.UUIDToString(member)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	h.publish(protocol.EventIssueMetadataChanged, workspaceID, actorType, actorID, map[string]any{
		"issue_id": util.UUIDToString(updated.ID), "metadata": parseIssueMetadata(updated.Metadata), "issue_revision": updated.Revision,
	})
	return h.matasUsageReceipt(r, ws, issueID, entry, kind, target, created, p.Days), nil
}

// matasUsageReceipt tells the app where the post is and its new totals, so it can confirm.
func (h *Handler) matasUsageReceipt(r *http.Request, ws, issueID pgtype.UUID, e matasUsageEntry, kind, target string, created bool, days []matasUsageDay) map[string]any {
	var tokens, usd float64
	for _, m := range e.Months {
		tokens, usd = tokens+m[0], usd+m[1]
	}
	months := map[string]any{}
	for _, d := range days {
		m := e.Months[d.Date[:7]]
		months[d.Date[:7]] = map[string]float64{"tokens": m[0], "usd": m[1]}
	}
	allocation := map[string]any{"kind": "unallocated"}
	switch {
	case e.ProjectID != nil:
		allocation = map[string]any{"kind": "project", "project_id": *e.ProjectID}
		if p, err := h.matasProject(r, ws, *e.ProjectID); err == nil {
			allocation["title"] = p.Title
			if n := h.matasNumberOf(r.Context(), ws, p.ID); n != "" {
				allocation["number"] = n
			}
		}
	case e.CapexID != nil:
		allocation = map[string]any{"kind": "capex", "capex_id": *e.CapexID}
		if item, err := h.matasCapexItem(r, ws, *e.CapexID); err == nil {
			allocation["number"], allocation["description"] = item["number"], item["description"]
		}
	}
	out := map[string]any{
		"issue_id": util.UUIDToString(issueID), "usage_id": e.ID, "label": e.Label, "created": created,
		"target": allocation, "days_reported": len(days), "through": e.Through,
		"totals": map[string]float64{"tokens": matasUsageRound(tokens), "usd": matasUsageRound(usd)}, "months": months,
	}
	if allocation["kind"] != kind || (allocation["capex_id"] != nil && allocation["capex_id"] != target) || (allocation["project_id"] != nil && allocation["project_id"] != target) {
		out["reported_to"] = map[string]string{"kind": kind, "id": target}
		out["note"] = "A person moved this post on the Capex page; it stays where they put it."
	}
	return out
}
