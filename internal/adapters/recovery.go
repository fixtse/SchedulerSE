package adapters

import (
	"database/sql"
	"errors"
)

func migrateRecovery(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(jobs)")
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
		{"kind", "TEXT NOT NULL DEFAULT ''"},
		{"script", "TEXT NOT NULL DEFAULT ''"},
		{"retry_count", "INTEGER NOT NULL DEFAULT 0"},
		{"retry_delay_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"retry_uncertain", "INTEGER NOT NULL DEFAULT 0"},
		{"fallback_url", "TEXT NOT NULL DEFAULT ''"},
		{"fallback_authorization", "TEXT NOT NULL DEFAULT ''"},
	} {
		if !columns[column.name] {
			if _, err = db.Exec("ALTER TABLE jobs ADD COLUMN " + column.name + " " + column.definition); err != nil {
				return err
			}
		}
	}
	return nil
}

// Attempt records intent before sending a POST, including attempts interrupted by a crash.
// Recovery never resumes these requests automatically.
func (s *Store) Attempt(id, slot string, fallback bool) error {
	result, err := s.DB.Exec("UPDATE runs SET attempts=attempts+1,fallback_used=MAX(fallback_used,?) WHERE job=? AND slot=? AND status='running'", fallback, id, slot)
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
