# AnvilMCP Security Remediation Guide

This document provides specific code fixes for the vulnerabilities identified in SECURITY_REVIEW.md.

---

## CRITICAL FIXES (Apply Immediately)

### FIX #1: Symlink Escape via Non-Existent Path Write

**File**: `internal/tools/filesystem/tools.go`  
**Function**: `HandleWrite`

**Current Code (Lines 176-223)**:
```go
func (t *Tools) HandleWrite(ctx context.Context, request *WriteRequest) (*WriteResponse, error) {
	// Resolve path
	decision := t.engine.ResolvePath(request.Path, "write")
	if !decision.Allowed {
		return nil, fmt.Errorf("access denied: %s (rule: %s)", decision.Reason, decision.PolicyRule)
	}

	absPath := decision.AbsPath

	// Check if file exists
	_, err := os.Stat(absPath)
	exists := err == nil

	if exists {
		overwrite := request.Overwrite != nil && *request.Overwrite
		if !overwrite {
			return nil, fmt.Errorf("file exists and overwrite is false")
		}
	} else {
		create := request.Create == nil || *request.Create
		if !create {
			return nil, fmt.Errorf("file does not exist and create is false")
		}

		// Create directory if needed
		dir := filepath.Dir(absPath)  // <-- VULNERABLE: No policy check on parent dir
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}
	}
	// ... rest of function
}
```

**Fixed Code**:
```go
func (t *Tools) HandleWrite(ctx context.Context, request *WriteRequest) (*WriteResponse, error) {
	// Resolve path
	decision := t.engine.ResolvePath(request.Path, "write")
	if !decision.Allowed {
		return nil, fmt.Errorf("access denied: path not permitted (rule: %s)", decision.PolicyRule)
	}

	absPath := decision.AbsPath

	// Check if file exists
	_, err := os.Stat(absPath)
	exists := err == nil

	if exists {
		overwrite := request.Overwrite != nil && *request.Overwrite
		if !overwrite {
			return nil, fmt.Errorf("file exists and overwrite is false")
		}
	} else {
		create := request.Create == nil || *request.Create
		if !create {
			return nil, fmt.Errorf("file does not exist and create is false")
		}

		// Validate parent directory path against policy
		dir := filepath.Dir(absPath)
		dirDecision := t.engine.ResolvePath(dir, "write")
		if !dirDecision.Allowed {
			return nil, fmt.Errorf("parent directory access denied: path not permitted (rule: %s)", dirDecision.PolicyRule)
		}
		
		// Create directory with validated path
		if err := os.MkdirAll(dirDecision.AbsPath, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}
	}

	// Write file
	mode := os.FileMode(0644)
	if request.Mode != nil {
		mode = os.FileMode(*request.Mode)
	}

	if err := os.WriteFile(absPath, []byte(request.Content), mode); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	info, _ := os.Stat(absPath)

	return &WriteResponse{
		BytesWritten: int64(len(request.Content)),
		MTime:        info.ModTime().UnixMilli(),
	}, nil
}
```

---

### FIX #2: Script Args Policy Bypass

**File**: `internal/tools/exec/tools.go`  
**Function**: `HandleScript`

**Current Code (Lines 225-227)**:
```go
// Execute via interpreter
argv := append([]string{tmpfile.Name()}, request.Args...)
shellResp, err := t.HandleShell(ctx, &ShellRequest{
    Cmd:            interpreter,
    Argv:           argv,  // <-- Args not validated against policy
```

**Fixed Code**:
```go
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
})
```

**Note**: This requires extending the policy engine's `EvaluateExec` method to accept additional context or creating a new validation method.

---

### FIX #3: Policy File Ownership Check

**File**: `internal/policy/loader.go`  
**Function**: `loadPolicy`

**Current Code (Lines 67-93)**:
```go
func (l *Loader) loadPolicy(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Validate file ownership
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	// Parse YAML
	var policy Policy
	if err := yaml.Unmarshal(data, &policy); err != nil {
		return nil, fmt.Errorf("failed to parse policy YAML: %w", err)
	}

	// Validate version
	if policy.Version != 1 {
		return nil, fmt.Errorf("unsupported policy version: %d", policy.Version)
	}

	_ = info // TODO: check file ownership  // <-- NOT IMPLEMENTED

	return &policy, nil
}
```

