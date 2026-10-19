package adapters

import (
	"encoding/json"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"strings"
	"testing"
)

func TestRecoveryPersistenceAndInterruptedAttempts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.DB.Close() }()
	j := domain.Job{ID: "task", Name: "Task", URL: "http://127.0.0.1/primary", Authorization: "primary-secret", Body: []byte(`{}`), Policy: "generic", Cron: "* * * * *", Enabled: true}
	if err = s.Save(j, true); err != nil {
		t.Fatal(err)
	}
	j, _ = s.Job(j.ID)
	if j.RetryCount != 0 || j.FallbackURL != "" || j.RetryUncertain {
		t.Fatal("recovery enabled without opt-in")
	}
	stale := j
	j.RetryCount, j.RetryDelayMS, j.RetryUncertain = 3, 500, true
	j.FallbackURL, j.FallbackAuth = "http://127.0.0.1/backup", "fallback-secret"
	if err = s.Save(j, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(stale, false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale save overwrote recovery settings")
	}
	if err = s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	j, err = s.Job(j.ID)
	if err != nil || j.RetryCount != 3 || j.RetryDelayMS != 500 || !j.RetryUncertain || j.FallbackURL != "http://127.0.0.1/backup" || j.FallbackAuth != "fallback-secret" || j.Cron != "* * * * *" || j.Revision != 2 {
		t.Fatal("saved recovery configuration lost")
	}
	encoded, err := json.Marshal(j)
	if err != nil || strings.Contains(string(encoded), "secret") {
		t.Fatal("authorization exposed through JSON")
	}
	if ok, err := s.Begin(j, "slot"); err != nil || !ok {
		t.Fatal("could not claim execution")
	}
	if err = s.Attempt(j.ID, "slot", false); err != nil {
		t.Fatal(err)
	}
	if err = s.Attempt(j.ID, "slot", true); err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(); err != nil {
		t.Fatal(err)
	}
	history, err := s.Recent()
	if err != nil || len(history) != 1 || history[0].Status != "interrupted" || history[0].Attempts != 2 || !history[0].FallbackUsed {
		t.Fatal("interrupted attempts lost or resumed")
	}
	if err = s.Attempt(j.ID, "slot", true); err == nil {
		t.Fatal("sent an attempt without an active execution")
	}
	if ok, err := s.Begin(j, "slot"); err != nil || ok {
		t.Fatal("interrupted slot was replayed")
	}
}

func TestRecoveryValidation(t *testing.T) {
	t.Parallel()
	j := domain.Job{ID: "task", Name: "Task", URL: "http://127.0.0.1/primary", Body: []byte(`{}`), Policy: "generic"}
	for _, change := range []func(*domain.Job){
		func(j *domain.Job) { j.RetryCount = -1 },
		func(j *domain.Job) { j.RetryCount = 6 },
		func(j *domain.Job) { j.RetryDelayMS = -1 },
		func(j *domain.Job) { j.RetryDelayMS = 99 },
		func(j *domain.Job) { j.RetryDelayMS = 60001 },
		func(j *domain.Job) { j.FallbackURL = "http://public.example/backup" },
		func(j *domain.Job) { j.FallbackURL = "https://user:secret@example.com" },
		func(j *domain.Job) { j.FallbackAuth = "Bearer secret\r\ninjected: header" },
	} {
		invalid := j
		change(&invalid)
		if ValidateJob(invalid, "UTC") == nil {
			t.Fatal("invalid recovery configuration accepted")
		}
	}
	j.RetryCount, j.RetryDelayMS = 5, 60000
	j.FallbackURL = "https://example.com/backup"
	if err := ValidateJob(j, "UTC"); err != nil {
		t.Fatal(err)
	}
}
