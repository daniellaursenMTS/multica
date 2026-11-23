package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

// Synergy check (MATA-265): before something is built, what was done before, what is in progress,
// what is decided, and a recommendation. It uses only what the backend already has: the typed
// memory views, the projects' contexts and the issues in projects; no store of its own and no
// embeddings. An item matches when it contains words of the idea, or a term from a project
// context that the idea names, and the answer says which words matched. Issues without a project
// are left out.
//
// Sharing is as for multica_search_memory: content only from projects the caller may read; for
// any other project the project, how many items matched and whom to ask.

const (
	matasSynergyMaxTerms     = 30
	matasSynergyDefaultLimit = 10
	matasSynergyMaxLimit     = 50
	matasSynergyMaxIssues    = 2000
)

// matasStopwords are common Danish and English words that say nothing about what an idea is.
var matasStopwords = func() map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(`og at det den der til er som på de med for af ikke var han hun vi du jeg man kan skal vil har have
		være blive bliver om så men eller når hvis fra ud op ind efter før over under mellem også kun alle hver nogle noget nye ny nyt
		sin sit sine deres vores min mit mine din dit dine hvad hvem hvor hvordan hvorfor denne dette disse et ved mod uden igen lave
		laver lavet bruge bruger skulle kunne ville gøre gør får få mere mest meget godt bedre ønsker gerne lige skal sådan
		the and for with are was were this that these those you our your their from not can should would will does how what who why
		when where which about into new use using make build idea want need have has had all any some more most also just than then`) {
		out[w] = true
	}
	return out
}()

