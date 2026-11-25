package daemon

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSecret marks every value in the fake runtime configurations that must
// never reach an agent's MCP config or the daemon log.
const fakeSecret = "Bearer runtime-user-secret"

// writeFakeClaudeUserConfig writes a ~/.claude.json with a user-scope server
// carrying an Authorization header, and a local-scope server (what
// `claude mcp add --scope local` writes) keyed to another project directory.
func writeFakeClaudeUserConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := `{
  "mcpServers": {
    "user-hub": {"type": "http", "url": "https://hub.example/mcp", "headers": {"Authorization": "` + fakeSecret + `"}}
  },
  "projects": {
    "` + filepath.Join(home, "dev", "own-project") + `": {
      "mcpServers": {
        "local-hub": {"type": "http", "url": "https://local.example/mcp", "headers": {"Authorization": "` + fakeSecret + `"}}
      }
    }
  }
}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func writeFakeCodexUserConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := `[mcp_servers.user_hub]
url = "https://hub.example/mcp"
http_headers = { Authorization = "` + fakeSecret + `" }
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func taskMcpServers(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var document struct {
		McpServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("task MCP config %q is not a managed document: %v", raw, err)
	}
	if document.McpServers == nil {
		t.Fatalf("task MCP config %q has no mcpServers map", raw)
	}
	return document.McpServers
}

func newCapturedLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return captureLogger(&buf), &buf
}

// Without an allow list, a strict provider gets only the agent's own servers,
// and an agent without mcp_config gets an empty managed set instead of native
// inheritance, so Claude launches with --strict-mcp-config and Codex strips
// the user's tables.
func TestResolveTaskMcpConfigDeniesRuntimeServersByDefault(t *testing.T) {
	cases := []struct {
		provider string
		setup    func(*testing.T)
		runtime  string
	}{
		{"claude", func(t *testing.T) { writeFakeClaudeUserConfig(t) }, "user-hub"},
		{"codex", writeFakeCodexUserConfig, "user_hub"},
	}
	agentConfigs := map[string]json.RawMessage{
		"no mcp_config": nil,
		"null":          json.RawMessage("null"),
		"empty":         json.RawMessage(`{}`),
		"own server":    json.RawMessage(`{"mcpServers":{"own":{"command":"own-server"}}}`),
	}
	for _, tc := range cases {
		for label, agentConfig := range agentConfigs {
			t.Run(tc.provider+"/"+label, func(t *testing.T) {
				tc.setup(t)
				logger, logs := newCapturedLogger()

				got := resolveTaskMcpConfig(tc.provider, agentConfig, "", logger)

				servers := taskMcpServers(t, got)
				if _, leaked := servers[tc.runtime]; leaked {
					t.Fatalf("runtime server %q reached the agent: %s", tc.runtime, got)
				}
				if strings.Contains(string(got), fakeSecret) || strings.Contains(string(got), "hub.example") {
					t.Fatalf("runtime config leaked into task config: %s", got)
				}
				want := 0
				if label == "own server" {
					want = 1
				}
				if len(servers) != want || (want == 1 && servers["own"] == nil) {
					t.Fatalf("servers = %v, want only the agent's own", servers)
				}
				if logs.Len() != 0 {
					t.Fatalf("nothing was passed on, so nothing should be logged: %s", logs)
				}
			})
		}
	}
}

// A runtime server named in MULTICA_RUNTIME_MCP_ALLOW is passed on, the
// agent's own servers stay, and the log carries the name only.
func TestResolveTaskMcpConfigPassesAllowedRuntimeServers(t *testing.T) {
	cases := []struct {
		provider string
		setup    func(*testing.T)
		runtime  string
	}{
		{"claude", func(t *testing.T) { writeFakeClaudeUserConfig(t) }, "user-hub"},
		{"codex", writeFakeCodexUserConfig, "user_hub"},
	}
	for _, tc := range cases {
		for label, agentConfig := range map[string]json.RawMessage{
			"no mcp_config": nil,
			"own server":    json.RawMessage(`{"mcpServers":{"own":{"command":"own-server"}}}`),
		} {
			t.Run(tc.provider+"/"+label, func(t *testing.T) {
				tc.setup(t)
				logger, logs := newCapturedLogger()

				got := resolveTaskMcpConfig(tc.provider, agentConfig, " other , "+tc.runtime, logger)

				servers := taskMcpServers(t, got)
				if _, ok := servers[tc.runtime]; !ok {
					t.Fatalf("allowed runtime server %q missing: %v", tc.runtime, servers)
				}
				if agentConfig != nil {
					if _, ok := servers["own"]; !ok {
						t.Fatalf("agent's own server dropped: %v", servers)
					}
				}
				logged := logs.String()
				if !strings.Contains(logged, tc.runtime) || !strings.Contains(logged, runtimeMcpAllowEnv) {
					t.Fatalf("log does not name the passed server: %s", logged)
				}
				for _, secret := range []string{fakeSecret, "runtime-user-secret", "hub.example", "Authorization", "own-server"} {
					if strings.Contains(logged, secret) {
						t.Fatalf("log contains %q: %s", secret, logged)
					}
				}
				// "other" is allowed but not configured; it must not appear as passed.
				if strings.Contains(logged, "other") {
					t.Fatalf("log names a server that was not passed: %s", logged)
				}
			})
		}
	}
}

// A `--scope local` server lives under projects[<dir>] in ~/.claude.json. The
// daemon never reads that map, so it cannot be passed on even when its name
// is allowed; Claude itself ignores it under --strict-mcp-config.
func TestResolveTaskMcpConfigNeverPassesClaudeLocalScope(t *testing.T) {
	writeFakeClaudeUserConfig(t)
	logger, _ := newCapturedLogger()

	got := resolveTaskMcpConfig("claude", nil, "local-hub", logger)

	if servers := taskMcpServers(t, got); len(servers) != 0 {
		t.Fatalf("local-scope server reached the agent: %v", servers)
	}
}

// A malformed user configuration must not reopen native inheritance: the task
// still gets a managed (empty) set.
func TestResolveTaskMcpConfigFailsClosedOnBrokenUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":`), 0o600); err != nil {
		t.Fatal(err)
	}
	logger, logs := newCapturedLogger()

	got := resolveTaskMcpConfig("claude", nil, "user-hub", logger)

	if servers := taskMcpServers(t, got); len(servers) != 0 {
		t.Fatalf("servers = %v, want empty managed set", servers)
	}
	if !strings.Contains(logs.String(), "runtime merge failed") {
		t.Fatalf("merge failure not logged: %s", logs)
	}
}

func TestParseRuntimeMcpAllowList(t *testing.T) {
	got := parseRuntimeMcpAllowList(" a, ,b ,a,")
	if len(got) != 2 || !got["a"] || !got["b"] {
		t.Fatalf("allow = %v", got)
	}
	if got := parseRuntimeMcpAllowList(""); len(got) != 0 {
		t.Fatalf("empty allow = %v", got)
	}
}
