package server

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"

	"github.com/artbegolli/anvilmcp/internal/metrics"
	kerneltools "github.com/artbegolli/anvilmcp/internal/tools/kernel"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// registerKernelTools registers the kernel.* tools. Only called when kernels
// are enabled (KERNEL_ENABLED); each handler is wrapped for logging, metrics,
// and panic recovery like every other tool.
func registerKernelTools(mcpServer *mcpserver.MCPServer, kt *kerneltools.Tools, logger *zap.Logger, m *metrics.Registry) {
	mcpServer.AddTool(
		mcp.NewTool(
			"kernel.start",
			mcp.WithDescription("Start a persistent Python kernel whose state (variables, imports) survives across kernel.exec calls"),
			mcp.WithObject("env", mcp.Description("Environment to launch in: {type: system, ref: ...}; system only in this build")),
			mcp.WithString("name", mcp.Description("Optional label for the kernel")),
		),
		wrapTool(logger, m, "kernel.start", kernelStartHandler(kt)),
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"kernel.exec",
			mcp.WithDescription("Execute Python code in a running kernel; variables and imports persist across calls"),
			mcp.WithString("kernel_id", mcp.Required(), mcp.Description("Kernel to run in (from kernel.start)")),
			mcp.WithString("code", mcp.Required(), mcp.Description("Python source to execute")),
			mcp.WithInteger("timeout_seconds", mcp.Description("Per-execution timeout; interrupt then kill on expiry")),
		),
		wrapTool(logger, m, "kernel.exec", kernelExecHandler(kt)),
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"kernel.list",
			mcp.WithDescription("List running kernels and their state"),
		),
		wrapTool(logger, m, "kernel.list", kernelListHandler(kt)),
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"kernel.status",
			mcp.WithDescription("Report a kernel's state, execution count, and last activity"),
			mcp.WithString("kernel_id", mcp.Required(), mcp.Description("Kernel to inspect")),
		),
		wrapTool(logger, m, "kernel.status", kernelStatusHandler(kt)),
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"kernel.shutdown",
			mcp.WithDescription("Stop a kernel and discard its state"),
			mcp.WithString("kernel_id", mcp.Required(), mcp.Description("Kernel to stop")),
		),
		wrapTool(logger, m, "kernel.shutdown", kernelShutdownHandler(kt)),
	)
}

func kernelStartHandler(kt *kerneltools.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		req := &kerneltools.StartRequest{Name: request.GetString("name", "")}
		if envRaw, ok := request.GetArguments()["env"]; ok {
			if envMap, ok := envRaw.(map[string]any); ok {
				req.Env = &kerneltools.EnvRef{
					Type: mapString(envMap, "type"),
					Ref:  mapString(envMap, "ref"),
				}
			}
		}
		return kernelToolResult(kt.HandleStart(ctx, req))
	}
}

func kernelExecHandler(kt *kerneltools.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		req := &kerneltools.ExecRequest{
			KernelID: request.GetString("kernel_id", ""),
			Code:     request.GetString("code", ""),
		}
		if to := request.GetInt("timeout_seconds", 0); to > 0 {
			req.TimeoutSeconds = &to
		}
		return kernelToolResult(kt.HandleExec(ctx, req))
	}
}

func kernelListHandler(kt *kerneltools.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return kernelToolResult(kt.HandleList(ctx, struct{}{}))
	}
}

func kernelStatusHandler(kt *kerneltools.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return kernelToolResult(kt.HandleStatus(ctx, &kerneltools.StatusRequest{
			KernelID: request.GetString("kernel_id", ""),
		}))
	}
}

func kernelShutdownHandler(kt *kerneltools.Tools) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return kernelToolResult(kt.HandleShutdown(ctx, &kerneltools.ShutdownRequest{
			KernelID: request.GetString("kernel_id", ""),
		}))
	}
}

// kernelToolResult renders a handler's typed response as a JSON tool result, or
// an error result. Generic over the response type.
func kernelToolResult[T any](resp T, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.NewTextContent(fmt.Sprintf("Error: %v", err))},
			IsError: true,
		}, nil
	}
	body, _ := json.MarshalIndent(resp, "", "  ")
	return &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent(string(body))}}, nil
}

func mapString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
