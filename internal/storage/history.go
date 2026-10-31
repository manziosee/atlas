package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Change kinds, shared by the history API and the event stream.
const (
	ChangeRegistered   = "registered"
	ChangeUpdated      = "updated"
	ChangeHealth       = "health"
	ChangeDeregistered = "deregistered"
)

// historyPerService caps the entries kept for one service.
const historyPerService = 100

// MaxHistoryPage bounds one History call.
const MaxHistoryPage = 500

// Kind classifies a committed write. It reports false for writes clients
// cannot observe, such as a tombstone for an ID this node never had.
func (c Change) Kind() (string, bool) {
	after := c.After
	wasActive := c.Before != nil && !c.Before.Deleted
	switch {
	case after.Deleted && wasActive:
		return ChangeDeregistered, true
	case after.Deleted:
		return "", false
	case !wasActive:
		return ChangeRegistered, true
	case c.Before.Health != after.Health && sameSpec(*c.Before, after) && c.Before.NodeID == after.NodeID:
		return ChangeHealth, true
	default:
		return ChangeUpdated, true
	}
}

// HistoryEntry is one recorded change. Service is the record as it was
// after the change. Seq orders entries on this node only.
type HistoryEntry struct {
	Seq       int64     `json:"seq"`
	ServiceID string    `json:"service_id"`
	Name      string    `json:"name"`
	Revision  uint64    `json:"revision"`
	Change    string    `json:"change"`
	At        time.Time `json:"at"`
	Service   Service   `json:"service"`
}

// HistoryFilter selects entries newest first. Set ServiceID or Name (or
// neither, for every service); Before continues from an earlier page.
type HistoryFilter struct {
	ServiceID string
	Name      string
	Before    int64
	Limit     int
}

// SetHistoryEnabled turns change recording on or off. It is on by default.
func (s *Store) SetHistoryEnabled(enabled bool) {
	s.historyOff.Store(!enabled)
}

// recordHistory appends change to the history inside the write's
// transaction. Callers hold s.mu.
func (s *Store) recordHistory(ctx context.Context, ex execer, change Change) error {
	if s.historyOff.Load() {
		return nil
	}
	kind, ok := change.Kind()
	if !ok {
		return nil
	}
	snapshot := change.After
	snapshot.CheckedAt, snapshot.HeartbeatAt = time.Time{}, time.Time{}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if _, err := ex.ExecContext(ctx, `INSERT INTO service_history (service_id, name, revision, change, at, record) VALUES (?,?,?,?,?,?)`,
		snapshot.ID, snapshot.Name, snapshot.Revision, kind, snapshot.UpdatedAt.UnixNano(), string(encoded)); err != nil {
		return fmt.Errorf("storage: record history: %w", err)
	}
	_, err = ex.ExecContext(ctx, `DELETE FROM service_history WHERE service_id = ? AND seq NOT IN
 (SELECT seq FROM service_history WHERE service_id = ? ORDER BY seq DESC LIMIT ?)`, snapshot.ID, snapshot.ID, historyPerService)
	if err != nil {
		return fmt.Errorf("storage: trim history: %w", err)
	}
	return nil
}

// History returns recorded changes newest first, and the Before value for
// the next page (zero when there are no more entries). Deregistered services
// keep their history.
func (s *Store) History(ctx context.Context, filter HistoryFilter) ([]HistoryEntry, int64, error) {
	if filter.Limit < 1 || filter.Limit > MaxHistoryPage {
		return nil, 0, fmt.Errorf("storage: history page size must be between 1 and %d", MaxHistoryPage)
	}
	var where []string
	var args []any
	if filter.ServiceID != "" {
		where, args = append(where, "service_id = ?"), append(args, filter.ServiceID)
	}
	if filter.Name != "" {
		where, args = append(where, "name = ?"), append(args, filter.Name)
	}
	if filter.Before > 0 {
		where, args = append(where, "seq < ?"), append(args, filter.Before)
	}
	query := `SELECT seq, service_id, name, revision, change, at, record FROM service_history`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ") // #nosec G202 -- fixed clauses; values are ? parameters.
	}
	query += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, filter.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	entries := make([]HistoryEntry, 0, filter.Limit)
	for rows.Next() {
		var entry HistoryEntry
		var at int64
		var record string
		if err := rows.Scan(&entry.Seq, &entry.ServiceID, &entry.Name, &entry.Revision, &entry.Change, &at, &record); err != nil {
			return nil, 0, err
		}
		entry.At = time.Unix(0, at).UTC()
		if err := json.Unmarshal([]byte(record), &entry.Service); err != nil {
			return nil, 0, fmt.Errorf("storage: decode history record: %w", err)
		}
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

// PruneHistory deletes entries for changes made before cutoff.
func (s *Store) PruneHistory(ctx context.Context, cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, errors.New("storage: prune cutoff is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM service_history WHERE at < ?`, cutoff.UnixNano())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
