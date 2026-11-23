package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
)

// The reader (workspace role viewer, MATA-286) is refused every route outside one allowlist.
// These tests walk the real router, so a route added later is covered without a change here.

var viewerRouteParam = regexp.MustCompile(`\{[^}]+\}`)

// viewerTestIs is whether a router middleware was made by the middleware package's function of
// this name. The compiler names an inlined closure differently, so the name is compared, not the
// function value.
func viewerTestIs(mw func(http.Handler) http.Handler, name string) bool {
	fn := runtime.FuncForPC(reflect.ValueOf(mw).Pointer())
	return fn != nil && strings.HasPrefix(fn.Name(), "github.com/multica-ai/multica/server/internal/middleware."+name+".")
}

func viewerTestGated(mw func(http.Handler) http.Handler) bool { return viewerTestIs(mw, "ViewerGate") }

// viewerTestUser makes a user with the given role in the integration workspace and a personal
// access token for them; the user is removed when the test ends.
func viewerTestUser(t *testing.T, role string) (string, string) {
	t.Helper()
	ctx := context.Background()
	email := "viewer-gate-" + role + "-" + uuid.NewString()[:8] + "@matas.test"
	var userID string
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email, onboarded_at) VALUES ($1, $2, now()) RETURNING id`, "Vera "+role, email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID) })
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)`, testWorkspaceID, userID, role); err != nil {
		t.Fatal(err)
	}
	token, err := auth.GeneratePATToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO personal_access_token (user_id, name, token_hash, token_prefix, expires_at) VALUES ($1, 'viewer gate test', $2, $3, $4)`,
		userID, auth.HashToken(token), token[:12], time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return userID, token
}

func viewerTestRequest(t *testing.T, token, method, path string, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, testServer.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestViewerGateCoversEveryAuthenticatedRoute: every route behind a login has the viewer gate,
// and every allowlist entry is a real route.
func TestViewerGateCoversEveryAuthenticatedRoute(t *testing.T) {
	routes := []string{}
	behindLogin := 0
	err := chi.Walk(testServer.Config.Handler.(chi.Routes), func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		routes = append(routes, method+" "+route)
		login := slices.ContainsFunc(mws, func(mw func(http.Handler) http.Handler) bool {
			return viewerTestIs(mw, "Auth") || viewerTestIs(mw, "DaemonAuth")
		})
		if login {
			behindLogin++
		}
		if login && !slices.ContainsFunc(mws, viewerTestGated) {
			t.Errorf("%s %s is behind a login but not behind the viewer gate", method, route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if behindLogin < 100 {
		t.Fatalf("found only %d routes behind a login; the walk does not see the middleware", behindLogin)
	}
	for _, entry := range middleware.ViewerAllowlist() {
		if !slices.Contains(routes, entry) {
			t.Errorf("allowlist entry %q is not a route", entry)
		}
	}
}

// TestViewerGateRefusesEveryRouteOffTheAllowlist sends a viewer's request to every gated route.
// The gate answers before any handler runs, so nothing is changed.
func TestViewerGateRefusesEveryRouteOffTheAllowlist(t *testing.T) {
	_, token := viewerTestUser(t, "viewer")
	checked := 0
	err := chi.Walk(testServer.Config.Handler.(chi.Routes), func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		if !slices.ContainsFunc(mws, viewerTestGated) {
			return nil
		}
		if middleware.ViewerAllowed(method, route) || method == "CONNECT" || method == "TRACE" {
			return nil
		}
		// The workspace is the viewer's own; any other id fails the route's own membership check.
		path := strings.Replace(route, "/api/workspaces/{id}", "/api/workspaces/"+testWorkspaceID, 1)
		path = strings.ReplaceAll(viewerRouteParam.ReplaceAllString(path, uuid.NewString()), "*", "x")
		status, body := viewerTestRequest(t, token, method, path, "{}")
		if status != http.StatusForbidden || (method != "HEAD" && !strings.Contains(body, "readers")) {
			t.Errorf("viewer %s %s: %d %s", method, route, status, body)
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 100 {
		t.Fatalf("only %d routes checked", checked)
	}
}

func TestViewerGate(t *testing.T) {
	_, viewer := viewerTestUser(t, "viewer")
	_, member := viewerTestUser(t, "member")
	issue := uuid.NewString()
	// Common routes: 403 for a viewer, open to a member.
	for _, path := range []string{"/api/issues", "/api/agents", "/api/runtimes", "/api/projects", "/api/workspaces/" + testWorkspaceID + "/members", "/api/me/onboarding"} {
		method := "GET"
		if strings.HasSuffix(path, "onboarding") {
			method = "PATCH"
		}
		if status, body := viewerTestRequest(t, viewer, method, path, "{}"); status != http.StatusForbidden {
			t.Errorf("viewer %s %s: %d %s", method, path, status, body)
		}
	}
	if status, _ := viewerTestRequest(t, viewer, "PUT", "/api/issues/"+issue+"/metadata/dia_spec", `{"value":"x"}`); status != http.StatusForbidden {
		t.Errorf("viewer set metadata: %d", status)
	}
	for _, path := range []string{"/api/issues", "/api/agents", "/api/runtimes", "/api/workspaces/" + testWorkspaceID + "/members"} {
		if status, body := viewerTestRequest(t, member, "GET", path, ""); status != http.StatusOK {
			t.Errorf("member GET %s: %d %s", path, status, body)
		}
	}
	// The allowlist: MCP, who they are, their own role and their tokens.
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{"GET", "/api/me", ""},
		{"GET", "/api/workspaces", ""},
		{"GET", "/api/workspaces/" + testWorkspaceID + "/membership", ""},
		{"GET", "/api/tokens", ""},
	} {
		status, body := viewerTestRequest(t, viewer, tc.method, tc.path, tc.body)
		if status != http.StatusOK {
			t.Errorf("viewer %s %s: %d %s", tc.method, tc.path, status, body)
		}
		if tc.path == "/api/workspaces/"+testWorkspaceID+"/membership" {
			var m struct {
				Role string `json:"role"`
			}
			if json.Unmarshal([]byte(body), &m) != nil || m.Role != "viewer" {
				t.Errorf("membership: %s", body)
			}
		}
	}
	// A viewer's MCP: only the read tools, and writing tools refused.
	_, body := viewerTestRequest(t, viewer, "POST", "/api/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"multica_create_task","arguments":{}}}`)
	if !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "readers") {
		t.Errorf("viewer create_task: %s", body)
	}
}
