package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/fixtse/SchedulerSE/internal/domain"
)

func migrateAgent(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_settings(id INTEGER PRIMARY KEY CHECK(id=1),token_hash TEXT NOT NULL DEFAULT '',webhook_url TEXT NOT NULL DEFAULT '',webhook_auth TEXT NOT NULL DEFAULT '',delivery_mode TEXT NOT NULL DEFAULT 'poll');
 INSERT OR IGNORE INTO agent_settings(id) VALUES(1);
 CREATE TABLE IF NOT EXISTS agent_tasks(id TEXT PRIMARY KEY,kind TEXT NOT NULL,title TEXT NOT NULL,message TEXT NOT NULL,context TEXT NOT NULL,run_at INTEGER NOT NULL,interval_seconds INTEGER NOT NULL,status TEXT NOT NULL DEFAULT 'pending',http_status INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '');
 CREATE INDEX IF NOT EXISTS agent_due ON agent_tasks(status,run_at);
 CREATE TABLE IF NOT EXISTS agent_pickups(task_id TEXT NOT NULL,run_at INTEGER NOT NULL,consumer_id TEXT NOT NULL,PRIMARY KEY(task_id,run_at));`)
	if err != nil {
		return err
	}
	var hasMode int
	if err = db.QueryRow("SELECT count(*) FROM pragma_table_info('agent_settings') WHERE name='delivery_mode'").Scan(&hasMode); err != nil {
		return err
	}
	if hasMode == 0 {
		_, err = db.Exec("ALTER TABLE agent_settings ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT 'webhook'")
	}
	if err != nil {
		return err
	}
	return migratePairing(db)
}
func (s *Store) AgentSettings() (v domain.AgentSettings, err error) {
	err = s.DB.QueryRow(`SELECT token_hash,webhook_url,webhook_auth,delivery_mode FROM agent_settings WHERE id=1`).Scan(&v.TokenHash, &v.WebhookURL, &v.WebhookAuth, &v.DeliveryMode)
	return
}
func (s *Store) SaveAgentWebhook(endpoint, auth string) error {
	return s.SaveAgentConnection("webhook", endpoint, auth)
}
func (s *Store) SaveAgentConnection(mode, endpoint, auth string) error {
	if mode != "poll" && mode != "webhook" {
		return errors.New("invalid delivery mode")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldURL, oldAuth string
	if err = tx.QueryRow("SELECT webhook_url,webhook_auth FROM agent_settings WHERE id=1").Scan(&oldURL, &oldAuth); err != nil {
		return err
	}
	if err = ValidateCredentialDestination(oldURL, endpoint, oldAuth, auth); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE agent_settings SET delivery_mode=?,webhook_url=?,webhook_auth=? WHERE id=1`, mode, endpoint, auth); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) SetAgentToken(hash string) error {
	_, err := s.DB.Exec(`UPDATE agent_settings SET token_hash=? WHERE id=1`, hash)
	return err
}

const agentColumns = `id,kind,title,message,context,run_at,interval_seconds,status,http_status,last_error`

