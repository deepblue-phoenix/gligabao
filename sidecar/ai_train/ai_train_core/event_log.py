"""The event log of a training job, on a key-value store the provider supplies (a Modal Dict). No heavy imports.

The training container appends events (progress_events.py: write_event / write_status / write_log); the web API reads
them from any index on and streams them to the Go server as SSE (api/api.py: /training/job_events). The log replaces
the progress file on a volume: there is nothing to commit or reload, and a reader that was cut off resumes with the
index of the first event it missed (its SSE id), as before.

The store only needs `get(key, default=None)` and `update(mapping)` (a modal.Dict has both; update is one request). Keys
of a job `<id>`:
    <id>:head     index of the last event (the log is empty without it)
    <id>:c<n>     chunk n: a list of `chunk_size` events, as JSON texts; event i is in chunk i // chunk_size. Reading
                  a stretch of events costs a request per chunk, not per event. The writer rewrites the chunk it is
                  filling, together with `head`, in one update, so a reader never sees an event without its head.
    <id>:meta     what the web API knows about the job (the provider's call id, see api/api.py); written by the web API
    <id>:stop     set by the web API to ask the job to stop (progress_events.py: request_stop)

One writer at a time: the training container, and, after it is gone, the web API's watchdog (api/api.py) that writes
the "failed" event of a job that died without reporting it. Entries expire in the store (7 days in a Modal Dict), which
is how old jobs are cleaned up: the Go server and its database are the permanent record.
"""
import json
import threading
import time
from typing import Any, Callable, Optional

CHUNK_SIZE = 50


class EventLog:
    def __init__(self, kv: Any, job_id: str, chunk_size: int = CHUNK_SIZE, retries: int = 4,
                 sleep: Optional[Callable[[float], None]] = None):
        self.kv, self.job_id, self.chunk_size = kv, job_id, chunk_size
        self.retries = retries
        self._sleep = sleep or (lambda seconds: time.sleep(seconds))
        self._lock = threading.Lock()
        self._head: Optional[int] = None      # index of the last event written (-1: none); None: not loaded yet
        self._chunk: list = []                # the chunk being filled

    def _key(self, name: str) -> str:
        return f"{self.job_id}:{name}"

    def _retry(self, fn: Callable, *args):
        """A request to the store, tried again (waiting 0.5 s, 1 s, 2 s, ...) when it fails."""
        for attempt in range(self.retries + 1):
            try:
                return fn(*args)
            except Exception:
                if attempt == self.retries:
                    raise
                self._sleep(0.5 * 2 ** attempt)

    def _get(self, name: str, default=None):
        return self._retry(self.kv.get, self._key(name), default)

    # ---- writing ----

    def _load_tail(self) -> None:
        """Continue after the events already in the store (a restarted writer)."""
        head = self._get("head", -1)
        self._head = head
        if head >= 0:
            self._chunk = list(self._get(f"c{head // self.chunk_size}", []))[: head % self.chunk_size + 1]

    def append(self, event: dict) -> int:
        """Add `event` (a dict that json.dumps(allow_nan=False) accepts) as the next event and return its index. Tried
        again when the store fails; if it keeps failing, the exception is raised and the event is not in the log."""
        text = json.dumps(event, allow_nan=False)
        with self._lock:
            if self._head is None:
                self._load_tail()
            seq = self._head + 1
            chunk = self._chunk if seq % self.chunk_size else []
            chunk = chunk + [text]
            self._retry(self.kv.update, {self._key(f"c{seq // self.chunk_size}"): chunk, self._key("head"): seq})
            self._head, self._chunk = seq, chunk
            return seq

    def set_meta(self, meta: dict) -> None:
        self._retry(self.kv.update, {self._key("meta"): meta})

    def request_stop(self) -> None:
        self._retry(self.kv.update, {self._key("stop"): time.time()})

    # ---- reading ----

    def head(self) -> int:
        """Index of the last event, -1 if there is none."""
        return self._get("head", -1)

    def meta(self) -> Optional[dict]:
        return self._get("meta")

    def exists(self) -> bool:
        """Whether the job is known: it has events or meta."""
        return self.meta() is not None or self.head() >= 0

    def stop_requested(self) -> bool:
        return self._get("stop") is not None

    def read_from(self, start: int = 0) -> list:
        """The events with index >= `start` as (index, event dict) pairs, in order. Events without a "type" get "progress"."""
        head = self.head()
        out = []
        if head < start:
            return out
        for n in range(start // self.chunk_size, head // self.chunk_size + 1):
            for i, text in enumerate(self._get(f"c{n}", [])):
                seq = n * self.chunk_size + i
                if seq < start or seq > head:
                    continue
                try:
                    ev = json.loads(text)
                except ValueError:
                    continue
                if isinstance(ev, dict):
                    ev.setdefault("type", "progress")
                    out.append((seq, ev))
        return out

    def last_event(self) -> Optional[dict]:
        head = self.head()
        if head < 0:
            return None
        events = self.read_from(head)
        return events[-1][1] if events else None
