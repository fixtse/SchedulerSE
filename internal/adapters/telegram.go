package adapters

import (
	"context"
	"database/sql"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"regexp"
	"strings"
	"time"
)

var telegramTokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
var telegramChatPattern = regexp.MustCompile(`^(-?[0-9]+|@[A-Za-z0-9_]{5,32})$`)

func ValidateTelegram(settings domain.TelegramSettings) error {
	if settings.Token != "" && (len(settings.Token) > 256 || !telegramTokenPattern.MatchString(settings.Token)) {
		return errors.New("enter a valid Telegram bot token")
	}
	if settings.Chat != "" && (len(settings.Chat) > 64 || !telegramChatPattern.MatchString(settings.Chat)) {
		return errors.New("enter a numeric chat ID or a public channel @username")
	}
	if settings.Enabled && (settings.Token == "" || settings.Chat == "") {
		return errors.New("set a bot token and chat ID before enabling Telegram")
	}
	return nil
}

func (s *Store) Telegram() (domain.TelegramSettings, error) {
	var settings domain.TelegramSettings
	err := s.DB.QueryRow("SELECT token,chat,enabled,revision,notify_success,notify_failure,notify_fallback FROM telegram_settings WHERE id=1").Scan(&settings.Token, &settings.Chat, &settings.Enabled, &settings.Revision, &settings.NotifySuccess, &settings.NotifyFailure, &settings.NotifyFallback)
	return settings, err
}

// ImportTelegram preserves an existing deployment's credentials once. Future changes
// belong to the UI, including disabling notifications or removing credentials.
func (s *Store) ImportTelegram(token, chat string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec("INSERT OR IGNORE INTO metadata(key,value) VALUES('telegram-imported','1')")
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		token, chat = strings.TrimSpace(token), strings.TrimSpace(chat)
		if _, err = tx.Exec("UPDATE telegram_settings SET token=?,chat=?,enabled=? WHERE id=1 AND revision=1 AND token='' AND chat=''", token, chat, token != "" && chat != ""); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SaveTelegram(settings domain.TelegramSettings) error {
	result, err := s.DB.Exec("UPDATE telegram_settings SET token=?,chat=?,enabled=?,notify_success=?,notify_failure=?,notify_fallback=?,revision=revision+1 WHERE id=1 AND revision=?", settings.Token, settings.Chat, settings.Enabled, settings.NotifySuccess, settings.NotifyFailure, settings.NotifyFallback, settings.Revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// ClaimTelegramTest records an attempt before sending. Reusing a form, overlapping
// tests, and rapid repeat requests cannot resend an uncertain Telegram POST.
func (s *Store) ClaimTelegramTest(token string, revision int64) (bool, error) {
	result, err := s.DB.Exec(`INSERT OR IGNORE INTO telegram_tests(token,revision,status,started)
 SELECT ?,?,'running',? WHERE EXISTS(SELECT 1 FROM telegram_settings WHERE id=1 AND revision=? AND enabled=1)
 AND NOT EXISTS(SELECT 1 FROM telegram_tests WHERE started>?)`, token, revision, time.Now().Unix(), revision, time.Now().Add(-10*time.Second).Unix())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) FinishTelegramTest(token, status string) error {
	_, err := s.DB.Exec("UPDATE telegram_tests SET status=? WHERE token=? AND status='running'", status, token)
	return err
}

type Telegram struct {
	Store *Store
	HTTP  *HTTP
}

// Every alert reads the latest saved settings without a restart or shared mutation.
func (t *Telegram) Send(ctx context.Context, text string) error {
	return t.Notify(ctx, domain.NotificationFailure, text)
}

func (t *Telegram) Notify(ctx context.Context, kind domain.NotificationType, text string) error {
	settings, err := t.Store.Telegram()
	if err != nil {
		return errors.New("notification settings unavailable")
	}
	if !settings.Accepts(kind) {
		return domain.ErrNotificationsDisabled
	}
	return t.SendSettings(ctx, settings, text)
}

func (t *Telegram) SendSettings(ctx context.Context, settings domain.TelegramSettings, text string) error {
	client := *t.HTTP
	client.Token, client.Chat = settings.Token, settings.Chat
	return client.Send(ctx, text)
}

// Existing installations keep failure and fallback alerts; success is opt-in.
func migrateTelegramNotifications(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(telegram_settings)")
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			break
		}
		columns[name] = true
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, column := range []struct{ name, defaultValue string }{
		{"notify_success", "0"},
		{"notify_failure", "1"},
		{"notify_fallback", "1"},
	} {
		if !columns[column.name] {
			if _, err = db.Exec("ALTER TABLE telegram_settings ADD COLUMN " + column.name + " INTEGER NOT NULL DEFAULT " + column.defaultValue); err != nil {
				return err
			}
		}
	}
	return nil
}
