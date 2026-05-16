package exec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/artbegolli/anvilmcp/internal/config"
	"github.com/artbegolli/anvilmcp/internal/policy"
	"go.uber.org/zap"
)

// Tools implements execution MCP tools.
type Tools struct {
	engine        *policy.Engine
	logger        *zap.Logger
	sessionConfig *config.SessionConfig

	concurrentMu sync.Mutex
	concurrent   int32
	maxConcurrent int32
}

// NewTools creates a new execution tools handler.
func NewTools(engine *policy.Engine, logger *zap.Logger, sessConfig *config.SessionConfig, maxConcurrent int) *Tools {
	return &Tools{
		engine:        engine,
		logger:        logger,
		sessionConfig: sessConfig,
		maxConcurrent: int32(maxConcurrent),
	}
}

// ShellRequest is the schema for exec.shell.
type ShellRequest struct {
	Cmd             string   `json:"cmd"`
	Argv            []string `json:"argv"`
	CWD             *string  `json:"cwd,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	TimeoutSeconds  *int     `json:"timeout_seconds,omitempty"`
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
	// Check concurrent limit
	if !t.acquireConcurrentSlot() {
		return nil, fmt.Errorf("max concurrent executions reached")
	}
	defer t.releaseConcurrentSlot()

	// Validate command
	if request.Cmd == "" {
		return nil, fmt.Errorf("cmd is required")
	}

	// Evaluate execution policy
	decision := t.engine.EvaluateExec(request.Cmd, request.Argv)
	if !decision.Allowed {
		return nil, fmt.Errorf("execution denied: %s (rule: %s)", decision.Reason, decision.PolicyRule)
	}

	if decision.RequiresConfirmation {
		return nil, fmt.Errorf("execution requires confirmation (rule: %s)", decision.PolicyRule)
	}

	// Resolve working directory
	cwd := t.sessionConfig.WorkspaceRoot
	if request.CWD != nil {
		pathDecision := t.engine.ResolvePath(*request.CWD, "read")
		if !pathDecision.Allowed {
			return nil, fmt.Errorf("cwd access denied: %s", pathDecision.Reason)
		}
		cwd = pathDecision.AbsPath
	}

	// Build command
	cmd := exec.CommandContext(ctx, request.Cmd, request.Argv...)
	cmd.Dir = cwd

	// Set environment (filtered through policy)
	cmdEnv := os.Environ()
	if len(request.Env) > 0 {
		// Start with the provided env vars, validate each
		filtered := []string{}
		for k, v := range request.Env {
			envDecision := t.engine.FilterEnv(k, v)
			if envDecision.Allowed {
				filtered = append(filtered, k+"="+envDecision.Value)
			}
		}
		cmdEnv = filtered
	}
	cmd.Env = cmdEnv

	// Setup output capture
	var stdout, stderr bytes.Buffer
	const maxOutputBytes = 10 * 1024 * 1024 // 10 MB

	stdoutLimited := &limitedWriter{dst: &stdout, limit: int64(maxOutputBytes)}
	stderrLimited := &limitedWriter{dst: &stderr, limit: int64(maxOutputBytes)}

	cmd.Stdout = stdoutLimited
	cmd.Stderr = stderrLimited
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
	ext := ".sh"
	interpreter := "/bin/sh"
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
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.WriteString(request.Content); err != nil {
		tmpfile.Close()
		return nil, fmt.Errorf("failed to write script: %w", err)
	}
	tmpfile.Close()

	// Make executable
	if err := os.Chmod(tmpfile.Name(), 0700); err != nil {
		return nil, fmt.Errorf("failed to chmod script: %w", err)
	}

	// Execute via interpreter
	argv := append([]string{tmpfile.Name()}, request.Args...)
	shellResp, err := t.HandleShell(ctx, &ShellRequest{
		Cmd:            interpreter,
		Argv:           argv,
		CWD:            request.CWD,
		Env:            request.Env,
		TimeoutSeconds: request.TimeoutSeconds,
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
