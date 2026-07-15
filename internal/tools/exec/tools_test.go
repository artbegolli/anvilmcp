package exec

import (
	"context"
	"testing"

	"github.com/artbegolli/anvilmcp/internal/config"
	"github.com/artbegolli/anvilmcp/internal/policy"
	"go.uber.org/zap"
)

func TestShellBasic(t *testing.T) {
	logger := zap.NewNop()
	tmpdir := t.TempDir()

	sessConfig := &config.SessionConfig{
		SessionID:     "test",
		WorkspaceRoot: tmpdir,
		HomeDir:       tmpdir,
	}

	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: tmpdir},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: tmpdir,
			AllowRead:     []string{tmpdir + "/**"},
			AllowWrite:    []string{tmpdir + "/**"},
			Deny:          []string{},
		},
		Exec: policy.ExecPolicy{
			Default: "deny",
			Allow: map[string]policy.CommandPolicy{
				"echo": {},
				"test": {},
				"bash": {},
				"sh":   {},
			},
		},
		Env:    policy.EnvironmentPolicy{},
		Kernel: policy.KernelPolicy{},
		Audit:  policy.AuditPolicy{},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger, sessConfig, 4, nil)
	ctx := context.Background()

	// Test simple echo command
	resp, err := tools.HandleShell(ctx, &ShellRequest{
		Cmd:  "echo",
		Argv: []string{"hello", "world"},
	})

	if err != nil {
		t.Fatalf("HandleShell failed: %v", err)
	}

	if resp.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", resp.ExitCode)
	}

	if !contains(resp.Stdout, "hello") || !contains(resp.Stdout, "world") {
		t.Errorf("expected stdout to contain 'hello world', got %q", resp.Stdout)
	}

	if resp.Truncated {
		t.Error("expected truncated=false")
	}
}

func TestShellDeniedCommand(t *testing.T) {
	logger := zap.NewNop()
	tmpdir := t.TempDir()

	sessConfig := &config.SessionConfig{
		SessionID:     "test",
		WorkspaceRoot: tmpdir,
		HomeDir:       tmpdir,
	}

	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: tmpdir},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: tmpdir,
			AllowRead:     []string{tmpdir + "/**"},
			AllowWrite:    []string{tmpdir + "/**"},
			Deny:          []string{},
		},
		Exec: policy.ExecPolicy{
			Default: "deny",
			Allow: map[string]policy.CommandPolicy{
				"echo": {},
			},
			Deny: []string{"rm", "dd", "mkfs*"},
		},
		Env:    policy.EnvironmentPolicy{},
		Kernel: policy.KernelPolicy{},
		Audit:  policy.AuditPolicy{},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger, sessConfig, 4, nil)
	ctx := context.Background()

	// Attempt to run a denied command
	_, err := tools.HandleShell(ctx, &ShellRequest{
		Cmd:  "rm",
		Argv: []string{"-rf", "/"},
	})

	if err == nil {
		t.Error("expected command to be denied")
	}

	if !contains(err.Error(), "denied") {
		t.Errorf("expected 'denied' in error message, got: %v", err)
	}
}

func TestShellArgRestriction(t *testing.T) {
	logger := zap.NewNop()
	tmpdir := t.TempDir()

	sessConfig := &config.SessionConfig{
		SessionID:     "test",
		WorkspaceRoot: tmpdir,
		HomeDir:       tmpdir,
	}

	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: tmpdir},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: tmpdir,
			AllowRead:     []string{tmpdir + "/**"},
			AllowWrite:    []string{tmpdir + "/**"},
			Deny:          []string{},
		},
		Exec: policy.ExecPolicy{
			Default: "deny",
			Allow: map[string]policy.CommandPolicy{
				"pip": {
					ArgsDeny: []string{"uninstall", "--break-system-packages"},
				},
			},
		},
		Env:    policy.EnvironmentPolicy{},
		Kernel: policy.KernelPolicy{},
		Audit:  policy.AuditPolicy{},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger, sessConfig, 4, nil)
	ctx := context.Background()

	// Attempt to run pip with a denied arg
	_, err := tools.HandleShell(ctx, &ShellRequest{
		Cmd:  "pip",
		Argv: []string{"install", "uninstall", "package"},
	})

	if err == nil {
		t.Error("expected pip uninstall to be denied")
	}
}

func TestScriptExecution(t *testing.T) {
	logger := zap.NewNop()
	tmpdir := t.TempDir()

	sessConfig := &config.SessionConfig{
		SessionID:     "test",
		WorkspaceRoot: tmpdir,
		HomeDir:       tmpdir,
	}

	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: tmpdir},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: tmpdir,
			AllowRead:     []string{tmpdir + "/**"},
			AllowWrite:    []string{tmpdir + "/**"},
			Deny:          []string{},
		},
		Exec: policy.ExecPolicy{
			Default: "deny",
			Allow: map[string]policy.CommandPolicy{
				"sh":    {},
				"bash":  {},
				"echo": {},
			},
		},
		Env:    policy.EnvironmentPolicy{},
		Kernel: policy.KernelPolicy{},
		Audit:  policy.AuditPolicy{},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger, sessConfig, 4, nil)
	ctx := context.Background()

	// Execute a simple shell script
	resp, err := tools.HandleScript(ctx, &ScriptRequest{
		Language: "sh",
		Content:  "#!/bin/sh\necho 'script output'\nexit 0\n",
	})

	if err != nil {
		t.Fatalf("HandleScript failed: %v", err)
	}

	if resp.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", resp.ExitCode)
	}

	if !contains(resp.Stdout, "script output") {
		t.Errorf("expected 'script output' in stdout, got %q", resp.Stdout)
	}
}

func TestConcurrentLimit(t *testing.T) {
	logger := zap.NewNop()
	tmpdir := t.TempDir()

	sessConfig := &config.SessionConfig{
		SessionID:     "test",
		WorkspaceRoot: tmpdir,
		HomeDir:       tmpdir,
	}

	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: tmpdir},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: tmpdir,
			AllowRead:     []string{tmpdir + "/**"},
			AllowWrite:    []string{tmpdir + "/**"},
			Deny:          []string{},
		},
		Exec: policy.ExecPolicy{
			Default: "deny",
			Allow: map[string]policy.CommandPolicy{
				"sleep": {},
			},
		},
		Env:    policy.EnvironmentPolicy{},
		Kernel: policy.KernelPolicy{},
		Audit:  policy.AuditPolicy{},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger, sessConfig, 1, nil) // Max 1 concurrent

	// Start one execution (would block on sleep)
	// For now, just verify the limit is enforced
	// TODO: implement async test using goroutines

	_ = tools.acquireConcurrentSlot()
	defer tools.releaseConcurrentSlot()

	// Should fail to acquire second slot
	if tools.acquireConcurrentSlot() {
		t.Error("expected second slot acquisition to fail")
		tools.releaseConcurrentSlot()
	}
}

// Helper functions

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr) >= 0)
}

func findSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
