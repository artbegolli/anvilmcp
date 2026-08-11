package kernel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.uber.org/zap"
)

const (
	// waitDelay bounds cmd.Wait after the process is signalled (same backstop
	// as internal/tools/exec).
	waitDelay = 10 * time.Second
	// handshakeTimeout bounds how long we wait for the harness "ready" frame.
	handshakeTimeout = 15 * time.Second
)

// interruptGrace is how long we wait for a cell to unwind after SIGINT before
// escalating to SIGKILL. A var (not const) so tests can shrink it.
var interruptGrace = 5 * time.Second

// errBusy is returned when an execution is already in flight on a kernel.
var errBusy = errors.New("kernel busy: an execution is already in progress")

var execSeq atomic.Uint64

// ExecResult is the outcome of a single kernel execution.
type ExecResult struct {
	Status         string // ok | error | interrupted | timeout
	ExecutionCount int
	Stdout         string
	Stderr         string
	ResultRepr     *string
	Ename          string
	Evalue         string
	Traceback      string
}

// Status is a point-in-time snapshot of a kernel.
type Status struct {
	ID             string
	Env            string
	Python         string
	State          string // idle | busy | dead
	ExecutionCount int
	StartedAt      time.Time
	LastActivity   time.Time
	DeadReason     string
}

// Kernel is a single running harness process.
type Kernel struct {
	id     string
	env    string
	python string
	cmd    *exec.Cmd
	conn   net.Conn
	stderr *bytes.Buffer // harness's own stderr, for crash diagnostics
	logger *zap.Logger

	execMu sync.Mutex // single-flight: one execution at a time (TryLock => reject-busy)

	stateMu    sync.Mutex
	busy       bool
	execCount  int
	startedAt  time.Time
	lastAct    time.Time
	dead       bool
	deadReason string
}

// startKernel launches argv (interpreter + harness + "--fd 3"), wires an
// AF_UNIX socketpair to the child's fd 3, and waits for the ready handshake.
func startKernel(id, envLabel string, argv []string, logger *zap.Logger) (*Kernel, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("socketpair: %w", err)
	}
	parentFile := os.NewFile(uintptr(fds[0]), "kernel-parent")
	childFile := os.NewFile(uintptr(fds[1]), "kernel-child")

	var stderr bytes.Buffer
	cmd := exec.Command(argv[0], argv[1:]...) // #nosec G204 -- launcher argv is policy-gated by the caller
	cmd.ExtraFiles = []*os.File{childFile}    // -> fd 3 in the child
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay
	cmd.Stdin = nil
	cmd.Stdout = nil // user output travels as frames; the harness never writes fd 1
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		_ = parentFile.Close()
		_ = childFile.Close()
		return nil, fmt.Errorf("start harness: %w", err)
	}
	_ = childFile.Close() // parent releases the child's end so EOF propagates on child death

	conn, err := net.FileConn(parentFile)
	_ = parentFile.Close() // FileConn dup'd the fd
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, fmt.Errorf("wrap kernel socket: %w", err)
	}

	k := &Kernel{
		id: id, env: envLabel, cmd: cmd, conn: conn, stderr: &stderr, logger: logger,
		startedAt: time.Now(), lastAct: time.Now(),
	}

	// One reaper goroutine owns cmd.Wait: it marks the kernel dead whenever the
	// process exits (crash, kill, or clean shutdown) and closes the conn.
	go k.reap()

	if err := k.readReady(); err != nil {
		k.forceKill("handshake failed")
		return nil, err
	}
	return k, nil
}

func (k *Kernel) readReady() error {
	_ = k.conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	defer func() { _ = k.conn.SetReadDeadline(time.Time{}) }()
	msg, err := readFrame(k.conn)
	if err != nil {
		return fmt.Errorf("kernel handshake: %w (stderr: %s)", err, strings.TrimSpace(k.stderr.String()))
	}
	if msg.Type != "ready" {
		return fmt.Errorf("kernel handshake: unexpected frame %q", msg.Type)
	}
	k.python = msg.Python
	return nil
}

func (k *Kernel) reap() {
	werr := k.cmd.Wait()
	k.stateMu.Lock()
	if !k.dead {
		k.dead = true
		k.deadReason = "process exited unexpectedly"
		if werr != nil {
			k.deadReason = "process exited: " + werr.Error()
		}
	}
	k.stateMu.Unlock()
	_ = k.conn.Close()
}

