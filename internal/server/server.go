package server

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/config"
	"github.com/artbegolli/anvilmcp/internal/history"
	"github.com/artbegolli/anvilmcp/internal/policy"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Server wraps the MCP server and related components.
type Server struct {
	logger         *zap.Logger
	sessionConfig  *config.SessionConfig
	policyLoader   *policy.Loader
	policyEngine   *policy.Engine
	historyStore   *history.Store
	mcpServer      *mcpserver.MCPServer
	httpServer     *mcpserver.StreamableHTTPServer
}

// NewServer creates and initializes a new anvil server.
func NewServer(
	ctx context.Context,
	logger *zap.Logger,
	sessConfig *config.SessionConfig,
	userPolicyPath, clusterPolicyPath, historyDBPath string,
) (*Server, error) {
	// Load policies
	policyLoader, err := policy.NewLoader(userPolicyPath, clusterPolicyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load policies: %w", err)
	}

	effectivePolicy := policyLoader.Effective()
	policyEngine := policy.NewEngine(effectivePolicy, policyLoader.PolicyFilePath())

	// Initialize history store
	histStore, err := history.NewStore(ctx, historyDBPath, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize history store: %w", err)
	}

	// Create MCP server
	mcpServer := mcpserver.NewMCPServer(
		"anvilmcp",
		"0.1.0",
		mcpserver.WithResourceCapabilities(true, true),
		mcpserver.WithToolCapabilities(true),
	)

	// Register tools (will be populated in Phase 1)
	// TODO: register filesystem, exec, environment, workspace, history, and policy tools

	srv := &Server{
		logger:        logger,
		sessionConfig: sessConfig,
		policyLoader:  policyLoader,
		policyEngine:  policyEngine,
		historyStore:  histStore,
		mcpServer:     mcpServer,
	}

	// Create HTTP server
	srv.httpServer = mcpserver.NewStreamableHTTPServer(mcpServer)

	logger.Info("Server initialized",
		zap.String("session_id", sessConfig.SessionID),
		zap.String("workspace_root", sessConfig.WorkspaceRoot),
	)

	return srv, nil
}

// Start starts the HTTP server on the given address.
func (s *Server) Start(addr string) error {
	s.logger.Info("Starting HTTP server", zap.String("addr", addr))
	return s.httpServer.Start(addr)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.historyStore != nil {
		if err := s.historyStore.Close(); err != nil {
			s.logger.Error("Failed to close history store", zap.Error(err))
		}
	}
	return nil
}

// Logger returns the server logger.
func (s *Server) Logger() *zap.Logger {
	return s.logger
}

// PolicyEngine returns the policy enforcement engine.
func (s *Server) PolicyEngine() *policy.Engine {
	return s.policyEngine
}

// HistoryStore returns the history store.
func (s *Server) HistoryStore() *history.Store {
	return s.historyStore
}

// MCPServer returns the underlying MCP server.
func (s *Server) MCPServer() *mcpserver.MCPServer {
	return s.mcpServer
}

// SessionConfig returns the session configuration.
func (s *Server) SessionConfig() *config.SessionConfig {
	return s.sessionConfig
}
