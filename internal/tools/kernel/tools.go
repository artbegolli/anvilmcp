// Package kernel exposes the MCP tool surface for persistent Python kernels,
// delegating lifecycle and execution to internal/kernel.
package kernel

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	kernelcore "github.com/artbegolli/anvilmcp/internal/kernel"
)

// Tools implements the kernel.* MCP tools.
type Tools struct {
	mgr    *kernelcore.Manager
	logger *zap.Logger
}

// NewTools creates a kernel tools handler backed by mgr.
func NewTools(mgr *kernelcore.Manager, logger *zap.Logger) *Tools {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Tools{mgr: mgr, logger: logger}
}

// EnvRef selects the interpreter/environment for a kernel.
type EnvRef struct {
	Type string `json:"type"` // system (this build); conda/uv later
	Ref  string `json:"ref"`  // env name / project dir
}

// StartRequest is the schema for kernel.start.
type StartRequest struct {
	Env  *EnvRef `json:"env,omitempty"`
	Name string  `json:"name,omitempty"`
}

// StartResponse is returned by kernel.start.
type StartResponse struct {
	KernelID string `json:"kernel_id"`
	Python   string `json:"python_version"`
	Env      string `json:"env"`
	State    string `json:"state"`
}

// HandleStart starts a new kernel.
func (t *Tools) HandleStart(ctx context.Context, req *StartRequest) (*StartResponse, error) {
	spec := kernelcore.StartSpec{Name: req.Name}
	if req.Env != nil {
		spec.Env = kernelcore.EnvSpec{Type: req.Env.Type, Ref: req.Env.Ref}
	}
	st, err := t.mgr.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &StartResponse{KernelID: st.ID, Python: st.Python, Env: st.Env, State: st.State}, nil
}

// ExecRequest is the schema for kernel.exec.
type ExecRequest struct {
	KernelID       string `json:"kernel_id"`
	Code           string `json:"code"`
	TimeoutSeconds *int   `json:"timeout_seconds,omitempty"`
}

// ExecResponse is returned by kernel.exec.
type ExecResponse struct {
	Status         string  `json:"status"` // ok | error | interrupted | timeout
	ExecutionCount int     `json:"execution_count"`
	Stdout         string  `json:"stdout"`
	Stderr         string  `json:"stderr"`
	ResultRepr     *string `json:"result_repr"` // null when the last statement isn't an expression
	Ename          string  `json:"ename,omitempty"`
	Evalue         string  `json:"evalue,omitempty"`
	Traceback      string  `json:"traceback,omitempty"`
}

// HandleExec runs code in a kernel. Output is buffered into the response in this
// build; live streaming to the client lands in a later milestone.
func (t *Tools) HandleExec(ctx context.Context, req *ExecRequest) (*ExecResponse, error) {
	if req.KernelID == "" {
		return nil, fmt.Errorf("kernel_id is required")
	}
	var timeout time.Duration
	if req.TimeoutSeconds != nil && *req.TimeoutSeconds > 0 {
		timeout = time.Duration(*req.TimeoutSeconds) * time.Second
	}
	res, err := t.mgr.Execute(ctx, req.KernelID, req.Code, timeout, nil)
	if err != nil {
		return nil, err
	}
	return &ExecResponse{
		Status:         res.Status,
		ExecutionCount: res.ExecutionCount,
		Stdout:         res.Stdout,
		Stderr:         res.Stderr,
		ResultRepr:     res.ResultRepr,
		Ename:          res.Ename,
		Evalue:         res.Evalue,
		Traceback:      res.Traceback,
	}, nil
}

// ShutdownRequest is the schema for kernel.shutdown.
type ShutdownRequest struct {
	KernelID string `json:"kernel_id"`
}

// OKResponse is a simple acknowledgement.
type OKResponse struct {
	OK bool `json:"ok"`
}

// HandleShutdown stops a kernel.
func (t *Tools) HandleShutdown(ctx context.Context, req *ShutdownRequest) (*OKResponse, error) {
	if req.KernelID == "" {
		return nil, fmt.Errorf("kernel_id is required")
	}
	if err := t.mgr.Shutdown(req.KernelID); err != nil {
		return nil, err
	}
	return &OKResponse{OK: true}, nil
}

// StatusInfo is one kernel's status in list/status responses.
type StatusInfo struct {
	KernelID       string `json:"kernel_id"`
	Env            string `json:"env"`
	Python         string `json:"python_version"`
	State          string `json:"state"` // idle | busy | dead
	ExecutionCount int    `json:"execution_count"`
	StartedAt      int64  `json:"started_at"`
	LastActivity   int64  `json:"last_activity"`
	DeadReason     string `json:"dead_reason,omitempty"`
}

func toStatusInfo(s kernelcore.Status) StatusInfo {
	return StatusInfo{
		KernelID: s.ID, Env: s.Env, Python: s.Python, State: s.State,
		ExecutionCount: s.ExecutionCount,
		StartedAt:      s.StartedAt.UnixMilli(),
		LastActivity:   s.LastActivity.UnixMilli(),
		DeadReason:     s.DeadReason,
	}
}

// StatusRequest is the schema for kernel.status.
type StatusRequest struct {
	KernelID string `json:"kernel_id"`
}

// HandleStatus returns one kernel's status.
func (t *Tools) HandleStatus(ctx context.Context, req *StatusRequest) (*StatusInfo, error) {
	if req.KernelID == "" {
		return nil, fmt.Errorf("kernel_id is required")
	}
	st, err := t.mgr.Status(req.KernelID)
	if err != nil {
		return nil, err
	}
	info := toStatusInfo(st)
	return &info, nil
}

// ListResponse is returned by kernel.list.
type ListResponse struct {
	Kernels []StatusInfo `json:"kernels"`
}

// HandleList returns all kernels.
func (t *Tools) HandleList(ctx context.Context, _ struct{}) (*ListResponse, error) {
	statuses := t.mgr.List()
	out := make([]StatusInfo, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, toStatusInfo(s))
	}
	return &ListResponse{Kernels: out}, nil
}
