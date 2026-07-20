package exec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/artbegolli/anvilmcp/internal/config"
	"github.com/artbegolli/anvilmcp/internal/metrics"
	"github.com/artbegolli/anvilmcp/internal/policy"
	"go.uber.org/zap"
)

// Tools implements execution MCP tools.
type Tools struct {
	engine        *policy.Engine
	logger        *zap.Logger
	sessionConfig *config.SessionConfig
	metrics       *metrics.Registry

	concurrent    int32
	maxConcurrent int32

	rateLimitMu         sync.Mutex
	execHistory         []time.Time
	maxExecPerSecond    int32
	maxTotalBytes       int64
	totalBytesGenerated int64
}

// NewTools creates a new execution tools handler. m may be nil.
func NewTools(engine *policy.Engine, logger *zap.Logger, sessConfig *config.SessionConfig, maxConcurrent int, m *metrics.Registry) *Tools {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	} else if maxConcurrent > math.MaxInt32 {
		maxConcurrent = math.MaxInt32
	}
	if m == nil {
		m = metrics.NewNoop()
	}
	return &Tools{
		engine:           engine,
		logger:           logger,
		sessionConfig:    sessConfig,
		metrics:          m,
		maxConcurrent:    int32(maxConcurrent),
		maxExecPerSecond: 10,              // Allow 10 executions per second
		maxTotalBytes:    1024 * 1024 * 1024, // 1GB per session
	}
}

