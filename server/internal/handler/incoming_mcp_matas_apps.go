package handler

import (
	_ "embed"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// MCP Apps (io.modelcontextprotocol/ui, MATA-284): five read-only tools also name a small view
// that Claude can draw in the chat: a project picker, the project context, memory cards with
// chips, the decision chain and the synergy check. The tools' results do not change: JSON as text
// plus structuredContent, so a client without MCP Apps, or one where the view does not appear,
// gets exactly the same data. The views only read what the host hands them; a click either asks
// the host to call one of these read-only tools again or puts a sentence in the chat.
//
// One HTML page serves every view. The view name picks how the result is turned into components
// (data) before they are drawn (DOM), so the components can later be described in A2UI
// (MATA-267) without rebuilding them. The page loads nothing from outside (no CSP domains).

const (
	matasAppMIME   = "text/html;profile=mcp-app"
	matasAppPrefix = "ui://matas-multica/"
)

//go:embed matas_apps/view.html
var matasAppHTML string

type matasAppView struct {
	View        string
	Name        string
	Description string
}

// matasAppViews: which tool draws which view.
var matasAppViews = map[string]matasAppView{
	"multica_list_projects":   {"projects", "Projektvælger", "The projects as a list to pick from."},
	"multica_project_context": {"context", "Projektkontekst", "A project's goals, milestones, decision rights and sources, with a project picker."},
	"multica_search_memory":   {"memory", "Projekthukommelse", "Memory posts as cards, with chips per type and the trail."},
	"multica_decision_log":    {"decisions", "Beslutningskæde", "The decisions that hold with the chain they replaced, and the proposals waiting."},
	"multica_synergy_check":   {"synergy", "Synergitjek", "Done before, in progress, decided and the recommendation, with the words that matched."},
}

func matasAppURI(view string) string { return matasAppPrefix + view + ".html" }

// matasWithApps points each tool that has a view at its resource. The tool definitions are built
// fresh on every tools/list, so they are changed in place.
func matasWithApps(tools []any) []any {
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		v, ok := matasAppViews[name]
		if !ok {
			continue
		}
		uri := matasAppURI(v.View)
		// ui/resourceUri is the older key some hosts still read; the spec keeps it until GA.
		m["_meta"] = map[string]any{"ui": map[string]any{"resourceUri": uri}, "ui/resourceUri": uri}
	}
	return tools
}

func matasAppResources() []any {
	views := make([]matasAppView, 0, len(matasAppViews))
	for _, v := range matasAppViews {
		views = append(views, v)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].View < views[j].View })
	out := make([]any, 0, len(views))
	for _, v := range views {
		out = append(out, map[string]any{"uri": matasAppURI(v.View), "name": v.Name, "description": v.Description, "mimeType": matasAppMIME})
	}
	return out
}

var errMatasAppNotFound = errors.New("resource not found")

// matasReadAppResource returns the page for one view.
func matasReadAppResource(raw json.RawMessage) (any, error) {
	var p struct {
		URI string `json:"uri"`
	}
	if json.Unmarshal(raw, &p) != nil || !strings.HasPrefix(p.URI, matasAppPrefix) {
		return nil, errMatasAppNotFound
	}
	for _, v := range matasAppViews {
		if matasAppURI(v.View) != p.URI {
			continue
		}
		html := strings.Replace(matasAppHTML, "__MATAS_VIEW__", v.View, 1)
		return map[string]any{"contents": []any{map[string]any{
			"uri": p.URI, "mimeType": matasAppMIME, "text": html,
			"_meta": map[string]any{"ui": map[string]any{"prefersBorder": true, "csp": map[string]any{}}},
		}}}, nil
	}
	return nil, errMatasAppNotFound
}
