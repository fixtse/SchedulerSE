package application_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/application"
	"github.com/fixtse/SchedulerSE/internal/domain"
)

func TestExecutionCapacityAndRelease(t *testing.T) {
	s, err := adapters.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	r := &application.Runner{Store: s, Notifications: &alerts{}, Log: &logs{}, Timeout: time.Second, Sync: syncFunc(func(context.Context, domain.Job) (domain.Response, error) { return domain.Response{}, nil })}
	var jobs []domain.Job
	for i := range 5 {
		j := domain.Job{ID: fmt.Sprintf("capacity-%d", i), Name: "Capacity", Body: []byte(`{}`)}
		if err := s.Save(j, true); err != nil {
			t.Fatal(err)
		}
		j, _ = s.Job(j.ID)
		jobs = append(jobs, j)
	}
	var work []func() (*domain.Outcome, error)
	for i := range 4 {
		fn, err := r.Prepare(context.Background(), jobs[i], "manual:first")
		if err != nil || fn == nil {
			t.Fatal(err)
		}
		work = append(work, fn)
	}
	if fn, err := r.Prepare(context.Background(), jobs[4], "manual:first"); err == nil || fn != nil {
		t.Fatal("unbounded admission")
	}
	if _, err := work[0](); err != nil {
		t.Fatal(err)
	}
	next, err := r.Prepare(context.Background(), jobs[4], "manual:first")
	if err != nil || next == nil {
		t.Fatal("capacity not released", err)
	}
	for _, fn := range work[1:] {
		if _, err := fn(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := next(); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		fn, err := r.Prepare(context.Background(), jobs[0], "manual:first")
		if err != nil || fn != nil {
			t.Fatal("duplicate must release capacity", err)
		}
	}
	fn, err := r.Prepare(context.Background(), jobs[0], "manual:second")
	if err != nil || fn == nil {
		t.Fatal("duplicate leaked capacity", err)
	}
	if _, err := fn(); err != nil {
		t.Fatal(err)
	}
}
