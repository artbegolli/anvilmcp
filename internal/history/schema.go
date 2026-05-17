package history

import (
	"time"
)

// Record represents a single tool invocation in the history.
type Record struct {
	ID                string    `db:"id"`
	MCPSessionID      string    `db:"mcp_session_id"`
	CorrelationID     string    `db:"correlation_id"`
	ChatSessionID     *string   `db:"chat_session_id"`
	TimestampStart    int64     `db:"ts_start"` // unix nanos
	TimestampEnd      *int64    `db:"ts_end"`
	Status            string    `db:"status"` // running, ok, error, denied, killed
	Tool              string    `db:"tool"`
	ArgsJSON          string    `db:"args_json"` // redacted
	Decision          string    `db:"decision"` // allow, deny, ask_approved, ask_denied
	PolicyRule        *string   `db:"policy_rule"`
	ExitCode          *int      `db:"exit_code"`
	DurationMS        *int64    `db:"duration_ms"`
	StdoutBytes       int64     `db:"stdout_bytes"`
	StderrBytes       int64     `db:"stderr_bytes"`
	StdoutTruncated   bool      `db:"stdout_truncated"`
	StderrTruncated   bool      `db:"stderr_truncated"`
	StdoutPreview     *string   `db:"stdout_preview"` // inline, <=64KB
	StderrPreview     *string   `db:"stderr_preview"`
	StdoutBlobPath    *string   `db:"stdout_blob_path"` // path on disk for large output
	StderrBlobPath    *string   `db:"stderr_blob_path"`
	CWD               string    `db:"cwd"`
	EnvSnapshotHash   *string   `db:"env_snapshot_hash"`
	Pinned            bool      `db:"pinned"`
	Tags              *string   `db:"tags"` // comma-separated
}

// NewRecord creates a new history record for a tool invocation.
func NewRecord(mcpSessionID, correlationID, tool string, argsJSON string) *Record {
	return &Record{
		MCPSessionID:   mcpSessionID,
		CorrelationID:  correlationID,
		Tool:           tool,
		ArgsJSON:       argsJSON,
		TimestampStart: time.Now().UnixNano(),
		Status:         "running",
	}
}

// Finalize marks the record as complete.
func (r *Record) Finalize(status string, exitCode *int, stdoutBytes, stderrBytes int64) {
	now := time.Now().UnixNano()
	r.TimestampEnd = &now
	r.Status = status
	r.ExitCode = exitCode
	r.StdoutBytes = stdoutBytes
	r.StderrBytes = stderrBytes
	if r.TimestampStart > 0 {
		dur := now - r.TimestampStart
		r.DurationMS = &dur
	}
}
