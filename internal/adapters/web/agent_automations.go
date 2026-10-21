package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/domain"
)

func automationInput(w http.ResponseWriter, r *http.Request, value any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		agentError(w, 415, "Use Content-Type: application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		agentError(w, 400, "Invalid JSON, unknown fields, or body exceeds 256 KiB")
		return false
	}
	return true
}

func (s *Server) agentAutomations(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		jobs, err := s.Store.Jobs()
		if err != nil {
			agentError(w, 503, "Automation storage unavailable")
			return
		}
		agentJSON(w, 200, jobs)
		return
	}
	var input struct {
		domain.Job
		Authorization         string `json:"authorization"`
		FallbackAuthorization string `json:"fallback_authorization"`
	}
	if !automationInput(w, r, &input) {
		return
	}
	job := input.Job
	job.Policy, job.Revision = "generic", 0
	job.Authorization, job.FallbackAuth = input.Authorization, input.FallbackAuthorization
	if len(job.Body) == 0 {
		job.Body = json.RawMessage(`{}`)
	}
	if err := adapters.ValidateJob(job, s.settings().Timezone); err != nil {
		agentError(w, 400, err.Error())
		return
	}
	if job.Enabled && job.Cron != "" {
		if err := s.validateRunnable(job); err != nil {
			agentError(w, 400, err.Error())
			return
		}
	}
	if err := s.Store.Save(job, true); err != nil {
		if _, exists := s.Store.Job(job.ID); exists == nil {
			agentError(w, 409, "ID already exists; inspect the saved automation before retrying")
		} else {
			agentError(w, 503, "Could not save automation")
		}
		return
	}
	s.Reload()
	saved, err := s.Store.Job(job.ID)
	if err != nil {
		agentError(w, 503, "Automation saved; inspect its ID before retrying")
		return
	}
	w.Header().Set("Location", "/api/agent/v1/automations/"+job.ID)
	agentJSON(w, 201, saved)
}

func (s *Server) loadAgentAutomation(w http.ResponseWriter, r *http.Request) (domain.Job, bool) {
	job, err := s.Store.Job(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			agentError(w, 404, "Automation not found")
		} else {
			agentError(w, 503, "Automation storage unavailable")
		}
		return job, false
	}
	return job, true
}

func (s *Server) agentAutomation(w http.ResponseWriter, r *http.Request) {
	if job, ok := s.loadAgentAutomation(w, r); ok {
		agentJSON(w, 200, job)
	}
}

func (s *Server) agentRunAutomation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision int64  `json:"revision"`
		RunToken string `json:"run_token"`
	}
	if !automationInput(w, r, &input) {
		return
	}
	if !runTokenPattern.MatchString(input.RunToken) {
		agentError(w, 400, "Use a stable random run_token of 26–128 letters, digits, underscores or hyphens")
		return
	}
	job, ok := s.loadAgentAutomation(w, r)
	if !ok {
		return
	}
	if input.Revision != job.Revision {
		agentError(w, 409, "Automation changed; inspect its current revision before running")
		return
	}
	if err := adapters.ValidateJob(job, s.settings().Timezone); err != nil {
		agentError(w, 400, err.Error())
		return
	}
	if err := s.validateRunnable(job); err != nil {
		agentError(w, 400, err.Error())
		return
	}
	status, message := s.startJob(job, input.RunToken)
	if status != http.StatusAccepted {
		agentError(w, status, message)
		return
	}
	agentJSON(w, status, map[string]string{"id": job.ID, "slot": "manual:" + input.RunToken, "status": "running"})
}
