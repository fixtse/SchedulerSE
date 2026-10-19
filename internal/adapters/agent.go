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
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_settings(id INTEGER PRIMARY KEY CHECK(id=1),token_hash TEXT NOT NULL DEFAULT '',webhook_url TEXT NOT NULL DEFAULT '',webhook_auth TEXT NOT NULL DEFAULT '');
 INSERT OR IGNORE INTO agent_settings(id) VALUES(1);
 CREATE TABLE IF NOT EXISTS agent_tasks(id TEXT PRIMARY KEY,kind TEXT NOT NULL,title TEXT NOT NULL,message TEXT NOT NULL,context TEXT NOT NULL,run_at INTEGER NOT NULL,interval_seconds INTEGER NOT NULL,status TEXT NOT NULL DEFAULT 'pending',http_status INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '');
 CREATE INDEX IF NOT EXISTS agent_due ON agent_tasks(status,run_at);`)
	return err
}
func (s *Store) AgentSettings() (v domain.AgentSettings, err error) {
	err = s.DB.QueryRow(`SELECT token_hash,webhook_url,webhook_auth FROM agent_settings WHERE id=1`).Scan(&v.TokenHash, &v.WebhookURL, &v.WebhookAuth)
	return
}
func (s *Store) SaveAgentWebhook(endpoint, auth string) error {
	_, err := s.DB.Exec(`UPDATE agent_settings SET webhook_url=?,webhook_auth=? WHERE id=1`, endpoint, auth)
	return err
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
	_, err := s.DB.Exec(`INSERT INTO agent_tasks(id,kind,title,message,context,run_at,interval_seconds) VALUES(?,?,?,?,?,?,?)`, v.ID, v.Kind, v.Title, v.Message, string(v.Context), v.RunAt, v.IntervalSeconds)
	return err
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
	if err != nil || settings.WebhookURL == "" {
		return
	}
	v, err := scanAgent(s.DB.QueryRow(`SELECT `+agentColumns+` FROM agent_tasks WHERE status='pending' AND run_at<=? ORDER BY run_at LIMIT 1`, time.Now().Unix()))
	if err != nil {
		return
	}
	result, err := s.DB.Exec(`UPDATE agent_tasks SET status='delivering' WHERE id=? AND status='pending' AND run_at=?`, v.ID, v.RunAt)
	if err != nil {
		return
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return
	}
	body, _ := json.Marshal(map[string]any{"event": "schedule.due", "event_id": v.ID + ":" + time.Unix(v.RunAt, 0).UTC().Format(time.RFC3339), "scheduled_at": time.Unix(v.RunAt, 0).UTC().Format(time.RFC3339), "task": v})
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
