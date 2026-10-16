package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	_ "modernc.org/sqlite"
)

// Store manages the command history database.
type Store struct {
	db     *sql.DB
	dbPath string
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

	s := &Store{db: db, dbPath: dbPath, logger: logger}

	// Initialize schema
	if err := s.initSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := s.restrictPermissions(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return s, nil
}

// restrictPermissions tightens the database files to 0600.
//
// SQLite creates them honouring the process umask, which on a typical image
// yields 0644 -- world-readable, for a file holding command arguments and
// output previews. The containing directory is 0700, so this is defence in
// depth rather than the only barrier, but the audit trail should not be the
// loosest thing on the volume.
func (s *Store) restrictPermissions() error {
	// The -wal and -shm sidecars inherit the main file's mode when SQLite
	// creates them, but WAL mode is enabled on the connection string, so they
	// may already exist by now.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := s.dbPath + suffix
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if err := os.Chmod(path, 0600); err != nil {
			return fmt.Errorf("restrict permissions on %s: %w", path, err)
		}
	}
	return nil
}

// OpenReadOnly opens an existing history database for reading.
//
// Distinct from NewStore, which creates the file and the schema: a reader that
// did that would turn a typo'd --history-db into a silent empty result rather
// than an error, which is precisely the wrong failure mode for an audit tool.
func OpenReadOnly(ctx context.Context, dbPath string, logger *zap.Logger) (*Store, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("history database %s: %w", dbPath, err)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open history database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to connect to history database: %w", err)
	}

	return &Store{db: db, dbPath: dbPath, logger: logger}, nil
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
		resolved_path TEXT,
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

	return s.migrate(ctx)
}

