package kernel

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

//go:embed harness.py
var harnessSource []byte

const (
	defaultMaxKernels = 4
	// DefaultTimeout is the per-execution deadline when a request omits one.
	DefaultTimeout = 60 * time.Second
)

// Manager owns the set of running kernels for a session plus the on-disk copy
// of the harness. Safe for concurrent use.
type Manager struct {
	logger      *zap.Logger
	pythonPath  string
	harnessPath string
	maxKernels  int
	gate        func(argv []string) error // launch authorization (policy chokepoint); may be nil

	mu      sync.Mutex
	kernels map[string]*Kernel
	nextID  int
	closed  bool
}

// Options configures a Manager.
type Options struct {
	Logger     *zap.Logger
	PythonPath string                    // system interpreter (config PYTHON_PATH)
	MaxKernels int                       // 0 => default
	Gate       func(argv []string) error // launch authorization; nil => no gating
}

// NewManager writes the embedded harness to a private temp dir and returns a
// ready manager.
func NewManager(opts Options) (*Manager, error) {
	dir, err := os.MkdirTemp("", "anvilmcp-kernel-")
	if err != nil {
		return nil, fmt.Errorf("create kernel tmpdir: %w", err)
	}
	harnessPath := filepath.Join(dir, "harness.py")
	if err := os.WriteFile(harnessPath, harnessSource, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write harness: %w", err)
	}

	maxK := opts.MaxKernels
	if maxK <= 0 {
		maxK = defaultMaxKernels
	}
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	pythonPath := opts.PythonPath
	if pythonPath == "" {
		pythonPath = "python3"
	}

	return &Manager{
		logger:      logger,
		pythonPath:  pythonPath,
		harnessPath: harnessPath,
		maxKernels:  maxK,
		gate:        opts.Gate,
		kernels:     make(map[string]*Kernel),
	}, nil
}

// Start launches a new kernel per spec and returns its status.
func (m *Manager) Start(ctx context.Context, spec StartSpec) (Status, error) {
	argv, label, err := m.resolveLaunch(spec)
	if err != nil {
		return Status{}, err
	}
	if m.gate != nil {
		if err := m.gate(argv); err != nil {
			return Status{}, err
		}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Status{}, fmt.Errorf("kernel manager is shut down")
	}
	if len(m.kernels) >= m.maxKernels {
		m.mu.Unlock()
		return Status{}, fmt.Errorf("kernel limit reached (%d live)", m.maxKernels)
	}
	m.nextID++
	id := fmt.Sprintf("k%d", m.nextID)
	m.mu.Unlock()

	k, err := startKernel(id, label, argv, m.logger)
	if err != nil {
		return Status{}, err
	}

	m.mu.Lock()
	m.kernels[id] = k
	m.mu.Unlock()
	m.logger.Info("kernel started",
		zap.String("kernel_id", id), zap.String("env", label), zap.String("python", k.python))
	return k.status(), nil
}

func (m *Manager) get(id string) (*Kernel, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.kernels[id]
	return k, ok
}

// Execute runs code on kernel id.
func (m *Manager) Execute(ctx context.Context, id, code string, timeout time.Duration, onChunk func(stream, text string)) (*ExecResult, error) {
	k, ok := m.get(id)
	if !ok {
		return nil, fmt.Errorf("no such kernel: %s", id)
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return k.Execute(ctx, code, timeout, onChunk)
}

// Status returns the status of kernel id.
func (m *Manager) Status(id string) (Status, error) {
	k, ok := m.get(id)
	if !ok {
		return Status{}, fmt.Errorf("no such kernel: %s", id)
	}
	return k.status(), nil
}

// List returns the status of every kernel, ordered by id.
func (m *Manager) List() []Status {
	m.mu.Lock()
	kernels := make([]*Kernel, 0, len(m.kernels))
	for _, k := range m.kernels {
		kernels = append(kernels, k)
	}
	m.mu.Unlock()

	out := make([]Status, 0, len(kernels))
	for _, k := range kernels {
		out = append(out, k.status())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Shutdown stops a kernel and removes it from the registry.
func (m *Manager) Shutdown(id string) error {
	m.mu.Lock()
	k, ok := m.kernels[id]
	if ok {
		delete(m.kernels, id)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such kernel: %s", id)
	}
	k.Shutdown()
	m.logger.Info("kernel shut down", zap.String("kernel_id", id))
	return nil
}

// ShutdownAll stops every kernel and removes the harness temp dir. Called on
// server shutdown.
func (m *Manager) ShutdownAll() {
	m.mu.Lock()
	m.closed = true
	kernels := make([]*Kernel, 0, len(m.kernels))
	for _, k := range m.kernels {
		kernels = append(kernels, k)
	}
	m.kernels = make(map[string]*Kernel)
	m.mu.Unlock()

	for _, k := range kernels {
		k.forceKill("manager shutdown")
	}
	if m.harnessPath != "" {
		_ = os.RemoveAll(filepath.Dir(m.harnessPath))
	}
}
