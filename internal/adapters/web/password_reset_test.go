package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fixtse/SchedulerSE/internal/adapters"
	"golang.org/x/crypto/bcrypt"
)

func TestTelegramPasswordRecoveryAndSessionRevocation(t *testing.T) {
	f := fixture(t)
	oldHash, err := bcrypt.GenerateFromPassword([]byte("previous-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.CreateAccount("owner", oldHash); err != nil {
		t.Fatal(err)
	}
	if err = f.store.AddSession(digest("old-session"), "old-csrf", time.Now().Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	messages := make(chan string, 2)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Chat string `json:"chat_id"`
			Text string `json:"text"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Chat != "123456" {
			t.Error("invalid recovery destination")
		}
		messages <- body.Text
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mock.Close()
	settings, err := f.store.Telegram()
	if err != nil {
		t.Fatal(err)
	}
	// Recovery is available with routine notifications and every notification type off.
	settings.Token, settings.Chat = "123456:test-secret", "123456"
	settings.Enabled, settings.NotifyFailure, settings.NotifySuccess, settings.NotifyFallback = false, false, false, false
	if err = f.store.SaveTelegram(settings); err != nil {
		t.Fatal(err)
	}
	f.app.Telegram = &adapters.Telegram{Store: f.store, HTTP: adapters.NewHTTP(adapters.Config{TelegramURL: mock.URL})}
	code, page := f.get(t, "/login")
	if code != 200 || !strings.Contains(page, "Forgot password?") || !strings.Contains(page, `name="remember"`) {
		t.Fatal("missing recovery or remember option")
	}
	code, page = f.get(t, "/forgot-password")
	if code != 200 {
		t.Fatal("recovery page requires sign-in")
	}
	csrfToken := csrf(t, page)
	code, _, _ = f.post(t, "/forgot-password", url.Values{"csrf": {"forged"}})
	if code != 403 || calls.Load() != 0 {
		t.Fatal("recovery sent without CSRF")
	}
	code, page, _ = f.post(t, "/forgot-password", url.Values{"csrf": {csrfToken}})
	if code != 200 || calls.Load() != 1 {
		t.Fatal("recovery did not send", code, page)
	}
	message := <-messages
	tokenMatch := regexp.MustCompile(`single-use token:\n([A-Za-z0-9_-]{26,128})\n`).FindStringSubmatch(message)
	if len(tokenMatch) != 2 {
		t.Fatal("missing recovery token")
	}
	token := tokenMatch[1]
	if strings.Contains(page, token) || strings.Contains(page, "test-secret") || strings.Contains(page, "123456") {
		t.Fatal("recovery page leaked secret or destination")
	}
	var savedDigest string
	if err = f.store.DB.QueryRow("SELECT token_hash FROM password_resets").Scan(&savedDigest); err != nil || savedDigest != digest(token) {
		t.Fatal("reset token was not stored as a digest", err)
	}
	code, _, _ = f.post(t, "/forgot-password", url.Values{"csrf": {csrfToken}})
	if code != 200 || calls.Load() != 1 {
		t.Fatal("duplicate recovery request resent Telegram message")
	}
	_, page = f.get(t, "/reset-password")
	reset := url.Values{"csrf": {csrf(t, page)}, "reset_token": {token}, "password": {"new-safe-password"}, "confirm_password": {"new-safe-password"}}
	reset.Set("csrf", "forged")
	code, _, _ = f.post(t, "/reset-password", reset)
	if code != 403 {
		t.Fatal("reset accepted invalid CSRF")
	}
	reset.Set("csrf", csrf(t, page))
	reset.Set("reset_token", "invalid")
	code, _, _ = f.post(t, "/reset-password", reset)
	if code != 400 {
		t.Fatal("invalid reset token accepted")
	}
	reset.Set("reset_token", token)
	reset.Set("confirm_password", "different-password")
	code, _, _ = f.post(t, "/reset-password", reset)
	if code != 400 {
		t.Fatal("mismatched password accepted")
	}
	reset.Set("confirm_password", "new-safe-password")
	code, page, response := f.post(t, "/reset-password", reset)
	if code != 303 || response.Header.Get("Location") != "/login?reset=complete" || strings.Contains(page, token) {
		t.Fatal("reset failed", code, page)
	}
	_, newHash, err := f.store.Account()
	if err != nil || bcrypt.CompareHashAndPassword(newHash, []byte("new-safe-password")) != nil {
		t.Fatal("new password not secured", err)
	}
	if _, err = f.store.Session(digest("old-session")); err == nil {
		t.Fatal("remembered session survived password reset")
	}
	if valid, err := f.store.ValidPasswordReset(digest(token)); err != nil || valid {
		t.Fatal("reset token reusable", err)
	}
	code, page = f.get(t, "/login?reset=complete")
	if code != 200 || !strings.Contains(page, "Password reset complete") {
		t.Fatal("missing reset completion notice")
	}
}

func TestRecoveryWithoutTelegramDoesNotRevealConfiguration(t *testing.T) {
	f := fixture(t)
	if err := f.store.CreateAccount("owner", []byte("unused-hash")); err != nil {
		t.Fatal(err)
	}
	_, page := f.get(t, "/forgot-password")
	code, page, _ := f.post(t, "/forgot-password", url.Values{"csrf": {csrf(t, page)}})
	if code != 200 || !strings.Contains(page, "If Telegram recovery is configured") {
		t.Fatal("unconfigured recovery disclosed state", code, page)
	}
	var count int
	if err := f.store.DB.QueryRow("SELECT count(*) FROM password_resets").Scan(&count); err != nil || count != 0 {
		t.Fatal("unconfigured recovery issued token", err)
	}
}

func TestStaySignedInControlsCookieAndDurableSessionLifetime(t *testing.T) {
	f := fixture(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("test-sign-in-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.CreateAccount("owner", hash); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		remember string
		seconds  int
	}{{"", 12 * 60 * 60}, {"on", 30 * 24 * 60 * 60}} {
		_, page := f.get(t, "/login")
		code, body, res := f.post(t, "/login", url.Values{"csrf": {csrf(t, page)}, "username": {"owner"}, "password": {"test-sign-in-password"}, "remember": {tc.remember}})
		if code != 303 {
			t.Fatal("sign-in failed", code, body)
		}
		var cookie *http.Cookie
		for _, item := range res.Cookies() {
			if item.Name == "session" {
				cookie = item
			}
		}
		if cookie == nil || cookie.MaxAge != tc.seconds || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatal("incorrect remembered session cookie", cookie)
		}
		var expires int64
		if err = f.store.DB.QueryRow("SELECT expires FROM sessions WHERE token=?", digest(cookie.Value)).Scan(&expires); err != nil {
			t.Fatal(err)
		}
		remaining := expires - time.Now().Unix()
		if remaining < int64(tc.seconds-5) || remaining > int64(tc.seconds) {
			t.Fatal("cookie and durable session expiry disagree", remaining)
		}
	}
}

func TestUncertainTelegramRecoveryDoesNotResendOrInvalidateDeliveredToken(t *testing.T) {
	f := fixture(t)
	if err := f.store.CreateAccount("owner", []byte("old-hash")); err != nil {
		t.Fatal(err)
	}
	settings, err := f.store.Telegram()
	if err != nil {
		t.Fatal(err)
	}
	settings.Token, settings.Chat = "123456:test-secret", "123456"
	if err = f.store.SaveTelegram(settings); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	messages := make(chan string, 1)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		messages <- body.Text
		w.WriteHeader(503)
		_, _ = w.Write([]byte("private upstream error"))
	}))
	defer mock.Close()
	f.app.Telegram = &adapters.Telegram{Store: f.store, HTTP: adapters.NewHTTP(adapters.Config{TelegramURL: mock.URL})}
	_, page := f.get(t, "/forgot-password")
	csrfToken := csrf(t, page)
	code, page, _ := f.post(t, "/forgot-password", url.Values{"csrf": {csrfToken}})
	message := <-messages
	token := ""
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		if line == "Your single-use token:" && i+1 < len(lines) {
			token = lines[i+1]
		}
	}
	if code != 200 || strings.Contains(page, "private upstream error") || token == "" {
		t.Fatal("uncertain recovery response incorrect", code)
	}
	if valid, err := f.store.ValidPasswordReset(digest(token)); err != nil || !valid {
		t.Fatal("possibly delivered reset token was discarded", err)
	}
	code, _, _ = f.post(t, "/forgot-password", url.Values{"csrf": {csrfToken}})
	if code != 200 || calls.Load() != 1 {
		t.Fatal("uncertain delivery was resent")
	}
}