func scanAgent(row interface{ Scan(...any) error }) (v domain.AgentTask, err error) {
	var raw string
	err = row.Scan(&v.ID, &v.Kind, &v.Title, &v.Message, &raw, &v.RunAt, &v.IntervalSeconds, &v.Status, &v.HTTPStatus, &v.LastError)
	v.Context = json.RawMessage(raw)
	return
}
func (s *Store) AgentTasks() ([]domain.AgentTask, error) {
	rows, err := s.DB.Query(`SELECT ` + agentColumns + ` FROM agent_tasks ORDER BY run_at DESC,id LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AgentTask{}
	for rows.Next() {
		v, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) AgentTask(id string) (domain.AgentTask, error) {
	return scanAgent(s.DB.QueryRow(`SELECT `+agentColumns+` FROM agent_tasks WHERE id=?`, id))
}
func (s *Store) CreateAgentTask(v domain.AgentTask) error {
	result, err := s.DB.Exec(`INSERT INTO agent_tasks(id,kind,title,message,context,run_at,interval_seconds) SELECT ?,?,?,?,?,?,? WHERE (SELECT count(*) FROM agent_tasks)<1000`, v.ID, v.Kind, v.Title, v.Message, string(v.Context), v.RunAt, v.IntervalSeconds)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrCapacity
	}
	return nil
}
func (s *Store) CancelAgentTask(id string) (bool, error) {
	result, err := s.DB.Exec(`UPDATE agent_tasks SET status='cancelled' WHERE id=? AND status='pending'`, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// ServeAgent delivers one callback per occurrence. A durable claim precedes network I/O;
// uncertain deliveries are never replayed after a crash or transport failure.
func (s *Store) ServeAgent(ctx context.Context, h *HTTP) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.deliverAgent(ctx, h)
	}
}

func (s *Store) deliverAgent(ctx context.Context, h *HTTP) {
	settings, err := s.AgentSettings()
	if err != nil || settings.DeliveryMode != "webhook" || settings.WebhookURL == "" {
		return
	}
	v, err := scanAgent(s.DB.QueryRow(`SELECT `+agentColumns+` FROM agent_tasks WHERE status='pending' AND run_at<=? ORDER BY run_at LIMIT 1`, time.Now().Unix()))
	if err != nil {
		return
	}
	result, err := s.DB.Exec(`UPDATE agent_tasks SET status='delivering' WHERE id=? AND status='pending' AND run_at=? AND (SELECT delivery_mode FROM agent_settings WHERE id=1)='webhook'`, v.ID, v.RunAt)
	if err != nil {
		return
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return
	}
	body, _ := json.Marshal(v.Event())
	deliveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	response, sendErr := h.Post(deliveryCtx, domain.Job{URL: settings.WebhookURL, Authorization: settings.WebhookAuth, Body: body, TimeoutMS: 10000})
	cancel()
	status, code := "delivered", ""
	if sendErr != nil {
		status, code = "failed", "delivery_unconfirmed"
		var failure *domain.Failure
		if errors.As(sendErr, &failure) {
			code = failure.Code
		}
	}
	next := v.RunAt
	if sendErr == nil && v.IntervalSeconds > 0 {
		status = "pending"
		next = v.RunAt + ((time.Now().Unix()-v.RunAt)/v.IntervalSeconds+1)*v.IntervalSeconds
	}
	// If this write fails, leave the durable claim for startup recovery.
	_, _ = s.DB.Exec(`UPDATE agent_tasks SET status=?,run_at=?,http_status=?,last_error=? WHERE id=? AND status='delivering'`, status, next, response.HTTPStatus, code, v.ID)
}

func (s *Store) AgentInbox(conversation string) ([]domain.AgentEvent, error) {
	// Filter in SQL so another conversation cannot starve this consumer's first page.
	rows, err := s.DB.Query(`SELECT `+agentColumns+` FROM agent_tasks WHERE status='pending' AND run_at<=? AND json_extract(context,'$.conversation_id')=? ORDER BY run_at,id LIMIT 10`, time.Now().Unix(), conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []domain.AgentEvent{}
	for rows.Next() {
		task, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, task.Event())
	}
	return events, rows.Err()
}

// PickupAgentTask assigns an occurrence to one durable consumer. Retrying the same
// consumer's acknowledgement is safe even after the task has advanced or settings change.
func (s *Store) PickupAgentTask(id string, at int64, consumer string) (bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var owner string
	err = tx.QueryRow(`SELECT consumer_id FROM agent_pickups WHERE task_id=? AND run_at=?`, id, at).Scan(&owner)
	if err == nil {
		return owner == consumer, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var mode string
	if err = tx.QueryRow(`SELECT delivery_mode FROM agent_settings WHERE id=1`).Scan(&mode); err != nil {
		return false, err
	}
	if mode != "poll" {
		return false, nil
	}
	task, err := scanAgent(tx.QueryRow(`SELECT `+agentColumns+` FROM agent_tasks WHERE id=? AND run_at=? AND run_at<=? AND status='pending'`, id, at, time.Now().Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	status, next := "delivered", task.RunAt
	if task.IntervalSeconds > 0 {
		status = "pending"
		next = task.RunAt + ((time.Now().Unix()-task.RunAt)/task.IntervalSeconds+1)*task.IntervalSeconds
	}
	var receipts int
	if err = tx.QueryRow("SELECT count(*) FROM agent_pickups").Scan(&receipts); err != nil {
		return false, err
	}
	if receipts >= 100000 {
		return false, ErrCapacity
	}
	if _, err = tx.Exec(`INSERT INTO agent_pickups(task_id,run_at,consumer_id) VALUES(?,?,?)`, id, at, consumer); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE agent_tasks SET status=?,run_at=?,http_status=0,last_error='' WHERE id=?`, status, next, id); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
