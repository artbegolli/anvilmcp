package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/metrics"
)

// MetricsServer serves the Prometheus /metrics endpoint on a separate port
// from the MCP server. The two surfaces have different consumers (scrapers
// vs MCP clients), different auth stories, and different lifecycles, so they
// stay physically separate.
type MetricsServer struct {
	logger *zap.Logger
	srv    *http.Server
}

// NewMetricsServer builds an http.Server bound to addr that exposes /metrics
// backed by reg. addr must be non-empty; the caller decides whether to run.
func NewMetricsServer(logger *zap.Logger, reg *metrics.Registry, addr string) *MetricsServer {
	mux := http.NewServeMux()
	mux.Handle("/metrics", reg.Handler())

	return &MetricsServer{
		logger: logger,
		srv: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

// Start blocks until the server exits. Returns nil on clean shutdown.
func (m *MetricsServer) Start() error {
	m.logger.Info("Metrics server listening", zap.String("addr", m.srv.Addr))
	err := m.srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown attempts a graceful stop.
func (m *MetricsServer) Shutdown(ctx context.Context) error {
	return m.srv.Shutdown(ctx)
}
