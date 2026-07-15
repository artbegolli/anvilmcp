package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStarterConfig writes a minimal but valid policy at path with the
// permissions the loader requires (0700 dir, 0600 file).
func writeStarterConfig(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, defaultConfigYAML, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestPolicyInit(t *testing.T) {
	t.Run("writes to fresh path", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sub", "config.yaml")

		var out bytes.Buffer
		if err := runPolicyInit(&out, path, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		// 0600 is required by the policy loader's permission check.
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("expected 0600, got %o", got)
		}
		if !strings.Contains(out.String(), "Wrote default policy") {
			t.Errorf("expected success message, got %q", out.String())
		}
	})

	t.Run("refuses to overwrite without --force", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		writeStarterConfig(t, path)

		err := runPolicyInit(&bytes.Buffer{}, path, false)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "refusing to overwrite") {
			t.Errorf("expected refusal message, got %q", err.Error())
		}
	})

	t.Run("--force overwrites", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(path, []byte("junk"), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}

		if err := runPolicyInit(&bytes.Buffer{}, path, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		body, _ := os.ReadFile(path)
		if !bytes.Equal(body, defaultConfigYAML) {
			t.Errorf("expected file to be replaced with default; got %d bytes", len(body))
		}
	})
}

func TestPolicyShow(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "config.yaml")
	writeStarterConfig(t, userPath)

	paths := &policyPathFlags{userPath: userPath, clusterPath: filepath.Join(dir, "missing.yaml")}

	t.Run("yaml effective", func(t *testing.T) {
		var out bytes.Buffer
		if err := runPolicyShow(&out, paths, false, false, false); err != nil {
			t.Fatalf("show: %v", err)
		}
		got := out.String()
		if !strings.Contains(got, "version: 1") {
			t.Errorf("expected YAML output with 'version: 1'; got: %s", got)
		}
	})

	t.Run("json effective uses snake_case", func(t *testing.T) {
		var out bytes.Buffer
		if err := runPolicyShow(&out, paths, false, false, true); err != nil {
			t.Fatalf("show json: %v", err)
		}
		got := out.String()
		if !strings.Contains(got, `"workspace_root"`) {
			t.Errorf("expected snake_case JSON tag; got: %s", got)
		}
		if strings.Contains(got, `"WorkspaceRoot"`) {
			t.Errorf("PascalCase leaking through JSON output: %s", got)
		}
	})

	t.Run("--user and --cluster paths return their respective layer", func(t *testing.T) {
		var userOut, clusterOut bytes.Buffer
		if err := runPolicyShow(&userOut, paths, true, false, false); err != nil {
			t.Fatalf("show user: %v", err)
		}
		if err := runPolicyShow(&clusterOut, paths, false, true, false); err != nil {
			t.Fatalf("show cluster: %v", err)
		}
		// Both should be valid YAML. Since the cluster path is missing, cluster
		// falls back to DefaultPolicy, which we can spot via /workspace defaults.
		if !strings.Contains(clusterOut.String(), "/workspace") {
			t.Errorf("expected DefaultPolicy for missing cluster file; got: %s", clusterOut.String())
		}
		if userOut.Len() == 0 {
			t.Errorf("expected non-empty user output")
		}
	})
}

func TestPolicyTest(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "config.yaml")
	writeStarterConfig(t, userPath)
	paths := &policyPathFlags{userPath: userPath, clusterPath: filepath.Join(dir, "missing.yaml")}

	t.Run("read allowed", func(t *testing.T) {
		var out bytes.Buffer
		err := runPolicyTestPath(&out, paths, "read", "/workspace/foo")
		if err != nil {
			t.Errorf("expected allow, got err=%v out=%s", err, out.String())
		}
		if !strings.HasPrefix(out.String(), "ALLOW") {
			t.Errorf("expected ALLOW prefix; got: %s", out.String())
		}
	})

	t.Run("read denied returns errDenied", func(t *testing.T) {
		var out bytes.Buffer
		err := runPolicyTestPath(&out, paths, "read", "/etc/passwd")
		if !errors.Is(err, errDenied) {
			t.Errorf("expected errDenied, got: %v", err)
		}
		if !strings.HasPrefix(out.String(), "DENY") {
			t.Errorf("expected DENY prefix; got: %s", out.String())
		}
	})

	t.Run("exec denied by exec.deny", func(t *testing.T) {
		var out bytes.Buffer
		err := runPolicyTestExec(&out, paths, "rm", []string{"-rf", "/"})
		if !errors.Is(err, errDenied) {
			t.Errorf("expected errDenied for rm; got: %v", err)
		}
	})

	t.Run("exec allowed", func(t *testing.T) {
		var out bytes.Buffer
		err := runPolicyTestExec(&out, paths, "python3", nil)
		if err != nil {
			t.Errorf("expected allow, got err=%v out=%s", err, out.String())
		}
	})
}
