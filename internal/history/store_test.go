package history

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(context.Background(), filepath.Join(t.TempDir(), "history.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestInsertAndGetRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rec := NewRecord("mcp-1", "corr-1", "exec.shell", `{"cmd":"ls"}`)
	if rec.ID == "" {
		t.Fatal("NewRecord must generate an ID: it is the table's primary key")
	}
	rec.Decision = "allow"
	rule := "exec.allow:ls"
	rec.PolicyRule = &rule
	rec.CWD = "/workspace"

	if err := s.InsertRecord(ctx, rec); err != nil {
		t.Fatalf("InsertRecord: %v", err)
	}

	// Previously a SELECT * read: the table has a trailing created_at column
	// that no Scan destination matched, so every read failed.
	got, err := s.GetRecord(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if got.Tool != "exec.shell" || got.Decision != "allow" || got.CWD != "/workspace" {
		t.Errorf("round trip lost data: %+v", got)
	}
	if got.PolicyRule == nil || *got.PolicyRule != rule {
		t.Errorf("policy_rule = %v, want %q", got.PolicyRule, rule)
	}
}

func TestDistinctRecordsDoNotCollide(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// With an unset ID these both wrote the empty-string primary key, so the
	// second insert failed with a uniqueness violation.
	for i := 0; i < 3; i++ {
		if err := s.InsertRecord(ctx, NewRecord("m", "c", "fs.read", "{}")); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	got, err := s.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("records = %d, want 3", len(got))
	}
}

func TestUpdateRecordPersistsDecision(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rec := NewRecord("m", "c", "exec.shell", "{}")
	if err := s.InsertRecord(ctx, rec); err != nil {
		t.Fatalf("InsertRecord: %v", err)
	}

	// The decision is only known once the handler has run, so it arrives on
	// the update rather than the insert.
	rec.Decision = "deny"
	rule := "exec.default:deny"
	rec.PolicyRule = &rule
	rec.CWD = "/workspace"
	code := 126
	rec.Finalize("denied", &code, 0, 12)
	if err := s.UpdateRecord(ctx, rec); err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}

	got, err := s.GetRecord(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if got.Decision != "deny" {
		t.Errorf("decision = %q, want deny", got.Decision)
	}
	if got.PolicyRule == nil || *got.PolicyRule != rule {
		t.Errorf("policy_rule = %v, want %q", got.PolicyRule, rule)
	}
	if got.CWD != "/workspace" {
		t.Errorf("cwd = %q, want /workspace", got.CWD)
	}
	if got.Status != "denied" || got.ExitCode == nil || *got.ExitCode != 126 {
		t.Errorf("status/exit = %q/%v, want denied/126", got.Status, got.ExitCode)
	}
}

func TestPruneRetentionKeepsPinned(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	old := NewRecord("m", "c", "fs.read", "{}")
	old.TimestampStart = time.Now().AddDate(0, 0, -60).UnixNano()
	pinned := NewRecord("m", "c", "fs.read", "{}")
	pinned.TimestampStart = old.TimestampStart
	pinned.Pinned = true

	for _, r := range []*Record{old, pinned} {
		if err := s.InsertRecord(ctx, r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	n, err := s.Prune(ctx, 30, 0)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned = %d, want 1", n)
	}
	if _, err := s.GetRecord(ctx, pinned.ID); err != nil {
		t.Errorf("pinned record was pruned: %v", err)
	}
}

func TestPruneNoLimitsIsNoop(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.InsertRecord(ctx, NewRecord("m", "c", "fs.read", "{}")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	n, err := s.Prune(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 {
		t.Errorf("pruned = %d with both limits disabled, want 0", n)
	}
}

func TestPruneSizeCapDropsOldestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Enough bulk to exceed the cap, with plenty of headroom above the
	// retained-records floor so the cap is actually reachable.
	body := make([]byte, 4096)
	for i := range body {
		body[i] = 'x'
	}
	var newest *Record
	for i := 0; i < 400; i++ {
		r := NewRecord("m", "c", "exec.shell", string(body))
		r.TimestampStart = int64(i+1) * int64(time.Second)
		if err := s.InsertRecord(ctx, r); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		newest = r
	}

	before, err := s.sizeBytes()
	if err != nil {
		t.Fatalf("sizeBytes: %v", err)
	}
	budget := before / 2

	n, err := s.Prune(ctx, 0, budget)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n == 0 {
		t.Fatal("size cap pruned nothing")
	}

	after, err := s.sizeBytes()
	if err != nil {
		t.Fatalf("sizeBytes: %v", err)
	}
	if after > budget {
		t.Errorf("size after prune = %d, want <= %d", after, budget)
	}
	if _, err := s.GetRecord(ctx, newest.ID); err != nil {
		t.Errorf("newest record was pruned: %v", err)
	}
}

func TestPruneUnreachableSizeCapKeepsRecentRecords(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 300; i++ {
		r := NewRecord("m", "c", "exec.shell", "{}")
		r.TimestampStart = int64(i+1) * int64(time.Second)
		if err := s.InsertRecord(ctx, r); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// 1 KB is below SQLite's floor for this schema: the cap can never be met.
	// The trail must survive that rather than being ground away hourly.
	_, err := s.Prune(ctx, 0, 1024)
	if !errors.Is(err, ErrSizeCapUnreachable) {
		t.Fatalf("Prune error = %v, want ErrSizeCapUnreachable", err)
	}

	got, err := s.ListRecent(ctx, 1000)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if int64(len(got)) < minRetainedRecords {
		t.Errorf("retained %d records, want at least %d", len(got), minRetainedRecords)
	}
}

func TestDatabaseIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.db")
	store, err := NewStore(context.Background(), path, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Write enough to force the WAL sidecars into existence.
	for i := 0; i < 5; i++ {
		if err := store.InsertRecord(context.Background(), NewRecord("m", "c", "exec.shell", `{"cmd":"ls"}`)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// The trail holds command arguments and output previews; SQLite would
	// otherwise create these 0644 under a typical umask.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			continue // sidecar not materialised on this platform
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s mode = %o, want no group/other access", path+suffix, perm)
		}
	}
}
