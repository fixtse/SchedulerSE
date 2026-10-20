package adapters

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetStore(t *testing.T) *Store {
	t.Helper()
	store := testStore(t)
	if err := store.CreateAccount("owner", []byte("old-password-hash")); err != nil {
		t.Fatal(err)
	}
	settings, err := store.Telegram()
	if err != nil {
		t.Fatal(err)
	}
	settings.Token, settings.Chat = "123456:test-token", "123456"
	if err = store.SaveTelegram(settings); err != nil {
		t.Fatal(err)
	}
	return store
}
func issueReset(t *testing.T, store *Store, hash string) bool {
	t.Helper()
	settings, err := store.Telegram()
	if err != nil {
		t.Fatal(err)
	}
	_, password, err := store.Account()
	if err != nil {
		t.Fatal(err)
	}
	ok, err := store.IssuePasswordReset(hash, password, settings.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestPasswordResetSingleUseRevokesSessionsAndAllTokens(t *testing.T) {
	store := resetStore(t)
	for _, name := range []string{"normal-session", "remembered-session"} {
		if err := store.AddSession(name, "csrf", time.Now().Add(30*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if !issueReset(t, store, "first-token-digest") {
		t.Fatal("token not issued")
	}
	if issueReset(t, store, "too-soon-digest") {
		t.Fatal("Telegram cooldown bypassed")
	}
	if _, err := store.DB.Exec("UPDATE password_resets SET issued=issued-61"); err != nil {
		t.Fatal(err)
	}
	if !issueReset(t, store, "second-token-digest") {
		t.Fatal("second token not issued after cooldown")
	}
	var winners atomic.Int32
	var active sync.WaitGroup
	for range 4 {
		active.Add(1)
		go func() {
			defer active.Done()
			ok, err := store.ResetPassword("first-token-digest", []byte("new-password-hash"))
			if err != nil {
				t.Error(err)
			}
			if ok {
				winners.Add(1)
			}
		}()
	}
	active.Wait()
	if winners.Load() != 1 {
		t.Fatal("concurrent reset was not single use", winners.Load())
	}
	username, password, err := store.Account()
	if err != nil || username != "owner" || string(password) != "new-password-hash" {
		t.Fatal("password was not updated", err)
	}
	for _, name := range []string{"normal-session", "remembered-session"} {
		if _, err := store.Session(name); err == nil {
			t.Fatal("old session survived reset")
		}
	}
	for _, hash := range []string{"first-token-digest", "second-token-digest"} {
		if valid, err := store.ValidPasswordReset(hash); err != nil || valid {
			t.Fatal("outstanding token survived reset", err)
		}
	}
}

func TestPasswordResetExpiryConfigurationBindingAndHourlyLimit(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		store := resetStore(t)
		if !issueReset(t, store, "expired-digest") {
			t.Fatal("issue failed")
		}
		if _, err := store.DB.Exec("UPDATE password_resets SET expires=?", time.Now().Add(-time.Second).Unix()); err != nil {
			t.Fatal(err)
		}
		if ok, err := store.ResetPassword("expired-digest", []byte("new")); err != nil || ok {
			t.Fatal("expired token changed password", err)
		}
	})
	t.Run("Telegram change", func(t *testing.T) {
		store := resetStore(t)
		if !issueReset(t, store, "old-chat-digest") {
			t.Fatal("issue failed")
		}
		settings, err := store.Telegram()
		if err != nil {
			t.Fatal(err)
		}
		settings.Chat = "654321"
		if err = store.SaveTelegram(settings); err != nil {
			t.Fatal(err)
		}
		if ok, err := store.ResetPassword("old-chat-digest", []byte("new")); err != nil || ok {
			t.Fatal("old Telegram settings could recover account", err)
		}
	})
	t.Run("hourly limit", func(t *testing.T) {
		store := resetStore(t)
		for i, hash := range []string{"one", "two", "three", "four", "five"} {
			if _, err := store.DB.Exec("UPDATE password_resets SET issued=?", time.Now().Add(-time.Duration(i+1)*time.Minute-time.Second).Unix()); err != nil {
				t.Fatal(err)
			}
			if !issueReset(t, store, hash) {
				t.Fatal("token not issued before hourly limit", i)
			}
		}
		if _, err := store.DB.Exec("UPDATE password_resets SET issued=issued-61"); err != nil {
			t.Fatal(err)
		}
		if issueReset(t, store, "six") {
			t.Fatal("hourly Telegram limit bypassed")
		}
	})
}

func TestPasswordResetRollsBackIfSessionRevocationFails(t *testing.T) {
	store := resetStore(t)
	if err := store.AddSession("existing", "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !issueReset(t, store, "reset-digest") {
		t.Fatal("issue failed")
	}
	if _, err := store.DB.Exec(`CREATE TRIGGER fail_session_revoke BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT,'simulated storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ResetPassword("reset-digest", []byte("new-password")); err == nil || ok {
		t.Fatal("reset succeeded without revoking sessions")
	}
	_, password, err := store.Account()
	if err != nil || string(password) != "old-password-hash" {
		t.Fatal("failed transaction changed password", err)
	}
	if valid, err := store.ValidPasswordReset("reset-digest"); err != nil || !valid {
		t.Fatal("failed transaction consumed token", err)
	}
}

func TestLoginWithPreviouslyVerifiedPasswordCannotSurviveRecovery(t *testing.T) {
	store := resetStore(t)
	if !issueReset(t, store, "reset-digest") {
		t.Fatal("issue failed")
	}
	ok, err := store.ResetPassword("reset-digest", []byte("new-password-hash"))
	if err != nil || !ok {
		t.Fatal("reset failed", err)
	}
	if err = store.AddSessionForPassword("stale-login", "csrf", time.Now().Add(30*24*time.Hour), []byte("old-password-hash")); err != ErrConflict {
		t.Fatal("stale login was allowed after reset", err)
	}
	if _, err = store.Session("stale-login"); err == nil {
		t.Fatal("stale login session was persisted")
	}
	if err = store.AddSessionForPassword("new-login", "csrf", time.Now().Add(30*24*time.Hour), []byte("new-password-hash")); err != nil {
		t.Fatal("current password could not sign in", err)
	}
}
