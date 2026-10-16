"""Long-running actions of a backend (e.g. deploying the Modal app) that an operator starts from the admin UI.

An `ActionRunner` runs one command as a subprocess, at most one at a time. Its merged stdout/stderr is kept line by line
(the last `max_lines`), so a client can follow it live (`follow`, Server-Sent Events) or read it afterwards (`snapshot`).
The state lives in memory: a sidecar restart forgets the last run (the outcome itself lives wherever the command put it,
e.g. Modal). The last run's log is kept until the next run starts.
"""
import asyncio
import json
import os
import re
import signal
import time
from dataclasses import dataclass
from typing import AsyncIterator, Mapping, Optional, Sequence

IDLE, RUNNING, SUCCEEDED, FAILED, CANCELLED = "idle", "running", "succeeded", "failed", "cancelled"

_ANSI = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07")
_MAX_LINE = 4000          # characters kept of a line
_CANCEL_GRACE_S = 10.0    # after SIGINT a cancelled command may clean up this long, then it is killed
_KEEPALIVE_S = 15.0       # an SSE comment this often while nothing happens


class ActionBusy(Exception):
    """The action is already running."""


class ActionNotRunning(Exception):
    """There is nothing to cancel."""


@dataclass(frozen=True)
class Stage:
    """A coarse step of an action, recognised by `pattern` in a log line. Stages only move forward."""
    name: str
    pattern: Optional[str] = None     # regex searched in each line; None = entered by start / success (no pattern)


