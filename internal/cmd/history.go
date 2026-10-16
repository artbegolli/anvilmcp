package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/config"
	"github.com/artbegolli/anvilmcp/internal/history"
	"github.com/artbegolli/anvilmcp/internal/policy"
)

// historyFlags carries the database location, resolved the same way the server
// resolves it so `anvilmcp history` reads the file `anvilmcp serve` writes:
// the policy's audit.history_db_path wins over --history-db, and a leading ~
// is expanded.
type historyFlags struct {
	dbPath      string
	policyPath  string
	clusterPath string
}

func (f *historyFlags) register(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&f.dbPath, "history-db", os.ExpandEnv("$HOME/.local/state/anvilmcp/history.db"), "Path to the history database")
	cmd.PersistentFlags().StringVar(&f.policyPath, "policy", os.ExpandEnv("$HOME/.config/anvilmcp/config.yaml"), "Path to user policy file")
	cmd.PersistentFlags().StringVar(&f.clusterPath, "cluster-policy", "/etc/anvilmcp/config.yaml", "Path to cluster policy file")
}

// open resolves the database path and opens it read-only.
func (f *historyFlags) open(ctx context.Context) (*history.Store, error) {
	path := config.ExpandUser(f.dbPath)

	// A missing or unreadable policy is not fatal here: the flag default is
	// still a usable answer, and refusing to show history because the policy
	// moved would be unhelpful exactly when you need the trail.
	if loader, err := policy.NewLoader(f.policyPath, f.clusterPath); err == nil {
		if p := loader.Effective().Audit.HistoryDBPath; p != "" {
			path = config.ExpandUser(p)
		}
	}

	return history.OpenReadOnly(ctx, path, zap.NewNop())
}

func NewHistoryCommand() *cobra.Command {
	flags := &historyFlags{}

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Inspect the audit trail of tool and resource calls",
		Long: `Read the audit trail written by anvilmcp serve.

Every tool and resource invocation is recorded with its arguments, the policy
decision that admitted or refused it, and its outcome. Records whose decision
is "unmediated" were evaluated by no chokepoint -- kernel execution, for
instance -- and are the ones worth reviewing most closely.`,
	}
	flags.register(cmd)

	cmd.AddCommand(newHistoryListCmd(flags))
	cmd.AddCommand(newHistoryShowCmd(flags))
	cmd.AddCommand(newHistoryTailCmd(flags))

	return cmd
}

// ---------------------------------------------------------------------------
// history list

