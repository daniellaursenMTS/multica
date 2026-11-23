package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

// Interview brief (MATA-266): before an agent interviews one of a diamond's owners for their
// update, it asks what is waiting for that person's role. Proposals in the typed memory that only
// they may confirm, risks nobody has updated for two weeks, milestones within two weeks, open
// questions, proposed context changes assigned to them, and 3–5 questions to ask, the concrete ones
// first and then the role's own (business case, scope, dependencies, architecture).
//
// A brief is only for the caller, and only for a project where the caller holds a role in the
// diamond, found by the e-mails on the role (dia_spec) or the roster's links (ctx_editors). Anyone
// else gets a clear refusal. Nothing from another project is read. The brief itself writes nothing:
// the update goes through multica_add_project_update with source "interview", after the person has
// approved the summary.

const (
	matasBriefRiskDays      = 14 // a risk with no post on its topic for this long is «without update»
	matasBriefMilestoneDays = 14
	matasBriefMaxItems      = 8
	matasBriefMaxQuestions  = 5
)

var matasRoleAreas = map[string][]string{"bo": {"business"}, "po": {"product"}, "plo": {"platform"}, "to": {"technical"}}

// matasRoleQuestions are the role's own questions, asked when the concrete ones leave room.
var matasRoleQuestions = map[string][]string{
	"bo": {
		"Holder business casen stadig, eller har noget ændret sig siden sidst?",
		"Hvilke gevinster har I set, eller hvilke er blevet mindre sikre?",
		"Har projektet samme prioritet over for andre projekter som før?",
	},
	"po": {
		"Har scope ændret sig siden sidst: er noget kommet med eller taget ud?",
		"Hvad siger brugerne? Har I fået feedback, der ændrer noget?",
		"Hvad er næste leverance, og hvornår kommer den?",
	},
	"plo": {
		"Venter I på andre teams eller systemer? Hvilke afhængigheder er kritiske nu?",
		"Hvordan går driften: hændelser, nedetid eller support, der fylder?",
		"Er der nye spørgsmål om sikkerhed, adgang eller GDPR?",
	},
	"to": {
		"Har I truffet tekniske valg eller ændret arkitekturen siden sidst?",
		"Hvilken teknisk gæld vokser, og hvad koster den jer?",
		"Venter et teknisk valg på en beslutning, og hvem skal tage den?",
	},
}

