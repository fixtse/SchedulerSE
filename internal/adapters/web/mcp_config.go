package web

import (
	"encoding/json"
	"fmt"
)

type mcpClientConfig struct {
	ID, Name, Instructions, DocsURL, Config string
}

// The plain token is available only in the response that creates it. Never load
// credentials from storage to populate client examples on subsequent visits.
func mcpClientConfigs(endpoint, token string) []mcpClientConfig {
	if token == "" {
		token = "YOUR_API_TOKEN"
	}
	header := "Bearer " + token
	jsonConfig := func(root string, httpType bool) string {
		server := map[string]any{"url": endpoint, "headers": map[string]string{"Authorization": header}}
		if httpType {
			server["type"] = "http"
		}
		data, _ := json.MarshalIndent(map[string]any{root: map[string]any{"schedulerse": server}}, "", "  ")
		return string(data)
	}
	return []mcpClientConfig{
		{"codex", "Codex", "Add this section to ~/.codex/config.toml, then reconnect or restart Codex.", "https://developers.openai.com/codex/mcp/", fmt.Sprintf("[mcp_servers.schedulerse]\nurl = %q\nhttp_headers = { Authorization = %q }", endpoint, header)},
		{"claude", "Claude Code", "Merge this into .mcp.json in your project, then approve SchedulerSE in Claude Code with /mcp.", "https://code.claude.com/docs/en/mcp", jsonConfig("mcpServers", true)},
		{"vscode", "Visual Studio Code", "Run “MCP: Open User Configuration” from the Command Palette and merge this into mcp.json. Start SchedulerSE from the MCP server list.", "https://code.visualstudio.com/docs/agents/reference/mcp-configuration", jsonConfig("servers", true)},
		{"cursor", "Cursor", "Merge this into ~/.cursor/mcp.json, then enable SchedulerSE in Cursor’s MCP settings.", "https://cursor.com/docs/mcp", jsonConfig("mcpServers", false)},
		{"other", "Other MCP clients", "For clients that accept mcpServers JSON with URL and headers, merge this into your MCP configuration. Select Streamable HTTP if asked.", "", jsonConfig("mcpServers", false)},
	}
}
