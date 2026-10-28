package adapters

import (
	"database/sql"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/domain"
)

type HistoryPage struct {
	Runs  []domain.Run `json:"runs"`
	Total int          `json:"total"`
	Page  int          `json:"page"`
	Pages int          `json:"pages"`
}

func (s *Store) History(page, size int) (HistoryPage, error) {
	return s.history(page, size, "", false)
}

// RecentHistory filters within the latest 200 executions without removing audit
// records used by execution deduplication and the agent API.
func (s *Store) RecentHistory(page, size int, status string) (HistoryPage, error) {
	return s.history(page, size, status, true)
}

func (s *Store) history(page, size int, status string, recent bool) (HistoryPage, error) {
	result := HistoryPage{Runs: []domain.Run{}, Page: page}
	if page < 1 || size < 1 || size > 100 {
		return result, errors.New("invalid history page")
	}
	source := "runs"
	prefix := ""
	where := ""
	args := []any{}
	if recent {
		prefix = `WITH recent_runs AS (SELECT rowid AS run_order,* FROM runs ORDER BY started DESC,rowid DESC LIMIT 200) `
		source = "recent_runs"
	}
	if status != "" {
		where = " WHERE status=?"
		args = append(args, status)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = tx.QueryRow(prefix+"SELECT count(*) FROM "+source+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	result.Pages = max(1, (result.Total+size-1)/size)
	result.Page = min(page, result.Pages)
	order := "rowid"
	if recent {
		order = "run_order"
	}
	args = append(args, size, (result.Page-1)*size)
	rows, err := tx.Query(prefix+`SELECT job,slot,status,notification,started,COALESCE(finished,''),job_name,job_revision,timeout_ms,http_status,error_code,duration_ms,attempts,fallback_used FROM `+source+where+` ORDER BY started DESC,`+order+` DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var run domain.Run
		if err = rows.Scan(&run.Job, &run.Slot, &run.Status, &run.Notification, &run.Started, &run.Finished, &run.JobName, &run.JobRevision, &run.TimeoutMS, &run.HTTPStatus, &run.ErrorCode, &run.DurationMS, &run.Attempts, &run.FallbackUsed); err != nil {
			break
		}
		result.Runs = append(result.Runs, run)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func migrateRunDetails(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(runs)")
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
	for _, column := range []struct{ name, definition string }{
		{"job_name", "TEXT NOT NULL DEFAULT ''"},
		{"job_revision", "INTEGER NOT NULL DEFAULT 0"},
		{"timeout_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"http_status", "INTEGER NOT NULL DEFAULT 0"},
		{"error_code", "TEXT NOT NULL DEFAULT ''"},
		{"duration_ms", "INTEGER NOT NULL DEFAULT -1"},
		{"attempts", "INTEGER NOT NULL DEFAULT 0"},
		{"fallback_used", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if !columns[column.name] {
			if _, err = db.Exec("ALTER TABLE runs ADD COLUMN " + column.name + " " + column.definition); err != nil {
				return err
			}
		}
	}
	return nil
}
