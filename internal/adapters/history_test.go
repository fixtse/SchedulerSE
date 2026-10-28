package adapters

import (
	"fmt"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"testing"
)

func TestHistoryPaginationAndExecutionSnapshots(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	empty, err := s.History(1, 25)
	if err != nil || empty.Total != 0 || empty.Pages != 1 || len(empty.Runs) != 0 {
		t.Fatal("empty history is not a valid page")
	}
	j := domain.Job{ID: "report", Name: "Original report", Authorization: "private-header", TimeoutMS: 5000, Enabled: true, Policy: "generic"}
	if err = s.Save(j, true); err != nil {
		t.Fatal(err)
	}
	j, _ = s.Job(j.ID)
	for i := range 52 {
		slot := fmt.Sprintf("manual:run-%02d", i)
		if ok, err := s.Begin(j, slot); err != nil || !ok {
			t.Fatalf("begin: %v", err)
		}
		if err = s.Finish(j.ID, slot, domain.Outcome{Status: "failed", Notification: "none", HTTPStatus: 503, ErrorCode: "HTTP_ERROR", DurationMS: 1234}); err != nil {
			t.Fatal(err)
		}
	}
	j.Name, j.TimeoutMS = "Edited report", 60000
	if err = s.Save(j, false); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for page := 1; page <= 3; page++ {
		result, err := s.History(page, 25)
		if err != nil || result.Total != 52 || result.Pages != 3 || result.Page != page {
			t.Fatalf("history page %d: %v", page, err)
		}
		want := 25
		if page == 3 {
			want = 2
		}
		if len(result.Runs) != want {
			t.Fatalf("page size: %d", len(result.Runs))
		}
		for _, run := range result.Runs {
			if seen[run.Slot] {
				t.Fatal("history duplicated a run across pages")
			}
			seen[run.Slot] = true
			if run.JobName != "Original report" || run.JobRevision != 1 || run.TimeoutMS != 5000 || run.HTTPStatus != 503 || run.ErrorCode != "HTTP_ERROR" || run.DurationMS != 1234 {
				t.Fatal("execution details lost or replaced by edited task settings")
			}
		}
	}
	if len(seen) != 52 {
		t.Fatal("history omitted older executions")
	}
	last, err := s.History(1000000000, 25)
	if err != nil || last.Page != 3 || len(last.Runs) != 2 {
		t.Fatal("out-of-range page not clamped")
	}
	for _, args := range [][2]int{{0, 25}, {1, 0}, {1, 101}} {
		if _, err := s.History(args[0], args[1]); err == nil {
			t.Fatal("invalid pagination accepted")
		}
	}
}

func TestRecentHistoryCapsBeforeFiltering(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	_, err := s.DB.Exec(`WITH RECURSIVE sequence(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM sequence WHERE n<240)
 INSERT INTO runs(job,slot,status,started)
 SELECT 'report',printf('run-%03d',n),CASE WHEN n<=40 THEN 'interrupted' WHEN n%2=0 THEN 'success' ELSE 'failed' END,'2026-10-28 12:00:00' FROM sequence`)
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.RecentHistory(1, 25, "")
	if err != nil || all.Total != 200 || all.Pages != 8 || len(all.Runs) != 25 || all.Runs[0].Slot != "run-240" {
		t.Fatalf("recent history: %+v, %v", all, err)
	}
	last, err := s.RecentHistory(999, 25, "")
	if err != nil || last.Page != 8 || last.Runs[24].Slot != "run-041" {
		t.Fatalf("recent boundary: %+v, %v", last, err)
	}
	for _, status := range []string{"success", "failed"} {
		seen := map[string]bool{}
		for page := 1; page <= 4; page++ {
			filtered, err := s.RecentHistory(page, 25, status)
			if err != nil || filtered.Total != 100 || filtered.Pages != 4 || len(filtered.Runs) != 25 {
				t.Fatalf("filtered page: %+v, %v", filtered, err)
			}
			for _, run := range filtered.Runs {
				if run.Status != status || seen[run.Slot] {
					t.Fatal("incorrect or duplicate filtered execution", run)
				}
				seen[run.Slot] = true
			}
		}
	}
	empty, err := s.RecentHistory(1, 25, "interrupted")
	if err != nil || empty.Total != 0 || empty.Pages != 1 || len(empty.Runs) != 0 {
		t.Fatalf("filter included entries outside latest 200: %+v, %v", empty, err)
	}
	audit, err := s.History(1, 25)
	if err != nil || audit.Total != 240 {
		t.Fatal("recent activity changed full execution history", audit, err)
	}
}
