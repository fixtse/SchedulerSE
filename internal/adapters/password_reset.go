package adapters

import (
	"database/sql"
	"time"
)

// Password reset receipts retain only token hashes. Used receipts remain briefly
// to keep Telegram rate limits durable across resets and service restarts.
func migratePasswordResets(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS password_resets(
 token_hash TEXT PRIMARY KEY,account_password BLOB NOT NULL,telegram_revision INTEGER NOT NULL,
 issued INTEGER NOT NULL,expires INTEGER NOT NULL,used INTEGER NOT NULL DEFAULT 0);
 CREATE INDEX IF NOT EXISTS password_reset_issued ON password_resets(issued);`)
	return err
}

func (s *Store) IssuePasswordReset(hash string, password []byte, telegramRevision int64) (bool, error) {
	now := time.Now()
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM password_resets WHERE issued<?`, now.Add(-24*time.Hour).Unix()); err != nil {
		return false, err
	}
	result, err := tx.Exec(`INSERT OR IGNORE INTO password_resets(token_hash,account_password,telegram_revision,issued,expires)
 SELECT ?,?,?,?,? WHERE EXISTS(SELECT 1 FROM account WHERE id=1 AND password=?)
 AND EXISTS(SELECT 1 FROM telegram_settings WHERE id=1 AND revision=? AND token<>'' AND chat<>'')
 AND NOT EXISTS(SELECT 1 FROM password_resets WHERE issued>?)
 AND (SELECT count(*) FROM password_resets WHERE issued>?)<5`, hash, password, telegramRevision, now.Unix(), now.Add(10*time.Minute).Unix(), password, telegramRevision, now.Add(-time.Minute).Unix(), now.Add(-time.Hour).Unix())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *Store) ValidPasswordReset(hash string) (bool, error) {
	var valid bool
	err := s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM password_resets r
 JOIN account a ON a.id=1 AND a.password=r.account_password
 JOIN telegram_settings t ON t.id=1 AND t.revision=r.telegram_revision
 WHERE r.token_hash=? AND r.used=0 AND r.expires>?)`, hash, time.Now().Unix()).Scan(&valid)
	return valid, err
}

// ResetPassword rechecks and consumes the token atomically with the password
// change and session invalidation. Concurrent reuse can never win twice.
func (s *Store) ResetPassword(hash string, password []byte) (bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE account SET password=? WHERE id=1 AND EXISTS(
 SELECT 1 FROM password_resets r JOIN telegram_settings t ON t.id=1 AND t.revision=r.telegram_revision
 WHERE r.token_hash=? AND r.used=0 AND r.expires>? AND r.account_password=account.password)`, password, hash, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, nil
	}
	if _, err = tx.Exec(`UPDATE password_resets SET used=1,account_password=X''; DELETE FROM sessions`); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
