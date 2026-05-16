# Docker Integration Testing Guide

This guide covers building and running the AnvilMCP server in a Docker container for integration testing.

## Overview

The provided `Dockerfile` creates a production-ready container that includes:

- **Base**: Ubuntu 24.04 LTS
- **Python**: 3.11 via micromamba
- **Dependencies**: pandas, numpy, scipy, scikit-learn, matplotlib, jupyter, ipython, requests
- **Security**: Runs as non-root user (`anvilmcp`, UID 1000)
- **Health Checks**: Built-in health check endpoint

## Building the Image

### Quick Build

```bash
make docker-build
```

This will:
1. Run tests to ensure build quality
2. Build the Docker image
3. Tag it as `anvilmcp:latest` and `anvilmcp:<git-short-hash>`

### Manual Build

```bash
docker build -t anvilmcp:latest .
```

## Running the Container

### Quick Start

```bash
make docker-run
```

This will:
1. Start the container with MCP server on localhost:8080
2. Mount local workspace and config directories
3. Display server logs in the terminal

### Manual Docker Run

```bash
docker run -it --rm \
  -p 8080:8080 \
  -v $(pwd)/workspace:/home/anvilmcp/workspace \
  -v $(pwd)/.config/mcp-session:/home/anvilmcp/.config/mcp-session \
  anvilmcp:latest
```

### Advanced Options

**Run in background with detach mode:**
```bash
docker run -d --rm \
  --name anvilmcp-server \
  -p 8080:8080 \
  -v $(pwd)/workspace:/home/anvilmcp/workspace \
  -v $(pwd)/.config/mcp-session:/home/anvilmcp/.config/mcp-session \
  anvilmcp:latest
```

**Run with custom environment variables:**
```bash
docker run -it --rm \
  -p 8080:8080 \
  -e ANVILMCP_DEV=1 \
  -e LOG_LEVEL=debug \
  -v $(pwd)/workspace:/home/anvilmcp/workspace \
  -v $(pwd)/.config/mcp-session:/home/anvilmcp/.config/mcp-session \
  anvilmcp:latest
```

**Run with custom workspace:**
```bash
docker run -it --rm \
  -p 8080:8080 \
  -v /custom/workspace:/home/anvilmcp/workspace \
  anvilmcp:latest
```

## Validating the MCP Server

### 1. Health Check

The container includes a health check endpoint:

```bash
curl http://localhost:8080/health
```

Expected response: `200 OK` with health status.

### 2. Basic MCP Call

Test the filesystem listing tool:

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
        "path": "/home/anvilmcp/workspace"
      }
    }
  }'
```

Expected response: JSON listing of workspace directory contents.

### 3. Test File Operations

Create a test file via the MCP:

```bash
curl -X POST http://localhost:8080/sse \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 2,
    "method": "tools/call",
    "params": {
      "name": "fs.write",
      "arguments": {
        "path": "/home/anvilmcp/workspace/test.txt",
        "content": "Hello from AnvilMCP!",
        "create": true
      }
    }
  }'
```

Read the file back:

```bash
curl -X POST http://localhost:8080/sse \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 3,
    "method": "tools/call",
    "params": {
      "name": "fs.read",
      "arguments": {
        "path": "/home/anvilmcp/workspace/test.txt"
      }
    }
  }'
```

### 4. Test Script Execution

Execute a Python script:

```bash
curl -X POST http://localhost:8080/sse \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 4,
    "method": "tools/call",
    "params": {
      "name": "exec.script",
      "arguments": {
        "language": "python3",
        "content": "import pandas as pd\nimport numpy as np\n\ndata = {\"A\": [1, 2, 3], \"B\": [4, 5, 6]}\ndf = pd.DataFrame(data)\nprint(df.describe())"
      }
    }
  }'
```

### 5. Using a Test Client

Create a test script to validate multiple operations:

```bash
cat > /tmp/test_mcp.sh << 'EOF'
#!/bin/bash

BASE_URL="http://localhost:8080"

echo "Testing AnvilMCP Server..."
echo ""

# Test 1: Health check
echo "1. Testing health endpoint..."
HEALTH=$(curl -s -o /dev/null -w "%{http_code}" ${BASE_URL}/health)
if [ "$HEALTH" = "200" ]; then
    echo "   ✓ Health check passed"
else
    echo "   ✗ Health check failed (HTTP $HEALTH)"
    exit 1
fi

# Test 2: List workspace
echo "2. Testing fs.list..."
RESPONSE=$(curl -s -X POST ${BASE_URL}/sse \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "fs.list",
      "arguments": {"path": "/home/anvilmcp/workspace"}
    }
  }')

if echo "$RESPONSE" | grep -q '"entries"'; then
    echo "   ✓ fs.list passed"
