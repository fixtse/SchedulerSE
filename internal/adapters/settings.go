package adapters

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fixtse/SchedulerSE/internal/domain"
)

func ValidateAppSettings(settings domain.AppSettings) error {
	if _, err := time.LoadLocation(settings.Timezone); err != nil || settings.Timezone == "" || settings.Timezone == "Local" || strings.ContainsAny(settings.Timezone, " \t\r\n") {
		return errors.New("choose a valid IANA timezone")
	}
	if settings.TimeoutMS < 100 || settings.TimeoutMS > 600000 {
		return errors.New("execution timeout must be 100–600000 milliseconds")
	}
	return nil
}

func (s *Store) AppSettings() (domain.AppSettings, error) {
	var settings domain.AppSettings
	var raw string
	if err := s.DB.QueryRow("SELECT value FROM metadata WHERE key='app_settings'").Scan(&raw); err != nil {
		return settings, err
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return settings, err
	}
	return settings, ValidateAppSettings(settings)
}

// ImportAppSettings preserves legacy environment values once. Saved UI settings
// take precedence on subsequent starts, even if the old environment remains.
func (s *Store) ImportAppSettings(c Config) (domain.AppSettings, error) {
	settings, err := s.AppSettings()
	if err == nil {
		return settings, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return settings, err
	}
	settings = domain.AppSettings{Timezone: c.Timezone, TimeoutMS: int(c.Timeout / time.Millisecond), CookieSecure: c.CookieSecure}
	if raw := os.Getenv("TZ"); raw != "" {
		settings.Timezone = raw
	}
	if raw := os.Getenv("REQUEST_TIMEOUT_MS"); raw != "" {
		settings.TimeoutMS, err = strconv.Atoi(raw)
		if err != nil {
			return settings, errors.New("invalid legacy execution timeout")
		}
	}
	if raw := os.Getenv("COOKIE_SECURE"); raw != "" {
		if raw != "true" && raw != "false" {
			return settings, errors.New("invalid legacy secure-cookie setting")
		}
		settings.CookieSecure = raw == "true"
	}
	if err = ValidateAppSettings(settings); err != nil {
		return settings, err
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return settings, err
	}
	if _, err = s.DB.Exec("INSERT OR IGNORE INTO metadata(key,value) VALUES('app_settings',?)", string(data)); err != nil {
		return settings, err
	}
	return s.AppSettings()
}

func (s *Store) SaveAppSettings(settings domain.AppSettings) error {
	if err := ValidateAppSettings(settings); err != nil {
		return err
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("INSERT INTO metadata(key,value) VALUES('app_settings',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(data))
	return err
}

func (s *Store) ExecutionDefaults(fallback time.Duration) (domain.AppSettings, error) {
	settings, err := s.AppSettings()
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AppSettings{TimeoutMS: int(fallback / time.Millisecond)}, nil
	}
	return settings, err
}
