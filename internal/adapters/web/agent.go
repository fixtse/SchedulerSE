package web

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"github.com/fixtse/SchedulerSE/skills"
)

//go:embed agent-guide.md
var agentGuide string

var agentID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func agentJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func agentError(w http.ResponseWriter, status int, message string) {
	agentJSON(w, status, map[string]string{"error": message})
}

// Bearer tokens are scoped to agent schedules. Browser sessions do not authorize this API.
func (s *Server) agentAPI(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.AgentSettings()
	if err != nil {
		agentError(w, 503, "Agent settings unavailable")
		return
	}
	auth := r.Header.Get("Authorization")
	if settings.TokenHash == "" || !strings.HasPrefix(auth, "Bearer ") || !equal(digest(strings.TrimPrefix(auth, "Bearer ")), settings.TokenHash) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="agent-schedules"`)
		agentError(w, 401, "A valid agent API token is required")
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agent/v1/inbox", s.agentInbox)
	mux.HandleFunc("POST /api/agent/v1/inbox/{id}/ack", s.agentPickup)
	mux.HandleFunc("GET /api/agent/v1/skill.zip", serveAgentSkill)
	mux.HandleFunc("GET /api/agent/v1/guide", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = io.WriteString(w, agentGuide)
	})
	mux.HandleFunc("GET /api/agent/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		tasks, err := s.Store.AgentTasks()
		if err != nil {
			agentError(w, 503, "Schedule storage unavailable")
			return
		}
		agentJSON(w, 200, tasks)
	})
	mux.HandleFunc("GET /api/agent/v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		task, err := s.Store.AgentTask(r.PathValue("id"))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				agentError(w, 404, "Schedule not found")
			} else {
				agentError(w, 503, "Schedule storage unavailable")
			}
			return
		}
		agentJSON(w, 200, task)
	})
	mux.HandleFunc("POST /api/agent/v1/tasks", s.createAgentTask)
	mux.HandleFunc("DELETE /api/agent/v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) { s.cancelAgent(w, r, true) })
	mux.ServeHTTP(w, r)
}
func (s *Server) createAgentTask(w http.ResponseWriter, r *http.Request) {
	if media := strings.Split(r.Header.Get("Content-Type"), ";")[0]; media != "application/json" {
		agentError(w, 415, "Use Content-Type: application/json")
		return
	}
	var input struct {
		ID              string          `json:"id"`
		Kind            string          `json:"kind"`
		Title           string          `json:"title"`
		Message         string          `json:"message"`
		Context         json.RawMessage `json:"context"`
		RunAt           string          `json:"run_at"`
		AfterSeconds    int64           `json:"after_seconds"`
		IntervalSeconds int64           `json:"interval_seconds"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF {
		agentError(w, 400, "Invalid JSON, unknown fields, or body exceeds 32 KiB")
		return
	}
	if !agentID.MatchString(input.ID) || strings.TrimSpace(input.Title) == "" || len(input.Title) > 100 || len(input.Message) > 8000 || (input.Kind != "timer" && input.Kind != "reminder" && input.Kind != "task") {
		agentError(w, 400, "Use an id of 1–64 lowercase letters, digits or hyphens, a title of 1–100 bytes, kind timer/reminder/task, and message up to 8000 bytes")
		return
	}
	if (input.RunAt == "") == (input.AfterSeconds == 0) || input.AfterSeconds < 0 || input.AfterSeconds > 31536000 || input.IntervalSeconds < 0 || (input.IntervalSeconds > 0 && input.IntervalSeconds < 60) || input.IntervalSeconds > 31536000 {
		agentError(w, 400, "Provide exactly one of run_at or after_seconds (1–31536000); interval_seconds must be 0 or 60–31536000")
		return
	}
	now := time.Now()
	at := now.Add(time.Duration(input.AfterSeconds) * time.Second)
	if input.RunAt != "" {
		var err error
		at, err = time.Parse(time.RFC3339, input.RunAt)
		if err != nil {
			agentError(w, 400, "run_at must be RFC3339 with a timezone offset")
			return
		}
	}
	if !at.After(now) || at.After(now.AddDate(5, 0, 0)) {
		agentError(w, 400, "Schedule must be in the future and within five years")
		return
	}
	if len(input.Context) == 0 {
		input.Context = json.RawMessage(`{}`)
	}
	var contextObject map[string]json.RawMessage
	if json.Unmarshal(input.Context, &contextObject) != nil || contextObject == nil {
		agentError(w, 400, "context must be a JSON object")
		return
	}
	settings, err := s.Store.AgentSettings()
	if err != nil {
		agentError(w, 503, "Agent settings unavailable")
		return
	}
	if settings.DeliveryMode == "poll" {
		var conversation string
		if json.Unmarshal(contextObject["conversation_id"], &conversation) != nil || conversation == "" || len(conversation) > 256 {
			agentError(w, 400, "Local polling requires context.conversation_id (1–256 bytes)")
			return
		}
	}
	if settings.DeliveryMode == "webhook" && settings.WebhookURL == "" {
		agentError(w, 409, "Configure the agent webhook in settings first")
		return
	}
	task := domain.AgentTask{ID: input.ID, Kind: input.Kind, Title: input.Title, Message: input.Message, Context: input.Context, RunAt: at.Unix(), IntervalSeconds: input.IntervalSeconds, Status: "pending"}
	if err = s.Store.CreateAgentTask(task); err != nil {
		if _, existsErr := s.Store.AgentTask(input.ID); existsErr == nil {
			agentError(w, 409, "ID already exists; GET the existing task before retrying")
		} else {
			agentError(w, 503, "Could not save schedule")
		}
		return
	}
	w.Header().Set("Location", "/api/agent/v1/tasks/"+task.ID)
	agentJSON(w, 201, task)
}
func (s *Server) cancelAgent(w http.ResponseWriter, r *http.Request, api bool) {
	ok, err := s.Store.CancelAgentTask(r.PathValue("id"))
	if err != nil {
		if api {
			agentError(w, 503, "Could not cancel schedule")
		} else {
			http.Error(w, "Could not cancel schedule", 503)
		}
		return
	}
	if !ok {
		if api {
			agentError(w, 409, "Schedule is missing or no longer pending")
		} else {
			http.Error(w, "Schedule is missing or no longer pending", 409)
		}
		return
	}
	if api {
		w.WriteHeader(204)
	} else {
		redirect(w, r, "/agent")
	}
}
func (s *Server) agentPage(w http.ResponseWriter, r *http.Request, csrf, user string) {
	tasks, err := s.Store.AgentTasks()
	if err != nil {
		http.Error(w, "Schedule storage unavailable", 503)
		return
	}
	render(w, 200, view{Page: "agent", Title: "Agent schedules", CSRF: csrf, Username: user, AgentTasks: tasks})
}
func (s *Server) agentSettingsPage(w http.ResponseWriter, r *http.Request, csrf, user string) {
	s.renderAgentSettings(w, 200, csrf, user, "", "", "")
}
func (s *Server) renderAgentSettings(w http.ResponseWriter, status int, csrf, user, token, notice, message string) {
	settings, err := s.Store.AgentSettings()
	if err != nil {
		http.Error(w, "Agent settings unavailable", 503)
		return
	}
	hasToken, hasAuth := settings.TokenHash != "", settings.WebhookAuth != ""
	settings.TokenHash, settings.WebhookAuth = "", ""
	render(w, status, view{Page: "agent-settings", Title: "Agent API settings", CSRF: csrf, Username: user, AgentSettings: settings, HasAgentToken: hasToken, HasWebhookAuth: hasAuth, NewAgentToken: token, Notice: notice, Error: message})
}
func (s *Server) saveAgentSettings(w http.ResponseWriter, r *http.Request, csrf, user string) {
	settings, err := s.Store.AgentSettings()
	if err != nil {
		http.Error(w, "Agent settings unavailable", 503)
		return
	}
	mode := r.PostFormValue("delivery_mode")
	if mode == "" {
		mode = "webhook"
	} // Preserve older forms already open in a browser.
	if mode != "poll" && mode != "webhook" {
		http.Error(w, "Invalid delivery mode", 400)
		return
	}
	endpoint := strings.TrimSpace(r.PostFormValue("webhook_url"))
	auth := r.PostFormValue("webhook_authorization")
	if auth == "" {
		auth = settings.WebhookAuth
	}
	if r.PostFormValue("clear_webhook_authorization") == "on" {
		auth = ""
	}
	if endpoint != "" {
		err = adapters.ValidateEndpoint(endpoint)
	}
	if len(endpoint) > 2048 || len(auth) > 8192 || strings.ContainsAny(auth, "\r\n") {
		err = errors.New("Webhook URL or authorization header is invalid or too long")
	}
	if err != nil {
		s.renderAgentSettings(w, 400, csrf, user, "", "", err.Error())
		return
	}
	if err = s.Store.SaveAgentConnection(mode, endpoint, auth); err != nil {
		http.Error(w, "Could not save agent delivery settings", 503)
		return
	}
	s.renderAgentSettings(w, 200, csrf, user, "", "Agent delivery settings saved. Changes apply to future deliveries.", "")
}
func (s *Server) agentToken(w http.ResponseWriter, r *http.Request, csrf, user string) {
	token, notice := "", "API token revoked. Existing schedules remain active."
	if r.PostFormValue("action") == "generate" {
		token = "sse_" + rand.Text()
		notice = "Copy this token now. It is shown only once; the previous token is invalid."
	} else if r.PostFormValue("action") != "revoke" {
		http.Error(w, "Invalid action", 400)
		return
	}
	hash := ""
	if token != "" {
		hash = digest(token)
	}
	if s.Store.SetAgentToken(hash) != nil {
		http.Error(w, "Could not update API token", 503)
		return
	}
	s.renderAgentSettings(w, 200, csrf, user, token, notice, "")
}