else
    echo "   ✗ fs.list failed"
    echo "   Response: $RESPONSE"
    exit 1
fi

# Test 3: Write file
echo "3. Testing fs.write..."
RESPONSE=$(curl -s -X POST ${BASE_URL}/sse \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 2,
    "method": "tools/call",
    "params": {
      "name": "fs.write",
      "arguments": {
        "path": "/home/anvilmcp/workspace/integration_test.txt",
        "content": "Integration test passed!",
        "create": true
      }
    }
  }')

if echo "$RESPONSE" | grep -q '"bytes_written"'; then
    echo "   ✓ fs.write passed"
else
    echo "   ✗ fs.write failed"
    echo "   Response: $RESPONSE"
    exit 1
fi

# Test 4: Read file
echo "4. Testing fs.read..."
RESPONSE=$(curl -s -X POST ${BASE_URL}/sse \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 3,
    "method": "tools/call",
    "params": {
      "name": "fs.read",
      "arguments": {"path": "/home/anvilmcp/workspace/integration_test.txt"}
    }
  }')

if echo "$RESPONSE" | grep -q "Integration test passed"; then
    echo "   ✓ fs.read passed"
else
    echo "   ✗ fs.read failed"
    echo "   Response: $RESPONSE"
    exit 1
fi

echo ""
echo "All integration tests passed! ✓"
EOF

chmod +x /tmp/test_mcp.sh
/tmp/test_mcp.sh
```

## Debugging

### View Container Logs

When running in interactive mode, logs appear directly in the terminal.

For background containers:

```bash
docker logs anvilmcp-server -f
```

### Inspect Container

```bash
# List running containers
docker ps

# Execute shell in running container
docker exec -it anvilmcp-server /bin/bash

# View container stats
docker stats anvilmcp-server
```

### Check Permissions

The container runs as user `anvilmcp` (UID 1000):

```bash
docker exec anvilmcp-server whoami
# Output: anvilmcp

docker exec anvilmcp-server id
# Output: uid=1000(anvilmcp) gid=1000(anvilmcp) groups=1000(anvilmcp)
```

## Production Deployment

For production use, consider:

1. **Volume Mounts**: Use named volumes instead of bind mounts
   ```bash
   docker volume create mcp-workspace
   docker volume create mcp-config
   
   docker run -d \
     -v mcp-workspace:/home/anvilmcp/workspace \
     -v mcp-config:/home/anvilmcp/.config/mcp-session \
     anvilmcp:latest
   ```

2. **Network**: Use a custom bridge network
   ```bash
   docker network create mcp-net
   docker run -d --network mcp-net --name mcp anvilmcp:latest
   ```

3. **Resource Limits**: Set CPU and memory limits
   ```bash
   docker run -d \
     --memory=2g \
     --cpus=2 \
     anvilmcp:latest
   ```

4. **Restart Policy**: Add restart policy
   ```bash
   docker run -d \
     --restart unless-stopped \
     anvilmcp:latest
   ```

## Troubleshooting

### Container exits immediately

Check the logs:
```bash
docker logs anvilmcp-server
```

Common issues:
- Port 8080 already in use: `lsof -i :8080` and kill the process, or use a different port
- Permission denied on workspace directory: Check ownership

### MCP Server not responding

1. Verify health check: `curl http://localhost:8080/health`
2. Check container is running: `docker ps`
3. Verify port mapping: `docker port anvilmcp-server`

### File permissions

The container user `anvilmcp` (UID 1000) must have permission to read/write mounted volumes. If using bind mounts from host, ensure the directory is readable/writable by UID 1000:

```bash
chmod 755 ./workspace
```

## Docker Compose

For more complex setups, create a `docker-compose.yml`:

```yaml
version: '3.8'

services:
  anvilmcp:
    build: .
    image: anvilmcp:latest
    container_name: anvilmcp-server
    ports:
      - "8080:8080"
    volumes:
      - ./workspace:/home/anvilmcp/workspace
      - ./.config/mcp-session:/home/anvilmcp/.config/mcp-session
    environment:
      - ANVILMCP_DEV=1
      - LOG_LEVEL=info
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8080/health"]
      interval: 30s
      timeout: 3s
      retries: 3
      start_period: 5s
```

Run with:
```bash
docker-compose up -d
docker-compose logs -f
```

## Performance Considerations

The micromamba environment takes time to initialize on first run. Subsequent runs are faster. For repeated integration testing, run the container in the background and reuse it for multiple test calls.

## Security Notes

- ✓ Runs as non-root user (UID 1000)
- ✓ No privileged mode
- ✓ Health check endpoint enabled
- ✓ Read-only filesystem support (set `--read-only` flag if needed)
- ⚠ Workspace volumes are writable - mount only necessary directories
- ⚠ No secrets management - use environment variables or config files as needed