// matasIdeaTerms are the idea's words worth matching: at least three letters, no stop words,
// each once.
func matasIdeaTerms(idea string) []string {
	out := []string{}
	for _, t := range matasMemoryTerms(idea) {
		if len([]rune(t)) >= 3 && !matasStopwords[t] && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// matasSynergyNeed is how many terms an item must contain: a quarter of the idea's terms, at
// least one and at most three, so a long idea does not match on one common word.
func matasSynergyNeed(terms int) int {
	return min(3, max(1, (terms+3)/4))
}

// matasContextHay is the searchable text of a project context: goals, priorities, what to
// watch for and its terms with their meanings.
func matasContextHay(m map[string]json.RawMessage) (string, []string, bool) {
	var c matasContext
	if json.Unmarshal([]byte(metaString(m, matasContextKey)), &c) != nil {
		return "", nil, false
	}
	parts := append(append(append([]string{}, c.Goals...), c.Priorities...), c.WatchFor...)
	terms := []string{}
	for _, t := range c.Terms {
		parts = append(parts, t.Term, t.Meaning)
		terms = append(terms, strings.ToLower(t.Term))
	}
	return strings.Join(parts, " "), terms, true
}

type matasSynergyItem struct {
	Kind      string         `json:"kind"` // issue or memory
	ID        string         `json:"id"`   // e.g. MATA-12 or 1004-2/B-017
	Project   map[string]any `json:"project"`
	Type      string         `json:"type,omitempty"`
	Text      string         `json:"text"`
	Status    string         `json:"status"`
	Date      string         `json:"date"`
	Source    string         `json:"source,omitempty"`
	SourceURL string         `json:"source_url,omitempty"`
	Matched   []string       `json:"matched"`
}

// matasSynergyPart sorts posts into the four parts: events and facts were done or found before,
// risks and open questions are in progress, decisions, constraints and preferences are decided.
func matasSynergyPart(p matasMemoryPost) string {
	switch p.Type {
	case "event", "fact":
		return "done_before"
	case "risk", "question":
		return "in_progress"
	default:
		return "decided"
	}
}

type matasSynergyIssue struct {
	ID, Title, Description, Status, Project, Updated string
	Number                                           int32
}

// matasSynergyIssues are the issues in a project whose title or description contains one of the
// terms, newest first. Intake, portfolio context and Capex usage issues are bookkeeping, not work.
func (h *Handler) matasSynergyIssues(r *http.Request, ws pgtype.UUID, terms []string, since, until string) ([]matasSynergyIssue, map[string]int, error) {
	patterns := make([]string, len(terms))
	for i, t := range terms {
		patterns[i] = "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(t) + "%"
	}
	const work = `workspace_id = $1 AND project_id IS NOT NULL AND NOT (metadata ?| array['pf_intake', 'portfolio_context', 'capex_usage'])`
	counts := map[string]int{}
	rows, err := h.DB.Query(r.Context(), `SELECT project_id, count(*) FROM issue WHERE `+work+` GROUP BY project_id`, ws)
	if err != nil {
		return nil, nil, errors.New("could not read issues")
	}
	for rows.Next() {
		var pid pgtype.UUID
		var n int
		if err := rows.Scan(&pid, &n); err != nil {
			rows.Close()
			return nil, nil, errors.New("could not read issues")
		}
		counts[util.UUIDToString(pid)] = n
	}
	rows.Close()
	rows, err = h.DB.Query(r.Context(), `SELECT id, number, title, left(coalesce(description, ''), 4000), status, project_id, to_char(updated_at AT TIME ZONE 'Europe/Copenhagen', 'YYYY-MM-DD')
		FROM issue WHERE `+work+` AND (title ILIKE ANY($2) OR description ILIKE ANY($2))
		AND ($3 = '' OR updated_at >= $3::date) AND ($4 = '' OR updated_at < $4::date + 1)
		ORDER BY updated_at DESC LIMIT $5`, ws, patterns, since, until, matasSynergyMaxIssues)
	if err != nil {
		return nil, nil, errors.New("could not read issues")
	}
	defer rows.Close()
	out := []matasSynergyIssue{}
	for rows.Next() {
		var id, project pgtype.UUID
		var i matasSynergyIssue
		if err := rows.Scan(&id, &i.Number, &i.Title, &i.Description, &i.Status, &project, &i.Updated); err != nil {
			return nil, nil, errors.New("could not read issues")
		}
		i.ID, i.Project = util.UUIDToString(id), util.UUIDToString(project)
		out = append(out, i)
	}
	return out, counts, rows.Err()
}

func (h *Handler) mcpSynergyCheck(r *http.Request, raw json.RawMessage) (any, error) {
	var a struct {
		Idea      string `json:"idea"`
		ProjectID string `json:"project_id"`
		Since     string `json:"since"`
		Until     string `json:"until"`
		Limit     int    `json:"limit"`
	}
	if err := decodeMCPArguments(raw, &a); err != nil {
		return nil, err
	}
	idea := strings.TrimSpace(a.Idea)
	if idea == "" || len([]rune(idea)) > 4000 {
		return nil, errors.New("idea must be 1–4000 characters")
	}
	since, until := strings.TrimSpace(a.Since), strings.TrimSpace(a.Until)
	for _, d := range []string{since, until} {
		if d != "" && !matasDateRE.MatchString(d) {
			return nil, errors.New("since and until must be days as YYYY-MM-DD")
		}
	}
	limit := a.Limit
	if limit == 0 {
		limit = matasSynergyDefaultLimit
	}
	if limit < 1 || limit > matasSynergyMaxLimit {
		return nil, fmt.Errorf("limit must be 1–%d", matasSynergyMaxLimit)
	}
	ws, member, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	scope, err := h.matasMemoryScope(r, ws, member, nil, true)
	if err != nil {
		return nil, err
	}
	var about *matasMemoryProject
	if ref := strings.TrimSpace(a.ProjectID); ref != "" {
		project, err := h.matasProject(r, ws, ref)
		if err != nil {
			return nil, err
		}
		for i := range scope {
			if scope[i].ID == util.UUIDToString(project.ID) {
				about = &scope[i]
			}
		}
	} else if p, ok := matasProjectInText(idea, scope); ok {
		about = &p
	}

	// Terms: the idea's own words, plus any term from a readable project's context that the idea
	// names as a whole (e.g. «Knowledge Hub»).
	terms := matasIdeaTerms(idea)
	contextTerms := []string{}
	lowerIdea := strings.ToLower(idea)
	for _, p := range scope {
		if !p.Readable {
			continue
		}
		if _, ts, ok := matasContextHay(p.Meta); ok {
			for _, t := range ts {
				if strings.Contains(t, " ") && strings.Contains(lowerIdea, t) && !slices.Contains(contextTerms, t) {
					contextTerms = append(contextTerms, t)
				}
			}
		}
	}
	terms = append(contextTerms, terms...)
	if len(terms) > matasSynergyMaxTerms {
		terms = terms[:matasSynergyMaxTerms]
	}
	interpreted := map[string]any{"idea": idea, "terms": terms, "context_terms": contextTerms, "project": nil, "since": since, "until": until,
		"matching": fmt.Sprintf("an item counts when it contains at least %d of the terms", matasSynergyNeed(len(terms)))}
	if about != nil {
		interpreted["project"] = about.ref()
	}
	if since == "" && until == "" {
		interpreted["period"] = "all dates"
	}
	trail := newMatasTrail(interpreted)
	parts := map[string][]matasSynergyItem{"done_before": {}, "in_progress": {}, "decided": {}}
	if len(terms) == 0 {
		trail.miss("words in the idea to match on; describe what it does and for whom", "the person asking")
		trail.settle(false, "The idea has no words to match on. Ask for a sentence on what it does.")
		return map[string]any{"interpreted_as": interpreted, "done_before": parts["done_before"], "in_progress": parts["in_progress"],
			"decided": parts["decided"], "related_projects": []any{}, "recommendation": trail.Recommendation, "confidence": trail.Confidence,
			"withheld": []any{}, "trail": trail}, nil
	}
	need := matasSynergyNeed(len(terms))
	matches := func(hay string) []string {
		if m := matasMatched(hay, terms); len(m) >= need {
			return m
		}
		return nil
	}

	issues, issueCounts, err := h.matasSynergyIssues(r, ws, terms, since, until)
	if err != nil {
		return nil, err
	}
	issuesByProject := map[string][]matasSynergyIssue{}
	for _, i := range issues {
		issuesByProject[i.Project] = append(issuesByProject[i.Project], i)
	}
	prefix := h.getIssuePrefix(r.Context(), ws)

	withheld, related := []any{}, []any{}
	reviewedIssues := 0
	for _, p := range scope {
		if !p.Readable {
			n := 0
			for _, post := range p.View.Posts {
				if (since != "" && post.SaidAt < since) || (until != "" && post.SaidAt > until) {
					continue
				}
				if post.Status != "superseded" && matches(strings.Join([]string{post.Text, post.Topic, post.Area}, " ")) != nil {
					n++
				}
			}
			for _, i := range issuesByProject[p.ID] {
				if matches(i.Title+" "+i.Description) != nil {
					n++
				}
			}
			if hay, _, ok := matasContextHay(p.Meta); ok && matches(hay) != nil {
				n++
			}
			if n > 0 {
				withheld = append(withheld, map[string]any{"project": p.ref(), "matching": n, "ask": matasMemoryAsk(p.Meta),
					"note": "Not shared with you. Name the project and whom to ask; do not guess at its content."})
				trail.withhold(p, n)
			}
			continue
		}
		if hay, _, ok := matasContextHay(p.Meta); ok {
			trail.Reviewed.Sources = append(trail.Reviewed.Sources, matasTrailSource{Kind: "context", Project: p.ref(), Count: 1})
			if m := matches(hay); m != nil {
				related = append(related, map[string]any{"project": p.ref(), "matched": m, "ask": matasMemoryAsk(p.Meta)})
				trail.Used = append(trail.Used, matasTrailUsed{ID: matasNumbered(p.Number, p.Title) + " context", Project: p.ref(), Source: "project context",
					Why: "its goals, priorities or terms mention " + matasQuoted(m)})
			}
		}
		if n := issueCounts[p.ID]; n > 0 {
			reviewedIssues += n
			trail.Reviewed.Sources = append(trail.Reviewed.Sources, matasTrailSource{Kind: "issues", Project: p.ref(), Count: n})
		}
		for _, i := range issuesByProject[p.ID] {
			m := matches(i.Title + " " + i.Description)
			if m == nil {
				continue
			}
			part := "in_progress"
			if i.Status == "done" || i.Status == "cancelled" {
				part = "done_before"
			}
			parts[part] = append(parts[part], matasSynergyItem{Kind: "issue", ID: fmt.Sprintf("%s-%d", prefix, i.Number), Project: p.ref(),
				Text: i.Title, Status: i.Status, Date: i.Updated, Matched: m})
		}
		if !p.HasView {
			continue
		}
		trail.Reviewed.Posts += len(p.View.Posts)
		trail.Reviewed.Sources = append(trail.Reviewed.Sources, matasTrailSource{Kind: "memory", Project: p.ref(), Count: len(p.View.Posts), GeneratedAt: p.View.GeneratedAt})
		for _, post := range p.View.Posts {
			if (since != "" && post.SaidAt < since) || (until != "" && post.SaidAt > until) {
				continue
			}
			m := matches(strings.Join([]string{post.Text, post.Topic, post.Area}, " "))
			if m == nil {
				continue
			}
			switch post.Status {
			case "superseded":
				trail.discard(matasTrailDiscarded{ID: post.ID, Project: p.ref(), Reason: matasDiscardSuperseded, Note: "replaced by " + post.SupersededBy})
				continue
			case "proposal":
				trail.discard(matasTrailDiscarded{ID: post.ID, Project: p.ref(), Reason: matasDiscardProposal, Note: "said without the decision right; not a decision"})
				trail.miss(post.ID+" is only a proposal; whether it is decided is not settled", matasMemoryDecider(p.Meta, post.Area))
				continue
			}
			if post.Review != "" {
				trail.miss(post.ID+" awaits review: "+post.Review, matasMemoryAsk(p.Meta))
			}
			part := matasSynergyPart(post)
			parts[part] = append(parts[part], matasSynergyItem{Kind: "memory", ID: post.ID, Project: p.ref(), Type: post.Type, Text: post.Text,
				Status: post.Status, Date: post.SaidAt, Source: matasPostSource(post), SourceURL: post.SourceURL, Matched: m})
		}
	}
	trail.Reviewed.Issues = &reviewedIssues

	// Most terms matched first, then newest; each part is cut at the limit.
	best := 0
	for name, items := range parts {
		sort.SliceStable(items, func(i, j int) bool {
			if len(items[i].Matched) != len(items[j].Matched) {
				return len(items[i].Matched) > len(items[j].Matched)
			}
			if items[i].Date != items[j].Date {
				return items[i].Date > items[j].Date
			}
			return items[i].ID > items[j].ID
		})
		if len(items) > limit {
			items = items[:limit]
		}
		parts[name] = items
		for _, it := range items {
			best = max(best, len(it.Matched))
		}
	}
	for _, name := range []string{"decided", "in_progress", "done_before"} {
		for _, it := range parts[name] {
			why := strings.ReplaceAll(name, "_", " ") + ": " + it.Status + " " + it.Date + "; matches " + matasQuoted(it.Matched)
			if it.Kind == "memory" {
				why = it.Type + ", " + why
			}
			source := it.Source
			if it.Kind == "issue" {
				source = "issue " + it.ID
			}
			trail.Used = append(trail.Used, matasTrailUsed{ID: it.ID, Project: it.Project, Source: source, Why: why})
		}
	}

	ids := func(items []matasSynergyItem) string {
		out := []string{}
		for i, it := range items {
			if i == 3 {
				break
			}
			out = append(out, it.ID)
		}
		return strings.Join(out, ", ")
	}
	rec := []string{}
	if d := parts["decided"]; len(d) > 0 {
		rec = append(rec, fmt.Sprintf("Read what is decided first (%s); it may settle or limit the idea.", ids(d)))
	}
	if p := parts["in_progress"]; len(p) > 0 {
		rec = append(rec, fmt.Sprintf("Similar work is in progress (%s); talk to those owners before starting anything new.", ids(p)))
	}
	if p := parts["done_before"]; len(p) > 0 {
		rec = append(rec, fmt.Sprintf("Build on what was done before (%s) instead of starting over.", ids(p)))
	}
	if len(withheld) > 0 {
		rec = append(rec, fmt.Sprintf("%d projects you may not read also match; ask their owners (see withheld).", len(withheld)))
	}
	if len(trail.Used) == 0 && len(withheld) == 0 {
		trail.miss("anything similar in the projects you may read", "Knowledge Hub (multica_ask_project)")
		rec = append(rec, "Nothing similar found in what you may read. Say that the check only covers shared projects and words in common.")
	}
	trail.settle(best >= 2 && len(withheld) == 0, strings.Join(rec, " "))

	return map[string]any{
		"interpreted_as": interpreted,
		"done_before":    parts["done_before"], "in_progress": parts["in_progress"], "decided": parts["decided"],
		"related_projects": related, "recommendation": trail.Recommendation, "confidence": trail.Confidence,
		"withheld": withheld, "trail": trail,
	}, nil
}

func matasSynergyTool(str func(string) map[string]any, obj func([]string, map[string]any) map[string]any, ro map[string]any) any {
	return map[string]any{"name": "multica_synergy_check", "description": "Synergy check for an idea or task before it is built, in four parts: done_before (closed issues, events and facts), in_progress (open issues, risks and open questions), decided (decisions that hold, constraints, preferences) and a recommendation with confidence (low, medium, high). Matching is by the idea's words and the terms of project contexts it names; each item says which words matched. Uses only projects you may read; others are listed under withheld with the number of matches and whom to ask, never with content. Issues without a project are not included. trail shows how the answer was reached: interpreted_as, reviewed, used, discarded (superseded, proposal, not_shared), recommendation, confidence and missing. Cite ids and sources.", "inputSchema": obj([]string{"idea"}, map[string]any{
		"idea":       str("The idea or task in free text, 1–4000 characters."),
		"project_id": str("Optional project the idea belongs to: UUID or project number, e.g. 1000-3. The check still looks across every project you may read."),
		"since":      str("Only items from this day on, YYYY-MM-DD."),
		"until":      str("Only items up to this day, YYYY-MM-DD."),
		"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": matasSynergyMaxLimit, "description": fmt.Sprintf("At most this many items per part (default %d).", matasSynergyDefaultLimit)},
	}), "annotations": ro}
}
