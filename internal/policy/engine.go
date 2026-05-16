package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Engine enforces policy decisions at three chokepoints: path resolution, exec evaluation, and env filtering.
type Engine struct {
	policy           *Policy
	redactPatterns   []*regexp.Regexp
	policyFilePath   string
	deniedPaths      map[string]bool
}

// NewEngine creates a new policy enforcement engine.
func NewEngine(policy *Policy, policyFilePath string) *Engine {
	engine := &Engine{
		policy:         policy,
		redactPatterns: policy.CompileRedactPatterns(),
		policyFilePath: policyFilePath,
		deniedPaths:    make(map[string]bool),
	}

	// Add policy file itself and related system paths to denied list
	if abs, err := filepath.Abs(policyFilePath); err == nil {
		engine.deniedPaths[abs] = true
	}

	return engine
}

// ResolvePathDecision represents the result of a path resolution.
type ResolvePathDecision struct {
	Allowed      bool
	AbsPath      string
	Reason       string
	PolicyRule   string
}

// ResolvePath is the first chokepoint. Resolves a potentially relative path against session.cwd,
// applies symlink resolution, and checks against deny/allow lists.
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

	// Resolve symlinks
	evalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil && !os.IsNotExist(err) {
		// EvalSymlinks may fail on non-existent paths; that's OK for writes
		if mode == "write" {
			evalPath = absPath
		} else {
			return ResolvePathDecision{Allowed: false, AbsPath: absPath, Reason: err.Error(), PolicyRule: "symlink_error"}
		}
	} else if err == nil {
		absPath = evalPath
	} else if os.IsNotExist(err) {
		// For non-existent files, try to resolve symlinks in the parent directory
		parentPath := filepath.Dir(absPath)
		if parentPath != absPath { // Avoid infinite loop at root
			if evalParent, parentErr := filepath.EvalSymlinks(parentPath); parentErr == nil {
				absPath = filepath.Join(evalParent, filepath.Base(absPath))
			}
		}
	}

	// Check against deny list first (deny always wins)
	for _, pattern := range e.policy.Paths.Deny {
		normalizedPattern := filepath.Clean(pattern)
		// Try resolving symlinks in pattern (handling ** globs)
		if strings.Contains(normalizedPattern, "**") {
			// For glob patterns, only try to eval the prefix
			parts := strings.Split(normalizedPattern, "**")
			prefix := parts[0]
			hadSeparator := strings.HasSuffix(prefix, string(filepath.Separator))
			prefix = strings.TrimSuffix(prefix, string(filepath.Separator))
			if evalPrefix, err := filepath.EvalSymlinks(prefix); err == nil {
				sep := ""
				if hadSeparator || len(parts[1]) > 0 {
					sep = string(filepath.Separator)
				}
				normalizedPattern = evalPrefix + sep + "**" + parts[1]
			}
		} else if evalPattern, err := filepath.EvalSymlinks(normalizedPattern); err == nil {
			normalizedPattern = evalPattern
		}
		if pathMatchesPattern(absPath, normalizedPattern) {
			return ResolvePathDecision{Allowed: false, AbsPath: absPath, Reason: "matched deny pattern", PolicyRule: "paths.deny:" + pattern}
		}
	}

	// Check against hard-coded denied paths (policy file, etc.)
	if e.deniedPaths[absPath] {
		return ResolvePathDecision{Allowed: false, AbsPath: absPath, Reason: "system-protected path", PolicyRule: "system_deny"}
	}

	// Check against allow list
	var allowList []string
	if mode == "write" {
		allowList = e.policy.Paths.AllowWrite
	} else {
		allowList = e.policy.Paths.AllowRead
	}

	for _, pattern := range allowList {
		normalizedPattern := filepath.Clean(pattern)
		// Try resolving symlinks in pattern (handling ** globs)
		if strings.Contains(normalizedPattern, "**") {
			// For glob patterns, only try to eval the prefix
			parts := strings.Split(normalizedPattern, "**")
			prefix := parts[0]
			hadSeparator := strings.HasSuffix(prefix, string(filepath.Separator))
			prefix = strings.TrimSuffix(prefix, string(filepath.Separator))
			if evalPrefix, err := filepath.EvalSymlinks(prefix); err == nil {
				sep := ""
				if hadSeparator || len(parts[1]) > 0 {
					sep = string(filepath.Separator)
				}
				normalizedPattern = evalPrefix + sep + "**" + parts[1]
			}
		} else if evalPattern, err := filepath.EvalSymlinks(normalizedPattern); err == nil {
			normalizedPattern = evalPattern
		}
		if pathMatchesPattern(absPath, normalizedPattern) {
			return ResolvePathDecision{Allowed: true, AbsPath: absPath, PolicyRule: "paths.allow_" + mode}
		}
	}

	return ResolvePathDecision{Allowed: false, AbsPath: absPath, Reason: "not in allow list", PolicyRule: "default_deny"}
}