// ShellRequest is the schema for exec.shell.
type ShellRequest struct {
	Cmd             string   `json:"cmd"`
	Argv            []string `json:"argv"`
	CWD             *string  `json:"cwd,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	TimeoutSeconds  *int     `json:"timeout_seconds,omitempty"`

	// OnChunk, when non-nil, receives batched stdout/stderr output while the
	// process runs. Set by the server adapter when the MCP client supplied a
	// progressToken; nil means fully buffered behavior. Not part of the wire
	// schema.
	OnChunk ChunkFunc `json:"-"`
}

// ShellResponse is the response from exec.shell.
type ShellResponse struct {
	ExitCode     int    `json:"exit_code"`
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	DurationMS   int64  `json:"duration_ms"`
	Truncated    bool   `json:"truncated"`
	StdoutBytes  int64  `json:"stdout_bytes"`
	StderrBytes  int64  `json:"stderr_bytes"`
}

// HandleShell handles the exec.shell tool.
func (t *Tools) HandleShell(ctx context.Context, request *ShellRequest) (*ShellResponse, error) {
	// Check rate limit BEFORE acquiring concurrent slot
	if !t.checkRateLimit() {
		t.metrics.ExecRateLimited.Inc()
		return nil, fmt.Errorf("rate limit exceeded: maximum %d executions per second", t.maxExecPerSecond)
	}

	// Check concurrent limit
	if !t.acquireConcurrentSlot() {
		return nil, fmt.Errorf("max concurrent executions reached")
	}
	t.metrics.ExecConcurrent.Inc()
	defer func() {
		t.releaseConcurrentSlot()
		t.metrics.ExecConcurrent.Dec()
	}()

	// Refuse upfront if the session output budget is already exhausted.
	// Previously the check only ran after execution, which meant the process
	// ran (and streaming, below, would already have delivered its output)
	// before the budget denial — making the budget an accounting fiction.
	t.rateLimitMu.Lock()
	budgetExhausted := t.totalBytesGenerated >= t.maxTotalBytes
	t.rateLimitMu.Unlock()
	if budgetExhausted {
		return nil, fmt.Errorf("session total output budget exhausted (limit: %d bytes)", t.maxTotalBytes)
	}

	// Validate command
	if request.Cmd == "" {
		return nil, fmt.Errorf("cmd is required")
	}

	// Evaluate execution policy
	decision := t.engine.EvaluateExec(request.Cmd, request.Argv)
	if !decision.Allowed {
		t.metrics.PolicyDecisions.WithLabelValues("exec", "deny").Inc()
		return nil, fmt.Errorf("execution denied: %s (rule: %s)", decision.Reason, decision.PolicyRule)
	}
	t.metrics.PolicyDecisions.WithLabelValues("exec", "allow").Inc()

	if decision.RequiresConfirmation {
		return nil, fmt.Errorf("execution requires confirmation (rule: %s)", decision.PolicyRule)
	}

	// Resolve working directory
	cwd := t.sessionConfig.WorkspaceRoot
	if request.CWD != nil {
		pathDecision := t.engine.ResolvePath(*request.CWD, "read")
		if !pathDecision.Allowed {
			t.metrics.PolicyDecisions.WithLabelValues("path", "deny").Inc()
			return nil, fmt.Errorf("cwd access denied: %s", pathDecision.Reason)
		}
		t.metrics.PolicyDecisions.WithLabelValues("path", "allow").Inc()
		cwd = pathDecision.AbsPath
	}

	// Build command. Cmd and Argv are user-supplied but EvaluateExec above has
	// already gated them against the policy's allow/deny/ask lists; running them
	// is the whole point of this tool.
	cmd := exec.CommandContext(ctx, request.Cmd, request.Argv...) // #nosec G204 -- policy-gated
	cmd.Dir = cwd

	// Build the child env from the parent process env, filtered through policy
	// (so denylisted/redacted vars like AWS_SECRET_ACCESS_KEY never reach the
	// subprocess), then overlay any request-supplied env on top — also filtered.
	// Request-supplied vars override inherited values of the same name; vars
	// rejected by policy are dropped silently rather than failing the call.
	var cmdEnv []string
	var envAllow, envDeny int
	for _, envStr := range os.Environ() {
		parts := strings.SplitN(envStr, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k, v := parts[0], parts[1]
		if envDecision := t.engine.FilterEnv(k, v); envDecision.Allowed {
			cmdEnv = append(cmdEnv, k+"="+envDecision.Value)
			envAllow++
		} else {
			envDeny++
		}
	}

	for k, v := range request.Env {
		envDecision := t.engine.FilterEnv(k, v)
		if !envDecision.Allowed {
			envDeny++
			continue
		}
		envAllow++
		replaced := false
		for i, envStr := range cmdEnv {
			if strings.HasPrefix(envStr, k+"=") {
				cmdEnv[i] = k + "=" + envDecision.Value
				replaced = true
				break
			}
		}
		if !replaced {
			cmdEnv = append(cmdEnv, k+"="+envDecision.Value)
		}
	}
	// Env decisions are batched per-exec to keep the per-var overhead invisible
	// on the hot path but still visible in aggregate.
	if envAllow > 0 {
		t.metrics.PolicyDecisions.WithLabelValues("env", "allow").Add(float64(envAllow))
	}
	if envDeny > 0 {
		t.metrics.PolicyDecisions.WithLabelValues("env", "deny").Add(float64(envDeny))
	}

	cmd.Env = cmdEnv

	// Setup output capture. The limitedWriters remain the source of truth for
	// what's captured; when the caller wants streaming, tee through
	// streamingWriters that batch copies of the same bytes out via OnChunk.
	var stdout, stderr bytes.Buffer
	const maxOutputBytes = 10 * 1024 * 1024 // 10 MB

	stdoutLimited := &limitedWriter{dst: &stdout, limit: int64(maxOutputBytes)}
	stderrLimited := &limitedWriter{dst: &stderr, limit: int64(maxOutputBytes)}

	// streamedCounted is a shadow tally of bytes already streamed this
	// invocation (guarded by rateLimitMu). It is never committed to
	// totalBytesGenerated — the post-run accounting charges the full captured
	// byte count, which includes every streamed byte — but the mid-run budget
	// check below needs it to see in-flight usage before that commit lands.
	var streamedCounted int64
	var stdoutStream, stderrStream *streamingWriter
	if request.OnChunk != nil {
		// Wrap the caller's callback with budget enforcement: each chunk is
		// charged against the session output budget before delivery, and
		// streaming stops (capture continues) once the budget is exhausted.
		// Without this, streamed chunks would bypass the budget entirely —
		// the post-run denial below withholds the response, but streamed
		// bytes would already be on the wire.
		inner := request.OnChunk
		budgeted := func(stream string, data []byte) {
			t.rateLimitMu.Lock()
			if t.totalBytesGenerated+streamedCounted+int64(len(data)) > t.maxTotalBytes {
				t.rateLimitMu.Unlock()
				return
			}
			streamedCounted += int64(len(data))
			t.rateLimitMu.Unlock()
			inner(stream, data)
		}
		stdoutStream = newStreamingWriter(stdoutLimited, "stdout", budgeted)
		stderrStream = newStreamingWriter(stderrLimited, "stderr", budgeted)
		cmd.Stdout = stdoutStream
		cmd.Stderr = stderrStream
	} else {
		cmd.Stdout = stdoutLimited
		cmd.Stderr = stderrLimited
	}
	cmd.Stdin = nil // Never allow interactive input

	// Apply timeout
	timeout := 5 * time.Minute
	if request.TimeoutSeconds != nil && *request.TimeoutSeconds > 0 {
		timeout = time.Duration(*request.TimeoutSeconds) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Execute
	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)
	t.metrics.ExecDuration.Observe(duration.Seconds())

	// Close explicitly (not deferred) so the trailing chunk is delivered
	// while the request is still open, before the final response is built.
	if stdoutStream != nil {
		stdoutStream.Close()
		stderrStream.Close()
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			exitCode = 124 // Standard timeout exit code
		} else {
			exitCode = 1
		}
	}

	// Track total output bytes for session limit
	totalBytes := stdoutLimited.written + stderrLimited.written
	t.metrics.ExecOutputBytes.Add(float64(totalBytes))
	t.rateLimitMu.Lock()
	t.totalBytesGenerated += totalBytes
	if t.totalBytesGenerated > t.maxTotalBytes {
		t.rateLimitMu.Unlock()
		return nil, fmt.Errorf("session total output exceeded: %d bytes (limit: %d)",
			t.totalBytesGenerated, t.maxTotalBytes)
	}
	t.rateLimitMu.Unlock()

	return &ShellResponse{
		ExitCode:    exitCode,
		Stdout:      stdout.String(),
		Stderr:      stderr.String(),
		DurationMS:  duration.Milliseconds(),
		Truncated:   stdoutLimited.truncated || stderrLimited.truncated,
		StdoutBytes: stdoutLimited.written,
		StderrBytes: stderrLimited.written,
	}, nil
}

// ScriptRequest is the schema for exec.script.
type ScriptRequest struct {
	Language       string            `json:"language"`
	Content        string            `json:"content"`
	Args           []string          `json:"args,omitempty"`
	CWD            *string           `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds *int              `json:"timeout_seconds,omitempty"`

	// OnChunk mirrors ShellRequest.OnChunk; passed through to the underlying
	// shell invocation.
	OnChunk ChunkFunc `json:"-"`
}

