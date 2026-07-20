package server

import (
	"errors"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/artbegolli/anvilmcp/internal/metrics"
)

func TestProgressChunkFuncMonotonicUnderConcurrency(t *testing.T) {
	m := metrics.NewNoop()

	var mu sync.Mutex
	var seen []int64
	send := func(params map[string]any) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, params["progress"].(int64))
		return nil
	}

	fn := progressChunkFunc("tok", send, m)

	// stdout and stderr streamingWriters invoke the callback from separate
	// goroutines in production; simulate that.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		stream := []string{"stdout", "stderr"}[i]
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				fn(stream, []byte("xxxx"))
			}
		}()
	}
	wg.Wait()

	if len(seen) != 1000 {
		t.Fatalf("expected 1000 notifications, got %d", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Fatalf("progress not monotonic at index %d: %d then %d", i, seen[i-1], seen[i])
		}
	}

	if got := testutil.ToFloat64(m.ExecStreamChunks.WithLabelValues("sent")); got != 1000 {
		t.Errorf("expected 1000 sent chunks, got %v", got)
	}
}

func TestProgressChunkFuncCountsFailures(t *testing.T) {
	m := metrics.NewNoop()
	send := func(params map[string]any) error { return errors.New("session gone") }

	fn := progressChunkFunc("tok", send, m)
	fn("stdout", []byte("dropped"))

	if got := testutil.ToFloat64(m.ExecStreamChunks.WithLabelValues("failed")); got != 1 {
		t.Errorf("expected 1 failed chunk, got %v", got)
	}
	if got := testutil.ToFloat64(m.ExecStreamChunks.WithLabelValues("sent")); got != 0 {
		t.Errorf("expected 0 sent chunks, got %v", got)
	}
}