func serveAgentSkill(w http.ResponseWriter, r *http.Request) {
	data, err := skills.AgentArchive()
	if err != nil {
		http.Error(w, "Could not prepare skill download", 503)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="schedulerse-agent.zip"`)
	_, _ = w.Write(data)
}

func (s *Server) agentInbox(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.AgentSettings()
	if err != nil {
		agentError(w, 503, "Agent settings unavailable")
		return
	}
	if settings.DeliveryMode != "poll" {
		agentError(w, 409, "Select local polling in Agent API settings")
		return
	}
	conversation := r.URL.Query().Get("conversation_id")
	if conversation == "" || len(conversation) > 256 {
		agentError(w, 400, "conversation_id is required (up to 256 bytes)")
		return
	}
	events, err := s.Store.AgentInbox(conversation)
	if err != nil {
		agentError(w, 503, "Schedule storage unavailable")
		return
	}
	agentJSON(w, 200, events)
}
func (s *Server) agentPickup(w http.ResponseWriter, r *http.Request) {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		agentError(w, 415, "Use Content-Type: application/json")
		return
	}
	var input struct {
		RunAt    int64  `json:"run_at_unix"`
		Consumer string `json:"consumer_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF || input.RunAt <= 0 || !runTokenPattern.MatchString(input.Consumer) {
		agentError(w, 400, "Provide run_at_unix and a stable random consumer_id of 26–128 letters, digits, underscores or hyphens")
		return
	}
	ok, err := s.Store.PickupAgentTask(r.PathValue("id"), input.RunAt, input.Consumer)
	if err != nil {
		agentError(w, 503, "Could not record pickup")
		return
	}
	if !ok {
		agentError(w, 409, "Occurrence cancelled, unavailable, or picked up by another consumer")
		return
	}
	w.WriteHeader(204)
}