// ScriptResponse is the response from exec.script.
type ScriptResponse struct {
	ExitCode     int    `json:"exit_code"`
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	DurationMS   int64  `json:"duration_ms"`
	ScriptPath   string `json:"script_path"`
	Truncated    bool   `json:"truncated"`
	StdoutBytes  int64  `json:"stdout_bytes"`
	StderrBytes  int64  `json:"stderr_bytes"`
}

// HandleScript handles the exec.script tool.
func (t *Tools) HandleScript(ctx context.Context, request *ScriptRequest) (*ScriptResponse, error) {
	// Determine script extension based on language
	var ext string
	var interpreter string
	switch strings.ToLower(request.Language) {
	case "python", "python3":
		ext = ".py"
		interpreter = "python3"
	case "bash":
		ext = ".sh"
		interpreter = "/bin/bash"
	case "sh":
		ext = ".sh"
		interpreter = "/bin/sh"
	default:
		return nil, fmt.Errorf("unsupported language: %s", request.Language)
	}

	// Create temporary script file
	tmpdir := filepath.Join(os.TempDir(), "anvilmcp-scripts")
	if err := os.MkdirAll(tmpdir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create script directory: %w", err)
	}

	tmpfile, err := os.CreateTemp(tmpdir, "script-*"+ext)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp script: %w", err)
	}
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	if _, err := tmpfile.WriteString(request.Content); err != nil {
		_ = tmpfile.Close()
		return nil, fmt.Errorf("failed to write script: %w", err)
	}
	if err := tmpfile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close script: %w", err)
	}

	// Script must be executable so the interpreter can run it; gosec flags 0700
	// for being above 0600 but executable temp scripts are the point here.
	if err := os.Chmod(tmpfile.Name(), 0700); err != nil { // #nosec G302 -- temp script needs exec bit
		return nil, fmt.Errorf("failed to chmod script: %w", err)
	}

	// Validate script arguments against exec policy
	for _, arg := range request.Args {
		// Check if argument would be denied by policy for the interpreter
		testDecision := t.engine.EvaluateExec(interpreter, []string{arg})
		if !testDecision.Allowed {
			return nil, fmt.Errorf("script argument denied by policy: %s (rule: %s)",
				testDecision.Reason, testDecision.PolicyRule)
		}
	}

	// Execute via interpreter
	argv := append([]string{tmpfile.Name()}, request.Args...)
	shellResp, err := t.HandleShell(ctx, &ShellRequest{
		Cmd:            interpreter,
		Argv:           argv,
		CWD:            request.CWD,
		Env:            request.Env,
		TimeoutSeconds: request.TimeoutSeconds,
		OnChunk:        request.OnChunk,
	})

	if err != nil {
		return nil, err
	}

	return &ScriptResponse{
		ExitCode:    shellResp.ExitCode,
		Stdout:      shellResp.Stdout,
		Stderr:      shellResp.Stderr,
		DurationMS:  shellResp.DurationMS,
		ScriptPath:  tmpfile.Name(),
		Truncated:   shellResp.Truncated,
		StdoutBytes: shellResp.StdoutBytes,
		StderrBytes: shellResp.StderrBytes,
	}, nil
}

