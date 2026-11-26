"""LRU cache, decorator classes and context managers."""
import threading
import time
from collections import OrderedDict
from contextlib import contextmanager


class LRU:
    def __init__(self, capacity=128):
        self.capacity = capacity
        self.data = OrderedDict()
        self.lock = threading.RLock()
        self.hits = self.misses = 0

    def get(self, key, default=None):
        with self.lock:
            if key in self.data:
                self.data.move_to_end(key)
                self.hits += 1
                return self.data[key]
            self.misses += 1
            return default

    def put(self, key, value):
        with self.lock:
            self.data[key] = value
            self.data.move_to_end(key)
            while len(self.data) > self.capacity:
                self.data.popitem(last=False)

    def stats(self):
        total = self.hits + self.misses
        return self.hits / total if total else 0.0


class cached:
    """Decorator class."""

    def __init__(self, capacity=32, ttl=None):
        self.cache = LRU(capacity)
        self.ttl = ttl

    def __call__(self, fn):
        def wrapper(*args):
            hit = self.cache.get(args)
            if hit is not None:
                value, stamp = hit
                if self.ttl is None or time.time() - stamp < self.ttl:
                    return value
            value = fn(*args)
            self.cache.put(args, (value, time.time()))
            return value

        wrapper.cache = self.cache
        return wrapper


@contextmanager
def locked(lock):
    lock.acquire()
    try:
        yield lock
    finally:
        lock.release()


@contextmanager
def stopwatch(sink):
    t0 = time.time()
    yield
    sink.append(time.time() - t0)


@cached(capacity=2, ttl=60)
def slow_square(n):
    time.sleep(0.01)
    return n * n


def run():
    times = []
    with stopwatch(times):
        for n in [1, 2, 1, 3, 1]:
            slow_square(n)
    with locked(threading.Lock()):
        print(slow_square.cache.stats(), times)
