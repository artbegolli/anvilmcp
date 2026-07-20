package exec

import (
	"sync"
	"time"
	"unicode/utf8"
)

// ChunkFunc receives incremental output from a running process. stream is
// "stdout" or "stderr"; data is a batched chunk of raw output. Callbacks are
// invoked from the writer goroutine and must not block for long — slow
// consumers delay the subprocess's writes.
type ChunkFunc func(stream string, data []byte)

const (
	// streamFlushBytes is the pending-buffer size that forces a flush.
	// 32KB batches keep notification overhead negligible for chatty
	// processes while still feeling incremental.
	streamFlushBytes = 32 * 1024
	// streamFlushInterval bounds the latency of a flush for quiet
	// processes (e.g. a test runner printing one line per second).
	streamFlushInterval = 150 * time.Millisecond
)

// streamingWriter tees writes into the wrapped limitedWriter (which remains
// the source of truth for capture and truncation) and batches copies of the
// same bytes for delivery via onChunk. Batching is by size (streamFlushBytes)
// with a ticker bounding flush latency (streamFlushInterval).
//
// Streaming honors the limitedWriter's cap: once the underlying writer
// truncates, no further chunks are forwarded, so a client can never receive
// more via notifications than the final response accounts for.
type streamingWriter struct {
	dst     *limitedWriter
	stream  string
	onChunk ChunkFunc

	// mu guards pending/closed. deliverMu serializes pop+deliver as a unit:
	// without it, the ticker flush and Write's threshold flush could each pop
	// a chunk and then race to invoke onChunk, delivering stream content out
	// of order.
	mu        sync.Mutex
	pending   []byte
	closed    bool
	deliverMu sync.Mutex

	stopTicker chan struct{}
	tickerDone chan struct{}
}

func newStreamingWriter(dst *limitedWriter, stream string, onChunk ChunkFunc) *streamingWriter {
	sw := &streamingWriter{
		dst:        dst,
		stream:     stream,
		onChunk:    onChunk,
		stopTicker: make(chan struct{}),
		tickerDone: make(chan struct{}),
	}

	go func() {
		defer close(sw.tickerDone)
		ticker := time.NewTicker(streamFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sw.deliver(false)
			case <-sw.stopTicker:
				return
			}
		}
	}()

	return sw
}

// Write implements io.Writer. The underlying limitedWriter always reports
// len(p) so the subprocess never sees a short write; we mirror that here.
func (sw *streamingWriter) Write(p []byte) (int, error) {
	n, err := sw.dst.Write(p)

	sw.mu.Lock()
	if !sw.closed && !sw.dst.isTruncated() {
		sw.pending = append(sw.pending, p...)
	}
	over := len(sw.pending) >= streamFlushBytes
	sw.mu.Unlock()

	if over {
		sw.deliver(false)
	}
	return n, err
}

// deliver pops the pending buffer and invokes onChunk with it. Pop and
// delivery happen under deliverMu as one unit so concurrent deliveries
// (ticker vs threshold) can't reorder chunks. Unless final, a trailing
// incomplete UTF-8 sequence is held back so multi-byte runes aren't split
// across chunk boundaries (which would surface as U+FFFD on the client).
func (sw *streamingWriter) deliver(final bool) {
	sw.deliverMu.Lock()
	defer sw.deliverMu.Unlock()

	sw.mu.Lock()
	chunk := sw.pending
	sw.pending = nil
	if !final {
		if hold := trailingIncompleteUTF8(chunk); hold > 0 {
			sw.pending = append(sw.pending, chunk[len(chunk)-hold:]...)
			chunk = chunk[:len(chunk)-hold]
		}
	}
	sw.mu.Unlock()

	if len(chunk) > 0 {
		sw.onChunk(sw.stream, chunk)
	}
}

// Close stops the ticker and delivers the final pending chunk. Call after the
// process has exited and before building the final response, so the last
// chunk reaches the client while the request is still open.
func (sw *streamingWriter) Close() {
	sw.mu.Lock()
	if sw.closed {
		sw.mu.Unlock()
		return
	}
	sw.closed = true
	sw.mu.Unlock()

	close(sw.stopTicker)
	<-sw.tickerDone
	sw.deliver(true)
}

// trailingIncompleteUTF8 returns how many trailing bytes of b form the start
// of a UTF-8 sequence whose continuation hasn't arrived yet (0..3). Invalid
// or binary tails return 0 — they're passed through rather than held forever.
func trailingIncompleteUTF8(b []byte) int {
	n := len(b)
	if n == 0 {
		return 0
	}

	// Walk back over continuation bytes (10xxxxxx), at most 3.
	start := n - 1
	for start >= 0 && n-start < utf8.UTFMax && b[start]&0xC0 == 0x80 {
		start--
	}
	if start < 0 || b[start]&0xC0 == 0x80 {
		return 0 // nothing but continuations: not a truncated sequence we can fix
	}

	var expected int
	switch {
	case b[start]&0x80 == 0x00:
		expected = 1
	case b[start]&0xE0 == 0xC0:
		expected = 2
	case b[start]&0xF0 == 0xE0:
		expected = 3
	case b[start]&0xF8 == 0xF0:
		expected = 4
	default:
		return 0 // invalid start byte: pass through
	}

	got := n - start
	if got < expected {
		return got // sequence is still incomplete: hold it back
	}
	return 0 // complete (or overlong garbage): pass through
}
