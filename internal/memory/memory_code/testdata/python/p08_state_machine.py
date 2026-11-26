"""Event system and state machine with nested functions."""
from collections import defaultdict


class EventBus:
    def __init__(self):
        self._subs = defaultdict(list)

    def on(self, name):
        def register(fn):
            self._subs[name].append(fn)
            return fn

        return register

    def emit(self, name, *args, **kwargs):
        return [fn(*args, **kwargs) for fn in self._subs[name]]


bus = EventBus()


@bus.on("start")
def log_start(machine):
    print("starting", machine.name)


@bus.on("stop")
def log_stop(machine):
    print("stopping", machine.name)


class Transition:
    def __init__(self, src, dst, guard=None):
        self.src = src
        self.dst = dst
        self.guard = guard or (lambda m: True)


class Machine:
    TRANSITIONS = {
        "idle": ["running"],
        "running": ["paused", "stopped"],
        "paused": ["running", "stopped"],
    }

    def __init__(self, name):
        self.name = name
        self.state = "idle"
        self.log = []

    def can(self, dst):
        return dst in self.TRANSITIONS.get(self.state, [])

    def go(self, dst):
        if not self.can(dst):
            raise RuntimeError(f"{self.state} -> {dst}")
        self.log.append((self.state, dst))
        self.state = dst
        if dst == "running":
            bus.emit("start", self)
        elif dst == "stopped":
            bus.emit("stop", self)
        return self

    def run_script(self, steps):
        def step(acc, dst):
            try:
                self.go(dst)
                return acc + [dst]
            except RuntimeError:
                return acc

        result = []
        for s in steps:
            result = step(result, s)
        return result


def make_guarded(machine, limit):
    calls = 0

    def guarded(dst):
        nonlocal calls
        calls += 1
        if calls > limit:
            raise RuntimeError("too many transitions")
        return machine.go(dst)

    return guarded


def history(machine):
    def fmt(pair):
        a, b = pair
        return f"{a}->{b}"

    return ", ".join(map(fmt, machine.log))


def demo():
    m = Machine("m1")
    g = make_guarded(m, 3)
    g("running")
    g("paused")
    g("running")
    print(history(m), m.run_script(["stopped", "running"]))
    return m
