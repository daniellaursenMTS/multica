package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

// Typed project memory (MATA-264): decisions, facts, risks, constraints, preferences, open
// questions and events, each with an id and a source. The posts are files in the project's hub
// (hermes/projects/<slug>/memory/, written by matas-ai-automation/memory/memory.py from released
// hub notes only); memory.py also writes one JSON view per project, which is mounted read-only at
// MATAS_MEMORY_DIR. Multica never writes them, and there is no database behind them.
//
// Who sees a project's posts follows the owners' choice: the diamond's owners (as for the project
// context) always, everyone else only when the owners share the whole project with Knowledge Hub.
// For any other project a search names the project, how many posts matched and whom to ask,
// and returns none of their content.

const (
	matasMemoryDirEnv     = "MATAS_MEMORY_DIR"
	matasMemoryDefaultDir = "/data/memory"
	matasMemoryMaxLimit   = 100
)

var (
	matasMemoryTypes    = []string{"decision", "fact", "risk", "constraint", "question", "preference", "event"}
	matasMemoryStatuses = []string{"proposal", "decision", "confirmed", "superseded", "recorded"}
	matasMemoryTermRE   = regexp.MustCompile(`[\p{L}\p{N}_]+`)
)

// matasMemoryPost is one post as memory.py writes it. Fields it does not set are left out.
type matasMemoryPost struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Text             string `json:"text"`
	Area             string `json:"area,omitempty"`
	Topic            string `json:"topic,omitempty"`
	SaidAt           string `json:"said_at"`
	SpeakerRole      string `json:"speaker_role,omitempty"`
	Speaker          string `json:"speaker,omitempty"`
	Source           string `json:"source"`
	SourceURL        string `json:"source_url,omitempty"`
	HubNote          string `json:"hub_note,omitempty"`
	Origin           string `json:"origin,omitempty"`
	GDPR             string `json:"gdpr,omitempty"`
	EventID          string `json:"event_id,omitempty"`
	RecordedBy       string `json:"recorded_by,omitempty"`
	RecordedAt       string `json:"recorded_at,omitempty"`
	Basis            string `json:"basis,omitempty"`
	Likelihood       string `json:"likelihood,omitempty"`
	Impact           string `json:"impact,omitempty"`
	ScoredBy         string `json:"scored_by,omitempty"`
	RiskNo           string `json:"risk_no,omitempty"`
	ReplacesProposed string `json:"replaces_proposed,omitempty"`
	Rev              int    `json:"rev,omitempty"`
	Status           string `json:"status"`
	Supersedes       string `json:"supersedes,omitempty"`
	SupersededBy     string `json:"superseded_by,omitempty"`
	ConfirmedBy      string `json:"confirmed_by,omitempty"`
	ConfirmedAt      string `json:"confirmed_at,omitempty"`
	ConfirmedByUser  string `json:"confirmed_by_user,omitempty"`
	ConfirmedVia     string `json:"confirmed_via,omitempty"`
	DeclinedBy       string `json:"declined_by,omitempty"`
	DeclinedByUser   string `json:"declined_by_user,omitempty"`
	DeclinedAt       string `json:"declined_at,omitempty"`
	Review           string `json:"review,omitempty"`
}

// matasMemoryView is one project's file in MATAS_MEMORY_DIR.
type matasMemoryView struct {
	Slug           string            `json:"slug"`
	Number         string            `json:"number"`
	Name           string            `json:"name"`
	MulticaProject string            `json:"multica_project"`
	GeneratedAt    string            `json:"generated_at"`
	Posts          []matasMemoryPost `json:"posts"`
}

func matasMemoryDir() string {
	if d := strings.TrimSpace(os.Getenv(matasMemoryDirEnv)); d != "" {
		return d
	}
	return matasMemoryDefaultDir
}

// matasLoadMemory reads every project's view, keyed by Multica project id. A missing folder is no
// memory yet; a file that cannot be read is skipped, so one bad file does not hide the rest.
func matasLoadMemory(dir string) map[string]matasMemoryView {
	out := map[string]matasMemoryView{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var v matasMemoryView
		if json.Unmarshal(raw, &v) != nil || v.MulticaProject == "" {
			continue
		}
		out[v.MulticaProject] = v
	}
	return out
}

// matasMemoryReadable: the diamond's owners always read a project's memory; others only when the
// owners have shared the whole project with Knowledge Hub and the sync has applied that choice.
func matasMemoryReadable(m map[string]json.RawMessage, email string) bool {
	if email = strings.ToLower(strings.TrimSpace(email)); email != "" && matasContextEditorEmails(m)[email] {
		return true
	}
	share := metaString(m, "kh_share")
	var ks struct {
		Mode string `json:"mode"`
	}
	return share != "" && share == metaString(m, "kh_synced") && json.Unmarshal([]byte(share), &ks) == nil && ks.Mode == "full"
}

