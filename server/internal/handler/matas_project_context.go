package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Project context («Kontekst»): the fixed facts agents look up before they act on a project —
// goals, priorities, milestones, who decides what, which sources count and which do not, known
// risks and the project's own terms. It is one JSON value, ctx_spec, on the project's intake
// issue next to dia_spec, so the schema is unchanged. The portfolio has one too, on the issue
// marked portfolio_context.
//
// The server is the one place that checks it: only members, never agents, write it; on a project
// only the diamond's owners and workspace owners/admins, on the portfolio only owners/admins. The
// value is validated and stored normalised, with updated_at and updated_by set here. Agents
// propose a change as a task in review for the product owner instead. sync_diamonds.py copies
// the context to roster.json and the hub's README and writes ctx_editors: the e-mails the roster
// links to each role, so owners without an e-mail in dia_spec can edit too.

const (
	matasContextKey        = "ctx_spec"
	matasContextEditorsKey = "ctx_editors"
	// matasPortfolioContextMark marks the one issue that holds the portfolio's context.
	matasPortfolioContextMark = "portfolio_context"
	matasContextMaxBytes      = 4500
	matasContextStaleDays     = 30
	matasPortfolioRef         = "portfolio"
)

var (
	matasContextAreas   = []string{"business", "product", "platform", "technical", "steerco"}
	matasSourceTypes    = []string{"clickup", "sharepoint", "teams", "repo", "figma", "multica", "other"}
	matasContextFields  = []string{"goals", "priorities", "milestones", "decision_rights", "sources", "not_sources", "watch_for", "terms"}
	matasContextURLRE   = regexp.MustCompile(`^https?://[^\s<>"]{1,490}$`)
	matasContextEmailRE = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)
)

type matasSource struct {
	Type string `json:"type"`
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type matasMilestone struct {
	Date string `json:"date"`
	What string `json:"what"`
}

type matasTerm struct {
	Term    string `json:"term"`
	Meaning string `json:"meaning"`
}

// matasContext is ctx_spec. Owner is the portfolio's owner and only used there.
type matasContext struct {
	Goals          []string          `json:"goals"`
	Priorities     []string          `json:"priorities"`
	Milestones     []matasMilestone  `json:"milestones"`
	DecisionRights map[string]string `json:"decision_rights"`
	Sources        []matasSource     `json:"sources"`
	NotSources     []matasSource     `json:"not_sources"`
	WatchFor       []string          `json:"watch_for"`
	Terms          []matasTerm       `json:"terms"`
	Owner          string            `json:"owner,omitempty"`
	UpdatedAt      string            `json:"updated_at"`
	UpdatedBy      string            `json:"updated_by"`
}

// matasContextText trims a text field and checks its length; control characters are refused.
func matasContextText(field string, s string, max int, required bool) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		if required {
			return "", fmt.Errorf("%s must not be empty", field)
		}
		return "", nil
	}
	if n := len([]rune(s)); n > max {
		return "", fmt.Errorf("%s is %d characters; at most %d", field, n, max)
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%s must be one line of plain text", field)
	}
	return s, nil
}

func matasContextList(field string, in []string, max, maxItems int) ([]string, error) {
	out := []string{}
	for i, s := range in {
		v, err := matasContextText(fmt.Sprintf("%s[%d]", field, i+1), s, max, false)
		if err != nil {
			return nil, err
		}
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) > maxItems {
		return nil, fmt.Errorf("%s has %d entries; at most %d", field, len(out), maxItems)
	}
	return out, nil
}

func matasContextSources(field string, in []matasSource, linkRequired bool) ([]matasSource, error) {
	out := []matasSource{}
	for i, s := range in {
		at := fmt.Sprintf("%s[%d]", field, i+1)
		if !slices.Contains(matasSourceTypes, s.Type) {
			return nil, fmt.Errorf("%s.type must be one of %s", at, strings.Join(matasSourceTypes, ", "))
		}
		name, err := matasContextText(at+".name", s.Name, 120, true)
		if err != nil {
			return nil, err
		}
		link := strings.TrimSpace(s.URL)
		if link == "" && linkRequired {
			return nil, fmt.Errorf("%s.url is required: a source is only useful with its link", at)
		}
		if link != "" && !matasContextURLRE.MatchString(link) {
			return nil, fmt.Errorf("%s.url must be an http(s) link", at)
		}
		out = append(out, matasSource{Type: s.Type, Name: name, URL: link})
	}
	if len(out) > 15 {
		return nil, fmt.Errorf("%s has %d entries; at most 15", field, len(out))
	}
	return out, nil
}

