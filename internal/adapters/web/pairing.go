package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fixtse/SchedulerSE/internal/adapters"
)

var pairingID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
var pairingDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Registration never grants access. Only an authenticated, CSRF-protected browser
// approval activates the digest. No raw API token crosses this API.
func (s *Server) pairingAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" && r.URL.Path == "/api/pairing/v1/requests" {
		if !s.allowAuth(r) {
			agentError(w, 429, "Too many pairing requests")
			return
		}
		var input struct {
			ID           string `json:"id"`
			SecretHash   string `json:"secret_hash"`
			TokenHash    string `json:"token_hash"`
			CallbackURL  string `json:"callback_url"`
			CallbackAuth string `json:"callback_authorization"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" || dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF || !pairingID.MatchString(input.ID) || !pairingDigest.MatchString(input.SecretHash) || !pairingDigest.MatchString(input.TokenHash) || !strings.HasPrefix(input.CallbackAuth, "Bearer ") || !pairingDigest.MatchString(strings.TrimPrefix(input.CallbackAuth, "Bearer ")) || len(input.CallbackURL) > 2048 || adapters.ValidateEndpoint(input.CallbackURL) != nil {
			agentError(w, 400, "Invalid pairing request; callback requires HTTPS (HTTP only on loopback)")
			return
		}
		p := adapters.AgentPairing{ID: input.ID, SecretHash: input.SecretHash, TokenHash: input.TokenHash, CallbackURL: input.CallbackURL, CallbackAuth: input.CallbackAuth, ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}
		if err := s.Store.CreateAgentPairing(p); err != nil {
			agentError(w, 409, "Pairing queue unavailable or request already registered; inspect its status without repeating registration")
			return
		}
		agentJSON(w, 201, map[string]any{"id": p.ID, "state": "pending", "expires_at": p.ExpiresAt})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/pairing/v1/requests/")
	if r.Method != "GET" || !pairingID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	p, err := s.Store.AgentPairing(id)
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if err != nil || auth == "" || !equal(digest(auth), p.SecretHash) {
		agentError(w, 404, "Pairing not found")
		return
	}
	state := p.State
	if state == "pending" && p.ExpiresAt <= time.Now().Unix() {
		state = "expired"
	}
	agentJSON(w, 200, map[string]any{"id": p.ID, "state": state, "expires_at": p.ExpiresAt})
}

var pairingPage = template.Must(template.New("pairing").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect Zero · SchedulerSE</title><link rel="stylesheet" href="{{.Style}}"></head><body><header class="top"><a class="brand" href="/">SchedulerSE</a></header><main class="workspace pairing-workspace"><section class="panel stack"><h1>Connect Zero</h1><p>Only approve a request you just started in Zero. Check the full delivery address before approving.</p><label>Deliver notifications to<input readonly value="{{.Callback}}"></label><p>This grants Zero access to create, inspect and cancel its own reminders. It cannot manage automations or change the shared delivery connection through its API credential.</p><p>Connecting preserves existing schedules and credentials. A different delivery connection blocks approval.</p>{{if .Error}}<div class="error" role="alert">{{.Error}}</div>{{end}}{{if eq .State "pending"}}<form method="post" class="stack"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="primary" name="action" value="approve">Approve connection</button><button name="action" value="revoke">Deny request</button></form>{{else if eq .State "approved"}}<p role="status">Approved. Return to Zero to finish connecting.</p><form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><button name="action" value="revoke">Revoke Zero's API access</button></form><p>Revoking access keeps existing schedules and delivery settings.</p>{{else}}<p role="status">This request is {{.State}}. Start a new request from Zero if needed.</p>{{end}}<a href="/settings?tab=agent">Agent settings</a></section></main></body></html>`))

func (s *Server) pairingApproval(w http.ResponseWriter, r *http.Request, csrf string) {
	id := r.PathValue("id")
	if !pairingID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	p, err := s.Store.AgentPairing(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	message := ""
	status := 200
	if r.Method == "POST" {
		switch r.PostFormValue("action") {
		case "approve":
			err = s.Store.ApproveAgentPairing(id)
		case "revoke":
			err = s.Store.RevokeAgentPairing(id)
		default:
			http.Error(w, "Invalid action", 400)
			return
		}
		if err != nil {
			status = 409
			message = "Request expired or unavailable. Reload before trying again."
			if errors.Is(err, adapters.ErrPairingConflict) {
				message = err.Error()
			}
		} else {
			redirect(w, r, "/pairing/"+id)
			return
		}
	}
	state := p.State
	if state == "pending" && p.ExpiresAt <= time.Now().Unix() {
		state = "expired"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = pairingPage.Execute(w, struct{ Style, Callback, CSRF, State, Error string }{styleAsset.URL(), p.CallbackURL, csrf, state, message})
}

type pairedAgentKey struct{}

func zeroTask(id string, raw json.RawMessage) bool {
	var context struct {
		Integration string `json:"integration"`
		RuleID      string `json:"rule_id"`
		Generation  int64  `json:"generation"`
	}
	if json.Unmarshal(raw, &context) != nil || context.Integration != "zero" || context.RuleID == "" || context.Generation < 0 {
		return false
	}
	return id == "zero-"+context.RuleID+"-"+strconv.FormatInt(context.Generation, 10)
}
func (s *Server) allowPairedAgent(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == "GET" && r.URL.Path == "/api/agent/v1/connection" {
		return true
	}
	if r.Method == "POST" && r.URL.Path == "/api/agent/v1/tasks" {
		return true
	}
	if (r.Method == "GET" || r.Method == "DELETE") && strings.HasPrefix(r.URL.Path, "/api/agent/v1/tasks/") {
		id := strings.TrimPrefix(r.URL.Path, "/api/agent/v1/tasks/")
		task, err := s.Store.AgentTask(id)
		if err == nil && zeroTask(task.ID, task.Context) {
			return true
		}
		if errors.Is(err, sql.ErrNoRows) {
			agentError(w, 404, "Schedule not found")
			return false
		}
		if err != nil {
			agentError(w, 503, "Schedule storage unavailable")
			return false
		}
	}
	agentError(w, 403, "Paired access is limited to Zero reminders and connection status")
	return false
}
