package exec

import (
	"sync"
	"time"
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

	mu      sync.Mutex
	pending []byte
	closed  bool

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
				sw.flush()
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
	var chunk []byte
	if len(sw.pending) >= streamFlushBytes {
		chunk = sw.pending
		sw.pending = nil
	}
	sw.mu.Unlock()

	if len(chunk) > 0 {
		sw.onChunk(sw.stream, chunk)
	}
	return n, err
}

// flush delivers any pending bytes.
func (sw *streamingWriter) flush() {
	sw.mu.Lock()
	chunk := sw.pending
	sw.pending = nil
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
	sw.flush()
}
