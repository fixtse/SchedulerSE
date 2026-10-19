package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/adapters/web"
	"github.com/fixtse/SchedulerSE/internal/application"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type logger struct{}

func (logger) Event(event string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	details["event"] = event
	details["time"] = time.Now().UTC().Format(time.RFC3339)
	_ = json.NewEncoder(os.Stdout).Encode(details)
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func healthcheck() error {
	address := env("LISTEN_ADDR", ":"+env("HEALTH_PORT", "8789"))
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("unhealthy")
	}
	return nil
}
func main() {
	syscall.Umask(0077)
	if err := run(); err != nil {
		logger{}.Event("command_failed", map[string]any{"message": "Check command, configuration and writable state directory; details withheld to protect credentials"})
		os.Exit(1)
	}
}
func run() error {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "healthcheck" {
		return healthcheck()
	}
	if command == "setup-token" {
		token, err := os.ReadFile(filepath.Join(env("DATA_DIR", "./data"), "setup-token"))
		if err != nil {
			return err
		}
		fmt.Println(strings.TrimSpace(string(token)))
		return nil
	}
	if command != "serve" && command != "run" && command != "plan" && command != "dry-run" {
		return errors.New("use serve, run <job>, plan, dry-run [job], healthcheck, or setup-token")
	}
	c, err := adapters.LoadConfig()
	if err != nil {
		return err
	}
	if command == "plan" || command == "dry-run" {
		return preview(c)
	}
	if err = os.MkdirAll(c.DataDir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(c.DataDir, "runner.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("state volume already locked")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	store, err := adapters.OpenStore(c.DataDir)
	if err != nil {
		return err
	}
	defer store.DB.Close()
	if err = store.ImportTelegram(c.TelegramToken, c.TelegramChat); err != nil {
		return err
	}
	if err = store.Recover(); err != nil {
		return err
	}
	jobs, err := store.Jobs()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if err = adapters.ValidateJob(j, c.Timezone); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	transport := adapters.NewHTTP(c)
	notifications := &adapters.Telegram{Store: store, HTTP: transport}
	runner := &application.Runner{Sync: transport, Notifications: notifications, Store: store, Log: logger{}, Timeout: c.Timeout}
	if command == "run" {
		if len(os.Args) != 3 {
			return errors.New("specify a job ID")
		}
		j, err := store.Job(os.Args[2])
		if err != nil {
			return err
		}
		telegram, settingsErr := store.Telegram()
		if settingsErr != nil {
			return settingsErr
		}
		if err = adapters.ValidateRunnable(j, telegram); err != nil {
			return err
		}
		outcome, err := runner.Run(ctx, j, "manual:"+rand.Text())
		if err != nil {
			return err
		}
		if outcome == nil || outcome.Status == "failed" || outcome.Status == "interrupted" || outcome.Notification == "failed" {
			return errors.New("job skipped or failed")
		}
		return nil
	}
	token := ""
	_, _, accountErr := store.Account()
	if errors.Is(accountErr, sql.ErrNoRows) {
		tokenPath := filepath.Join(c.DataDir, "setup-token")
		data, readErr := os.ReadFile(tokenPath)
		if readErr == nil {
			token = strings.TrimSpace(string(data))
		} else if !os.IsNotExist(readErr) {
			return readErr
		}
		if token == "" {
			token = rand.Text()
			if err = os.WriteFile(tokenPath, []byte(token+"\n"), 0600); err != nil {
				return err
			}
		}
		logger{}.Event("setup_required", map[string]any{"message": "Open the management interface and retrieve the setup token with: docker compose exec schedulerse /app/schedulerse setup-token"})
	} else if accountErr != nil {
		return accountErr
	} else {
		_ = os.Remove(filepath.Join(c.DataDir, "setup-token"))
	}
	scheduler := adapters.NewScheduler(store, runner, c.Timezone, logger{})
	handler := web.New(store, c, scheduler.Reload, token)
	handler.Runner = runner
	handler.Context = ctx
	handler.Telegram = notifications
	server := &http.Server{Addr: c.Address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, err := net.Listen("tcp", c.Address)
	if err != nil {
		return err
	}
	agentDone := make(chan struct{})
	go func() { defer close(agentDone); store.ServeAgent(ctx, transport) }()
	scheduledDone := make(chan struct{})
	go func() { defer close(scheduledDone); scheduler.Serve(ctx) }()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(listener) }()
	logger{}.Event("started", map[string]any{"schedulerEnabled": true, "timezone": c.Timezone})
	select {
	case <-ctx.Done():
	case err = <-serverErrors:
		stop()
	}
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		_ = server.Close()
	}
	handler.Wait()
	<-agentDone
	select {
	case <-scheduledDone:
	case <-shutdownCtx.Done():
		return errors.New("shutdown deadline exceeded")
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
func preview(c adapters.Config) error {
	jobs := []domain.Job{}
	dbPath := filepath.Join(c.DataDir, "runs.sqlite")
	if _, err := os.Stat(dbPath); err == nil {
		db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
		if err != nil {
			return err
		}
		defer db.Close()
		var seeded int
		err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='jobs'").Scan(&seeded)
		if err != nil {
			return err
		}
		if seeded != 0 {
			jobs, err = (&adapters.Store{DB: db}).Jobs()
			if err != nil {
				return err
			}
		}
	}
	type planJob struct {
		ID, URL, Method  string
		Body             json.RawMessage
		Cron             string
		Timezone         string
		Enabled          bool
		RequestTimeoutMS int
		Next             []string
	}
	plan := struct {
		Timezone         string
		SchedulerEnabled bool
		Jobs             []planJob
	}{Timezone: c.Timezone, SchedulerEnabled: true, Jobs: []planJob{}}
	for _, j := range jobs {
		if len(os.Args) > 2 && j.ID != os.Args[2] {
			continue
		}
		u, err := neturl(j.URL)
		if err != nil {
			return err
		}
		p := planJob{ID: j.ID, URL: u, Method: "POST", Body: j.Body, Cron: j.Cron, Timezone: adapters.JobTimezone(j, c.Timezone), Enabled: j.Enabled, RequestTimeoutMS: int(c.Timeout / time.Millisecond), Next: []string{}}
		if j.TimeoutMS > 0 {
			p.RequestTimeoutMS = j.TimeoutMS
		}
		if j.Cron != "" {
			schedule, err := adapters.ParseSchedule(j.Cron, adapters.JobTimezone(j, c.Timezone))
			if err != nil {
				return err
			}
			now := time.Now()
			for range 3 {
				now = schedule.Next(now)
				p.Next = append(p.Next, now.UTC().Format(time.RFC3339))
			}
		}
		plan.Jobs = append(plan.Jobs, p)
	}
	if len(os.Args) > 2 && len(plan.Jobs) == 0 {
		return errors.New("unknown job")
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(plan)
}
func neturl(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.RawQuery = ""
	return u.String(), nil
}
