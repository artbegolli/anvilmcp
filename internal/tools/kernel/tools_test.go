package kernel

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	kernelcore "github.com/artbegolli/anvilmcp/internal/kernel"
	"go.uber.org/zap"
)

func newTools(t *testing.T) *Tools {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	m, err := kernelcore.NewManager(kernelcore.Options{PythonPath: "python3"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(m.ShutdownAll)
	return NewTools(m, zap.NewNop())
}

func TestToolsStartExecPersistShutdown(t *testing.T) {
	kt := newTools(t)
	ctx := context.Background()

	st, err := kt.HandleStart(ctx, &StartRequest{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if st.KernelID == "" || st.State != "idle" {
		t.Fatalf("unexpected start response: %+v", st)
	}

	if _, err := kt.HandleExec(ctx, &ExecRequest{KernelID: st.KernelID, Code: "a = 10"}); err != nil {
		t.Fatalf("exec 1: %v", err)
	}
	res, err := kt.HandleExec(ctx, &ExecRequest{KernelID: st.KernelID, Code: "a * 2"})
	if err != nil {
		t.Fatalf("exec 2: %v", err)
	}
	if res.ResultRepr == nil || *res.ResultRepr != "20" {
		t.Errorf("result_repr = %v, want 20 (state did not persist)", res.ResultRepr)
	}

	list, err := kt.HandleList(ctx, struct{}{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Kernels) != 1 {
		t.Errorf("kernels = %d, want 1", len(list.Kernels))
	}

	status, err := kt.HandleStatus(ctx, &StatusRequest{KernelID: st.KernelID})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.ExecutionCount != 2 {
		t.Errorf("execution_count = %d, want 2", status.ExecutionCount)
	}

	if _, err := kt.HandleShutdown(ctx, &ShutdownRequest{KernelID: st.KernelID}); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if _, err := kt.HandleStatus(ctx, &StatusRequest{KernelID: st.KernelID}); err == nil {
		t.Error("status on shut-down kernel should fail")
	}
}

func TestToolsExecValidation(t *testing.T) {
	kt := newTools(t)
	ctx := context.Background()
	if _, err := kt.HandleExec(ctx, &ExecRequest{Code: "1"}); err == nil {
		t.Error("empty kernel_id should error")
	}
	if _, err := kt.HandleExec(ctx, &ExecRequest{KernelID: "nope", Code: "1"}); err == nil {
		t.Error("unknown kernel should error")
	}
}

func TestToolsUnsupportedEnv(t *testing.T) {
	kt := newTools(t)
	_, err := kt.HandleStart(context.Background(), &StartRequest{Env: &EnvRef{Type: "conda", Ref: "x"}})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("conda env err = %v, want 'not supported'", err)
	}
}
