package middleware

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The workspace role viewer (Matas, MATA-286): a reader who finds knowledge through the project
// portal and the MCP read tools and never uses Multica's own interface. This file is the one rule
// for it, so no handler has to know about viewers: a viewer gets 403 on every route that is not
// on viewerAllowlist. A route added later, upstream or by us, is closed to viewers until it is
// put on the list on purpose. Which MCP tools a viewer may call is decided in the MCP dispatcher.
//
// The rule follows the workspace a request is about: the {id} of /api/workspaces/{id}/…, else the
// workspace headers and query the other middleware read. A request about no workspace (/api/me,
// /api/tokens, …) is gated when the user is a viewer in every workspace they belong to.

const RoleViewer = "viewer"

// viewerAllowlist is every route a viewer may call, as "METHOD pattern" with the pattern chi
// matches, and why.
var viewerAllowlist = map[string]string{
	"POST /api/mcp": "the MCP endpoint; the dispatcher allows a viewer only the read tools and multica_ask_project",
	"GET /api/me":   "who is signed in, for the page that points a viewer to the portal",
	"PATCH /api/me": "a viewer's own name",
	// The workspace list and the caller's own membership tell the frontend to show a viewer the
	// portal page instead of Multica.
	"GET /api/workspaces/":                "the workspaces the user belongs to",
	"GET /api/workspaces/{id}/membership": "the caller's own role in the workspace",
	"GET /api/invitations":                "the user's own invitations",
	"GET /api/invitations/{id}":           "one of the user's own invitations",
	"POST /api/invitations/{id}/accept":   "accepting an invitation, which is how a viewer joins",
	"POST /api/invitations/{id}/decline":  "declining an invitation",
	"GET /api/tokens/":                    "the user's personal access tokens, which MCP clients sign in with",
	"POST /api/tokens/":                   "creating a personal access token for an MCP client",
	"POST /api/tokens/current/renew":      "renewing the token an MCP client uses",
	"DELETE /api/tokens/{id}":             "revoking one of the user's own tokens",
}

// ViewerAllowed reports whether a viewer may call the route with this method and chi pattern.
func ViewerAllowed(method, pattern string) bool {
	_, ok := viewerAllowlist[method+" "+pattern]
	return ok
}

// ViewerAllowlist returns the allowlist's keys, for tests that check it against the router.
func ViewerAllowlist() []string {
	out := make([]string, 0, len(viewerAllowlist))
	for k := range viewerAllowlist {
		out = append(out, k)
	}
	return out
}

// IsViewer is true for the reader role. The realtime socket uses it too.
func IsViewer(role string) bool {
	return role == RoleViewer
}

// ViewerGate refuses a viewer every route in routes that is not on the allowlist. It runs after
// the authentication middleware, which sets X-User-ID; requests without a user pass through to
// the route's own checks.
func ViewerGate(queries *db.Queries, routes chi.Routes) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := r.Header.Get("X-User-ID")
			if userID == "" {
				next.ServeHTTP(w, r)
				return
			}
			viewer, err := viewerOnRequest(r, queries, userID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "could not check your workspace role")
				return
			}
			if viewer && !viewerRouteAllowed(routes, r.Method, r.URL.Path) {
				writeError(w, http.StatusForbidden, "readers use the project portal and the MCP read tools; this part of Multica is not open to them")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// viewerRouteAllowed is whether a viewer may call the route chi serves the request with. A
// mounted router serves /api/tokens with its "/" route, which the router walk lists as
// /api/tokens/; Find reports the request without the slash as /api/tokens.
func viewerRouteAllowed(routes chi.Routes, method, path string) bool {
	if ViewerAllowed(method, routes.Find(chi.NewRouteContext(), method, path)) {
		return true
	}
	return !strings.HasSuffix(path, "/") && ViewerAllowed(method, routes.Find(chi.NewRouteContext(), method, path+"/"))
}

// viewerOnRequest is whether the user is a viewer in the workspace the request is about, or in
// every workspace they belong to when it is about none. A workspace the user is not a member of
// is left to the route's own membership check.
func viewerOnRequest(r *http.Request, queries *db.Queries, userID string) (bool, error) {
	uid, err := util.ParseUUID(userID)
	if err != nil {
		return false, nil
	}
	ws := workspaceInPath(r.URL.Path)
	if ws == "" {
		ws = ResolveWorkspaceIDFromRequest(r, queries)
	}
	if ws != "" {
		wid, err := util.ParseUUID(ws)
		if err != nil {
			return false, nil
		}
		member, err := queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: uid, WorkspaceID: wid})
		if err != nil {
			return false, nil
		}
		return IsViewer(member.Role), nil
	}
	workspaces, err := queries.ListWorkspaces(r.Context(), uid)
	if err != nil {
		return false, err
	}
	if len(workspaces) == 0 {
		return false, nil
	}
	for _, w := range workspaces {
		member, err := queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: uid, WorkspaceID: w.ID})
		if err != nil {
			return false, err
		}
		if !IsViewer(member.Role) {
			return false, nil
		}
	}
	return true, nil
}

// workspaceInPath is the {id} of /api/workspaces/{id}/…, when it is a UUID.
func workspaceInPath(path string) string {
	rest, ok := strings.CutPrefix(path, "/api/workspaces/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	if _, err := util.ParseUUID(id); err != nil {
		return ""
	}
	return id
}
