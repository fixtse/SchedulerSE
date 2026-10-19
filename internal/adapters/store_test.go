package adapters

import (
	"database/sql"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s
}
func TestDurableDedupConcurrencyAndStaleEdits(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	j := domain.Job{ID: "one", Name: "One", URL: "https://example.com", Body: []byte(`{}`), Cron: "* * * * *", Enabled: true, Policy: "generic"}
	k := j
	k.ID = "two"
	if err := s.Seed([]domain.Job{j, k}); err != nil {
		t.Fatal(err)
	}
	j, _ = s.Job("one")
	k, _ = s.Job("two")
	if ok, err := s.Begin(j, "slot"); err != nil || !ok {
		t.Fatalf("begin: %v %v", ok, err)
	}
	for _, slot := range []string{"slot", "other"} {
		if ok, _ := s.Begin(j, slot); ok {
			t.Fatal("overlap allowed")
		}
	}
	if ok, _ := s.Begin(k, "slot"); !ok {
		t.Fatal("independent job blocked")
	}
	if err := s.Finish(k.ID, "slot", domain.Outcome{Status: "success", Notification: "none"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Begin(j, "slot"); ok {
		t.Fatal("interrupted slot replayed")
	}
	var status string
	_ = s.DB.QueryRow("SELECT status FROM runs WHERE job='one' AND slot='slot'").Scan(&status)
	if status != "interrupted" {
		t.Fatal(status)
	}
	stale := j
	j.Enabled = false
	if err := s.Save(j, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Begin(stale, "new"); ok {
		t.Fatal("stale schedule ran after disable")
	}
	if err := s.Save(stale, false); err != ErrConflict {
		t.Fatal("stale edit accepted")
	}
	if err := s.Seed([]domain.Job{{ID: "unexpected"}}); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.Jobs()
	if len(jobs) != 2 {
		t.Fatal("environment reseeded jobs")
	}
}
func TestOriginalJournalMigration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", dir+"/runs.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE runs(job TEXT NOT NULL,slot TEXT NOT NULL,status TEXT NOT NULL,notification TEXT NOT NULL DEFAULT 'none',started TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,finished TEXT,PRIMARY KEY(job,slot)); INSERT INTO runs(job,slot,status) VALUES('fixtse','existing','success')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err = s.Seed([]domain.Job{{ID: "fixtse", Name: "FixtSE", URL: "https://example.com", Body: []byte(`{}`), Enabled: true, Policy: "fixtse"}}); err != nil {
		t.Fatal(err)
	}
	j, _ := s.Job("fixtse")
	if ok, _ := s.Begin(j, "existing"); ok {
		t.Fatal("original journal dedup lost")
	}
	runs, err := s.Recent()
	if err != nil || len(runs) != 1 || runs[0].Status != "success" {
		t.Fatalf("migration: %#v %v", runs, err)
	}
}

func TestManualDisabledRunsRetainDedupAndRevisionChecks(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	j := domain.Job{ID: "manual", Name: "Manual", URL: "http://127.0.0.1/mock", Body: []byte(`{}`), Timezone: "America/Lima", Policy: "generic"}
	if err := s.Save(j, true); err != nil {
		t.Fatal(err)
	}
	j, _ = s.Job(j.ID)
	if j.Timezone != "America/Lima" {
		t.Fatal("timezone not persisted")
	}
	if ok, err := s.Begin(j, "scheduled"); err != nil || ok {
		t.Fatal("disabled schedule executed")
	}
	if ok, err := s.Begin(j, "manual:one"); err != nil || !ok {
		t.Fatalf("manual disabled execution rejected: %v", err)
	}
	if ok, _ := s.Begin(j, "manual:two"); ok {
		t.Fatal("manual overlap allowed")
	}
	if err := s.Finish(j.ID, "manual:one", domain.Outcome{Status: "success", Notification: "none"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Begin(j, "manual:one"); ok {
		t.Fatal("manual form replay allowed")
	}
	stale := j
	j.Timezone = "UTC"
	if err := s.Save(j, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Begin(stale, "manual:three"); ok {
		t.Fatal("stale manual revision executed")
	}
}

func TestExistingJobsTimezoneMigration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", dir+"/runs.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE jobs(id TEXT PRIMARY KEY,name TEXT NOT NULL,url TEXT NOT NULL,authorization TEXT NOT NULL,body TEXT NOT NULL,cron TEXT NOT NULL,enabled INTEGER NOT NULL,policy TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 1); INSERT INTO jobs VALUES('old','Old','http://127.0.0.1/mock','private','{}','0 0 * * *',1,'generic',4)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		j, err := s.Job("old")
		s.DB.Close()
		if err != nil || j.Timezone != "" || j.Revision != 4 || j.TimeoutMS != 0 || j.Cron != "0 0 * * *" || j.Authorization != "private" {
			t.Fatalf("migration changed saved job: %v", err)
		}
	}
}

func TestUpgradeKeepsSavedTasksWithoutSeeding(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Jobs()
	if err != nil || len(jobs) != 0 {
		t.Fatal("fresh installation contains tasks")
	}
	legacy := domain.Job{ID: "saved-task", Name: "Saved task", URL: "http://127.0.0.1/mock", Authorization: "Bearer test-only", Body: []byte(`{"fullSync":"false"}`), Enabled: true, Policy: "youtube-incremental", Timezone: "Europe/Madrid"}
	if err = s.Save(legacy, true); err != nil {
		t.Fatal(err)
	}
	legacy, _ = s.Job(legacy.ID)
	if err = s.CreateAccount("owner", []byte("test-only")); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Begin(legacy, "manual:test-only"); err != nil || !ok {
		t.Fatalf("record run: %v", err)
	}
	if err = s.Finish(legacy.ID, "manual:test-only", domain.Outcome{Status: "success", Notification: "none"}); err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err = s.Recover(); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Jobs()
	if err != nil || len(jobs) != 1 {
		t.Fatal("saved task disappeared on upgrade")
	}
	got := jobs[0]
	if got.ID != legacy.ID || got.Name != legacy.Name || got.URL != legacy.URL || got.Authorization != legacy.Authorization || string(got.Body) != string(legacy.Body) || got.Cron != legacy.Cron || got.Enabled != legacy.Enabled || got.Policy != legacy.Policy || got.Timezone != legacy.Timezone || got.Revision != legacy.Revision {
		t.Fatal("upgrade modified saved task")
	}
	user, _, err := s.Account()
	if err != nil || user != "owner" {
		t.Fatal("account lost on upgrade")
	}
	runs, err := s.Recent()
	if err != nil || len(runs) != 1 || runs[0].Status != "success" {
		t.Fatal("execution history lost on upgrade")
	}
}

func TestRequestTimeoutPersistenceAndReset(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	j := domain.Job{ID: "timeout", Name: "Timeout", URL: "http://127.0.0.1/mock", Body: []byte(`{}`), Policy: "generic", TimeoutMS: 300000}
	if err = s.Save(j, true); err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	j, err = s.Job(j.ID)
	if err != nil || j.TimeoutMS != 300000 {
		t.Fatal("task timeout not persisted")
	}
	stale := j
	j.TimeoutMS = 0
	if err = s.Save(j, false); err != nil {
		t.Fatal(err)
	}
	stale.TimeoutMS = 600000
	if err = s.Save(stale, false); err != ErrConflict {
		t.Fatal("stale timeout edit accepted")
	}
	j, _ = s.Job(j.ID)
	if j.TimeoutMS != 0 {
		t.Fatal("task timeout did not reset to inherited default")
	}
}
