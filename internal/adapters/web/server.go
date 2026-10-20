package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/application"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"golang.org/x/crypto/bcrypt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed page.html
var pageHTML string

//go:embed style.css
var stylesheet string

//go:embed schedule.js
var scheduleJS string

//go:embed vendor/htmx.min.js
var htmxJS string

//go:embed favicon.svg
var faviconSVG []byte

//go:embed favicon.ico
var faviconICO []byte

var pages = template.Must(template.New("page").Funcs(template.FuncMap{
	"styleURL":  styleAsset.URL,
	"scriptURL": scriptAsset.URL,
	"htmxURL":   htmxAsset.URL,
	"uiVersion": func() string { return uiAssetVersion },
}).Parse(pageHTML))

type Server struct {
	Store      *adapters.Store
	Config     adapters.Config
	Reload     func()
	SetupToken string
	Runner     *application.Runner
	Context    context.Context
	Telegram   *adapters.Telegram
	runMu      sync.Mutex
	runs       sync.WaitGroup
	closing    bool
	authMu     sync.Mutex
	attempts   int
	window     time.Time
	hashing    chan struct{}
}
type jobView struct {
	domain.Job
	Next         string
	Schedule     string
	UsesTimezone bool
}
type view struct {
	AgentTasks                           []domain.AgentTask
	AgentSettings                        domain.AgentSettings
	HasAgentToken, HasWebhookAuth        bool
	NewAgentToken                        string
	Page, Title, Error, CSRF, Username   string
	Timezone                             string
	Jobs                                 []jobView
	Runs                                 []runView
	Job                                  domain.Job
	New, HasAuthorization                bool
	HasFallbackAuth                      bool
	Schedule                             scheduleForm
	Timezones, Weekdays                  []choice
	RunToken, Notice                     string
	Telegram                             domain.TelegramSettings
	HasBotToken                          bool
	TestToken                            string
	DefaultTimeoutMS                     int64
	HistoryPage, HistoryPages            int
	HistoryTotal, HistoryFrom, HistoryTo int
	PreviousPage, NextPage               int
}

