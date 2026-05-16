package server

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/config"
	"github.com/artbegolli/anvilmcp/internal/history"
	"github.com/artbegolli/anvilmcp/internal/policy"
	"github.com/artbegolli/anvilmcp/internal/tools/exec"
	"github.com/artbegolli/anvilmcp/internal/tools/filesystem"
	policytools "github.com/artbegolli/anvilmcp/internal/tools/policy"
	"github.com/mark3labs/mcp-go/mcp"
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

	// Instantiate tool handlers
	fsTools := filesystem.NewTools(policyEngine, logger)
	execTools := exec.NewTools(policyEngine, logger, sessConfig, 4)
	policyToolsHandler := policytools.NewTools(policyEngine, logger)

	// Register filesystem tools
	registerFilesystemTools(mcpServer, fsTools)

	// Register exec tools
	registerExecTools(mcpServer, execTools)

	// Register policy tools
	registerPolicyTools(mcpServer, policyToolsHandler)

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

// registerFilesystemTools registers all filesystem tools with the MCP server.
func registerFilesystemTools(mcpServer *mcpserver.MCPServer, fsTools *filesystem.Tools) {
	// fs.list - List directory contents
	mcpServer.AddTool(
		mcp.NewTool(
			"fs.list",
			mcp.WithDescription("List directory contents, optionally filtered by glob pattern"),
			mcp.WithString("path", mcp.Required(), mcp.Description("Directory path to list (relative or absolute)")),
			mcp.WithString("pattern", mcp.Description("Optional glob pattern to filter results")),
			mcp.WithBoolean("recursive", mcp.Description("Whether to list recursively")),
		),
		fsToolListHandler(fsTools),
	)

	// fs.read - Read file contents
	mcpServer.AddTool(
		mcp.NewTool(
			"fs.read",
			mcp.WithDescription("Read file contents with optional offset and length limiting"),
			mcp.WithString("path", mcp.Required(), mcp.Description("File path to read (relative or absolute)")),
			mcp.WithInteger("offset", mcp.Description("Optional byte offset to start reading from")),
			mcp.WithInteger("length", mcp.Description("Optional number of bytes to read")),
		),
		fsToolReadHandler(fsTools),
	)

	// fs.write - Write file contents
	mcpServer.AddTool(
		mcp.NewTool(
			"fs.write",
			mcp.WithDescription("Write or update file contents"),
			mcp.WithString("path", mcp.Required(), mcp.Description("File path to write to (relative or absolute)")),
			mcp.WithString("content", mcp.Required(), mcp.Description("Content to write")),
			mcp.WithBoolean("create", mcp.Description("Whether to create if missing")),
			mcp.WithBoolean("overwrite", mcp.Description("Whether to overwrite if exists")),
		),
		fsToolWriteHandler(fsTools),
	)

	// fs.stat - Get file metadata
	mcpServer.AddTool(
		mcp.NewTool(
			"fs.stat",
			mcp.WithDescription("Get file or directory metadata"),
			mcp.WithString("path", mcp.Required(), mcp.Description("File or directory path (relative or absolute)")),
		),
		fsToolStatHandler(fsTools),
	)

	// fs.delete - Delete file or directory
	mcpServer.AddTool(
		mcp.NewTool(
			"fs.delete",
			mcp.WithDescription("Delete a file or directory"),
			mcp.WithString("path", mcp.Required(), mcp.Description("Path to delete (relative or absolute)")),
			mcp.WithBoolean("recursive", mcp.Description("If true, delete directories and their contents")),
		),
		fsToolDeleteHandler(fsTools),
	)
}

// registerPolicyTools registers all policy tools with the MCP server.
func registerPolicyTools(mcpServer *mcpserver.MCPServer, policyTools *policytools.Tools) {
	// policy.show - Show current policy
	mcpServer.AddTool(
		mcp.NewTool(
			"policy.show",
			mcp.WithDescription("Display the current effective security policy"),
		),
		policyToolShowHandler(policyTools),
	)

	// policy.test - Test policy rules
	mcpServer.AddTool(
		mcp.NewTool(
			"policy.test",
			mcp.WithDescription("Test whether an action would be allowed under the current policy"),
			mcp.WithString("action", mcp.Required(), mcp.Description("Action to test: read, write, or exec")),
			mcp.WithString("path", mcp.Description("File path (required for read/write actions)")),
			mcp.WithString("cmd", mcp.Description("Command name (required for exec actions)")),
		),
		policyToolTestHandler(policyTools),
	)
}