// matasNormalizeContext validates a ctx_spec value and returns it as stored: trimmed, empty
// entries dropped, updated_at and updated_by set by the server whatever the client sent.
func matasNormalizeContext(raw string, portfolio bool, by string, now time.Time) (string, error) {
	var c matasContext
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return "", fmt.Errorf("the context must be a JSON object with the fields %s: %v", strings.Join(matasContextFields, ", "), err)
	}
	if d.More() {
		return "", errors.New("the context must be one JSON object")
	}
	var err error
	if c.Goals, err = matasContextList("goals", c.Goals, 200, 3); err != nil {
		return "", err
	}
	if len(c.Goals) == 0 {
		return "", errors.New("goals must have 1–3 goals, one sentence each")
	}
	if c.Priorities, err = matasContextList("priorities", c.Priorities, 200, 3); err != nil {
		return "", err
	}
	if c.WatchFor, err = matasContextList("watch_for", c.WatchFor, 200, 8); err != nil {
		return "", err
	}
	milestones := []matasMilestone{}
	for i, m := range c.Milestones {
		at := fmt.Sprintf("milestones[%d]", i+1)
		day := strings.TrimSpace(m.Date)
		if _, err := time.Parse("2006-01-02", day); err != nil || !matasDateRE.MatchString(day) {
			return "", fmt.Errorf("%s.date must be a day as YYYY-MM-DD", at)
		}
		what, err := matasContextText(at+".what", m.What, 200, true)
		if err != nil {
			return "", err
		}
		milestones = append(milestones, matasMilestone{Date: day, What: what})
	}
	if len(milestones) > 20 {
		return "", fmt.Errorf("milestones has %d entries; at most 20", len(milestones))
	}
	c.Milestones = milestones
	rights := map[string]string{}
	for area, who := range c.DecisionRights {
		if !slices.Contains(matasContextAreas, area) {
			return "", fmt.Errorf("decision_rights.%s is not an area; use %s", area, strings.Join(matasContextAreas, ", "))
		}
		v, err := matasContextText("decision_rights."+area, who, 200, false)
		if err != nil {
			return "", err
		}
		if v != "" {
			rights[area] = v
		}
	}
	c.DecisionRights = rights
	if c.Sources, err = matasContextSources("sources", c.Sources, true); err != nil {
		return "", err
	}
	if c.NotSources, err = matasContextSources("not_sources", c.NotSources, false); err != nil {
		return "", err
	}
	terms := []matasTerm{}
	for i, t := range c.Terms {
		at := fmt.Sprintf("terms[%d]", i+1)
		term, err := matasContextText(at+".term", t.Term, 60, true)
		if err != nil {
			return "", err
		}
		meaning, err := matasContextText(at+".meaning", t.Meaning, 200, true)
		if err != nil {
			return "", err
		}
		terms = append(terms, matasTerm{Term: term, Meaning: meaning})
	}
	if len(terms) > 30 {
		return "", fmt.Errorf("terms has %d entries; at most 30", len(terms))
	}
	c.Terms = terms
	if !portfolio && strings.TrimSpace(c.Owner) != "" {
		return "", errors.New("owner is only for the portfolio context; a project's owners are its diamond")
	}
	if c.Owner, err = matasContextText("owner", c.Owner, 80, false); err != nil {
		return "", err
	}
	c.UpdatedAt = now.UTC().Format(time.RFC3339)
	c.UpdatedBy = by
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(c); err != nil {
		return "", err
	}
	out := strings.TrimSpace(buf.String())
	if len(out) > matasContextMaxBytes {
		return "", fmt.Errorf("the context is %d bytes; at most %d. Shorten the longest texts", len(out), matasContextMaxBytes)
	}
	return out, nil
}

