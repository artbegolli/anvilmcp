# MCP Inspector Testing Guide

This guide explains how to test AnvilMCP using MCP Inspector, a debugging and testing tool for Model Context Protocol servers.

## Overview

MCP Inspector provides a command-line interface for testing MCP servers without needing a full client implementation. It supports the Streamable HTTP transport that AnvilMCP uses.

## Setup

### Prerequisites

- MCP Inspector installed locally
- Docker running with AnvilMCP image built
- Port 8080 available

### Start AnvilMCP Server

```bash
cd /path/to/anvilmcp

docker run --rm \
  -p 8080:8080 \
  -v $(pwd)/workspace:/home/anvilmcp/workspace \
  -v $(pwd)/.config/mcp-session:/home/anvilmcp/.config/mcp-session \
  anvilmcp:latest
```

The server listens on `http://localhost:8080/mcp` using Streamable HTTP transport.

## MCP Inspector Configuration

### Option 1: Streamable HTTP (Recommended for Local Testing)

Create or update your MCP Inspector configuration with:

```json
{
  "anvilmcp": {
    "url": "http://localhost:8080",
    "transport": "streamable-http"
  }
}
```

Then connect to the server:
```bash
mcp-inspector --config path/to/config.json anvilmcp
```

Or directly:
```bash
mcp-inspector --transport streamable-http --url http://localhost:8080
```

### Option 2: Docker Stdio (Alternative)

If you prefer to run the server through MCP Inspector:

```json
{
  "anvilmcp": {
    "command": "docker",
    "args": [
      "run", "--rm",
      "-p", "8080:8080",
      "-v", "$(pwd)/workspace:/home/anvilmcp/workspace",
      "-v", "$(pwd)/.config/mcp-session:/home/anvilmcp/.config/mcp-session",
      "anvilmcp:latest"
    ]
  }
}
```

## Testing with MCP Inspector

Once connected, you can test tools interactively:

### Filesystem Operations

```
# List workspace contents
Tool: fs.list
Arguments: {
  "path": "/home/anvilmcp/workspace"
}

# Read a file
Tool: fs.read
Arguments: {
  "path": "/home/anvilmcp/workspace/file.txt"
}

# Write a file
Tool: fs.write
Arguments: {
  "path": "/home/anvilmcp/workspace/test.txt",
  "content": "Test content",
  "create": true
}

# Get file metadata
Tool: fs.stat
Arguments: {
  "path": "/home/anvilmcp/workspace/test.txt"
}

# Delete a file
Tool: fs.delete
Arguments: {
  "path": "/home/anvilmcp/workspace/test.txt"
}
```

### Shell Execution

```
# Run a shell command
Tool: exec.shell
Arguments: {
  "cmd": "ls",
  "argv": ["-la", "/home/anvilmcp/workspace"],
  "timeout_seconds": 10
}

# Run with environment variables
Tool: exec.shell
Arguments: {
  "cmd": "env",
  "argv": [],
  "env": {
    "TEST_VAR": "test_value"
  },
  "timeout_seconds": 5
}
```

### Python Execution

```
# Check Python version
Tool: exec.shell
Arguments: {
  "cmd": "python",
  "argv": ["--version"],
  "timeout_seconds": 10
}

# Test pandas
Tool: exec.shell
Arguments: {
  "cmd": "python",
  "argv": ["-c", "import pandas as pd; print(pd.__version__)"],
  "timeout_seconds": 10
}

# Test numpy, scipy, scikit-learn
Tool: exec.shell
Arguments: {
  "cmd": "python",
  "argv": ["-c", "import numpy, scipy, sklearn; print('All packages available')"],
  "timeout_seconds": 10
}
```

### Policy Management

```
# Show current policy
Tool: policy.show
Arguments: {}

# Test read permission
Tool: policy.test
Arguments: {
  "action": "read",
  "path": "/home/anvilmcp/workspace/file.txt"
}

# Test write permission
Tool: policy.test
Arguments: {
  "action": "write",
  "path": "/home/anvilmcp/workspace/newfile.txt"
}

# Test exec permission
Tool: policy.test
Arguments: {
  "action": "exec",
  "cmd": "ls"
}
```

## Automated Testing Scripts

### Quick Test Generator

View all available test requests:

```bash
bash scripts/mcp-inspector-tests.sh | head -100
```

### Interactive Testing

