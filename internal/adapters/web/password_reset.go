package web

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"time"

	"github.com/fixtse/SchedulerSE/internal/adapters"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) passwordRecovery(w http.ResponseWriter, r *http.Request, kind string, password []byte) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", 405)
		return
	}
	csrf := s.preauth(w, r)
	title := "Forgot your password?"
	if kind == "reset-password" {
		title = "Choose a new password"
	}
	v := view{Page: kind, Title: title, CSRF: csrf}
	if r.Method == http.MethodGet {
		render(w, 200, v)
		return
	}
	if !equal(r.PostFormValue("csrf"), csrf) {
		http.Error(w, "Invalid form token; reload the page", 403)
		return
	}
	if kind == "forgot-password" {
		// A uniform response does not expose the configured destination or bot details.
		// Record before sending; uncertain Telegram deliveries are never retried.
		settings, err := s.Store.Telegram()
		if err == nil && s.Telegram != nil && s.Telegram.HTTP != nil && settings.Token != "" && settings.Chat != "" && adapters.ValidateTelegram(settings) == nil {
			token := rand.Text()
			claimed, claimErr := s.Store.IssuePasswordReset(digest(token), password, settings.Revision)
			if claimErr != nil {
				http.Error(w, "Password recovery storage unavailable", 503)
				return
			}
			if claimed {
				ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				_ = s.Telegram.SendSettings(ctx, settings, "SchedulerSE password reset\n\nYour single-use token:\n"+token+"\n\nThis token expires in 10 minutes. Open your usual SchedulerSE sign-in page, choose Forgot password, then Use a reset token.\n\nIf you did not request this, ignore this message. Your password has not changed.")
				cancel()
			}
		}
		v.Notice = "If Telegram recovery is configured, check the saved chat for a reset token. Wait at least one minute before requesting another (up to five requests per hour). Delivery is attempted once."
		render(w, 200, v)
		return
	}
	if !s.allowAuth() {
		w.Header().Set("Retry-After", "60")
		v.Error = "Too many attempts. Try again in one minute."
		render(w, 429, v)
		return
	}
	token := strings.TrimSpace(r.PostFormValue("reset_token"))
	newPassword := r.PostFormValue("password")
	if len(newPassword) < 12 || len(newPassword) > 72 || newPassword != r.PostFormValue("confirm_password") {
		v.Error = "Use matching passwords of 12–72 bytes."
		render(w, 400, v)
		return
	}
	valid := false
	var err error
	if runTokenPattern.MatchString(token) {
		valid, err = s.Store.ValidPasswordReset(digest(token))
	}
	if err != nil {
		http.Error(w, "Password recovery storage unavailable", 503)
		return
	}
	if !valid {
		v.Error = "This reset token is invalid or expired. Request another token if needed."
		render(w, 400, v)
		return
	}
	select {
	case s.hashing <- struct{}{}:
		defer func() { <-s.hashing }()
	default:
		v.Error = "Another sign-in or reset is in progress. Try again shortly."
		render(w, 429, v)
		return
	}
	encoded, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		http.Error(w, "Could not secure password", 500)
		return
	}
	changed, err := s.Store.ResetPassword(digest(token), encoded)
	if err != nil {
		http.Error(w, "Could not reset password", 503)
		return
	}
	if !changed {
		v.Error = "This reset token is invalid or expired. Request another token if needed."
		render(w, 400, v)
		return
	}
	s.cookie(w, "session", "", -1)
	s.cookie(w, "preauth", "", -1)
	redirect(w, r, "/login?reset=complete")
}
