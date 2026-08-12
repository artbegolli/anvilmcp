#!/usr/bin/env python3
"""AnvilMCP kernel harness.

A long-lived, stdlib-only Python process that holds one persistent namespace and
executes code submitted by the Go server over an inherited AF_UNIX socket
(socketpair end passed as fd 3). Messages are length-prefixed JSON:

    go   -> harness : {"type":"execute","id":..,"code":..}  |  {"type":"shutdown"}
    harness -> go   : {"type":"ready", ...}                    (once, on startup)
                      {"type":"stream","id":..,"name":"stdout|stderr","text":..}
                      {"type":"result","id":..,"status":"ok|error|interrupted",..}

Interrupt is delivered as SIGINT (not a message): while executing, the main
thread is blocked in exec() and can't read the socket, so a signal is the only
way in. The Go side escalates to SIGKILL if the interrupt is ignored.
"""
import ast
import io
import json
import signal
import socket
import struct
import sys
import traceback

DEFAULT_FD = 3
REPR_LIMIT = 8192


def send(conn, obj):
    data = json.dumps(obj).encode("utf-8")
    conn.sendall(struct.pack(">I", len(data)) + data)


def _recvn(conn, n):
    buf = bytearray()
    while len(buf) < n:
        chunk = conn.recv(n - len(buf))
        if not chunk:
            return None
        buf.extend(chunk)
    return bytes(buf)


def recv(conn):
    header = _recvn(conn, 4)
    if header is None:
        return None
    (n,) = struct.unpack(">I", header)
    body = _recvn(conn, n)
    if body is None:
        return None
    return json.loads(body.decode("utf-8"))


class StreamWriter(io.TextIOBase):
    """Stand-in for sys.stdout/stderr that frames each write to the socket."""

    def __init__(self, conn, exec_id, name):
        self._conn = conn
        self._id = exec_id
        self._name = name

    def write(self, s):
        if s:
            send(self._conn, {"type": "stream", "id": self._id, "name": self._name, "text": s})
        return len(s)

    def writable(self):
        return True


def _split_last_expr(src):
    """Compile src, returning (exec_block_or_None, eval_expr_or_None).

    Mirrors notebook behaviour: if the final top-level statement is a bare
    expression, it is compiled for eval so its repr can be shown; everything
    before it is compiled for exec.
    """
    tree = ast.parse(src, "<kernel>", "exec")
    if not tree.body:
        return None, None
    last = tree.body[-1]
    if isinstance(last, ast.Expr):
        head = tree.body[:-1]
        block = compile(ast.Module(body=head, type_ignores=[]), "<kernel>", "exec") if head else None
        expr = compile(ast.Expression(body=last.value), "<kernel>", "eval")
        return block, expr
    return compile(tree, "<kernel>", "exec"), None


def _safe_repr(value):
    try:
        r = repr(value)
    except Exception as exc:  # noqa: BLE001 - repr must never take down the kernel
        return "<unreprable {}: {}>".format(type(value).__name__, exc)
    if len(r) > REPR_LIMIT:
        return r[:REPR_LIMIT] + "... (+{} chars)".format(len(r) - REPR_LIMIT)
    return r


def handle_execute(conn, ns, state, msg):
    exec_id = msg.get("id")
    code = msg.get("code", "")
    state["count"] += 1
    count = state["count"]

    old_out, old_err = sys.stdout, sys.stderr
    sys.stdout = StreamWriter(conn, exec_id, "stdout")
    sys.stderr = StreamWriter(conn, exec_id, "stderr")

    result = {"type": "result", "id": exec_id, "execution_count": count, "result_repr": None}
    try:
        block, expr = _split_last_expr(code)
        if block is not None:
            exec(block, ns)  # noqa: S102 - executing user code is the whole point
        if expr is not None:
            value = eval(expr, ns)  # noqa: S307
            if value is not None:
                result["result_repr"] = _safe_repr(value)
        result["status"] = "ok"
    except KeyboardInterrupt:
        result["status"] = "interrupted"
    except BaseException as exc:  # noqa: BLE001 - report, never crash the harness
        result["status"] = "error"
        result["ename"] = type(exc).__name__
        result["evalue"] = str(exc)
        result["traceback"] = "".join(traceback.format_exception(type(exc), exc, exc.__traceback__))
    finally:
        sys.stdout, sys.stderr = old_out, old_err

    send(conn, result)


def main():
    fd = DEFAULT_FD
    args = sys.argv[1:]
    if "--fd" in args:
        fd = int(args[args.index("--fd") + 1])

    conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM, fileno=fd)

    # Default handler raises KeyboardInterrupt in the running cell on SIGINT.
    signal.signal(signal.SIGINT, signal.default_int_handler)

    ns = {"__name__": "__anvil_kernel__", "__builtins__": __builtins__}
    state = {"count": 0}

    send(conn, {"type": "ready", "python": sys.version.split()[0], "executable": sys.executable})

    while True:
        try:
            msg = recv(conn)
        except KeyboardInterrupt:
            # Stray SIGINT between executions (interrupt raced the result). Ignore.
            continue
        except (OSError, ValueError):
            break
        if msg is None:
            break
        mtype = msg.get("type")
        if mtype == "execute":
            handle_execute(conn, ns, state, msg)
        elif mtype == "shutdown":
            break

    try:
        conn.close()
    except OSError:
        pass


if __name__ == "__main__":
    main()
