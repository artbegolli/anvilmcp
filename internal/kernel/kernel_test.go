package kernel

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	m, err := NewManager(Options{PythonPath: "python3"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(m.ShutdownAll)
	return m
}

func startOne(t *testing.T, m *Manager) string {
	t.Helper()
	st, err := m.Start(context.Background(), StartSpec{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return st.ID
}

func TestKernelStatePersistsAcrossExecs(t *testing.T) {
	m := newTestManager(t)
	id := startOne(t, m)
	ctx := context.Background()

	if _, err := m.Execute(ctx, id, "x = 41", 0, nil); err != nil {
		t.Fatalf("exec 1: %v", err)
	}
	res, err := m.Execute(ctx, id, "x += 1\nprint('value', x)\nx", 0, nil)
	if err != nil {
		t.Fatalf("exec 2: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	if !strings.Contains(res.Stdout, "value 42") {
		t.Errorf("stdout = %q, want it to contain 'value 42'", res.Stdout)
	}
	if res.ResultRepr == nil || *res.ResultRepr != "42" {
		t.Errorf("result_repr = %v, want 42 (state did not persist)", res.ResultRepr)
	}
	if res.ExecutionCount != 2 {
		t.Errorf("execution_count = %d, want 2", res.ExecutionCount)
	}
}

func TestKernelReportsErrors(t *testing.T) {
	m := newTestManager(t)
	id := startOne(t, m)

	res, err := m.Execute(context.Background(), id, "1/0", 0, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("status = %q, want error", res.Status)
	}
	if res.Ename != "ZeroDivisionError" {
		t.Errorf("ename = %q, want ZeroDivisionError", res.Ename)
	}
	if !strings.Contains(res.Traceback, "ZeroDivisionError") {
		t.Errorf("traceback missing exception: %q", res.Traceback)
	}
}

func TestKernelStreamsOutput(t *testing.T) {
	m := newTestManager(t)
	id := startOne(t, m)

	var mu sync.Mutex
	var chunks []string
	onChunk := func(stream, text string) {
		mu.Lock()
		chunks = append(chunks, stream+":"+text)
		mu.Unlock()
	}
	res, err := m.Execute(context.Background(), id,
		"import sys\nprint('to out')\nprint('to err', file=sys.stderr)", 0, onChunk)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q", res.Status)
	}
	mu.Lock()
	joined := strings.Join(chunks, "|")
	mu.Unlock()
	if !strings.Contains(joined, "stdout:to out") {
		t.Errorf("missing stdout chunk: %q", joined)
	}
	if !strings.Contains(joined, "stderr:to err") {
		t.Errorf("missing stderr chunk: %q", joined)
	}
}

// TestKernelInterruptKeepsKernelAlive: a sleeping cell responds to SIGINT, so
// the exec comes back "interrupted" and the kernel survives for reuse.
func TestKernelInterruptKeepsKernelAlive(t *testing.T) {
	m := newTestManager(t)
	id := startOne(t, m)
	ctx := context.Background()

	start := time.Now()
	res, err := m.Execute(ctx, id, "import time\ntime.sleep(30)", 1*time.Second, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if time.Since(start) > 8*time.Second {
		t.Fatalf("interrupt took too long: %v", time.Since(start))
	}
	if res.Status != "interrupted" {
		t.Errorf("status = %q, want interrupted", res.Status)
	}
	// Kernel must still work.
	res2, err := m.Execute(ctx, id, "1 + 1", 0, nil)
	if err != nil {
		t.Fatalf("post-interrupt exec: %v", err)
	}
	if res2.ResultRepr == nil || *res2.ResultRepr != "2" {
		t.Errorf("kernel not usable after interrupt: %v", res2.ResultRepr)
	}
}

// TestKernelTimeoutKillsUnresponsiveCell: a cell that ignores SIGINT is killed
// after the grace window; the kernel is then dead.
func TestKernelTimeoutKillsUnresponsiveCell(t *testing.T) {
	m := newTestManager(t)
	id := startOne(t, m)
	ctx := context.Background()

	old := interruptGrace
	interruptGrace = 500 * time.Millisecond
	defer func() { interruptGrace = old }()

	code := "import signal, time\nsignal.signal(signal.SIGINT, signal.SIG_IGN)\nwhile True: time.sleep(0.05)"
	start := time.Now()
	res, err := m.Execute(ctx, id, code, 1*time.Second, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Status != "timeout" {
		t.Errorf("status = %q, want timeout", res.Status)
	}
	if time.Since(start) > 6*time.Second {
		t.Errorf("kill took too long: %v", time.Since(start))
	}
	// Kernel should now be dead.
	st, _ := m.Status(id)
	if st.State != "dead" {
		t.Errorf("state = %q, want dead", st.State)
	}
	if _, err := m.Execute(ctx, id, "1", 0, nil); err == nil {
		t.Error("expected exec on dead kernel to fail")
	}
}

func TestKernelRejectsConcurrentExec(t *testing.T) {
	m := newTestManager(t)
	id := startOne(t, m)
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.Execute(ctx, id, "import time\ntime.sleep(2)", 10*time.Second, nil)
	}()

	// Wait for the first exec to be in flight.
	deadline := time.After(3 * time.Second)
	for {
		st, _ := m.Status(id)
		if st.State == "busy" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first exec never became busy")
		case <-time.After(20 * time.Millisecond):
		}
	}

	_, err := m.Execute(ctx, id, "1", 0, nil)
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("concurrent exec err = %v, want a busy error", err)
	}
	<-done
}

func TestKernelShutdownRemovesKernel(t *testing.T) {
	m := newTestManager(t)
	id := startOne(t, m)

	if err := m.Shutdown(id); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if _, err := m.Status(id); err == nil {
		t.Error("expected status on shut-down kernel to fail")
	}
	if len(m.List()) != 0 {
		t.Errorf("expected empty list after shutdown, got %d", len(m.List()))
	}
}

func TestManagerListOrdersByID(t *testing.T) {
	m := newTestManager(t)
	a := startOne(t, m)
	b := startOne(t, m)
	list := m.List()
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	if list[0].ID != a || list[1].ID != b {
		t.Errorf("list order = %s,%s want %s,%s", list[0].ID, list[1].ID, a, b)
	}
}
