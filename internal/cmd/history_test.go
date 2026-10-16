package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/history"
)

// seedHistory builds a database with a representative spread of records and
// returns its path.
func seedHistory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "history.db")

	store, err := history.NewStore(context.Background(), path, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	base := time.Now().Add(-time.Hour)
	add := func(tool, decision, status, args string, rule *string, offset time.Duration) *history.Record {
		rec := history.NewRecord("mcp-1", "sess-1", tool, args)
		rec.TimestampStart = base.Add(offset).UnixNano()
		rec.Decision = decision
		rec.PolicyRule = rule
		code := 0
		rec.Finalize(status, &code, 0, 0)
		if err := store.InsertRecord(context.Background(), rec); err != nil {
			t.Fatalf("insert %s: %v", tool, err)
		}
		return rec
	}

	denyRule := "paths.deny:/etc/**"
	allowRule := "exec.allow:python3"
	add("fs.read", "deny", "denied", `{"path":"/etc/passwd"}`, &denyRule, 0)
	add("exec.shell", "allow", "ok", `{"cmd":"python3","argv":["-c","print(1)"]}`, &allowRule, time.Minute)
	add("kernel.exec", "unmediated", "ok", `{"code":"import os"}`, nil, 2*time.Minute)

	return path
}

// runHistory executes the history command with args against a seeded database.
func runHistory(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	cmd := NewHistoryCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	// Point the policy flags at paths that do not exist so the loader falls
	// back to defaults and leaves --history-db in charge.
	full := append([]string{
		"--history-db", dbPath,
		"--policy", filepath.Join(t.TempDir(), "none.yaml"),
		"--cluster-policy", filepath.Join(t.TempDir(), "none.yaml"),
	}, args...)
	cmd.SetArgs(full)
	err := cmd.Execute()
	return out.String(), err
}

func TestHistoryListShowsAllRecords(t *testing.T) {
	db := seedHistory(t)
	out, err := runHistory(t, db, "list")
	if err != nil {
		t.Fatalf("list: %v (%s)", err, out)
	}
	for _, want := range []string{"fs.read", "exec.shell", "kernel.exec", "denied", "unmediated"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestHistoryListSummarisesTheIdentifyingArgument(t *testing.T) {
	db := seedHistory(t)
	out, err := runHistory(t, db, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// The command, not just the tool name, is what makes a row actionable.
	if !strings.Contains(out, "python3 -c print(1)") {
		t.Errorf("expected the exec command in the summary column:\n%s", out)
	}
	if !strings.Contains(out, "/etc/passwd") {
		t.Errorf("expected the denied path in the summary column:\n%s", out)
	}
}

func TestHistoryListFilters(t *testing.T) {
	db := seedHistory(t)

	tests := []struct {
		name    string
		args    []string
		want    []string
		exclude []string
	}{
		{"denied shorthand", []string{"list", "--denied"}, []string{"fs.read"}, []string{"exec.shell", "kernel.exec"}},
		{"unmediated shorthand", []string{"list", "--unmediated"}, []string{"kernel.exec"}, []string{"fs.read", "exec.shell"}},
		{"by tool", []string{"list", "--tool", "exec.shell"}, []string{"exec.shell"}, []string{"fs.read", "kernel.exec"}},
		{"by status", []string{"list", "--status", "ok"}, []string{"exec.shell", "kernel.exec"}, []string{"fs.read"}},
		{"by decision", []string{"list", "--decision", "allow"}, []string{"exec.shell"}, []string{"kernel.exec"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runHistory(t, db, tc.args...)
			if err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q:\n%s", w, out)
				}
			}
			for _, x := range tc.exclude {
				if strings.Contains(out, x) {
					t.Errorf("unexpected %q:\n%s", x, out)
				}
			}
		})
	}
}

func TestHistoryListSinceWindow(t *testing.T) {
	db := seedHistory(t)

	// Everything seeded is within the last hour.
	out, err := runHistory(t, db, "list", "--since", "2h")
	if err != nil {
		t.Fatalf("since 2h: %v", err)
	}
	if !strings.Contains(out, "exec.shell") {
		t.Errorf("--since 2h should include seeded records:\n%s", out)
	}

	out, err = runHistory(t, db, "list", "--since", "1s")
	if err != nil {
		t.Fatalf("since 1s: %v", err)
	}
	if !strings.Contains(out, "No matching records") {
		t.Errorf("--since 1s should exclude everything:\n%s", out)
	}

	if _, err := runHistory(t, db, "list", "--since", "yesterday"); err == nil {
		t.Error("an unparseable --since should error")
	}
}

func TestHistoryListJSONEmbedsArgs(t *testing.T) {
	db := seedHistory(t)
	out, err := runHistory(t, db, "list", "--tool", "exec.shell", "--json")
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}

	var records []map[string]any
	if err := json.Unmarshal([]byte(out), &records); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	// args must be a nested object, not a quoted string, so jq can reach into it.
	args, ok := records[0]["args"].(map[string]any)
	if !ok {
		t.Fatalf("args is %T, want an object", records[0]["args"])
	}
	if args["cmd"] != "python3" {
		t.Errorf("args.cmd = %v, want python3", args["cmd"])
	}
	if records[0]["decision"] != "allow" {
		t.Errorf("decision = %v, want allow", records[0]["decision"])
	}
}

