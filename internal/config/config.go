package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// SessionConfig holds session-level configuration from environment variables.
type SessionConfig struct {
	SessionID     string
	WorkspaceRoot string
	VenvPath      string
	PythonPath    string
	UserID        string
	HomeDir       string
}

// NewSessionConfig loads session configuration from environment variables.
func NewSessionConfig() (*SessionConfig, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine user home directory: %w", err)
	}

	cfg := &SessionConfig{
		SessionID:     getEnv("SESSION_ID", "default"),
		WorkspaceRoot: getEnv("WORKSPACE_ROOT", filepath.Join(homeDir, "workspace")),
		VenvPath:      getEnv("VENV_PATH", filepath.Join(homeDir, ".venv")),
		PythonPath:    getEnv("PYTHON_PATH", "python3"),
		UserID:        getEnv("USER", "root"),
		HomeDir:       homeDir,
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// ExpandUser expands a leading ~ or ~/ in path using the current user's home
// directory. Paths from the policy file are written by hand, and
// config.yaml.example has always shown "~/.local/state/anvilmcp/history.db";
// those fields were unread until the audit trail was wired up, at which point
// an unexpanded tilde would have created a literal "./~" directory.
func ExpandUser(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	if len(path) > 1 && path[1] != '/' {
		// ~user form: not supported, leave it to fail visibly rather than
		// silently resolving to the wrong account.
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
