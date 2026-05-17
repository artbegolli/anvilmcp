# AnvilMCP

A per-session [Model Context Protocol](https://modelcontextprotocol.io/) server
that exposes filesystem, exec, and policy tools to a chat client — gated by a
two-layer YAML policy so a platform team can hand engineers a dev container
without handing them root.

AnvilMCP runs inside the dev container, not on the host. One process per
session, no shared state between users.

## What it gives the model

**Tools** (model-mediated, called explicitly):

| Tool          | Purpose                                                  |
|---------------|----------------------------------------------------------|
| `fs.list`     | List a directory, optionally recursive / glob-filtered   |
| `fs.read`     | Read a file with optional offset / length                |
| `fs.write`    | Create or overwrite a file                               |
| `fs.stat`     | Type, size, mtime, permissions                           |
| `fs.delete`   | Remove a file or directory                               |
| `exec.shell`  | Run a command with argv (no shell interpolation)         |
| `exec.script` | Run a `sh` / `bash` / `python3` script                   |
| `policy.show` | Return the effective policy                              |
| `policy.test` | Ask whether a read/write/exec would be allowed           |

**Resources** (host-mediated, for browsing/pinning without going through
the model):

- `policy://effective` — the effective policy as JSON
- `file:///{+path}` — full file contents
- `stat:///{+path}` — file metadata

## Security model

Every tool call passes through one of three chokepoints in
[`internal/policy/engine.go`](internal/policy/engine.go):

1. **Path resolution** — input path is resolved (symlinks included), then
   matched against the policy's `allow_read` / `allow_write` globs, with
   `deny` entries taking precedence.
2. **Exec evaluation** — command and argv are matched against `allow` /
   `deny` / `ask` lists, including per-command `args_deny`.
3. **Env filtering** — environment variables are redacted by regex
   (`secret`, `token`, `password`, `api_key` by default) plus an explicit
   denylist; child processes never see the unfiltered parent env.

The effective policy is `cluster ∩ user` — the cluster policy is the
ceiling set by the platform team, and the user policy can only narrow it.

## Quick start

```bash
mkdir -p ~/.config/anvilmcp ~/.local/state/anvilmcp
cp config.yaml.example ~/.config/anvilmcp/config.yaml
go run ./cmd/anvilmcp serve --port :8080
```

The server speaks the MCP Streamable HTTP transport on `:8080`.

Connect from any MCP client; for interactive exploration:

```bash
npx @modelcontextprotocol/inspector --url http://localhost:8080
```

## Configuration

Default paths (all overridable via CLI flags):

| Default                                  | Flag               |
|------------------------------------------|--------------------|
| `~/.config/anvilmcp/config.yaml`         | `--policy`         |
| `/etc/anvilmcp/config.yaml`              | `--cluster-policy` |
| `~/.local/state/anvilmcp/history.db`     | `--history-db`     |

See [`docs/POLICY_CONFIGURATION.md`](docs/POLICY_CONFIGURATION.md) for the
full policy schema and worked examples.

## Building & testing

```bash
make build           # compile binary
make test            # go test -race ./...
make docker-build    # integration-test container image
make docker-run      # run that image on :8080
```

End-to-end / container flows are covered in
[`docs/INTEGRATION_TESTING.md`](docs/INTEGRATION_TESTING.md).

The `Dockerfile` exists for integration testing only — AnvilMCP is **not**
distributed as a published image. Production deployments run the binary
directly inside the platform-provided dev container.

## Status

Phase 1 is shipped: filesystem + exec + policy tools, policy engine,
history store, MCP resources, full CI.

Phase 2 (not yet implemented): Jupyter kernel execution with import-level
policy enforcement, audit-log surfacing, richer rate-limit controls.

## License

Apache License 2.0. See [LICENSE](LICENSE).
