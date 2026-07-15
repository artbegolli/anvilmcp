package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/metrics"
)

func TestWrapToolRecoversPanic(t *testing.T) {
	m := metrics.NewNoop()
	logger := zap.NewNop()

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		panic("boom")
	}

	wrapped := wrapTool(logger, m, "test.tool", handler)
	result, err := wrapped(context.Background(), mcp.CallToolRequest{})

	if err == nil {
		t.Fatal("expected error from recovered panic, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected error to mention panic value, got %q", err.Error())
	}
	if result == nil || !result.IsError {
		t.Errorf("expected error result, got %+v", result)
	}

	got := testutil.ToFloat64(m.Panics.WithLabelValues("tool:test.tool"))
	if got != 1 {
		t.Errorf("expected panics counter to be 1, got %v", got)
	}
	// Panics still count as an invocation (with result="error") so operators can
	// see them in the invocations series too.
	got = testutil.ToFloat64(m.ToolInvocations.WithLabelValues("test.tool", "error"))
	if got != 1 {
		t.Errorf("expected tool_invocations{result=error} to be 1, got %v", got)
	}
}

func TestWrapToolRecordsSuccess(t *testing.T) {
	m := metrics.NewNoop()
	logger := zap.NewNop()

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}

	wrapped := wrapTool(logger, m, "test.tool", handler)
	if _, err := wrapped(context.Background(), mcp.CallToolRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := testutil.ToFloat64(m.ToolInvocations.WithLabelValues("test.tool", "success")); got != 1 {
		t.Errorf("expected tool_invocations{result=success}=1, got %v", got)
	}
}

func TestWrapToolClassifiesErrors(t *testing.T) {
	// Handler error path
	m := metrics.NewNoop()
	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("bad")
	}
	wrapped := wrapTool(zap.NewNop(), m, "t", handler)
	_, _ = wrapped(context.Background(), mcp.CallToolRequest{})

	if got := testutil.ToFloat64(m.ToolInvocations.WithLabelValues("t", "error")); got != 1 {
		t.Errorf("expected error label bumped, got %v", got)
	}

	// Tool-reported error (result.IsError=true) is classified separately from
	// handler errors so operators can tell "the tool ran and said no" apart
	// from "the tool exploded."
	m = metrics.NewNoop()
	handler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{IsError: true}, nil
	}
	wrapped = wrapTool(zap.NewNop(), m, "t", handler)
	_, _ = wrapped(context.Background(), mcp.CallToolRequest{})

	if got := testutil.ToFloat64(m.ToolInvocations.WithLabelValues("t", "tool_error")); got != 1 {
		t.Errorf("expected tool_error label bumped, got %v", got)
	}
}

func TestWrapResourceRecoversPanic(t *testing.T) {
	m := metrics.NewNoop()
	handler := func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		panic("splat")
	}

	wrapped := wrapResource(zap.NewNop(), m, "test://res", handler)
	_, err := wrapped(context.Background(), mcp.ReadResourceRequest{})

	if err == nil {
		t.Fatal("expected error from recovered panic")
	}
	if got := testutil.ToFloat64(m.Panics.WithLabelValues("resource:test://res")); got != 1 {
		t.Errorf("expected panics{location=resource:test://res}=1, got %v", got)
	}
}
