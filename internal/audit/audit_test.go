package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/history"
	"github.com/artbegolli/anvilmcp/internal/metrics"
	"github.com/artbegolli/anvilmcp/internal/policy"
)

func newRecorder(t *testing.T, cfg policy.AuditPolicy) (*Recorder, *history.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := history.NewStore(context.Background(), filepath.Join(dir, "history.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	engine := policy.NewEngine(policy.DefaultPolicy(), filepath.Join(dir, "config.yaml"))
	rec, err := NewRecorder(store, engine, cfg, "sess-test", zap.NewNop(), metrics.NewNoop())
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return rec, store
}

func TestRecordsCallWithDecisionAndOutcome(t *testing.T) {
	rec, store := newRecorder(t, policy.AuditPolicy{})
	ctx := context.Background()

	ctx, entry, err := rec.Begin(ctx, Call{
		Tool: "exec.shell",
		Args: map[string]any{"cmd": "python3", "argv": []string{"-c", "print(1)"}},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// The record must be durable before the call completes, so a crash or a
	// hang still leaves evidence the call was attempted.
	running, err := store.ListByTool(ctx, "exec.shell", 10)
	if err != nil {
		t.Fatalf("ListByTool: %v", err)
	}
	if len(running) != 1 {
		t.Fatalf("records mid-call = %d, want 1", len(running))
	}
	if running[0].Status != "running" {
		t.Errorf("mid-call status = %q, want running", running[0].Status)
	}

	if got := FromContext(ctx); got != entry {
		t.Error("FromContext did not return the open entry")
	}

	entry.SetDecision(DecisionAllow, "exec.allow:python3")
	entry.SetCWD("/workspace")
	entry.SetExitCode(0)
	entry.SetOutput("1\n", "", 2, 0, false)
	entry.Finish(ctx, StatusOK)

	got, err := store.ListByTool(ctx, "exec.shell", 10)
	if err != nil {
		t.Fatalf("ListByTool: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1 (Finish must update, not insert again)", len(got))
	}
	r := got[0]
	if r.Status != StatusOK {
		t.Errorf("status = %q, want %q", r.Status, StatusOK)
	}
	if r.Decision != DecisionAllow {
		t.Errorf("decision = %q, want %q", r.Decision, DecisionAllow)
	}
	if r.PolicyRule == nil || *r.PolicyRule != "exec.allow:python3" {
		t.Errorf("policy_rule = %v, want exec.allow:python3", r.PolicyRule)
	}
	if r.ExitCode == nil || *r.ExitCode != 0 {
		t.Errorf("exit_code = %v, want 0", r.ExitCode)
	}
	if r.StdoutPreview == nil || *r.StdoutPreview != "1\n" {
		t.Errorf("stdout_preview = %v, want %q", r.StdoutPreview, "1\n")
	}
	if r.CWD != "/workspace" {
		t.Errorf("cwd = %q, want /workspace", r.CWD)
	}
	if r.DurationMS == nil {
		t.Error("duration_ms not set")
	}
	if !strings.Contains(r.ArgsJSON, "python3") {
		t.Errorf("args_json = %q, want it to mention python3", r.ArgsJSON)
	}
}

func TestDenyAlwaysStoredAsDenied(t *testing.T) {
	rec, store := newRecorder(t, policy.AuditPolicy{})
	ctx := context.Background()

	ctx, entry, _ := rec.Begin(ctx, Call{Tool: "fs.read", Args: map[string]any{"path": "/etc/passwd"}})
	entry.SetDecision(DecisionDeny, "paths.deny:/etc/**")

	// A denial reaches the middleware as a plain error; it must not be filed
	// as one, or probing becomes indistinguishable from a missing file.
	entry.Finish(ctx, StatusError)

	got, _ := store.ListByTool(ctx, "fs.read", 1)
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	if got[0].Status != StatusDenied {
		t.Errorf("status = %q, want %q", got[0].Status, StatusDenied)
	}
}

func TestDefaultDecisionIsUnmediated(t *testing.T) {
	rec, store := newRecorder(t, policy.AuditPolicy{})
	ctx := context.Background()

	// kernel.exec is gated only at launch; nothing evaluates the code itself.
	// That must be visible in the trail rather than looking like an allow.
	ctx, entry, _ := rec.Begin(ctx, Call{Tool: "kernel.exec", Args: map[string]any{"code": "import os"}})
	entry.Finish(ctx, StatusOK)

	got, _ := store.ListByTool(ctx, "kernel.exec", 1)
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	if got[0].Decision != DecisionUnmediated {
		t.Errorf("decision = %q, want %q", got[0].Decision, DecisionUnmediated)
	}
}

func TestSetStatusOverridesToolSuccess(t *testing.T) {
	rec, store := newRecorder(t, policy.AuditPolicy{})
	ctx := context.Background()
	ctx, entry, _ := rec.Begin(ctx, Call{Tool: "kernel.exec"})
	entry.SetStatus(StatusError)
	entry.Finish(ctx, StatusOK)

	got, _ := store.ListByTool(ctx, "kernel.exec", 1)
	if got[0].Status != StatusError {
		t.Errorf("status = %q, want %q", got[0].Status, StatusError)
	}
}

func TestRecordSurvivesCancelledRequest(t *testing.T) {
	rec, store := newRecorder(t, policy.AuditPolicy{})
	ctx, cancel := context.WithCancel(context.Background())

	ctx, entry, _ := rec.Begin(ctx, Call{Tool: "exec.shell", Args: map[string]any{"cmd": "sleep"}})
	entry.SetDecision(DecisionAllow, "exec.allow:sleep")

	// A client that disconnects must not be able to drop its own record.
	cancel()
	entry.Finish(ctx, StatusKilled)

	got, err := store.ListByTool(context.Background(), "exec.shell", 1)
	if err != nil {
		t.Fatalf("ListByTool: %v", err)
	}
	if len(got) != 1 || got[0].Status != StatusKilled {
		t.Fatalf("record after cancel = %+v, want one killed record", got)
	}
}

func TestNoStoreToolsAndNoStoreOutput(t *testing.T) {
	rec, store := newRecorder(t, policy.AuditPolicy{
		NoStoreTools:  []string{"policy.show"},
		NoStoreOutput: []string{"fs.read"},
	})
	ctx := context.Background()

	_, entry, err := rec.Begin(ctx, Call{Tool: "policy.show"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if entry != nil {
		t.Error("no_store_tools should yield a nil entry")
	}
	// Nil entries must tolerate the full method set; handlers do not nil-check.
	entry.SetDecision(DecisionAllow, "x")
	entry.SetCWD("/x")
	entry.SetExitCode(1)
	entry.SetStatus(StatusOK)
	entry.SetOutput("a", "b", 1, 1, false)
	entry.Finish(ctx, StatusOK)
	if got := entry.Tool(); got != "" {
		t.Errorf("nil entry Tool() = %q, want empty", got)
	}
	if n, _ := store.ListByTool(ctx, "policy.show", 1); len(n) != 0 {
		t.Errorf("policy.show records = %d, want 0", len(n))
	}

	rctx, re, _ := rec.Begin(ctx, Call{Tool: "fs.read", Args: map[string]any{"path": "/workspace/a"}})
	re.SetOutput("file body", "", 9, 0, false)
	re.Finish(rctx, StatusOK)

	got, _ := store.ListByTool(ctx, "fs.read", 1)
	if len(got) != 1 {
		t.Fatalf("fs.read records = %d, want 1 (the call is still recorded)", len(got))
	}
	if got[0].StdoutPreview != nil {
		t.Errorf("stdout_preview = %v, want nil under no_store_output", *got[0].StdoutPreview)
	}
	if got[0].StdoutBytes != 9 {
		t.Errorf("stdout_bytes = %d, want 9 (volume is still recorded)", got[0].StdoutBytes)
	}
}

func TestFailClosedRefusesWhenStoreIsUnusable(t *testing.T) {
	dir := t.TempDir()
	store, err := history.NewStore(context.Background(), filepath.Join(dir, "history.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	engine := policy.NewEngine(policy.DefaultPolicy(), filepath.Join(dir, "config.yaml"))

	rec, err := NewRecorder(store, engine, policy.AuditPolicy{FailClosed: true}, "s", zap.NewNop(), metrics.NewNoop())
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	// Closing the store is the cheapest faithful stand-in for a state volume
	// that has gone away mid-session.
	_ = store.Close()

	_, entry, err := rec.Begin(context.Background(), Call{Tool: "exec.shell"})
	if err == nil {
		t.Fatal("fail_closed Begin should error when the record cannot be written")
	}
	if entry != nil {
		t.Error("fail_closed Begin should not return a usable entry")
	}
	if !strings.Contains(err.Error(), "fail_closed") {
		t.Errorf("error = %q, want it to name fail_closed", err)
	}
}

func TestFailOpenStillRecordsOnFinish(t *testing.T) {
	rec, _ := newRecorder(t, policy.AuditPolicy{})
	ctx := context.Background()
	// Default (fail-open) must not refuse the call.
	_, entry, err := rec.Begin(ctx, Call{Tool: "exec.shell"})
	if err != nil || entry == nil {
		t.Fatalf("Begin = %v, %v; want a usable entry", entry, err)
	}
}

func TestJSONLMirror(t *testing.T) {
	dir := t.TempDir()
	mirrorPath := filepath.Join(dir, "nested", "audit.jsonl")
	rec, _ := newRecorder(t, policy.AuditPolicy{JSONLMirrorPath: mirrorPath})

	ctx := context.Background()
	ctx, entry, _ := rec.Begin(ctx, Call{Tool: "exec.shell", Args: map[string]any{"cmd": "ls"}})
	entry.SetDecision(DecisionAllow, "exec.allow:ls")
	entry.SetExitCode(0)
	entry.Finish(ctx, StatusOK)

	f, err := os.Open(mirrorPath) // #nosec G304 -- test-local temp path
	if err != nil {
		t.Fatalf("open mirror: %v", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatal("mirror is empty")
	}
	var l struct {
		Tool     string `json:"tool"`
		Status   string `json:"status"`
		Decision string `json:"decision"`
		Rule     string `json:"policy_rule"`
	}
	if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
		t.Fatalf("mirror line is not valid JSON: %v (%s)", err, sc.Text())
	}
	if l.Tool != "exec.shell" || l.Status != StatusOK || l.Decision != DecisionAllow {
		t.Errorf("mirror line = %+v, want exec.shell/ok/allow", l)
	}
	if l.Rule != "exec.allow:ls" {
		t.Errorf("mirror policy_rule = %q, want exec.allow:ls", l.Rule)
	}
	if sc.Scan() {
		t.Errorf("mirror has a second line: %q (only completed calls should be written)", sc.Text())
	}

	// Permissions: the mirror carries command arguments.
	info, err := os.Stat(mirrorPath)
	if err != nil {
		t.Fatalf("stat mirror: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("mirror mode = %o, want 600", perm)
	}
}

func TestRedactsSecretLookingArgs(t *testing.T) {
	rec, store := newRecorder(t, policy.AuditPolicy{})
	ctx := context.Background()

	ctx, entry, _ := rec.Begin(ctx, Call{Tool: "exec.shell", Args: map[string]any{
		"cmd": "python3",
		"env": map[string]string{
			"AWS_SECRET_ACCESS_KEY": "super-secret",
			"GITHUB_TOKEN":          "ghp_xxx",
			"MY_PASSWORD":           "hunter2",
			"PATH":                  "/usr/bin",
		},
	}})
	entry.Finish(ctx, StatusOK)

	got, _ := store.ListByTool(ctx, "exec.shell", 1)
	args := got[0].ArgsJSON
	for _, secret := range []string{"super-secret", "ghp_xxx", "hunter2"} {
		if strings.Contains(args, secret) {
			t.Errorf("args_json leaked %q: %s", secret, args)
		}
	}
	if !strings.Contains(args, "/usr/bin") {
		t.Errorf("args_json dropped a non-secret value: %s", args)
	}
	if !strings.Contains(args, redactedMarker) {
		t.Errorf("args_json has no redaction marker: %s", args)
	}
}

func TestLargeArgsAreSummarisedNotTruncatedMidJSON(t *testing.T) {
	rec, store := newRecorder(t, policy.AuditPolicy{})
	ctx := context.Background()

	ctx, entry, _ := rec.Begin(ctx, Call{Tool: "fs.write", Args: map[string]any{
		"path":    "/workspace/big.py",
		"content": strings.Repeat("x", 200*1024),
	}})
	entry.Finish(ctx, StatusOK)

	got, _ := store.ListByTool(ctx, "fs.write", 1)
	args := got[0].ArgsJSON
	if len(args) > maxArgsJSONBytes {
		t.Errorf("args_json is %d bytes, want <= %d", len(args), maxArgsJSONBytes)
	}
	// Must stay parseable: a log pipeline consumes this.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("args_json is not valid JSON: %v", err)
	}
	if parsed["content"] == nil && parsed["_audit_truncated"] == nil {
		t.Errorf("expected either a truncated value or a truncation summary, got %v", parsed)
	}
}

func TestTruncateDoesNotSplitRunes(t *testing.T) {
	// 3-byte runes against a limit that lands mid-rune.
	s := strings.Repeat("héllo wörld ", 500)
	out := truncate(s, 1024)
	if len(out) < 1 {
		t.Fatal("empty output")
	}
	head := strings.Split(out, "…")[0]
	for i, r := range head {
		if r == '�' {
			t.Fatalf("invalid rune at byte %d", i)
		}
	}
}

func TestPruneEnforcesRetention(t *testing.T) {
	rec, store := newRecorder(t, policy.AuditPolicy{RetentionDays: 7})
	ctx := context.Background()

	// One fresh record, one backdated past the retention window.
	fresh := history.NewRecord("s", "s", "fs.read", "{}")
	if err := store.InsertRecord(ctx, fresh); err != nil {
		t.Fatalf("insert fresh: %v", err)
	}
	old := history.NewRecord("s", "s", "fs.read", "{}")
	old.TimestampStart = time.Now().AddDate(0, 0, -30).UnixNano()
	if err := store.InsertRecord(ctx, old); err != nil {
		t.Fatalf("insert old: %v", err)
	}

	rec.prune(ctx)

	got, err := store.ListByTool(ctx, "fs.read", 10)
	if err != nil {
		t.Fatalf("ListByTool: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("records after prune = %d, want 1", len(got))
	}
	if got[0].ID != fresh.ID {
		t.Error("prune deleted the wrong record")
	}
}

func TestNilRecorderIsUsable(t *testing.T) {
	// wrapTool is constructed with a nil recorder in unit tests and in any
	// future path where auditing is not configured.
	var rec *Recorder
	ctx, entry, err := rec.Begin(context.Background(), Call{Tool: "fs.read"})
	if err != nil || entry != nil {
		t.Fatalf("nil recorder Begin = %v, %v; want nil, nil", entry, err)
	}
	if FromContext(ctx) != nil {
		t.Error("nil recorder should not install an entry")
	}
	if err := rec.Close(); err != nil {
		t.Errorf("nil recorder Close = %v", err)
	}
}
