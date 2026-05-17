#!/bin/bash

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Configuration
MCP_URL="${MCP_URL:-http://localhost:8080/mcp}"
VERBOSE="${VERBOSE:-0}"
PASSED=0
FAILED=0
SESSION_ID=""

# Helpers
log_test() {
    echo -e "${YELLOW}▶${NC} $1"
}

log_pass() {
    echo -e "${GREEN}✓${NC} $1"
    ((PASSED++))
}

log_fail() {
    echo -e "${RED}✗${NC} $1"
    if [ "$VERBOSE" = "1" ]; then
        echo "  Response: $2"
    fi
    ((FAILED++))
}

log_section() {
    echo -e "\n${BLUE}=== $1 ===${NC}\n"
}

# Initialize MCP session
init_session() {
    echo -e "${BLUE}Initializing MCP session...${NC}"

    local response=$(curl -s -i -X POST "$MCP_URL" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0"}}}' 2>&1)

    # Extract session ID from header
    SESSION_ID=$(echo "$response" | grep -i "mcp-session-id" | cut -d' ' -f2 | tr -d '\r')

    if [ -z "$SESSION_ID" ]; then
        echo -e "${RED}Failed to initialize session${NC}"
        if [ "$VERBOSE" = "1" ]; then
            echo "Response: $response"
        fi
        exit 1
    fi

    echo -e "${GREEN}✓ Session initialized: $SESSION_ID${NC}\n"
}

# Execute MCP request and check response
run_mcp_test() {
    local test_name=$1
    local tool_name=$2
    local arguments=$3
    local expected_field=${4:-"error"}  # Check for presence of error by default, or specified field
    local should_fail=${5:-false}

    log_test "$test_name"

    local request="{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool_name\",\"arguments\":$arguments}}"

    local response=$(curl -s -X POST "$MCP_URL" \
        -H "Content-Type: application/json" \
        -H "Mcp-Session-Id: $SESSION_ID" \
        -d "$request" 2>&1)

    if [ "$VERBOSE" = "1" ]; then
        echo "[DEBUG] Request: $request"
        echo "[DEBUG] Response: $response"
    fi

    # Check response
    if [ "$should_fail" = "true" ]; then
        # Expect error
        if echo "$response" | grep -q "\"error\""; then
            log_pass "$test_name"
            return 0
        else
            log_fail "$test_name" "$response"
            return 1
        fi
    else
        # Expect success (result field present or no error)
        if echo "$response" | grep -q "\"result\""; then
            log_pass "$test_name"
            return 0
        elif echo "$response" | grep -q "\"error\""; then
            log_fail "$test_name" "$response"
            return 1
        else
            log_pass "$test_name"
            return 0
        fi
    fi
}

# ============================================================================
# SESSION INITIALIZATION
# ============================================================================

init_session

# ============================================================================
# FILESYSTEM TESTS
# ============================================================================

log_section "Filesystem Operations"

run_mcp_test "fs.list - List workspace" "fs.list" \
    '{"path":"/home/anvilmcp/workspace"}' "result"

run_mcp_test "fs.write - Create test file" "fs.write" \
    '{"path":"/home/anvilmcp/workspace/test-integration.txt","content":"AnvilMCP Integration Test\nTimestamp: '"$(date)"'","create":true}' "result"

run_mcp_test "fs.read - Read test file" "fs.read" \
    '{"path":"/home/anvilmcp/workspace/test-integration.txt"}' "result"

run_mcp_test "fs.stat - Get file metadata" "fs.stat" \
    '{"path":"/home/anvilmcp/workspace/test-integration.txt"}' "result"

run_mcp_test "fs.write - Create JSON file" "fs.write" \
    '{"path":"/home/anvilmcp/workspace/test-data.json","content":"{\"name\":\"AnvilMCP\",\"version\":\"0.1.0\"}","create":true}' "result"

run_mcp_test "fs.read - Read JSON file" "fs.read" \
    '{"path":"/home/anvilmcp/workspace/test-data.json"}' "result"

run_mcp_test "fs.delete - Delete test file" "fs.delete" \
    '{"path":"/home/anvilmcp/workspace/test-integration.txt"}' "result"

run_mcp_test "fs.delete - Delete JSON file" "fs.delete" \
    '{"path":"/home/anvilmcp/workspace/test-data.json"}' "result"

# ============================================================================
# EXECUTION TESTS
# ============================================================================

log_section "Shell Execution"

run_mcp_test "exec.shell - Echo command" "exec.shell" \
    '{"cmd":"echo","argv":["Hello from AnvilMCP"]}' "result"

run_mcp_test "exec.shell - List workspace files" "exec.shell" \
    '{"cmd":"ls","argv":["-la","/home/anvilmcp/workspace"]}' "result"

run_mcp_test "exec.shell - Get current date" "exec.shell" \
    '{"cmd":"date","argv":[]}' "result"

run_mcp_test "exec.shell - Check working directory" "exec.shell" \
    '{"cmd":"pwd","argv":[]}' "result"

# ============================================================================
# PYTHON TESTS
# ============================================================================

log_section "Python Integration"

run_mcp_test "Python - Check version" "exec.shell" \
    '{"cmd":"python","argv":["--version"],"timeout_seconds":10}' "result"

run_mcp_test "Python - Test pandas" "exec.shell" \
    '{"cmd":"python","argv":["-c","import pandas as pd; print(f'"'"'pandas {pd.__version__}'"'"')"],"timeout_seconds":10}' "result"

run_mcp_test "Python - Test numpy" "exec.shell" \
    '{"cmd":"python","argv":["-c","import numpy as np; print(f'"'"'numpy {np.__version__}'"'"')"],"timeout_seconds":10}' "result"

run_mcp_test "Python - Test scipy" "exec.shell" \
    '{"cmd":"python","argv":["-c","import scipy; print(f'"'"'scipy {scipy.__version__}'"'"')"],"timeout_seconds":10}' "result"

run_mcp_test "Python - Test scikit-learn" "exec.shell" \
    '{"cmd":"python","argv":["-c","from sklearn import __version__; print(f'"'"'sklearn {__version__}'"'"')"],"timeout_seconds":10}' "result"

run_mcp_test "Python - Test matplotlib" "exec.shell" \
    '{"cmd":"python","argv":["-c","import matplotlib; print(f'"'"'matplotlib {matplotlib.__version__}'"'"')"],"timeout_seconds":10}' "result"

run_mcp_test "Python - Test jupyter" "exec.shell" \
    '{"cmd":"python","argv":["-c","import jupyter; print('"'"'jupyter available'"'"')"],"timeout_seconds":10}' "result"

# ============================================================================
# POLICY TESTS
# ============================================================================

log_section "Policy Management"

run_mcp_test "policy.show - Display policy" "policy.show" \
    '{}' "result"

run_mcp_test "policy.test - Test read allowed" "policy.test" \
    '{"action":"read","path":"/home/anvilmcp/workspace"}' "result"

run_mcp_test "policy.test - Test write allowed" "policy.test" \
    '{"action":"write","path":"/home/anvilmcp/workspace/test.txt"}' "result"

run_mcp_test "policy.test - Test exec allowed" "policy.test" \
    '{"action":"exec","cmd":"ls"}' "result"

# ============================================================================
# ERROR HANDLING TESTS
# ============================================================================

log_section "Error Handling"

run_mcp_test "fs.read - Nonexistent file (should error)" "fs.read" \
    '{"path":"/home/anvilmcp/workspace/nonexistent-file-12345.txt"}' "error" "true"

run_mcp_test "fs.delete - Nonexistent file (should error)" "fs.delete" \
    '{"path":"/home/anvilmcp/workspace/nonexistent-file-12345.txt"}' "error" "true"

# ============================================================================
# SUMMARY
# ============================================================================

log_section "Test Summary"

TOTAL=$((PASSED + FAILED))

if [ $TOTAL -gt 0 ]; then
    PASS_RATE=$((PASSED * 100 / TOTAL))
else
    PASS_RATE=0
fi

echo "Total Tests:  $TOTAL"
echo -e "Passed:       ${GREEN}$PASSED${NC}"
echo -e "Failed:       ${RED}$FAILED${NC}"
echo "Pass Rate:    ${BLUE}${PASS_RATE}%${NC}"

if [ $FAILED -eq 0 ]; then
    echo -e "\n${GREEN}✓ All tests passed!${NC}\n"
    exit 0
else
    echo -e "\n${RED}✗ Some tests failed!${NC}\n"
    exit 1
fi
