package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MaxWebhooks bounds the webhooks one node delivers.
const MaxWebhooks = 32

// ErrWebhookLimit is returned when a node already has MaxWebhooks webhooks.
var ErrWebhookLimit = errors.New("storage: webhook limit reached")

// Webhook is an HTTP endpoint that receives registry and membership events.
// It belongs to the node it was created on, which is the only node that
// delivers it, so each event is sent once. Secret signs every delivery and is
// returned only when the webhook is created.
type Webhook struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Secret      string    `json:"-"`
	Kinds       []string  `json:"kinds,omitempty"`
	Service     string    `json:"service,omitempty"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Webhooks returns the node's webhooks, oldest first.
func (s *Store) Webhooks(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,url,secret,kinds,service,description,created_at FROM webhooks ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		var hook Webhook
		var kinds string
		var created int64
		if err := rows.Scan(&hook.ID, &hook.URL, &hook.Secret, &kinds, &hook.Service, &hook.Description, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(kinds), &hook.Kinds); err != nil {
			return nil, fmt.Errorf("storage: decode webhook kinds: %w", err)
		}
		hook.CreatedAt = time.Unix(0, created).UTC()
		out = append(out, hook)
	}
	return out, rows.Err()
}

// CreateWebhookWithAudit stores a webhook and its audit entry atomically.
func (s *Store) CreateWebhookWithAudit(ctx context.Context, hook Webhook, audit *AuditEntry) error {
	if hook.ID == "" || hook.URL == "" || hook.Secret == "" || hook.CreatedAt.IsZero() {
		return errors.New("storage: webhook requires an ID, URL, secret and creation time")
	}
	kinds, err := json.Marshal(hook.Kinds)
	if err != nil {
		return fmt.Errorf("storage: encode webhook kinds: %w", err)
	}
	if hook.Kinds == nil {
		kinds = []byte("[]")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM webhooks`).Scan(&count); err != nil {
		return err
	}
	if count >= MaxWebhooks {
		return ErrWebhookLimit
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO webhooks(id,url,secret,kinds,service,description,created_at) VALUES(?,?,?,?,?,?,?)`,
		hook.ID, hook.URL, hook.Secret, string(kinds), hook.Service, hook.Description, hook.CreatedAt.UnixNano()); err != nil {
		return fmt.Errorf("storage: create webhook: %w", err)
	}
	if audit != nil {
		if err := insertAudit(ctx, tx, *audit); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteWebhookWithAudit removes a webhook and records the audit entry
// atomically. It returns ErrNotFound for an unknown ID.
func (s *Store) DeleteWebhookWithAudit(ctx context.Context, id string, audit *AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("storage: delete webhook: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	if audit != nil {
		if err := insertAudit(ctx, tx, *audit); err != nil {
			return err
		}
	}
	return tx.Commit()
}
