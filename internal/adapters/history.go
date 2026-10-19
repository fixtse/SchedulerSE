package adapters

import (
	"database/sql"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/domain"
)

type HistoryPage struct {
	Runs               []domain.Run
	Total, Page, Pages int
}

func (s *Store) History(page, size int) (HistoryPage, error) {
	result := HistoryPage{Runs: []domain.Run{}, Page: page}
	if page < 1 || size < 1 || size > 100 {
		return result, errors.New("invalid history page")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = tx.QueryRow("SELECT count(*) FROM runs").Scan(&result.Total); err != nil {
		return result, err
	}
	result.Pages = max(1, (result.Total+size-1)/size)
	result.Page = min(page, result.Pages)
	rows, err := tx.Query(`SELECT job,slot,status,notification,started,COALESCE(finished,''),job_name,job_revision,timeout_ms,http_status,error_code,duration_ms FROM runs ORDER BY started DESC,rowid DESC LIMIT ? OFFSET ?`, size, (result.Page-1)*size)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var run domain.Run
		if err = rows.Scan(&run.Job, &run.Slot, &run.Status, &run.Notification, &run.Started, &run.Finished, &run.JobName, &run.JobRevision, &run.TimeoutMS, &run.HTTPStatus, &run.ErrorCode, &run.DurationMS); err != nil {
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
	} {
		if !columns[column.name] {
			if _, err = db.Exec("ALTER TABLE runs ADD COLUMN " + column.name + " " + column.definition); err != nil {
				return err
			}
		}
	}
	return nil
}