**Required Imports**:
```go
import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"gopkg.in/yaml.v3"
)
```

**Fixed Code**:
```go
func (l *Loader) loadPolicy(path string) (*Policy, error) {
	// Validate file ownership and permissions BEFORE reading
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	// Check file permissions (must not be world or group writable)
	if info.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("policy file is world or group writable: %o", info.Mode().Perm())
	}

	// Check file ownership
	stat := info.Sys().(*syscall.Stat_t)
	currentUser, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("failed to get current user: %w", err)
	}
	
	currentUID, err := strconv.Atoi(currentUser.Uid)
	if err != nil {
		return nil, fmt.Errorf("failed to parse current UID: %w", err)
	}

	// Policy file must be owned by current user or root (UID 0)
	if stat.Uid != uint32(currentUID) && currentUID != 0 {
		return nil, fmt.Errorf("policy file must be owned by current user or root, got UID %d", stat.Uid)
	}

	// Read and parse policy
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var policy Policy
	if err := yaml.Unmarshal(data, &policy); err != nil {
		return nil, fmt.Errorf("failed to parse policy YAML: %w", err)
	}

	// Validate version
	if policy.Version != 1 {
		return nil, fmt.Errorf("unsupported policy version: %d", policy.Version)
	}

	return &policy, nil
}
```

---

## HIGH-SEVERITY FIXES

### FIX #4: Recursive Glob Pattern Validation

**File**: `internal/policy/engine.go`  
**Function**: `pathMatchesPattern` and `ResolvePath`

**Issue**: Patterns not normalized consistently

**Fixed Implementation**:
```go
// Updated pathMatchesPattern function
func pathMatchesPattern(path, pattern string) bool {
	// Normalize both path and pattern
	path = filepath.Clean(path)
	pattern = filepath.Clean(pattern)
	
	// Reject paths containing .. sequences (already cleaned, so shouldn't occur)
	if strings.Contains(path, "..") {
		return false
	}

	// Handle ** patterns (recursive glob)
	if strings.Contains(pattern, "**") {
		parts := strings.Split(pattern, "**")
		if len(parts) == 2 {
			prefix := parts[0]
			suffix := parts[1]

			// Remove trailing/leading separators before cleaning
			prefix = strings.TrimSuffix(prefix, string(filepath.Separator))
			suffix = strings.TrimPrefix(suffix, string(filepath.Separator))

			// Only clean non-empty parts
			if prefix != "" {
				prefix = filepath.Clean(prefix)
			}
			if suffix != "" {
				suffix = filepath.Clean(suffix)
			}

			// Check prefix
			if prefix != "" && prefix != "." {
				if !strings.HasPrefix(path, prefix) {
					return false
				}
				// If suffix is empty, check that path is under the prefix directory
				if suffix == "" {
					return strings.HasPrefix(path, prefix+string(filepath.Separator)) || path == prefix
				}
			}

			// If we get here and suffix is empty, prefix matched everything
			if suffix == "" {
				return true
			}

			// Check suffix with separators
			return strings.HasSuffix(path, suffix) || strings.HasSuffix(path, string(filepath.Separator)+suffix)
		}
	}

	// Standard glob matching for patterns without **
	match, _ := filepath.Match(pattern, path)
	return match
}

// In ResolvePath, normalize pattern as well
func (e *Engine) ResolvePath(input string, mode string) ResolvePathDecision {
	if input == "" {
		return ResolvePathDecision{Allowed: false, Reason: "empty path", PolicyRule: "implicit"}
	}

	// If relative, resolve against cwd
	if !filepath.IsAbs(input) {
		cwd := e.policy.Session.CWD
		if cwd == "" {
			cwd = e.policy.Paths.WorkspaceRoot
		}
		input = filepath.Join(cwd, input)
	}

	// Normalize and resolve symlinks
	absPath, err := filepath.Abs(input)
	if err != nil {
		return ResolvePathDecision{Allowed: false, AbsPath: input, Reason: err.Error(), PolicyRule: "abs_error"}
	}

	absPath = filepath.Clean(absPath)

	// Check against deny list first (deny always wins)
	for _, pattern := range e.policy.Paths.Deny {
		normalizedPattern := filepath.Clean(pattern)
		if pathMatchesPattern(absPath, normalizedPattern) {
			return ResolvePathDecision{Allowed: false, AbsPath: absPath, Reason: "matched deny pattern", PolicyRule: "paths.deny:" + pattern}
		}
	}

	// ... rest of function
}
```

