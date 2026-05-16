# AnvilMCP Testing Guide

This guide explains how to test the AnvilMCP server and its tools.

## Unit Tests

Run all unit tests:

```bash
make test
```

Run tests with coverage:

```bash
make coverage
```

Current test coverage:
- **Policy Engine**: 8/8 pathMatchesPattern tests passing ✓
- **Exec Tools**: 5/5 tests passing ✓
  - TestShellBasic: Execute simple commands
  - TestShellDeniedCommand: Verify blocked commands are denied
  - TestShellArgRestriction: Verify argument restrictions work
  - TestScriptExecution: Execute scripts in sh/bash/python3
  - TestConcurrentLimit: Verify execution limits enforced
- **Filesystem Tools**: Integration tests (some test infrastructure issues)
- **Policy Tools**: Ready for integration testing

## Starting the Server

### Basic Start

```bash
make run
```

This starts the server in development mode on port 8080.

### Custom Configuration

Start with custom policy and ports:

```bash
# Create your policy file
mkdir -p ~/.config/mcp-session
cp .mcp-policy.yaml.example ~/.config/mcp-session/policy.yaml

# Start server with custom settings
go run ./cmd/anvilmcp serve \
  --port :8888 \
  --policy ~/.config/mcp-session/policy.yaml \
  --history-db ~/.local/state/mcp-session/history.db
```

### Environment Variables

Set these before starting the server:

```bash
export SESSION_ID=test-session-123      # Unique session identifier
export WORKSPACE_ROOT=/workspace         # Working directory
export HOME=$(eval echo ~$USER)          # User home directory
export USER=$(whoami)                    # Current user
export PYTHON_PATH=/usr/bin/python3      # Python executable path
export VENV_PATH=/path/to/venv           # Virtual env (optional)

go run ./cmd/anvilmcp serve
```

## Testing Tools

### Using curl to Test MCP Server

The MCP server uses HTTP with Streamable HTTP transport. Test a tool call:

```bash
curl -X POST http://localhost:8080/sse \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "fs.list",
      "arguments": {
        "path": ".",
        "recursive": false
      }
    }
  }'
```

### Using MCP Client

If you have an MCP client installed, connect to the server:

```bash
mcp-client http://localhost:8080/sse
```

Then list available tools:

```
> tools/list
```

And call a tool:

```
> tools/call fs.list '{"path": "."}'
```

## Testing Individual Tools

### Filesystem Tools

**fs.list** - List directory contents:
```json
{
  "path": ".",
  "recursive": false,
  "pattern": "*.txt"
}
```

**fs.read** - Read file:
```json
{
  "path": "test.txt",
  "offset": 0,
  "length": 100
}
```

**fs.write** - Write file:
```json
{
  "path": "output.txt",
  "content": "Hello, World!",
  "create": true,
  "overwrite": true
}
```

**fs.stat** - Get file metadata:
```json
{
  "path": "test.txt"
}
```

**fs.delete** - Delete file:
```json
{
  "path": "temp.txt",
  "recursive": false
}
```

### Execution Tools

**exec.shell** - Execute shell command:
```json
{
  "cmd": "python3",
  "argv": ["-c", "print('Hello')"],
  "cwd": ".",
  "timeout_seconds": 10
}
```

**exec.script** - Execute script:
```json
{
  "language": "python3",
  "content": "print('Hello, World!')\nprint('Line 2')",
  "args": [],
  "timeout_seconds": 30
}
```

### Policy Tools

**policy.show** - Display current policy:
```json
{}
```

**policy.test** - Test policy rules:
```json
{
  "action": "read",
  "path": "/workspace/file.txt"
}
```

Test write access:
```json
{
  "action": "write",
  "path": "/workspace/output.txt"
}
```

Test command execution:
```json
{
  "action": "exec",
  "cmd": "python3"
}
```

## Integration Testing Checklist

