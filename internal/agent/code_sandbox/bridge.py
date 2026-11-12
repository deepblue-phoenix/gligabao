# The bridge between gligabao (Go) and Pydantic Monty: docs/agents/monty-code-execution.md, "The bridge protocol".
# One JSON object per line on stdin (from Go) and stdout (to Go). Every run is one Monty session in a thread of its own; the
# host functions are Python callables that ask Go (a "call" message) and wait for the matching "result".
import json
import os
import queue
import sys
import threading
import time

PROTOCOL = 1

out_lock = threading.Lock()
out = sys.stdout
sys.stdout = sys.stderr  # nothing but the protocol may be written to the real stdout


def send(msg):
    line = json.dumps(msg, default=repr, ensure_ascii=False)
    with out_lock:
        out.write(line + "\n")
        out.flush()


try:
    import pydantic_monty as pm
except Exception as exc:  # the Go side shows this message
    send({"type": "ready", "protocol": PROTOCOL, "error": "cannot import pydantic_monty: %s" % exc})
    sys.exit(0)

runs = {}  # run id -> Run
runs_lock = threading.Lock()


class Run:
    def __init__(self, rid):
        self.id = rid
        self.inbox = queue.Queue()
        self.cancelled = threading.Event()
        self.calls = 0
        self.host_error = ""


def strict(value):
    def bad(o):
        raise TypeError("cannot pass a value of type %s to a host function" % type(o).__name__)

    return json.loads(json.dumps(value, default=bad))


def make_host(run, name, send_call):
    def host(*args, **kwargs):
        if run.cancelled.is_set():
            raise RuntimeError("the run was cancelled")
        run.calls += 1
        call_id = run.calls
        try:
            a, k = strict(list(args)), strict(kwargs)
        except TypeError as exc:
            run.host_error = str(exc)
            raise
        send({"type": "call", "id": run.id, "call_id": call_id, "name": name, "args": a, "kwargs": k})
        while True:
            m = run.inbox.get()
            if m.get("type") == "cancel":
                raise RuntimeError("the run was cancelled")
            if m.get("call_id") == call_id:
                break
        if m.get("ok"):
            return m.get("value")
        run.host_error = str(m.get("error", "host function failed"))
        raise RuntimeError(run.host_error)

    return host


def frame_line(exc):
    try:
        frames = exc.traceback()
        return frames[-1].line if frames else 0
    except Exception:
        return 0


def classify(run, exc):
    if isinstance(exc, pm.MontySyntaxError):
        return "syntax", exc.display("msg") if hasattr(exc, "display") else str(exc), frame_line(exc)
    if isinstance(exc, pm.MontyTypingError):
        return "type", exc.display(), 0
    if isinstance(exc, pm.MontyCrashedError):
        return ("timeout" if exc.timed_out else "crashed"), str(exc), 0
    if isinstance(exc, pm.MontyRuntimeError):
        text = exc.display("type-msg") if hasattr(exc, "display") else str(exc)
        inner = ""
        try:
            inner = type(exc.exception()).__name__
        except Exception:
            pass
        line = frame_line(exc)
        if inner == "TimeoutError" or text.startswith("TimeoutError"):
            return "timeout", text, line
        if inner == "MemoryError" or text.startswith("MemoryError"):
            return "memory", text, line
        if run.host_error and run.host_error in text:
            return "host_function", text, line
        return "runtime", text, line
    return "runtime", "%s: %s" % (type(exc).__name__, exc), 0


def execute(pool, hello, msg):
    run = runs[msg["id"]]
    started = time.time()
    try:
        lim = msg.get("limits") or {}
        limits = {}
        if lim.get("max_duration_s"):
            limits["max_duration_secs"] = float(lim["max_duration_s"])
        if lim.get("max_memory_mb"):
            limits["max_memory"] = int(lim["max_memory_mb"]) * 1024 * 1024
        if lim.get("max_recursion"):
            limits["max_recursion_depth"] = int(lim["max_recursion"])
        kw = {"limits": limits}
        if msg.get("type_check"):
            kw.update(type_check=True, type_check_stubs=msg.get("stubs") or None)
        lookup = {n: make_host(run, n, send) for n in (msg.get("functions") or [])}
        col = pm.CollectString(max_bytes=1024 * 1024)
        with pool.checkout(**kw) as session:
            value = session.feed_run(msg["code"], inputs=msg.get("inputs") or None, external_lookup=lookup, print_callback=col)
        send({"type": "done", "id": run.id, "value": value, "stdout": col.output, "stderr": "",
              "stats": {"wall_ms": int((time.time() - started) * 1000), "host_calls": run.calls}})
    except BaseException as exc:  # a run must always be answered
        kind, message, line = classify(run, exc)
        send({"type": "failed", "id": run.id, "kind": kind, "message": message, "line": line})
    finally:
        with runs_lock:
            runs.pop(run.id, None)


def main():
    kwargs = {"max_processes": int(os.environ.get("GLIGABAO_MONTY_WORKERS", "2"))}
    if os.environ.get("GLIGABAO_MONTY_REQUEST_TIMEOUT"):
        kwargs["request_timeout"] = float(os.environ["GLIGABAO_MONTY_REQUEST_TIMEOUT"])
    try:
        pool = pm.Monty(**kwargs)
        pool.__enter__()  # starts the workers: fails here if the monty binary is missing
    except Exception as exc:
        send({"type": "ready", "protocol": PROTOCOL, "error": "cannot start the monty workers: %s" % exc})
        return
    send({"type": "ready", "protocol": PROTOCOL, "monty_version": getattr(pm, "__version__", "")})
    hello = {}
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        msg = json.loads(line)
        t = msg.get("type")
        if t == "hello":
            hello = msg
        elif t == "run":
            with runs_lock:
                runs[msg["id"]] = Run(msg["id"])
            threading.Thread(target=execute, args=(pool, hello, msg), daemon=True).start()
        elif t in ("result", "cancel"):
            with runs_lock:
                run = runs.get(msg.get("id"))
            if run is not None:
                if t == "cancel":
                    run.cancelled.set()
                run.inbox.put(msg)
        elif t == "shutdown":
            break
    os._exit(0)


main()
