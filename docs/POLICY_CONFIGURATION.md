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

Every tool and resource invocation is recorded: the call, its arguments, the
policy decision and rule that produced it, and the outcome. Nothing is opt-in —
leaving this block out gives you the defaults below, not silence.

```yaml
audit:
  history_db_path: ~/.local/state/anvilmcp/history.db
  retention_days: 30          # Drop records older than this (0 = keep forever)
  max_total_bytes: 1073741824 # 1 GB; oldest rows dropped first once exceeded

  jsonl_mirror_path: ""       # Also append completed records as JSON Lines
  fail_closed: false          # Refuse a call if its record can't be written

  no_store_output:            # Record the call, but not its output preview
    - fs.read
    - fs.list

  no_store_tools:             # Don't record these at all
    - policy.show
```

`history_db_path` here overrides the `--history-db` flag: the policy is the
cluster-controlled layer, so where the trail lands is the operator's call.
A leading `~` is expanded.

#### Why this matters more than the other sections

The path and exec rules above gate tool *arguments*. They do not gate what an
allowed program then does. `exec.shell` with `cmd: python3` is permitted by the
default policy, and that interpreter can read any path the container user can —
`paths.deny` included. The persistent kernel is the same story by design: only
the interpreter launch is policy-gated.

So treat the first four sections as guardrails against accident, and this one
as the control that still holds against intent. A record whose `decision` is
`unmediated` is a call no chokepoint evaluated; those are the ones worth
alerting on.

#### What is stored

| Column | Notes |
|---|---|
| `args_json` | Arguments, with keys matching `env.redact_patterns` replaced by `[REDACTED]`. Values over 4 KB are truncated; a blob over 16 KB is replaced by a SHA-256 digest plus the key names. |
| `decision` | `allow`, `deny`, or `unmediated` (no chokepoint evaluated the call). |
| `policy_rule` | The exact rule that decided it, e.g. `paths.deny:/etc/**`. |
| `status` | `running`, `ok`, `error`, `denied`, `killed`. A denial is always stored as `denied`, never as a generic error. |
| `stdout_preview` / `stderr_preview` | First 16 KB, unless the tool is in `no_store_output`. Byte counts are recorded either way. |

Redaction is storage hygiene, not a guarantee: a secret passed as a positional
value under an innocuous key is recorded as given. The database and the JSONL
mirror are created `0600` in a `0700` directory on that basis.

#### Operational notes

- A record is written *before* the handler runs, so a call that panics, hangs,
  or is cancelled still leaves evidence it was attempted. Writes are detached
  from the request context — a client that disconnects cannot drop its own
  record.
- Watch `anvilmcp_audit_writes_total{result="failed"}`. Non-zero means calls
  are executing without a durable record, unless `fail_closed` is set.
- `max_total_bytes` will not prune below the 100 most recent records. A cap too
  small to satisfy is logged as a warning and left unsatisfied, rather than
  grinding the trail away hourly to meet it.

#### Reading the trail

```bash
anvilmcp history list                      # recent calls, newest first
anvilmcp history list --denied             # only what policy refused
anvilmcp history list --unmediated         # only what no chokepoint evaluated
anvilmcp history list --tool exec.shell --since 24h
anvilmcp history show <id>                 # one call in full
anvilmcp history tail                      # follow live
```

`--json` on any of them emits machine-readable output with the stored
arguments re-embedded as a nested object, so `jq` can reach into them:

```bash
anvilmcp history list --json | jq -r '.[] | select(.decision=="unmediated") | .args.code'
```

These read the same database `anvilmcp serve` writes, resolving the path the
same way (`audit.history_db_path` over `--history-db`, `~` expanded). They open
it **read-only** and fail if it does not exist, rather than creating an empty
one and reporting a quiet "no records" for what is actually a wrong path.

`tail` prints a line when a call starts and another when it finishes, so a call
that hangs shows up as a `running` line with no terminal partner. Calls already
in flight when you start tailing are included.

**There is deliberately no `history.*` MCP tool.** The model is the audited
party; giving it a read interface onto its own trail would let it enumerate
what the operator can see. Access is out-of-band: the CLI, the database, or the
JSONL mirror.

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
