package adapters

import (
	"database/sql"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/domain"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Store struct{ DB *sql.DB }

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "runs.sqlite")+"?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	_, err = db.Exec(`
 CREATE TABLE IF NOT EXISTS runs(job TEXT NOT NULL,slot TEXT NOT NULL,status TEXT NOT NULL,
 notification TEXT NOT NULL DEFAULT 'none',started TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,finished TEXT,PRIMARY KEY(job,slot));
 CREATE UNIQUE INDEX IF NOT EXISTS running_job ON runs(job) WHERE status='running';
 CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,name TEXT NOT NULL,url TEXT NOT NULL,authorization TEXT NOT NULL,body TEXT NOT NULL,cron TEXT NOT NULL,enabled INTEGER NOT NULL,policy TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 1);
 CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS account(id INTEGER PRIMARY KEY CHECK(id=1),username TEXT NOT NULL,password BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS sessions(token TEXT PRIMARY KEY,csrf TEXT NOT NULL,expires INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS telegram_settings(id INTEGER PRIMARY KEY CHECK(id=1),token TEXT NOT NULL,chat TEXT NOT NULL,enabled INTEGER NOT NULL,revision INTEGER NOT NULL);
 INSERT OR IGNORE INTO telegram_settings(id,token,chat,enabled,revision) VALUES(1,'','',0,1);
 CREATE TABLE IF NOT EXISTS telegram_tests(token TEXT PRIMARY KEY,revision INTEGER NOT NULL,status TEXT NOT NULL,started INTEGER NOT NULL);
 CREATE UNIQUE INDEX IF NOT EXISTS active_telegram_test ON telegram_tests((1)) WHERE status='running';
 `)
	if err != nil {
		db.Close()
		return nil, err
	}
	// Add per-job settings without changing existing schedules or revisions.
	rows, err := db.Query("PRAGMA table_info(jobs)")
	if err != nil {
		db.Close()
		return nil, err
	}
	hasTimezone, hasTimeout := false, false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			break
		}
		hasTimezone = hasTimezone || name == "timezone"
		hasTimeout = hasTimeout || name == "request_timeout_ms"
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err == nil && !hasTimezone {
		_, err = db.Exec("ALTER TABLE jobs ADD COLUMN timezone TEXT NOT NULL DEFAULT ''")
	}
	if err == nil && !hasTimeout {
		_, err = db.Exec("ALTER TABLE jobs ADD COLUMN request_timeout_ms INTEGER NOT NULL DEFAULT 0")
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	if err = migrateRecovery(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = migrateRunDetails(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = migrateTelegramNotifications(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = migrateAgent(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = migratePasswordResets(db); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Seed(jobs []domain.Job) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRow("SELECT count(*) FROM metadata WHERE key='jobs-seeded'").Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	for _, j := range jobs {
		if _, err = tx.Exec(`INSERT INTO jobs(id,name,url,authorization,body,cron,enabled,policy,timezone,request_timeout_ms,retry_count,retry_delay_ms,retry_uncertain,fallback_url,fallback_authorization) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, j.ID, j.Name, j.URL, j.Authorization, string(j.Body), j.Cron, j.Enabled, j.Policy, j.Timezone, j.TimeoutMS, j.RetryCount, j.RetryDelayMS, j.RetryUncertain, j.FallbackURL, j.FallbackAuth); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("INSERT INTO metadata(key,value) VALUES('jobs-seeded','1')"); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Recover() error {
	_, err := s.DB.Exec("UPDATE agent_tasks SET status='interrupted',last_error='delivery_unconfirmed' WHERE status='delivering'; UPDATE runs SET status='interrupted',finished=CURRENT_TIMESTAMP WHERE status='running'; UPDATE telegram_tests SET status='interrupted' WHERE status='running'")
	return err
}
func (s *Store) Begin(j domain.Job, slot string) (bool, error) {
	result, err := s.DB.Exec(`INSERT OR IGNORE INTO runs(job,slot,status,job_name,job_revision,timeout_ms) SELECT ?,?,'running',?,?,? WHERE EXISTS(SELECT 1 FROM jobs WHERE id=? AND revision=? AND (enabled=1 OR ?))`, j.ID, slot, j.Name, j.Revision, j.TimeoutMS, j.ID, j.Revision, strings.HasPrefix(slot, "manual:"))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (s *Store) Finish(id, slot string, o domain.Outcome) error {
	result, err := s.DB.Exec("UPDATE runs SET status=?,notification=?,http_status=?,error_code=?,duration_ms=?,finished=CURRENT_TIMESTAMP WHERE job=? AND slot=? AND status='running'", o.Status, o.Notification, o.HTTPStatus, o.ErrorCode, o.DurationMS, id, slot)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("run is not active")
	}
	return nil
}
func (s *Store) Health() error {
	var n int
	return s.DB.QueryRow("SELECT count(*) FROM metadata").Scan(&n)
}

const jobColumns = "id,name,url,authorization,body,cron,enabled,policy,revision,timezone,request_timeout_ms,retry_count,retry_delay_ms,retry_uncertain,fallback_url,fallback_authorization"

func scanJob(row interface{ Scan(...any) error }) (domain.Job, error) {
	var j domain.Job
	var body string
	err := row.Scan(&j.ID, &j.Name, &j.URL, &j.Authorization, &body, &j.Cron, &j.Enabled, &j.Policy, &j.Revision, &j.Timezone, &j.TimeoutMS, &j.RetryCount, &j.RetryDelayMS, &j.RetryUncertain, &j.FallbackURL, &j.FallbackAuth)
	j.Body = []byte(body)
	return j, err
}
func (s *Store) Jobs() ([]domain.Job, error) {
	rows, err := s.DB.Query("SELECT " + jobColumns + " FROM jobs ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []domain.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}
func (s *Store) Job(id string) (domain.Job, error) {
	return scanJob(s.DB.QueryRow("SELECT "+jobColumns+" FROM jobs WHERE id=?", id))
}

var ErrConflict = errors.New("job changed in another session; reload before saving")

func (s *Store) Save(j domain.Job, create bool) error {
	if create {
		_, err := s.DB.Exec(`INSERT INTO jobs(id,name,url,authorization,body,cron,enabled,policy,timezone,request_timeout_ms,retry_count,retry_delay_ms,retry_uncertain,fallback_url,fallback_authorization) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, j.ID, j.Name, j.URL, j.Authorization, string(j.Body), j.Cron, j.Enabled, j.Policy, j.Timezone, j.TimeoutMS, j.RetryCount, j.RetryDelayMS, j.RetryUncertain, j.FallbackURL, j.FallbackAuth)
		return err
	}
	result, err := s.DB.Exec(`UPDATE jobs SET name=?,url=?,authorization=?,body=?,cron=?,enabled=?,timezone=?,request_timeout_ms=?,retry_count=?,retry_delay_ms=?,retry_uncertain=?,fallback_url=?,fallback_authorization=?,revision=revision+1 WHERE id=? AND revision=? AND policy=?`, j.Name, j.URL, j.Authorization, string(j.Body), j.Cron, j.Enabled, j.Timezone, j.TimeoutMS, j.RetryCount, j.RetryDelayMS, j.RetryUncertain, j.FallbackURL, j.FallbackAuth, j.ID, j.Revision, j.Policy)
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
func (s *Store) Recent() ([]domain.Run, error) {
	page, err := s.History(1, 30)
	return page.Runs, err
}
func (s *Store) Account() (string, []byte, error) {
	var user string
	var hash []byte
	err := s.DB.QueryRow("SELECT username,password FROM account WHERE id=1").Scan(&user, &hash)
	return user, hash, err
}
func (s *Store) CreateAccount(user string, hash []byte) error {
	_, err := s.DB.Exec("INSERT INTO account(id,username,password) VALUES(1,?,?)", user, hash)
	return err
}
func (s *Store) AddSession(token, csrf string, expires time.Time) error {
	return s.addSession(token, csrf, expires, nil)
}

// Recheck the verified password at insertion so a login already in flight cannot
// create a new session with the old password after a concurrent recovery.
func (s *Store) AddSessionForPassword(token, csrf string, expires time.Time, password []byte) error {
	return s.addSession(token, csrf, expires, &password)
}
func (s *Store) addSession(token, csrf string, expires time.Time, password *[]byte) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM sessions WHERE expires<=?", time.Now().Unix()); err != nil {
		return err
	}
	// Bound persistent sessions, including repeated successful logins.
	if _, err = tx.Exec("DELETE FROM sessions WHERE token IN (SELECT token FROM sessions ORDER BY expires DESC LIMIT -1 OFFSET 19)"); err != nil {
		return err
	}
	if password == nil {
		if _, err = tx.Exec("INSERT INTO sessions(token,csrf,expires) VALUES(?,?,?)", token, csrf, expires.Unix()); err != nil {
			return err
		}
	} else {
		result, err := tx.Exec("INSERT INTO sessions(token,csrf,expires) SELECT ?,?,? WHERE EXISTS(SELECT 1 FROM account WHERE id=1 AND password=?)", token, csrf, expires.Unix(), *password)
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
	}
	return tx.Commit()
}
func (s *Store) Session(token string) (string, error) {
	var csrf string
	err := s.DB.QueryRow("SELECT csrf FROM sessions WHERE token=? AND expires>?", token, time.Now().Unix()).Scan(&csrf)
	return csrf, err
}
func (s *Store) DeleteSession(token string) error {
	_, err := s.DB.Exec("DELETE FROM sessions WHERE token=?", token)
	return err
}
