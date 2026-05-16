package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artbegolli/anvilmcp/internal/policy"
	"go.uber.org/zap"
)

func TestResolvePath(t *testing.T) {
	tmpdir := t.TempDir()
	workspace := filepath.Join(tmpdir, "workspace")

	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: workspace},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: workspace,
			AllowRead:     []string{workspace + "/**"},
			AllowWrite:    []string{workspace + "/**"},
			Deny: []string{
				workspace + "/.git/**",
				workspace + "/**/.secret",
			},
		},
		Exec:   policy.ExecPolicy{Default: "deny"},
		Env:    policy.EnvironmentPolicy{},
		Kernel: policy.KernelPolicy{},
		Audit:  policy.AuditPolicy{},
	}

	engine := policy.NewEngine(p, "")

	tests := []struct {
		name     string
		input    string
		mode     string
		allowed  bool
		contains string // substring that should be in absPath
	}{
		{
			name:     "relative path resolves against cwd",
			input:    "file.txt",
			mode:     "read",
			allowed:  true,
			contains: "workspace",
		},
		{
			name:     "absolute path inside workspace",
			input:    filepath.Join(workspace, "file.txt"),
			mode:     "read",
			allowed:  true,
			contains: "workspace",
		},
		{
			name:     "denied path: .git",
			input:    filepath.Join(workspace, ".git", "config"),
			mode:     "read",
			allowed:  false,
			contains: "",
		},
		{
			name:     "denied path: .secret file",
			input:    filepath.Join(workspace, "dir", ".secret"),
			mode:     "read",
			allowed:  false,
			contains: "",
		},
		{
			name:     "path outside workspace",
			input:    "/etc/passwd",
			mode:     "read",
			allowed:  false,
			contains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := engine.ResolvePath(tt.input, tt.mode)
			if decision.Allowed != tt.allowed {
				t.Errorf("expected allowed=%v, got %v (reason: %s, rule: %s)", tt.allowed, decision.Allowed, decision.Reason, decision.PolicyRule)
			}
			if tt.allowed && tt.contains != "" {
				if !containsSubpath(decision.AbsPath, tt.contains) {
					t.Errorf("expected absPath to contain %q, got %q", tt.contains, decision.AbsPath)
				}
			}
		})
	}
}

func TestListRead(t *testing.T) {
	tmpdir := t.TempDir()
	logger := zap.NewNop()

	// Create test structure
	workspace := filepath.Join(tmpdir, "workspace")
	os.MkdirAll(filepath.Join(workspace, "subdir"), 0755)
	os.WriteFile(filepath.Join(workspace, "file1.txt"), []byte("content"), 0644)
	os.WriteFile(filepath.Join(workspace, "subdir", "file2.txt"), []byte("content2"), 0644)

	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: workspace},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: workspace,
			AllowRead:     []string{workspace + "/**", workspace},
			AllowWrite:    []string{workspace + "/**", workspace},
			Deny:          []string{},
		},
		Exec:   policy.ExecPolicy{Default: "deny"},
		Env:    policy.EnvironmentPolicy{},
		Kernel: policy.KernelPolicy{},
		Audit:  policy.AuditPolicy{},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger)
	ctx := context.Background()

	// Test list - use absolute path to workspace
	listResp, err := tools.HandleList(ctx, &ListRequest{Path: workspace})
	if err != nil {
		t.Fatalf("HandleList failed: %v", err)
	}

	if len(listResp.Entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(listResp.Entries))
	}

	// Test read
	readResp, err := tools.HandleRead(ctx, &ReadRequest{Path: filepath.Join(workspace, "file1.txt")})
	if err != nil {
		t.Fatalf("HandleRead failed: %v", err)
	}

	if readResp.Content != "content" {
		t.Errorf("expected content 'content', got %q", readResp.Content)
	}

	if readResp.Truncated {
		t.Error("expected truncated=false")
	}
}

func TestWriteProtection(t *testing.T) {
	tmpdir := t.TempDir()
	logger := zap.NewNop()

	workspace := filepath.Join(tmpdir, "workspace")
	dir := filepath.Join(workspace, "dir")
	os.MkdirAll(workspace, 0755)

	// Policy that allows write to workspace and subdirs, but denies .secret files
	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: workspace},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: workspace,
			AllowRead:     []string{workspace + "/**", workspace},
			AllowWrite:    []string{workspace + "/**", workspace},
			Deny: []string{
				workspace + "/**/.secret",
			},
		},
		Exec:   policy.ExecPolicy{Default: "deny"},
		Env:    policy.EnvironmentPolicy{},
		Kernel: policy.KernelPolicy{},
		Audit:  policy.AuditPolicy{},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger)
	ctx := context.Background()

	// Attempt to write a .secret file (should be denied by deny list)
	_, err := tools.HandleWrite(ctx, &WriteRequest{
		Path:    filepath.Join(dir, ".secret"),
		Content: "secret",
		Create:  ptrBool(true),
	})

	if err == nil {
		t.Error("expected write to .secret file to be denied")
	}

	// Write an allowed file in subdirectory (should succeed)
	resp, err := tools.HandleWrite(ctx, &WriteRequest{
		Path:    filepath.Join(workspace, "allowed.txt"),
		Content: "allowed content",
		Create:  ptrBool(true),
	})

	if err != nil {
		t.Errorf("expected write to succeed, got error: %v", err)
	}

	if resp == nil {
		t.Fatal("expected resp to not be nil")
	}

	if resp.BytesWritten != int64(len("allowed content")) {
		t.Errorf("expected bytes_written=%d, got %d", len("allowed content"), resp.BytesWritten)
	}
}

