package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

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
	logger.Info("Loading policies",
		zap.String("user_policy", userPolicyPath),
		zap.String("cluster_policy", clusterPolicyPath),
	)
	policyLoader, err := policy.NewLoader(userPolicyPath, clusterPolicyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load policies: %w", err)
	}

	effectivePolicy := policyLoader.Effective()
	policyEngine := policy.NewEngine(effectivePolicy, policyLoader.PolicyFilePath())
	logger.Info("Policies loaded",
		zap.String("effective_policy_path", policyLoader.PolicyFilePath()),
		zap.Int("allow_read_rules", len(effectivePolicy.Paths.AllowRead)),
		zap.Int("allow_write_rules", len(effectivePolicy.Paths.AllowWrite)),
		zap.Int("exec_allow_rules", len(effectivePolicy.Exec.Allow)),
		zap.String("exec_default", effectivePolicy.Exec.Default),
	)

	logger.Info("Initializing history store", zap.String("db_path", historyDBPath))
	histStore, err := history.NewStore(ctx, historyDBPath, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize history store: %w", err)
	}

	mcpServer := mcpserver.NewMCPServer(
		"anvilmcp",
		"0.1.0",
		mcpserver.WithResourceCapabilities(true, true),
		mcpserver.WithToolCapabilities(true),
	)

	fsTools := filesystem.NewTools(policyEngine, logger)
	execTools := exec.NewTools(policyEngine, logger, sessConfig, 4)
	policyToolsHandler := policytools.NewTools(policyEngine, logger)

	registerFilesystemTools(mcpServer, fsTools, logger)
	registerExecTools(mcpServer, execTools, logger)
	registerPolicyTools(mcpServer, policyToolsHandler, logger)
	registerResources(mcpServer, fsTools, policyToolsHandler, logger)

	srv := &Server{
		logger:        logger,
		sessionConfig: sessConfig,
		policyLoader:  policyLoader,
		policyEngine:  policyEngine,
		historyStore:  histStore,
		mcpServer:     mcpServer,
	}

	srv.httpServer = mcpserver.NewStreamableHTTPServer(mcpServer)

	logger.Info("Server initialized",
		zap.String("session_id", sessConfig.SessionID),
		zap.String("workspace_root", sessConfig.WorkspaceRoot),
		zap.String("user_id", sessConfig.UserID),
		zap.String("home_dir", sessConfig.HomeDir),
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

// loggedTool wraps a tool handler to log invocations and result-level errors.
func loggedTool(logger *zap.Logger, name string, h mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger.Info("tool invoked", zap.String("tool", name))
		result, err := h(ctx, request)
		if err != nil {
			logger.Error("tool failed", zap.String("tool", name), zap.Error(err))
		} else if result != nil && result.IsError {
			logger.Warn("tool returned error", zap.String("tool", name))
		}
		return result, err
	}
}

// loggedResource wraps a resource handler to log reads.
func loggedResource(logger *zap.Logger, uri string, h mcpserver.ResourceHandlerFunc) mcpserver.ResourceHandlerFunc {
	return func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		logger.Info("resource read", zap.String("uri", uri))
		return h(ctx, request)
	}
}

// loggedResourceTemplate wraps a templated resource handler to log reads.
func loggedResourceTemplate(logger *zap.Logger, template string, h mcpserver.ResourceTemplateHandlerFunc) mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		logger.Info("resource read", zap.String("template", template), zap.String("uri", request.Params.URI))
		return h(ctx, request)
	}
}

func registerFilesystemTools(mcpServer *mcpserver.MCPServer, fsTools *filesystem.Tools, logger *zap.Logger) {
	mcpServer.AddTool(
		mcp.NewTool(
			"fs.list",
			mcp.WithDescription("List directory contents, optionally filtered by glob pattern"),
			mcp.WithString("path", mcp.Required(), mcp.Description("Directory path to list (relative or absolute)")),
			mcp.WithString("pattern", mcp.Description("Optional glob pattern to filter results")),
			mcp.WithBoolean("recursive", mcp.Description("Whether to list recursively")),
		),
		loggedTool(logger, "fs.list", fsToolListHandler(fsTools)),
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"fs.read",
			mcp.WithDescription("Read file contents with optional offset and length limiting"),
			mcp.WithString("path", mcp.Required(), mcp.Description("File path to read (relative or absolute)")),
			mcp.WithInteger("offset", mcp.Description("Optional byte offset to start reading from")),
			mcp.WithInteger("length", mcp.Description("Optional number of bytes to read")),
		),
		loggedTool(logger, "fs.read", fsToolReadHandler(fsTools)),
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"fs.write",
			mcp.WithDescription("Write or update file contents"),
			mcp.WithString("path", mcp.Required(), mcp.Description("File path to write to (relative or absolute)")),
			mcp.WithString("content", mcp.Required(), mcp.Description("Content to write")),
			mcp.WithBoolean("create", mcp.Description("Whether to create if missing")),
			mcp.WithBoolean("overwrite", mcp.Description("Whether to overwrite if exists")),
		),
		loggedTool(logger, "fs.write", fsToolWriteHandler(fsTools)),
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"fs.stat",
			mcp.WithDescription("Get file or directory metadata"),
			mcp.WithString("path", mcp.Required(), mcp.Description("File or directory path (relative or absolute)")),
		),
		loggedTool(logger, "fs.stat", fsToolStatHandler(fsTools)),
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"fs.delete",
			mcp.WithDescription("Delete a file or directory"),
			mcp.WithString("path", mcp.Required(), mcp.Description("Path to delete (relative or absolute)")),
			mcp.WithBoolean("recursive", mcp.Description("If true, delete directories and their contents")),
		),
		loggedTool(logger, "fs.delete", fsToolDeleteHandler(fsTools)),
	)
}