// migrate brings an existing table up to the current column set. CREATE TABLE
// IF NOT EXISTS is a no-op against a database made by an earlier build, so new
// columns have to be added explicitly.
func (s *Store) migrate(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(history)")
	if err != nil {
		return fmt.Errorf("failed to inspect history schema: %w", err)
	}
	existing := map[string]bool{}
	for rows.Next() {
		var (
			cid        int
			name, typ  string
			notNull    int
			dflt       sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("failed to read history schema: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	// Only additive, nullable columns belong here; each entry is a constant.
	additions := []struct{ column, ddl string }{
		{"resolved_path", "ALTER TABLE history ADD COLUMN resolved_path TEXT"},
	}
	for _, a := range additions {
		if existing[a.column] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, a.ddl); err != nil {
			return fmt.Errorf("failed to add column %s: %w", a.column, err)
		}
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
		stdout_blob_path, stderr_blob_path, cwd, resolved_path, env_snapshot_hash, pinned, tags
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		rec.ID, rec.MCPSessionID, rec.CorrelationID, rec.ChatSessionID,
		rec.TimestampStart, rec.TimestampEnd, rec.Status, rec.Tool, rec.ArgsJSON, rec.Decision,
		rec.PolicyRule, rec.ExitCode, rec.DurationMS, rec.StdoutBytes, rec.StderrBytes,
		rec.StdoutTruncated, rec.StderrTruncated, rec.StdoutPreview, rec.StderrPreview,
		rec.StdoutBlobPath, rec.StderrBlobPath, rec.CWD, rec.ResolvedPath, rec.EnvSnapshotHash, rec.Pinned, rec.Tags,
	)

	if err != nil {
		return fmt.Errorf("failed to insert history record: %w", err)
	}

	return nil
}

// UpdateRecord updates an existing history record.
//
// decision, policy_rule and cwd are in the SET list because they are only
// known once the handler has run: the record is inserted when the call starts,
// and the chokepoints annotate it mid-flight. Leaving them out (as this
// originally did) silently dropped the policy verdict — the single most
// important column in the table — on every completed call.
func (s *Store) UpdateRecord(ctx context.Context, rec *Record) error {
	query := `
	UPDATE history SET
		ts_end = ?, status = ?, exit_code = ?, duration_ms = ?,
		decision = ?, policy_rule = ?, cwd = ?, resolved_path = ?,
		stdout_bytes = ?, stderr_bytes = ?, stdout_truncated = ?,
		stderr_truncated = ?, stdout_preview = ?, stderr_preview = ?,
		stdout_blob_path = ?, stderr_blob_path = ?
	WHERE id = ?
	`

	_, err := s.db.ExecContext(ctx, query,
		rec.TimestampEnd, rec.Status, rec.ExitCode, rec.DurationMS,
		rec.Decision, rec.PolicyRule, rec.CWD, rec.ResolvedPath,
		rec.StdoutBytes, rec.StderrBytes, rec.StdoutTruncated,
		rec.StderrTruncated, rec.StdoutPreview, rec.StderrPreview,
		rec.StdoutBlobPath, rec.StderrBlobPath, rec.ID,
	)

	if err != nil {
		return fmt.Errorf("failed to update history record: %w", err)
	}

	return nil
}

// recordColumns is the explicit column list every read uses, in Record field
// order. The reads used to be "SELECT *", which silently depended on the
// CREATE TABLE order and broke on the trailing created_at column: 26 columns
// scanned into 25 destinations, so every Get/List returned a scan error.
const recordColumns = `
	id, mcp_session_id, correlation_id, chat_session_id,
	ts_start, ts_end, status, tool, args_json, decision,
	policy_rule, exit_code, duration_ms, stdout_bytes, stderr_bytes,
	stdout_truncated, stderr_truncated, stdout_preview, stderr_preview,
	stdout_blob_path, stderr_blob_path, cwd, resolved_path, env_snapshot_hash, pinned, tags`

// Queries are declared as constant expressions rather than assembled at call
// time: the column list is fixed, and keeping it constant means no SQL string
// is ever built from a variable.
const (
	selectRecordByID = `SELECT ` + recordColumns + ` FROM history WHERE id = ?`
)

func scanRecord(sc interface{ Scan(...any) error }) (*Record, error) {
	rec := &Record{}
	err := sc.Scan(
		&rec.ID, &rec.MCPSessionID, &rec.CorrelationID, &rec.ChatSessionID,
		&rec.TimestampStart, &rec.TimestampEnd, &rec.Status, &rec.Tool, &rec.ArgsJSON, &rec.Decision,
		&rec.PolicyRule, &rec.ExitCode, &rec.DurationMS, &rec.StdoutBytes, &rec.StderrBytes,
		&rec.StdoutTruncated, &rec.StderrTruncated, &rec.StdoutPreview, &rec.StderrPreview,
		&rec.StdoutBlobPath, &rec.StderrBlobPath, &rec.CWD, &rec.ResolvedPath, &rec.EnvSnapshotHash, &rec.Pinned, &rec.Tags,
	)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// GetRecord retrieves a single history record by ID.
func (s *Store) GetRecord(ctx context.Context, id string) (*Record, error) {
	rec, err := scanRecord(s.db.QueryRowContext(ctx, selectRecordByID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("record not found")
	}
	return rec, err
}

// Filter narrows a history query. A zero value matches everything.
type Filter struct {
	Tool     string
	Status   string
	Decision string
	Since    time.Time
	Limit    int
}

// DefaultLimit caps an unbounded query.
const DefaultLimit = 50

// selectFiltered is one constant query with every filter made optional in SQL
// rather than by assembling a WHERE clause at call time. Slower in principle —
// no index short-circuit — and irrelevant in practice against a retention-
// bounded table, in exchange for never building a SQL string from a variable.
const selectFiltered = `SELECT ` + recordColumns + ` FROM history
	WHERE (? = '' OR tool = ?)
	  AND (? = '' OR status = ?)
	  AND (? = '' OR decision = ?)
	  AND (? = 0 OR ts_start >= ?)
	ORDER BY ts_start DESC
	LIMIT ?`

// Query returns records matching f, newest first.
func (s *Store) Query(ctx context.Context, f Filter) ([]*Record, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	var since int64
	if !f.Since.IsZero() {
		since = f.Since.UnixNano()
	}

	rows, err := s.db.QueryContext(ctx, selectFiltered,
		f.Tool, f.Tool,
		f.Status, f.Status,
		f.Decision, f.Decision,
		since, since,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var records []*Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

// ListRecent returns the N most recent history records.
func (s *Store) ListRecent(ctx context.Context, limit int) ([]*Record, error) {
	return s.Query(ctx, Filter{Limit: limit})
}

// ListByTool returns the N most recent records for a single tool.
func (s *Store) ListByTool(ctx context.Context, tool string, limit int) ([]*Record, error) {
	return s.Query(ctx, Filter{Tool: tool, Limit: limit})
}

// ErrSizeCapUnreachable reports that audit.max_total_bytes cannot be satisfied
// without destroying the whole trail. Prune stops and surfaces this instead:
// an audit log that silently erases itself to hit a misconfigured byte cap is
// worse than one that is over budget and says so.
var ErrSizeCapUnreachable = errors.New("audit size cap cannot be met")

// minRetainedRecords is the floor the size cap will not prune below.
const minRetainedRecords = 100

// Prune enforces audit.retention_days and audit.max_total_bytes.
//
// Retention is applied first (cheap, bounded by time), then the size cap: rows
// are deleted oldest-first in batches until the database file fits, with a
// VACUUM afterwards because SQLite does not return freed pages to the
// filesystem on its own. Either limit is skipped when its value is <= 0.
func (s *Store) Prune(ctx context.Context, retentionDays int, maxTotalBytes int64) (int64, error) {
	var deleted int64

	if retentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -retentionDays).UnixNano()
		res, err := s.db.ExecContext(ctx, "DELETE FROM history WHERE ts_start < ? AND pinned = 0", cutoff)
		if err != nil {
			return deleted, fmt.Errorf("retention prune: %w", err)
		}
		n, _ := res.RowsAffected()
		deleted += n
	}

	if maxTotalBytes > 0 {
		// Bounded loop: each pass drops the oldest unpinned batch, vacuums, and
		// re-measures. Stops when the file fits, when there is nothing left to
		// delete, when a pass fails to shrink the file, or once the floor of
		// recent records is reached.
		const (
			batch     = int64(200)
			maxPasses = 200
		)
		prevSize := int64(-1)
		for pass := 0; pass < maxPasses; pass++ {
			size, err := s.sizeBytes()
			if err != nil {
				return deleted, fmt.Errorf("size prune: measure: %w", err)
			}
			if size <= maxTotalBytes {
				return deleted, nil
			}
			if prevSize >= 0 && size >= prevSize {
				// The last pass deleted rows without shrinking the file, so
				// the cap is below SQLite's own floor for this schema. Stop
				// rather than grinding the trail away to meet it.
				return deleted, fmt.Errorf("%w: %d bytes on disk with max_total_bytes=%d", ErrSizeCapUnreachable, size, maxTotalBytes)
			}
			prevSize = size

			var remaining int64
			if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM history WHERE pinned = 0").Scan(&remaining); err != nil {
				return deleted, fmt.Errorf("size prune: count: %w", err)
			}
			if remaining <= minRetainedRecords {
				return deleted, fmt.Errorf("%w: %d bytes on disk with max_total_bytes=%d, holding the %d most recent records", ErrSizeCapUnreachable, size, maxTotalBytes, remaining)
			}

			limit := batch
			if over := remaining - minRetainedRecords; over < limit {
				limit = over
			}

			res, err := s.db.ExecContext(ctx, `
				DELETE FROM history WHERE id IN (
					SELECT id FROM history WHERE pinned = 0 ORDER BY ts_start ASC LIMIT ?
				)`, limit)
			if err != nil {
				return deleted, fmt.Errorf("size prune: %w", err)
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				return deleted, nil
			}
			deleted += n
			if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
				return deleted, fmt.Errorf("vacuum: %w", err)
			}
		}
	}

	return deleted, nil
}

// sizeBytes reports the on-disk size of the database, including the WAL, which
// is where recent writes live until a checkpoint.
func (s *Store) sizeBytes() (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal"} {
		info, err := os.Stat(s.dbPath + suffix)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}