func TestSymlinkResolution(t *testing.T) {
	tmpdir := t.TempDir()
	logger := zap.NewNop()

	workspace := filepath.Join(tmpdir, "workspace")
	secret := filepath.Join(tmpdir, "secret")
	os.MkdirAll(workspace, 0755)
	os.MkdirAll(secret, 0755)

	// Create a file outside the workspace
	os.WriteFile(filepath.Join(secret, "sensitive.txt"), []byte("sensitive"), 0644)

	// Create a symlink inside the workspace pointing outside
	linkPath := filepath.Join(workspace, "link-to-secret")
	os.Symlink(filepath.Join(secret, "sensitive.txt"), linkPath)

	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: workspace},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: workspace,
			AllowRead:     []string{workspace + "/**"},
			AllowWrite:    []string{workspace + "/**"},
			Deny:          []string{secret + "/**"},
		},
		Exec:   policy.ExecPolicy{Default: "deny"},
		Env:    policy.EnvironmentPolicy{},
		Kernel: policy.KernelPolicy{},
		Audit:  policy.AuditPolicy{},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger)
	ctx := context.Background()

	// Attempt to read the symlink (should fail because it resolves to outside workspace)
	_, err := tools.HandleRead(ctx, &ReadRequest{Path: "link-to-secret"})

	if err == nil {
		t.Error("expected symlink escape to be blocked")
	}
}

func TestDeleteProtection(t *testing.T) {
	tmpdir := t.TempDir()
	logger := zap.NewNop()

	workspace := filepath.Join(tmpdir, "workspace")
	os.MkdirAll(workspace, 0755)
	os.WriteFile(filepath.Join(workspace, "deleteme.txt"), []byte("content"), 0644)

	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: workspace},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: workspace,
			AllowRead:     []string{workspace + "/**"},
			AllowWrite:    []string{workspace + "/**"},
			Deny:          []string{},
		},
		Exec:   policy.ExecPolicy{Default: "deny"},
		Env:    policy.EnvironmentPolicy{},
		Kernel: policy.KernelPolicy{},
		Audit:  policy.AuditPolicy{},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger)
	ctx := context.Background()

	// Delete the file
	resp, err := tools.HandleDelete(ctx, &DeleteRequest{Path: "deleteme.txt"})
	if err != nil {
		t.Fatalf("expected delete to succeed, got error: %v", err)
	}

	if resp == nil {
		t.Fatal("expected resp to not be nil")
	}

	if resp.Deleted != 1 {
		t.Errorf("expected deleted=1, got %d", resp.Deleted)
	}

	// Verify file is gone
	if _, err := os.Stat(filepath.Join(workspace, "deleteme.txt")); err == nil {
		t.Error("expected file to be deleted")
	}
}

func TestListLimitValidation(t *testing.T) {
	tmpdir := t.TempDir()
	logger := zap.NewNop()

	workspace := filepath.Join(tmpdir, "workspace")
	os.MkdirAll(workspace, 0755)

	// Create a file to list
	os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("content"), 0644)

	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: workspace},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: workspace,
			AllowRead:     []string{workspace + "/**", workspace},
			AllowWrite:    []string{workspace + "/**", workspace},
			Deny:          []string{},
		},
		Exec:   policy.ExecPolicy{Default: "deny"},
		Env:    policy.EnvironmentPolicy{},
		Kernel: policy.KernelPolicy{},
		Audit:  policy.AuditPolicy{},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger)
	ctx := context.Background()

	// Test that excessive limit is rejected
	largeLimit := 150000
	_, err := tools.HandleList(ctx, &ListRequest{
		Path:  workspace,
		Limit: &largeLimit,
	})

	if err == nil {
		t.Error("expected limit validation to reject excessive limit")
	}
	if err != nil && !strings.Contains(err.Error(), "limit too large") {
		t.Errorf("expected 'limit too large' error, got: %v", err)
	}

	// Test that reasonable limit works
	reasonableLimit := 100
	_, err = tools.HandleList(ctx, &ListRequest{
		Path:  workspace,
		Limit: &reasonableLimit,
	})

	if err != nil {
		t.Errorf("expected list with reasonable limit to succeed, got error: %v", err)
	}
}

// Helper functions

func ptrBool(b bool) *bool {
	return &b
}

func ptrInt(i int) *int {
	return &i
}

func ptrString(s string) *string {
	return &s
}

// containsSubpath checks if path contains subpath as a substring
func containsSubpath(path, subpath string) bool {
	// Normalize paths
	path = filepath.Clean(path)
	return len(path) >= len(subpath) && (path == subpath || len(path) > len(subpath) && path[:len(subpath)] == subpath)
}
