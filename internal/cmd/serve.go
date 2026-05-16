package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/config"
	"github.com/artbegolli/anvilmcp/internal/logging"
	"github.com/artbegolli/anvilmcp/internal/server"
)

func NewServeCommand() *cobra.Command {
	var (
		port              string
		policyPath        string
		clusterPolicyPath string
		historyDBPath     string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd.Context(), &ServeConfig{
				Port:              port,
				PolicyPath:        policyPath,
				ClusterPolicyPath: clusterPolicyPath,
				HistoryDBPath:     historyDBPath,
			})
		},
	}

	cmd.Flags().StringVarP(&port, "port", "p", ":8080", "Port to listen on")
	cmd.Flags().StringVar(&policyPath, "policy", os.ExpandEnv("$HOME/.config/mcp-session/policy.yaml"), "Path to user policy file")
	cmd.Flags().StringVar(&clusterPolicyPath, "cluster-policy", "/etc/mcp-session/cluster-policy.yaml", "Path to cluster policy file")
	cmd.Flags().StringVar(&historyDBPath, "history-db", os.ExpandEnv("$HOME/.local/state/mcp-session/history.db"), "Path to history database")

	return cmd
}

type ServeConfig struct {
	Port              string
	PolicyPath        string
	ClusterPolicyPath string
	HistoryDBPath     string
}

func runServe(ctx context.Context, cfg *ServeConfig) error {
	logger := logging.NewLogger()
	defer logger.Sync()

	logger.Info("Starting Anvil MCP Server",
		zap.String("port", cfg.Port),
		zap.String("policy", cfg.PolicyPath),
	)

	// Load configuration
	sessConfig, err := config.NewSessionConfig()
	if err != nil {
		logger.Error("Failed to load session config", zap.Error(err))
		return err
	}

	// Initialize server
	srv, err := server.NewServer(ctx, logger, sessConfig, cfg.PolicyPath, cfg.ClusterPolicyPath, cfg.HistoryDBPath)
	if err != nil {
		logger.Error("Failed to initialize server", zap.Error(err))
		return err
	}

	// Start server
	go func() {
		logger.Info("Listening", zap.String("addr", cfg.Port))
		if err := srv.Start(cfg.Port); err != nil {
			logger.Error("Server error", zap.Error(err))
		}
	}()

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Info("Shutting down")
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("Shutdown error", zap.Error(err))
		return err
	}

	return nil
}
