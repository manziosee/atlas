package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func testManagedCredential(token string, now time.Time) ManagedCredential {
	digest := sha256.Sum256([]byte(token))
	return ManagedCredential{
		ID: hex.EncodeToString(digest[:12]), Name: "integration test", Digest: digest[:],
		Scopes: []string{"read", "write"}, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
}

func TestManagedCredentialsPersistReplicateAndNeverResurrect(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "credentials.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	credential := testManagedCredential("runtime-secret-token", now)
	if err := first.CreateManagedCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.MergeManagedCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	firstDigest, err := first.ManagedCredentialDigest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := second.ManagedCredentialDigest(ctx)
	if err != nil || firstDigest != secondDigest {
		t.Fatalf("replica digests differ: %x / %x, err %v", firstDigest, secondDigest, err)
	}
	revoked, err := second.RevokeManagedCredential(ctx, credential.ID, now.Add(time.Minute))
	if err != nil || revoked.RevokedAt.IsZero() {
		t.Fatalf("revoke = %+v, err %v", revoked, err)
	}
	if err := first.MergeManagedCredential(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	if err := first.MergeManagedCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	rows, err := first.ManagedCredentials(ctx)
	if err != nil || len(rows) != 1 || rows[0].RevokedAt.IsZero() {
		t.Fatalf("stale record resurrected the credential: %+v, err %v", rows, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rows, err = reopened.ManagedCredentials(ctx)
	if err != nil || len(rows) != 1 || rows[0].ID != credential.ID || rows[0].RevokedAt.IsZero() {
		t.Fatalf("persisted credentials = %+v, err %v", rows, err)
	}
}

func TestCredentialAndAuditWritesAreAtomic(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	credential := testManagedCredential("atomic-secret-token", now)
	invalidAudit := &AuditEntry{ActorID: "actor", Action: "credential.created", At: now, Details: json.RawMessage(`{`)}
	if err := store.CreateManagedCredentialWithAudit(ctx, credential, invalidAudit); err == nil {
		t.Fatal("invalid audit record accepted")
	}
	if rows, err := store.ManagedCredentials(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("credential persisted without its audit record: %+v, err %v", rows, err)
	}
	if err := store.CreateManagedCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeManagedCredentialWithAudit(ctx, credential.ID, now.Add(time.Minute), invalidAudit); err == nil {
		t.Fatal("invalid revocation audit accepted")
	}
	rows, err := store.ManagedCredentials(ctx)
	if err != nil || len(rows) != 1 || !rows[0].RevokedAt.IsZero() {
		t.Fatalf("credential revoked without its audit record: %+v, err %v", rows, err)
	}
	if _, err := store.RevokeManagedCredential(ctx, "missing", now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing credential err = %v, want sql.ErrNoRows", err)
	}
}

func TestAuditPagingAndRetention(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	for i := range 3 {
		if err := store.AddAudit(ctx, AuditEntry{
			At: now.Add(time.Duration(i) * time.Second), ActorID: "actor", Action: "credential.created",
			ResourceID: string(rune('a' + i)), Details: json.RawMessage(`{"scopes":["read"]}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	page, next, err := store.Audit(ctx, AuditFilter{Limit: 2})
	if err != nil || len(page) != 2 || next == 0 || page[0].Action != "credential.created" {
		t.Fatalf("first audit page = %+v next=%d err=%v", page, next, err)
	}
	last, next, err := store.Audit(ctx, AuditFilter{Before: next, Limit: 2})
	if err != nil || len(last) != 1 || next != 0 {
		t.Fatalf("second audit page = %+v next=%d err=%v", last, next, err)
	}
	removed, err := store.PruneAudit(ctx, now.AddDate(0, 0, AuditRetentionDays+1))
	if err != nil || removed != 3 {
		t.Fatalf("pruned %d audit entries, err %v", removed, err)
	}
	remaining, _, err := store.Audit(ctx, AuditFilter{Limit: 10})
	if err != nil || len(remaining) != 0 {
		t.Fatalf("audit after pruning = %+v, err %v", remaining, err)
	}
}
