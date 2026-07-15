package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/config"
	"github.com/artbegolli/anvilmcp/internal/logging"
	"github.com/artbegolli/anvilmcp/internal/metrics"
	"github.com/artbegolli/anvilmcp/internal/server"
)

func NewServeCommand() *cobra.Command {
	cfg := &ServeConfig{}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSupervised(cmd.Context(), cfg)
		},
	}

	cmd.Flags().StringVarP(&cfg.Port, "port", "p", ":8080", "Port to listen on for MCP traffic")
	cmd.Flags().StringVar(&cfg.PolicyPath, "policy", os.ExpandEnv("$HOME/.config/anvilmcp/config.yaml"), "Path to user policy file")
	cmd.Flags().StringVar(&cfg.ClusterPolicyPath, "cluster-policy", "/etc/anvilmcp/config.yaml", "Path to cluster policy file")
	cmd.Flags().StringVar(&cfg.HistoryDBPath, "history-db", os.ExpandEnv("$HOME/.local/state/anvilmcp/history.db"), "Path to history database")
	cmd.Flags().StringVar(&cfg.MetricsPort, "metrics-port", ":9090", "Port to serve /metrics on; empty string disables")
	cmd.Flags().BoolVar(&cfg.AutoRestart, "auto-restart", false, "Restart the MCP server on unexpected crash or error (SIGTERM/SIGINT are always terminal)")
	cmd.Flags().IntVar(&cfg.MaxRestartAttempts, "max-restart-attempts", 5, "Give up after N consecutive failed restarts within a short window (0 = unlimited)")

	return cmd
}

type ServeConfig struct {
	Port               string
	PolicyPath         string
	ClusterPolicyPath  string
	HistoryDBPath      string
	MetricsPort        string
	AutoRestart        bool
	MaxRestartAttempts int
}

// runSupervised runs runServe once, or in a restart loop when --auto-restart
// is set. Consecutive restart attempts back off exponentially (1s → 60s cap)
// and the attempt counter resets when the server survives ≥ stableThreshold.
func runSupervised(ctx context.Context, cfg *ServeConfig) error {
	logger := logging.NewLogger()
	defer func() { _ = logger.Sync() }()

	metricsReg := metrics.New()

	// Metrics server lives across restarts of the MCP server so scrapers see
	// a continuous series (and we can observe the restart counter itself
	// even during a restart loop).
	var metricsSrv *server.MetricsServer
	metricsCtx, metricsCancel := context.WithCancel(context.Background())
	defer metricsCancel()
	if cfg.MetricsPort != "" {
		metricsSrv = server.NewMetricsServer(logger, metricsReg, cfg.MetricsPort)
		go func() {
			if err := metricsSrv.Start(); err != nil {
				logger.Error("Metrics server exited", zap.Error(err))
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = metricsSrv.Shutdown(shutdownCtx)
		}()
	}
	_ = metricsCtx

	// Single-shot path. runServeOnce returns nil on graceful shutdown.
	if !cfg.AutoRestart {
		return runServeOnce(ctx, logger, metricsReg, cfg)
	}

	const (
		baseBackoff      = 1 * time.Second
		maxBackoff       = 60 * time.Second
		stableThreshold  = 30 * time.Second // reset attempt counter after this long
	)

	attempt := 0
	backoff := baseBackoff

	for {
		start := time.Now()
		err := runServeOnce(ctx, logger, metricsReg, cfg)
		uptime := time.Since(start)

		// Terminal reasons: clean shutdown, or the parent context was cancelled
		// (SIGTERM / SIGINT). Never restart from those.
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			logger.Info("Supervisor stopping: parent context cancelled", zap.Error(ctx.Err()))
			return err
		}

		// Reset backoff if the last run held together for a while.
		if uptime >= stableThreshold {
			attempt = 0
			backoff = baseBackoff
		}
		attempt++

		reason := "error"
		if errors.Is(err, errServerPanic) {
			reason = "panic"
		}
		metricsReg.Restarts.WithLabelValues(reason).Inc()

		if cfg.MaxRestartAttempts > 0 && attempt > cfg.MaxRestartAttempts {
			logger.Error("Supervisor giving up: max restart attempts exceeded",
				zap.Int("attempts", attempt-1),
				zap.Int("limit", cfg.MaxRestartAttempts),
				zap.Error(err),
			)
			return fmt.Errorf("supervisor exhausted after %d attempts: %w", cfg.MaxRestartAttempts, err)
		}

		logger.Warn("Server exited unexpectedly; restarting after backoff",
			zap.Int("attempt", attempt),
			zap.Duration("backoff", backoff),
			zap.Duration("last_uptime", uptime),
			zap.String("reason", reason),
			zap.Error(err),
		)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// errServerPanic marks that runServeOnce recovered a panic. Wrapped so the
// supervisor can label the restart reason.
var errServerPanic = errors.New("server goroutine panicked")

// runServeOnce constructs and runs a single instance of the MCP server. It
// blocks until either the server exits (via internal error or SIGINT/SIGTERM)
// or a panic escapes and is recovered here.
func runServeOnce(ctx context.Context, logger *zap.Logger, metricsReg *metrics.Registry, cfg *ServeConfig) (err error) {
	logger.Info("Starting Anvil MCP Server",
		zap.String("port", cfg.Port),
		zap.String("policy", cfg.PolicyPath),
	)

	sessConfig, err := config.NewSessionConfig()
	if err != nil {
		logger.Error("Failed to load session config", zap.Error(err))
		return err
	}

	srv, err := server.NewServer(ctx, logger, sessConfig, cfg.PolicyPath, cfg.ClusterPolicyPath, cfg.HistoryDBPath, metricsReg)
	if err != nil {
		logger.Error("Failed to initialize server", zap.Error(err))
		return err
	}

	// If the MCP server goroutine panics, convert it into an error so the
	// supervisor can decide whether to restart. Without this the panic would
	// crash the whole process even under --auto-restart.
	serverErr := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				metricsReg.Panics.WithLabelValues("server").Inc()
				logger.Error("MCP server goroutine panicked",
					zap.Any("panic", r),
					zap.ByteString("stack", debug.Stack()),
				)
				serverErr <- fmt.Errorf("%w: %v", errServerPanic, r)
			}
		}()
		logger.Info("Listening", zap.String("addr", cfg.Port))
		serverErr <- srv.Start(cfg.Port)
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	select {
	case <-sigChan:
		logger.Info("Shutting down (signal received)")
	case err = <-serverErr:
		if err != nil {
			logger.Error("MCP server exited unexpectedly", zap.Error(err))
		}
	case <-ctx.Done():
		logger.Info("Shutting down (context cancelled)", zap.Error(ctx.Err()))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if shutdownErr := srv.Shutdown(shutdownCtx); shutdownErr != nil {
		logger.Error("Shutdown error", zap.Error(shutdownErr))
		if err == nil {
			err = shutdownErr
		}
	}

	return err
}
