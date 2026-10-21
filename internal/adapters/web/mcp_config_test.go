package web

import (
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestMCPClientConfigurationFormats(t *testing.T) {
	endpoint := "https://scheduler.example.com/api/agent/v1/mcp"
	token := "sse_test_token"
	for _, client := range mcpClientConfigs(endpoint, token) {
		t.Run(client.ID, func(t *testing.T) {
			if client.ID == "codex" {
				want := "[mcp_servers.schedulerse]\nurl = \"" + endpoint + "\"\nhttp_headers = { Authorization = \"Bearer " + token + "\" }"
				if client.Config != want {
					t.Fatal("incorrect Codex TOML configuration")
				}
				return
			}
			var config map[string]map[string]struct {
				Type    string            `json:"type"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			}
			if err := json.Unmarshal([]byte(client.Config), &config); err != nil {
				t.Fatal(err)
			}
			root := "mcpServers"
			if client.ID == "vscode" {
				root = "servers"
			}
			server, ok := config[root]["schedulerse"]
			if !ok || server.URL != endpoint || server.Headers["Authorization"] != "Bearer "+token {
				t.Fatal("incorrect endpoint, authentication or client root key")
			}
			if (client.ID == "claude" || client.ID == "vscode") && server.Type != "http" {
				t.Fatal("client requires explicit HTTP transport")
			}
		})
	}
	for _, client := range mcpClientConfigs(endpoint, "") {
		if !strings.Contains(client.Config, "Bearer YOUR_API_TOKEN") {
			t.Fatal("missing placeholder without newly generated token")
		}
	}
}

func TestMCPConfigurationInSetupAndTokenShownOnlyOnce(t *testing.T) {
	f := fixture(t)
	csrf := f.setup(t)
	code, page := f.get(t, "/settings?tab=agent")
	if code != 200 {
		t.Fatal("settings unavailable", code)
	}
	tokenStep := strings.Index(page, `<h3>Generate API token</h3>`)
	addStep := strings.Index(page, `<h3>Add SchedulerSE to your agent</h3>`)
	showConfig := strings.Index(page, `<summary>Show configuration</summary>`)
	askStep := strings.Index(page, `<h3>Ask for a reminder</h3>`)
	advanced := strings.Index(page, `<span>Advanced options<small>`)
	if tokenStep < 0 || addStep < tokenStep || showConfig < addStep || askStep < showConfig || advanced < askStep {
		t.Fatal("configuration is not in step 2 after token generation")
	}
	if strings.Contains(page, `<h2>MCP client configuration</h2>`) {
		t.Fatal("old advanced configuration was retained")
	}
	configs := regexp.MustCompile(`(?s)<pre[^>]*data-mcp-config[^>]*>(.*?)</pre>`)
	if len(configs.FindAllStringSubmatch(page, -1)) != 5 {
		t.Fatal("missing client configuration examples")
	}
	code, page, response := f.post(t, "/settings/agent/token", url.Values{"csrf": {csrf}, "action": {"generate"}})
	if code != 200 || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("token page must be private and successful", code)
	}
	tokenMatch := regexp.MustCompile(`value="(sse_[A-Za-z0-9]+)"`).FindStringSubmatch(page)
	if len(tokenMatch) != 2 {
		t.Fatal("new token not shown")
	}
	if !strings.Contains(page, `class="mcp-configuration" open`) {
		t.Fatal("configuration not expanded after generation")
	}
	rendered := configs.FindAllStringSubmatch(page, -1)
	if len(rendered) != 5 {
		t.Fatal("generated configurations missing")
	}
	for _, match := range rendered {
		config := html.UnescapeString(match[1])
		if !strings.Contains(config, "Bearer "+tokenMatch[1]) || !strings.Contains(config, f.server.URL+"/api/agent/v1/mcp") || strings.Contains(config, "YOUR_API_TOKEN") {
			t.Fatal("configuration must include newly generated token and full endpoint")
		}
	}
	_, page = f.get(t, "/settings?tab=agent")
	if strings.Contains(page, tokenMatch[1]) {
		t.Fatal("token must never reappear after leaving the generation response")
	}
	for _, match := range configs.FindAllStringSubmatch(page, -1) {
		if !strings.Contains(match[1], "YOUR_API_TOKEN") {
			t.Fatal("saved token must use placeholder")
		}
	}
}
