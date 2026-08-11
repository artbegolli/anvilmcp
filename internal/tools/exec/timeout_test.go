package exec

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/artbegolli/anvilmcp/internal/config"
	"github.com/artbegolli/anvilmcp/internal/policy"
	"go.uber.org/zap"
)

// newTimeoutTestTools builds a Tools whose policy allows the given commands,
// rooted at a temp workspace.
func newTimeoutTestTools(t *testing.T, allow ...string) *Tools {
	t.Helper()
	tmpdir := t.TempDir()
	allowMap := make(map[string]policy.CommandPolicy, len(allow))
	for _, c := range allow {
		allowMap[c] = policy.CommandPolicy{}
	}
	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: tmpdir},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: tmpdir,
			AllowRead:     []string{tmpdir + "/**"},
			AllowWrite:    []string{tmpdir + "/**"},
		},
		Exec: policy.ExecPolicy{Default: "deny", Allow: allowMap},
	}
	return NewTools(policy.NewEngine(p, ""), zap.NewNop(), &config.SessionConfig{
		SessionID: "test", WorkspaceRoot: tmpdir, HomeDir: tmpdir,
	}, 4, nil)
}

// TestHandleShellTimeoutEnforced verifies the per-call timeout actually kills
// the process (the regression: it used to be wired to a context the command
// wasn't bound to, so it never fired).
func TestHandleShellTimeoutEnforced(t *testing.T) {
	tools := newTimeoutTestTools(t, "sleep")
	to := 1

	start := time.Now()
	resp, err := tools.HandleShell(context.Background(), &ShellRequest{
		Cmd:            "sleep",
		Argv:           []string{"30"},
		TimeoutSeconds: &to,
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("HandleShell returned error: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("timeout not enforced: ran %v for a 1s timeout on sleep 30", elapsed)
	}
	if resp.ExitCode != 124 {
		t.Errorf("expected exit code 124 (timeout), got %d", resp.ExitCode)
	}
}

// TestHandleShellTimeoutReapsForkedWorkers is the load-bearing test: a payload
// that forks a worker inheriting stdout must not hang cmd.Wait past the
// deadline. The worker sleeps 30s; with the process-group kill we return in
// ~1s. If only WaitDelay (10s) saved us, we'd return at ~11s; if nothing did,
// ~30s. Asserting < 8s specifically proves the group kill fired.
func TestHandleShellTimeoutReapsForkedWorkers(t *testing.T) {
	tools := newTimeoutTestTools(t, "sh")
	to := 1

	start := time.Now()
	resp, err := tools.HandleShell(context.Background(), &ShellRequest{
		Cmd:            "sh",
		Argv:           []string{"-c", "sleep 30 & echo started; wait"},
		TimeoutSeconds: &to,
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("HandleShell returned error: %v", err)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("cmd.Wait hung on a forked worker: ran %v (group kill did not reap it)", elapsed)
	}
	if resp.ExitCode != 124 {
		t.Errorf("expected exit code 124 (timeout), got %d", resp.ExitCode)
	}
	if !strings.Contains(resp.Stdout, "started") {
		t.Errorf("expected captured output before the kill, got %q", resp.Stdout)
	}
}

// TestHandleShellCompletesWithinTimeout guards against a false positive: a
// command that finishes well within its timeout must return its real exit code
// and not be killed.
func TestHandleShellCompletesWithinTimeout(t *testing.T) {
	tools := newTimeoutTestTools(t, "sh")
	to := 30

	resp, err := tools.HandleShell(context.Background(), &ShellRequest{
		Cmd:            "sh",
		Argv:           []string{"-c", "echo hi; exit 3"},
		TimeoutSeconds: &to,
	})
	if err != nil {
		t.Fatalf("HandleShell returned error: %v", err)
	}
	if resp.ExitCode != 3 {
		t.Errorf("expected exit code 3, got %d", resp.ExitCode)
	}
	if !strings.Contains(resp.Stdout, "hi") {
		t.Errorf("expected stdout to contain 'hi', got %q", resp.Stdout)
	}
}

// TestHandleShellTimeoutWithStreaming confirms the timeout still fires when the
// caller opted into streaming (the streamingWriter sits between the process and
// capture; a hang there would be a separate failure mode).
func TestHandleShellTimeoutWithStreaming(t *testing.T) {
	tools := newTimeoutTestTools(t, "sh")
	to := 1

	// A non-nil OnChunk routes output through the streamingWriter, which is the
	// path we want to prove still honors the timeout.
	onChunk := func(stream string, data []byte) {}

	start := time.Now()
	resp, err := tools.HandleShell(context.Background(), &ShellRequest{
		Cmd:            "sh",
		Argv:           []string{"-c", "echo warmup; sleep 30"},
		TimeoutSeconds: &to,
		OnChunk:        onChunk,
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("HandleShell returned error: %v", err)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("timeout not enforced under streaming: ran %v", elapsed)
	}
	if resp.ExitCode != 124 {
		t.Errorf("expected exit code 124, got %d", resp.ExitCode)
	}
}
