package server

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/audit"
	"github.com/artbegolli/anvilmcp/internal/history"
	"github.com/artbegolli/anvilmcp/internal/metrics"
	"github.com/artbegolli/anvilmcp/internal/policy"
)

func newAuditRecorder(t *testing.T, cfg policy.AuditPolicy) (*audit.Recorder, *history.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := history.NewStore(context.Background(), filepath.Join(dir, "history.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	engine := policy.NewEngine(policy.DefaultPolicy(), filepath.Join(dir, "config.yaml"))
	rec, err := audit.NewRecorder(store, engine, cfg, "sess", zap.NewNop(), metrics.NewNoop())
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return rec, store
}

func callToolRequest(name string, args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	return req
}

func TestWrapToolRecordsInvocation(t *testing.T) {
	rec, store := newAuditRecorder(t, policy.AuditPolicy{})

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Stand in for a chokepoint annotating the in-flight record.
		audit.FromContext(ctx).SetDecision(audit.DecisionAllow, "exec.allow:ls")
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent("ok")}}, nil
	}

	wrapped := wrapTool(zap.NewNop(), metrics.NewNoop(), rec, "exec.shell", handler)
	if _, err := wrapped(context.Background(), callToolRequest("exec.shell", map[string]any{"cmd": "ls"})); err != nil {
		t.Fatalf("handler: %v", err)
	}

	got, err := store.ListByTool(context.Background(), "exec.shell", 10)
	if err != nil {
		t.Fatalf("ListByTool: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	if got[0].Status != audit.StatusOK {
		t.Errorf("status = %q, want %q", got[0].Status, audit.StatusOK)
	}
	if got[0].Decision != audit.DecisionAllow {
		t.Errorf("decision = %q, want %q", got[0].Decision, audit.DecisionAllow)
	}
}

func TestWrapToolRecordsPanickingTool(t *testing.T) {
	rec, store := newAuditRecorder(t, policy.AuditPolicy{})

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		panic("boom")
	}

	wrapped := wrapTool(zap.NewNop(), metrics.NewNoop(), rec, "fs.read", handler)
	if _, err := wrapped(context.Background(), callToolRequest("fs.read", nil)); err == nil {
		t.Fatal("expected the recovered panic to surface as an error")
	}

	// A crashing tool is exactly when the trail matters most.
	got, _ := store.ListByTool(context.Background(), "fs.read", 10)
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	if got[0].Status != audit.StatusError {
		t.Errorf("status = %q, want %q", got[0].Status, audit.StatusError)
	}
}

func TestWrapToolFailClosedRefusesCall(t *testing.T) {
	dir := t.TempDir()
	store, err := history.NewStore(context.Background(), filepath.Join(dir, "history.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	engine := policy.NewEngine(policy.DefaultPolicy(), filepath.Join(dir, "config.yaml"))
	rec, err := audit.NewRecorder(store, engine, policy.AuditPolicy{FailClosed: true}, "s", zap.NewNop(), metrics.NewNoop())
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	_ = store.Close()

	ran := false
	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ran = true
		return &mcp.CallToolResult{}, nil
	}

	wrapped := wrapTool(zap.NewNop(), metrics.NewNoop(), rec, "exec.shell", handler)
	result, err := wrapped(context.Background(), callToolRequest("exec.shell", nil))
	if err == nil {
		t.Fatal("fail_closed should refuse the call when the record cannot be written")
	}
	if ran {
		t.Error("handler ran despite fail_closed refusing the call")
	}
	if result == nil || !result.IsError {
		t.Errorf("result = %+v, want an error result", result)
	}
}

func TestWrapResourceRecordsRead(t *testing.T) {
	rec, store := newAuditRecorder(t, policy.AuditPolicy{})

	handler := func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		return nil, errors.New("denied")
	}

	wrapped := wrapResource(zap.NewNop(), metrics.NewNoop(), rec, "file:///{+path}", handler)
	var req mcp.ReadResourceRequest
	req.Params.URI = "file:///etc/passwd"
	if _, err := wrapped(context.Background(), req); err == nil {
		t.Fatal("expected the handler error to surface")
	}

	// Resource reads are a second path to file contents; they must not be a
	// blind spot in the trail.
	got, _ := store.ListByTool(context.Background(), "resource:file:///{+path}", 10)
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	if got[0].Status != audit.StatusError {
		t.Errorf("status = %q, want %q", got[0].Status, audit.StatusError)
	}
	if want := "file:///etc/passwd"; !strings.Contains(got[0].ArgsJSON, want) {
		t.Errorf("args_json = %q, want it to contain %q", got[0].ArgsJSON, want)
	}
}