// matasLeadRole is the project lead (MATA-286): dia_spec.project_lead, one primary per project,
// who owns the project and keeps it and its diamond current. It is not a diamond role and owns no
// area; ctx_editors carries the e-mails the roster links to it under this key.
const matasLeadRole = "pl"

// matasPerson is a role or the project lead in dia_spec.
type matasPerson struct {
	Person string `json:"person"`
	Email  string `json:"email"`
}

// matasSpecPeople is the part of dia_spec the server's checks read.
type matasSpecPeople struct {
	Archived    bool                   `json:"archived"`
	Roles       map[string]matasPerson `json:"roles"`
	ProjectLead *matasPerson           `json:"project_lead"`
}

// matasSpecOf reads dia_spec from intake metadata; ok is false without one.
func matasSpecOf(m map[string]json.RawMessage) (matasSpecPeople, bool) {
	var spec matasSpecPeople
	s := metaString(m, "dia_spec")
	return spec, s != "" && json.Unmarshal([]byte(s), &spec) == nil
}

// lead is the project lead with a name, or nil.
func (s matasSpecPeople) lead() *matasPerson {
	if s.ProjectLead == nil || strings.TrimSpace(s.ProjectLead.Person) == "" {
		return nil
	}
	return s.ProjectLead
}

// matasContextEditorEmails are the e-mails that may edit a project's context: those on a role or
// the project lead in dia_spec and those the roster links to them (ctx_editors, written by the
// sync). An archived diamond has no editors.
func matasContextEditorEmails(m map[string]json.RawMessage) map[string]bool {
	out := map[string]bool{}
	spec, ok := matasSpecOf(m)
	if !ok || spec.Archived {
		return out
	}
	people := []matasPerson{}
	for _, r := range spec.Roles {
		people = append(people, r)
	}
	if l := spec.lead(); l != nil {
		people = append(people, *l)
	}
	for _, r := range people {
		if e := strings.ToLower(strings.TrimSpace(r.Email)); e != "" && strings.TrimSpace(r.Person) != "" {
			out[e] = true
		}
	}
	for _, emails := range matasContextEditorsByRole(m) {
		for _, e := range emails {
			out[e] = true
		}
	}
	return out
}

// matasRoleEmails are the e-mails linked to one role of a live diamond, or to its project lead
// (matasLeadRole): the one in dia_spec and the roster's (ctx_editors).
func matasRoleEmails(m map[string]json.RawMessage, role string) map[string]bool {
	out := map[string]bool{}
	spec, ok := matasSpecOf(m)
	if !ok || spec.Archived {
		return out
	}
	p, has := spec.Roles[role]
	if role == matasLeadRole {
		if l := spec.lead(); l != nil {
			p, has = *l, true
		}
	}
	if !has || strings.TrimSpace(p.Person) == "" {
		return out
	}
	if e := strings.ToLower(strings.TrimSpace(p.Email)); e != "" {
		out[e] = true
	}
	for _, e := range matasContextEditorsByRole(m)[role] {
		out[e] = true
	}
	return out
}

// matasContextEditorsByRole reads ctx_editors: {"bo": ["a@matas.dk"], "po": [...], ...}.
func matasContextEditorsByRole(m map[string]json.RawMessage) map[string][]string {
	out := map[string][]string{}
	var editors map[string][]string
	if s := metaString(m, matasContextEditorsKey); s != "" && json.Unmarshal([]byte(s), &editors) == nil {
		for role, emails := range editors {
			for _, e := range emails {
				if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
					out[role] = append(out[role], e)
				}
			}
		}
	}
	return out
}

