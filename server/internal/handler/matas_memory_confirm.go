package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Confirming a proposal on the project page (MATA-282). A proposal in a project's typed memory
// becomes a decision only when the person who holds the decision right for its area says so,
// with their own Multica account: «Bekræft» on the Beslutninger tab. «Ikke en beslutning» closes
// the review and leaves the proposal as it is.
//
// Who holds the right follows memory.py's holds_right(): decision_rights in the project context
// names the person (or the role), else the diamond role that owns the area decides (business BO,
// product PO, platform PLO, technical TO; steerco only whom the context names). The person is
// linked to a Multica user by the e-mails the diamond has for their role (dia_spec, or the
// roster's links in ctx_editors). Unless that gives exactly one user, nobody can confirm, and the
// tab says why. Only members press the button, never agents, and admins get no shortcut.
//
// Multica never writes the hub. The confirmation is stored as metadata mem_confirm.<post id> (one
// key per post, e.g. mem_confirm.B-004) on the post's «Afklar hukommelse» task (mem_review), else
// on the project's intake issue. Only this endpoint writes those keys. memory.py
// apply-confirmations reads them, checks the right and the rev again, and updates the post.

const matasMemConfirmPrefix = "mem_confirm."

var (
	matasRoleOrder = []string{"bo", "po", "plo", "to"}
	matasAreaRole  = map[string]string{"business": "bo", "product": "po", "platform": "plo", "technical": "to"}
	matasRoleTitle = map[string]string{"bo": "Business Owner", "po": "Product Owner", "plo": "Platform Owner", "to": "Technical Owner"}
)

// matasMemConfirmReserved is true for the keys only the confirm endpoint writes.
func matasMemConfirmReserved(key string) bool {
	return key == "mem_confirm" || strings.HasPrefix(key, matasMemConfirmPrefix)
}

// matasMemConfirm is one mem_confirm.<post id> value: who pressed which button on which version.
type matasMemConfirm struct {
	Post   string `json:"post"`
	Action string `json:"action"` // confirm or decline
	Rev    int    `json:"rev"`
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"` // the person as the diamond names them
	Role   string `json:"role"` // the diamond role that holds the right
	At     string `json:"at"`
}

// matasDecisionHolder is who may confirm proposals in one area of one project.
type matasDecisionHolder struct {
	Named  string // decision_rights[area], when the context names someone
	Role   string
	Name   string
	Emails []string
	UserID pgtype.UUID
	Reason string // why nobody can confirm; empty when UserID is set
}

