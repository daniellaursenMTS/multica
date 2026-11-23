package handler

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func incomingRPC(t *testing.T, user, method string, params any) map[string]any {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	req := testutil.WithHeaders(testutil.JSONRequest("POST", "/api/mcp", body), "Authorization", "Bearer mul_test", "X-User-ID", user, "X-Workspace-ID", testWorkspaceID)
	var response map[string]any
	testutil.Call(t, testHandler.IncomingMCP, req).Want(200).JSON(&response)
	return response
}

func TestIncomingMCPApps(t *testing.T) {
	init := incomingRPC(t, testUserID, "initialize", map[string]any{"protocolVersion": "2025-11-25"})
	if _, ok := init["result"].(map[string]any)["capabilities"].(map[string]any)["resources"]; !ok {
		t.Fatalf("initialize does not offer resources: %v", init["result"])
	}

	// The five read tools name their view; the writing tools do not.
	tools := incomingRPC(t, testUserID, "tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	withView := map[string]string{}
	for _, x := range tools {
		tool := x.(map[string]any)
		meta, ok := tool["_meta"].(map[string]any)
		if !ok {
			continue
		}
		uri := meta["ui"].(map[string]any)["resourceUri"].(string)
		if meta["ui/resourceUri"] != uri {
			t.Fatalf("%s: legacy key %v differs from %s", tool["name"], meta["ui/resourceUri"], uri)
		}
		if tool["annotations"].(map[string]any)["readOnlyHint"] != true {
			t.Fatalf("%s writes but has a view", tool["name"])
		}
		withView[tool["name"].(string)] = uri
	}
	want := map[string]string{
		"multica_list_projects":   "ui://matas-multica/projects.html",
		"multica_project_context": "ui://matas-multica/context.html",
		"multica_search_memory":   "ui://matas-multica/memory.html",
		"multica_decision_log":    "ui://matas-multica/decisions.html",
		"multica_synergy_check":   "ui://matas-multica/synergy.html",
	}
	if !reflect.DeepEqual(withView, want) {
		t.Fatalf("tools with a view: %v", withView)
	}

	// Every view is listed and readable as one self-contained page for its own view.
	resources := incomingRPC(t, testUserID, "resources/list", nil)["result"].(map[string]any)["resources"].([]any)
	if len(resources) != len(want) {
		t.Fatalf("resources/list: %v", resources)
	}
	external := regexp.MustCompile(`(?i)<(script|link|img|iframe)[^>]+(src|href)=|@import|url\(\s*['"]?https?:`)
	for _, x := range resources {
		res := x.(map[string]any)
		uri := res["uri"].(string)
		if res["mimeType"] != "text/html;profile=mcp-app" {
			t.Fatalf("%s: mimeType %v", uri, res["mimeType"])
		}
		read := incomingRPC(t, testUserID, "resources/read", map[string]any{"uri": uri})
		content := read["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)
		html := content["text"].(string)
		view := strings.TrimSuffix(strings.TrimPrefix(uri, "ui://matas-multica/"), ".html")
		if content["uri"] != uri || content["mimeType"] != "text/html;profile=mcp-app" || !strings.Contains(html, `data-view="`+view+`"`) || strings.Contains(html, "__MATAS_VIEW__") {
			t.Fatalf("%s: wrong page (%v, %v)", uri, content["uri"], content["mimeType"])
		}
		if external.MatchString(html) {
			t.Fatalf("%s loads something from outside: %s", uri, external.FindString(html))
		}
		if strings.Contains(html, "innerHTML") || strings.Contains(html, "insertAdjacentHTML") {
			t.Fatalf("%s writes data as HTML", uri)
		}
		csp := content["_meta"].(map[string]any)["ui"].(map[string]any)["csp"].(map[string]any)
		if len(csp) != 0 {
			t.Fatalf("%s asks for outside domains: %v", uri, csp)
		}
	}
	for _, uri := range []string{"ui://matas-multica/nope.html", "ui://other/projects.html", "file:///etc/passwd"} {
		resp := incomingRPC(t, testUserID, "resources/read", map[string]any{"uri": uri})
		if e, ok := resp["error"].(map[string]any); !ok || e["code"] != float64(-32002) {
			t.Fatalf("%s: %v", uri, resp)
		}
	}
}

// With or without a view, the tools answer with the same plain JSON: as text for any client, and
// as structuredContent for the view.
func TestIncomingMCPAppsPlainJSON(t *testing.T) {
	f := newMemoryFixture(t)
	po, poEmail := f.member("Petra")
	number := "MV" + f.tag + "-1"
	f.project("Alfa", number, map[string]any{"po": map[string]any{"person": "Petra", "email": poEmail}}, memoryPosts(), nil)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"multica_list_projects", map[string]any{}},
		{"multica_project_context", map[string]any{"project_id": number}},
		{"multica_search_memory", map[string]any{"project_id": number, "query": "hosting"}},
		{"multica_decision_log", map[string]any{"project_id": number}},
		{"multica_synergy_check", map[string]any{"idea": "hosting på Azure"}},
	} {
		resp := incomingRPC(t, po, "tools/call", map[string]any{"name": c.tool, "arguments": c.args})
		result := resp["result"].(map[string]any)
		if result["isError"] == true {
			t.Fatalf("%s: %v", c.tool, result["content"])
		}
		content := result["content"].([]any)
		if len(content) != 1 || content[0].(map[string]any)["type"] != "text" {
			t.Fatalf("%s: content %v", c.tool, content)
		}
		var fromText any
		if err := json.Unmarshal([]byte(content[0].(map[string]any)["text"].(string)), &fromText); err != nil {
			t.Fatalf("%s: text is not JSON: %v", c.tool, err)
		}
		if !reflect.DeepEqual(fromText, result["structuredContent"]) {
			t.Fatalf("%s: text and structuredContent differ", c.tool)
		}
	}
}