func newHistoryListCmd(flags *historyFlags) *cobra.Command {
	var (
		limit        int
		tool         string
		status       string
		decision     string
		since        string
		asJSON       bool
		deniedOnly   bool
		unmediatedOn bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent calls, newest first",
		Example: `  anvilmcp history list
  anvilmcp history list --denied
  anvilmcp history list --unmediated --since 24h
  anvilmcp history list --tool exec.shell --limit 100 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := printer{w: cmd.OutOrStdout()}

			if deniedOnly {
				status = "denied"
			}
			if unmediatedOn {
				decision = "unmediated"
			}

			filter := history.Filter{
				Tool:     tool,
				Status:   status,
				Decision: decision,
				Limit:    limit,
			}
			if since != "" {
				d, err := time.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("invalid --since %q: %w (expected a duration like 30m, 24h)", since, err)
				}
				filter.Since = time.Now().Add(-d)
			}

			store, err := flags.open(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			records, err := store.Query(cmd.Context(), filter)
			if err != nil {
				return fmt.Errorf("query history: %w", err)
			}

			if asJSON {
				return writeJSON(cmd.OutOrStdout(), recordsToJSON(records))
			}
			if len(records) == 0 {
				out.println("No matching records.")
				return nil
			}
			writeTable(out, records)
			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", history.DefaultLimit, "Maximum records to return")
	cmd.Flags().StringVar(&tool, "tool", "", "Only this tool (e.g. exec.shell)")
	cmd.Flags().StringVar(&status, "status", "", "Only this status (running, ok, error, denied, killed)")
	cmd.Flags().StringVar(&decision, "decision", "", "Only this decision (allow, deny, unmediated)")
	cmd.Flags().StringVar(&since, "since", "", "Only calls newer than this duration ago (e.g. 30m, 24h)")
	cmd.Flags().BoolVar(&deniedOnly, "denied", false, "Shorthand for --status denied")
	cmd.Flags().BoolVar(&unmediatedOn, "unmediated", false, "Shorthand for --decision unmediated: calls no chokepoint evaluated")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of a table")

	return cmd
}

// ---------------------------------------------------------------------------
// history show

func newHistoryShowCmd(flags *historyFlags) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one call in full, including arguments and output previews",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := flags.open(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			rec, err := store.GetRecord(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("record %s: %w", args[0], err)
			}

			if asJSON {
				return writeJSON(cmd.OutOrStdout(), recordToJSON(rec))
			}
			writeDetail(printer{w: cmd.OutOrStdout()}, rec)
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of a detail view")
	return cmd
}

// ---------------------------------------------------------------------------
// history tail

func newHistoryTailCmd(flags *historyFlags) *cobra.Command {
	var (
		interval time.Duration
		asJSON   bool
	)

	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Follow calls as they happen",
		Long: `Follow the audit trail live.

A line is printed when a call starts and again when it finishes, so a call
that hangs is visible as a "running" line that never gets a terminal partner.
Polls the database; there is no push channel.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := flags.open(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			return tailLoop(cmd.Context(), store, printer{w: cmd.OutOrStdout()}, interval, asJSON)
		},
	}

	cmd.Flags().DurationVar(&interval, "interval", 500*time.Millisecond, "Poll interval")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit one JSON object per line")
	return cmd
}

// tailLoop polls for new and newly-completed records until ctx is cancelled.
//
// Two queries per tick, because a completion does not move ts_start: new
// records come from a time window, while records already seen as running are
// re-read by id. Filtering on the window alone would print the start of a long
// call and never its outcome, which is the half that matters.
func tailLoop(ctx context.Context, store *history.Store, out printer, interval time.Duration, asJSON bool) error {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}

	// Start from now: tailing a month-old database should not replay it.
	watermark := time.Now()

	// seen maps record id to the status last printed. Entries for in-flight
	// calls are held until they reach a terminal status; terminal ones are
	// dropped on the next tick, so the map stays bounded by concurrency.
	seen := map[string]string{}

	emit := func(rec *history.Record) error {
		if prev, known := seen[rec.ID]; known && prev == rec.Status {
			return nil
		}
		seen[rec.ID] = rec.Status
		if asJSON {
			return writeJSON(out.w, recordToJSON(rec))
		}
		out.println(tailLine(rec))
		return nil
	}

	// Seed with whatever is already in flight. Without this, starting a tail
	// while a long call is running would show neither its start (ts_start is
	// behind the watermark) nor its completion (nothing is tracking it) -- the
	// call would be invisible for its whole life.
	inflight, err := store.Query(ctx, history.Filter{Status: "running", Limit: 100})
	if err != nil {
		return tailErr(ctx, err)
	}
	for i := len(inflight) - 1; i >= 0; i-- {
		if err := emit(inflight[i]); err != nil {
			return err
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		// Capture the poll time before querying so a record written during the
		// query is re-read next tick rather than skipped; the seen map makes
		// the overlap harmless.
		polledAt := time.Now()

		records, err := store.Query(ctx, history.Filter{Since: watermark, Limit: 500})
		if err != nil {
			return tailErr(ctx, err)
		}
		// Query returns newest first; print oldest first so the feed reads
		// forward in time.
		for i := len(records) - 1; i >= 0; i-- {
			if err := emit(records[i]); err != nil {
				return err
			}
		}

		// Re-read anything still in flight. Bounded by exec.max_concurrent
		// plus live kernels, so this stays a handful of point lookups.
		for id, status := range seen {
			if status != "running" {
				delete(seen, id)
				continue
			}
			rec, err := store.GetRecord(ctx, id)
			if err != nil {
				// Pruned out from under us, or gone: stop tracking it.
				delete(seen, id)
				continue
			}
			if err := emit(rec); err != nil {
				return err
			}
		}

		watermark = polledAt

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return nil
		}
	}
}

