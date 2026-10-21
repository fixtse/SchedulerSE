package web

import (
	"context"
	"crypto/rand"
	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) renderTelegram(w http.ResponseWriter, status int, csrf, user string, settings domain.TelegramSettings, message, notice string) {
	hasToken := settings.Token != ""
	settings.Token = ""
	render(w, status, view{Page: "telegram", Title: "Telegram notifications", Username: user, CSRF: csrf, Telegram: settings, HasBotToken: hasToken, TestToken: rand.Text(), Error: message, Notice: notice})
}

func (s *Server) telegramPage(w http.ResponseWriter, r *http.Request, csrf, user string) {
	settings, err := s.Store.Telegram()
	if err != nil {
		http.Error(w, "Notification settings unavailable", 503)
		return
	}
	notice := ""
	switch r.URL.Query().Get("result") {
	case "saved":
		notice = "Telegram settings saved. Changes apply immediately."
	case "sent":
		notice = "Telegram accepted the test message. Check the selected chat."
	}
	s.renderTelegram(w, 200, csrf, user, settings, "", notice)
}

func (s *Server) saveTelegram(w http.ResponseWriter, r *http.Request, csrf, user string) {
	settings, err := s.Store.Telegram()
	if err != nil {
		http.Error(w, "Notification settings unavailable", 503)
		return
	}
	revision, _ := strconv.ParseInt(r.PostFormValue("revision"), 10, 64)
	if revision != settings.Revision {
		http.Error(w, "Settings changed; reload before saving", 409)
		return
	}
	storedToken := settings.Token
	if token := strings.TrimSpace(r.PostFormValue("bot_token")); token != "" {
		settings.Token = token
	} else if r.PostFormValue("clear_token") == "on" {
		settings.Token = ""
	}
	settings.Chat = strings.TrimSpace(r.PostFormValue("chat_id"))
	settings.Enabled = r.PostFormValue("enabled") == "on"
	if r.PostFormValue("notification_types_present") == "1" {
		settings.NotifySuccess = r.PostFormValue("notify_success") == "on"
		settings.NotifyFailure = r.PostFormValue("notify_failure") == "on"
		settings.NotifyFallback = r.PostFormValue("notify_fallback") == "on"
	}
	if err = adapters.ValidateTelegram(settings); err != nil {
		settings.Token = storedToken
		s.renderTelegram(w, 400, csrf, user, settings, err.Error(), "")
		return
	}
	if err = s.Store.SaveTelegram(settings); err != nil {
		http.Error(w, "Could not save settings; reload before trying again", 409)
		return
	}
	s.Reload()
	redirect(w, r, "/settings?tab=telegram&result=saved")
}

func (s *Server) testTelegram(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.Telegram()
	if err != nil {
		http.Error(w, "Notification settings unavailable", 503)
		return
	}
	revision, _ := strconv.ParseInt(r.PostFormValue("revision"), 10, 64)
	if revision != settings.Revision {
		http.Error(w, "Settings changed; reload before testing", 409)
		return
	}
	if err = adapters.ValidateTelegram(settings); err != nil || !settings.Enabled {
		http.Error(w, "Save and enable valid Telegram settings before testing", 400)
		return
	}
	token := r.PostFormValue("test_token")
	if !runTokenPattern.MatchString(token) {
		http.Error(w, "Invalid test token; reload the settings page", 400)
		return
	}
	if s.Telegram == nil {
		http.Error(w, "Telegram testing unavailable", 503)
		return
	}
	claimed, err := s.Store.ClaimTelegramTest(token, revision)
	if err != nil {
		http.Error(w, "Could not record test; check your chat before trying again", 503)
		return
	}
	if !claimed {
		http.Error(w, "This test was submitted, another test is running, or settings changed. Wait 10 seconds and reload to test again.", 409)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err = s.Telegram.SendSettings(ctx, settings, "SchedulerSE: Telegram notifications are connected. This is a test message.")
	status := "accepted"
	if err != nil {
		status = "uncertain"
	}
	if finishErr := s.Store.FinishTelegramTest(token, status); finishErr != nil {
		http.Error(w, "Test result could not be recorded. Check your chat before trying again.", 503)
		return
	}
	if err != nil {
		http.Error(w, "Could not confirm delivery. Check the bot token, chat ID, and bot access to the chat. Check your chat before sending another test; the message may have been delivered. No automatic retry was made.", 502)
		return
	}
	redirect(w, r, "/settings?tab=telegram&result=sent")
}