// matasContextGuard checks a write to ctx_spec or ctx_editors before it happens. For ctx_spec it
// returns the value to store. status is 0 when the write may go ahead.
func (h *Handler) matasContextGuard(r *http.Request, issue db.Issue, key string, value json.RawMessage) (json.RawMessage, int, string) {
	ws := uuidToString(issue.WorkspaceID)
	if actorType, _ := h.resolveActor(r, requestUserID(r), ws); actorType != "member" {
		return nil, http.StatusForbidden, "agents cannot edit a project context; propose the change as a task in review for the project's product owner"
	}
	member, err := h.getWorkspaceMember(r.Context(), requestUserID(r), ws)
	if err != nil {
		return nil, http.StatusForbidden, "only workspace members can edit a project context"
	}
	admin := roleAllowed(member.Role, "owner", "admin")
	if key == matasPortfolioContextMark {
		if !admin {
			return nil, http.StatusForbidden, "only workspace owners and admins mark the portfolio context issue"
		}
		return value, 0, ""
	}
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(issue.Metadata, &m)
	portfolio := string(m[matasPortfolioContextMark]) == "true"
	intake := string(m["pf_intake"]) == "true" && issue.ProjectID.Valid
	if !portfolio && !intake {
		return nil, http.StatusBadRequest, key + " belongs on a project's intake issue or on the portfolio context issue"
	}
	if key == matasContextEditorsKey {
		if !admin {
			return nil, http.StatusForbidden, "only workspace owners and admins (the diamond sync) write ctx_editors"
		}
		return value, 0, ""
	}
	user, err := h.Queries.GetUser(r.Context(), member.UserID)
	if err != nil {
		return nil, http.StatusForbidden, "could not read your account"
	}
	email := strings.ToLower(strings.TrimSpace(user.Email))
	switch {
	case admin:
	case portfolio:
		return nil, http.StatusForbidden, "only workspace owners and admins can edit the portfolio context"
	case middleware.IsViewer(member.Role) || !matasContextEditorEmails(m)[email]:
		return nil, http.StatusForbidden, "only the project's diamond owners, its project lead and workspace admins can edit its context"
	}
	if value == nil {
		return nil, 0, "" // a delete
	}
	var raw string
	if json.Unmarshal(value, &raw) != nil {
		return nil, http.StatusBadRequest, "ctx_spec must be a JSON string holding the context object"
	}
	normalized, err := matasNormalizeContext(raw, portfolio, email, time.Now())
	if err != nil {
		return nil, http.StatusBadRequest, err.Error()
	}
	out, _ := json.Marshal(normalized)
	return out, 0, ""
}

// matasContextGuarded is true for the keys matasContextGuard checks.
func matasContextGuarded(key string) bool {
	return key == matasContextKey || key == matasContextEditorsKey || key == matasPortfolioContextMark
}

// matasDiamondGuard checks a write to dia_spec (value nil for a delete). A diamond that exists —
// its roles, stakeholders, logins, archiving or restoring it — is changed by workspace owners and
// admins and by the project's own project lead (MATA-286); its Product Owner may change only the
// stakeholders. Only owners and admins appoint, change or remove the project lead, and only they
// delete dia_spec. Creating a diamond where there is none stays open, so a member's «Opret
// diamant» and Mrs.Intake's new_project.py keep working, as long as it names no project lead.
// status is 0 when the write may go ahead.
func (h *Handler) matasDiamondGuard(r *http.Request, issue db.Issue, value json.RawMessage) (int, string) {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(issue.Metadata, &m)
	_, exists := m["dia_spec"]
	next := map[string]json.RawMessage{}
	if value != nil {
		next["dia_spec"] = value
	}
	oldSpec, _ := matasSpecOf(m)
	newSpec, _ := matasSpecOf(next)
	leadChanged := value != nil && !matasSameLead(oldSpec.lead(), newSpec.lead())
	if !exists && !leadChanged {
		return 0, ""
	}
	ws := uuidToString(issue.WorkspaceID)
	if actorType, _ := h.resolveActor(r, requestUserID(r), ws); actorType != "member" {
		return http.StatusForbidden, "agents cannot change a diamond or its project lead; a workspace admin or the project lead changes it on the project page"
	}
	member, err := h.getWorkspaceMember(r.Context(), requestUserID(r), ws)
	if err != nil {
		return http.StatusForbidden, "only workspace members can change a diamond"
	}
	if roleAllowed(member.Role, "owner", "admin") {
		return 0, ""
	}
	switch {
	case leadChanged:
		return http.StatusForbidden, "only workspace owners and admins appoint, change or remove a project's project lead"
	case value == nil:
		return http.StatusForbidden, "only workspace owners and admins can remove a diamond"
	}
	user, err := h.Queries.GetUser(r.Context(), member.UserID)
	if err != nil {
		return http.StatusForbidden, "could not read your account"
	}
	email := strings.ToLower(strings.TrimSpace(user.Email))
	switch {
	case email == "" || middleware.IsViewer(member.Role):
	case matasRoleEmails(m, matasLeadRole)[email]:
		return 0, ""
	case matasRoleEmails(m, "po")[email] && matasOnlyStakeholders(metaString(m, "dia_spec"), metaString(next, "dia_spec")):
		return 0, ""
	}
	return http.StatusForbidden, "only workspace admins and the project's project lead change a diamond's roles or archive it; its Product Owner may change the stakeholders"
}