// tailErr swallows the error a cancelled query returns. Ctrl-C lands here as a
// "context canceled" from whichever statement was mid-flight; that is the user
// asking to stop, not a failure to report.
func tailErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return fmt.Errorf("tail history: %w", err)
}

// ---------------------------------------------------------------------------
// rendering

func writeTable(out printer, records []*history.Record) {
	out.printf("%-12s  %-8s  %-18s  %-10s  %-11s  %-9s  %s\n",
		"ID", "TIME", "TOOL", "DECISION", "STATUS", "DURATION", "SUMMARY")
	for _, r := range records {
		out.printf("%-12s  %-8s  %-18s  %-10s  %-11s  %-9s  %s\n",
			shortID(r.ID),
			time.Unix(0, r.TimestampStart).Format("15:04:05"),
			truncateCell(r.Tool, 18),
			truncateCell(r.Decision, 10),
			statusCell(r),
			durationCell(r),
			truncateCell(summarize(r), 48),
		)
	}
}

func writeDetail(out printer, r *history.Record) {
	out.printf("id            %s\n", r.ID)
	out.printf("tool          %s\n", r.Tool)
	out.printf("started       %s\n", time.Unix(0, r.TimestampStart).Format(time.RFC3339Nano))
	if r.TimestampEnd != nil {
		out.printf("ended         %s\n", time.Unix(0, *r.TimestampEnd).Format(time.RFC3339Nano))
	}
	if r.DurationMS != nil {
		out.printf("duration      %s\n", time.Duration(*r.DurationMS).Round(time.Millisecond))
	}
	out.printf("status        %s\n", r.Status)
	out.printf("decision      %s", r.Decision)
	if r.Decision == "unmediated" {
		out.printf("   (no chokepoint evaluated this call)")
	}
	out.println()
	if r.PolicyRule != nil {
		out.printf("policy rule   %s\n", *r.PolicyRule)
	}
	if r.ExitCode != nil {
		out.printf("exit code     %d", *r.ExitCode)
		if *r.ExitCode == 124 {
			out.printf("   (timeout)")
		}
		out.println()
	}
	if r.CWD != "" {
		out.printf("cwd           %s\n", r.CWD)
	}
	if r.ResolvedPath != nil && *r.ResolvedPath != "" {
		out.printf("resolved path %s\n", *r.ResolvedPath)
	}
	out.printf("session       %s\n", r.MCPSessionID)

	out.println()
	out.printf("arguments\n%s\n", indent(prettyJSON(r.ArgsJSON), "  "))

	if r.StdoutPreview != nil && *r.StdoutPreview != "" {
		out.printf("\nstdout (%d bytes%s)\n%s\n", r.StdoutBytes, truncatedNote(r.StdoutTruncated), indent(*r.StdoutPreview, "  "))
	} else if r.StdoutBytes > 0 {
		out.printf("\nstdout        %d bytes (preview not stored)\n", r.StdoutBytes)
	}
	if r.StderrPreview != nil && *r.StderrPreview != "" {
		out.printf("\nstderr (%d bytes%s)\n%s\n", r.StderrBytes, truncatedNote(r.StderrTruncated), indent(*r.StderrPreview, "  "))
	} else if r.StderrBytes > 0 {
		out.printf("\nstderr        %d bytes (preview not stored)\n", r.StderrBytes)
	}
}

func tailLine(r *history.Record) string {
	return fmt.Sprintf("%s  %-12s  %-18s  %-10s  %-11s  %s",
		time.Now().Format("15:04:05"),
		shortID(r.ID),
		truncateCell(r.Tool, 18),
		truncateCell(r.Decision, 10),
		statusCell(r),
		truncateCell(summarize(r), 60),
	)
}

