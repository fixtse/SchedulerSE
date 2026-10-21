package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/application"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func mcpRequest(f *browserFixture, token, method string, params any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	request := httptest.NewRequest(http.MethodPost, "/api/agent/v1/mcp", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	f.app.ServeHTTP(response, request)
	return response
}

type mcpToolResponse struct {
	IsError           bool `json:"isError"`
	StructuredContent struct {
		Result json.RawMessage `json:"result"`
		Status string          `json:"status"`
	} `json:"structuredContent"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

func callMCP(t *testing.T, f *browserFixture, name string, args any) mcpToolResponse {
	t.Helper()
	response := mcpRequest(f, "mcp-secret", "tools/call", map[string]any{"name": name, "arguments": args})
	var envelope struct {
		Result mcpToolResponse `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || len(envelope.Error) > 0 {
		t.Fatalf("MCP %s: %d %s", name, response.Code, response.Body.String())
	}
	return envelope.Result
}

func TestMCPTransportDiscoveryAndAuthorization(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("mcp-secret")); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "wrong"} {
		if res := mcpRequest(f, token, "tools/list", map[string]any{}); res.Code != 401 {
			t.Fatal("unauthorized MCP", res.Code)
		}
	}
	initialized := mcpRequest(f, "mcp-secret", "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if initialized.Code != 200 || !strings.Contains(initialized.Body.String(), `"serverInfo"`) || !strings.Contains(initialized.Body.String(), `"SchedulerSE"`) {
		t.Fatal("initialize failed", initialized.Code, initialized.Body.String())
	}
	response := mcpRequest(f, "mcp-secret", "tools/list", map[string]any{})
	var envelope struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]any `json:"properties"`
					Required   []string       `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &envelope) != nil {
		t.Fatal("discovery failed", response.Body.String())
	}
	names := map[string]bool{}
	for _, tool := range envelope.Result.Tools {
		names[tool.Name] = true
		if tool.Name == "create_timer" {
			for _, field := range []string{"id", "title", "context", "after_seconds"} {
				if _, ok := tool.InputSchema.Properties[field]; !ok {
					t.Errorf("timer missing schema field %s", field)
				}
				found := false
				for _, required := range tool.InputSchema.Required {
					found = found || required == field
				}
				if !found {
					t.Errorf("timer field %s must be required", field)
				}
			}
		}
	}
	for _, name := range []string{"create_timer", "create_reminder", "list_schedules", "get_schedule", "cancel_schedule", "create_automation", "list_automations", "get_automation", "run_automation", "list_runs", "get_connection", "configure_connection", "get_guide", "get_delivery_file"} {
		if !names[name] {
			t.Error("missing MCP tool", name)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/agent/v1/mcp", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer mcp-secret")
	request.Header.Set("Origin", "https://untrusted.example")
	forbidden := httptest.NewRecorder()
	f.app.ServeHTTP(forbidden, request)
	if forbidden.Code != 403 {
		t.Fatal("cross-origin MCP accepted", forbidden.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/agent/v1/mcp", nil)
	request.Header.Set("Authorization", "Bearer mcp-secret")
	response = httptest.NewRecorder()
	f.app.ServeHTTP(response, request)
	if response.Code != 405 {
		t.Fatal("stateless GET accepted", response.Code)
	}
	if err := f.store.SetAgentToken(""); err != nil {
		t.Fatal(err)
	}
	if res := mcpRequest(f, "mcp-secret", "tools/list", map[string]any{}); res.Code != 401 {
		t.Fatal("revoked token accepted", res.Code)
	}
}

func TestMCPSchedulesAndConnection(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("mcp-secret")); err != nil {
		t.Fatal(err)
	}
	result := callMCP(t, f, "configure_connection", map[string]any{"delivery_mode": "poll"})
	if result.IsError {
		t.Fatal(result.Content)
	}
	timer := map[string]any{"id": "tea-timer", "title": "Tea", "after_seconds": 60, "context": map[string]any{"conversation_id": "conversation-1"}}
	if result = callMCP(t, f, "create_timer", timer); result.IsError {
		t.Fatal(result.Content)
	}
	saved, err := f.store.AgentTask("tea-timer")
	if err != nil || saved.Kind != "timer" || saved.Status != "pending" {
		t.Fatal("timer not persisted", saved, err)
	}
	if result = callMCP(t, f, "create_timer", timer); !result.IsError {
		t.Fatal("duplicate timer accepted")
	}
	if result = callMCP(t, f, "create_reminder", map[string]any{"id": "report-reminder", "title": "Report", "run_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "context": map[string]any{"conversation_id": "conversation-1"}}); result.IsError {
		t.Fatal(result.Content)
	}
	for _, tool := range []string{"list_schedules", "get_connection", "get_guide"} {
		if result = callMCP(t, f, tool, map[string]any{}); result.IsError {
			t.Fatal(tool, result.Content)
		}
	}
	if result = callMCP(t, f, "get_schedule", map[string]any{"id": "tea-timer"}); result.IsError || !strings.Contains(string(result.StructuredContent.Result), "tea-timer") {
		t.Fatal(result)
	}
	if result = callMCP(t, f, "cancel_schedule", map[string]any{"id": "tea-timer"}); result.IsError || result.StructuredContent.Status != "cancelled" {
		t.Fatal(result)
	}
	if saved, err = f.store.AgentTask("tea-timer"); err != nil || saved.Status != "cancelled" {
		t.Fatal("cancel not persisted", saved, err)
	}
	for _, invalid := range []map[string]any{
		{"id": "../connection"},
		{"id": "tea-timer/extra"},
		{},
	} {
		if result = callMCP(t, f, "get_schedule", invalid); !result.IsError {
			t.Fatal("invalid ID accepted", invalid)
		}
	}
	if result = callMCP(t, f, "create_timer", map[string]any{"id": "missing-context", "title": "Invalid", "after_seconds": 60}); !result.IsError {
		t.Fatal("missing context accepted")
	}
	if result = callMCP(t, f, "configure_connection", map[string]any{"delivery_mode": "webhook", "webhook_url": "https://example.com/hook", "webhook_authorization": "Bearer private-webhook"}); result.IsError {
		t.Fatal(result.Content)
	}
	if result = callMCP(t, f, "get_connection", map[string]any{}); result.IsError || strings.Contains(string(result.StructuredContent.Result), "private-webhook") {
		t.Fatal("webhook read leaked secret", result)
	}
}

func TestMCPAutomationsExecuteAndReportHistory(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("mcp-secret")); err != nil {
		t.Fatal(err)
	}
	calls := make(chan string, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- r.Header.Get("Authorization")
		w.WriteHeader(204)
	}))
	defer endpoint.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); f.app.Wait() }()
	transport := adapters.NewHTTP(adapters.Config{Timeout: time.Second})
	f.app.Context = ctx
	f.app.Runner = &application.Runner{Store: f.store, Sync: transport, Notifications: &adapters.Telegram{Store: f.store, HTTP: transport}, Log: silentLog{}, Timeout: time.Second}
	result := callMCP(t, f, "create_automation", map[string]any{"id": "mcp-post", "name": "MCP POST", "kind": "http", "url": endpoint.URL, "authorization": "Bearer private-endpoint", "body": map[string]any{"ready": true}})
	if result.IsError || strings.Contains(string(result.StructuredContent.Result), "private-endpoint") {
		t.Fatal("automation create", result)
	}
	for _, tool := range []string{"list_automations", "get_automation"} {
		args := map[string]any{}
		if tool == "get_automation" {
			args["id"] = "mcp-post"
		}
		if result = callMCP(t, f, tool, args); result.IsError || strings.Contains(string(result.StructuredContent.Result), "private-endpoint") {
			t.Fatal("read leaked credentials", tool, result)
		}
	}
	run := map[string]any{"id": "mcp-post", "revision": 1, "run_token": "mcp_execution_token_1234567890"}
	if result = callMCP(t, f, "run_automation", run); result.IsError || !strings.Contains(string(result.StructuredContent.Result), "manual:mcp_execution_token_1234567890") {
		t.Fatal("run not accepted", result)
	}
	select {
	case auth := <-calls:
		if auth != "Bearer private-endpoint" {
			t.Fatal("wrong endpoint auth")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("POST never executed")
	}
	if result = callMCP(t, f, "run_automation", run); !result.IsError {
		t.Fatal("execution replayed")
	}
	if result = callMCP(t, f, "create_automation", map[string]any{"id": "mcp-script", "name": "MCP Bash", "kind": "bash", "script": "true"}); result.IsError {
		t.Fatal(result.Content)
	}
	if result = callMCP(t, f, "run_automation", map[string]any{"id": "mcp-script", "revision": 1, "run_token": "mcp_script_execution_1234567890"}); result.IsError {
		t.Fatal(result.Content)
	}
	f.app.Wait()
	result = callMCP(t, f, "list_runs", map[string]any{})
	if result.IsError {
		t.Fatal(result.Content)
	}
	text := string(result.StructuredContent.Result)
	for _, want := range []string{"mcp-post", "mcp-script", "success", "204"} {
		if !strings.Contains(text, want) {
			t.Error("missing result", want, text)
		}
	}
	if strings.Contains(text, "private-endpoint") {
		t.Fatal("history leaked credentials")
	}
	if result = callMCP(t, f, "list_runs", map[string]any{"page": -1}); !result.IsError {
		t.Fatal("invalid history page accepted")
	}
}

type mcpBearerTransport struct{ base http.RoundTripper }

func (transport mcpBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer mcp-secret")
	return transport.base.RoundTrip(clone)
}

func TestMCPOfficialClientInteroperability(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("mcp-secret")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             f.server.URL + "/api/agent/v1/mcp",
		HTTPClient:           &http.Client{Transport: mcpBearerTransport{base: http.DefaultTransport}},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal("official client failed to connect", err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil || len(tools.Tools) != 14 {
		t.Fatalf("discovery: %+v, %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_connection", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("tool call: %+v, %v", result, err)
	}
	if err := f.store.SetAgentToken(""); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_connection", Arguments: map[string]any{}}); err == nil {
		t.Fatal("connected MCP client retained access after revocation")
	}
}

func TestMCPBodyLimitAndBrowserSessionIsolation(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("mcp-secret")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddSession(digest("browser-session"), "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/agent/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.AddCookie(&http.Cookie{Name: "session", Value: "browser-session"})
	response := httptest.NewRecorder()
	f.app.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("browser session authorized MCP", response.Code)
	}
	oversized := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_guide","arguments":{"extra":"` + strings.Repeat("x", 270<<10) + `"}}}`
	request = httptest.NewRequest(http.MethodPost, "/api/agent/v1/mcp", strings.NewReader(oversized))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	request.Header.Set("Authorization", "Bearer mcp-secret")
	response = httptest.NewRecorder()
	f.app.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("oversized MCP body accepted", response.Code)
	}
}

func TestMCPDeliverySetupWithoutSkill(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("mcp-secret")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"README.md", "scripts/bridge.py", "references/local.md", "references/t3.md", "references/cloudflare.md", "connection.json"} {
		result := callMCP(t, f, "get_delivery_file", map[string]any{"path": path})
		if result.IsError || len(result.Content) != 1 || result.Content[0].Text == "" {
			t.Fatalf("file %s: %+v", path, result)
		}
		if strings.Contains(result.Content[0].Text, "mcp-secret") {
			t.Fatal("credential leaked into delivery files")
		}
		if path == "connection.json" {
			var metadata map[string]string
			if json.Unmarshal([]byte(result.Content[0].Text), &metadata) != nil || metadata["service_url"] != "http://example.com" || metadata["mcp_url"] != "http://example.com/api/agent/v1/mcp" {
				t.Fatal("missing request-derived connection metadata", result)
			}
		}
	}
	for _, path := range []string{"SKILL.md", "../bundle.go", "/etc/passwd"} {
		if result := callMCP(t, f, "get_delivery_file", map[string]any{"path": path}); !result.IsError {
			t.Fatal("unexpected delivery file exposed", path)
		}
	}
}
