package adapters

import (
	"encoding/json"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"github.com/robfig/cron/v3"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	DataDir, Address, Timezone, TelegramToken, TelegramChat, TelegramURL string
	CookieSecure                                                         bool
	Timeout                                                              time.Duration
}

func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
func LoadConfig() (Config, error) {
	c := Config{DataDir: env("DATA_DIR", "./data"), Address: env("LISTEN_ADDR", ":"+env("HEALTH_PORT", "8789")), Timezone: env("TZ", "Europe/Madrid"), TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"), TelegramChat: os.Getenv("TELEGRAM_CHAT_ID"), TelegramURL: env("TELEGRAM_API_URL", "https://api.telegram.org")}
	secure := env("COOKIE_SECURE", "false")
	if secure != "true" && secure != "false" {
		return c, errors.New("COOKIE_SECURE must be true or false")
	}
	c.CookieSecure = secure == "true"
	n, err := strconv.Atoi(env("REQUEST_TIMEOUT_MS", "120000"))
	if err != nil || n < 100 || n > 600000 {
		return c, errors.New("invalid REQUEST_TIMEOUT_MS")
	}
	c.Timeout = time.Duration(n) * time.Millisecond
	if _, err = time.LoadLocation(c.Timezone); err != nil {
		return c, errors.New("invalid TZ")
	}
	if err = ValidateEndpoint(c.TelegramURL); err != nil {
		return c, err
	}
	return c, nil
}

var jobID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func ValidateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return errors.New("invalid endpoint; embedded credentials and fragments are forbidden")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return errors.New("endpoint must use HTTPS; HTTP is allowed only on loopback for local tests")
	}
	return nil
}
func JobTimezone(j domain.Job, fallback string) string {
	if j.Timezone != "" {
		return j.Timezone
	}
	return fallback
}

func ParseSchedule(spec, tz string) (cron.Schedule, error) {
	if _, err := time.LoadLocation(tz); err != nil || strings.ContainsAny(tz, " \t\r\n") || tz == "Local" {
		return nil, errors.New("invalid timezone; choose an IANA timezone")
	}
	if len(strings.Fields(spec)) != 5 && len(strings.Fields(spec)) != 6 {
		return nil, errors.New("cron needs five fields or six including seconds")
	}
	parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse("CRON_TZ=" + tz + " " + spec)
	if err != nil {
		return nil, errors.New("invalid cron expression")
	}
	if schedule.Next(time.Now()).IsZero() {
		return nil, errors.New("cron has no upcoming occurrence")
	}
	return schedule, nil
}
func ValidateJob(j domain.Job, tz string) error {
	if j.TimeoutMS != 0 && (j.TimeoutMS < 100 || j.TimeoutMS > 600000) {
		return errors.New("request timeout must be 100–600000 milliseconds, or blank to use the server default")
	}
	if j.RetryCount < 0 || j.RetryCount > 5 {
		return errors.New("retry count must be between 0 and 5")
	}
	if j.RetryDelayMS != 0 && (j.RetryDelayMS < 100 || j.RetryDelayMS > 60000) {
		return errors.New("retry delay must be 100–60000 milliseconds, or blank for 1000")
	}
	if j.FallbackURL != "" {
		if err := ValidateEndpoint(j.FallbackURL); err != nil {
			return errors.New("invalid fallback endpoint: " + err.Error())
		}
	}
	if len(j.FallbackAuth) > 8192 || strings.ContainsAny(j.FallbackAuth, "\r\n") {
		return errors.New("invalid fallback authorization header")
	}
	tz = JobTimezone(j, tz)
	if _, err := time.LoadLocation(tz); err != nil || strings.ContainsAny(tz, " \t\r\n") || tz == "Local" {
		return errors.New("invalid timezone; choose an IANA timezone")
	}
	if j.ID == "new" {
		return errors.New("ID new is reserved; choose another ID")
	}
	if !jobID.MatchString(j.ID) {
		return errors.New("ID must contain lowercase letters, digits or hyphens, up to 64 characters")
	}
	if strings.TrimSpace(j.Name) == "" || len(j.Name) > 100 || strings.ContainsAny(j.Name, "\r\n") {
		return errors.New("name must be 1–100 characters on one line")
	}
	if err := ValidateEndpoint(j.URL); err != nil {
		return err
	}
	if len(j.Authorization) > 8192 || strings.ContainsAny(j.Authorization, "\r\n") {
		return errors.New("invalid authorization header")
	}
	var body map[string]json.RawMessage
	if len(j.Body) > 65536 || json.Unmarshal(j.Body, &body) != nil || body == nil {
		return errors.New("body must be a JSON object, up to 64 KiB")
	}
	switch j.Policy {
	case "youtube-full", "youtube-incremental":
		want := `"true"`
		if j.Policy == "youtube-incremental" {
			want = `"false"`
		}
		if len(body) != 1 || string(body["fullSync"]) != want {
			return errors.New("legacy payload must preserve the fullSync string value")
		}
	case "fixtse", "magnaweb":
		if len(body) != 0 {
			return errors.New("legacy request body must remain {}")
		}
	case "generic":
	default:
		return errors.New("invalid alert policy")
	}
	if j.Cron != "" {
		_, err := ParseSchedule(j.Cron, tz)
		return err
	}
	return nil
}

// Legacy persisted jobs retain authenticated requests and configured alert delivery.
// Generic jobs can intentionally target public endpoints without an auth header.
func ValidateRunnable(j domain.Job, telegram domain.TelegramSettings) error {
	if j.Policy != "generic" {
		if strings.TrimSpace(j.Authorization) == "" {
			return errors.New("set the job's authorization header before enabling execution")
		}
		if !telegram.Enabled || telegram.Token == "" || telegram.Chat == "" {
			return errors.New("configure and enable Telegram in settings before running this saved task")
		}
	}
	return nil
}