// summarize pulls the most identifying argument out of the stored JSON: the
// command for an exec, the path for a filesystem call, the URI for a resource.
func summarize(r *history.Record) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(r.ArgsJSON), &args); err != nil {
		return ""
	}
	if cmd, ok := args["cmd"].(string); ok {
		parts := []string{cmd}
		if argv, ok := args["argv"].([]any); ok {
			for _, a := range argv {
				if s, ok := a.(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, " ")
	}
	for _, key := range []string{"path", "uri", "code", "content", "kernel_id"} {
		if v, ok := args[key].(string); ok {
			return strings.ReplaceAll(v, "\n", "⏎")
		}
	}
	if args["_audit_truncated"] == true {
		return "[arguments too large; digest stored]"
	}
	return ""
}

func statusCell(r *history.Record) string {
	if r.Status == "denied" {
		return "denied ✗"
	}
	if r.Status == "error" {
		return "error"
	}
	return r.Status
}

func durationCell(r *history.Record) string {
	if r.DurationMS == nil {
		return "-"
	}
	return time.Duration(*r.DurationMS).Round(time.Millisecond).String()
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func truncateCell(s string, width int) string {
	if len(s) <= width {
		return s
	}
	if width <= 1 {
		return s[:width]
	}
	return s[:width-1] + "…"
}

func truncatedNote(truncated bool) string {
	if truncated {
		return ", truncated"
	}
	return ""
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func prettyJSON(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return raw
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// JSON output

// recordJSON is the CLI's --json shape. Declared explicitly rather than
// marshalling history.Record so the field names are a stable interface for
// whatever consumes them.
type recordJSON struct {
	ID            string  `json:"id"`
	Tool          string  `json:"tool"`
	MCPSessionID  string  `json:"mcp_session_id"`
	CorrelationID string  `json:"correlation_id"`
	TSStart       string  `json:"ts_start"`
	TSEnd         *string `json:"ts_end,omitempty"`
	DurationMS    *int64  `json:"duration_ms,omitempty"`
	Status        string  `json:"status"`
	Decision      string  `json:"decision"`
	PolicyRule    *string `json:"policy_rule,omitempty"`
	ExitCode      *int    `json:"exit_code,omitempty"`
	CWD           string  `json:"cwd,omitempty"`
	ResolvedPath  *string `json:"resolved_path,omitempty"`
	Args          any     `json:"args"`
	StdoutBytes   int64   `json:"stdout_bytes"`
	StderrBytes   int64   `json:"stderr_bytes"`
	StdoutPreview *string `json:"stdout_preview,omitempty"`
	StderrPreview *string `json:"stderr_preview,omitempty"`
	Truncated     bool    `json:"truncated,omitempty"`
}

func recordToJSON(r *history.Record) recordJSON {
	out := recordJSON{
		ID:            r.ID,
		Tool:          r.Tool,
		MCPSessionID:  r.MCPSessionID,
		CorrelationID: r.CorrelationID,
		TSStart:       time.Unix(0, r.TimestampStart).Format(time.RFC3339Nano),
		DurationMS:    r.DurationMS,
		Status:        r.Status,
		Decision:      r.Decision,
		PolicyRule:    r.PolicyRule,
		ExitCode:      r.ExitCode,
		CWD:           r.CWD,
		ResolvedPath:  r.ResolvedPath,
		StdoutBytes:   r.StdoutBytes,
		StderrBytes:   r.StderrBytes,
		StdoutPreview: r.StdoutPreview,
		StderrPreview: r.StderrPreview,
		Truncated:     r.StdoutTruncated || r.StderrTruncated,
	}
	if r.TimestampEnd != nil {
		s := time.Unix(0, *r.TimestampEnd).Format(time.RFC3339Nano)
		out.TSEnd = &s
	}
	// Re-embed the stored arguments as JSON rather than a quoted string, so
	// `anvilmcp history list --json | jq '.[].args.cmd'` works.
	var args any
	if err := json.Unmarshal([]byte(r.ArgsJSON), &args); err == nil {
		out.Args = args
	} else {
		out.Args = r.ArgsJSON
	}
	return out
}

func recordsToJSON(records []*history.Record) []recordJSON {
	out := make([]recordJSON, 0, len(records))
	for _, r := range records {
		out = append(out, recordToJSON(r))
	}
	return out
}

func writeJSON(w interface{ Write([]byte) (int, error) }, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
