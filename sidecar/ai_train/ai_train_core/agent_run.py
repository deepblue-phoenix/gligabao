"""Runs the research agent, the `gligabao_agent` Go binary, inside the remote container. Provider neutral: no modal/aws imports.

The Go server starts a research with runner "modal" through the sidecar (POST /research/job_start); the web API spawns a function that
calls `run_agent_job` here. The binary researches a topic (the agent and its sub-agents, in the container, in a database of the container)
and this module reports what it does to the job's event log (event_log.py: a key-value store of the provider), which the web API
streams to the Go server as SSE (/research/job_events). Nothing here knows about the research itself: it passes the binary's
`--events` lines on and reports how it ended.

Events of a research job (like the training job's, see progress_events.py; "completed", "failed" and "cancelled" end it):
    {"type": "status", "status": "running"}
    {"type": "state", "agent": "main" | <sub-agent name>, "state_seq": n, "state": ..., "step": n, "detail": ..., "error": ...}
        an agent entered a state; a second event with the same agent and state_seq carries the error the state ended with
    {"type": "usage", "usage": {"steps", "tool_calls", "input_tokens", "output_tokens", "cost_usd", "subagents"}}
        what the whole tree of agents spent so far
    {"type": "log", "message": str}                                   a line the binary wrote to stderr (warnings, errors)
    {"type": "status", "status": "completed", "report": str, "usage": {...}, "export": {...}}
    {"type": "status", "status": "failed" | "cancelled", "error": str, "export": {...}}

`export` ({"bytes", "sha256"}) is there when the binary wrote its export file (`--export`: the knowledge it gathered, its runs and
costs, see internal/db/dbexport) and it was stored in the job's event log (EventLog.put_export). The container and its storage are
gone when the job ends, so this is how the knowledge gets back to the server that started the job: the web API serves the file
(GET /research/job_export) and the server loads it into its databases. It is there for a failed or stopped job too (what it learned
until then is kept), and it is stored before the final event, so whoever sees the final event finds the file.

Stopping: the web API sets the stop flag of the log (progress_events.request_stop); a watcher thread here sees it, ends the binary
with SIGTERM (which ends the research properly) and reports "cancelled".
"""
import collections
import json
import os
import queue
import shutil
import signal
import subprocess
import tempfile
import threading
import traceback
from typing import Callable, Optional

from .event_log import EventLog
from .progress_events import stop_requested, write_event, write_log, write_status

AGENT_BINARY = "/opt/gligabao_agent"     # where the image has the binary (app.py)
EXPORT_FILE = "export.zip"               # the file the binary writes its state to (--export), in the job's directory
EVENT_PREFIX = '{"event":'               # a line of the binary's stderr that is an event (gligabao_agent --events)

STOP_POLL_S = 2.0                        # how often the stop flag is looked at
STOP_GRACE_S = 20.0                      # how long the binary has to end after SIGTERM before it is killed
TIMEOUT_MARGIN_S = 90                    # the binary's own timeout is this much shorter than the function's, so it ends by itself
MAX_LOG_LINES = 200                      # at most this many stderr lines become log events (the rest is only counted)
MAX_LOG_CHARS = 1000
QUEUE_SIZE = 2000                        # events waiting for the log; a store that is down for long makes it drop the oldest news
ERROR_TAIL_LINES = 15                    # lines of stderr that explain a failed binary

_USAGE_KEYS = ("steps", "tool_calls", "input_tokens", "output_tokens", "cost_usd", "subagents")


BUNDLE_FILE = "agent_bundle.json"        # the agent definitions of the job (agent_defs.Bundle), in the job's directory


def build_command(binary: str, workdir: str, max_steps: int, model: str, dataset: str, timeout_s: int,
                  export_path: str = "", agent: str = "", bundle_path: str = "", max_cost_usd: float = 0.0) -> list:
    """The command line of the binary: the topic comes on stdin ("-"), the database is in `workdir`. With `export_path` the binary
    writes its state there when it ends (--export). `bundle_path` is the file with the agent definitions (--agent-bundle), `agent` the
    one to run (--agent) and `max_cost_usd` the cap on the cost of the whole research (--max-cost-usd)."""
    cmd = [binary, "--json", "--events", "--max-steps", str(max_steps), "--db", os.path.join(workdir, "gligabao.db")]
    if export_path:
        cmd += ["--export", export_path]
    if bundle_path:
        cmd += ["--agent-bundle", bundle_path]
    if agent:
        cmd += ["--agent", agent]
    if max_cost_usd and max_cost_usd > 0:
        cmd += ["--max-cost-usd", repr(float(max_cost_usd))]
    if model:
        cmd += ["--model", model]
    if dataset:
        cmd += ["--dataset", dataset]
    if timeout_s > 0:
        cmd += ["--timeout", f"{timeout_s}s"]
    return cmd + ["-"]


