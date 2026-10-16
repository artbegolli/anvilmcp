// Package audit records every MCP tool and resource invocation to the history
// store, so what the model did in a session is reconstructable afterwards.
//
// Why this is load-bearing: the policy chokepoints gate tool *arguments*, not
// the code those arguments go on to run. An allow-listed interpreter — python3
// via exec.shell, or a persistent kernel — can reach anything the container
// can, so path and exec rules are a guardrail against accident rather than a
// boundary against intent. The audit trail is the control that still holds
// when the others are stepped around: it records the call, the policy decision
// (including that there wasn't one), and the outcome.
//
// Recording is deliberately detached from the caller's context. A client that
// disconnects mid-call, or a cancelled request, must not take its own record
// down with it.
package audit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/history"
	"github.com/artbegolli/anvilmcp/internal/metrics"
	"github.com/artbegolli/anvilmcp/internal/policy"
)

// Terminal statuses for a recorded call.
const (
	StatusOK     = "ok"
	StatusError  = "error"
	StatusDenied = "denied"
	StatusKilled = "killed"
)

// Policy decisions recorded alongside a call.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"

	// DecisionUnmediated marks a call that no chokepoint evaluated — the
	// kernel surface, for instance, where only the launcher is gated and the
	// code itself runs unmediated. Explicit so these are greppable rather
	// than indistinguishable from an allow.
	DecisionUnmediated = "unmediated"
)

// writeTimeout bounds a single audit write. Short: a stalled state volume
// should degrade the audit trail, not the session.
const writeTimeout = 5 * time.Second

// pruneInterval is how often retention and the size cap are re-applied.
const pruneInterval = time.Hour

type ctxKey struct{}

// Recorder writes audit records for tool and resource invocations.
type Recorder struct {
	store   *history.Store
	mirror  *mirror
	engine  *policy.Engine
	logger  *zap.Logger
	metrics *metrics.Registry

	cfg       policy.AuditPolicy
	sessionID string

	noStoreTools  map[string]bool
	noStoreOutput map[string]bool

	// lifecycleMu guards started/closed so Close works whether or not Start
	// was ever called -- it is optional, and tests skip it.
	lifecycleMu sync.Mutex
	started     bool
	closed      bool
	stop        chan struct{}
	done        chan struct{}
}

// Call describes an invocation about to run.
type Call struct {
	Tool string
	// MCPSessionID is the transport-level client session, when the transport
	// exposes one. Empty falls back to the container session id.
	MCPSessionID string
	Args         map[string]any
}