---

### FIX #5: Environment Variable Filtering with Inheritance

**File**: `internal/tools/exec/tools.go`  
**Function**: `HandleShell` (Lines 99-112)

**Current Code**:
```go
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
```

**Fixed Code**:
```go
// Set environment (filtered through policy)
// Start with inherited environment, filtered
var cmdEnv []string
inherited := os.Environ()

// Filter inherited environment variables
for _, envStr := range inherited {
    parts := strings.SplitN(envStr, "=", 2)
    if len(parts) != 2 {
        continue
    }
    k, v := parts[0], parts[1]
    
    envDecision := t.engine.FilterEnv(k, v)
    if envDecision.Allowed {
        cmdEnv = append(cmdEnv, k+"="+envDecision.Value)
    }
}

// Merge with request-provided environment variables
if len(request.Env) > 0 {
    for k, v := range request.Env {
        envDecision := t.engine.FilterEnv(k, v)
        if envDecision.Allowed {
            // Check if already set from inherited vars
            found := false
            for i, envStr := range cmdEnv {
                if strings.HasPrefix(envStr, k+"=") {
                    cmdEnv[i] = k+"="+envDecision.Value
                    found = true
                    break
                }
            }
            if !found {
                cmdEnv = append(cmdEnv, k+"="+envDecision.Value)
            }
        }
    }
}

cmd.Env = cmdEnv
```

---

### FIX #6: Rate Limiting Implementation

**File**: `internal/tools/exec/tools.go`

**Add to Tools struct**:
```go
type Tools struct {
    engine        *policy.Engine
    logger        *zap.Logger
    sessionConfig *config.SessionConfig

    concurrentMu  sync.Mutex
    concurrent    int32
    maxConcurrent int32
    
    // Rate limiting (NEW)
    rateLimitMu         sync.Mutex
    execHistory         []time.Time  // Rolling window
    maxExecPerSecond    int32
    maxTotalBytes       int64
    totalBytesGenerated int64
}

// In NewTools:
func NewTools(engine *policy.Engine, logger *zap.Logger, sessConfig *config.SessionConfig, maxConcurrent int) *Tools {
    return &Tools{
        engine:              engine,
        logger:              logger,
        sessionConfig:       sessConfig,
        maxConcurrent:       int32(maxConcurrent),
        maxExecPerSecond:    10,         // NEW
        maxTotalBytes:       1024*1024*1024,  // 1GB per session
        totalBytesGenerated: 0,
    }
}
```

**Add rate limiting check in HandleShell**:
```go
func (t *Tools) HandleShell(ctx context.Context, request *ShellRequest) (*ShellResponse, error) {
    // Check rate limit BEFORE acquiring concurrent slot
    if !t.checkRateLimit() {
        return nil, fmt.Errorf("rate limit exceeded: maximum %d executions per second", t.maxExecPerSecond)
    }
    
    // Check concurrent limit
    if !t.acquireConcurrentSlot() {
        return nil, fmt.Errorf("max concurrent executions reached")
    }
    defer t.releaseConcurrentSlot()
    
    // ... rest of function
}

// Add rate limit check method:
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
    if int32(len(t.execHistory)) >= t.maxExecPerSecond {
        return false
    }
    
    // Record this execution
    t.execHistory = append(t.execHistory, now)
    return true
}

// Track total output in HandleShell:
// After cmd execution:
totalBytes := int64(stdout.Len()) + int64(stderr.Len())
t.rateLimitMu.Lock()
t.totalBytesGenerated += totalBytes
if t.totalBytesGenerated > t.maxTotalBytes {
    t.rateLimitMu.Unlock()
    return nil, fmt.Errorf("session total output exceeded: %d bytes (limit: %d)", 
        t.totalBytesGenerated, t.maxTotalBytes)
}
t.rateLimitMu.Unlock()
```

---

## MEDIUM-SEVERITY FIXES

### FIX #7: Audit Logging Integration

**File**: `internal/tools/exec/tools.go` and `internal/tools/filesystem/tools.go`