def convert_event(line: str) -> Optional[dict]:
    """An event of the job from a line of the binary's stderr, None if the line is not one of them."""
    if not line.startswith(EVENT_PREFIX):
        return None
    try:
        ev = json.loads(line)
    except ValueError:
        return None
    kind = ev.get("event")
    if kind == "state":
        return {"type": "state", "agent": ev.get("agent") or "main", "state_seq": ev.get("state_seq", 0),
                "state": ev.get("state", ""), "step": ev.get("step", 0), "detail": ev.get("detail", ""),
                "error": ev.get("error", "")}
    if kind == "usage":
        return {"type": "usage", "usage": {k: ev.get(k, 0) for k in _USAGE_KEYS}}
    return None


def _usage_of_result(result: dict) -> dict:
    """The usage block of the completed event, from the binary's --json output."""
    return {"steps": result.get("steps", 0), "tool_calls": result.get("tool_calls", 0),
            "input_tokens": result.get("input_tokens", 0), "output_tokens": result.get("output_tokens", 0),
            "cost_usd": result.get("cost_usd", 0), "subagents": result.get("subagents", 0)}


def _executable(binary: str, workdir: str) -> str:
    """The binary, made executable (files baked into an image may not keep their mode; if the image's own copy cannot be changed, a
    copy in `workdir` is)."""
    if os.access(binary, os.X_OK):
        return binary
    try:
        os.chmod(binary, 0o755)
        return binary
    except OSError:
        copy = os.path.join(workdir, "gligabao_agent")
        shutil.copyfile(binary, copy)
        os.chmod(copy, 0o755)
        return copy


def run_agent_job(topic: str, max_steps: int, model: str, dataset: str, job_id: str, *, event_kv, timeout_s: int,
                  binary: str = AGENT_BINARY, popen: Callable = subprocess.Popen, agent: str = "", agent_bundle: str = "",
                  max_cost_usd: float = 0.0) -> dict:
    """Research `topic` with the binary and report to the job's event log (`event_kv`: the provider's key-value store). The events
    end with "completed" (with the report), "cancelled" (a stop was requested) or "failed" (with why); the result says which.
    `timeout_s` is the function's timeout: the binary is told to stop a little before it. `agent_bundle` (JSON text) holds the definitions of
    the agent `agent` and of the sub-agents it may start: the container has no agent_defs directory."""
    log = EventLog(event_kv, job_id)
    write_status(log, "running")
    if stop_requested(log):    # stopped before the container started
        write_status(log, "cancelled", error="stopped before the agent started")
        return {"status": "cancelled"}
    if not os.path.isfile(binary):
        error = (f"the gligabao_agent binary is not in this container ({binary}): build it with ./run_build.sh (it makes "
                 "build/gligabao_agent_linux_amd64) and deploy the Modal app again")
        write_status(log, "failed", error=error)
        return {"status": "failed", "error": error}

    workdir = tempfile.mkdtemp(prefix="agent-")
    try:
        return _run(log, topic, max_steps, model, dataset, timeout_s, binary, workdir, popen, agent, agent_bundle, max_cost_usd)
    except BaseException as e:
        try:
            write_status(log, "failed", error=f"{type(e).__name__}: {e}"[:2000],
                         traceback="".join(traceback.format_exception(e))[-6000:])
        except Exception as we:
            print(f"[job] could not record the failure: {we!r}", flush=True)
        raise
    finally:
        shutil.rmtree(workdir, ignore_errors=True)


