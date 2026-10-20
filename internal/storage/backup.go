package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Backup creates a transactionally consistent standalone SQLite snapshot.
// The destination must not already exist.
func (s *Store) Backup(ctx context.Context, destination string) error {
	if destination == "" || destination == ":memory:" {
		return errors.New("storage: backup destination must be a file path")
	}
	source, err := filepath.Abs(s.path)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if filepath.Clean(source) == filepath.Clean(target) {
		return errors.New("storage: backup destination must differ from the live database")
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("storage: backup destination already exists: %s", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage: inspect backup destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("storage: create backup directory: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, target); err != nil {
		return fmt.Errorf("storage: create backup: %w", err)
	}
	return nil
}

// Restore copies an integrity-checked backup into a new database path. It
// refuses to overwrite an existing path so operators must move live data
// aside deliberately before restoring.
func Restore(ctx context.Context, backupPath, destination string) error {
	if backupPath == "" || destination == "" {
		return errors.New("storage: backup and destination paths are required")
	}
	source, err := filepath.Abs(backupPath)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if filepath.Clean(source) == filepath.Clean(target) {
		return errors.New("storage: restore source and destination must differ")
	}
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("storage: read backup: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("storage: restore destination already exists: %s", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage: inspect restore destination: %w", err)
	}
	if err := verifyDatabase(ctx, source); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("storage: create restore directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".atlas-restore-*")
	if err != nil {
		return fmt.Errorf("storage: create restore temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	in, err := os.Open(source) // #nosec G304 -- source is the operator-supplied backup path.
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: open backup: %w", err)
	}
	_, copyErr := io.Copy(tmp, in)
	closeInErr := in.Close()
	if copyErr != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: copy backup: %w", copyErr)
	}
	if closeInErr != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: close backup: %w", closeInErr)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: sync restored database: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: set restored database permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close restored database: %w", err)
	}
	// A hard link installs the fully written file atomically and fails if a
	// destination appeared after the earlier existence check. Rename would
	// replace that file on Unix, violating the no-overwrite contract.
	if err := os.Link(tmpName, target); err != nil {
		return fmt.Errorf("storage: install restored database: %w", err)
	}
	if err := os.Remove(tmpName); err != nil {
		return fmt.Errorf("storage: remove restore temporary file: %w", err)
	}
	return nil
}

func verifyDatabase(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("storage: open backup: %w", err)
	}
	defer func() { _ = db.Close() }()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("storage: verify backup: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("storage: backup integrity check failed: %s", result)
	}
	return nil
}
