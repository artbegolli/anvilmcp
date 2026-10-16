// Package e2e drives the real anvilmcp binary over its MCP Streamable HTTP
// endpoint and asserts on actual responses.
//
// These tests build the server, start it on an ephemeral port with a scoped
// policy and temp workspace, and speak JSON-RPC to it exactly as a client
// would. They run as part of the normal `go test ./...` CI job (skipped by
// `-short`), so the end-to-end surface cannot silently rot.
package e2e

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	binPath string      // the built server binary, shared by all tests
	shared  *testServer // default server: kernel disabled, metrics enabled
)

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		fmt.Println("e2e: skipped in -short mode")
		os.Exit(0)
	}

	tmp, err := os.MkdirTemp("", "anvilmcp-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: mkdtemp:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	binPath = filepath.Join(tmp, "anvilmcp")
	build := exec.Command("go", "build", "-o", binPath, "github.com/artbegolli/anvilmcp/cmd/anvilmcp")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build failed: %v\n%s\n", err, out)
		os.Exit(1)
	}

	shared, err = newServer(serverOpts{metrics: true})
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: start shared server:", err)
		os.Exit(1)
	}

	code := m.Run()
	shared.Close()
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// server lifecycle

type serverOpts struct {
	enableKernel bool
	metrics      bool
}

type testServer struct {
	cmd        *exec.Cmd
	dir        string
	workspace  string
	mcpURL     string
	metricsURL string
	stderr     *lockedBuffer
}

// startServer boots a server bound to this test's lifetime.
func startServer(t *testing.T, opts serverOpts) *testServer {
	t.Helper()
	s, err := newServer(opts)
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func newServer(opts serverOpts) (*testServer, error) {
	dir, err := os.MkdirTemp("", "anvilmcp-e2e-")
	if err != nil {
		return nil, err
	}
	s := &testServer{dir: dir, workspace: filepath.Join(dir, "workspace"), stderr: &lockedBuffer{}}

	home := filepath.Join(dir, "home")
	for _, d := range []string{s.workspace, home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}

	policyPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(policyPath, []byte(policyYAML(s.workspace, dir)), 0o600); err != nil {
		return nil, err
	}

	port, err := freePort()
	if err != nil {
		return nil, err
	}
	s.mcpURL = fmt.Sprintf("http://127.0.0.1:%s/mcp", port)

	args := []string{
		"serve",
		"--port", ":" + port,
		"--policy", policyPath,
		// Deliberately absent so the loader falls back to the built-in default
		// cluster policy (which is what supplies the exec allow/deny lists).
		"--cluster-policy", filepath.Join(dir, "no-cluster-policy.yaml"),
		"--history-db", filepath.Join(dir, "history.db"),
	}
	if opts.metrics {
		mport, err := freePort()
		if err != nil {
			return nil, err
		}
		s.metricsURL = fmt.Sprintf("http://127.0.0.1:%s/metrics", mport)
		args = append(args, "--metrics-port", ":"+mport)
	} else {
		args = append(args, "--metrics-port", "")
	}
	if opts.enableKernel {
		args = append(args, "--enable-kernel")
	}

	s.cmd = exec.Command(binPath, args...)
	s.cmd.Env = append(os.Environ(),
		"HOME="+home,
		"WORKSPACE_ROOT="+s.workspace,
		"SESSION_ID=e2e",
	)
	s.cmd.Stdout = io.Discard
	s.cmd.Stderr = s.stderr
	if err := s.cmd.Start(); err != nil {
		return nil, err
	}

	if err := waitListening(port, 15*time.Second); err != nil {
		logs := s.stderr.String()
		s.Close()
		return nil, fmt.Errorf("%w (server stderr: %s)", err, strings.TrimSpace(logs))
	}
	return s, nil
}

func (s *testServer) Close() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}

// path returns an absolute path inside this server's workspace.
func (s *testServer) path(name string) string { return filepath.Join(s.workspace, name) }

// policyYAML scopes reads/writes to the temp workspace. Exec allow/deny come
// from the built-in default cluster policy (python3/ls/cat allowed, rm denied),
// because the loader's effective-policy merge takes exec lists from the cluster
// layer.
func policyYAML(workspace, dir string) string {
	return fmt.Sprintf(`version: 1
session:
  cwd: %[1]s
paths:
  workspace_root: %[1]s
  allow_read:
    - %[1]s/**
    - %[1]s
  allow_write:
    - %[1]s/**
    - %[1]s
  deny:
    - %[1]s/forbidden/**
exec:
  default: deny
env:
  redact_patterns:
    - '(?i)secret'
  explicit_deny: []
  allow_only: []
audit:
  history_db_path: %[2]s/history.db
`, workspace, dir)
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = l.Close() }()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}

func waitListening(port string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 500*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("server did not listen on %s within %s", port, timeout)
}

// lockedBuffer is a Writer safe to read while the child process writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