// matasSameLead is whether two project leads are the same person with the same e-mail.
func matasSameLead(a, b *matasPerson) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return strings.TrimSpace(a.Person) == strings.TrimSpace(b.Person) &&
		strings.EqualFold(strings.TrimSpace(a.Email), strings.TrimSpace(b.Email))
}

// matasOnlyStakeholders is whether two dia_spec values differ in nothing but their stakeholders.
func matasOnlyStakeholders(before, after string) bool {
	var a, b map[string]any
	if json.Unmarshal([]byte(before), &a) != nil || json.Unmarshal([]byte(after), &b) != nil {
		return false
	}
	delete(a, "stakeholders")
	delete(b, "stakeholders")
	return reflect.DeepEqual(a, b)
}

// matasPortfolioContextIssue finds the one issue that holds the portfolio context.
func (h *Handler) matasPortfolioContextIssue(ctx context.Context, ws pgtype.UUID) (map[string]json.RawMessage, bool, error) {
	rows, err := h.DB.Query(ctx, `SELECT metadata FROM issue WHERE workspace_id = $1 AND metadata @> '{"portfolio_context": true}'::jsonb ORDER BY created_at LIMIT 2`, ws)
	if err != nil {
		return nil, false, errors.New("could not read the portfolio context")
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, false, errors.New("could not read the portfolio context")
	}
	if len(found) > 1 {
		return nil, false, errors.New("the portfolio context is on more than one issue; a person must fix that")
	}
	if len(found) == 0 {
		return nil, false, nil
	}
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(found[0], &m)
	return m, true, nil
}

// matasContextView is the context as the MCP returns it, with how old it is.
func matasContextView(m map[string]json.RawMessage, now time.Time) (map[string]any, bool) {
	raw := metaString(m, matasContextKey)
	var c matasContext
	if raw == "" || json.Unmarshal([]byte(raw), &c) != nil {
		return nil, false
	}
	out := map[string]any{"context": json.RawMessage(raw)}
	if at, err := time.Parse(time.RFC3339, c.UpdatedAt); err == nil {
		days := int(now.Sub(at).Hours() / 24)
		out["days_since_update"] = days
		out["stale"] = days >= matasContextStaleDays
	}
	return out, true
}

