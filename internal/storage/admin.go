package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	MaxManagedCredentials = 10_000
	MaxAuditPage          = 500
	AuditRetentionDays    = 90
)

// ManagedCredential contains only a token digest and metadata. Digest is
// replicated only over the authenticated gossip transport.
type ManagedCredential struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Digest    []byte    `json:"digest"`
	Scopes    []string  `json:"scopes"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	RevokedAt time.Time `json:"revoked_at,omitzero"`
}

func (c ManagedCredential) Validate() error {
	if len(c.ID) != 24 || len(c.Digest) != sha256.Size || hex.EncodeToString(c.Digest[:12]) != c.ID {
		return errors.New("storage: invalid managed credential fingerprint or digest")
	}
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 128 {
		return errors.New("storage: credential name must be 1 to 128 characters")
	}
	if len(c.Scopes) == 0 || len(c.Scopes) > 3 {
		return errors.New("storage: credential must have 1 to 3 scopes")
	}
	seen := map[string]bool{}
	for _, scope := range c.Scopes {
		if scope != "read" && scope != "write" && scope != "admin" || seen[scope] {
			return errors.New("storage: invalid or duplicate credential scope")
		}
		seen[scope] = true
	}
	if c.ExpiresAt.UnixNano() <= 0 || c.CreatedAt.UnixNano() <= 0 || c.UpdatedAt.UnixNano() <= 0 {
		return errors.New("storage: credential expiry and timestamps are required")
	}
	if c.UpdatedAt.Before(c.CreatedAt) {
		return errors.New("storage: credential update predates creation")
	}
	if !c.RevokedAt.IsZero() && c.RevokedAt.Before(c.CreatedAt) {
		return errors.New("storage: credential revocation predates creation")
	}
	if !c.RevokedAt.IsZero() && c.UpdatedAt.Before(c.RevokedAt) {
		return errors.New("storage: credential update predates revocation")
	}
	return nil
}

type AuditEntry struct {
	Seq        int64           `json:"seq"`
	At         time.Time       `json:"at"`
	ActorID    string          `json:"actor_id"`
	Action     string          `json:"action"`
	ResourceID string          `json:"resource_id,omitempty"`
	Details    json.RawMessage `json:"details"`
}

type AuditFilter struct {
	Before int64
	Limit  int
}

var errCredentialLimit = errors.New("storage: managed credential limit reached")

// OnCredentialChange registers a callback for new, updated, or replicated
// credentials. The callback runs after the transaction commits.
func (s *Store) OnCredentialChange(fn func(ManagedCredential)) {
	s.credentialListenersMu.Lock()
	s.credentialListeners = append(s.credentialListeners, fn)
	s.credentialListenersMu.Unlock()
}

func (s *Store) ManagedCredentials(ctx context.Context) ([]ManagedCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,digest,scopes,expires_at,created_at,updated_at,revoked_at FROM managed_credentials ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	credentials := make([]ManagedCredential, 0)
	for rows.Next() {
		c, err := scanManagedCredential(rows)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, c)
		if len(credentials) > MaxManagedCredentials {
			return nil, errCredentialLimit
		}
	}
	return credentials, rows.Err()
}

func (s *Store) CreateManagedCredential(ctx context.Context, c ManagedCredential) error {
	return s.CreateManagedCredentialWithAudit(ctx, c, nil)
}

func (s *Store) CreateManagedCredentialWithAudit(ctx context.Context, c ManagedCredential, audit *AuditEntry) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.RevokedAt.IsZero() {
		c.RevokedAt = time.Time{}
	}
	s.mu.Lock()
	err := func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM managed_credentials`).Scan(&count); err != nil {
			return err
		}
		if count >= MaxManagedCredentials {
			return errCredentialLimit
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO managed_credentials(id,name,digest,scopes,expires_at,created_at,updated_at,revoked_at) VALUES(?,?,?,?,?,?,?,?)`,
			c.ID, c.Name, c.Digest, encodeScopes(c.Scopes), c.ExpiresAt.UnixNano(), c.CreatedAt.UnixNano(), c.UpdatedAt.UnixNano(), timeNanos(c.RevokedAt)); err != nil {
			return fmt.Errorf("storage: create managed credential: %w", err)
		}
		if audit != nil {
			if err := insertAudit(ctx, tx, *audit); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return nil
	}()
	s.mu.Unlock()
	if err == nil {
		s.notifyCredentialChange(c)
	}
	return err
}

// RevokeManagedCredential permanently revokes a credential. Revocation is
// monotonic when records converge from multiple peers.
func (s *Store) RevokeManagedCredential(ctx context.Context, id string, at time.Time) (ManagedCredential, error) {
	return s.RevokeManagedCredentialWithAudit(ctx, id, at, nil)
}

func (s *Store) RevokeManagedCredentialWithAudit(ctx context.Context, id string, at time.Time, audit *AuditEntry) (ManagedCredential, error) {
	if at.IsZero() {
		return ManagedCredential{}, errors.New("storage: credential revocation time is required")
	}
	s.mu.Lock()
	current, err := func() (ManagedCredential, error) {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return ManagedCredential{}, err
		}
		defer func() { _ = tx.Rollback() }()
		current, err := scanManagedCredential(tx.QueryRowContext(ctx, `SELECT id,name,digest,scopes,expires_at,created_at,updated_at,revoked_at FROM managed_credentials WHERE id=?`, id))
		if err != nil {
			return ManagedCredential{}, err
		}
		if current.RevokedAt.IsZero() {
			current.RevokedAt, current.UpdatedAt = at.UTC(), at.UTC()
			if _, err := tx.ExecContext(ctx, `UPDATE managed_credentials SET revoked_at=?,updated_at=? WHERE id=? AND revoked_at=0`,
				current.RevokedAt.UnixNano(), current.UpdatedAt.UnixNano(), id); err != nil {
				return ManagedCredential{}, fmt.Errorf("storage: revoke managed credential: %w", err)
			}
		}
		if audit != nil {
			if err := insertAudit(ctx, tx, *audit); err != nil {
				return ManagedCredential{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return ManagedCredential{}, err
		}
		return current, nil
	}()
	s.mu.Unlock()
	if err != nil {
		return ManagedCredential{}, err
	}
	s.notifyCredentialChange(current)
	return current, nil
}

// MergeManagedCredential applies a replicated credential record. A revoked
// record always wins so a later stale snapshot cannot reactivate a token.
func (s *Store) MergeManagedCredential(ctx context.Context, incoming ManagedCredential) error {
	if err := incoming.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	current, err := scanManagedCredential(s.db.QueryRowContext(ctx, `SELECT id,name,digest,scopes,expires_at,created_at,updated_at,revoked_at FROM managed_credentials WHERE id=?`, incoming.ID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.mu.Unlock()
		return err
	}
	if err == nil {
		if !current.RevokedAt.IsZero() && incoming.RevokedAt.IsZero() {
			s.mu.Unlock()
			return nil
		}
		if incoming.RevokedAt.IsZero() && incoming.UpdatedAt.Before(current.UpdatedAt) {
			s.mu.Unlock()
			return nil
		}
		if incoming.UpdatedAt.Equal(current.UpdatedAt) && !incoming.RevokedAt.IsZero() && current.RevokedAt.IsZero() {
			// A revoke wins a tie.
		} else if incoming.UpdatedAt.Equal(current.UpdatedAt) && managedCredentialEqual(current, incoming) {
			s.mu.Unlock()
			return nil
		} else if incoming.UpdatedAt.Equal(current.UpdatedAt) && managedCredentialOrder(incoming, current) <= 0 {
			s.mu.Unlock()
			return nil
		}
		if !incoming.RevokedAt.IsZero() && current.UpdatedAt.After(incoming.UpdatedAt) {
			incoming.UpdatedAt = current.UpdatedAt
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_credentials`).Scan(&count); err != nil {
			s.mu.Unlock()
			return err
		}
		if count >= MaxManagedCredentials {
			s.mu.Unlock()
			return errCredentialLimit
		}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_credentials(id,name,digest,scopes,expires_at,created_at,updated_at,revoked_at) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name,digest=excluded.digest,scopes=excluded.scopes,expires_at=excluded.expires_at,created_at=excluded.created_at,updated_at=excluded.updated_at,revoked_at=excluded.revoked_at`,
		incoming.ID, incoming.Name, incoming.Digest, encodeScopes(incoming.Scopes), incoming.ExpiresAt.UnixNano(), incoming.CreatedAt.UnixNano(), incoming.UpdatedAt.UnixNano(), timeNanos(incoming.RevokedAt))
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("storage: merge managed credential: %w", err)
	}
	s.notifyCredentialChange(incoming)
	return nil
}

func (s *Store) ManagedCredentialDigest(ctx context.Context) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	credentials, err := s.ManagedCredentials(ctx)
	if err != nil {
		return digest, err
	}
	h := sha256.New()
	for _, c := range credentials {
		encoded, err := json.Marshal(canonicalManagedCredential(c))
		if err != nil {
			return digest, err
		}
		var length [4]byte
		if uint64(len(encoded)) > uint64(^uint32(0)) {
			return digest, errCredentialLimit
		}
		binary.BigEndian.PutUint32(length[:], uint32(len(encoded)))
		_, _ = h.Write(length[:])
		_, _ = h.Write(encoded)
	}
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

func (s *Store) ManagedCredentialSnapshot(ctx context.Context) ([]ManagedCredential, error) {
	return s.ManagedCredentials(ctx)
}

func (s *Store) notifyCredentialChange(c ManagedCredential) {
	s.credentialListenersMu.RLock()
	defer s.credentialListenersMu.RUnlock()
	for _, listener := range s.credentialListeners {
		listener(c)
	}
}

func (s *Store) AddAudit(ctx context.Context, entry AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return insertAudit(ctx, s.db, entry)
}

func (s *Store) Audit(ctx context.Context, filter AuditFilter) ([]AuditEntry, int64, error) {
	if filter.Limit < 1 || filter.Limit > MaxAuditPage {
		return nil, 0, fmt.Errorf("storage: audit page size must be between 1 and %d", MaxAuditPage)
	}
	query := `SELECT seq,at,actor_id,action,resource_id,details FROM admin_audit_log`
	args := []any{}
	if filter.Before > 0 {
		query += ` WHERE seq < ?`
		args = append(args, filter.Before)
	}
	query += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, filter.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	entries := make([]AuditEntry, 0, filter.Limit)
	for rows.Next() {
		var entry AuditEntry
		var at int64
		var details string
		if err := rows.Scan(&entry.Seq, &at, &entry.ActorID, &entry.Action, &entry.ResourceID, &details); err != nil {
			return nil, 0, err
		}
		entry.At, entry.Details = time.Unix(0, at).UTC(), json.RawMessage(details)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next int64
	if len(entries) > filter.Limit {
		entries = entries[:filter.Limit]
		next = entries[len(entries)-1].Seq
	}
	return entries, next, nil
}

func (s *Store) PruneAudit(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, errors.New("storage: audit prune time is required")
	}
	cutoff := now.UTC().AddDate(0, 0, -AuditRetentionDays)
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM admin_audit_log WHERE at < ?`, cutoff.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("storage: prune admin audit log: %w", err)
	}
	return result.RowsAffected()
}