1. Start the server:
```bash
docker run --rm -p 8080:8080 -v $(pwd)/workspace:/home/anvilmcp/workspace -v $(pwd)/.config/mcp-session:/home/anvilmcp/.config/mcp-session anvilmcp:latest
```

2. In another terminal, start MCP Inspector:
```bash
mcp-inspector --transport streamable-http --url http://localhost:8080
```

3. Test each tool as shown above

## Common Test Scenarios

### Test 1: Basic Filesystem Operations

```
1. fs.list /home/anvilmcp/workspace
2. fs.write /home/anvilmcp/workspace/test.txt "Content"
3. fs.read /home/anvilmcp/workspace/test.txt
4. fs.stat /home/anvilmcp/workspace/test.txt
5. fs.delete /home/anvilmcp/workspace/test.txt
6. fs.list /home/anvilmcp/workspace (verify deletion)
```

### Test 2: Python Data Science Stack

```
1. exec.shell python --version
2. exec.shell python -c "import pandas; print(pandas.__version__)"
3. exec.shell python -c "import numpy; print(numpy.__version__)"
4. exec.shell python -c "import scipy; print(scipy.__version__)"
5. exec.shell python -c "from sklearn import datasets; print('sklearn OK')"
```

### Test 3: Policy Enforcement

```
1. policy.show (view all rules)
2. policy.test read /home/anvilmcp/workspace
3. policy.test read /etc/passwd (should be denied)
4. policy.test exec ls (should be allowed)
5. policy.test exec rm -rf / (should be denied)
```

### Test 4: Error Handling

```
1. fs.read /nonexistent/file.txt (expect error)
2. fs.write /readonly/file.txt "content" (expect permission denied)
3. exec.shell sleep 1000 with timeout_seconds=1 (expect timeout)
```

## Troubleshooting

### Server Not Responding

```bash
# Check if server is running
curl http://localhost:8080/mcp

# Check container logs
docker logs $(docker ps | grep anvilmcp | awk '{print $1}')

# Verify port binding
lsof -i :8080
```

### Session Issues

- The server uses stateless sessions by default
- Each request should include proper MCP protocol framing
- Session IDs are managed automatically by the client

### Network Issues

- Ensure port 8080 is not blocked by firewall
- For remote testing, use port forwarding or expose the service properly
- CORS is not enabled by default

## Advanced Usage

### Custom Policy Testing

Edit `.config/mcp-session/policy.yaml` to test different policies:

```yaml
allow:
  - path: /home/anvilmcp/workspace/**
  - cmd: "ls,cat,python"

deny:
  - path: /etc/**
  - path: /root/**
```

Restart the server for changes to take effect.

### Monitoring Tool Execution

Enable verbose logging:

```bash
VERBOSE=1 bash scripts/run-mcp-tests.sh
```

### Performance Testing

Test with large files or heavy computations:

```
# Create large file
Tool: exec.shell
Arguments: {
  "cmd": "dd",
  "argv": ["if=/dev/zero", "of=/home/anvilmcp/workspace/large-file.bin", "bs=1M", "count=100"],
  "timeout_seconds": 30
}

# Read large file
Tool: fs.read
Arguments: {
  "path": "/home/anvilmcp/workspace/large-file.bin"
}
```

## Known Limitations

1. **Stream Resumability**: Not currently supported (MCP spec feature)
2. **Binary Data**: May have encoding issues with large files
3. **Health Check Endpoint**: `/health` endpoint is not implemented (cosmetic issue)
4. **Session Persistence**: Sessions are stateless by default

## Getting Help

- Check tool documentation: `policy.show` for current rules
- Review logs: `docker logs <container_id>`
- Verify policy with `policy.test` before executing operations
- Use `fs.stat` to verify file properties before operations

## Integration with CI/CD

To integrate automated testing into your CI/CD pipeline:

```bash
# Start server in background
docker run -d --name anvilmcp-test -p 8080:8080 \
  -v $(pwd)/workspace:/home/anvilmcp/workspace \
  -v $(pwd)/.config/mcp-session:/home/anvilmcp/.config/mcp-session \
  anvilmcp:latest

# Run tests
mcp-inspector --transport streamable-http --url http://localhost:8080 < test-requests.json

# Clean up
docker stop anvilmcp-test
docker rm anvilmcp-test
```

## References

- [MCP Specification](https://modelcontextprotocol.io/)
- [MCP Inspector Documentation](https://github.com/modelcontextprotocol/inspector)
- [Streamable HTTP Transport](https://modelcontextprotocol.io/specification/2025-03-26/basic/transports#streamable-http)
