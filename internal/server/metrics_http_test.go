package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/metrics"
)

// TestMetricsServerServesPromText spins up the metrics server on an ephemeral
// port and verifies the /metrics endpoint returns Prometheus-format output.
// Uses a real socket rather than httptest because the code under test builds
// its own http.Server (to control ReadHeaderTimeout etc.).
func TestMetricsServerServesPromText(t *testing.T) {
	reg := metrics.New()
	reg.ExecConcurrent.Set(3)

	// Bind to :0 to grab a free port, then use the resolved address so the
	// test isn't sensitive to whatever else is on the machine.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv := NewMetricsServer(zap.NewNop(), reg, addr)
	go func() {
		_ = srv.Start()
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	// Poll briefly until the listener is up. The server starts async so a
	// straight GET can race the bind.
	var resp *http.Response
	for i := 0; i < 20; i++ {
		resp, err = http.Get("http://" + addr + "/metrics")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "anvilmcp_exec_concurrent 3") {
		t.Errorf("expected primed metric in output; got:\n%s", body)
	}
}
