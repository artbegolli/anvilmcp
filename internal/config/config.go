package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SessionConfig holds session-level configuration from environment variables.
type SessionConfig struct {
	SessionID     string
	WorkspaceRoot string
	VenvPath      string
	PythonPath    string
	UserID        string
	HomeDir       string

	// KernelEnabled turns on the persistent Python kernel tools (kernel.*).
	// Off by default: enabling it lets the model run unmediated code as the
	// container user (see docs / kernel design), so it is opt-in.
	KernelEnabled bool
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
		KernelEnabled: getEnvBool("KERNEL_ENABLED", false),
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultVal
	}
}
