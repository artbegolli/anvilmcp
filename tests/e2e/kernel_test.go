package e2e

import (
	"strings"
	"testing"
)

// TestKernelLifecycleAndPersistence exercises the opt-in kernel surface end to
// end: the warm namespace must survive across *separate MCP tool calls*, which
// is the whole point of the feature and can only be proven over the wire.
func TestKernelLifecycleAndPersistence(t *testing.T) {
	requirePython(t)
	srv := startServer(t, serverOpts{enableKernel: true})
	c := newClient(t, srv)

	names := c.toolNames(t)
	for _, want := range []string{"kernel.start", "kernel.exec", "kernel.list", "kernel.status", "kernel.shutdown"} {
		if !contains(names, want) {
			t.Fatalf("tool %q not registered with --enable-kernel; got %v", want, names)
		}
	}

	var start struct {
		KernelID string `json:"kernel_id"`
		Python   string `json:"python_version"`
		State    string `json:"state"`
	}
	c.mustCallTool(t, "kernel.start", nil).decode(t, &start)
	if start.KernelID == "" {
		t.Fatal("kernel.start returned no kernel_id")
	}
	if start.Python == "" {
		t.Error("kernel.start returned no python_version")
	}
	if start.State != "idle" {
		t.Errorf("state = %q, want idle", start.State)
	}

	type execResp struct {
		Status         string  `json:"status"`
		ExecutionCount int     `json:"execution_count"`
		Stdout         string  `json:"stdout"`
		ResultRepr     *string `json:"result_repr"`
		Ename          string  `json:"ename"`
	}

	// Bind a variable, then read it back in a *separate* call.
	var first execResp
	c.mustCallTool(t, "kernel.exec", map[string]any{
		"kernel_id": start.KernelID, "code": "x = 7",
	}).decode(t, &first)
	if first.Status != "ok" {
		t.Fatalf("first exec status = %q", first.Status)
	}

	var second execResp
	c.mustCallTool(t, "kernel.exec", map[string]any{
		"kernel_id": start.KernelID, "code": "print('doubled', x * 2)\nx * 6",
	}).decode(t, &second)
	if second.Status != "ok" {
		t.Fatalf("second exec status = %q", second.Status)
	}
	if second.ResultRepr == nil || *second.ResultRepr != "42" {
		t.Errorf("result_repr = %v, want \"42\" (state did not persist across calls)", second.ResultRepr)
	}
	if !strings.Contains(second.Stdout, "doubled 14") {
		t.Errorf("stdout = %q, want it to contain 'doubled 14'", second.Stdout)
	}
	if second.ExecutionCount != 2 {
		t.Errorf("execution_count = %d, want 2", second.ExecutionCount)
	}

	// Errors come back structured, not as a transport failure.
	var failed execResp
	c.mustCallTool(t, "kernel.exec", map[string]any{
		"kernel_id": start.KernelID, "code": "1/0",
	}).decode(t, &failed)
	if failed.Status != "error" {
		t.Errorf("status = %q, want error", failed.Status)
	}
	if failed.Ename != "ZeroDivisionError" {
		t.Errorf("ename = %q, want ZeroDivisionError", failed.Ename)
	}

	// An error must not kill the kernel.
	var afterError execResp
	c.mustCallTool(t, "kernel.exec", map[string]any{
		"kernel_id": start.KernelID, "code": "x + 1",
	}).decode(t, &afterError)
	if afterError.ResultRepr == nil || *afterError.ResultRepr != "8" {
		t.Errorf("kernel unusable after an error: result_repr = %v", afterError.ResultRepr)
	}

	var status struct {
		State          string `json:"state"`
		ExecutionCount int    `json:"execution_count"`
	}
	c.mustCallTool(t, "kernel.status", map[string]any{"kernel_id": start.KernelID}).decode(t, &status)
	if status.State != "idle" {
		t.Errorf("state = %q, want idle", status.State)
	}
	if status.ExecutionCount != 4 {
		t.Errorf("execution_count = %d, want 4", status.ExecutionCount)
	}

	var list struct {
		Kernels []struct {
			KernelID string `json:"kernel_id"`
		} `json:"kernels"`
	}
	c.mustCallTool(t, "kernel.list", nil).decode(t, &list)
	if len(list.Kernels) != 1 || list.Kernels[0].KernelID != start.KernelID {
		t.Errorf("kernel.list = %+v, want just %s", list.Kernels, start.KernelID)
	}

	// Default max kernels is 1, so a second start is refused.
	if tr := c.callTool(t, "kernel.start", nil, nil); !tr.IsError {
		t.Errorf("second kernel.start should hit the limit, got: %s", tr.text())
	} else if !strings.Contains(strings.ToLower(tr.text()), "limit") {
		t.Errorf("limit error should mention the limit, got: %s", tr.text())
	}

	var ok struct {
		OK bool `json:"ok"`
	}
	c.mustCallTool(t, "kernel.shutdown", map[string]any{"kernel_id": start.KernelID}).decode(t, &ok)
	if !ok.OK {
		t.Error("kernel.shutdown did not report ok")
	}
	if tr := c.callTool(t, "kernel.exec", map[string]any{
		"kernel_id": start.KernelID, "code": "1",
	}, nil); !tr.IsError {
		t.Error("exec on a shut-down kernel should fail")
	}
}

// TestKernelTimeoutInterruptsCell covers the kernel's timeout path: a sleeping
// cell is interrupted and the kernel stays usable.
func TestKernelTimeoutInterruptsCell(t *testing.T) {
	requirePython(t)
	srv := startServer(t, serverOpts{enableKernel: true})
	c := newClient(t, srv)

	var start struct {
		KernelID string `json:"kernel_id"`
	}
	c.mustCallTool(t, "kernel.start", nil).decode(t, &start)

	var slow struct {
		Status string `json:"status"`
	}
	c.mustCallTool(t, "kernel.exec", map[string]any{
		"kernel_id": start.KernelID, "code": "import time; time.sleep(60)", "timeout_seconds": 1,
	}).decode(t, &slow)
	if slow.Status != "interrupted" {
		t.Errorf("status = %q, want interrupted", slow.Status)
	}

	// The kernel survives an interrupt and keeps its namespace.
	var after struct {
		Status     string  `json:"status"`
		ResultRepr *string `json:"result_repr"`
	}
	c.mustCallTool(t, "kernel.exec", map[string]any{
		"kernel_id": start.KernelID, "code": "2 + 3",
	}).decode(t, &after)
	if after.Status != "ok" || after.ResultRepr == nil || *after.ResultRepr != "5" {
		t.Errorf("kernel unusable after interrupt: status=%q repr=%v", after.Status, after.ResultRepr)
	}
}