// matasMemoryAsk is whom to ask about a project whose memory is not shared: its product owner,
// else its platform owner, as named in the diamond.
func matasMemoryAsk(m map[string]json.RawMessage) string {
	var spec struct {
		Roles map[string]struct {
			Person string `json:"person"`
		} `json:"roles"`
	}
	_ = json.Unmarshal([]byte(metaString(m, "dia_spec")), &spec)
	for _, r := range [][2]string{{"po", "Product Owner"}, {"plo", "Platform Owner"}, {"bo", "Business Owner"}} {
		if p := strings.TrimSpace(spec.Roles[r[0]].Person); p != "" {
			return r[1] + ": " + p
		}
	}
	return "the project's owners (no one named in its diamond)"
}

type matasMemoryQuery struct {
	Type   string
	Status string
	Since  string
	Until  string
	Query  string
	View   string // current (default) or log
}

type matasMemoryHit struct {
	Post  matasMemoryPost
	Score int
}

func matasMemoryTerms(q string) []string {
	out := []string{}
	for _, t := range matasMemoryTermRE.FindAllString(strings.ToLower(q), -1) {
		if len([]rune(t)) > 1 {
			out = append(out, t)
		}
	}
	return out
}

// matasSearchPosts filters one project's posts like memory.py search: the current memory leaves out
// superseded posts and events, the log has every post. Free text must hit text, topic, area,
// source or id; more hits rank higher, then newer.
func matasSearchPosts(posts []matasMemoryPost, q matasMemoryQuery) []matasMemoryHit {
	terms := matasMemoryTerms(q.Query)
	hits := []matasMemoryHit{}
	for _, p := range posts {
		if q.View != "log" && (p.Status == "superseded" || p.Type == "event") {
			continue
		}
		if (q.Type != "" && p.Type != q.Type) || (q.Status != "" && p.Status != q.Status) {
			continue
		}
		if (q.Since != "" && p.SaidAt < q.Since) || (q.Until != "" && p.SaidAt > q.Until) {
			continue
		}
		hay := strings.ToLower(strings.Join([]string{p.Text, p.Topic, p.Area, p.Source, p.ID}, " "))
		score := 0
		for _, t := range terms {
			score += strings.Count(hay, t)
		}
		if len(terms) > 0 && score == 0 {
			continue
		}
		hits = append(hits, matasMemoryHit{Post: p, Score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Post.SaidAt != b.Post.SaidAt {
			return a.Post.SaidAt > b.Post.SaidAt
		}
		return a.Post.ID > b.Post.ID
	})
	return hits
}

type matasDecisionEntry struct {
	matasMemoryPost
	Chain []matasMemoryPost `json:"chain"`
}

// matasBuildDecisionLog: decisions that hold, newest first, each with the chain of decisions it
// replaced; proposals waiting for whoever holds the decision right; posts awaiting review.
func matasBuildDecisionLog(posts []matasMemoryPost) map[string]any {
	byID := map[string]matasMemoryPost{}
	for _, p := range posts {
		byID[p.ID] = p
	}
	ordered := slices.Clone(posts)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].SaidAt != ordered[j].SaidAt {
			return ordered[i].SaidAt > ordered[j].SaidAt
		}
		return ordered[i].ID > ordered[j].ID
	})
	decisions, proposals, reviews := []matasDecisionEntry{}, []matasMemoryPost{}, []matasMemoryPost{}
	for _, p := range ordered {
		if p.Review != "" {
			reviews = append(reviews, p)
		}
		if p.Type != "decision" {
			continue
		}
		switch p.Status {
		case "decision", "confirmed":
			chain := []matasMemoryPost{}
			for prev := p.Supersedes; prev != "" && len(chain) < 50; {
				old, ok := byID[prev]
				if !ok {
					break
				}
				chain = append(chain, old)
				prev = old.Supersedes
			}
			decisions = append(decisions, matasDecisionEntry{matasMemoryPost: p, Chain: chain})
		case "proposal":
			proposals = append(proposals, p)
		}
	}
	return map[string]any{"decisions": decisions, "proposals": proposals, "reviews": reviews}
}

// matasMemoryScope is what one member may read: every project in the workspace with its intake
// metadata and memory view, and whether the member reads its posts.
type matasMemoryProject struct {
	ID       string
	Title    string
	Number   string
	Meta     map[string]json.RawMessage
	View     matasMemoryView
	HasView  bool
	Readable bool
}

func (p matasMemoryProject) ref() map[string]any {
	out := map[string]any{"id": p.ID, "title": p.Title}
	if p.Number != "" {
		out["number"] = p.Number
	}
	return out
}