func (h *Handler) mcpProjectContext(r *http.Request, raw json.RawMessage) (any, error) {
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
	now := time.Now()
	if strings.EqualFold(strings.TrimSpace(p.ProjectID), matasPortfolioRef) {
		m, ok, err := h.matasPortfolioContextIssue(r.Context(), ws)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"scope": "portfolio", "context": nil}
		if view, has := matasContextView(m, now); ok && has {
			for k, v := range view {
				out[k] = v
			}
		} else {
			out["note"] = "No portfolio context has been written yet. Workspace admins write it on the Projects page."
		}
		return out, nil
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
	out := map[string]any{"scope": "project", "project": map[string]any{"id": util.UUIDToString(project.ID), "title": project.Title}, "context": nil}
	if n := metaString(m, matasNumberKey); n != "" {
		out["project"].(map[string]any)["number"] = n
	}
	var spec struct {
		Archived    bool            `json:"archived"`
		Roles       json.RawMessage `json:"roles"`
		ProjectLead json.RawMessage `json:"project_lead"`
	}
	viewer := h.matasViewer(r)
	if s := metaString(m, "dia_spec"); s != "" && json.Unmarshal([]byte(s), &spec) == nil && !spec.Archived {
		if viewer {
			spec.Roles, spec.ProjectLead = matasWithoutEmails(spec.Roles), matasWithoutEmails(spec.ProjectLead)
		}
		out["diamond"] = spec.Roles
		if l, ok := matasSpecOf(m); ok && l.lead() != nil {
			out["project_lead"] = spec.ProjectLead
		}
	}
	// A reader (MATA-286) gets only what the owners shared: the whole context when they shared the
	// whole project, their approved summary when they shared that, else who to ask. Decision rights,
	// sources and what to watch for stay with the project's own people.
	if viewer {
		switch mode := matasSharedMode(m); mode {
		case "full":
		case "summary":
			out["shared"], out["summary"] = mode, metaString(m, "kh_summary")
			out["note"] = "The owners shared only this summary with readers; the project context is not shared."
			return out, nil
		default:
			out["shared"] = "none"
			out["note"] = "The owners have not shared this project. Name it and ask " + matasMemoryAsk(m) + "; do not guess at its context."
			return out, nil
		}
	}
	if view, ok := matasContextView(m, now); ok {
		if viewer {
			view["context"] = matasWithoutUpdatedBy(view["context"].(json.RawMessage))
		}
		for k, v := range view {
			out[k] = v
		}
	} else {
		out["note"] = "The project's owners have not written a project context yet."
	}
	return out, nil
}

// matasSharedMode is what the owners share with Knowledge Hub once the sync has applied it: full,
// summary or none.
func matasSharedMode(m map[string]json.RawMessage) string {
	share := metaString(m, "kh_share")
	var ks struct {
		Mode string `json:"mode"`
	}
	if share == "" || share != metaString(m, "kh_synced") || json.Unmarshal([]byte(share), &ks) != nil || (ks.Mode != "full" && ks.Mode != "summary") {
		return "none"
	}
	return ks.Mode
}

// matasWithoutUpdatedBy drops the editor's e-mail from a context shown to a reader.
func matasWithoutUpdatedBy(raw json.RawMessage) json.RawMessage {
	var c map[string]any
	if json.Unmarshal(raw, &c) != nil {
		return raw
	}
	delete(c, "updated_by")
	out, err := json.Marshal(c)
	if err != nil {
		return raw
	}
	return out
}

