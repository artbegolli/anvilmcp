package history

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
	"go.uber.org/zap"
)

// Store manages the command history database.
type Store struct {
	db     *sql.DB
	logger *zap.Logger
}

// NewStore opens or creates the history database.
func NewStore(ctx context.Context, dbPath string, logger *zap.Logger) (*Store, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create history directory: %w", err)
	}

	// Open database
	db, err := sql.Open("sqlite", "file:"+dbPath+"?cache=shared&mode=rwc&_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open history database: %w", err)
	}

	// Test connection
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to history database: %w", err)
	}

	s := &Store{db: db, logger: logger}

	// Initialize schema
	if err := s.initSchema(ctx); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initSchema(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS history (
		id TEXT PRIMARY KEY,
		mcp_session_id TEXT NOT NULL,
		correlation_id TEXT,
		chat_session_id TEXT,
		ts_start INTEGER NOT NULL,
		ts_end INTEGER,
		status TEXT NOT NULL DEFAULT 'running',
		tool TEXT NOT NULL,
		args_json TEXT,
		decision TEXT,
		policy_rule TEXT,
		exit_code INTEGER,
		duration_ms INTEGER,
		stdout_bytes INTEGER DEFAULT 0,
		stderr_bytes INTEGER DEFAULT 0,
		stdout_truncated BOOLEAN DEFAULT 0,
		stderr_truncated BOOLEAN DEFAULT 0,
		stdout_preview TEXT,
		stderr_preview TEXT,
		stdout_blob_path TEXT,
		stderr_blob_path TEXT,
		cwd TEXT,
		env_snapshot_hash TEXT,
		pinned BOOLEAN DEFAULT 0,
		tags TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_history_ts ON history(ts_start DESC);
	CREATE INDEX IF NOT EXISTS idx_history_tool ON history(tool);
	CREATE INDEX IF NOT EXISTS idx_history_status ON history(status);
	CREATE INDEX IF NOT EXISTS idx_history_correlation ON history(correlation_id);
	CREATE INDEX IF NOT EXISTS idx_history_session ON history(mcp_session_id);
	`

	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("failed to initialize schema: %w", err)
	}

	return nil
}

// InsertRecord inserts a new history record.
func (s *Store) InsertRecord(ctx context.Context, rec *Record) error {
	query := `
	INSERT INTO history (
		id, mcp_session_id, correlation_id, chat_session_id,
		ts_start, ts_end, status, tool, args_json, decision,
		policy_rule, exit_code, duration_ms, stdout_bytes, stderr_bytes,
		stdout_truncated, stderr_truncated, stdout_preview, stderr_preview,
		stdout_blob_path, stderr_blob_path, cwd, env_snapshot_hash, pinned, tags
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		rec.ID, rec.MCPSessionID, rec.CorrelationID, rec.ChatSessionID,
		rec.TimestampStart, rec.TimestampEnd, rec.Status, rec.Tool, rec.ArgsJSON, rec.Decision,
		rec.PolicyRule, rec.ExitCode, rec.DurationMS, rec.StdoutBytes, rec.StderrBytes,
		rec.StdoutTruncated, rec.StderrTruncated, rec.StdoutPreview, rec.StderrPreview,
		rec.StdoutBlobPath, rec.StderrBlobPath, rec.CWD, rec.EnvSnapshotHash, rec.Pinned, rec.Tags,
	)

	if err != nil {
		return fmt.Errorf("failed to insert history record: %w", err)
	}

	return nil
}

// UpdateRecord updates an existing history record.
func (s *Store) UpdateRecord(ctx context.Context, rec *Record) error {
	query := `
	UPDATE history SET
		ts_end = ?, status = ?, exit_code = ?, duration_ms = ?,
		stdout_bytes = ?, stderr_bytes = ?, stdout_truncated = ?,
		stderr_truncated = ?, stdout_preview = ?, stderr_preview = ?,
		stdout_blob_path = ?, stderr_blob_path = ?
	WHERE id = ?
	`

	_, err := s.db.ExecContext(ctx, query,
		rec.TimestampEnd, rec.Status, rec.ExitCode, rec.DurationMS,
		rec.StdoutBytes, rec.StderrBytes, rec.StdoutTruncated,
		rec.StderrTruncated, rec.StdoutPreview, rec.StderrPreview,
		rec.StdoutBlobPath, rec.StderrBlobPath, rec.ID,
	)

	if err != nil {
		return fmt.Errorf("failed to update history record: %w", err)
	}

	return nil
}

// GetRecord retrieves a single history record by ID.
func (s *Store) GetRecord(ctx context.Context, id string) (*Record, error) {
	rec := &Record{}
	err := s.db.QueryRowContext(ctx, "SELECT * FROM history WHERE id = ?", id).Scan(
		&rec.ID, &rec.MCPSessionID, &rec.CorrelationID, &rec.ChatSessionID,
		&rec.TimestampStart, &rec.TimestampEnd, &rec.Status, &rec.Tool, &rec.ArgsJSON, &rec.Decision,
		&rec.PolicyRule, &rec.ExitCode, &rec.DurationMS, &rec.StdoutBytes, &rec.StderrBytes,
		&rec.StdoutTruncated, &rec.StderrTruncated, &rec.StdoutPreview, &rec.StderrPreview,
		&rec.StdoutBlobPath, &rec.StderrBlobPath, &rec.CWD, &rec.EnvSnapshotHash, &rec.Pinned, &rec.Tags,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("record not found")
	}

	return rec, err
}

// ListRecent returns the N most recent history records.
func (s *Store) ListRecent(ctx context.Context, limit int) ([]*Record, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT * FROM history ORDER BY ts_start DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*Record
	for rows.Next() {
		rec := &Record{}
		err := rows.Scan(
			&rec.ID, &rec.MCPSessionID, &rec.CorrelationID, &rec.ChatSessionID,
			&rec.TimestampStart, &rec.TimestampEnd, &rec.Status, &rec.Tool, &rec.ArgsJSON, &rec.Decision,
			&rec.PolicyRule, &rec.ExitCode, &rec.DurationMS, &rec.StdoutBytes, &rec.StderrBytes,
			&rec.StdoutTruncated, &rec.StderrTruncated, &rec.StdoutPreview, &rec.StderrPreview,
			&rec.StdoutBlobPath, &rec.StderrBlobPath, &rec.CWD, &rec.EnvSnapshotHash, &rec.Pinned, &rec.Tags,
		)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}

	return records, rows.Err()
}
