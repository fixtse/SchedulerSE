package adapters

import (
	"database/sql"
	"errors"
	"time"
)

var ErrPairingConflict = errors.New("agent delivery is already in use; existing credentials, schedules and delivery were preserved")

// Pairing stores only digests of the two client-generated credentials. The
// callback authorization has the same private storage boundary as agent_settings.
type AgentPairing struct {
	ID, SecretHash, TokenHash, CallbackURL, CallbackAuth, State string
	ExpiresAt                                                   int64
}

func migratePairing(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_pairings (
 id TEXT PRIMARY KEY, secret_hash TEXT NOT NULL, token_hash TEXT NOT NULL,
 callback_url TEXT NOT NULL, callback_auth TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending', expires_at INTEGER NOT NULL)`)
	return err
}
func (s *Store) CreateAgentPairing(p AgentPairing) error {
	// A bounded queue limits unauthenticated registrations, including expired rows.
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM agent_pairings WHERE state != 'approved' AND expires_at <= ?`, time.Now().Unix()); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM agent_pairings`).Scan(&count); err != nil {
		return err
	}
	if count >= 64 {
		return ErrCapacity
	}
	if _, err = tx.Exec(`INSERT INTO agent_pairings(id,secret_hash,token_hash,callback_url,callback_auth,expires_at) VALUES(?,?,?,?,?,?)`, p.ID, p.SecretHash, p.TokenHash, p.CallbackURL, p.CallbackAuth, p.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) AgentPairing(id string) (p AgentPairing, err error) {
	err = s.DB.QueryRow(`SELECT id,secret_hash,token_hash,callback_url,callback_auth,state,expires_at FROM agent_pairings WHERE id=?`, id).Scan(&p.ID, &p.SecretHash, &p.TokenHash, &p.CallbackURL, &p.CallbackAuth, &p.State, &p.ExpiresAt)
	return
}
func (s *Store) ApproveAgentPairing(id string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var endpoint, auth, state string
	var expires int64
	if err = tx.QueryRow(`SELECT callback_url,callback_auth,state,expires_at FROM agent_pairings WHERE id=?`, id).Scan(&endpoint, &auth, &state, &expires); err != nil {
		return err
	}
	if state != "pending" || expires <= time.Now().Unix() {
		return errors.New("pairing expired or already handled")
	}
	var mode, oldURL, oldAuth, oldToken string
	if err = tx.QueryRow(`SELECT delivery_mode,webhook_url,webhook_auth,token_hash FROM agent_settings WHERE id=1`).Scan(&mode, &oldURL, &oldAuth, &oldToken); err != nil {
		return err
	}
	if mode != "webhook" || oldURL != endpoint || oldAuth != auth {
		var active int
		if err = tx.QueryRow(`SELECT count(*) FROM agent_tasks WHERE status IN ('pending','delivering')`).Scan(&active); err != nil {
			return err
		}
		if oldURL != "" || oldAuth != "" || oldToken != "" || active != 0 {
			return ErrPairingConflict
		}
		if _, err = tx.Exec(`UPDATE agent_settings SET delivery_mode='webhook',webhook_url=?,webhook_auth=? WHERE id=1`, endpoint, auth); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE agent_pairings SET state='approved' WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RevokeAgentPairing(id string) error {
	// Also discards the callback credential for denied/revoked requests. Delivery
	// and schedules remain untouched; revocation only removes API access.
	_, err := s.DB.Exec(`UPDATE agent_pairings SET state='revoked',callback_auth='',token_hash='' WHERE id=?`, id)
	return err
}
func (s *Store) PairedAgentToken(hash string) (bool, error) {
	var count int
	err := s.DB.QueryRow(`SELECT count(*) FROM agent_pairings p JOIN agent_settings s ON s.id=1 WHERE p.state='approved' AND p.token_hash=? AND s.delivery_mode='webhook' AND p.callback_url=s.webhook_url AND p.callback_auth=s.webhook_auth`, hash).Scan(&count)
	return count > 0, err
}

// AgentPairingLinks exposes no credential material to HTML views.
func (s *Store) AgentPairingLinks() ([]string, error) {
	rows, err := s.DB.Query(`SELECT id FROM agent_pairings WHERE state='approved' ORDER BY id LIMIT 64`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