// registerExecTools registers all execution tools with the MCP server.
func registerExecTools(mcpServer *mcpserver.MCPServer, execTools *exec.Tools) {
	// exec.shell - Execute shell command
	mcpServer.AddTool(
		mcp.NewTool(
			"exec.shell",
			mcp.WithDescription("Execute a shell command with arguments"),
			mcp.WithString("cmd", mcp.Required(), mcp.Description("Command to execute")),
			mcp.WithArray("argv", mcp.WithStringItems(), mcp.Description("Command arguments as separate items")),
			mcp.WithString("cwd", mcp.Description("Optional working directory")),
			mcp.WithObject("env", mcp.Description("Optional environment variables to set")),
			mcp.WithInteger("timeout_seconds", mcp.Description("Optional timeout in seconds")),
		),
		execToolShellHandler(execTools),
	)

	// exec.script - Execute a script
	mcpServer.AddTool(
		mcp.NewTool(
			"exec.script",
			mcp.WithDescription("Execute a script in a specified language"),
			mcp.WithString("language", mcp.Required(), mcp.Description("Programming language: sh, bash, or python3")),
			mcp.WithString("content", mcp.Required(), mcp.Description("Script content to execute")),
			mcp.WithArray("args", mcp.WithStringItems(), mcp.Description("Optional script arguments")),
			mcp.WithString("cwd", mcp.Description("Optional working directory")),
			mcp.WithObject("env", mcp.Description("Optional environment variables to set")),
			mcp.WithInteger("timeout_seconds", mcp.Description("Optional timeout in seconds")),
		),
		execToolScriptHandler(execTools),
	)
}

// Tool handler adapters - convert mcp.CallToolRequest to our tool-specific request types

func fsToolListHandler(fsTools *filesystem.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := request.GetString("path", ".")
		pattern := request.GetString("pattern", "")
		recursive := request.GetBool("recursive", false)

		glob := &pattern
		if pattern == "" {
			glob = nil
		}
		resp, err := fsTools.HandleList(ctx, &filesystem.ListRequest{
			Path:      path,
			Glob:      glob,
			Recursive: &recursive,
		})
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent(fmt.Sprintf("Error: %v", err)),
				},
				IsError: true,
			}, nil
		}

		respJSON, _ := json.MarshalIndent(resp, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(string(respJSON)),
			},
		}, nil
	}
}

func fsToolReadHandler(fsTools *filesystem.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := request.GetString("path", "")
		offsetVal := int64(request.GetInt("offset", 0))
		lengthVal := int64(request.GetInt("length", 0))

		var offsetPtr, lengthPtr *int64
		if offsetVal != 0 {
			offsetPtr = &offsetVal
		}
		if lengthVal > 0 {
			lengthPtr = &lengthVal
		}

		resp, err := fsTools.HandleRead(ctx, &filesystem.ReadRequest{
			Path:   path,
			Offset: offsetPtr,
			Length: lengthPtr,
		})
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent(fmt.Sprintf("Error: %v", err)),
				},
				IsError: true,
			}, nil
		}

		respJSON, _ := json.MarshalIndent(resp, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(string(respJSON)),
			},
		}, nil
	}
}

func fsToolWriteHandler(fsTools *filesystem.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := request.GetString("path", "")
		content := request.GetString("content", "")
		create := request.GetBool("create", false)
		overwrite := request.GetBool("overwrite", false)

		resp, err := fsTools.HandleWrite(ctx, &filesystem.WriteRequest{
			Path:      path,
			Content:   content,
			Create:    &create,
			Overwrite: &overwrite,
		})
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent(fmt.Sprintf("Error: %v", err)),
				},
				IsError: true,
			}, nil
		}

		respJSON, _ := json.MarshalIndent(resp, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(string(respJSON)),
			},
		}, nil
	}
}

func fsToolStatHandler(fsTools *filesystem.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := request.GetString("path", "")

		resp, err := fsTools.HandleStat(ctx, &filesystem.StatRequest{
			Path: path,
		})
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent(fmt.Sprintf("Error: %v", err)),
				},
				IsError: true,
			}, nil
		}

		respJSON, _ := json.MarshalIndent(resp, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(string(respJSON)),
			},
		}, nil
	}
}