// NewRecorder builds a Recorder. store must be non-nil; engine supplies the
// redaction patterns used on arguments. A JSONL mirror is opened when
// cfg.JSONLMirrorPath is set, and failing to open it is fatal — an operator
// who configured a mirror should not silently get none.
func NewRecorder(
	store *history.Store,
	engine *policy.Engine,
	cfg policy.AuditPolicy,
	sessionID string,
	logger *zap.Logger,
	m *metrics.Registry,
) (*Recorder, error) {
	if store == nil {
		return nil, fmt.Errorf("audit: history store is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if m == nil {
		m = metrics.NewNoop()
	}

	r := &Recorder{
		store:         store,
		engine:        engine,
		logger:        logger,
		metrics:       m,
		cfg:           cfg,
		sessionID:     sessionID,
		noStoreTools:  toSet(cfg.NoStoreTools),
		noStoreOutput: toSet(cfg.NoStoreOutput),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}

	if cfg.JSONLMirrorPath != "" {
		mw, err := openMirror(cfg.JSONLMirrorPath)
		if err != nil {
			return nil, fmt.Errorf("audit: open jsonl mirror: %w", err)
		}
		r.mirror = mw
	}

	return r, nil
}

// Start applies retention once and then hourly until Close. Optional: the
// recorder records fine without it, it just never prunes. Idempotent.
func (r *Recorder) Start(ctx context.Context) {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.started || r.closed {
		return
	}
	r.started = true

	go func() {
		defer close(r.done)
		t := time.NewTicker(pruneInterval)
		defer t.Stop()
		for {
			r.prune(ctx)
			select {
			case <-t.C:
			case <-r.stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (r *Recorder) prune(ctx context.Context) {
	if r.cfg.RetentionDays <= 0 && r.cfg.MaxTotalBytes <= 0 {
		return
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()

	n, err := r.store.Prune(pctx, r.cfg.RetentionDays, r.cfg.MaxTotalBytes)
	if err != nil {
		r.metrics.AuditWrites.WithLabelValues("prune", "failed").Inc()
		if errors.Is(err, history.ErrSizeCapUnreachable) {
			// Misconfiguration, not a failure: rows were still pruned, and the
			// trail was deliberately kept rather than erased to hit the cap.
			r.logger.Warn("audit size cap cannot be met; keeping recent records",
				zap.Int64("max_total_bytes", r.cfg.MaxTotalBytes),
				zap.Int64("pruned", n),
				zap.Error(err),
			)
		} else {
			r.logger.Error("audit prune failed", zap.Error(err))
		}
	}
	if n > 0 {
		r.metrics.AuditPruned.Add(float64(n))
		r.logger.Info("audit records pruned",
			zap.Int64("rows", n),
			zap.Int("retention_days", r.cfg.RetentionDays),
			zap.Int64("max_total_bytes", r.cfg.MaxTotalBytes),
		)
	}
}

// Close stops the prune loop and closes the JSONL mirror.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.lifecycleMu.Lock()
	if r.closed {
		r.lifecycleMu.Unlock()
		return nil
	}
	r.closed = true
	started := r.started
	close(r.stop)
	r.lifecycleMu.Unlock()

	if started {
		<-r.done
	}
	if r.mirror != nil {
		return r.mirror.Close()
	}
	return nil
}

// Begin opens a record for call and returns a context carrying it, so handlers
// deeper in the stack can annotate the policy decision and outcome via
// FromContext.
//
// A tool listed in audit.no_store_tools yields a nil *Entry, which every Entry
// method tolerates — callers never need to nil-check. The returned error is
// non-nil only under audit.fail_closed, and means the call must be refused.
func (r *Recorder) Begin(ctx context.Context, call Call) (context.Context, *Entry, error) {
	if r == nil {
		return ctx, nil, nil
	}
	if r.noStoreTools[call.Tool] {
		r.metrics.AuditSkipped.Inc()
		return ctx, nil, nil
	}

	sessionID := call.MCPSessionID
	if sessionID == "" {
		sessionID = r.sessionID
	}

	rec := history.NewRecord(sessionID, r.sessionID, call.Tool, r.redactArgs(call.Args))
	rec.Decision = DecisionUnmediated

	e := &Entry{rec: rec, r: r, skipOutput: r.noStoreOutput[call.Tool]}

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()

	if err := r.store.InsertRecord(wctx, rec); err != nil {
		r.metrics.AuditWrites.WithLabelValues("insert", "failed").Inc()
		r.logger.Error("audit insert failed",
			zap.String("tool", call.Tool),
			zap.Bool("fail_closed", r.cfg.FailClosed),
			zap.Error(err),
		)
		if r.cfg.FailClosed {
			return ctx, nil, fmt.Errorf("refusing %s: audit record could not be written (audit.fail_closed): %w", call.Tool, err)
		}
		// Carry on unrecorded-but-tracked: Finish retries as an insert, and
		// the JSONL mirror still gets the completed line.
	} else {
		e.inserted = true
		r.metrics.AuditWrites.WithLabelValues("insert", "ok").Inc()
	}

	return context.WithValue(ctx, ctxKey{}, e), e, nil
}

// FromContext returns the record for the in-flight call, or nil when there is
// none. Every Entry method is nil-safe, so the result needs no guard.
func FromContext(ctx context.Context) *Entry {
	e, _ := ctx.Value(ctxKey{}).(*Entry)
	return e
}

// Entry is the open record for one invocation. All methods are safe on a nil
// receiver and safe for concurrent use.
type Entry struct {
	r          *Recorder
	mu         sync.Mutex
	rec        *history.Record
	status     string
	skipOutput bool
	inserted   bool
	finished   bool
}

// SetDecision records the policy verdict and the rule that produced it.
func (e *Entry) SetDecision(decision, rule string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rec.Decision = decision
	if rule != "" {
		e.rec.PolicyRule = &rule
	}
}

// SetCWD records the resolved working directory of the call.
func (e *Entry) SetCWD(cwd string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rec.CWD = cwd
}

// SetResolvedPath records the post-symlink absolute target of a filesystem
// call. Kept apart from SetCWD so the trail does not file a target path under
// a column named cwd: the useful signal is the difference between this and the
// raw path in the arguments, which is what a symlink escape looks like.
func (e *Entry) SetResolvedPath(path string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rec.ResolvedPath = &path
}

// SetExitCode records a subprocess exit status.
func (e *Entry) SetExitCode(code int) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rec.ExitCode = &code
}

// SetOutput records output volume, and previews unless the tool is listed in
// audit.no_store_output.
func (e *Entry) SetOutput(stdout, stderr string, stdoutBytes, stderrBytes int64, truncated bool) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rec.StdoutBytes = stdoutBytes
	e.rec.StderrBytes = stderrBytes
	e.rec.StdoutTruncated = truncated
	e.rec.StderrTruncated = truncated
	if e.skipOutput {
		return
	}
	if p := preview(stdout); p != "" {
		e.rec.StdoutPreview = &p
	}
	if p := preview(stderr); p != "" {
		e.rec.StderrPreview = &p
	}
}

// SetStatus pins the terminal status, for a tool whose MCP call succeeds while
// the work it describes did not — kernel.exec returning a Python traceback, for
// instance. A deny still wins in Finish.
func (e *Entry) SetStatus(status string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status = status
}

// Finish closes the record. A call whose decision was a deny is always stored
// as StatusDenied regardless of the status passed in, so a denial can never be
// filed as a generic error.
func (e *Entry) Finish(ctx context.Context, status string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.finished {
		e.mu.Unlock()
		return
	}
	e.finished = true
	if e.status != "" {
		status = e.status
	}
	if e.rec.Decision == DecisionDeny {
		status = StatusDenied
	}
	e.rec.Finalize(status, e.rec.ExitCode, e.rec.StdoutBytes, e.rec.StderrBytes)
	rec := *e.rec
	inserted := e.inserted
	e.mu.Unlock()

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()

	var err error
	op := "update"
	if inserted {
		err = e.r.store.UpdateRecord(wctx, &rec)
	} else {
		op = "insert"
		err = e.r.store.InsertRecord(wctx, &rec)
	}
	if err != nil {
		e.r.metrics.AuditWrites.WithLabelValues(op, "failed").Inc()
		e.r.logger.Error("audit finalize failed",
			zap.String("tool", rec.Tool),
			zap.String("status", status),
			zap.Error(err),
		)
	} else {
		e.r.metrics.AuditWrites.WithLabelValues(op, "ok").Inc()
	}

	if e.r.mirror != nil {
		if err := e.r.mirror.append(&rec); err != nil {
			e.r.metrics.AuditWrites.WithLabelValues("mirror", "failed").Inc()
			e.r.logger.Error("audit mirror write failed", zap.Error(err))
		} else {
			e.r.metrics.AuditWrites.WithLabelValues("mirror", "ok").Inc()
		}
	}
}

// Tool reports the tool name on the record. Used in tests and logging.
func (e *Entry) Tool() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rec.Tool
}

func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	s := make(map[string]bool, len(items))
	for _, it := range items {
		s[it] = true
	}
	return s
}