func (h *Handler) matasMemoryScope(r *http.Request, ws, member pgtype.UUID, only *pgtype.UUID) ([]matasMemoryProject, error) {
	user, err := h.Queries.GetUser(r.Context(), member)
	if err != nil {
		return nil, errors.New("could not read your account")
	}
	intakes, err := h.matasIntakes(r, ws)
	if err != nil {
		return nil, errors.New("could not read project fields")
	}
	views := matasLoadMemory(matasMemoryDir())
	var ids []pgtype.UUID
	if only != nil {
		ids = []pgtype.UUID{*only}
	} else {
		for id := range views {
			if pid, err := util.ParseUUID(id); err == nil {
				ids = append(ids, pid)
			}
		}
	}
	out := []matasMemoryProject{}
	for _, pid := range ids {
		id := util.UUIDToString(pid)
		project, err := h.matasProject(r, ws, id)
		if err != nil {
			continue // a view for a project in another workspace, or one that is gone
		}
		m := intakes[id]
		v, has := views[id]
		out = append(out, matasMemoryProject{ID: id, Title: project.Title, Number: metaString(m, matasNumberKey), Meta: m,
			View: v, HasView: has, Readable: matasMemoryReadable(m, user.Email)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, nil
}

func matasMemoryFilters(raw json.RawMessage) (matasMemoryQuery, string, int, error) {
	var p struct {
		ProjectID string `json:"project_id"`
		Type      string `json:"type"`
		Status    string `json:"status"`
		Since     string `json:"since"`
		Until     string `json:"until"`
		Query     string `json:"query"`
		View      string `json:"view"`
		Limit     int    `json:"limit"`
	}
	if err := decodeMCPArguments(raw, &p); err != nil {
		return matasMemoryQuery{}, "", 0, err
	}
	q := matasMemoryQuery{Type: strings.TrimSpace(p.Type), Status: strings.TrimSpace(p.Status), Since: strings.TrimSpace(p.Since),
		Until: strings.TrimSpace(p.Until), Query: strings.TrimSpace(p.Query), View: strings.TrimSpace(p.View)}
	if q.Type != "" && !slices.Contains(matasMemoryTypes, q.Type) {
		return q, "", 0, fmt.Errorf("type must be one of %s", strings.Join(matasMemoryTypes, ", "))
	}
	if q.Status != "" && !slices.Contains(matasMemoryStatuses, q.Status) {
		return q, "", 0, fmt.Errorf("status must be one of %s", strings.Join(matasMemoryStatuses, ", "))
	}
	for _, d := range []string{q.Since, q.Until} {
		if d != "" && !matasDateRE.MatchString(d) {
			return q, "", 0, errors.New("since and until must be days as YYYY-MM-DD")
		}
	}
	if q.View == "" {
		q.View = "current"
	}
	if q.View != "current" && q.View != "log" {
		return q, "", 0, errors.New("view must be current or log")
	}
	if len([]rune(q.Query)) > 500 {
		return q, "", 0, errors.New("query is at most 500 characters")
	}
	limit := p.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > matasMemoryMaxLimit {
		return q, "", 0, fmt.Errorf("limit must be 1–%d", matasMemoryMaxLimit)
	}
	return q, strings.TrimSpace(p.ProjectID), limit, nil
}

// mcpSearchMemory searches the typed memory of one project or of every project the caller may read.
func (h *Handler) mcpSearchMemory(r *http.Request, raw json.RawMessage) (any, error) {
	q, projectRef, limit, err := matasMemoryFilters(raw)
	if err != nil {
		return nil, err
	}
	ws, member, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	var only *pgtype.UUID
	if projectRef != "" {
		project, err := h.matasProject(r, ws, projectRef)
		if err != nil {
			return nil, err
		}
		only = &project.ID
	}
	scope, err := h.matasMemoryScope(r, ws, member, only)
	if err != nil {
		return nil, err
	}
	type found struct {
		hit     matasMemoryHit
		project matasMemoryProject
	}
	all := []found{}
	withheld := []any{}
	reviewed, searched := 0, []any{}
	for _, p := range scope {
		hits := matasSearchPosts(p.View.Posts, q)
		if !p.Readable {
			if len(hits) > 0 {
				withheld = append(withheld, map[string]any{"project": p.ref(), "matching_posts": len(hits), "ask": matasMemoryAsk(p.Meta),
					"note": "Not shared with you. Name the project and whom to ask; do not guess at its content."})
			}
			continue
		}
		if !p.HasView {
			continue
		}
		reviewed += len(p.View.Posts)
		searched = append(searched, p.ref())
		for _, hit := range hits {
			all = append(all, found{hit, p})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i].hit, all[j].hit
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.Post.SaidAt > b.Post.SaidAt
	})
	posts := []any{}
	for i, f := range all {
		if i == limit {
			break
		}
		posts = append(posts, map[string]any{"project": f.project.ref(), "post": f.hit.Post})
	}
	out := map[string]any{
		"interpreted_as": map[string]any{"project": projectRef, "type": q.Type, "status": q.Status, "since": q.Since, "until": q.Until,
			"query": q.Query, "view": q.View},
		"searched_projects": searched, "posts_reviewed": reviewed, "matches": len(all), "posts": posts, "withheld": withheld,
	}
	if only != nil && len(scope) == 1 && scope[0].Readable && !scope[0].HasView {
		out["note"] = "This project has no typed memory yet."
	}
	return out, nil
}

// matasDecisionLogFor is one project's decision log for the calling member, or a note saying
// whom to ask when its memory is not shared with them.
func (h *Handler) matasDecisionLogFor(r *http.Request, ws, member pgtype.UUID, projectRef string) (map[string]any, error) {
	project, err := h.matasProject(r, ws, projectRef)
	if err != nil {
		return nil, err
	}
	scope, err := h.matasMemoryScope(r, ws, member, &project.ID)
	if err != nil {
		return nil, err
	}
	if len(scope) != 1 {
		return nil, errors.New("project not found in this workspace")
	}
	p := scope[0]
	out := map[string]any{"project": p.ref(), "shared": p.Readable}
	switch {
	case !p.Readable:
		out["note"] = "The owners have not shared this project's memory with you."
		out["ask"] = matasMemoryAsk(p.Meta)
	case !p.HasView:
		out["note"] = "This project has no typed memory yet."
		for k, v := range matasBuildDecisionLog(nil) {
			out[k] = v
		}
	default:
		log := matasBuildDecisionLog(p.View.Posts)
		proposals, err := h.matasProposalEntries(r.Context(), ws, member, p, log["proposals"].([]matasMemoryPost))
		if err != nil {
			return nil, err
		}
		log["proposals"] = proposals
		for k, v := range log {
			out[k] = v
		}
		out["generated_at"] = p.View.GeneratedAt
	}
	return out, nil
}

func (h *Handler) mcpDecisionLog(r *http.Request, raw json.RawMessage) (any, error) {
	var p struct {
		ProjectID string `json:"project_id"`
	}
	if err := decodeMCPArguments(raw, &p); err != nil {
		return nil, err
	}
	ws, member, err := h.matasWorkspace(r)
	if err != nil {
		return nil, err
	}
	return h.matasDecisionLogFor(r, ws, member, p.ProjectID)
}

// GetProjectDecisionLog serves the project page's «Beslutninger» tab with the same rules as the
// MCP tool.
func (h *Handler) GetProjectDecisionLog(w http.ResponseWriter, r *http.Request) {
	ws, member, err := h.matasWorkspace(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.matasDecisionLogFor(r, ws, member, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func matasMemoryTools(str func(string) map[string]any, obj func([]string, map[string]any) map[string]any, ro map[string]any) []any {
	enum := func(values []string, d string) map[string]any {
		return map[string]any{"type": "string", "enum": values, "description": d}
	}
	return []any{
		map[string]any{"name": "multica_search_memory", "description": "Search projects' typed memory: decisions, facts, risks, constraints, preferences, open questions and events, each with its id (e.g. 1004-2/B-017), the speaker's role, when it was said and its source (issue and hub note). Searches one project or all you may read; projects whose owners have not shared their memory with you are listed under withheld with whom to ask, never with content. Cite ids and sources in your answer.", "inputSchema": obj([]string{}, map[string]any{
			"project_id": str("Optional project UUID or project number (e.g. 1004-2). Without it, every project you may read."),
			"type":       enum(matasMemoryTypes, "Only posts of this type."),
			"status":     enum(matasMemoryStatuses, "Only posts with this status: proposal (said by someone without the decision right), decision, confirmed, superseded, recorded."),
			"since":      str("Said on or after this day, YYYY-MM-DD."),
			"until":      str("Said on or before this day, YYYY-MM-DD."),
			"query":      str("Free text; posts must contain at least one of its words."),
			"view":       enum([]string{"current", "log"}, "current (default): what holds now, without superseded posts and events. log: every post, events and superseded ones included."),
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": matasMemoryMaxLimit, "description": "At most this many posts (default 20)."},
		}), "annotations": ro},
		map[string]any{"name": "multica_decision_log", "description": "A project's decision log: the decisions that hold, each with the chain of decisions it replaced, the role that took it, when and its source; proposals waiting for whoever holds the decision right, each with who that is and any confirmation waiting for the next sync; posts awaiting review. Only that person turns a proposal into a decision, with «Bekræft» on the project page; agents never do. Only for projects whose memory you may read; otherwise it says whom to ask.", "inputSchema": obj([]string{"project_id"}, map[string]any{
			"project_id": str("Project UUID or project number, e.g. 1004-2."),
		}), "annotations": ro},
	}
}