// ExecDecision represents the result of evaluating a command execution.
type ExecDecision struct {
	Allowed    bool
	Reason     string
	PolicyRule string
	RequiresConfirmation bool
}

// EvaluateExec is the second chokepoint. Evaluates whether a command should be allowed.
func (e *Engine) EvaluateExec(cmd string, argv []string) ExecDecision {
	if cmd == "" {
		return ExecDecision{Allowed: false, Reason: "empty command", PolicyRule: "implicit"}
	}

	// Extract the binary name
	base := filepath.Base(cmd)

	// Check deny list first (deny always wins)
	for _, pattern := range e.policy.Exec.Deny {
		if match, _ := filepath.Match(pattern, base); match {
			return ExecDecision{Allowed: false, Reason: "matched deny pattern", PolicyRule: "exec.deny:" + pattern}
		}
	}

	// Check ask list
	if _, ok := e.policy.Exec.Ask[base]; ok {
		return ExecDecision{
			Allowed:              true,
			PolicyRule:           "exec.ask:" + base,
			RequiresConfirmation: true,
		}
	}

	// Check allow list
	if policy, ok := e.policy.Exec.Allow[base]; ok {
		// Check args_deny
		for _, deniedArg := range policy.ArgsDeny {
			for _, arg := range argv {
				if strings.Contains(arg, deniedArg) {
					return ExecDecision{Allowed: false, Reason: fmt.Sprintf("argument %q denied for %s", deniedArg, base), PolicyRule: "exec.allow[" + base + "].args_deny"}
				}
			}
		}
		return ExecDecision{Allowed: true, PolicyRule: "exec.allow:" + base}
	}

	// Fall back to default
	if e.policy.Exec.Default == "allow" {
		return ExecDecision{Allowed: true, PolicyRule: "exec.default:allow"}
	}

	return ExecDecision{Allowed: false, Reason: "not in allow list and default is deny", PolicyRule: "exec.default:deny"}
}

// FilterEnvDecision represents the result of env variable filtering.
type FilterEnvDecision struct {
	Allowed bool
	Value   string // empty if not allowed or redacted
	Reason  string
}

// FilterEnv is the third chokepoint. Returns whether an environment variable can be exposed.
func (e *Engine) FilterEnv(name, value string) FilterEnvDecision {
	// Check explicit deny
	for _, denied := range e.policy.Env.ExplicitDeny {
		if name == denied {
			return FilterEnvDecision{Allowed: false, Reason: "explicitly denied"}
		}
	}

	// Check allow-only list (whitelist mode)
	if len(e.policy.Env.AllowOnly) > 0 {
		allowed := false
		for _, allowed_name := range e.policy.Env.AllowOnly {
			if name == allowed_name {
				allowed = true
				break
			}
		}
		if !allowed {
			return FilterEnvDecision{Allowed: false, Reason: "not in allow list"}
		}
	} else {
		// Apply redact patterns
		for _, pattern := range e.redactPatterns {
			if pattern.MatchString(name) {
				return FilterEnvDecision{Allowed: false, Reason: "matched redact pattern"}
			}
		}
	}

	return FilterEnvDecision{Allowed: true, Value: value}
}

// pathMatchesPattern checks if a path matches a glob pattern, supporting ** for recursive matching.
// SECURITY FIX #4: Normalize both path and pattern, reject paths with .. sequences
func pathMatchesPattern(path, pattern string) bool {
	// Normalize both path and pattern
	path = filepath.Clean(path)
	pattern = filepath.Clean(pattern)

	// Reject paths containing .. sequences (already cleaned, so shouldn't occur but be defensive)
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
