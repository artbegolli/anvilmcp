# AnvilMCP Integration Testing

This guide is the single source of truth for exercising AnvilMCP end-to-end.
It replaces the previous `TESTING.md`, `DOCKER.md`, and `MCP-INSPECTOR-TESTING.md`.

The goal is a reproducible loop: build → run → exercise → tear down.

## 0. Prerequisites

- Go 1.25.5+
- Docker (for the containerised flow)
- `jq` (for inspecting JSON responses)
- Optional: Node 20+ if you want to use [MCP Inspector](https://github.com/modelcontextprotocol/inspector)

## 1. Unit tests (fastest signal)

```bash
make test
```

Runs `go test -race ./...`. Should complete in well under a minute.

## 2. Run the server locally (no container)

```bash
mkdir -p ~/.config/anvilmcp ~/.local/state/anvilmcp
cp config.yaml.example ~/.config/anvilmcp/config.yaml
go run ./cmd/anvilmcp serve --port :8080
```

The server listens on `http://localhost:8080` with Streamable HTTP transport.
Override paths with `--policy`, `--cluster-policy`, `--history-db`.

## 3. Run the server in Docker (closer to production)

The `Dockerfile` exists for integration testing only — AnvilMCP is not
published as a container image. The image bundles Python 3.12 + a small data
science stack to mirror the dev-container runtime.

```bash
make docker-build   # builds anvilmcp:latest
make docker-run     # starts on :8080 with workspace + config mounts
```

The `docker-run` target mounts `./workspace` and `./.config/anvilmcp` so you
can edit the policy on the host and restart the container to pick it up.

## 4. Exercise the server

### Smoke test with curl

The MCP Streamable HTTP transport requires an initialize handshake; for a
quick liveness check, hit the server root:

```bash
curl -i http://localhost:8080/
```

### Automated end-to-end suite

```bash
go test ./tests/e2e/           # or: go test -race ./...
go test -short ./tests/e2e/    # skip it
```

`tests/e2e/` is the real suite: it builds the server binary, starts it on an
ephemeral port with a scoped policy and temp workspace, and drives it over the
MCP Streamable HTTP endpoint as a client would — asserting on actual responses.
It runs as part of the normal `go test ./...` CI job, so it cannot silently rot.

It covers the initialize handshake and tool registry, the filesystem round trip
(write → read → stat → list → delete), path and exec policy denials, exec
timeouts (exit 124, including reaping forked workers), the policy tools,
MCP resources, the `/metrics` endpoint, streamed exec output via
`notifications/progress`, and — when enabled — the kernel surface including
state persistence across separate tool calls.

### Interactive exploration with MCP Inspector

```bash
npx @modelcontextprotocol/inspector --url http://localhost:8080
```

Inspector connects to the running server and lets you call tools, browse
resources, and inspect schemas from a browser. Use it when you want to
explore behaviour rather than verify a known-good flow.

## 5. Tear down

```bash
docker stop anvilmcp-server 2>/dev/null || true
rm -rf workspace/.anvilmcp-test-*
```

## What to verify in a passing run

- Server logs include `Server initialized` with the loaded workspace_root.
- `fs.read` of a workspace file returns its contents.
- `fs.read` of `/etc/passwd` returns `access denied` (policy deny).
- `exec.shell` with `cmd: rm` returns `execution denied` (exec.deny).
- `policy.test` with `action: read, path: /etc/passwd` returns `allowed: false`.

If any of the above fail, regenerate the config from
`config.yaml.example` — most failures here are stale policy.
