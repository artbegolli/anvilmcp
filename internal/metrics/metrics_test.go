package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewExposesAllExpectedSeries(t *testing.T) {
	r := New()

	// Touch every metric so it becomes visible in the exposition output.
	// Prometheus counters/histograms with labels are only emitted after a
	// first WithLabelValues(...) call, so we prime each one.
	r.ToolInvocations.WithLabelValues("fs.read", "success").Inc()
	r.ToolDuration.WithLabelValues("fs.read").Observe(0.1)
	r.ResourceReads.WithLabelValues("file:///{+path}", "success").Inc()
	r.PolicyDecisions.WithLabelValues("path", "allow").Inc()
	r.ExecConcurrent.Inc()
	r.ExecRateLimited.Inc()
	r.ExecOutputBytes.Add(1024)
	r.ExecDuration.Observe(0.5)
	r.Panics.WithLabelValues("tool:fs.read").Inc()
	r.Restarts.WithLabelValues("panic").Inc()

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body, _ := io.ReadAll(rec.Body)
	got := string(body)

	wants := []string{
		"anvilmcp_tool_invocations_total",
		"anvilmcp_tool_duration_seconds",
		"anvilmcp_resource_reads_total",
		"anvilmcp_policy_decisions_total",
		"anvilmcp_exec_concurrent",
		"anvilmcp_exec_rate_limited_total",
		"anvilmcp_exec_output_bytes_total",
		"anvilmcp_exec_duration_seconds",
		"anvilmcp_panics_total",
		"anvilmcp_restarts_total",
		// runtime collectors
		"go_goroutines",
		"process_start_time_seconds",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("expected metric %q in output; not found", w)
		}
	}
}

func TestNewNoopDoesNotPanic(t *testing.T) {
	// The noop registry is what callers get when they pass nil; every metric
	// handle must be safe to increment.
	r := NewNoop()
	r.ToolInvocations.WithLabelValues("x", "y").Inc()
	r.PolicyDecisions.WithLabelValues("path", "allow").Inc()
	r.Panics.WithLabelValues("test").Inc()
}
