package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/artbegolli/anvilmcp/internal/history"
)

// mirror appends completed records to a JSONL file, for shipping to a
// cluster-side log pipeline that tails it. Only completed calls are written:
// the database holds the in-flight "running" rows.
type mirror struct {
	mu sync.Mutex
	f  *os.File
}

// line is the on-disk shape. Declared separately from history.Record so the
// field names consumers parse are an explicit interface rather than whatever
// the Go struct happens to be called this week.
type line struct {
	ID            string  `json:"id"`
	MCPSessionID  string  `json:"mcp_session_id"`
	CorrelationID string  `json:"correlation_id"`
	TSStart       int64   `json:"ts_start"`
	TSEnd         *int64  `json:"ts_end,omitempty"`
	DurationMS    *int64  `json:"duration_ms,omitempty"`
	Status        string  `json:"status"`
	Tool          string  `json:"tool"`
	Args          string  `json:"args_json"`
	Decision      string  `json:"decision"`
	PolicyRule    *string `json:"policy_rule,omitempty"`
	ExitCode      *int    `json:"exit_code,omitempty"`
	CWD           string  `json:"cwd,omitempty"`
	StdoutBytes   int64   `json:"stdout_bytes"`
	StderrBytes   int64   `json:"stderr_bytes"`
	Truncated     bool    `json:"truncated,omitempty"`
}

func openMirror(path string) (*mirror, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create mirror directory: %w", err)
	}
	// 0600: the mirror carries command arguments, so it is as sensitive as
	// the history database. The path is operator-supplied via
	// audit.jsonl_mirror_path, the same trust level as --history-db.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600) // #nosec G304 -- operator-configured audit sink
	if err != nil {
		return nil, err
	}
	return &mirror{f: f}, nil
}

func (m *mirror) append(rec *history.Record) error {
	b, err := json.Marshal(line{
		ID:            rec.ID,
		MCPSessionID:  rec.MCPSessionID,
		CorrelationID: rec.CorrelationID,
		TSStart:       rec.TimestampStart,
		TSEnd:         rec.TimestampEnd,
		DurationMS:    rec.DurationMS,
		Status:        rec.Status,
		Tool:          rec.Tool,
		Args:          rec.ArgsJSON,
		Decision:      rec.Decision,
		PolicyRule:    rec.PolicyRule,
		ExitCode:      rec.ExitCode,
		CWD:           rec.CWD,
		StdoutBytes:   rec.StdoutBytes,
		StderrBytes:   rec.StderrBytes,
		Truncated:     rec.StdoutTruncated || rec.StderrTruncated,
	})
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.f.Write(append(b, '\n')); err != nil {
		return err
	}
	// Sync so a crash cannot lose the tail of the trail. One fsync per
	// completed tool call is affordable at this call volume.
	return m.f.Sync()
}

func (m *mirror) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.f.Close()
}
