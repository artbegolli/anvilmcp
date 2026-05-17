# AnvilMCP Policy Configuration Guide

AnvilMCP uses YAML-based policy files to control what operations are allowed on the container. This document explains how to configure policies.

## Quick Start

1. **Copy the example policy** to your config directory:
   ```bash
   mkdir -p ~/.config/anvilmcp
   cp config.yaml.example ~/.config/anvilmcp/config.yaml
   ```

2. **Edit the policy** to your needs (see sections below)

3. **Test your policy** using the `policy.test` MCP tool

## Policy Hierarchy

AnvilMCP enforces a two-layer policy system:

1. **Cluster Policy** (`/etc/anvilmcp/config.yaml`): The security ceiling set by your platform administrator. Users cannot broaden this.
2. **User Policy** (`~/.config/anvilmcp/config.yaml`): Your personal security settings. Can only narrow (restrict) the cluster policy.

**Effective Policy** = Intersection of cluster and user policies (most restrictive wins)

## Configuration Sections

### 1. Session Configuration

```yaml
session:
  cwd: /workspace  # Default working directory for all operations
```

### 2. Filesystem Access Control

Controls which paths can be read or written:

```yaml
paths:
  workspace_root: /workspace

  allow_read:
    - /workspace/**           # Recursive glob pattern
    - /usr/local/lib/**       # Multiple patterns supported
    - /home/user/.config/**

  allow_write:
    - /workspace/**
    - /tmp/**

  deny:                       # Always enforced, overrides allow lists
    - /workspace/.git/**
    - /home/**/.ssh/**
    - /etc/**
    - **/.env                 # Hidden from all paths
```

**Pattern Syntax:**
- `**` matches any number of path segments (recursive)
- `*` matches any characters in a single path segment
- Exact paths are matched literally

### 3. Command Execution Control

Controls which commands can be executed:

```yaml
exec:
  default: deny              # Default policy: "allow" or "deny"

  allow:
    python3:
      args_deny:            # Arguments that block execution
        - "import os"
    git:
      args_deny:
        - push              # Block git push
        - reset             # Block git reset
    pip:
      args_deny:
        - uninstall

  deny:                      # Always blocked commands
    - rm
    - dd
    - ssh
    - curl

  # Execution limits
  shell_interpolation: false # Disable shell variable expansion
  max_runtime_seconds: 300   # 5 minute timeout
  max_output_bytes: 5000000  # 5 MB output limit
  max_concurrent: 4          # Max parallel executions
```

**Best Practices:**
- Use `default: deny` and whitelist only what you need
- Use `args_deny` to block dangerous arguments
- Set reasonable timeouts and output limits
- Disable `shell_interpolation` for security

### 4. Environment Variable Control

Controls which environment variables are exposed:

```yaml
env:
  redact_patterns:          # Variables matching these patterns are hidden
    - '(?i)secret'
    - '(?i)token'
    - '(?i)password'
    - '(?i)api[_-]?key'

  explicit_deny:            # Specifically blocked variables
    - KUBECONFIG
    - AWS_ACCESS_KEY_ID

  allow_only: []             # If non-empty, whitelist mode (most restrictive)
```

### 5. Audit Configuration

Controls history and logging:

```yaml
audit:
  history_db_path: ~/.local/state/anvilmcp/history.db
  retention_days: 30         # Keep history for 30 days
  max_total_bytes: 1073741824 # 1 GB total storage

  no_store_output:           # Don't store output of these tools
    - fs.read
    - fs.list

  no_store_tools:            # Don't record these tools at all
    - policy.show
```

## Testing Your Policy

Use the `policy.test` tool to verify your policy rules:

```bash
# Test read access
policy.test -action read -path /workspace/file.txt

# Test write access
policy.test -action write -path /workspace/output.txt

# Test command execution
policy.test -action exec -cmd python3

# View entire effective policy
policy.show
```

## Common Configurations

### Restrictive (Minimal Permissions)

```yaml
exec:
  default: deny
  allow:
    python3: {}
    pytest: {}

paths:
  allow_read:
    - /workspace/**
  allow_write:
    - /workspace/output/**
```

### Permissive (Development)

```yaml
exec:
  default: allow
  deny:
    - rm
    - sudo
    - chmod

paths:
  allow_read:
    - /**
  allow_write:
    - /workspace/**
```

### Balanced (Recommended)

```yaml
exec:
  default: deny
  allow:
    python3: {}
    pytest: {}
    git:
      args_deny: [push, reset]
    pip:
      args_deny: [uninstall]

paths:
  allow_read:
    - /workspace/**
    - /usr/local/lib/**
  allow_write:
    - /workspace/**

env:
  redact_patterns:
    - '(?i)secret'
    - '(?i)token'
```

## File Locations

- **User Policy**: `~/.config/anvilmcp/config.yaml`
- **Cluster Policy**: `/etc/anvilmcp/config.yaml`
- **History Database**: `~/.local/state/anvilmcp/history.db`

Override these with CLI flags:
```bash
anvilmcp serve \
  --policy ~/.config/custom-policy.yaml \
  --cluster-policy /etc/custom-cluster-policy.yaml \
  --history-db ~/.local/custom-history.db
```

## Troubleshooting

**"access denied: not in allow list"**
- Add the path to your `allow_read` or `allow_write` list
- Check policy.test to debug

**"execution denied: matched deny pattern"**
- The command is explicitly blocked in `deny` list
- Or `default: deny` and command not in `allow` list

**"matched redact pattern"**
- Environment variable matches redaction pattern
- Either remove from `redact_patterns` or add to `allow_only` whitelist

## Security Recommendations

1. **Use `default: deny`** for command execution
2. **Whitelist specific paths** rather than broad globs
3. **Redact sensitive variables** (secrets, tokens, credentials)
4. **Set output limits** to prevent resource exhaustion
5. **Use `args_deny`** to block dangerous arguments
6. **Enable audit logging** for compliance
7. **Review cluster policy** to understand the ceiling
