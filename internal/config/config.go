package config

import (
	"os"
	"path/filepath"
)

// SessionConfig holds session-level configuration from environment variables.
type SessionConfig struct {
	SessionID      string
	WorkspaceRoot  string
	VenvPath       string
	PythonPath     string
	UserID         string
	HomeDir        string
}

// NewSessionConfig loads session configuration from environment variables.
func NewSessionConfig() (*SessionConfig, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "/root"
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