func registerPolicyTools(mcpServer *mcpserver.MCPServer, policyTools *policytools.Tools, logger *zap.Logger) {
	mcpServer.AddTool(
		mcp.NewTool(
			"policy.show",
			mcp.WithDescription("Display the current effective security policy"),
		),
		loggedTool(logger, "policy.show", policyToolShowHandler(policyTools)),
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"policy.test",
			mcp.WithDescription("Test whether an action would be allowed under the current policy"),
			mcp.WithString("action", mcp.Required(), mcp.Description("Action to test: read, write, or exec")),
			mcp.WithString("path", mcp.Description("File path (required for read/write actions)")),
			mcp.WithString("cmd", mcp.Description("Command name (required for exec actions)")),
		),
		loggedTool(logger, "policy.test", policyToolTestHandler(policyTools)),
	)
}

func registerExecTools(mcpServer *mcpserver.MCPServer, execTools *exec.Tools, logger *zap.Logger) {
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
		loggedTool(logger, "exec.shell", execToolShellHandler(execTools)),
	)

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
		loggedTool(logger, "exec.script", execToolScriptHandler(execTools)),
	)
}

// registerResources exposes a read-only resource view of policy, file contents,
// and file metadata. Tools and resources intentionally cover overlapping data:
// tools are the model-mediated path (LLM calls them with parameters; required
// for writes, exec, and parameterized reads like offset/length); resources are
// the host-mediated path that lets a UI browse, pin, or stream content
// without going through the model. The MCP spec encourages this duality so
// clients that don't speak resources still work, and clients that do can
// expose richer UX. See https://modelcontextprotocol.io/specification.
func registerResources(mcpServer *mcpserver.MCPServer, fsTools *filesystem.Tools, policyTools *policytools.Tools, logger *zap.Logger) {
	mcpServer.AddResource(
		mcp.NewResource(
			"policy://effective",
			"Effective security policy",
			mcp.WithResourceDescription("Current effective policy (cluster ∩ user) governing path, exec, and env decisions"),
			mcp.WithMIMEType("application/json"),
		),
		loggedResource(logger, "policy://effective", policyResourceHandler(policyTools)),
	)

	mcpServer.AddResourceTemplate(
		mcp.NewResourceTemplate(
			"file:///{+path}",
			"Workspace file content",
			mcp.WithTemplateDescription("Full contents of a file at the given absolute path; use the fs.read tool for paged reads"),
		),
		loggedResourceTemplate(logger, "file:///{+path}", fileResourceHandler(fsTools)),
	)

	mcpServer.AddResourceTemplate(
		mcp.NewResourceTemplate(
			"stat:///{+path}",
			"File metadata",
			mcp.WithTemplateDescription("Type, size, mtime, and permissions for a file or directory at the given absolute path"),
			mcp.WithTemplateMIMEType("application/json"),
		),
		loggedResourceTemplate(logger, "stat:///{+path}", statResourceHandler(fsTools)),
	)
}

func policyResourceHandler(policyTools *policytools.Tools) mcpserver.ResourceHandlerFunc {
	return func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		resp, err := policyTools.HandleShow(ctx, &policytools.ShowRequest{})
		if err != nil {
			return nil, err
		}
		body, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("failed to marshal policy: %w", err)
		}
		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      request.Params.URI,
				MIMEType: "application/json",
				Text:     string(body),
			},
		}, nil
	}
}

func fileResourceHandler(fsTools *filesystem.Tools) mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		path, err := pathFromResourceURI(request.Params.URI)
		if err != nil {
			return nil, err
		}
		resp, err := fsTools.HandleRead(ctx, &filesystem.ReadRequest{Path: path})
		if err != nil {
			return nil, err
		}
		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:  request.Params.URI,
				Text: resp.Content,
			},
		}, nil
	}
}

func statResourceHandler(fsTools *filesystem.Tools) mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		path, err := pathFromResourceURI(request.Params.URI)
		if err != nil {
			return nil, err
		}
		resp, err := fsTools.HandleStat(ctx, &filesystem.StatRequest{Path: path})
		if err != nil {
			return nil, err
		}
		body, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("failed to marshal stat: %w", err)
		}
		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      request.Params.URI,
				MIMEType: "application/json",
				Text:     string(body),
			},
		}, nil
	}
}

// pathFromResourceURI extracts the filesystem path from a resource URI like
// "file:///etc/hosts" or "stat:///workspace/foo.py". Policy enforcement still
// runs inside the underlying tool handler via ResolvePath.
func pathFromResourceURI(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid resource URI %q: %w", raw, err)
	}
	if u.Path == "" {
		return "", fmt.Errorf("resource URI %q has no path component", raw)
	}
	return u.Path, nil
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
