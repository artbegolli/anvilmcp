package policy

import (
	"regexp"
)

// Policy represents the parsed policy YAML structure.
type Policy struct {
	Version int               `yaml:"version" json:"version"`
	Session SessionPolicy     `yaml:"session" json:"session"`
	Paths   PathsPolicy       `yaml:"paths" json:"paths"`
	Exec    ExecPolicy        `yaml:"exec" json:"exec"`
	Env     EnvironmentPolicy `yaml:"env" json:"env"`
	Kernel  KernelPolicy      `yaml:"kernel" json:"kernel"`
	Audit   AuditPolicy       `yaml:"audit" json:"audit"`
}

// SessionPolicy configures session-level defaults.
type SessionPolicy struct {
	CWD string `yaml:"cwd" json:"cwd"`
}

// PathsPolicy controls filesystem access.
type PathsPolicy struct {
	WorkspaceRoot string   `yaml:"workspace_root" json:"workspace_root"`
	AllowRead     []string `yaml:"allow_read" json:"allow_read"`
	AllowWrite    []string `yaml:"allow_write" json:"allow_write"`
	Deny          []string `yaml:"deny" json:"deny"`
}

// ExecPolicy controls command execution.
type ExecPolicy struct {
	Default            string                   `yaml:"default" json:"default"` // "allow" or "deny"
	Allow              map[string]CommandPolicy `yaml:"allow" json:"allow"`
	Deny               []string                 `yaml:"deny" json:"deny"`
	Ask                map[string]CommandPolicy `yaml:"ask" json:"ask"`
	ShellInterpolation bool                     `yaml:"shell_interpolation" json:"shell_interpolation"`
	MaxRuntimeSeconds  int                      `yaml:"max_runtime_seconds" json:"max_runtime_seconds"`
	MaxOutputBytes     int                      `yaml:"max_output_bytes" json:"max_output_bytes"`
	MaxConcurrent      int                      `yaml:"max_concurrent" json:"max_concurrent"`
}

// CommandPolicy restricts a specific command.
type CommandPolicy struct {
	ArgsDeny    []string `yaml:"args_deny" json:"args_deny"`
	Subcommands []string `yaml:"subcommands" json:"subcommands"`
}

// EnvironmentPolicy controls environment variable access.
type EnvironmentPolicy struct {
	RedactPatterns []string `yaml:"redact_patterns" json:"redact_patterns"`
	ExplicitDeny   []string `yaml:"explicit_deny" json:"explicit_deny"`
	AllowOnly      []string `yaml:"allow_only" json:"allow_only"`
}

// KernelPolicy controls Python kernel behavior.
type KernelPolicy struct {
	Default           string   `yaml:"default" json:"default"` // "allow" or "deny"
	DenyImports       []string `yaml:"deny_imports" json:"deny_imports"`
	MaxRuntimeSeconds int      `yaml:"max_runtime_seconds" json:"max_runtime_seconds"`
}

// AuditPolicy controls history and logging.
type AuditPolicy struct {
	HistoryDBPath   string   `yaml:"history_db_path" json:"history_db_path"`
	JSONLMirrorPath string   `yaml:"jsonl_mirror_path" json:"jsonl_mirror_path"`
	RetentionDays   int      `yaml:"retention_days" json:"retention_days"`
	MaxTotalBytes   int64    `yaml:"max_total_bytes" json:"max_total_bytes"`
	NoStoreTools    []string `yaml:"no_store_tools" json:"no_store_tools"`
	NoStoreOutput   []string `yaml:"no_store_output" json:"no_store_output"`
}

// DefaultPolicy returns a safe-by-default policy.
func DefaultPolicy() *Policy {
	return &Policy{
		Version: 1,
		Session: SessionPolicy{
			CWD: "/workspace",
		},
		Paths: PathsPolicy{
			WorkspaceRoot: "/workspace",
			AllowRead: []string{
				"/workspace/**",
			},
			AllowWrite: []string{
				"/workspace/**",
			},
			Deny: []string{
				"/workspace/.git/**",
				"/home/**/.ssh/**",
				"/home/**/.aws/**",
				"/home/**/.gnupg/**",
				"/run/secrets/**",
				"/etc/**",
				"**/.env",
				"**/anvilmcp/config.yaml",
			},
		},
		Exec: ExecPolicy{
			Default:            "deny",
			Allow:              defaultAllowedCommands(),
			Deny:               []string{"rm", "dd", "mkfs*", "ssh", "scp", "curl", "wget"},
			Ask:                map[string]CommandPolicy{},
			ShellInterpolation: false,
			MaxRuntimeSeconds:  300,
			MaxOutputBytes:     5_000_000,
			MaxConcurrent:      4,
		},
		Env: EnvironmentPolicy{
			RedactPatterns: []string{
				"(?i)secret",
				"(?i)token",
				"(?i)password",
				"(?i)api[_-]?key",
				"(?i)credential",
			},
			ExplicitDeny: []string{},
			AllowOnly:    []string{},
		},
		Kernel: KernelPolicy{
			Default:           "allow",
			DenyImports:       []string{},
			MaxRuntimeSeconds: 600,
		},
		Audit: AuditPolicy{
			RetentionDays: 30,
			MaxTotalBytes: 1_073_741_824, // 1 GB
		},
	}
}

func defaultAllowedCommands() map[string]CommandPolicy {
	return map[string]CommandPolicy{
		"python":  {},
		"python3": {},
		"pytest":  {},
		"ls":      {},
		"cat":     {},
		"rg":      {},
		"git": {
			ArgsDeny: []string{"push", "reset", "clean", "checkout"},
		},
		"pip": {
			ArgsDeny: []string{"uninstall", "--break-system-packages"},
		},
	}
}

// CompileRedactPatterns compiles regex patterns for environment variable redaction.
func (p *Policy) CompileRedactPatterns() []*regexp.Regexp {
	var patterns []*regexp.Regexp
	for _, pattern := range p.Env.RedactPatterns {
		if compiled, err := regexp.Compile(pattern); err == nil {
			patterns = append(patterns, compiled)
		}
	}
	return patterns
}