func TestHistoryShow(t *testing.T) {
	db := seedHistory(t)

	// Grab an id from the JSON listing rather than hardcoding one.
	listOut, err := runHistory(t, db, "list", "--denied", "--json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal([]byte(listOut), &records); err != nil {
		t.Fatalf("list json: %v", err)
	}
	id, _ := records[0]["id"].(string)

	out, err := runHistory(t, db, "show", id)
	if err != nil {
		t.Fatalf("show: %v (%s)", err, out)
	}
	for _, want := range []string{id, "fs.read", "denied", "paths.deny:/etc/**", "/etc/passwd"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
}

func TestHistoryShowExplainsUnmediated(t *testing.T) {
	db := seedHistory(t)
	listOut, _ := runHistory(t, db, "list", "--unmediated", "--json")
	var records []map[string]any
	if err := json.Unmarshal([]byte(listOut), &records); err != nil {
		t.Fatalf("list json: %v", err)
	}
	id, _ := records[0]["id"].(string)

	out, err := runHistory(t, db, "show", id)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out, "no chokepoint evaluated this call") {
		t.Errorf("an unmediated record should say so plainly:\n%s", out)
	}
}

func TestHistoryShowUnknownID(t *testing.T) {
	db := seedHistory(t)
	if _, err := runHistory(t, db, "show", "does-not-exist"); err == nil {
		t.Error("expected an error for an unknown id")
	}
}

func TestHistoryMissingDatabaseErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.db")
	out, err := runHistory(t, missing, "list")
	if err == nil {
		t.Fatalf("a missing database must error rather than report an empty trail:\n%s", out)
	}
	// Creating the file here would turn a typo into a silent empty result.
	if !strings.Contains(err.Error(), "nope.db") {
		t.Errorf("error should name the path, got %v", err)
	}
}

func TestHistoryTailEmitsStartAndCompletion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.db")

	writer, err := history.NewStore(context.Background(), path, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = writer.Close() }()

	reader, err := history.OpenReadOnly(context.Background(), path, zap.NewNop())
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = reader.Close() }()

	rec := history.NewRecord("mcp", "sess", "exec.shell", `{"cmd":"sleep"}`)
	rec.Decision = "allow"
	if err := writer.InsertRecord(context.Background(), rec); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tailLoop(ctx, reader, printer{w: &out}, 20*time.Millisecond, false) }()

	// Let the running row be picked up, then complete it.
	time.Sleep(120 * time.Millisecond)
	code := 0
	rec.Finalize("ok", &code, 5, 0)
	if err := writer.UpdateRecord(context.Background(), rec); err != nil {
		t.Fatalf("update: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("tailLoop: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "running") {
		t.Errorf("tail should announce the call when it starts:\n%s", got)
	}
	if !strings.Contains(got, "ok") {
		t.Errorf("tail should announce the call when it completes:\n%s", got)
	}
	// Exactly two lines: one per status transition, not one per poll.
	lines := strings.Count(strings.TrimSpace(got), "\n") + 1
	if lines != 2 {
		t.Errorf("tail emitted %d lines, want 2 (start + completion):\n%s", lines, got)
	}
}
