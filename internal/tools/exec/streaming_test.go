package exec

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artbegolli/anvilmcp/internal/config"
	"github.com/artbegolli/anvilmcp/internal/policy"
	"go.uber.org/zap"
)

// chunkCollector is a threadsafe ChunkFunc for tests.
type chunkCollector struct {
	mu     sync.Mutex
	chunks []string // "<stream>:<data>"
}

func (c *chunkCollector) fn(stream string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chunks = append(c.chunks, stream+":"+string(data))
}

func (c *chunkCollector) joined() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.chunks, "|")
}

func (c *chunkCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.chunks)
}

func TestStreamingWriterFlushOnThreshold(t *testing.T) {
	var sink bytes.Buffer
	lw := &limitedWriter{dst: &sink, limit: 1 << 20}
	col := &chunkCollector{}
	sw := newStreamingWriter(lw, "stdout", col.fn)
	defer sw.Close()

	big := bytes.Repeat([]byte("x"), streamFlushBytes)
	if _, err := sw.Write(big); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Threshold flush happens synchronously inside Write.
	if col.count() != 1 {
		t.Errorf("expected 1 chunk after threshold write, got %d", col.count())
	}
}

func TestStreamingWriterCloseFlushesRemainder(t *testing.T) {
	var sink bytes.Buffer
	lw := &limitedWriter{dst: &sink, limit: 1 << 20}
	col := &chunkCollector{}
	sw := newStreamingWriter(lw, "stdout", col.fn)

	if _, err := sw.Write([]byte("tail")); err != nil {
		t.Fatalf("write: %v", err)
	}
	sw.Close()

	if got := col.joined(); !strings.Contains(got, "stdout:tail") {
		t.Errorf("expected trailing chunk flushed on Close; got %q", got)
	}
	// Close is idempotent.
	sw.Close()
}

func TestStreamingWriterTickerFlushesQuietStream(t *testing.T) {
	var sink bytes.Buffer
	lw := &limitedWriter{dst: &sink, limit: 1 << 20}
	col := &chunkCollector{}
	sw := newStreamingWriter(lw, "stdout", col.fn)
	defer sw.Close()

	if _, err := sw.Write([]byte("one line")); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for col.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("ticker never flushed the pending chunk")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestStreamingWriterStopsAtTruncation(t *testing.T) {
	var sink bytes.Buffer
	lw := &limitedWriter{dst: &sink, limit: 8} // tiny cap
	col := &chunkCollector{}
	sw := newStreamingWriter(lw, "stdout", col.fn)

	// First write fills the cap and truncates; second write must not stream.
	if _, err := sw.Write([]byte("12345678")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := sw.Write([]byte("OVERFLOW")); err != nil {
		t.Fatalf("write: %v", err)
	}
	sw.Close()

	if got := col.joined(); strings.Contains(got, "OVERFLOW") {
		t.Errorf("streamed bytes past the capture cap: %q", got)
	}
}

func TestHandleShellStreamsChunks(t *testing.T) {
	logger := zap.NewNop()
	tmpdir := t.TempDir()

	sessConfig := &config.SessionConfig{
		SessionID:     "test",
		WorkspaceRoot: tmpdir,
		HomeDir:       tmpdir,
	}

	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: tmpdir},
		Paths: policy.PathsPolicy{
			WorkspaceRoot: tmpdir,
			AllowRead:     []string{tmpdir + "/**"},
			AllowWrite:    []string{tmpdir + "/**"},
		},
		Exec: policy.ExecPolicy{
			Default: "deny",
			Allow: map[string]policy.CommandPolicy{
				"sh": {},
			},
		},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger, sessConfig, 4, nil)

	col := &chunkCollector{}
	resp, err := tools.HandleShell(context.Background(), &ShellRequest{
		Cmd:     "sh",
		Argv:    []string{"-c", "echo out-line; echo err-line 1>&2"},
		OnChunk: col.fn,
	})
	if err != nil {
		t.Fatalf("HandleShell: %v", err)
	}

	// Chunks were delivered (Close flushes before the response is built, so
	// by the time HandleShell returns everything must have arrived).
	joined := col.joined()
	if !strings.Contains(joined, "stdout:") || !strings.Contains(joined, "out-line") {
		t.Errorf("expected stdout chunk with output; got %q", joined)
	}
	if !strings.Contains(joined, "stderr:") || !strings.Contains(joined, "err-line") {
		t.Errorf("expected stderr chunk; got %q", joined)
	}

	// Final response still carries the complete output.
	if !strings.Contains(resp.Stdout, "out-line") {
		t.Errorf("final response missing stdout; got %q", resp.Stdout)
	}
	if !strings.Contains(resp.Stderr, "err-line") {
		t.Errorf("final response missing stderr; got %q", resp.Stderr)
	}
}