- [ ] **Server Startup**: `make run` starts successfully
- [ ] **Tool Registration**: All 11 tools are listed by `tools/list`
- [ ] **Filesystem Operations**: 
  - [ ] fs.list works on existing directory
  - [ ] fs.read works on existing file
  - [ ] fs.write creates/updates files
  - [ ] fs.delete removes files
  - [ ] fs.stat returns file metadata
  - [ ] Policy deny rules block access
- [ ] **Execution Operations**:
  - [ ] exec.shell runs allowed commands
  - [ ] exec.shell blocks denied commands
  - [ ] exec.shell respects timeout
  - [ ] exec.script runs sh/bash/python3 scripts
- [ ] **Policy Operations**:
  - [ ] policy.show returns current policy
  - [ ] policy.test correctly evaluates read access
  - [ ] policy.test correctly evaluates write access
  - [ ] policy.test correctly evaluates exec access
- [ ] **Error Handling**:
  - [ ] Invalid arguments return proper errors
  - [ ] Missing required parameters return errors
  - [ ] Policy violations return clear error messages
- [ ] **Concurrent Operations**:
  - [ ] Multiple tool calls work simultaneously
  - [ ] Execution limits are enforced
  - [ ] Output is capped at limits

## Performance Testing

Test server performance:

```bash
# Generate test load
for i in {1..100}; do
  curl -s -X POST http://localhost:8080/sse \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":'$i',"method":"tools/list"}' &
done
wait
```

Monitor resource usage:

```bash
# Watch server memory and CPU
while true; do
  ps aux | grep anvilmcp-server | grep -v grep
  sleep 1
done
```

## Debugging

### Enable Debug Logging

Set environment variable before starting:

```bash
export ANVILMCP_DEV=true
go run ./cmd/anvilmcp serve
```

This enables detailed logging for debugging.

### Inspect Server State

View logs and errors:

```bash
# View recent logs
tail -f /tmp/anvilmcp.log  # If logging to file

# Monitor live server output
make run 2>&1 | tee /tmp/anvilmcp-debug.log
```

### Test Policy Configuration

Create a test policy:

```bash
mkdir -p ~/.config/mcp-session

cat > ~/.config/mcp-session/policy.yaml << 'EOF'
version: 1

session:
  cwd: /workspace

paths:
  workspace_root: /workspace
  allow_read:
    - /workspace/**
  allow_write:
    - /workspace/**
  deny:
    - /workspace/.git/**

exec:
  default: deny
  allow:
    python3: {}
    bash: {}

env:
  redact_patterns:
    - '(?i)secret'
    - '(?i)token'

kernel:
  default: allow

audit:
  retention_days: 30
EOF
```

Then test your policy:

```bash
curl -X POST http://localhost:8080/sse \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "policy.test",
      "arguments": {
        "action": "read",
        "path": "/workspace/test.txt"
      }
    }
  }'
```

## Troubleshooting

### Server won't start

```bash
# Check if port is in use
lsof -i :8080

# Try different port
go run ./cmd/anvilmcp serve --port :9090
```

### Policy file not found

```bash
# Create the config directory
mkdir -p ~/.config/mcp-session

# Copy example policy
cp .mcp-policy.yaml.example ~/.config/mcp-session/policy.yaml
```

### Tools not responding

```bash
# Verify server is running
curl http://localhost:8080/tools/list

# Check server logs
ANVILMCP_DEV=true go run ./cmd/anvilmcp serve
```

### Permission denied errors

```bash
# Check policy rules
curl -X POST http://localhost:8080/sse \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"policy.show"}}'

# Test specific path
curl -X POST http://localhost:8080/sse \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"policy.test","arguments":{"action":"read","path":"/workspace/file.txt"}}}'
```

## Next Steps

- [ ] Implement remaining Phase 1 tools (environment, workspace, history)
- [ ] Build MCP client library for Go
- [ ] Integration with Claude
- [ ] Phase 2: Python kernel implementation
