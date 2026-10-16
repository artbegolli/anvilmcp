package policy

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"gopkg.in/yaml.v3"
)

// Loader handles loading and managing policy files.
type Loader struct {
	userPolicyPath    string
	clusterPolicyPath string
	userPolicy        *Policy
	clusterPolicy     *Policy
	effective         *Policy
}

// NewLoader creates a new policy loader.
func NewLoader(userPath, clusterPath string) (*Loader, error) {
	l := &Loader{
		userPolicyPath:    userPath,
		clusterPolicyPath: clusterPath,
	}

	if err := l.Load(); err != nil {
		return nil, err
	}

	return l, nil
}

// Load loads both user and cluster policies and computes the effective policy.
func (l *Loader) Load() error {
	// Load cluster policy first (hard ceiling)
	clusterPolicy, err := l.loadPolicy(l.clusterPolicyPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to load cluster policy: %w", err)
	}
	if clusterPolicy == nil {
		clusterPolicy = DefaultPolicy()
	}
	l.clusterPolicy = clusterPolicy

	// Load user policy (can narrow but not broaden)
	userPolicy, err := l.loadPolicy(l.userPolicyPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to load user policy: %w", err)
	}
	if userPolicy == nil {
		userPolicy = DefaultPolicy()
	}
	l.userPolicy = userPolicy

	// Compute effective policy (intersection)
	l.effective = l.computeEffectivePolicy()

	return nil
}

// Effective returns the current effective policy.
func (l *Loader) Effective() *Policy {
	return l.effective
}

// UserPolicy returns the user layer as loaded (or DefaultPolicy() if the file
// was missing). Used by `anvilmcp policy show --user`.
func (l *Loader) UserPolicy() *Policy {
	return l.userPolicy
}

// ClusterPolicy returns the cluster layer as loaded (or DefaultPolicy() if
// the file was missing). Used by `anvilmcp policy show --cluster`.
func (l *Loader) ClusterPolicy() *Policy {
	return l.clusterPolicy
}

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

	currentUID, err := strconv.ParseUint(currentUser.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("failed to parse current UID: %w", err)
	}

	// Policy file must be owned by current user or root (UID 0)
	if stat.Uid != uint32(currentUID) && currentUID != 0 {
		return nil, fmt.Errorf("policy file must be owned by current user or root, got UID %d", stat.Uid)
	}

	// Path is operator-supplied via --policy/--cluster-policy and the lines above
	// already verified ownership (root or current user) and that the file is not
	// world/group writable, so file inclusion via a hostile path is mitigated.
	data, err := os.ReadFile(path) // #nosec G304 -- validated above
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

// computeEffectivePolicy returns the intersection of cluster and user policies.
// User policy can only narrow, never broaden.
func (l *Loader) computeEffectivePolicy() *Policy {
	effective := l.clusterPolicy

	// User CWD overrides if set
	if l.userPolicy.Session.CWD != "" {
		effective.Session.CWD = l.userPolicy.Session.CWD
	}

	// User workspace root overrides if set. This only moves the base used to
	// resolve relative paths (and Session.CWD above takes precedence for that);
	// every access still goes through the allow/deny lists, so carrying it over
	// cannot broaden what the user may reach. Without this, a user-set
	// workspace_root was silently dropped and policy.show reported a root
	// inconsistent with the effective allow rules.
	if l.userPolicy.Paths.WorkspaceRoot != "" {
		effective.Paths.WorkspaceRoot = l.userPolicy.Paths.WorkspaceRoot
	}

	// Paths: union the most restrictive
	if len(l.userPolicy.Paths.AllowRead) > 0 {
		effective.Paths.AllowRead = l.userPolicy.Paths.AllowRead
	}
	if len(l.userPolicy.Paths.AllowWrite) > 0 {
		effective.Paths.AllowWrite = l.userPolicy.Paths.AllowWrite
	}
	effective.Paths.Deny = append(effective.Paths.Deny, l.userPolicy.Paths.Deny...)

	// Exec: user can only narrow
	if l.userPolicy.Exec.Default != "" && l.userPolicy.Exec.Default == "deny" {
		effective.Exec.Default = "deny"
	}

	// TODO: merge allow/deny lists respecting constraints

	return effective
}

// PolicyFilePath returns the absolute path to the effective user policy file.
func (l *Loader) PolicyFilePath() string {
	if abs, err := filepath.Abs(l.userPolicyPath); err == nil {
		return abs
	}
	return l.userPolicyPath
}
