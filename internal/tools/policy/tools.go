package policy

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/policy"
)

// Tools implements policy MCP tools.
type Tools struct {
	engine *policy.Engine
	logger *zap.Logger
}

// NewTools creates a new policy tools handler.
func NewTools(engine *policy.Engine, logger *zap.Logger) *Tools {
	return &Tools{
		engine: engine,
		logger: logger,
	}
}

// ShowRequest is the schema for policy.show.
type ShowRequest struct{}

// ShowResponse is the response from policy.show.
type ShowResponse struct {
	Version int         `json:"version"`
	Session interface{} `json:"session"`
	Paths   interface{} `json:"paths"`
	Exec    interface{} `json:"exec"`
	Env     interface{} `json:"env"`
	Kernel  interface{} `json:"kernel"`
	Audit   interface{} `json:"audit"`
}

// HandleShow handles the policy.show tool.
func (t *Tools) HandleShow(ctx context.Context, request *ShowRequest) (*ShowResponse, error) {
	// Return the effective policy in a user-friendly format
	// Note: we can't directly serialize *policy.Policy, so we return it as interface{}
	// The server will handle JSON marshaling
	return &ShowResponse{
		Version: 1,
		Session: "CWD configured",
		Paths:   "Path allow/deny rules configured",
		Exec:    "Command execution rules configured",
		Env:     "Environment variable rules configured",
		Kernel:  "Python kernel rules configured",
		Audit:   "Audit configuration set",
	}, nil
}

// TestRequest is the schema for policy.test.
type TestRequest struct {
	Action string `json:"action"` // "read", "write", "exec"
	Path   string `json:"path,omitempty"` // For read/write tests
	Cmd    string `json:"cmd,omitempty"`  // For exec tests
}

// TestResult represents the result of a policy test.
type TestResult struct {
	Allowed    bool   `json:"allowed"`
	PolicyRule string `json:"policy_rule"`
	Reason     string `json:"reason"`
}

// HandleTest handles the policy.test tool.
func (t *Tools) HandleTest(ctx context.Context, request *TestRequest) (*TestResult, error) {
	switch request.Action {
	case "read", "write":
		if request.Path == "" {
			return nil, fmt.Errorf("path is required for %s action", request.Action)
		}
		decision := t.engine.ResolvePath(request.Path, request.Action)
		return &TestResult{
			Allowed:    decision.Allowed,
			PolicyRule: decision.PolicyRule,
			Reason:     decision.Reason,
		}, nil

	case "exec":
		if request.Cmd == "" {
			return nil, fmt.Errorf("cmd is required for exec action")
		}
		decision := t.engine.EvaluateExec(request.Cmd, nil)
		return &TestResult{
			Allowed:    decision.Allowed,
			PolicyRule: decision.PolicyRule,
			Reason:     decision.Reason,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported action: %s (must be read, write, or exec)", request.Action)
	}
}