func matasNorm(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// matasWordIn: word occurs in hay as whole words (both already matasNorm'ed). Not a regexp \b,
// which in Go only knows ASCII and would miss names ending in æ, ø or å.
func matasWordIn(hay, word string) bool {
	if word == "" {
		return false
	}
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
	for i := 0; ; {
		j := strings.Index(hay[i:], word)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(word)
		before, _ := utf8.DecodeLastRuneInString(hay[:start])
		after, _ := utf8.DecodeRuneInString(hay[end:])
		if !isWord(before) && !isWord(after) {
			return true
		}
		i = start + 1
	}
}

// matasHolderFor finds the role and person that hold the decision right for an area, from the
// intake's dia_spec, ctx_spec and ctx_editors. It does not look up the Multica user.
func matasHolderFor(area string, m map[string]json.RawMessage) matasDecisionHolder {
	var spec struct {
		Archived bool `json:"archived"`
		Roles    map[string]struct {
			Person string `json:"person"`
			Email  string `json:"email"`
		} `json:"roles"`
	}
	if s := metaString(m, "dia_spec"); s == "" || json.Unmarshal([]byte(s), &spec) != nil {
		return matasDecisionHolder{Reason: "Projektet har ingen diamant, så ingen kan kobles til beslutningsretten."}
	}
	if spec.Archived {
		return matasDecisionHolder{Reason: "Diamanten er arkiveret."}
	}
	var ctx struct {
		DecisionRights map[string]string `json:"decision_rights"`
	}
	_ = json.Unmarshal([]byte(metaString(m, matasContextKey)), &ctx)
	person := func(r string) string { return strings.TrimSpace(spec.Roles[r].Person) }
	out := matasDecisionHolder{Named: strings.TrimSpace(ctx.DecisionRights[area])}
	roles := []string{}
	switch role, owned := matasAreaRole[area]; {
	case out.Named != "":
		// As holds_right(): the person's full or first name, or the role's name, in the entry.
		h := matasNorm(out.Named)
		for _, r := range matasRoleOrder {
			parts := strings.Fields(matasNorm(person(r)))
			if len(parts) == 0 {
				continue
			}
			if matasWordIn(h, strings.Join(parts, " ")) || matasWordIn(h, parts[0]) || strings.Contains(h, strings.ToLower(matasRoleTitle[r])) {
				roles = append(roles, r)
			}
		}
		if len(roles) == 0 {
			out.Reason = out.Named + " er ikke koblet til en Multica-bruger."
			return out
		}
	case !owned:
		out.Reason = "Projektkonteksten udpeger ingen, der afgør for styregruppen."
		return out
	case person(role) == "":
		out.Reason = "Ingen har rollen " + matasRoleTitle[role] + " i diamanten, og en dækkende rolle giver ikke beslutningsret."
		return out
	default:
		roles = []string{role}
	}
	names := []string{}
	for _, r := range roles {
		if !slices.Contains(names, person(r)) {
			names = append(names, person(r))
		}
	}
	if len(names) > 1 {
		out.Reason = fmt.Sprintf("«%s» passer på flere personer i diamanten (%s).", out.Named, strings.Join(names, ", "))
		return out
	}
	out.Name, out.Role = names[0], roles[0]
	if r := matasAreaRole[area]; slices.Contains(roles, r) {
		out.Role = r
	}
	editors := matasContextEditorsByRole(m)
	for _, r := range roles {
		for _, e := range append([]string{strings.ToLower(strings.TrimSpace(spec.Roles[r].Email))}, editors[r]...) {
			if matasContextEmailRE.MatchString(e) && !slices.Contains(out.Emails, e) {
				out.Emails = append(out.Emails, e)
			}
		}
	}
	return out
}

// matasHolderUser links the holder to exactly one member of the workspace by the diamond's e-mails.
func (h *Handler) matasHolderUser(ctx context.Context, ws pgtype.UUID, holder *matasDecisionHolder) error {
	if holder.Reason != "" {
		return nil
	}
	ids := []pgtype.UUID{}
	if len(holder.Emails) > 0 {
		rows, err := h.DB.Query(ctx, `SELECT DISTINCT u.id FROM "user" u JOIN member mb ON mb.user_id = u.id WHERE mb.workspace_id = $1 AND lower(u.email) = ANY($2)`, ws, holder.Emails)
		if err != nil {
			return errors.New("could not read the workspace's members")
		}
		defer rows.Close()
		for rows.Next() {
			var id pgtype.UUID
			if err := rows.Scan(&id); err != nil {
				return errors.New("could not read the workspace's members")
			}
			ids = append(ids, id)
		}
		if rows.Err() != nil {
			return errors.New("could not read the workspace's members")
		}
	}
	switch len(ids) {
	case 0:
		holder.Reason = holder.Name + " er ikke koblet til en Multica-bruger."
	case 1:
		holder.UserID = ids[0]
	default:
		holder.Reason = holder.Name + " er koblet til flere Multica-brugere; en admin må rette e-mails i diamanten."
	}
	return nil
}

// matasMemConfirms reads every confirmation stored for a project, keyed by the post's local id
// (B-004). Only the intake and «Afklar hukommelse» tasks hold them; the newest one per post wins.
func (h *Handler) matasMemConfirms(ctx context.Context, ws, project pgtype.UUID) (map[string]matasMemConfirm, error) {
	rows, err := h.DB.Query(ctx, `SELECT metadata FROM issue WHERE workspace_id = $1 AND project_id = $2 AND (metadata @> '{"pf_intake": true}'::jsonb OR metadata ? 'mem_review')`, ws, project)
	if err != nil {
		return nil, errors.New("could not read the confirmations")
	}
	defer rows.Close()
	out := map[string]matasMemConfirm{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, errors.New("could not read the confirmations")
		}
		m := map[string]json.RawMessage{}
		_ = json.Unmarshal(raw, &m)
		for k := range m {
			if !strings.HasPrefix(k, matasMemConfirmPrefix) {
				continue
			}
			var c matasMemConfirm
			if json.Unmarshal([]byte(metaString(m, k)), &c) != nil {
				continue
			}
			lid := strings.TrimPrefix(k, matasMemConfirmPrefix)
			if old, ok := out[lid]; !ok || c.At > old.At {
				out[lid] = c
			}
		}
	}
	return out, rows.Err()
}