class ActionRunner:
    def __init__(self, name: str, label: str, argv: Sequence[str], cwd: str, env: Optional[Mapping[str, str]] = None,
                 stages: Sequence[Stage] = (), max_lines: int = 5000, confirm: str = "", danger: bool = False) -> None:
        self.name = name
        self.label = label
        self.confirm = confirm          # what the UI asks before starting it ("" = its default question)
        self.danger = danger            # the UI shows its button as a destructive one
        self.exclusive_with: list["ActionRunner"] = []   # actions that must not run at the same time as this one
        self._argv = list(argv)
        self._cwd = cwd
        self._env = dict(env) if env is not None else None
        self._stages = tuple(stages)
        self._patterns = [re.compile(s.pattern, re.I) if s.pattern else None for s in self._stages]
        self._max_lines = max_lines

        self._state = IDLE
        self._run_id = 0
        self._stage = -1                      # index into _stages, -1: none yet
        self._started: Optional[float] = None
        self._finished: Optional[float] = None
        self._exit_code: Optional[int] = None
        self._error = ""
        self._lines: list[tuple[int, float, str]] = []   # (absolute index, time, text)
        self._next_index = 0
        self._version = 0                     # bumped on every change, so followers know to wake up
        self._cond = asyncio.Condition()
        self._proc: Optional[asyncio.subprocess.Process] = None
        self._task: Optional[asyncio.Task] = None
        self._cancel_requested = False

    # ---- state ----
    @property
    def running(self) -> bool:
        return self._state == RUNNING

    def state(self) -> dict:
        """The run's state, without the log."""
        return {
            "action": self.name, "label": self.label, "confirm": self.confirm, "danger": self.danger, "run_id": self._run_id, "state": self._state,
            "stage": self._stages[self._stage].name if self._stage >= 0 else None,
            "stages": [s.name for s in self._stages],
            "started_at": self._started, "finished_at": self._finished, "exit_code": self._exit_code,
            "error": self._error, "lines": self._next_index,
        }

    def snapshot(self, from_: int = 0) -> dict:
        """State plus the kept log lines with an index >= from_."""
        return {**self.state(), "log": [{"i": i, "t": t, "line": s} for i, t, s in self._lines if i >= from_]}

    # ---- running ----
    async def start(self) -> dict:
        """Start the command. Raises ActionBusy if the previous run is still going."""
        if self.running:
            raise ActionBusy(f"{self.name} is already running")
        for other in self.exclusive_with:
            if other.running:
                raise ActionBusy(f"{other.name} is running; wait for it or cancel it")
        self._run_id += 1
        self._lines, self._next_index = [], 0
        self._stage, self._exit_code, self._error, self._finished = 0 if self._stages else -1, None, "", None
        self._cancel_requested = False
        self._started, self._state = time.time(), RUNNING
        await self._changed()
        self._task = asyncio.create_task(self._run(self._run_id))
        return self.state()

    async def _run(self, run_id: int) -> None:
        try:
            env = {**os.environ, **(self._env or {})}
            self._proc = await asyncio.create_subprocess_exec(
                *self._argv, cwd=self._cwd, env=env, stdin=asyncio.subprocess.DEVNULL,
                stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.STDOUT)
            if self._cancel_requested:      # cancelled before the process existed
                self._proc.send_signal(signal.SIGINT)
            await self._pump(self._proc.stdout)
            code = await self._proc.wait()
            self._exit_code = code
            ok = code == 0 and not self._cancel_requested
            if self._cancel_requested:
                self._error = "cancelled"
            elif not ok:
                self._error = f"exited with code {code}"
        except asyncio.CancelledError:
            if self._proc and self._proc.returncode is None:
                self._proc.kill()
                await self._proc.wait()
            self._error, ok = "interrupted (sidecar shutting down)", False
        except Exception as e:
            self._error, ok = f"could not run {self._argv[0]!r}: {e}", False
            await self._add_lines([self._error])
        finally:
            self._proc = None
        if ok and self._stages:
            self._stage = len(self._stages) - 1     # the last stage is the success one
        self._finished = time.time()
        self._state = SUCCEEDED if ok else CANCELLED if self._cancel_requested else FAILED
        await self._changed()

    async def cancel(self) -> dict:
        """Ask the running command to stop: SIGINT (so it can clean up, e.g. a Python script sees KeyboardInterrupt), and
        kill it if it is still there after a grace period. The run ends as `cancelled`. Raises ActionNotRunning."""
        if not self.running:
            raise ActionNotRunning(f"{self.name} is not running")
        if not self._cancel_requested:
            self._cancel_requested = True
            await self._add_lines(["-- cancelling (sent SIGINT) --"])
            if self._proc and self._proc.returncode is None:
                self._proc.send_signal(signal.SIGINT)
                asyncio.create_task(self._kill_later(self._proc))
        return self.state()

    @staticmethod
    async def _kill_later(proc: asyncio.subprocess.Process) -> None:
        try:
            await asyncio.wait_for(proc.wait(), _CANCEL_GRACE_S)
        except asyncio.TimeoutError:
            if proc.returncode is None:
                proc.kill()

    async def _pump(self, stream: asyncio.StreamReader) -> None:
        """Read the output and turn it into lines. Both \\n and \\r end a line (progress bars rewrite with \\r)."""
        pending = ""
        while chunk := await stream.read(4096):
            parts = re.split(r"\r\n|\r|\n", pending + chunk.decode("utf-8", "replace"))
            pending = parts.pop()
            if parts:
                await self._add_lines(parts)
        if pending:
            await self._add_lines([pending])

    async def _add_lines(self, raw: Sequence[str]) -> None:
        now = time.time()
        for text in raw:
            text = _ANSI.sub("", text).rstrip()
            if not text:
                continue
            self._lines.append((self._next_index, now, text[:_MAX_LINE]))
            self._next_index += 1
            for idx, pat in enumerate(self._patterns):     # stages move forward only; the last one is for success
                if pat and idx > self._stage and idx < len(self._stages) - 1 and pat.search(text):
                    self._stage = idx
        if len(self._lines) > self._max_lines:
            del self._lines[:len(self._lines) - self._max_lines]
        await self._changed()

    async def _changed(self) -> None:
        async with self._cond:
            self._version += 1
            self._cond.notify_all()

    # ---- following ----
    async def follow(self, from_: int = 0) -> AsyncIterator[bytes]:
        """Server-Sent Events of the current (or last) run: `state` events (the state, no log) and `log` events (`lines`:
        [{i, t, line}, ...], from index `from_`), then it ends once the run is no longer running and everything is sent."""
        seen_version, next_i = -1, from_
        while True:
            idle = False
            async with self._cond:
                if self._version == seen_version:
                    try:
                        await asyncio.wait_for(self._cond.wait(), _KEEPALIVE_S)
                    except asyncio.TimeoutError:
                        idle = True
                seen_version = self._version
                state = self.state()
                new = [{"i": i, "t": t, "line": s} for i, t, s in self._lines if i >= next_i]
            if idle:
                yield b": keepalive\n\n"     # yielded outside the lock: a slow client must not block the runner
                continue
            if new:
                next_i = new[-1]["i"] + 1
                yield _sse("log", {"lines": new}, new[-1]["i"])
            yield _sse("state", state)
            if state["state"] != RUNNING:
                return

    async def aclose(self) -> None:
        """Stop a running command (sidecar shutdown)."""
        if self._task and not self._task.done():
            self._task.cancel()
            await asyncio.gather(self._task, return_exceptions=True)
        if self.running:     # cancelled before the run began: it never got to record its end
            self._error, self._finished, self._state = "interrupted (sidecar shutting down)", time.time(), FAILED
            await self._changed()


def _sse(event: str, data: dict, id_: Optional[int] = None) -> bytes:
    head = f"id: {id_}\n" if id_ is not None else ""
    return f"{head}event: {event}\ndata: {json.dumps(data)}\n\n".encode()
