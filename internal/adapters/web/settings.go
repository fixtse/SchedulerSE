package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/domain"
)

func (s *Server) settings() domain.AppSettings {
	if saved := s.appSettings.Load(); saved != nil {
		return *saved
	}
	return domain.AppSettings{Timezone: s.Config.Timezone, TimeoutMS: int(s.Config.Timeout / time.Millisecond), CookieSecure: s.Config.CookieSecure}
}
func (s *Server) defaultTimeout() time.Duration {
	return time.Duration(s.settings().TimeoutMS) * time.Millisecond
}

func (s *Server) generalSettings(w http.ResponseWriter, r *http.Request, csrf, user string) {
	if r.Method == http.MethodPost {
		s.settingsMu.Lock()
		defer s.settingsMu.Unlock()
	}
	settings := s.settings()
	if r.Method == http.MethodGet {
		s.renderGeneralSettings(w, 200, csrf, user, settings, "", "")
		return
	}
	settings.Timezone = strings.TrimSpace(r.PostFormValue("timezone"))
	var err error
	settings.TimeoutMS, err = strconv.Atoi(r.PostFormValue("request_timeout_ms"))
	settings.CookieSecure = r.PostFormValue("cookie_secure") == "on"
	message := ""
	if err != nil {
		message = "Execution timeout must be a whole number of milliseconds."
	} else if err = adapters.ValidateAppSettings(settings); err != nil {
		message = err.Error()
	}
	if settings.CookieSecure && !requestUsesHTTPS(r) {
		message = "Open SchedulerSE over HTTPS before enabling secure cookies."
	}
	if message != "" {
		s.renderGeneralSettings(w, 400, csrf, user, settings, message, "")
		return
	}
	// Preserve the signed-in browser's original session expiry when updating its
	// Secure attribute, including remembered sessions.
	cookie, err := r.Cookie("session")
	if err != nil {
		http.Error(w, "Session unavailable; sign in again", 401)
		return
	}
	var expires int64
	if err = s.Store.DB.QueryRow("SELECT expires FROM sessions WHERE token=?", digest(cookie.Value)).Scan(&expires); err != nil {
		http.Error(w, "Session storage unavailable", 503)
		return
	}
	if err = s.Store.SaveAppSettings(settings); err != nil {
		s.renderGeneralSettings(w, 503, csrf, user, settings, "Could not save settings. Try again.", "")
		return
	}
	s.appSettings.Store(&settings)
	s.Reload()
	s.cookie(w, "session", cookie.Value, max(1, int(time.Until(time.Unix(expires, 0))/time.Second)))
	s.renderGeneralSettings(w, 200, csrf, user, settings, "", "Settings saved. New runs and schedules use these defaults.")
}

func requestUsesHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	for _, value := range []string{r.Header.Get("Origin"), r.Referer()} {
		parsed, err := url.Parse(value)
		if err == nil && parsed.Scheme == "https" && parsed.Host == r.Host && parsed.User == nil {
			return true
		}
	}
	return false
}

func (s *Server) renderGeneralSettings(w http.ResponseWriter, status int, csrf, user string, settings domain.AppSettings, message, notice string) {
	render(w, status, view{Page: "settings", Title: "General settings", CSRF: csrf, Username: user, AppSettings: settings, Timezones: timezoneChoices(settings.Timezone, s.Config.Timezone), Error: message, Notice: notice})
}

// Settings sections share one URL and navigation, with native links for no-JS clients.
func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request, csrf, user string) {
	switch r.URL.Query().Get("tab") {
	case "telegram":
		s.telegramPage(w, r, csrf, user)
	case "agent":
		s.agentSettingsPage(w, r, csrf, user)
	default:
		s.generalSettings(w, r, csrf, user)
	}
}
