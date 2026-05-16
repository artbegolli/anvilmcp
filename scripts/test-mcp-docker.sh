#!/bin/bash

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
MCP_URL="${MCP_URL:-http://localhost:8080}"
TIMEOUT="${TIMEOUT:-5}"
VERBOSE="${VERBOSE:-0}"

# Helper functions
log_info() {
    echo -e "${GREEN}✓${NC} $1"
}

log_error() {
    echo -e "${RED}✗${NC} $1"
}

log_test() {
    echo -e "${YELLOW}Testing${NC}: $1"
}

log_debug() {
    if [ "$VERBOSE" = "1" ]; then
        echo "[DEBUG] $1"
    fi
}

check_health() {
    log_test "MCP server health"

    RESPONSE=$(curl -s --max-time $TIMEOUT -w "\n%{http_code}" "${MCP_URL}/health" 2>/dev/null || echo "error")
    HTTP_CODE=$(echo "$RESPONSE" | tail -n1)

    if [ "$HTTP_CODE" = "200" ]; then
        log_info "MCP server is healthy"
        return 0
    else
        log_error "MCP server health check failed (HTTP $HTTP_CODE)"
        return 1
    fi
}

call_mcp_tool() {
    local tool_name=$1
    local arguments=$2
    local call_id=${3:-1}

    curl -s --max-time $TIMEOUT -X POST "${MCP_URL}/sse" \
        -H "Content-Type: application/json" \
        -d "{
            \"jsonrpc\": \"2.0\",
            \"id\": ${call_id},
            \"method\": \"tools/call\",
            \"params\": {
                \"name\": \"${tool_name}\",
                \"arguments\": ${arguments}
            }
        }" 2>/dev/null || echo '{"error":"curl failed"}'
}

test_fs_list() {
    log_test "fs.list tool"

    RESPONSE=$(call_mcp_tool "fs.list" '{"path": "/home/anvilmcp/workspace"}' 1)
    log_debug "Response: $RESPONSE"

    if echo "$RESPONSE" | grep -q '"entries"'; then
        log_info "fs.list works correctly"
        return 0
    else
        log_error "fs.list failed"
        log_debug "Response: $RESPONSE"
        return 1
    fi
}

test_fs_write() {
    log_test "fs.write tool"

    TIMESTAMP=$(date +%s%N)
    TEST_FILE="/home/anvilmcp/workspace/test-${TIMESTAMP}.txt"
    TEST_CONTENT="AnvilMCP Docker Integration Test - $(date)"

    RESPONSE=$(call_mcp_tool "fs.write" "{\"path\": \"${TEST_FILE}\", \"content\": \"${TEST_CONTENT}\", \"create\": true}" 2)
    log_debug "Response: $RESPONSE"

    if echo "$RESPONSE" | grep -q '"bytes_written"'; then
        log_info "fs.write works correctly"
        return 0
    else
        log_error "fs.write failed"
        log_debug "Response: $RESPONSE"
        return 1
    fi
}

test_fs_read() {
    log_test "fs.read tool"

    TIMESTAMP=$(date +%s%N)
    TEST_FILE="/home/anvilmcp/workspace/test-${TIMESTAMP}.txt"
    TEST_CONTENT="Read Test Content"

    # First write a test file
    call_mcp_tool "fs.write" "{\"path\": \"${TEST_FILE}\", \"content\": \"${TEST_CONTENT}\", \"create\": true}" 2 > /dev/null

    # Then read it
    RESPONSE=$(call_mcp_tool "fs.read" "{\"path\": \"${TEST_FILE}\"}" 3)
    log_debug "Response: $RESPONSE"

    if echo "$RESPONSE" | grep -q '"content"'; then
        log_info "fs.read works correctly"
        return 0
    else
        log_error "fs.read failed"
        log_debug "Response: $RESPONSE"
        return 1
    fi
}

test_fs_delete() {
    log_test "fs.delete tool"

    TIMESTAMP=$(date +%s%N)
    TEST_FILE="/home/anvilmcp/workspace/delete-test-${TIMESTAMP}.txt"

    # First write a test file
    call_mcp_tool "fs.write" "{\"path\": \"${TEST_FILE}\", \"content\": \"delete me\", \"create\": true}" 2 > /dev/null

    # Then delete it
    RESPONSE=$(call_mcp_tool "fs.delete" "{\"path\": \"${TEST_FILE}\"}" 4)
    log_debug "Response: $RESPONSE"

    if echo "$RESPONSE" | grep -q '"deleted"'; then
        log_info "fs.delete works correctly"
        return 0
    else
        log_error "fs.delete failed"
        log_debug "Response: $RESPONSE"
        return 1
    fi
}

test_exec_shell() {
    log_test "exec.shell tool"

    RESPONSE=$(call_mcp_tool "exec.shell" '{"cmd": "echo", "argv": ["Hello", "from", "Docker"]}' 5)
    log_debug "Response: $RESPONSE"

    if echo "$RESPONSE" | grep -q '"stdout"'; then
        log_info "exec.shell works correctly"
        return 0
    else
        log_error "exec.shell failed"
        log_debug "Response: $RESPONSE"
        return 1
    fi
}

test_exec_python() {
    log_test "exec.script (Python) tool"

    PYTHON_CODE='import sys; print(f"Python {sys.version}"); import pandas as pd; print("pandas available"); import numpy as np; print("numpy available")'
    RESPONSE=$(call_mcp_tool "exec.script" "{\"language\": \"python3\", \"content\": \"${PYTHON_CODE}\"}" 6)
    log_debug "Response: $RESPONSE"

    if echo "$RESPONSE" | grep -q '"stdout"'; then
        log_info "exec.script (Python) works correctly"
        return 0
    else
        log_error "exec.script (Python) failed"
        log_debug "Response: $RESPONSE"
        return 1
    fi
}

test_exec_python_pandas() {
    log_test "exec.script (Python pandas) tool"

    PYTHON_CODE='import pandas as pd; import numpy as np; df = pd.DataFrame({"A": [1, 2, 3], "B": [4, 5, 6]}); print(df.describe())'
    RESPONSE=$(call_mcp_tool "exec.script" "{\"language\": \"python3\", \"content\": \"${PYTHON_CODE}\"}" 7)
    log_debug "Response: $RESPONSE"

    if echo "$RESPONSE" | grep -q '"stdout"'; then
        log_info "exec.script (pandas) works correctly"
        return 0
    else
        log_error "exec.script (pandas) failed"
        log_debug "Response: $RESPONSE"
        return 1
    fi
}

# Main execution
main() {
    echo ""
    echo "======================================"
    echo "AnvilMCP Docker Integration Test Suite"
    echo "======================================"
    echo "Target: ${MCP_URL}"
    echo "Timeout: ${TIMEOUT}s"
    echo ""

    TESTS=(
        "check_health"
        "test_fs_list"
        "test_fs_write"
        "test_fs_read"
        "test_fs_delete"
        "test_exec_shell"
        "test_exec_python"
        "test_exec_python_pandas"
    )

    PASSED=0
    FAILED=0

    for test in "${TESTS[@]}"; do
        if $test; then
            ((PASSED++))
        else
            ((FAILED++))
        fi
    done

    echo ""
    echo "======================================"
    echo "Test Results: ${PASSED} passed, ${FAILED} failed"
    echo "======================================"
    echo ""

    if [ $FAILED -eq 0 ]; then
        echo -e "${GREEN}All tests passed!${NC}"
        exit 0
    else
        echo -e "${RED}Some tests failed!${NC}"
        exit 1
    fi
}

# Run if called directly
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