func New(s *adapters.Store, c adapters.Config, reload func(), token string) *Server {
	return &Server{Store: s, Config: c, Reload: reload, SetupToken: token, hashing: make(chan struct{}, 1)}
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func (s *Server) cookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}
func (s *Server) preauth(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("preauth"); err == nil && len(c.Value) >= 26 && len(c.Value) <= 128 {
		return c.Value
	}
	token := rand.Text()
	s.cookie(w, "preauth", token, 1800)
	return token
}
func render(w http.ResponseWriter, status int, v view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = pages.Execute(w, v)
}
func redirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, path, http.StatusSeeOther)
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	// A tab with assets from the previous release must reload its head before
	// rendering newly deployed markup. Ordinary navigation is unaffected.
	if version := r.Header.Get("X-SchedulerSE-UI"); r.Method == http.MethodGet && r.Header.Get("HX-Boosted") == "true" && version != "" && version != uiAssetVersion {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.URL.Path == "/favicon.svg" || r.URL.Path == "/favicon.ico" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		icon, contentType := faviconSVG, "image/svg+xml"
		if r.URL.Path == "/favicon.ico" {
			icon, contentType = faviconICO, "image/x-icon"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(icon)))
		w.Header().Set("Cache-Control", "public, max-age=3600")
		if r.Method == http.MethodGet {
			_, _ = w.Write(icon)
		}
		return
	}
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		// Health is reachable only over loopback; reverse proxies should not forward it.
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if s.Store.Health() != nil {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"status":"unhealthy"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","schedulerEnabled":true}`))
		return
	}
	if r.URL.Path == htmxAsset.path {
		htmxAsset.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == scriptAsset.path {
		scriptAsset.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == styleAsset.path {
		styleAsset.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/agent/") {
		s.agentAPI(w, r)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		if r.ParseForm() != nil {
			http.Error(w, "Invalid or oversized form", 400)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Cross-site request rejected", 403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				http.Error(w, "Cross-site request rejected", 403)
				return
			}
		}
	}
	username, hash, err := s.Store.Account()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Account storage unavailable", 503)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		if r.URL.Path != "/setup" {
			redirect(w, r, "/setup")
			return
		}
		s.authPage(w, r, "setup", username, hash)
		return
	}
	if r.URL.Path == "/setup" {
		redirect(w, r, "/login")
		return
	}
	if r.URL.Path == "/login" {
		s.authPage(w, r, "login", username, hash)
		return
	}
	session, err := r.Cookie("session")
	csrf := ""
	if err == nil {
		csrf, err = s.Store.Session(digest(session.Value))
	}
	if err != nil || csrf == "" {
		redirect(w, r, "/login")
		return
	}
	if r.Method == http.MethodPost && !equal(r.PostFormValue("csrf"), csrf) {
		http.Error(w, "Invalid form token; reload the page", 403)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /settings/agent/skill.zip", s.serveAgentSkill)
	mux.HandleFunc("GET /settings/agent/guide", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(agentGuide))
	})
	mux.HandleFunc("GET /agent", func(w http.ResponseWriter, r *http.Request) { s.agentPage(w, r, csrf, username) })
	mux.HandleFunc("POST /agent/{id}/cancel", func(w http.ResponseWriter, r *http.Request) { s.cancelAgent(w, r, false) })
	mux.HandleFunc("GET /settings/agent", func(w http.ResponseWriter, r *http.Request) { s.agentSettingsPage(w, r, csrf, username) })
	mux.HandleFunc("POST /settings/agent", func(w http.ResponseWriter, r *http.Request) { s.saveAgentSettings(w, r, csrf, username) })
	mux.HandleFunc("POST /settings/agent/token", func(w http.ResponseWriter, r *http.Request) { s.agentToken(w, r, csrf, username) })
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { s.dashboard(w, r, csrf, username) })
	mux.HandleFunc("GET /settings/telegram", func(w http.ResponseWriter, r *http.Request) { s.telegramPage(w, r, csrf, username) })
	mux.HandleFunc("POST /settings/telegram", func(w http.ResponseWriter, r *http.Request) { s.saveTelegram(w, r, csrf, username) })
	mux.HandleFunc("POST /settings/telegram/test", s.testTelegram)
	mux.HandleFunc("POST /logout", func(w http.ResponseWriter, r *http.Request) {
		if s.Store.DeleteSession(digest(session.Value)) != nil {
			http.Error(w, "Could not sign out", 503)
			return
		}
		s.cookie(w, "session", "", -1)
		redirect(w, r, "/login")
	})
	mux.HandleFunc("GET /jobs/new", func(w http.ResponseWriter, r *http.Request) { s.jobPage(w, r, csrf, true) })
	mux.HandleFunc("POST /jobs/new", func(w http.ResponseWriter, r *http.Request) { s.saveJob(w, r, csrf, true) })
	mux.HandleFunc("GET /jobs/{id}", func(w http.ResponseWriter, r *http.Request) { s.jobPage(w, r, csrf, false) })
	mux.HandleFunc("POST /jobs/{id}", func(w http.ResponseWriter, r *http.Request) { s.saveJob(w, r, csrf, false) })
	mux.HandleFunc("POST /jobs/{id}/run", s.runJob)
	mux.HandleFunc("POST /jobs/{id}/toggle", func(w http.ResponseWriter, r *http.Request) {
		j, err := s.Store.Job(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		revision, _ := strconv.ParseInt(r.PostFormValue("revision"), 10, 64)
		if revision != j.Revision {
			http.Error(w, "Job changed; reload before trying again", 409)
			return
		}
		j.Enabled = !j.Enabled
		if j.Enabled && j.Cron != "" {
			if err = s.validateRunnable(j); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		if err = s.Store.Save(j, false); err != nil {
			http.Error(w, "Could not update job; reload before trying again", 409)
			return
		}
		s.Reload()
		redirect(w, r, "/")
	})
	mux.ServeHTTP(w, r)
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

func (s *Server) allowAuth() bool {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	now := time.Now()
	if now.Sub(s.window) >= time.Minute {
		s.window = now
		s.attempts = 0
	}
	if s.attempts >= 5 {
		return false
	}
	s.attempts++
	return true
}
func (s *Server) authPage(w http.ResponseWriter, r *http.Request, kind, user string, hash []byte) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", 405)
		return
	}
	csrf := s.preauth(w, r)
	v := view{Page: kind, Title: "Sign in", CSRF: csrf}
	if kind == "setup" {
		v.Title = "Make this space yours"
	}
	if r.Method == http.MethodGet {
		render(w, 200, v)
		return
	}
	if !equal(r.PostFormValue("csrf"), csrf) {
		http.Error(w, "Invalid form token; reload the page", 403)
		return
	}
	if !s.allowAuth() {
		w.Header().Set("Retry-After", "60")
		v.Error = "Too many attempts. Try again in one minute."
		render(w, 429, v)
		return
	}
	select {
	case s.hashing <- struct{}{}:
		defer func() { <-s.hashing }()
	default:
		v.Error = "Another sign-in is in progress. Try again shortly."
		render(w, 429, v)
		return
	}
	name, password := r.PostFormValue("username"), r.PostFormValue("password")
	if kind == "setup" {
		if !equal(r.PostFormValue("setup_token"), s.SetupToken) || s.SetupToken == "" {
			v.Error = "The setup token is incorrect."
			render(w, 400, v)
			return
		}
		if !usernamePattern.MatchString(name) || len(password) < 12 || len(password) > 72 || password != r.PostFormValue("confirm_password") {
			v.Error = "Use a 3–64 character username (letters, digits, dots, underscores or hyphens), and matching passwords of 12–72 bytes."
			render(w, 400, v)
			return
		}
		encoded, err := bcrypt.GenerateFromPassword([]byte(password), 12)
		if err != nil {
			http.Error(w, "Could not secure password", 500)
			return
		}
		if err = s.Store.CreateAccount(name, encoded); err != nil {
			http.Error(w, "Account setup is already complete or unavailable", 409)
			return
		}
		_ = os.Remove(filepath.Join(s.Config.DataDir, "setup-token"))
	} else {
		// Always check the hash, including an incorrect username.
		hashErr := bcrypt.CompareHashAndPassword(hash, []byte(password))
		if !equal(name, user) || hashErr != nil {
			v.Error = "Username or password is incorrect."
			render(w, 401, v)
			return
		}
	}
	token, newCSRF := rand.Text(), rand.Text()
	if err := s.Store.AddSession(digest(token), newCSRF, time.Now().Add(12*time.Hour)); err != nil {
		http.Error(w, "Could not create session; sign in again", 503)
		return
	}
	s.cookie(w, "session", token, 12*60*60)
	s.cookie(w, "preauth", "", -1)
	redirect(w, r, "/")
}
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, csrf, user string) {
	jobs, err := s.Store.Jobs()
	if err != nil {
		http.Error(w, "Job storage unavailable", 503)
		return
	}
	page := 1
	if value := r.URL.Query().Get("page"); value != "" {
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 {
			http.Error(w, "Invalid history page", http.StatusBadRequest)
			return
		}
	}
	const pageSize = 25
	history, err := s.Store.History(page, pageSize)
	if err != nil {
		http.Error(w, "Execution history unavailable", 503)
		return
	}
	if page != history.Page {
		redirect(w, r, "/?page="+strconv.Itoa(history.Page)+"#activity")
		return
	}
	v := view{Page: "dashboard", Title: "Your automations", CSRF: csrf, Username: user, Timezone: s.Config.Timezone, HistoryPage: history.Page, HistoryPages: history.Pages, HistoryTotal: history.Total}
	for _, run := range history.Runs {
		v.Runs = append(v.Runs, executionView(run))
	}
	if history.Total > 0 {
		v.HistoryFrom = (history.Page-1)*pageSize + 1
		v.HistoryTo = v.HistoryFrom + len(history.Runs) - 1
	}
	if history.Page > 1 {
		v.PreviousPage = history.Page - 1
	}
	if history.Page < history.Pages {
		v.NextPage = history.Page + 1
	}
	v.RunToken = rand.Text()
	if r.URL.Query().Get("run") == "started" {
		v.Notice = "Run started. Refresh recent activity to see the result."
	}
	for _, j := range jobs {
		j.Timezone = adapters.JobTimezone(j, s.Config.Timezone)
		loc, _ := time.LoadLocation(j.Timezone)
		next := "Manual only"
		if j.Cron != "" {
			next = "Paused"
			if j.Enabled {
				schedule, err := adapters.ParseSchedule(j.Cron, j.Timezone)
				if s.validateRunnable(j) != nil {
					next = "Needs configuration"
				} else if err == nil {
					next = schedule.Next(time.Now()).In(loc).Format("02 Jan · 15:04:05 MST")
				}
			}
		}
		// Never pass stored headers into rendered views.
		j.Authorization = ""
		j.FallbackAuth = ""
		v.Jobs = append(v.Jobs, jobView{j, next, scheduleDescription(j.Cron), readSchedule(j.Cron).UsesTimezone()})
	}
	render(w, 200, v)
}
func (s *Server) jobPage(w http.ResponseWriter, r *http.Request, csrf string, create bool) {
	j := domain.Job{Enabled: false, Policy: "generic", Body: []byte(`{}`)}
	if !create {
		var err error
		j, err = s.Store.Job(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	has := j.Authorization != ""
	j.Authorization = ""
	s.renderJob(w, 200, csrf, j, create, has, readSchedule(j.Cron), "")
}
func (s *Server) saveJob(w http.ResponseWriter, r *http.Request, csrf string, create bool) {
	j := domain.Job{ID: strings.TrimSpace(r.PostFormValue("id")), Policy: "generic"}
	if !create {
		var err error
		j, err = s.Store.Job(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	has := j.Authorization != ""
	j.Name = strings.TrimSpace(r.PostFormValue("name"))
	j.URL = strings.TrimSpace(r.PostFormValue("url"))
	j.Body = []byte(strings.TrimSpace(r.PostFormValue("body")))
	cron, scheduleErr := buildSchedule(r.PostForm)
	j.Cron = cron
	if _, submitted := r.PostForm["timezone"]; submitted {
		j.Timezone = strings.TrimSpace(r.PostFormValue("timezone"))
	}
	var timeoutErr error
	if _, submitted := r.PostForm["request_timeout_ms"]; submitted {
		j.TimeoutMS = 0
		if raw := strings.TrimSpace(r.PostFormValue("request_timeout_ms")); raw != "" {
			j.TimeoutMS, timeoutErr = strconv.Atoi(raw)
			if timeoutErr != nil {
				timeoutErr = errors.New("request timeout must be a whole number of milliseconds")
			}
		}
	}
	if _, submitted := r.PostForm["recovery_settings"]; submitted {
		var countErr, delayErr error
		j.RetryCount, countErr = strconv.Atoi(r.PostFormValue("retry_count"))
		j.RetryDelayMS = 0
		if raw := strings.TrimSpace(r.PostFormValue("retry_delay_ms")); raw != "" {
			j.RetryDelayMS, delayErr = strconv.Atoi(raw)
		}
		if countErr != nil || delayErr != nil {
			timeoutErr = errors.New("retry count and delay must be whole numbers")
		}
		j.RetryUncertain = r.PostFormValue("retry_uncertain") == "on"
		j.FallbackURL = strings.TrimSpace(r.PostFormValue("fallback_url"))
		if auth := r.PostFormValue("fallback_authorization"); auth != "" {
			j.FallbackAuth = auth
		} else if r.PostFormValue("clear_fallback_authorization") == "on" {
			j.FallbackAuth = ""
		}
	}
	j.Enabled = r.PostFormValue("enabled") == "on"
	if auth := r.PostFormValue("authorization"); auth != "" {
		j.Authorization = auth
	} else if r.PostFormValue("clear_authorization") == "on" {
		j.Authorization = ""
	}
	if !create {
		j.Revision, _ = strconv.ParseInt(r.PostFormValue("revision"), 10, 64)
	}
	err := scheduleErr
	if err == nil {
		err = timeoutErr
	}
	if err == nil {
		err = adapters.ValidateJob(j, s.Config.Timezone)
	}
	if err == nil && j.Enabled && j.Cron != "" {
		err = s.validateRunnable(j)
	}
	status := 400
	if err == nil {
		err = s.Store.Save(j, create)
		if err != nil {
			status = 409
			err = errors.New("Could not save: the ID already exists or this job changed. Reload and try again.")
		}
	}
	if err != nil {
		j.Authorization = ""
		s.renderJob(w, status, csrf, j, create, has, submittedSchedule(r.PostForm), err.Error())
		return
	}
	s.Reload()
	redirect(w, r, "/")
}

func (s *Server) renderJob(w http.ResponseWriter, status int, csrf string, j domain.Job, create, has bool, schedule scheduleForm, message string) {
	zone := adapters.JobTimezone(j, s.Config.Timezone)
	hasFallback := j.FallbackAuth != ""
	j.Authorization, j.FallbackAuth = "", ""
	render(w, status, view{Page: "job", Title: "Edit automation", CSRF: csrf, Job: j, New: create, HasAuthorization: has, HasFallbackAuth: hasFallback, Timezone: zone, Schedule: schedule, Timezones: timezoneChoices(zone, s.Config.Timezone), Weekdays: weekdayChoices(schedule.Days), Error: message, RunToken: rand.Text(), DefaultTimeoutMS: s.Config.Timeout.Milliseconds()})
}

var runTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{26,128}$`)

func (s *Server) runJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.Store.Job(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	revision, _ := strconv.ParseInt(r.PostFormValue("revision"), 10, 64)
	if revision != j.Revision {
		http.Error(w, "Job changed; reload before running it", 409)
		return
	}
	token := r.PostFormValue("run_token")
	if !runTokenPattern.MatchString(token) {
		http.Error(w, "Invalid run token; reload before running it", 400)
		return
	}
	if err = adapters.ValidateJob(j, s.Config.Timezone); err == nil {
		err = s.validateRunnable(j)
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.closing || s.Runner == nil || s.Context == nil || s.Context.Err() != nil {
		http.Error(w, "Manual execution unavailable", 503)
		return
	}
	execute, err := s.Runner.Prepare(s.Context, j, "manual:"+token)
	if err != nil {
		http.Error(w, "Could not record execution; check recent activity before trying again", 503)
		return
	}
	if execute == nil {
		http.Error(w, "This run was already submitted, the task is running, or the job changed. Check recent activity.", 409)
		return
	}
	s.runs.Add(1)
	go func() {
		defer s.runs.Done()
		_, _ = execute()
	}()
	redirect(w, r, "/?run=started")
}

// Wait stops admitting manual runs and waits before the state store is closed.
func (s *Server) Wait() {
	s.runMu.Lock()
	s.closing = true
	s.runMu.Unlock()
	s.runs.Wait()
}

func (s *Server) validateRunnable(j domain.Job) error {
	telegram, err := s.Store.Telegram()
	if err != nil {
		return errors.New("notification settings unavailable")
	}
	return adapters.ValidateRunnable(j, telegram)
}
