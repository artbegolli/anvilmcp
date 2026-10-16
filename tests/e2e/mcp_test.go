package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func requirePython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
}

func TestHandshakeAndToolList(t *testing.T) {
	c := newClient(t, shared)
	names := c.toolNames(t)

	for _, want := range []string{
		"fs.list", "fs.read", "fs.write", "fs.stat", "fs.delete",
		"exec.shell", "exec.script",
		"policy.show", "policy.test",
	} {
		if !contains(names, want) {
			t.Errorf("tool %q not registered; got %v", want, names)
		}
	}
	// Kernels are opt-in; the shared server runs without --enable-kernel.
	for _, name := range names {
		if strings.HasPrefix(name, "kernel.") {
			t.Errorf("kernel tool %q registered without --enable-kernel", name)
		}
	}
}

func TestFilesystemRoundTrip(t *testing.T) {
	c := newClient(t, shared)
	path := shared.path("roundtrip.txt")
	const content = "hello e2e"

	var write struct {
		BytesWritten int64 `json:"bytes_written"`
	}
	c.mustCallTool(t, "fs.write", map[string]any{
		"path": path, "content": content, "create": true,
	}).decode(t, &write)
	if write.BytesWritten != int64(len(content)) {
		t.Errorf("bytes_written = %d, want %d", write.BytesWritten, len(content))
	}

	var read struct {
		Content   string `json:"content"`
		Size      int64  `json:"size"`
		Truncated bool   `json:"truncated"`
	}
	c.mustCallTool(t, "fs.read", map[string]any{"path": path}).decode(t, &read)
	if read.Content != content {
		t.Errorf("content = %q, want %q", read.Content, content)
	}
	if read.Truncated {
		t.Error("content unexpectedly truncated")
	}

	var stat struct {
		Type string `json:"type"`
		Size int64  `json:"size"`
	}
	c.mustCallTool(t, "fs.stat", map[string]any{"path": path}).decode(t, &stat)
	if stat.Type != "file" || stat.Size != int64(len(content)) {
		t.Errorf("stat = %+v, want file of %d bytes", stat, len(content))
	}

	var list struct {
		Entries []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"entries"`
	}
	c.mustCallTool(t, "fs.list", map[string]any{"path": shared.workspace}).decode(t, &list)
	var found bool
	for _, e := range list.Entries {
		if e.Name == "roundtrip.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("fs.list did not report roundtrip.txt; got %+v", list.Entries)
	}

	var del struct {
		Deleted int `json:"deleted"`
	}
	c.mustCallTool(t, "fs.delete", map[string]any{"path": path}).decode(t, &del)
	if del.Deleted != 1 {
		t.Errorf("deleted = %d, want 1", del.Deleted)
	}
	if tr := c.callTool(t, "fs.stat", map[string]any{"path": path}, nil); !tr.IsError {
		t.Error("expected fs.stat to fail after delete")
	}
}

// TestFilesystemPolicyDenial proves the path chokepoint is live over the wire,
// not just in unit tests.
func TestFilesystemPolicyDenial(t *testing.T) {
	c := newClient(t, shared)

	for _, path := range []string{"/etc/passwd", shared.path("forbidden/secret.txt")} {
		tr := c.callTool(t, "fs.read", map[string]any{"path": path}, nil)
		if !tr.IsError {
			t.Errorf("fs.read %s should have been denied, got: %s", path, tr.text())
			continue
		}
		if !strings.Contains(strings.ToLower(tr.text()), "denied") {
			t.Errorf("fs.read %s error should mention denial, got: %s", path, tr.text())
		}
	}

	tr := c.callTool(t, "fs.write", map[string]any{
		"path": "/etc/anvil-e2e-should-not-exist", "content": "nope", "create": true,
	}, nil)
	if !tr.IsError {
		t.Errorf("fs.write outside the workspace should have been denied, got: %s", tr.text())
	}
}

func TestExecAllowedAndDenied(t *testing.T) {
	c := newClient(t, shared)

	// Seed a file, then read it back through an allowed command.
	path := shared.path("exec-input.txt")
	const content = "exec sees this"
	c.mustCallTool(t, "fs.write", map[string]any{"path": path, "content": content, "create": true})

	var run struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
	}
	c.mustCallTool(t, "exec.shell", map[string]any{
		"cmd": "cat", "argv": []string{path},
	}).decode(t, &run)
	if run.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", run.ExitCode)
	}
	if !strings.Contains(run.Stdout, content) {
		t.Errorf("stdout = %q, want it to contain %q", run.Stdout, content)
	}

	// `rm` is on the default cluster policy's deny list.
	tr := c.callTool(t, "exec.shell", map[string]any{
		"cmd": "rm", "argv": []string{"-rf", shared.workspace},
	}, nil)
	if !tr.IsError {
		t.Fatalf("exec.shell rm should have been denied, got: %s", tr.text())
	}
	if !strings.Contains(strings.ToLower(tr.text()), "denied") {
		t.Errorf("rm denial should say denied, got: %s", tr.text())
	}
}

// TestExecTimeoutReportsExit124 is the end-to-end regression for the exec
// timeout fix: the per-call timeout must actually kill the process and report
// 124 rather than hanging or returning -1.
func TestExecTimeoutReportsExit124(t *testing.T) {
	requirePython(t)
	c := newClient(t, shared)

	start := time.Now()
	var run struct {
		ExitCode int `json:"exit_code"`
	}
	c.mustCallTool(t, "exec.shell", map[string]any{
		"cmd":             "python3",
		"argv":            []string{"-c", "import time; time.sleep(60)"},
		"timeout_seconds": 1,
	}).decode(t, &run)
	elapsed := time.Since(start)

	if elapsed > 20*time.Second {
		t.Fatalf("timeout not enforced: call took %v for a 1s timeout", elapsed)
	}
	if run.ExitCode != 124 {
		t.Errorf("exit_code = %d, want 124 (timeout)", run.ExitCode)
	}
}

// TestExecTimeoutReapsForkedWorkers covers the process-group half of the fix: a
// payload that forks a worker inheriting stdout must not hang the request.
func TestExecTimeoutReapsForkedWorkers(t *testing.T) {
	requirePython(t)
	c := newClient(t, shared)

	code := "import subprocess, sys, time; " +
		"subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(60)']); " +
		"print('spawned', flush=True); time.sleep(60)"

	start := time.Now()
	var run struct {
		ExitCode int `json:"exit_code"`
	}
	c.mustCallTool(t, "exec.shell", map[string]any{
		"cmd":             "python3",
		"argv":            []string{"-c", code},
		"timeout_seconds": 2,
	}).decode(t, &run)
	elapsed := time.Since(start)

	// With the group kill this returns in ~2s. Without it, cmd.Wait blocks on
	// the worker's inherited pipe for the full 60s.
	if elapsed > 25*time.Second {
		t.Fatalf("request hung on a forked worker: %v", elapsed)
	}
	if run.ExitCode != 124 {
		t.Errorf("exit_code = %d, want 124", run.ExitCode)
	}
}

func TestPolicyTools(t *testing.T) {
	c := newClient(t, shared)

	var show struct {
		Version int `json:"version"`
		Paths   struct {
			WorkspaceRoot string   `json:"workspace_root"`
			AllowRead     []string `json:"allow_read"`
		} `json:"paths"`
	}
	c.mustCallTool(t, "policy.show", nil).decode(t, &show)
	if show.Version != 1 {
		t.Errorf("policy version = %d, want 1", show.Version)
	}
	// Also asserts the schema emits snake_case JSON rather than Go field names.
	if show.Paths.WorkspaceRoot != shared.workspace {
		t.Errorf("workspace_root = %q, want %q", show.Paths.WorkspaceRoot, shared.workspace)
	}
	if len(show.Paths.AllowRead) == 0 {
		t.Error("policy.show returned no allow_read rules")
	}

	cases := []struct {
		name    string
		args    map[string]any
		allowed bool
	}{
		{"read inside workspace", map[string]any{"action": "read", "path": shared.path("x.txt")}, true},
		{"read outside workspace", map[string]any{"action": "read", "path": "/etc/passwd"}, false},
		{"write inside workspace", map[string]any{"action": "write", "path": shared.path("y.txt")}, true},
		{"exec allowed cmd", map[string]any{"action": "exec", "cmd": "python3"}, true},
		{"exec denied cmd", map[string]any{"action": "exec", "cmd": "rm"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var res struct {
				Allowed    bool   `json:"allowed"`
				PolicyRule string `json:"policy_rule"`
			}
			c.mustCallTool(t, "policy.test", tc.args).decode(t, &res)
			if res.Allowed != tc.allowed {
				t.Errorf("allowed = %v, want %v (rule %q)", res.Allowed, tc.allowed, res.PolicyRule)
			}
		})
	}
}

func TestResources(t *testing.T) {
	c := newClient(t, shared)

	res, _ := c.rpc(t, "resources/list", map[string]any{})
	var list struct {
		Resources []struct {
			URI string `json:"uri"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		t.Fatalf("decode resources/list: %v", err)
	}
	var hasPolicy bool
	for _, r := range list.Resources {
		if r.URI == "policy://effective" {
			hasPolicy = true
		}
	}
	if !hasPolicy {
		t.Errorf("policy://effective not listed; got %+v", list.Resources)
	}

	readResource := func(uri string) string {
		t.Helper()
		res, _ := c.rpc(t, "resources/read", map[string]any{"uri": uri})
		var out struct {
			Contents []struct {
				Text string `json:"text"`
			} `json:"contents"`
		}
		if err := json.Unmarshal(res, &out); err != nil {
			t.Fatalf("decode resources/read %s: %v", uri, err)
		}
		if len(out.Contents) == 0 {
			t.Fatalf("resources/read %s returned no contents", uri)
		}
		return out.Contents[0].Text
	}

	if body := readResource("policy://effective"); !strings.Contains(body, shared.workspace) {
		t.Errorf("policy resource missing workspace root: %s", body)
	}

	path := shared.path("resource.txt")
	const content = "via resource template"
	c.mustCallTool(t, "fs.write", map[string]any{"path": path, "content": content, "create": true})
	if body := readResource("file://" + path); !strings.Contains(body, content) {
		t.Errorf("file resource = %q, want it to contain %q", body, content)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	c := newClient(t, shared)
	// Generate at least one tool invocation so the counters exist.
	c.mustCallTool(t, "policy.show", nil)

	resp, err := http.Get(shared.metricsURL)
	if err != nil {
		t.Fatalf("GET %s: %v", shared.metricsURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	text := string(body)

	for _, want := range []string{
		"anvilmcp_tool_invocations_total",
		"anvilmcp_tool_duration_seconds",
		"anvilmcp_policy_decisions_total",
		"go_goroutines",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
	if !strings.Contains(text, `anvilmcp_tool_invocations_total{result="success",tool="policy.show"}`) {
		t.Error("metrics missing a success-labeled policy.show invocation")
	}
}

// TestExecStreamingProgressNotifications is the end-to-end regression for
// streamed exec output: supplying a progressToken must produce
// notifications/progress carrying the process output while the call runs.
func TestExecStreamingProgressNotifications(t *testing.T) {
	requirePython(t)
	c := newClient(t, shared)

	const token = "e2e-progress-token"
	code := "import time\nfor i in range(3):\n    print('chunk', i, flush=True)\n    time.sleep(0.25)\n"
	tr := c.callTool(t, "exec.shell", map[string]any{
		"cmd": "python3", "argv": []string{"-c", code},
	}, map[string]any{"progressToken": token})
	if tr.IsError {
		t.Fatalf("exec.shell failed: %s", tr.text())
	}

	var streamed strings.Builder
	var progressCount int
	for _, n := range tr.notifications {
		if n.Method != "notifications/progress" {
			continue
		}
		var p struct {
			ProgressToken string `json:"progressToken"`
			Progress      int64  `json:"progress"`
			Message       string `json:"message"`
			Stream        string `json:"stream"`
		}
		if err := json.Unmarshal(n.Params, &p); err != nil {
			t.Fatalf("decode progress params: %v (raw %s)", err, n.Params)
		}
		if p.ProgressToken != token {
			t.Errorf("progressToken = %q, want %q", p.ProgressToken, token)
		}
		if p.Stream != "stdout" && p.Stream != "stderr" {
			t.Errorf("unexpected stream label %q", p.Stream)
		}
		progressCount++
		streamed.WriteString(p.Message)
	}

	if progressCount == 0 {
		t.Fatal("no notifications/progress received despite supplying a progressToken")
	}
	if !strings.Contains(streamed.String(), "chunk 0") {
		t.Errorf("streamed output missing expected text; got %q", streamed.String())
	}

	// The final result must still carry the complete output.
	var run struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
	}
	tr.decode(t, &run)
	if run.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", run.ExitCode)
	}
	for i := 0; i < 3; i++ {
		if !strings.Contains(run.Stdout, "chunk") {
			t.Errorf("final stdout missing chunks: %q", run.Stdout)
			break
		}
	}
}
