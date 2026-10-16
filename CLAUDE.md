# AnvilMCP - Claude Code Instructions

This file contains project-specific instructions and context for Claude Code sessions.

## Git Workflow

### ⚠️ CRITICAL: Never Push Directly to Main

**Rule:** NEVER push commits directly to the `main` branch.

**Always:**
1. Create a feature branch for your work: `git checkout -b feature/description`
2. Make commits on the feature branch
3. Push the feature branch to GitHub
4. Create a Pull Request (PR) for review
5. Only merge to main after PR approval

**Example:**
```bash
# ✅ CORRECT
git checkout -b feature/add-audit-logging
# ... make changes ...
git commit -m "Add audit logging"
git push origin feature/add-audit-logging
gh pr create --title "Add audit logging" --body "..."

# ❌ WRONG - Don't do this
git push origin main
```

### Branch Naming Convention

- `feature/` - New features (e.g., `feature/audit-logging`)
- `fix/` - Bug fixes (e.g., `fix/symlink-escape`)
- `security/` - Security improvements (e.g., `security/rate-limiting`)
- `docs/` - Documentation (e.g., `docs/docker-guide`)
- `test/` - Test improvements (e.g., `test/integration-suite`)

### PR Requirements

All PRs must:
- Be based on `main`
- Have descriptive title and body
- Include test coverage
- Pass all CI checks
- Reference related issues if applicable

## Project Context

### Technology Stack
- **Language:** Go 1.25.5
- **Container:** Docker (Ubuntu 24.04 LTS)
- **Python:** 3.12 via micromamba
- **Testing:** `go test` with race detector

### Key Files
- `Dockerfile` - Integration-test container definition (not distributed)
- `Makefile` - Build, test, and Docker targets
- `internal/policy/engine.go` - Core security policy engine
- `internal/tools/` - Filesystem and execution tools
- `tests/e2e/` - End-to-end suite (drives the real binary over MCP; runs in CI)
- `docs/INTEGRATION_TESTING.md` - Build, run, and exercise the server

### Security Model

AnvilMCP uses a **three-chokepoint security model**:

1. **Path Resolution** - Validates file paths against allow/deny lists
2. **Execution Evaluation** - Controls command execution with policy
3. **Environment Filtering** - Sanitizes environment variables

All paths and commands MUST pass policy validation before execution.

## Development Workflow

### Building
```bash
make build           # Build binary
make test            # Run all tests
make docker-build    # Build Docker image
```

### Running
```bash
make run             # Run development server
make docker-run      # Run in Docker container
```

### Testing
```bash
make test                           # Run all tests with race detector
make coverage                       # Generate coverage report
go test ./tests/e2e/               # Run end-to-end tests (add -short to skip)
```

## Current Implementation Status

### Phase 1: Complete ✅
- [x] Filesystem tools (fs.list, fs.read, fs.write, fs.stat, fs.delete)
- [x] Execution tools (exec.shell, exec.script)
- [x] Policy engine with three-chokepoint model
- [x] Security hardening (7 CRITICAL + HIGH vulnerabilities fixed)
- [x] Docker integration with data science stack
- [x] 22/22 tests passing

### Phase 2: Planned
- [ ] Python kernel execution (Jupyter kernel support)
- [ ] Enhanced authentication and authorization
- [ ] Audit logging integration
- [ ] Metrics and monitoring
- [ ] Advanced glob pattern matching with ripgrep
- [ ] Streaming responses for large outputs

## Code Quality Standards

- ✅ All tests must pass with race detector: `go test -race ./...`
- ✅ No comments unless WHY is non-obvious
- ✅ Prefer small, focused functions
- ✅ Security-first design
- ✅ Thread-safe implementations
- ✅ Cross-platform compatibility (Linux/macOS)

## Security Review Checklist

Before creating a PR with security changes:

- [ ] All affected code paths validated
- [ ] New tests added for security fix
- [ ] No regression in existing tests
- [ ] Documentation updated
- [ ] Policy enforcement verified
- [ ] Rate limits respected
- [ ] Symlink handling correct

## Remote Setup

The repository is hosted at: `https://github.com/artbegolli/anvilmcp`

SSH is preferred:
```bash
git remote set-url origin git@github.com:artbegolli/anvilmcp.git
```

## Questions?

Refer to:
- `README.md` - Project overview
- `docs/INTEGRATION_TESTING.md` - Build, run, and exercise the server
- `docs/POLICY_CONFIGURATION.md` - Policy configuration
- Phase status and roadmap are tracked at `~/Documents/workspace/SPECS/SESSION_MCP/IMPLEMENTATION_STATUS.md` (outside this repo)

---

**Last Updated:** May 16, 2026
**Maintained By:** Arben (artbegolli@gmail.com)
