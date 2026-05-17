#!/bin/bash

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test configuration
VERBOSE="${VERBOSE:-0}"
FAILED_TESTS=0
PASSED_TESTS=0

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

log_section() {
    echo -e "\n${BLUE}===${NC} $1 ${BLUE}===${NC}\n"
}

log_debug() {
    if [ "$VERBOSE" = "1" ]; then
        echo "[DEBUG] $1"
    fi
}

# Send MCP request via stdin to mcp-inspector
send_mcp_request() {
    local request=$1
    log_debug "Sending request: $request"
    echo "$request"
}

# Test result handler
handle_test_result() {
    local test_name=$1
    local response=$2
    local expected_field=$3

    if echo "$response" | grep -q "$expected_field"; then
        log_info "$test_name"
        ((PASSED_TESTS++))
        return 0
    else
        log_error "$test_name"
        log_debug "Response: $response"
        ((FAILED_TESTS++))
        return 1
    fi
}

# ============================================================================
# FILESYSTEM TESTS
# ============================================================================

log_section "Filesystem Operations"

# Test 1: fs.list - List workspace contents
log_test "fs.list - List workspace directory"
cat <<'EOF'
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fs.list","arguments":{"path":"/home/anvilmcp/workspace"}}}
EOF

# Test 2: fs.write - Create test file
log_test "fs.write - Create test file"
cat <<'EOF'
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fs.write","arguments":{"path":"/home/anvilmcp/workspace/test-file.txt","content":"AnvilMCP Integration Test\nTimestamp: $(date)","create":true}}}
EOF

# Test 3: fs.read - Read test file
log_test "fs.read - Read test file"
cat <<'EOF'
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fs.read","arguments":{"path":"/home/anvilmcp/workspace/test-file.txt"}}}
EOF

# Test 4: fs.stat - Get file metadata
log_test "fs.stat - Get file metadata"
cat <<'EOF'
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fs.stat","arguments":{"path":"/home/anvilmcp/workspace/test-file.txt"}}}
EOF

# Test 5: fs.write - Create JSON test file
log_test "fs.write - Create JSON test file"
cat <<'EOF'
{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"fs.write","arguments":{"path":"/home/anvilmcp/workspace/test-data.json","content":"{\"name\":\"AnvilMCP\",\"version\":\"0.1.0\",\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}","create":true}}}
EOF

# Test 6: fs.read - Read JSON file
log_test "fs.read - Read JSON file"
cat <<'EOF'
{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"fs.read","arguments":{"path":"/home/anvilmcp/workspace/test-data.json"}}}
EOF

# ============================================================================
# EXECUTION TESTS
# ============================================================================

log_section "Shell Execution"

# Test 7: exec.shell - Echo command
log_test "exec.shell - Echo command"
cat <<'EOF'
{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"exec.shell","arguments":{"cmd":"echo","argv":["Hello from AnvilMCP"]}}}
EOF

# Test 8: exec.shell - List files in workspace
log_test "exec.shell - List workspace files"
cat <<'EOF'
{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"exec.shell","arguments":{"cmd":"ls","argv":["-la","/home/anvilmcp/workspace"]}}}
EOF

# Test 9: exec.shell - Get current date
log_test "exec.shell - Get current date"
cat <<'EOF'
{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"exec.shell","arguments":{"cmd":"date","argv":[]}}}
EOF

# ============================================================================
# PYTHON EXECUTION TESTS
# ============================================================================

log_section "Python Execution"

# Test 10: exec.shell - Python version
log_test "exec.shell - Check Python version"
cat <<'EOF'
{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"exec.shell","arguments":{"cmd":"python","argv":["--version"]}}}
EOF

# Test 11: exec.shell - Python with pandas
log_test "exec.shell - Test pandas import"
cat <<'EOF'
{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"exec.shell","arguments":{"cmd":"python","argv":["-c","import pandas as pd; print(f'pandas {pd.__version__}')"],"timeout_seconds":10}}}
EOF

# Test 12: exec.shell - Python with numpy
log_test "exec.shell - Test numpy import"
cat <<'EOF'
{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"exec.shell","arguments":{"cmd":"python","argv":["-c","import numpy as np; arr = np.array([1,2,3]); print(f'numpy {np.__version__}: {arr}')"],"timeout_seconds":10}}}
EOF

# Test 13: exec.shell - Python with scipy
log_test "exec.shell - Test scipy import"
cat <<'EOF'
{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"exec.shell","arguments":{"cmd":"python","argv":["-c","import scipy; print(f'scipy {scipy.__version__}')"],"timeout_seconds":10}}}
EOF

# Test 14: exec.shell - Python with sklearn
log_test "exec.shell - Test scikit-learn"
cat <<'EOF'
{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"exec.shell","arguments":{"cmd":"python","argv":["-c","from sklearn import __version__; print(f'scikit-learn {__version__}')"],"timeout_seconds":10}}}
EOF

# ============================================================================
# POLICY TESTS
# ============================================================================

log_section "Policy Management"

# Test 15: policy.show - Display current policy
log_test "policy.show - Display current policy"
cat <<'EOF'
{"jsonrpc":"2.0","id":15,"method":"tools/call","params":{"name":"policy.show"}}
EOF

# Test 16: policy.test - Test read permission
log_test "policy.test - Test read permission"
cat <<'EOF'
{"jsonrpc":"2.0","id":16,"method":"tools/call","params":{"name":"policy.test","arguments":{"action":"read","path":"/home/anvilmcp/workspace/test-file.txt"}}}
EOF

# Test 17: policy.test - Test write permission
log_test "policy.test - Test write permission"
cat <<'EOF'
{"jsonrpc":"2.0","id":17,"method":"tools/call","params":{"name":"policy.test","arguments":{"action":"write","path":"/home/anvilmcp/workspace/another-test.txt"}}}
EOF

# Test 18: policy.test - Test exec permission
log_test "policy.test - Test exec permission"
cat <<'EOF'
{"jsonrpc":"2.0","id":18,"method":"tools/call","params":{"name":"policy.test","arguments":{"action":"exec","cmd":"ls"}}}
EOF

# ============================================================================
# CLEANUP TESTS
# ============================================================================

log_section "Cleanup"

# Test 19: fs.delete - Delete test files
log_test "fs.delete - Delete test files"
cat <<'EOF'
{"jsonrpc":"2.0","id":19,"method":"tools/call","params":{"name":"fs.delete","arguments":{"path":"/home/anvilmcp/workspace/test-file.txt"}}}
EOF

# Test 20: fs.delete - Delete JSON test file
log_test "fs.delete - Delete JSON test file"
cat <<'EOF'
{"jsonrpc":"2.0","id":20,"method":"tools/call","params":{"name":"fs.delete","arguments":{"path":"/home/anvilmcp/workspace/test-data.json"}}}
EOF

# ============================================================================
# SUMMARY
# ============================================================================

log_section "Test Summary"
TOTAL_TESTS=$((PASSED_TESTS + FAILED_TESTS))
echo -e "Total Tests: $TOTAL_TESTS"
echo -e "Passed: ${GREEN}$PASSED_TESTS${NC}"
echo -e "Failed: ${RED}$FAILED_TESTS${NC}"

if [ $FAILED_TESTS -eq 0 ]; then
    echo -e "\n${GREEN}All tests passed!${NC}\n"
    exit 0
else
    echo -e "\n${RED}Some tests failed!${NC}\n"
    exit 1
fi