def _run(log: EventLog, topic: str, max_steps: int, model: str, dataset: str, timeout_s: int, binary: str, workdir: str,
         popen: Callable, agent: str = "", agent_bundle: str = "", max_cost_usd: float = 0.0) -> dict:
    agent_timeout = max(timeout_s - TIMEOUT_MARGIN_S, 60) if timeout_s else 0
    export_path = os.path.join(workdir, EXPORT_FILE)
    bundle_path = ""
    if agent_bundle:
        bundle_path = os.path.join(workdir, BUNDLE_FILE)
        with open(bundle_path, "w", encoding="utf-8") as f:
            f.write(agent_bundle)
    cmd = build_command(_executable(binary, workdir), workdir, max_steps, model, dataset, agent_timeout, export_path, agent, bundle_path,
                        max_cost_usd)
    write_log(log, f"starting the agent: {os.path.basename(binary)} --max-steps {max_steps}" + (f" --model {model}" if model else "")
              + (f" --agent {agent}" if agent else ""))
    proc = popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, cwd=workdir)

    # Events go to the log from a thread of their own, so that a slow store never makes the binary block on a full stderr pipe.
    pending: "queue.Queue" = queue.Queue(maxsize=QUEUE_SIZE)
    sender = threading.Thread(target=_send, args=(log, pending), daemon=True)
    sender.start()

    stdout_chunks: list = []
    reader_out = threading.Thread(target=lambda: stdout_chunks.append(proc.stdout.read()), daemon=True)
    reader_out.start()

    cancelled = threading.Event()
    finished = threading.Event()
    watcher = threading.Thread(target=_watch_stop, args=(log, proc, cancelled, finished), daemon=True)
    watcher.start()

    tail: collections.deque = collections.deque(maxlen=ERROR_TAIL_LINES)
    logged = 0
    try:
        proc.stdin.write(topic)
        proc.stdin.close()
    except OSError:
        pass   # the binary ended before reading it: its exit says why
    for raw in proc.stderr:
        line = raw.rstrip("\n")
        ev = convert_event(line)
        if ev is not None:
            _put(pending, ev)
            continue
        if line.strip():
            tail.append(line)
            if logged < MAX_LOG_LINES:
                logged += 1
                _put(pending, {"type": "log", "message": line[:MAX_LOG_CHARS]})
    code = proc.wait()
    finished.set()
    reader_out.join(timeout=10)
    pending.put(None)
    sender.join(timeout=60)   # the events come before the final status
    watcher.join(timeout=STOP_POLL_S + 5)
    stdout = stdout_chunks[0] if stdout_chunks else ""

    # The export goes first: whoever sees the final event finds the file. Also for a failed or a stopped job.
    export = _store_export(log, export_path)
    extra = {"export": export} if export else {}

    if cancelled.is_set():
        write_status(log, "cancelled", error="stopped", **extra)
        return {"status": "cancelled"}
    result = _result_of(stdout)
    if code != 0 and result is not None and not export and not os.path.exists(export_path):
        # The research ended with a report, but the binary exits with an error when it could not write the export (see
        # gligabao_agent): the report is the result, the missing export is only said.
        write_log(log, "the agent finished, but its export could not be written: " + (" | ".join(tail) or "(no output)")[:500])
        code = 0
    if code == 0:
        if result is None:
            error = "the agent ended without a readable result: " + (stdout.strip()[:500] or "(no output)")
            write_status(log, "failed", error=error, **extra)
            return {"status": "failed", "error": error}
        write_status(log, "completed", report=result["report"], usage=_usage_of_result(result), **extra)
        return {"status": "completed", "run_id": result.get("run_id"), "cost_usd": result.get("cost_usd")}
    error = f"the agent exited with code {code}: " + (" | ".join(tail) or "(no output)")
    write_status(log, "failed", error=error[:2000], **extra)
    return {"status": "failed", "error": error[:2000]}


def _result_of(stdout: str) -> Optional[dict]:
    """The binary's --json output when it is a result with a report, else None."""
    try:
        result = json.loads(stdout)
        result["report"]
    except (ValueError, KeyError, TypeError):
        return None
    return result if isinstance(result, dict) else None


def _store_export(log: EventLog, path: str) -> Optional[dict]:
    """Put the binary's export file (if it wrote one) in the job's event log; returns {"bytes", "sha256"}, None if there is no
    file or it could not be stored (said in a log event: the job's result does not depend on it)."""
    if not os.path.isfile(path):
        return None
    try:
        info = log.put_export(path)
    except Exception as e:
        write_log(log, f"the export of the agent could not be stored, so its knowledge is lost: {type(e).__name__}: {e}"[:MAX_LOG_CHARS])
        return None
    write_log(log, f"the export of the agent is stored ({info['bytes']} bytes)")
    return info


def _put(pending: "queue.Queue", ev: dict) -> None:
    """Queue an event; when the queue is full (the store is far behind) the oldest event is dropped for it."""
    while True:
        try:
            pending.put_nowait(ev)
            return
        except queue.Full:
            try:
                pending.get_nowait()
            except queue.Empty:
                pass


def _send(log: EventLog, pending: "queue.Queue") -> None:
    """Thread: write the queued events to the log (best effort: a store that fails skips the event, see write_event) until None."""
    while True:
        ev = pending.get()
        if ev is None:
            return
        write_event(log, ev, best_effort=True)


def _watch_stop(log: EventLog, proc, cancelled: threading.Event, finished: threading.Event) -> None:
    """Thread: when the stop flag of the job is set, end the binary (SIGTERM ends the research as a stopped one, SIGKILL after a grace)."""
    while not finished.wait(STOP_POLL_S):
        if not stop_requested(log):
            continue
        cancelled.set()
        print("[job] stop requested: ending the agent", flush=True)
        try:
            proc.send_signal(signal.SIGTERM)
        except OSError:
            return
        if not finished.wait(STOP_GRACE_S):
            try:
                proc.kill()
            except OSError:
                pass
        return
