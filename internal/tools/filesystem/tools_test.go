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
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

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
	subdir := filepath.Join(workspace, "subdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	file1Path := filepath.Join(workspace, "file1.txt")
	file2Path := filepath.Join(subdir, "file2.txt")

	if err := os.WriteFile(file1Path, []byte("content"), 0644); err != nil {
		t.Fatalf("failed to write file1: %v", err)
	}
	if err := os.WriteFile(file2Path, []byte("content2"), 0644); err != nil {
		t.Fatalf("failed to write file2: %v", err)
	}

	// Use pattern that matches workspace root and subdirs
	// The pattern MUST match files under workspace
	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: workspace},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: workspace,
			AllowRead:     []string{workspace + "/**", workspace},  // Also add workspace itself
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

	// Test reading a file inside workspace
	readResp, err := tools.HandleRead(ctx, &ReadRequest{Path: file1Path})
	if err != nil {
		t.Fatalf("HandleRead failed: %v", err)
	}

	if readResp.Content != "content" {
		t.Errorf("expected content 'content', got %q", readResp.Content)
	}

	if readResp.Truncated {
		t.Error("expected truncated=false")
	}

	// Test listing a subdirectory
	listResp, err := tools.HandleList(ctx, &ListRequest{Path: subdir})
	if err != nil {
		t.Fatalf("HandleList subdir failed: %v", err)
	}

	if len(listResp.Entries) != 1 {
		t.Errorf("expected 1 entry in subdir, got %d", len(listResp.Entries))
	}
}

func TestWriteProtection(t *testing.T) {
	tmpdir := t.TempDir()
	logger := zap.NewNop()

	workspace := filepath.Join(tmpdir, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	// Create file for testing
	existingFile := filepath.Join(workspace, "existing.txt")
	if err := os.WriteFile(existingFile, []byte("existing"), 0644); err != nil {
		t.Fatalf("failed to write existing file: %v", err)
	}

	// Policy that allows write to workspace and subdirs, but denies .secret files
	// For parent directory validation, the workspace dir itself must be in the allow list
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

	// Test 1: Overwrite an existing file (should succeed)
	resp, err := tools.HandleWrite(ctx, &WriteRequest{
		Path:      existingFile,
		Content:   "updated content",
		Overwrite: ptrBool(true),
	})

	if err != nil {
		t.Errorf("expected overwrite to succeed, got error: %v", err)
	}

	if resp != nil && resp.BytesWritten != int64(len("updated content")) {
		t.Errorf("expected bytes_written=%d, got %d", len("updated content"), resp.BytesWritten)
	}

	// Test 2: Attempt to write a .secret file (should be denied by deny list)
	_, err = tools.HandleWrite(ctx, &WriteRequest{
		Path:    filepath.Join(workspace, ".secret"),
		Content: "secret",
		Create:  ptrBool(true),
	})

	if err == nil {
		t.Error("expected write to .secret file to be denied")
	}
}

func TestSymlinkResolution(t *testing.T) {
	tmpdir := t.TempDir()
	logger := zap.NewNop()

	workspace := filepath.Join(tmpdir, "workspace")
	secret := filepath.Join(tmpdir, "secret")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}
	if err := os.MkdirAll(secret, 0755); err != nil {
		t.Fatalf("failed to create secret: %v", err)
	}

	// Create a file outside the workspace
	if err := os.WriteFile(filepath.Join(secret, "sensitive.txt"), []byte("sensitive"), 0644); err != nil {
		t.Fatalf("failed to write sensitive file: %v", err)
	}

	// Create a symlink inside the workspace pointing outside
	linkPath := filepath.Join(workspace, "link-to-secret")
	if err := os.Symlink(filepath.Join(secret, "sensitive.txt"), linkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

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
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}
	filePath := filepath.Join(workspace, "deleteme.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

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

	// Delete the file using absolute path
	resp, err := tools.HandleDelete(ctx, &DeleteRequest{Path: filePath})
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
	if _, err := os.Stat(filePath); err == nil {
		t.Error("expected file to be deleted")
	}
}

func TestListLimitValidation(t *testing.T) {
	tmpdir := t.TempDir()
	logger := zap.NewNop()

	workspace := filepath.Join(tmpdir, "workspace")
	subdir := filepath.Join(workspace, "subdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	// Create files to list
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatalf("failed to write file.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "file2.txt"), []byte("content"), 0644); err != nil {
		t.Fatalf("failed to write file2.txt: %v", err)
	}

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

	// Test 1: Excessive limit is rejected (SECURITY FIX #6b)
	largeLimit := 150000
	_, err := tools.HandleList(ctx, &ListRequest{
		Path:  subdir,
		Limit: &largeLimit,
	})

	if err == nil {
		t.Error("expected limit validation to reject excessive limit")
	}
	if err != nil && !strings.Contains(err.Error(), "limit too large") {
		t.Errorf("expected 'limit too large' error, got: %v", err)
	}

	// Test 2: Reasonable limit works
	reasonableLimit := 100
	resp, err := tools.HandleList(ctx, &ListRequest{
		Path:  subdir,
		Limit: &reasonableLimit,
	})

	if err != nil {
		t.Errorf("expected list with reasonable limit to succeed, got error: %v", err)
	}

	if resp != nil && len(resp.Entries) != 1 {
		t.Errorf("expected 1 entry in subdir, got %d", len(resp.Entries))
	}
}

// Helper functions

func ptrBool(b bool) *bool {
	return &b
}

// containsSubpath checks if path contains subpath as a substring
func containsSubpath(path, subpath string) bool {
	// Normalize paths
	path = filepath.Clean(path)
	subpath = filepath.Clean(subpath)
	return strings.Contains(path, subpath)
}