func scanManagedCredential(row interface{ Scan(...any) error }) (ManagedCredential, error) {
	var c ManagedCredential
	var digest []byte
	var scopes string
	var expiresAt, createdAt, updatedAt, revokedAt int64
	err := row.Scan(&c.ID, &c.Name, &digest, &scopes, &expiresAt, &createdAt, &updatedAt, &revokedAt)
	if err != nil {
		return ManagedCredential{}, err
	}
	if err := json.Unmarshal([]byte(scopes), &c.Scopes); err != nil {
		return ManagedCredential{}, fmt.Errorf("storage: decode managed credential scopes: %w", err)
	}
	c.Digest = bytes.Clone(digest)
	c.ExpiresAt, c.CreatedAt, c.UpdatedAt = time.Unix(0, expiresAt).UTC(), time.Unix(0, createdAt).UTC(), time.Unix(0, updatedAt).UTC()
	if revokedAt != 0 {
		c.RevokedAt = time.Unix(0, revokedAt).UTC()
	}
	return c, nil
}

type auditExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertAudit(ctx context.Context, executor auditExecutor, entry AuditEntry) error {
	if entry.At.IsZero() || entry.ActorID == "" || entry.Action == "" {
		return errors.New("storage: audit entry requires a timestamp, actor and action")
	}
	if len(entry.Details) == 0 {
		entry.Details = json.RawMessage(`{}`)
	}
	if !json.Valid(entry.Details) {
		return errors.New("storage: audit details must be valid JSON")
	}
	if _, err := executor.ExecContext(ctx, `INSERT INTO admin_audit_log(at,actor_id,action,resource_id,details) VALUES(?,?,?,?,?)`,
		entry.At.UTC().UnixNano(), entry.ActorID, entry.Action, entry.ResourceID, string(entry.Details)); err != nil {
		return fmt.Errorf("storage: write admin audit entry: %w", err)
	}
	return nil
}

func encodeScopes(scopes []string) string {
	normalized := slices.Clone(scopes)
	slices.Sort(normalized)
	raw, _ := json.Marshal(normalized)
	return string(raw)
}

func canonicalManagedCredential(c ManagedCredential) ManagedCredential {
	c.Scopes = slices.Clone(c.Scopes)
	slices.Sort(c.Scopes)
	c.ExpiresAt, c.CreatedAt, c.UpdatedAt, c.RevokedAt = c.ExpiresAt.UTC().Round(0), c.CreatedAt.UTC().Round(0), c.UpdatedAt.UTC().Round(0), c.RevokedAt.UTC().Round(0)
	return c
}

func managedCredentialEqual(a, b ManagedCredential) bool {
	left, _ := json.Marshal(canonicalManagedCredential(a))
	right, _ := json.Marshal(canonicalManagedCredential(b))
	return bytes.Equal(left, right)
}

func managedCredentialOrder(a, b ManagedCredential) int {
	left, _ := json.Marshal(canonicalManagedCredential(a))
	right, _ := json.Marshal(canonicalManagedCredential(b))
	return bytes.Compare(left, right)
}
