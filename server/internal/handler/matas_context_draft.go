package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// A draft of a project context (MATA-266), so the owners correct and approve instead of filling
// in every field. The draft takes only what Multica already holds about the project: the diamond
// (decision rights), the intake's ClickUp link and the project's repositories (sources), its end
// date (a milestone) and the released risks in its typed memory (what to watch for). Every filled
// value names where it comes from. What cannot be derived stays empty, with a question for the
// interview: the goals, holes in the decision rights, and names that look like the project but are
// not its sources. Nothing that is already in the portfolio context is repeated.
//
// The portfolio has a draft too: the terms and risks that hold for every project, each with the
// document that defines it, plus terms and risks that two or more projects already wrote. Only
// workspace admins get it.
//
// A draft is never stored. multica_save_project_context stores an approved context as the calling
// person, through the same check as the «Kontekst» tab (matasContextGuard): only the diamond's
// owners and workspace admins, never agents.

const matasDocs = "https://github.com/daniellaursenMTS/matas-ai-automation/blob/main/"

type matasDraftSource struct {
	Field  string `json:"field"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

type matasDraftEmpty struct {
	Field string `json:"field"`
	Why   string `json:"why"`
}

type matasDraftQuestion struct {
	Field string   `json:"field"`
	Text  string   `json:"text"`
	Hints []string `json:"hints,omitempty"`
}

type matasDraft struct {
	ctx       matasContext
	sources   []matasDraftSource
	empty     []matasDraftEmpty
	questions []matasDraftQuestion
	leftOut   []matasDraftSource
}

func (d *matasDraft) cite(field, value, source string) {
	d.sources = append(d.sources, matasDraftSource{Field: field, Value: value, Source: source})
}

// matasCommonItem is a term or a risk that holds for every project, with the document defining it.
type matasCommonItem struct{ Term, Text, Doc string }

var (
	matasCommonTerms = []matasCommonItem{
		{"Diamant", "Projektets fire ejerroller: Business, Product, Platform og Technical Owner.", "diamond/README.md"},
		{"Hub", "Projektets vidensmappe i hermes med de noter, GDPR-gaten har frigivet.", "docs/knowledge-intelligence-poc.md"},
		{"GDPR-gaten", "Agenten GDPR Compliance Gate, der frigiver en opdatering, før noget når hub eller hukommelse.", "docs/knowledge-intelligence-poc.md"},
		{"Projektnummer", "Capex-nummer og løbenummer, fx 1004-2; kun projekter med et Capex har et.", "docs/knowledge-intelligence-poc.md"},
		{"Typet hukommelse", "Projektets poster (beslutning, fakta, risiko, begrænsning, åbent spørgsmål), hver med id og kilde.", "memory/README.md"},
		{"Forslag", "En beslutning sagt af en uden beslutningsretten; den gælder først, når den rette har bekræftet den.", "memory/README.md"},
	}
	matasCommonRisks = []matasCommonItem{
		{"", "Kun indhold, som GDPR-gaten har frigivet, må nå hub og hukommelse; rå noter deles aldrig.", "docs/knowledge-intelligence-poc.md"},
		{"", "Et projekt, der ikke er delt, gengives aldrig; agenten peger på projektet og dets ejer.", "docs/knowledge-intelligence-poc.md"},
		{"", "Multica og agenterne kører på én Mac, indtil Multica er hostet centralt.", "docs/knowledge-intelligence-poc.md"},
	}
	matasGitSSHRE = regexp.MustCompile(`^git@([^:]+):(.+)$`)
)

// matasRepoLink turns a repository URL into an https link and a short name (owner/repo).
func matasRepoLink(raw string) (string, string, bool) {
	link := strings.TrimSpace(raw)
	if m := matasGitSSHRE.FindStringSubmatch(link); m != nil {
		link = "https://" + m[1] + "/" + m[2]
	}
	link = strings.TrimSuffix(strings.TrimSuffix(link, "/"), ".git")
	if !matasContextURLRE.MatchString(link) {
		return "", "", false
	}
	path := strings.Split(strings.TrimPrefix(strings.TrimPrefix(link, "https://"), "http://"), "/")
	name := link
	if len(path) >= 3 {
		name = path[1] + "/" + path[2]
	}
	return link, name, true
}

// matasInPortfolio: the portfolio context already says this (same term, same link, or one text
// containing the other).
func matasInPortfolio(p *matasContext, kind, value string) bool {
	if p == nil {
		return false
	}
	v := matasNorm(value)
	switch kind {
	case "term":
		return slices.ContainsFunc(p.Terms, func(t matasTerm) bool { return matasNorm(t.Term) == v })
	case "source":
		return slices.ContainsFunc(p.Sources, func(s matasSource) bool { return strings.TrimSuffix(s.URL, "/") == strings.TrimSuffix(value, "/") })
	case "watch_for":
		return slices.ContainsFunc(p.WatchFor, func(w string) bool {
			w = matasNorm(w)
			return w == v || (len(w) > 20 && strings.Contains(v, w)) || (len(v) > 20 && strings.Contains(w, v))
		})
	}
	return false
}

// matasSimilarTitles are other projects whose title shares a word of four letters or more with
// this project's name: candidates for «ikke kilder», never filled in on their own.
func matasSimilarTitles(name string, aliases []string, own string, titles map[string]string) []string {
	words := []string{}
	for _, n := range append([]string{name}, aliases...) {
		for _, w := range matasMemoryTerms(n) {
			if len([]rune(w)) >= 4 && !matasStopwords[w] && !slices.Contains(words, w) {
				words = append(words, w)
			}
		}
	}
	out := []string{}
	for id, title := range titles {
		if id == own {
			continue
		}
		t := matasNorm(title)
		for _, w := range words {
			if matasWordIn(strings.Join(matasMemoryTerms(t), " "), w) && !slices.Contains(out, title) {
				out = append(out, title)
			}
		}
	}
	slices.Sort(out)
	return matasFirst(out, 5)
}

// matasProjectDraft builds the draft for one project from what Multica holds.
func (h *Handler) matasProjectDraft(r *http.Request, ws pgtype.UUID, project db.Project, m map[string]json.RawMessage, readable bool, posts []matasMemoryPost, portfolio *matasContext) (*matasDraft, error) {
	d := &matasDraft{ctx: matasContext{Goals: []string{}, Priorities: []string{}, Milestones: []matasMilestone{}, DecisionRights: map[string]string{},
		Sources: []matasSource{}, NotSources: []matasSource{}, WatchFor: []string{}, Terms: []matasTerm{}}}
	var spec struct {
		Name     string   `json:"name"`
		Aliases  []string `json:"aliases"`
		Archived bool     `json:"archived"`
		Roles    map[string]struct {
			Person string `json:"person"`
		} `json:"roles"`
	}
	hasDiamond := json.Unmarshal([]byte(metaString(m, "dia_spec")), &spec) == nil && !spec.Archived && spec.Roles != nil

	// Goals: never derived. The question carries what the owners wrote about the project elsewhere.
	hints := []string{}
	if s := strings.TrimSpace(project.Description.String); s != "" {
		hints = append(hints, "Projektbeskrivelsen i Multica: "+truncateRunes(s, 300))
	}
	if s := strings.TrimSpace(metaString(m, "pf_success_metrics")); s != "" {
		hints = append(hints, "Succeskriterier fra intake: "+truncateRunes(s, 300))
	}
	d.empty = append(d.empty, matasDraftEmpty{"goals", "Mål kan ikke udledes; ejerne skriver dem. Mål er det eneste felt, der er krævet."})
	d.questions = append(d.questions, matasDraftQuestion{"goals", "Hvad er projektets 1–3 mål for perioden, én sætning hver?", hints})

	// Decision rights: the person in each role of the diamond decides that area. An empty role
	// gives nobody the right, and the steering committee is never in the diamond: both are asked.
	holes := []string{}
	for _, area := range matasContextAreas {
		role, owned := matasAreaRole[area]
		person := ""
		if owned && hasDiamond {
			person = strings.TrimSpace(spec.Roles[role].Person)
		}
		if person == "" {
			holes = append(holes, area)
			continue
		}
		value := person + " (" + matasRoleTitle[role] + ")"
		d.ctx.DecisionRights[area] = value
		d.cite("decision_rights."+area, value, "diamanten: "+matasRoleTitle[role]+" er "+person)
	}
	if len(holes) > 0 {
		why := "Rollen er tom i diamanten, og en dækkende rolle giver ikke beslutningsret."
		if !hasDiamond {
			why = "Projektet har ingen diamant."
		}
		for _, area := range holes {
			if area == "steerco" {
				d.empty = append(d.empty, matasDraftEmpty{"decision_rights.steerco", "Styregruppen står ikke i diamanten."})
			} else {
				d.empty = append(d.empty, matasDraftEmpty{"decision_rights." + area, why})
			}
		}
		d.questions = append(d.questions, matasDraftQuestion{"decision_rights", "Hvem afgør for " + strings.Join(matasAreaNames(holes), ", ") + "? Skriv et navn eller et forum, eller lad det stå tomt.", nil})
	}

	// Sources: the intake's ClickUp link and the project's repositories in Multica.
	addSource := func(s matasSource, from string) {
		if matasInPortfolio(portfolio, "source", s.URL) {
			d.leftOut = append(d.leftOut, matasDraftSource{"sources", s.Name, "står allerede i porteføljekonteksten"})
			return
		}
		if slices.ContainsFunc(d.ctx.Sources, func(x matasSource) bool { return x.URL == s.URL }) || len(d.ctx.Sources) >= 15 {
			return
		}
		d.ctx.Sources = append(d.ctx.Sources, s)
		d.cite(fmt.Sprintf("sources[%d]", len(d.ctx.Sources)), s.Name+" "+s.URL, from)
	}
	if u := strings.TrimSpace(metaString(m, "pf_clickup_url")); matasContextURLRE.MatchString(u) {
		addSource(matasSource{Type: "clickup", Name: "ClickUp-opgave", URL: u}, "intake: ClickUp-link (pf_clickup_url)")
	}
	resources, err := h.Queries.ListProjectResources(r.Context(), project.ID)
	if err != nil {
		return nil, errors.New("could not read the project's resources")
	}
	local := 0
	for _, res := range resources {
		switch res.ResourceType {
		case "github_repo":
			var ref githubRepoRef
			if json.Unmarshal(res.ResourceRef, &ref) != nil {
				continue
			}
			if link, name, ok := matasRepoLink(ref.URL); ok {
				if l := strings.TrimSpace(res.Label.String); l != "" {
					name = l
				}
				addSource(matasSource{Type: "repo", Name: truncateRunes(name, 120), URL: link}, "Multica: projektets ressourcer (repo)")
			}
		case "local_directory":
			local++
		}
	}
	if len(d.ctx.Sources) == 0 {
		why := "Projektet har ingen ClickUp-link eller repo i Multica."
		if local > 0 {
			why += fmt.Sprintf(" Det har %d mappe(r) på en Mac, men en kilde skal have et link.", local)
		}
		d.empty = append(d.empty, matasDraftEmpty{"sources", why})
		d.questions = append(d.questions, matasDraftQuestion{"sources", "Hvor ligger projektets viden: ClickUp-liste, Teams-kanal, SharePoint-mappe, repo eller Figma? Giv et link til hver.", nil})
	}

	// Not sources: never filled in. Projects with a similar name are the usual candidates.
	titles := map[string]string{}
	if rows, err := h.DB.Query(r.Context(), `SELECT id, title FROM project WHERE workspace_id = $1`, ws); err == nil {
		for rows.Next() {
			var id pgtype.UUID
			var title string
			if rows.Scan(&id, &title) == nil {
				titles[util.UUIDToString(id)] = title
			}
		}
		rows.Close()
	}
	name := project.Title
	if spec.Name != "" {
		name = spec.Name + " " + project.Title
	}
	similar := matasSimilarTitles(name, spec.Aliases, util.UUIDToString(project.ID), titles)
	d.empty = append(d.empty, matasDraftEmpty{"not_sources", "Hvad der ikke er en kilde, kan kun ejerne sige."})
	q := matasDraftQuestion{Field: "not_sources", Text: "Er der kanaler, mapper eller projekter med lignende navne, som agenterne ikke må bruge som kilde?"}
	for _, t := range similar {
		q.Hints = append(q.Hints, "Multica-projekt med lignende navn: "+t)
	}
	d.questions = append(d.questions, q)

	// Milestones: the project's end date in Multica.
	if project.DueDate.Valid {
		day := project.DueDate.Time.Format("2006-01-02")
		d.ctx.Milestones = append(d.ctx.Milestones, matasMilestone{Date: day, What: "Projektets slutdato"})
		d.cite("milestones[1]", day+" Projektets slutdato", "Multica: projektets slutdato")
	} else {
		d.empty = append(d.empty, matasDraftEmpty{"milestones", "Projektet har ingen slutdato i Multica. Valgfrit."})
	}

	// Watch for: the risks in the released memory that hold now.
	if readable {
		for _, hit := range matasSearchPosts(posts, matasMemoryQuery{View: "current", Type: "risk"}) {
			p := hit.Post
			if len(d.ctx.WatchFor) >= 8 {
				break
			}
			text := truncateRunes(p.Text, 200)
			if matasInPortfolio(portfolio, "watch_for", text) {
				d.leftOut = append(d.leftOut, matasDraftSource{"watch_for", text, "står allerede i porteføljekonteksten"})
				continue
			}
			d.ctx.WatchFor = append(d.ctx.WatchFor, text)
			d.cite(fmt.Sprintf("watch_for[%d]", len(d.ctx.WatchFor)), text, p.ID+" ("+matasPostSource(p)+")")
		}
	} else {
		d.empty = append(d.empty, matasDraftEmpty{"watch_for", "Projektets hukommelse er ikke delt med dig, så dens risici er ikke læst."})
	}
	if len(d.ctx.WatchFor) == 0 && readable {
		d.empty = append(d.empty, matasDraftEmpty{"watch_for", "Ingen risici i projektets frigivne hukommelse. Valgfrit."})
	}

	d.empty = append(d.empty,
		matasDraftEmpty{"priorities", "Prioriteter kan ikke udledes. Valgfrit."},
		matasDraftEmpty{"terms", "Projektets egne termer kan ikke udledes; fælles termer hører til i porteføljekonteksten. Valgfrit."})
	return d, nil
}

func matasAreaNames(areas []string) []string {
	names := map[string]string{"business": "forretning", "product": "produkt", "platform": "platform", "technical": "teknik", "steerco": "styregruppen"}
	out := make([]string, len(areas))
	for i, a := range areas {
		out[i] = names[a]
	}
	return out
}

// matasPortfolioDraft: the terms and risks every project shares, and those two or more projects
// already wrote in their own context.
func (h *Handler) matasPortfolioDraft(r *http.Request, ws pgtype.UUID, current *matasContext) (*matasDraft, error) {
	d := &matasDraft{ctx: matasContext{Goals: []string{}, Priorities: []string{}, Milestones: []matasMilestone{}, DecisionRights: map[string]string{},
		Sources: []matasSource{}, NotSources: []matasSource{}, WatchFor: []string{}, Terms: []matasTerm{}}}
	for _, t := range matasCommonTerms {
		if matasInPortfolio(current, "term", t.Term) {
			d.leftOut = append(d.leftOut, matasDraftSource{"terms", t.Term, "står allerede i porteføljekonteksten"})
			continue
		}
		d.ctx.Terms = append(d.ctx.Terms, matasTerm{Term: t.Term, Meaning: t.Text})
		d.cite(fmt.Sprintf("terms[%d]", len(d.ctx.Terms)), t.Term, matasDocs+t.Doc)
	}
	for _, w := range matasCommonRisks {
		if matasInPortfolio(current, "watch_for", w.Text) {
			d.leftOut = append(d.leftOut, matasDraftSource{"watch_for", w.Text, "står allerede i porteføljekonteksten"})
			continue
		}
		d.ctx.WatchFor = append(d.ctx.WatchFor, w.Text)
		d.cite(fmt.Sprintf("watch_for[%d]", len(d.ctx.WatchFor)), w.Text, matasDocs+w.Doc)
	}
	// What two or more projects wrote the same way is common, not theirs alone.
	intakes, err := h.matasIntakes(r, ws)
	if err != nil {
		return nil, errors.New("could not read project fields")
	}
	type seen struct {
		text     string
		projects []string
	}
	terms, risks := map[string]*seen{}, map[string]*seen{}
	order := []string{}
	for _, m := range intakes {
		var c matasContext
		if json.Unmarshal([]byte(metaString(m, matasContextKey)), &c) != nil {
			continue
		}
		label := matasNumbered(metaString(m, matasNumberKey), matasSpecName(m))
		for _, t := range c.Terms {
			k := matasNorm(t.Term)
			if terms[k] == nil {
				terms[k] = &seen{text: t.Term + "\x00" + t.Meaning}
				order = append(order, "t"+k)
			}
			terms[k].projects = append(terms[k].projects, label)
		}
		for _, w := range c.WatchFor {
			k := matasNorm(w)
			if risks[k] == nil {
				risks[k] = &seen{text: w}
				order = append(order, "r"+k)
			}
			risks[k].projects = append(risks[k].projects, label)
		}
	}
	slices.Sort(order)
	for _, key := range order {
		kind, k := key[:1], key[1:]
		if kind == "t" && len(terms[k].projects) >= 2 && len(d.ctx.Terms) < 30 {
			parts := strings.SplitN(terms[k].text, "\x00", 2)
			if matasInPortfolio(current, "term", parts[0]) || matasInPortfolio(&d.ctx, "term", parts[0]) {
				continue
			}
			d.ctx.Terms = append(d.ctx.Terms, matasTerm{Term: parts[0], Meaning: parts[1]})
			d.cite(fmt.Sprintf("terms[%d]", len(d.ctx.Terms)), parts[0], "projektkonteksten i "+strings.Join(terms[k].projects, ", "))
		}
		if kind == "r" && len(risks[k].projects) >= 2 && len(d.ctx.WatchFor) < 8 {
			if matasInPortfolio(current, "watch_for", risks[k].text) || matasInPortfolio(&d.ctx, "watch_for", risks[k].text) {
				continue
			}
			d.ctx.WatchFor = append(d.ctx.WatchFor, risks[k].text)
			d.cite(fmt.Sprintf("watch_for[%d]", len(d.ctx.WatchFor)), risks[k].text, "projektkonteksten i "+strings.Join(risks[k].projects, ", "))
		}
	}
	d.empty = append(d.empty,
		matasDraftEmpty{"goals", "Porteføljens mål kan ikke udledes. Krævet."},
		matasDraftEmpty{"owner", "Hvem der ejer porteføljen, står ingen steder i Multica."},
		matasDraftEmpty{"priorities", "Kan ikke udledes. Valgfrit."},
		matasDraftEmpty{"decision_rights", "Hvem afgør på tværs, kan kun en admin sige. Valgfrit."})
	d.questions = append(d.questions,
		matasDraftQuestion{"goals", "Hvad er porteføljens 1–3 mål for perioden?", nil},
		matasDraftQuestion{"owner", "Hvem ejer porteføljen?", nil},
		matasDraftQuestion{"priorities", "Hvad er de tre vigtigste prioriteter på tværs lige nu?", nil},
		matasDraftQuestion{"terms", "Mangler der fælles termer eller risici, eller er nogle af dem i udkastet kun ét projekts?", nil})
	return d, nil
}

func matasSpecName(m map[string]json.RawMessage) string {
	var spec struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(metaString(m, "dia_spec")), &spec)
	return spec.Name
}

// matasDraftView is the draft as the MCP and the «Kontekst» tab get it.
func matasDraftView(scope string, ref map[string]any, d *matasDraft, current map[string]json.RawMessage) map[string]any {
	draft := map[string]any{"goals": d.ctx.Goals, "priorities": d.ctx.Priorities, "milestones": d.ctx.Milestones, "decision_rights": d.ctx.DecisionRights,
		"sources": d.ctx.Sources, "not_sources": d.ctx.NotSources, "watch_for": d.ctx.WatchFor, "terms": d.ctx.Terms}
	if scope == "portfolio" {
		draft["owner"] = ""
	}
	out := map[string]any{"scope": scope, "draft": draft, "sources": d.sources, "empty": d.empty, "questions": matasFirst(d.questions, 4),
		"left_out": d.leftOut, "required": []string{"goals"}, "most_important": []string{"goals", "decision_rights", "sources"},
		"note": "A draft; nothing is saved. Show it to the owner with where each value comes from, ask the questions, and save only what they approve: multica_save_project_context, or «Kontekst» on the project page. Never fill an empty field with a guess or a name that is not in the diamond."}
	if ref != nil {
		out["project"] = ref
	}
	if d.sources == nil {
		out["sources"] = []matasDraftSource{}
	}
	if d.leftOut == nil {
		out["left_out"] = []matasDraftSource{}
	}
	var c matasContext
	if raw := metaString(current, matasContextKey); raw != "" && json.Unmarshal([]byte(raw), &c) == nil {
		out["current"] = map[string]any{"updated_at": c.UpdatedAt, "updated_by": c.UpdatedBy}
	} else {
		out["current"] = nil
	}
	return out
}

// matasContextDraftFor drafts a project's context, or the portfolio's for "portfolio". Only those
// who may save it get it: the diamond's owners and workspace admins (the portfolio: admins).
func (h *Handler) matasContextDraftFor(r *http.Request, ws, member pgtype.UUID, ref string) (map[string]any, error) {
	m, err := h.getWorkspaceMember(r.Context(), util.UUIDToString(member), util.UUIDToString(ws))
	if err != nil {
		return nil, errors.New("only workspace members get a context draft")
	}
	admin := roleAllowed(m.Role, "owner", "admin")
	portfolioMeta, _, err := h.matasPortfolioContextIssue(r.Context(), ws)
	if err != nil {
		return nil, err
	}
	var portfolio *matasContext
	var pc matasContext
	if raw := metaString(portfolioMeta, matasContextKey); raw != "" && json.Unmarshal([]byte(raw), &pc) == nil {
		portfolio = &pc
	}
	if strings.EqualFold(strings.TrimSpace(ref), matasPortfolioRef) {
		if !admin {
			return nil, errors.New("only workspace owners and admins draft the portfolio context")
		}
		d, err := h.matasPortfolioDraft(r, ws, portfolio)
		if err != nil {
			return nil, err
		}
		return matasDraftView("portfolio", nil, d, portfolioMeta), nil
	}
	project, err := h.matasProject(r, ws, ref)
	if err != nil {
		return nil, err
	}
	scope, err := h.matasMemoryScope(r, ws, member, &project.ID, false)
	if err != nil || len(scope) != 1 {
		return nil, errors.New("project not found in this workspace")
	}
	p := scope[0]
	user, err := h.Queries.GetUser(r.Context(), member)
	if err != nil {
		return nil, errors.New("could not read your account")
	}
	if !admin && !matasContextEditorEmails(p.Meta)[strings.ToLower(strings.TrimSpace(user.Email))] {
		return nil, fmt.Errorf("only the diamond's owners and workspace admins get a draft of the context of %s; ask %s", matasNumbered(p.Number, p.Title), matasMemoryAsk(p.Meta))
	}
	d, err := h.matasProjectDraft(r, ws, project, p.Meta, p.Readable && p.HasView, p.View.Posts, portfolio)
	if err != nil {
		return nil, err
	}
	return matasDraftView("project", p.ref(), d, p.Meta), nil
}

func (h *Handler) mcpDraftProjectContext(r *http.Request, raw json.RawMessage) (any, error) {
	var a struct {
		ProjectID string `json:"project_id"`
	}
	if err := decodeMCPArguments(raw, &a); err != nil {
		return nil, err
	}
	ws, member, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	return h.matasContextDraftFor(r, ws, member, a.ProjectID)
}

// GetProjectContextDraft serves «Lav udkast» on the Kontekst tab and on the portfolio context.
func (h *Handler) GetProjectContextDraft(w http.ResponseWriter, r *http.Request) {
	ws, member, err := h.matasWorkspace(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.matasContextDraftFor(r, ws, member, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// matasContextIssue is the issue that holds the context: the project's intake, or the portfolio's
// issue. create makes the portfolio's issue when there is none yet, as the Projects page does.
func (h *Handler) matasContextIssue(r *http.Request, ws, member pgtype.UUID, ref string, create bool) (db.Issue, map[string]any, error) {
	var id pgtype.UUID
	if strings.EqualFold(strings.TrimSpace(ref), matasPortfolioRef) {
		rows, err := h.DB.Query(r.Context(), `SELECT id FROM issue WHERE workspace_id = $1 AND metadata @> '{"portfolio_context": true}'::jsonb ORDER BY created_at LIMIT 2`, ws)
		if err != nil {
			return db.Issue{}, nil, errors.New("could not read the portfolio context")
		}
		ids := []pgtype.UUID{}
		for rows.Next() {
			var x pgtype.UUID
			if rows.Scan(&x) == nil {
				ids = append(ids, x)
			}
		}
		rows.Close()
		switch {
		case len(ids) > 1:
			return db.Issue{}, nil, errors.New("the portfolio context is on more than one issue; a person must fix that")
		case len(ids) == 1:
			id = ids[0]
		case !create:
			return db.Issue{}, nil, errors.New("no portfolio context yet")
		default:
			m, err := h.getWorkspaceMember(r.Context(), util.UUIDToString(member), util.UUIDToString(ws))
			if err != nil || !roleAllowed(m.Role, "owner", "admin") {
				return db.Issue{}, nil, errors.New("only workspace owners and admins can edit the portfolio context")
			}
			result, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{WorkspaceID: ws, Title: "Porteføljekontekst", Status: "done", Priority: "none",
				Description: pgtype.Text{String: "Porteføljens kontekst: tværgående mål, prioriteter og ejer. Redigeres fra Projekter i Multica.", Valid: true},
				CreatorType: "member", CreatorID: member, AllowDuplicate: true}, service.IssueCreateOpts{Platform: "mcp", BroadcastPayload: h.mcpIssueBroadcast(r)})
			if err != nil {
				return db.Issue{}, nil, errors.New("could not create the portfolio context's issue")
			}
			issue, err := h.Queries.SetIssueMetadataKey(r.Context(), db.SetIssueMetadataKeyParams{ID: result.Issue.ID, WorkspaceID: ws, Key: matasPortfolioContextMark, Value: []byte("true")})
			if err != nil {
				return db.Issue{}, nil, errors.New("could not mark the portfolio context's issue")
			}
			return issue, map[string]any{"scope": "portfolio"}, nil
		}
		issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: ws})
		return issue, map[string]any{"scope": "portfolio"}, err
	}
	project, err := h.matasProject(r, ws, ref)
	if err != nil {
		return db.Issue{}, nil, err
	}
	if h.DB.QueryRow(r.Context(), `SELECT id FROM issue WHERE workspace_id = $1 AND project_id = $2 AND metadata @> '{"pf_intake": true}'::jsonb ORDER BY created_at LIMIT 1`, ws, project.ID).Scan(&id) != nil {
		return db.Issue{}, nil, errors.New("the project has no intake issue yet; save its context once under «Kontekst» on the project page")
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: ws})
	ref2 := map[string]any{"scope": "project", "project": map[string]any{"id": util.UUIDToString(project.ID), "title": project.Title}}
	if n := h.matasNumberOf(r.Context(), ws, project.ID); n != "" {
		ref2["project"].(map[string]any)["number"] = n
	}
	return issue, ref2, err
}

// mcpSaveProjectContext stores an approved context as the calling person. matasContextGuard makes
// the same checks as the Kontekst tab: members only, the diamond's owners and admins (the
// portfolio: admins), a valid value. based_on_updated_at refuses to overwrite a newer edit.
func (h *Handler) mcpSaveProjectContext(r *http.Request, raw json.RawMessage) (any, error) {
	var a struct {
		ProjectID string          `json:"project_id"`
		Context   json.RawMessage `json:"context"`
		BasedOn   *string         `json:"based_on_updated_at"`
	}
	if err := decodeMCPArguments(raw, &a); err != nil {
		return nil, err
	}
	if len(a.Context) == 0 || a.Context[0] != '{' {
		return nil, errors.New("context must be an object with the context's fields")
	}
	ws, member, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	issue, out, err := h.matasContextIssue(r, ws, member, a.ProjectID, true)
	if err != nil {
		return nil, err
	}
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(issue.Metadata, &m)
	if a.BasedOn != nil {
		var c matasContext
		_ = json.Unmarshal([]byte(metaString(m, matasContextKey)), &c)
		if strings.TrimSpace(*a.BasedOn) != c.UpdatedAt {
			return nil, fmt.Errorf("the context changed after the draft was made (now updated %s by %s); fetch it again and show the person the difference", c.UpdatedAt, c.UpdatedBy)
		}
	}
	value, _ := json.Marshal(string(a.Context))
	stored, status, msg := h.matasContextGuard(r, issue, matasContextKey, value)
	if status != 0 {
		return nil, errors.New(msg)
	}
	updated, err := h.Queries.SetIssueMetadataKey(r.Context(), db.SetIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: ws, Key: matasContextKey, Value: stored})
	if err != nil {
		slog.Warn("save project context failed", append(logger.RequestAttrs(r), "error", err, "issue_id", util.UUIDToString(issue.ID))...)
		return nil, errors.New("Multica could not save the context; nothing was changed")
	}
	h.publish(protocol.EventIssueMetadataChanged, util.UUIDToString(ws), "member", util.UUIDToString(member), map[string]any{
		"issue_id": util.UUIDToString(updated.ID), "metadata": parseIssueMetadata(updated.Metadata), "issue_revision": updated.Revision,
	})
	var saved string
	_ = json.Unmarshal(stored, &saved)
	out["saved"] = true
	out["context"] = json.RawMessage(saved)
	out["note"] = "Saved as you. The hub gets it with the next diamond sync (within a minute)."
	return out, nil
}

func matasContextDraftTools(str func(string) map[string]any, obj func([]string, map[string]any) map[string]any, ro map[string]any) []any {
	ctxSchema := map[string]any{"type": "object", "description": "The whole context as approved: goals (1–3, required), priorities, milestones [{date, what}], decision_rights {business, product, platform, technical, steerco}, sources [{type, name, url}], not_sources, watch_for, terms [{term, meaning}]; owner only for the portfolio. It replaces the stored context."}
	return []any{
		map[string]any{"name": "multica_draft_project_context", "description": "Draft a project's context from what Multica holds, so the owners correct and approve it instead of writing it from scratch: decision rights from the diamond, sources from the intake's ClickUp link and the project's repositories, the end date as a milestone, released risks from its typed memory as what to watch for. Every filled value is listed under sources with where it comes from; fields that cannot be derived stay empty (listed under empty), with 3–4 questions for the interview (goals, holes in the decision rights, names that look alike but are not sources). Nothing already in the portfolio context is repeated (left_out). Pass \"portfolio\" for a first draft of the portfolio context with the terms and risks every project shares (admins only). Only for the diamond's owners and workspace admins. Saves nothing.", "inputSchema": obj([]string{"project_id"}, map[string]any{
			"project_id": str("Project UUID, project number (e.g. 1000-3), or \"portfolio\"."),
		}), "annotations": ro},
		map[string]any{"name": "multica_save_project_context", "description": "Save a project's context as you, after you have shown the person the whole context and they approved it. The same server check as the «Kontekst» tab: only the diamond's owners and workspace admins (the portfolio: admins), never agents; the value is validated and stored with you as updated_by. Pass based_on_updated_at from the draft's current.updated_at (empty string when there was none) so a newer edit is never overwritten.", "inputSchema": obj([]string{"project_id", "context"}, map[string]any{
			"project_id":          str("Project UUID, project number, or \"portfolio\"."),
			"context":             ctxSchema,
			"based_on_updated_at": str("updated_at of the context the person saw; empty string if there was none."),
		}), "annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": true, "openWorldHint": false}},
	}
}