// Helper functions

func (t *Tools) acquireConcurrentSlot() bool {
	for {
		current := atomic.LoadInt32(&t.concurrent)
		if current >= t.maxConcurrent {
			return false
		}
		if atomic.CompareAndSwapInt32(&t.concurrent, current, current+1) {
			return true
		}
	}
}

func (t *Tools) releaseConcurrentSlot() {
	atomic.AddInt32(&t.concurrent, -1)
}

// checkRateLimit enforces per-second execution rate limiting
func (t *Tools) checkRateLimit() bool {
	t.rateLimitMu.Lock()
	defer t.rateLimitMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-1 * time.Second)

	// Prune old events outside the 1-second window
	filtered := []time.Time{}
	for _, tm := range t.execHistory {
		if tm.After(cutoff) {
			filtered = append(filtered, tm)
		}
	}
	t.execHistory = filtered

	// Check if we've exceeded the rate limit
	if len(t.execHistory) >= int(t.maxExecPerSecond) {
		return false
	}

	// Record this execution
	t.execHistory = append(t.execHistory, now)
	return true
}

// limitedWriter caps the amount of data written to avoid memory exhaustion.
type limitedWriter struct {
	dst       io.Writer
	limit     int64
	written   int64
	truncated bool
	mu        sync.Mutex
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()

	if lw.truncated {
		return len(p), nil // Drop subsequent writes silently
	}

	remaining := lw.limit - lw.written
	if remaining <= 0 {
		lw.truncated = true
		return len(p), nil
	}

	toWrite := int64(len(p))
	if toWrite > remaining {
		toWrite = remaining
		lw.truncated = true
	}

	n, err := lw.dst.Write(p[:toWrite])
	lw.written += int64(n)
	return len(p), err
}

// isTruncated reports whether the writer has hit its cap.
func (lw *limitedWriter) isTruncated() bool {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.truncated
}