// mcpProposeProjectContext files a proposed change to a project's context as a task in review for
// its product owner. Nothing in the context changes until an owner edits it on the project page.
func (h *Handler) mcpProposeProjectContext(r *http.Request, raw json.RawMessage) (any, error) {
	var p struct {
		RequestID string `json:"request_id"`
		ProjectID string `json:"project_id"`
		Field     string `json:"field"`
		Proposal  string `json:"proposal"`
		Why       string `json:"why"`
	}
	if err := decodeMCPArguments(raw, &p); err != nil {
		return nil, err
	}
	if !slices.Contains(matasContextFields, p.Field) {
		return nil, fmt.Errorf("field must be one of %s", strings.Join(matasContextFields, ", "))
	}
	proposal, why := strings.TrimSpace(p.Proposal), strings.TrimSpace(p.Why)
	if proposal == "" || len([]rune(proposal)) > 2000 || why == "" || len([]rune(why)) > 2000 {
		return nil, errors.New("proposal and why must each be 1–2000 characters")
	}
	rid, err := util.ParseUUID(p.RequestID)
	if err != nil {
		return nil, errors.New("request_id must be a UUID")
	}
	ws, creator, err := h.matasWorkspace(r)
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
	assignee, who := h.matasProductOwner(r.Context(), ws, project, m)
	name := matasNumbered(metaString(m, matasNumberKey), project.Title)
	text := "Forslag til projektkonteksten for **" + name + "**, feltet `" + p.Field + "`.\n\n**Forslag:** " + proposal +
		"\n\n**Hvorfor:** " + why + "\n\nKonteksten ændres ikke af forslaget. Er du enig, så ret den under fanen «Kontekst» på projektsiden og sæt opgaven til done; ellers luk den med en kort begrundelse.\n"
	if who != "" {
		text += "\n" + who + "\n"
	}
	// The service's submission ledger is for issues an agent runs, and this one goes to a person,
	// so a retry is recognised by its request_id in metadata instead.
	requestID := util.UUIDToString(rid)
	var existing pgtype.UUID
	if h.DB.QueryRow(r.Context(), `SELECT id FROM issue WHERE workspace_id = $1 AND metadata->>'ctx_proposal_request_id' = $2 LIMIT 1`, ws, requestID).Scan(&existing) == nil {
		issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: existing, WorkspaceID: ws})
		if err != nil {
			return nil, errors.New("could not read the earlier proposal with this request_id")
		}
		return h.mcpIssueReceipt(r, issue), nil
	}
	params := service.IssueCreateParams{
		WorkspaceID: ws, Title: truncateRunes("Forslag til kontekst: "+name+" — "+p.Field, 200), Description: pgtype.Text{String: text, Valid: true},
		Status: "in_review", Priority: "medium", CreatorType: "member", CreatorID: creator, ProjectID: project.ID, AllowDuplicate: true,
	}
	if assignee.Valid {
		params.AssigneeType, params.AssigneeID = pgtype.Text{String: "member", Valid: true}, assignee
	}
	result, err := h.IssueService.Create(r.Context(), params, service.IssueCreateOpts{Platform: "mcp", BroadcastPayload: h.mcpIssueBroadcast(r)})
	if err != nil {
		return nil, errors.New("Multica could not accept this; retry with the same request_id")
	}
	issue := result.Issue
	field, _ := json.Marshal(p.Field)
	rq, _ := json.Marshal(requestID)
	for _, kv := range [][2]any{{"ctx_proposal_request_id", rq}, {"ctx_proposal", []byte("true")}, {"ctx_field", field}} {
		if issue, err = h.Queries.SetIssueMetadataKey(r.Context(), db.SetIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: ws, Key: kv[0].(string), Value: kv[1].([]byte)}); err != nil {
			return nil, errors.New("the proposal was created but could not be marked; retry with the same request_id")
		}
	}
	return h.mcpIssueReceipt(r, issue), nil
}

// matasProductOwner is the member a context proposal goes to: the product owner by the e-mail on
// the role or the roster's link to it, else the project lead. The note says who it went to.
func (h *Handler) matasProductOwner(ctx context.Context, ws pgtype.UUID, project db.Project, m map[string]json.RawMessage) (pgtype.UUID, string) {
	var spec struct {
		Roles map[string]struct {
			Email string `json:"email"`
		} `json:"roles"`
	}
	_ = json.Unmarshal([]byte(metaString(m, "dia_spec")), &spec)
	emails := []string{strings.ToLower(strings.TrimSpace(spec.Roles["po"].Email))}
	emails = append(emails, matasContextEditorsByRole(m)["po"]...)
	for _, e := range emails {
		if e == "" || !matasContextEmailRE.MatchString(e) {
			continue
		}
		var userID pgtype.UUID
		if h.DB.QueryRow(ctx, `SELECT u.id FROM "user" u JOIN member mb ON mb.user_id = u.id WHERE mb.workspace_id = $1 AND lower(u.email) = $2`, ws, e).Scan(&userID) == nil {
			return userID, ""
		}
	}
	if project.LeadType.String == "member" && project.LeadID.Valid {
		return project.LeadID, "Projektet har ingen Product Owner med et Multica-login, så forslaget gik til projektets lead."
	}
	return pgtype.UUID{}, "Projektet har ingen Product Owner med et Multica-login og ingen lead. En admin må give opgaven til den rette."
}