func fsToolDeleteHandler(fsTools *filesystem.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := request.GetString("path", "")
		recursive := request.GetBool("recursive", false)

		resp, err := fsTools.HandleDelete(ctx, &filesystem.DeleteRequest{
			Path:      path,
			Recursive: &recursive,
		})
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent(fmt.Sprintf("Error: %v", err)),
				},
				IsError: true,
			}, nil
		}

		respJSON, _ := json.MarshalIndent(resp, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(string(respJSON)),
			},
		}, nil
	}
}

func execToolShellHandler(execTools *exec.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cmd := request.GetString("cmd", "")
		argv := request.GetStringSlice("argv", nil)
		cwd := request.GetString("cwd", "")
		timeoutSeconds := request.GetInt("timeout_seconds", 0)

		var cwdPtr *string
		if cwd != "" {
			cwdPtr = &cwd
		}

		var envStrMap map[string]string
		args := request.GetArguments()
		if envRaw, ok := args["env"]; ok {
			if envMap, ok := envRaw.(map[string]any); ok {
				envStrMap = make(map[string]string)
				for k, v := range envMap {
					envStrMap[k] = fmt.Sprintf("%v", v)
				}
			}
		}

		var timeoutPtr *int
		if timeoutSeconds > 0 {
			timeoutPtr = &timeoutSeconds
		}

		resp, err := execTools.HandleShell(ctx, &exec.ShellRequest{
			Cmd:            cmd,
			Argv:           argv,
			CWD:            cwdPtr,
			Env:            envStrMap,
			TimeoutSeconds: timeoutPtr,
		})
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent(fmt.Sprintf("Error: %v", err)),
				},
				IsError: true,
			}, nil
		}

		respJSON, _ := json.MarshalIndent(resp, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(string(respJSON)),
			},
		}, nil
	}
}

func execToolScriptHandler(execTools *exec.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		language := request.GetString("language", "")
		content := request.GetString("content", "")
		args := request.GetStringSlice("args", nil)
		cwd := request.GetString("cwd", "")
		timeoutSeconds := request.GetInt("timeout_seconds", 0)

		var cwdPtr *string
		if cwd != "" {
			cwdPtr = &cwd
		}

		var envStrMap map[string]string
		reqArgs := request.GetArguments()
		if envRaw, ok := reqArgs["env"]; ok {
			if envMap, ok := envRaw.(map[string]any); ok {
				envStrMap = make(map[string]string)
				for k, v := range envMap {
					envStrMap[k] = fmt.Sprintf("%v", v)
				}
			}
		}

		var timeoutPtr *int
		if timeoutSeconds > 0 {
			timeoutPtr = &timeoutSeconds
		}

		resp, err := execTools.HandleScript(ctx, &exec.ScriptRequest{
			Language:       language,
			Content:        content,
			Args:           args,
			CWD:            cwdPtr,
			Env:            envStrMap,
			TimeoutSeconds: timeoutPtr,
		})
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent(fmt.Sprintf("Error: %v", err)),
				},
				IsError: true,
			}, nil
		}

		respJSON, _ := json.MarshalIndent(resp, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(string(respJSON)),
			},
		}, nil
	}
}

func policyToolShowHandler(policyTools *policytools.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		resp, err := policyTools.HandleShow(ctx, &policytools.ShowRequest{})
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent(fmt.Sprintf("Error: %v", err)),
				},
				IsError: true,
			}, nil
		}

		respJSON, _ := json.MarshalIndent(resp, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(string(respJSON)),
			},
		}, nil
	}
}

func policyToolTestHandler(policyTools *policytools.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		action := request.GetString("action", "")
		path := request.GetString("path", "")
		cmd := request.GetString("cmd", "")

		resp, err := policyTools.HandleTest(ctx, &policytools.TestRequest{
			Action: action,
			Path:   path,
			Cmd:    cmd,
		})
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent(fmt.Sprintf("Error: %v", err)),
				},
				IsError: true,
			}, nil
		}

		respJSON, _ := json.MarshalIndent(resp, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(string(respJSON)),
			},
		}, nil
	}
}