func matasLocalID(id string) string {
	return id[strings.LastIndex(id, "/")+1:]
}

type matasHolderView struct {
	Name   string `json:"name,omitempty"`
	Role   string `json:"role,omitempty"`
	Named  string `json:"named,omitempty"`
	Linked bool   `json:"linked"`
	Reason string `json:"reason,omitempty"`
}

type matasConfirmView struct {
	Action string `json:"action"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	At     string `json:"at"`
	Rev    int    `json:"rev"`
	State  string `json:"state"` // pending: applied at the next sync; stale: made on an older rev
}

type matasProposalEntry struct {
	matasMemoryPost
	Holder       matasHolderView   `json:"holder"`
	CanConfirm   bool              `json:"can_confirm"`
	Confirmation *matasConfirmView `json:"confirmation,omitempty"`
}

// matasProposalEntries adds to each proposal who holds the right, whether the member may confirm
// it now and any confirmation waiting for memory.py.
func (h *Handler) matasProposalEntries(ctx context.Context, ws, member pgtype.UUID, p matasMemoryProject, proposals []matasMemoryPost) ([]matasProposalEntry, error) {
	out := []matasProposalEntry{}
	if len(proposals) == 0 {
		return out, nil
	}
	project, _ := util.ParseUUID(p.ID)
	confirms, err := h.matasMemConfirms(ctx, ws, project)
	if err != nil {
		return nil, err
	}
	holders := map[string]matasDecisionHolder{}
	for _, post := range proposals {
		holder, ok := holders[post.Area]
		if !ok {
			holder = matasHolderFor(post.Area, p.Meta)
			if err := h.matasHolderUser(ctx, ws, &holder); err != nil {
				return nil, err
			}
			holders[post.Area] = holder
		}
		e := matasProposalEntry{matasMemoryPost: post, Holder: matasHolderView{Name: holder.Name, Role: holder.Role, Named: holder.Named,
			Linked: holder.Reason == "", Reason: holder.Reason}}
		open := post.DeclinedAt == ""
		if c, ok := confirms[matasLocalID(post.ID)]; ok && open {
			state := "pending"
			if c.Rev != post.Rev {
				state = "stale"
			}
			e.Confirmation = &matasConfirmView{Action: c.Action, Name: c.Name, Role: c.Role, At: c.At, Rev: c.Rev, State: state}
			open = state == "stale"
		}
		e.CanConfirm = open && holder.Reason == "" && holder.UserID == member
		out = append(out, e)
	}
	return out, nil
}

// matasMemConfirmIssue is where a post's confirmation is stored: its «Afklar hukommelse» task in
// the project, else the project's intake.
func (h *Handler) matasMemConfirmIssue(ctx context.Context, ws, project pgtype.UUID, post matasMemoryPost) (db.Issue, error) {
	var id pgtype.UUID
	err := h.DB.QueryRow(ctx, `SELECT id FROM issue WHERE workspace_id = $1 AND project_id = $2 AND metadata->>'mem_review' IN ($3, $4) ORDER BY created_at DESC LIMIT 1`,
		ws, project, post.ID, matasLocalID(post.ID)).Scan(&id)
	if err != nil {
		err = h.DB.QueryRow(ctx, `SELECT id FROM issue WHERE workspace_id = $1 AND project_id = $2 AND metadata @> '{"pf_intake": true}'::jsonb ORDER BY created_at DESC LIMIT 1`,
			ws, project).Scan(&id)
	}
	if err != nil {
		return db.Issue{}, errors.New("the project has no intake issue to store the confirmation on")
	}
	return h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: ws})
}

// ConfirmProjectDecision: «Bekræft» (action confirm) or «Ikke en beslutning» (action decline) on a
// proposal in the Beslutninger tab. Returns the decision log as the tab shows it.
func (h *Handler) ConfirmProjectDecision(w http.ResponseWriter, r *http.Request) {
	ws, member, err := h.matasWorkspace(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if actorType, _ := h.resolveActor(r, requestUserID(r), util.UUIDToString(ws)); actorType != "member" {
		writeError(w, http.StatusForbidden, "agents cannot confirm a decision; only the person who holds the decision right does, on the project page")
		return
	}
	if _, err := h.getWorkspaceMember(r.Context(), requestUserID(r), util.UUIDToString(ws)); err != nil {
		writeError(w, http.StatusForbidden, "only workspace members can confirm a decision")
		return
	}
	var req struct {
		Post   string `json:"post"`
		Rev    int    `json:"rev"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Action != "confirm" && req.Action != "decline" {
		writeError(w, http.StatusBadRequest, "action must be confirm or decline")
		return
	}
	projectRef := chi.URLParam(r, "id")
	project, err := h.matasProject(r, ws, projectRef)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	scope, err := h.matasMemoryScope(r, ws, member, &project.ID, false)
	if err != nil || len(scope) != 1 {
		writeError(w, http.StatusNotFound, "project not found in this workspace")
		return
	}
	p := scope[0]
	if !p.HasView {
		writeError(w, http.StatusNotFound, "this project has no typed memory yet")
		return
	}
	lid := matasLocalID(strings.TrimSpace(req.Post))
	idx := slices.IndexFunc(p.View.Posts, func(x matasMemoryPost) bool { return matasLocalID(x.ID) == lid })
	if lid == "" || idx < 0 {
		writeError(w, http.StatusNotFound, "no post "+req.Post+" in this project")
		return
	}
	post := p.View.Posts[idx]
	switch {
	case post.Type != "decision" || post.Status != "proposal":
		writeError(w, http.StatusConflict, fmt.Sprintf("%s is not a proposal (it is a %s with status %s); only a proposal is confirmed here", post.ID, post.Type, post.Status))
		return
	case post.DeclinedAt != "":
		writeError(w, http.StatusConflict, post.ID+" is already marked as not a decision")
		return
	case req.Rev != post.Rev:
		writeError(w, http.StatusConflict, fmt.Sprintf("%s is at rev %d, not %d; reload the page and look at it again", post.ID, post.Rev, req.Rev))
		return
	}
	holder := matasHolderFor(post.Area, p.Meta)
	if err := h.matasHolderUser(r.Context(), ws, &holder); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if holder.Reason != "" {
		writeError(w, http.StatusForbidden, "nobody can confirm "+post.ID+" yet: "+holder.Reason)
		return
	}
	if holder.UserID != member || !p.Readable {
		writeError(w, http.StatusForbidden, fmt.Sprintf("only %s (%s) can confirm %s: they hold the decision right for its area", holder.Name, matasRoleTitle[holder.Role], post.ID))
		return
	}
	confirms, err := h.matasMemConfirms(r.Context(), ws, project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c, ok := confirms[lid]; ok && c.Rev == post.Rev {
		writeError(w, http.StatusConflict, fmt.Sprintf("%s was already answered by %s on %s; the hub is updated at the next sync", post.ID, c.Name, c.At[:10]))
		return
	}
	user, err := h.Queries.GetUser(r.Context(), member)
	if err != nil {
		writeError(w, http.StatusForbidden, "could not read your account")
		return
	}
	issue, err := h.matasMemConfirmIssue(r.Context(), ws, project.ID, post)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	key := matasMemConfirmPrefix + lid
	if existing := parseIssueMetadata(issue.Metadata); existing[key] == nil && len(existing) >= maxIssueMetadataKeys {
		writeError(w, http.StatusConflict, fmt.Sprintf("the issue that holds confirmations already has %d metadata keys", maxIssueMetadataKeys))
		return
	}
	record, _ := json.Marshal(matasMemConfirm{Post: post.ID, Action: req.Action, Rev: post.Rev, UserID: util.UUIDToString(member),
		Email: strings.ToLower(strings.TrimSpace(user.Email)), Name: holder.Name, Role: holder.Role, At: time.Now().UTC().Format(time.RFC3339)})
	value, _ := json.Marshal(string(record))
	updated, err := h.Queries.SetIssueMetadataKey(r.Context(), db.SetIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: ws, Key: key, Value: value})
	if err != nil {
		if isCheckViolation(err) {
			writeError(w, http.StatusConflict, "the issue that holds confirmations has no room left (8KB metadata limit); a person must make room")
			return
		}
		slog.Warn("ConfirmProjectDecision failed", append(logger.RequestAttrs(r), "error", err, "post", post.ID)...)
		writeError(w, http.StatusInternalServerError, "could not store the confirmation")
		return
	}
	h.publish(protocol.EventIssueMetadataChanged, util.UUIDToString(ws), "member", util.UUIDToString(member), map[string]any{
		"issue_id":       util.UUIDToString(updated.ID),
		"metadata":       parseIssueMetadata(updated.Metadata),
		"issue_revision": updated.Revision,
	})
	out, err := h.matasDecisionLogFor(r, ws, member, projectRef)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