func TestStreamingWriterPreservesOrder(t *testing.T) {
	// The ticker flush and Write's threshold flush run on different
	// goroutines; deliveries must still arrive in stream order. Hammer the
	// writer with sequential data and assert the chunk concatenation equals
	// the input exactly.
	var sink bytes.Buffer
	lw := &limitedWriter{dst: &sink, limit: 64 << 20}
	col := &chunkCollector{}
	sw := newStreamingWriter(lw, "stdout", func(stream string, data []byte) {
		col.mu.Lock()
		col.chunks = append(col.chunks, string(data))
		col.mu.Unlock()
	})

	var want bytes.Buffer
	line := []byte(strings.Repeat("0123456789", 100) + "\n") // ~1KB per write
	for i := 0; i < 20000; i++ {
		want.Write(line)
		if _, err := sw.Write(line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	sw.Close()

	col.mu.Lock()
	got := strings.Join(col.chunks, "")
	col.mu.Unlock()

	if got != want.String() {
		t.Fatalf("chunk concatenation diverged from input: got %d bytes, want %d bytes", len(got), want.Len())
	}
}

func TestStreamingWriterHoldsIncompleteUTF8(t *testing.T) {
	var sink bytes.Buffer
	lw := &limitedWriter{dst: &sink, limit: 1 << 20}
	col := &chunkCollector{}
	sw := newStreamingWriter(lw, "stdout", col.fn)

	// "é" is 0xC3 0xA9. Write the first byte, force a non-final delivery, and
	// the incomplete sequence must be held back rather than split.
	if _, err := sw.Write([]byte{'a', 0xC3}); err != nil {
		t.Fatalf("write: %v", err)
	}
	sw.deliver(false)
	if got := col.joined(); got != "stdout:a" {
		t.Fatalf("expected only complete runes delivered, got %q", got)
	}

	// Completing the rune and closing flushes the rest.
	if _, err := sw.Write([]byte{0xA9}); err != nil {
		t.Fatalf("write: %v", err)
	}
	sw.Close()
	if got := col.joined(); got != "stdout:a|stdout:é" {
		t.Fatalf("expected held-back bytes delivered on close, got %q", got)
	}
}

func TestHandleShellBudgetStopsStreaming(t *testing.T) {
	logger := zap.NewNop()
	tmpdir := t.TempDir()

	sessConfig := &config.SessionConfig{SessionID: "test", WorkspaceRoot: tmpdir, HomeDir: tmpdir}
	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: tmpdir},
		Paths:   policy.PathsPolicy{WorkspaceRoot: tmpdir, AllowRead: []string{tmpdir + "/**"}, AllowWrite: []string{tmpdir + "/**"}},
		Exec:    policy.ExecPolicy{Default: "deny", Allow: map[string]policy.CommandPolicy{"sh": {}}},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger, sessConfig, 4, nil)
	tools.maxTotalBytes = 16 // tiny session budget

	col := &chunkCollector{}
	_, err := tools.HandleShell(context.Background(), &ShellRequest{
		Cmd:     "sh",
		Argv:    []string{"-c", "printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'"}, // 40 bytes > budget
		OnChunk: col.fn,
	})
	if err == nil {
		t.Fatal("expected budget error from HandleShell")
	}
	if !strings.Contains(err.Error(), "output exceeded") && !strings.Contains(err.Error(), "budget") {
		t.Errorf("expected budget-related error, got: %v", err)
	}

	// Streamed bytes must not exceed the budget: chunks past it are dropped.
	var streamed int
	col.mu.Lock()
	for _, c := range col.chunks {
		streamed += len(c) - len("stdout:")
	}
	col.mu.Unlock()
	if streamed > 16 {
		t.Errorf("streamed %d bytes past a 16-byte session budget", streamed)
	}

	// A subsequent call must be refused before the process even runs.
	_, err = tools.HandleShell(context.Background(), &ShellRequest{
		Cmd:  "sh",
		Argv: []string{"-c", "echo more"},
	})
	if err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Errorf("expected upfront budget refusal, got: %v", err)
	}
}

func TestHandleShellNoCallbackStaysBuffered(t *testing.T) {
	logger := zap.NewNop()
	tmpdir := t.TempDir()

	sessConfig := &config.SessionConfig{SessionID: "test", WorkspaceRoot: tmpdir, HomeDir: tmpdir}
	p := &policy.Policy{
		Version: 1,
		Session: policy.SessionPolicy{CWD: tmpdir},
		Paths:   policy.PathsPolicy{WorkspaceRoot: tmpdir, AllowRead: []string{tmpdir + "/**"}, AllowWrite: []string{tmpdir + "/**"}},
		Exec:    policy.ExecPolicy{Default: "deny", Allow: map[string]policy.CommandPolicy{"echo": {}}},
	}

	engine := policy.NewEngine(p, "")
	tools := NewTools(engine, logger, sessConfig, 4, nil)

	resp, err := tools.HandleShell(context.Background(), &ShellRequest{
		Cmd:  "echo",
		Argv: []string{"buffered"},
	})
	if err != nil {
		t.Fatalf("HandleShell: %v", err)
	}
	if !strings.Contains(resp.Stdout, "buffered") {
		t.Errorf("expected buffered output; got %q", resp.Stdout)
	}
}