// Execute runs code in the kernel and blocks until it finishes, is interrupted,
// or times out. onChunk (may be nil) receives streamed output as it arrives.
// Only one execution runs at a time; a concurrent call returns errBusy.
func (k *Kernel) Execute(ctx context.Context, code string, timeout time.Duration, onChunk func(stream, text string)) (*ExecResult, error) {
	if !k.execMu.TryLock() {
		return nil, errBusy
	}
	defer k.execMu.Unlock()

	k.stateMu.Lock()
	if k.dead {
		reason := k.deadReason
		k.stateMu.Unlock()
		return nil, fmt.Errorf("kernel is dead: %s", reason)
	}
	k.busy = true
	k.stateMu.Unlock()
	defer k.setBusy(false)

	id := strconv.FormatUint(execSeq.Add(1), 10)
	if err := writeFrame(k.conn, outExecute{Type: "execute", ID: id, Code: code}); err != nil {
		k.markDead("write failed: " + err.Error())
		return nil, fmt.Errorf("send execute: %w", err)
	}

	// Watch ctx: if the request is cancelled (client disconnect), trip the read
	// deadline so the loop escalates through the same interrupt→kill path rather
	// than abandoning a read mid-execution (which would desync the protocol —
	// the next execute would read this cell's leftover frames).
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = k.conn.SetReadDeadline(time.Now())
		case <-watchDone:
		}
	}()

	var out, errb strings.Builder
	deadline := time.Now().Add(timeout)
	readUntil := deadline
	interrupted := false

	for {
		_ = k.conn.SetReadDeadline(readUntil)
		msg, err := readFrame(k.conn)
		if err != nil {
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				if !interrupted {
					// Deadline reached: interrupt the running cell (graceful).
					_ = k.signal(syscall.SIGINT)
					interrupted = true
					readUntil = time.Now().Add(interruptGrace)
					continue
				}
				// Grace expired: the cell ignored SIGINT. Kill and drop the kernel.
				k.forceKill("execution timed out")
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return &ExecResult{Status: "timeout", Stdout: out.String(), Stderr: errb.String()}, nil
			}
			// EOF / connection error: the harness died mid-execution.
			k.markDead("connection lost during execute")
			return nil, fmt.Errorf("kernel connection lost: %w", err)
		}

		switch msg.Type {
		case "stream":
			if msg.Name == "stderr" {
				errb.WriteString(msg.Text)
			} else {
				out.WriteString(msg.Text)
			}
			if onChunk != nil {
				onChunk(msg.Name, msg.Text)
			}
		case "result":
			_ = k.conn.SetReadDeadline(time.Time{})
			k.recordActivity(msg.ExecutionCount)
			return &ExecResult{
				Status:         msg.Status,
				ExecutionCount: msg.ExecutionCount,
				Stdout:         out.String(),
				Stderr:         errb.String(),
				ResultRepr:     msg.ResultRepr,
				Ename:          msg.Ename,
				Evalue:         msg.Evalue,
				Traceback:      msg.Traceback,
			}, nil
		}
	}
}

// Shutdown asks the harness to exit cleanly, then ensures the process is gone.
func (k *Kernel) Shutdown() {
	k.stateMu.Lock()
	dead := k.dead
	k.stateMu.Unlock()
	if !dead {
		_ = writeFrame(k.conn, outShutdown{Type: "shutdown"})
	}
	// Give the clean exit a brief moment, then force the group down. The reaper
	// goroutine performs the actual Wait.
	time.AfterFunc(2*time.Second, func() { k.forceKill("shutdown") })
}

func (k *Kernel) forceKill(reason string) {
	k.markDead(reason)
	if k.cmd.Process != nil {
		_ = syscall.Kill(-k.cmd.Process.Pid, syscall.SIGKILL)
	}
}

func (k *Kernel) signal(sig syscall.Signal) error {
	if k.cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-k.cmd.Process.Pid, sig)
}

func (k *Kernel) markDead(reason string) {
	k.stateMu.Lock()
	if !k.dead {
		k.dead = true
		k.deadReason = reason
	}
	k.stateMu.Unlock()
}

func (k *Kernel) setBusy(b bool) {
	k.stateMu.Lock()
	k.busy = b
	k.stateMu.Unlock()
}

func (k *Kernel) recordActivity(count int) {
	k.stateMu.Lock()
	k.execCount = count
	k.lastAct = time.Now()
	k.stateMu.Unlock()
}

func (k *Kernel) status() Status {
	k.stateMu.Lock()
	defer k.stateMu.Unlock()
	state := "idle"
	switch {
	case k.dead:
		state = "dead"
	case k.busy:
		state = "busy"
	}
	return Status{
		ID: k.id, Env: k.env, Python: k.python, State: state,
		ExecutionCount: k.execCount, StartedAt: k.startedAt, LastActivity: k.lastAct,
		DeadReason: k.deadReason,
	}
}