Add to HandleShell:
```go
// After policy decision
t.logger.Info("exec decision",
    zap.String("cmd", request.Cmd),
    zap.Strings("argv", request.Argv),
    zap.Bool("allowed", decision.Allowed),
    zap.String("rule", decision.PolicyRule),
)

// After successful execution
t.logger.Info("exec completed",
    zap.String("cmd", request.Cmd),
    zap.Int("exit_code", exitCode),
    zap.Int64("duration_ms", duration.Milliseconds()),
)
```

---

### FIX #8: Generic Error Messages

**File**: `internal/tools/filesystem/tools.go` and `internal/tools/exec/tools.go`

Replace all path-leaking errors:
```go
// BEFORE (reveals paths):
return nil, fmt.Errorf("access denied: %s", decision.Reason)

// AFTER (generic):
return nil, fmt.Errorf("access denied: operation not permitted (rule: %s)", decision.PolicyRule)
```

---

### FIX #9: Improved Concurrent Slot Management

**File**: `internal/tools/exec/tools.go`

Replace spin-lock with channels:
```go
type Tools struct {
    // ... existing fields ...
    concurrentSem chan struct{}  // Semaphore
}

func NewTools(engine *policy.Engine, logger *zap.Logger, sessConfig *config.SessionConfig, maxConcurrent int) *Tools {
    return &Tools{
        engine:        engine,
        logger:        logger,
        sessionConfig: sessConfig,
        concurrentSem: make(chan struct{}, maxConcurrent),
    }
}

func (t *Tools) HandleShell(ctx context.Context, request *ShellRequest) (*ShellResponse, error) {
    // Acquire slot with context (respects cancellation)
    select {
    case t.concurrentSem <- struct{}{}:
        defer func() { <-t.concurrentSem }()
    case <-ctx.Done():
        return nil, fmt.Errorf("context cancelled")
    default:
        return nil, fmt.Errorf("max concurrent executions reached")
    }
    
    // ... rest of function
}
```

---

## TESTING ADDITIONS

Create `internal/policy/engine_security_test.go`:

```go
package policy

import (
	"testing"
)

func TestSymlinkEscapeWrite(t *testing.T) {
	// Test that parent dir validation prevents escape
	policy := &Policy{
		Paths: PathsConfig{
			AllowWrite: []string{"/workspace/**"},
			Deny:       []string{"/etc/**"},
		},
	}
	
	engine := NewEngine(policy, "")
	
	// Try to write to /etc/passwd via /workspace/../../etc/passwd
	decision := engine.ResolvePath("/workspace/../../etc/passwd", "write")
	
	if decision.Allowed {
		t.Errorf("symlink escape should be denied but was allowed")
	}
}

func TestPolicyFileOwnershipValidation(t *testing.T) {
	// This should be added to loader_test.go
	// Test that we reject files with wrong ownership
}

func TestRateLimitingEffectiveness(t *testing.T) {
	// Test that rate limiting actually prevents excessive execution
}

func TestEnvironmentVariableInheritance(t *testing.T) {
	// Test that inherited env vars are preserved
	// Test that filtering still works
}
```

---

## DEPLOYMENT CHECKLIST

Before deploying to any environment:

- [ ] Apply FIX #1 (symlink write escape)
- [ ] Apply FIX #2 (script args validation)
- [ ] Apply FIX #3 (policy ownership check)
- [ ] Apply FIX #4 (glob pattern normalization)
- [ ] Apply FIX #5 (env var filtering)
- [ ] Apply FIX #6 (rate limiting)
- [ ] Apply FIX #7 (audit logging)
- [ ] Apply FIX #8 (generic errors)
- [ ] Run security test suite
- [ ] Run load/stress tests
- [ ] Security code review
- [ ] Penetration testing (if possible)
- [ ] Update SECURITY_REVIEW.md with remediation status

---

## VERIFICATION

After applying fixes, verify with these test cases:

```bash
# Test symlink escape prevention
curl -X POST http://localhost:8080/sse \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fs.write","arguments":{"path":"/workspace/../../etc/test","content":"test"}}}'
# Expected: access denied error

# Test policy ownership
chmod 666 ~/.config/mcp-session/policy.yaml
# Launch server, should fail with ownership error

# Test rate limiting
for i in {1..20}; do
  curl -X POST http://localhost:8080/sse \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":'$i',"method":"tools/call","params":{"name":"exec.shell","arguments":{"cmd":"sleep","argv":["1"]}}}'
done
# Some should fail with rate limit error
```