// matasCallerRoles are the diamond roles the e-mail holds in a project, in role order, and the
// person's name as the diamond has it. An archived diamond gives none.
func matasCallerRoles(m map[string]json.RawMessage, email string) ([]string, string) {
	var spec struct {
		Archived bool `json:"archived"`
		Roles    map[string]struct {
			Person string `json:"person"`
			Email  string `json:"email"`
		} `json:"roles"`
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || json.Unmarshal([]byte(metaString(m, "dia_spec")), &spec) != nil || spec.Archived {
		return nil, ""
	}
	editors := matasContextEditorsByRole(m)
	roles, name := []string{}, ""
	for _, r := range matasRoleOrder {
		person := strings.TrimSpace(spec.Roles[r].Person)
		if person == "" {
			continue
		}
		if strings.ToLower(strings.TrimSpace(spec.Roles[r].Email)) == email || slices.Contains(editors[r], email) {
			roles = append(roles, r)
			if name == "" {
				name = person
			}
		}
	}
	return roles, name
}

type matasBriefItem struct {
	Kind   string `json:"kind"` // decision, risk, milestone, question, proposal
	ID     string `json:"id,omitempty"`
	Text   string `json:"text"`
	Date   string `json:"date,omitempty"`
	Area   string `json:"area,omitempty"`
	Source string `json:"source"`
	Why    string `json:"why"`
}

type matasBriefQuestion struct {
	Text  string `json:"text"`
	About string `json:"about,omitempty"` // the item's id, when the question is about one
	Kind  string `json:"kind"`            // the item's kind, or role
}

// matasForRoles: an item in one of the roles' areas, or in no area, is for these roles.
func matasForRoles(area string, roles []string) bool {
	if area == "" {
		return true
	}
	for _, r := range roles {
		if slices.Contains(matasRoleAreas[r], area) {
			return true
		}
	}
	return false
}

// matasLastSaid is the newest day anything was said on each topic, superseded posts and events
// included: a later post on a risk's topic counts as an update of it.
func matasLastSaid(posts []matasMemoryPost) map[string]string {
	out := map[string]string{}
	for _, p := range posts {
		if p.Topic != "" && p.SaidAt > out[p.Topic] {
			out[p.Topic] = p.SaidAt
		}
	}
	return out
}

// matasBriefMemory picks the risks without update and the open questions for the roles.
func matasBriefMemory(posts []matasMemoryPost, roles []string, now time.Time) (risks, questions []matasBriefItem) {
	last := matasLastSaid(posts)
	cutoff := now.AddDate(0, 0, -matasBriefRiskDays).Format("2006-01-02")
	risks, questions = []matasBriefItem{}, []matasBriefItem{}
	for _, p := range matasSearchPosts(posts, matasMemoryQuery{View: "current"}) {
		post := p.Post
		if !matasForRoles(post.Area, roles) {
			continue
		}
		switch post.Type {
		case "risk":
			newest := post.SaidAt
			if post.Topic != "" && last[post.Topic] > newest {
				newest = last[post.Topic]
			}
			if newest > cutoff {
				continue
			}
			risks = append(risks, matasBriefItem{Kind: "risk", ID: post.ID, Text: post.Text, Date: newest, Area: post.Area, Source: matasPostSource(post),
				Why: fmt.Sprintf("nothing said on it since %s (%d days)", newest, matasDaysSince(newest, now))})
		case "question":
			questions = append(questions, matasBriefItem{Kind: "question", ID: post.ID, Text: post.Text, Date: post.SaidAt, Area: post.Area,
				Source: matasPostSource(post), Why: "open question, said " + post.SaidAt})
		}
	}
	// Oldest first: what has waited longest is asked about first.
	oldest := func(xs []matasBriefItem) func(i, j int) bool {
		return func(i, j int) bool {
			if xs[i].Date != xs[j].Date {
				return xs[i].Date < xs[j].Date
			}
			return xs[i].ID < xs[j].ID
		}
	}
	sort.SliceStable(risks, oldest(risks))
	sort.SliceStable(questions, oldest(questions))
	return matasFirst(risks, matasBriefMaxItems), matasFirst(questions, matasBriefMaxItems)
}

// matasBriefMilestones are the context's milestones from two weeks ago to two weeks ahead.
func matasBriefMilestones(c matasContext, now time.Time) []matasBriefItem {
	from := now.AddDate(0, 0, -matasBriefMilestoneDays).Format("2006-01-02")
	today := now.Format("2006-01-02")
	to := now.AddDate(0, 0, matasBriefMilestoneDays).Format("2006-01-02")
	out := []matasBriefItem{}
	for _, m := range c.Milestones {
		if m.Date < from || m.Date > to {
			continue
		}
		why := "due within two weeks"
		if m.Date < today {
			why = "passed in the last two weeks"
		}
		out = append(out, matasBriefItem{Kind: "milestone", Text: m.What, Date: m.Date, Source: "project context, milestones", Why: why})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

func matasDaysSince(day string, now time.Time) int {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return 0
	}
	return int(now.Sub(d).Hours() / 24)
}

func matasFirst[T any](xs []T, n int) []T {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

func matasDay(day string) string {
	if d, err := time.Parse("2006-01-02", day); err == nil {
		return fmt.Sprintf("%d.%d.%d", d.Day(), int(d.Month()), d.Year())
	}
	return day
}

// matasBriefQuestions: up to three concrete questions about what waits (decisions first, then
// milestones, risks, context proposals, open questions and a missing or stale context), then the
// role's own questions, 3–5 in all.
func matasBriefQuestions(roles []string, waiting map[string][]matasBriefItem, contextNote string) []matasBriefQuestion {
	concrete := []matasBriefQuestion{}
	add := func(q matasBriefQuestion) {
		if len(concrete) < 3 {
			concrete = append(concrete, q)
		}
	}
	for _, it := range waiting["decisions"] {
		add(matasBriefQuestion{Kind: "decision", About: it.ID, Text: fmt.Sprintf("«%s» (%s) er et forslag, der venter på dig. Er det besluttet, eller skal det op i styregruppen?", it.Text, it.ID)})
	}
	for _, it := range waiting["milestones"] {
		text := fmt.Sprintf("Milepælen «%s» er %s. Holder datoen?", it.Text, matasDay(it.Date))
		if it.Why != "due within two weeks" {
			text = fmt.Sprintf("Milepælen «%s» var %s. Nåede I den?", it.Text, matasDay(it.Date))
		}
		add(matasBriefQuestion{Kind: "milestone", Text: text})
	}
	for _, it := range waiting["risks"] {
		add(matasBriefQuestion{Kind: "risk", About: it.ID, Text: fmt.Sprintf("Risikoen «%s» (%s) er ikke opdateret siden %s. Er den stadig aktuel, og hvad gør I ved den?", it.Text, it.ID, matasDay(it.Date))})
	}
	for _, it := range waiting["proposals"] {
		add(matasBriefQuestion{Kind: "proposal", About: it.ID, Text: fmt.Sprintf("Et forslag til projektkonteksten venter på dig (%s: %s). Skal det med?", it.ID, it.Text)})
	}
	for _, it := range waiting["questions"] {
		add(matasBriefQuestion{Kind: "question", About: it.ID, Text: fmt.Sprintf("Det åbne spørgsmål «%s» (%s): er det afklaret?", it.Text, it.ID)})
	}
	if contextNote != "" {
		add(matasBriefQuestion{Kind: "context", Text: contextNote})
	}
	want := min(matasBriefMaxQuestions, max(3, len(concrete)+2))
	out := concrete
	for i := 0; len(out) < want && i < 3; i++ {
		for _, r := range roles {
			if len(out) < want {
				out = append(out, matasBriefQuestion{Kind: "role", Text: matasRoleQuestions[r][i]})
			}
		}
	}
	return out
}

// matasContextProposalsFor are the open proposed context changes in a project assigned to the member.
func (h *Handler) matasContextProposalsFor(r *http.Request, ws, project, member pgtype.UUID) ([]matasBriefItem, error) {
	rows, err := h.DB.Query(r.Context(), `SELECT number, title, to_char(created_at AT TIME ZONE 'Europe/Copenhagen', 'YYYY-MM-DD') FROM issue
		WHERE workspace_id = $1 AND project_id = $2 AND metadata @> '{"ctx_proposal": true}'::jsonb AND status NOT IN ('done', 'cancelled')
		AND assignee_type = 'member' AND assignee_id = $3 ORDER BY created_at LIMIT $4`, ws, project, member, matasBriefMaxItems)
	if err != nil {
		return nil, errors.New("could not read proposals for the context")
	}
	defer rows.Close()
	prefix := h.getIssuePrefix(r.Context(), ws)
	out := []matasBriefItem{}
	for rows.Next() {
		var n int32
		var title, day string
		if err := rows.Scan(&n, &title, &day); err != nil {
			return nil, errors.New("could not read proposals for the context")
		}
		key := fmt.Sprintf("%s-%d", prefix, n)
		out = append(out, matasBriefItem{Kind: "proposal", ID: key, Text: title, Date: day, Source: "issue " + key, Why: "a proposed context change assigned to you"})
	}
	return out, rows.Err()
}

func (h *Handler) mcpInterviewBrief(r *http.Request, raw json.RawMessage) (any, error) {
	var a struct {
		ProjectID string `json:"project_id"`
		Person    string `json:"person"`
	}
	if err := decodeMCPArguments(raw, &a); err != nil {
		return nil, err
	}
	ws, member, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	project, err := h.matasProject(r, ws, a.ProjectID)
	if err != nil {
		return nil, err
	}
	user, err := h.Queries.GetUser(r.Context(), member)
	if err != nil {
		return nil, errors.New("could not read your account")
	}
	scope, err := h.matasMemoryScope(r, ws, member, &project.ID, false)
	if err != nil || len(scope) != 1 {
		return nil, errors.New("project not found in this workspace")
	}
	p := scope[0]
	name := matasNumbered(p.Number, p.Title)
	roles, person := matasCallerRoles(p.Meta, user.Email)
	if len(roles) == 0 {
		return nil, fmt.Errorf("you hold no role in the diamond of %s, so there is no interview to prepare: a brief is only for the project's Business, Product, Platform and Technical Owner. Ask %s, or a workspace admin if your role is missing", name, matasMemoryAsk(p.Meta))
	}
	if asked := strings.TrimSpace(a.Person); asked != "" && matasNorm(asked) != matasNorm(person) {
		return nil, fmt.Errorf("a brief is only prepared for yourself: you are %s in the diamond of %s, not %q", person, name, asked)
	}
	now := time.Now()
	var ctx matasContext
	hasContext := json.Unmarshal([]byte(metaString(p.Meta, matasContextKey)), &ctx) == nil && len(ctx.Goals) > 0
	contextNote := ""
	switch view, ok := matasContextView(p.Meta, now); {
	case !hasContext:
		contextNote = "Projektet har ingen kontekst endnu. Hvad er de 1–3 mål for perioden? (Et kontekst-udkast kan laves med multica_draft_project_context.)"
	case ok && view["stale"] == true:
		contextNote = fmt.Sprintf("Projektkonteksten er ikke ændret i %v dage. Er mål og prioriteter de samme?", view["days_since_update"])
	}

	decisions := []matasBriefItem{}
	if p.HasView {
		log := matasBuildDecisionLog(p.View.Posts)
		entries, err := h.matasProposalEntries(r.Context(), ws, member, p, log["proposals"].([]matasMemoryPost))
		if err != nil {
			return nil, err
		}
		// Only proposals this person may confirm wait for them; «Bekræft» on the project page decides.
		for _, e := range entries {
			if e.CanConfirm {
				decisions = append(decisions, matasBriefItem{Kind: "decision", ID: e.ID, Text: e.Text, Date: e.SaidAt, Area: e.Area, Source: matasPostSource(e.matasMemoryPost),
					Why: "a proposal in " + e.Area + ", where you hold the decision right; confirm it on the Beslutninger tab if it is decided"})
			}
		}
	}
	risks, questions := []matasBriefItem{}, []matasBriefItem{}
	if p.HasView {
		risks, questions = matasBriefMemory(p.View.Posts, roles, now)
	}
	proposals, err := h.matasContextProposalsFor(r, ws, project.ID, member)
	if err != nil {
		return nil, err
	}
	waiting := map[string][]matasBriefItem{"decisions": matasFirst(decisions, matasBriefMaxItems), "risks": risks,
		"milestones": matasBriefMilestones(ctx, now), "questions": questions, "proposals": proposals}

	roleViews := []map[string]string{}
	for _, r := range roles {
		roleViews = append(roleViews, map[string]string{"role": r, "title": matasRoleTitle[r]})
	}
	out := map[string]any{
		"project":   p.ref(),
		"person":    map[string]any{"name": person, "roles": roleViews},
		"waiting":   waiting,
		"questions": matasBriefQuestions(roles, waiting, contextNote),
		"send": map[string]any{"tool": "multica_add_project_update", "speaker": person, "source": "interview", "project_id": util.UUIDToString(project.ID),
			"note": "Show the person a short summary of what you noted and send it only after they approve it. Nothing is sent by this brief."},
		"follow_up": []string{
			"Er det en beslutning, eller skal den op i styregruppen?",
			"Hvem ejer det, og hvornår ved I mere?",
			"Er det en ny risiko, eller en vi kender?",
		},
	}
	if !p.HasView {
		out["note"] = "This project has no typed memory yet, so no decisions, risks or questions wait from it."
	}
	return out, nil
}

func matasInterviewTool(str func(string) map[string]any, obj func([]string, map[string]any) map[string]any, ro map[string]any) any {
	return map[string]any{"name": "multica_interview_brief", "description": "Prepare an interview with one of a project's diamond owners for their update: their role(s); what waits for that role (proposals only they may confirm, risks with nothing said on them for 14 days, milestones within two weeks, open questions, proposed context changes assigned to them), each with id and source; 3–5 questions, the concrete ones first and then the role's own (Business Owner: business case, benefits, priority; Product Owner: scope, user feedback, next delivery; Platform Owner: dependencies, operations, security; Technical Owner: architecture, technical choices, debt); and how to send the update afterwards. Only for yourself and only for a project where you hold a role; anyone else gets a refusal. Reads one project only and writes nothing.", "inputSchema": obj([]string{"project_id"}, map[string]any{
		"project_id": str("Project UUID or project number, e.g. 1004-2."),
		"person":     str("Optional: your name as the diamond has it, to check that the brief is for you."),
	}), "annotations": ro}
}
