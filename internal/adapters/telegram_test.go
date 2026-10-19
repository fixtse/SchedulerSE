package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTelegramPersistenceImportAndRevisions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.Telegram()
	if err != nil || initial.Enabled || initial.Token != "" || initial.Revision != 1 {
		t.Fatal("fresh Telegram settings are not empty")
	}
	if err = s.ImportTelegram("123456:original", "-10012345"); err != nil {
		t.Fatal(err)
	}
	settings, _ := s.Telegram()
	if !settings.Enabled || settings.Token != "123456:original" || settings.Chat != "-10012345" {
		t.Fatal("existing environment credentials were not imported")
	}
	stale := settings
	settings.Token, settings.Chat, settings.Enabled = "", "", false
	if err = s.SaveTelegram(settings); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveTelegram(stale); err != ErrConflict {
		t.Fatal("stale settings overwritten")
	}
	s.DB.Close()
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err = s.ImportTelegram("123456:old-environment", "12345"); err != nil {
		t.Fatal(err)
	}
	settings, _ = s.Telegram()
	if settings.Enabled || settings.Token != "" || settings.Chat != "" || settings.Revision != 2 {
		t.Fatal("old environment replaced UI settings after restart")
	}
}

func TestTelegramLiveSettingsAndSingleAttempt(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Error("Telegram request was not POST")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/bot123456:first/sendMessage":
			if payload["chat_id"] != "12345" {
				t.Error("initial chat missing")
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/bot123456:second/sendMessage":
			if payload["chat_id"] != "-10098765" {
				t.Error("updated chat missing")
			}
			w.WriteHeader(503)
		default:
			t.Error("unexpected token or endpoint")
		}
	}))
	defer server.Close()
	telegram := &Telegram{Store: s, HTTP: NewHTTP(Config{Timeout: time.Second, TelegramURL: server.URL})}
	if err := telegram.Send(context.Background(), "disabled"); !errors.Is(err, domain.ErrNotificationsDisabled) || calls.Load() != 0 {
		t.Fatal("disabled notifications made a request")
	}
	settings, _ := s.Telegram()
	settings.Token, settings.Chat, settings.Enabled = "123456:first", "12345", true
	if err := s.SaveTelegram(settings); err != nil {
		t.Fatal(err)
	}
	if err := telegram.Send(context.Background(), "first"); err != nil || calls.Load() != 1 {
		t.Fatalf("first notification: %v", err)
	}
	settings, _ = s.Telegram()
	settings.Token, settings.Chat = "123456:second", "-10098765"
	if err := s.SaveTelegram(settings); err != nil {
		t.Fatal(err)
	}
	if err := telegram.Send(context.Background(), "second"); err == nil || calls.Load() != 2 {
		t.Fatal("new credentials not used or failed send retried")
	}
	settings, _ = s.Telegram()
	settings.Enabled = false
	if err := s.SaveTelegram(settings); err != nil {
		t.Fatal(err)
	}
	if err := telegram.Send(context.Background(), "disabled"); !errors.Is(err, domain.ErrNotificationsDisabled) || calls.Load() != 2 {
		t.Fatal("disabling notifications did not apply immediately")
	}
}

func TestTelegramTestClaimsSurviveRestart(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if err := s.ImportTelegram("123456:token", "12345"); err != nil {
		t.Fatal(err)
	}
	settings, _ := s.Telegram()
	if ok, err := s.ClaimTelegramTest("first", settings.Revision); err != nil || !ok {
		t.Fatalf("claim: %v", err)
	}
	if ok, _ := s.ClaimTelegramTest("different", settings.Revision); ok {
		t.Fatal("overlapping Telegram test admitted")
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE telegram_tests SET started=0"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ClaimTelegramTest("first", settings.Revision); ok {
		t.Fatal("uncertain Telegram test replayed")
	}
	if ok, _ := s.ClaimTelegramTest("stale", settings.Revision+1); ok {
		t.Fatal("stale Telegram settings tested")
	}
	if ok, err := s.ClaimTelegramTest("second", settings.Revision); err != nil || !ok {
		t.Fatalf("new test not admitted: %v", err)
	}
}

func TestTelegramValidation(t *testing.T) {
	for _, settings := range []domain.TelegramSettings{
		{Token: "123456:token", Chat: "-10012345", Enabled: true},
		{Token: "123456:token", Chat: "@channel_name", Enabled: true},
		{},
	} {
		if err := ValidateTelegram(settings); err != nil {
			t.Fatal(err)
		}
	}
	for _, settings := range []domain.TelegramSettings{
		{Enabled: true},
		{Token: "123456:token/other", Chat: "12345", Enabled: true},
		{Token: "123456:token\n", Chat: "12345", Enabled: true},
		{Token: "123456:token", Chat: "invalid chat", Enabled: true},
	} {
		if ValidateTelegram(settings) == nil {
			t.Fatal("invalid Telegram settings accepted")
		}
	}
}
