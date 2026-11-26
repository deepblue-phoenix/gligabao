"""Generators, iterators and lazy pipelines."""
import itertools
from collections import deque


def naturals(start=0):
    n = start
    while True:
        yield n
        n += 1


def take(n, it):
    return list(itertools.islice(it, n))


def primes():
    seen = []
    for n in naturals(2):
        if all(n % p for p in seen):
            seen.append(n)
            yield n


def chunked(iterable, size):
    it = iter(iterable)
    while True:
        chunk = take(size, it)
        if not chunk:
            return
        yield chunk


def window(iterable, size):
    buf = deque(maxlen=size)
    for item in iterable:
        buf.append(item)
        if len(buf) == size:
            yield tuple(buf)


def running_total(iterable):
    total = 0
    for x in iterable:
        total += x
        yield total


def echo():
    received = []
    while True:
        value = yield len(received)
        if value is None:
            break
        received.append(value)
    return received


class Countdown:
    def __init__(self, start):
        self.start = start

    def __iter__(self):
        return CountdownIter(self.start)


class CountdownIter:
    def __init__(self, n):
        self.n = n

    def __iter__(self):
        return self

    def __next__(self):
        if self.n <= 0:
            raise StopIteration
        self.n -= 1
        return self.n + 1


def flatten(nested):
    for item in nested:
        if isinstance(item, (list, tuple)):
            yield from flatten(item)
        else:
            yield item


def pairs(a, b):
    return ((x, y) for x in a for y in b if x != y)


def squares_of_evens(limit):
    return {n: n * n for n in range(limit) if n % 2 == 0}


def demo():
    print(take(10, primes()))
    print(list(chunked(range(10), 3)))
    print(list(window([1, 2, 3, 4, 5], 3)))
    print(list(running_total(naturals(1))[:5]) if False else None)
    print(list(Countdown(3)), list(flatten([1, [2, [3, 4]], 5])))
    gen = echo()
    next(gen)
    gen.send("a")
    print(list(pairs("ab", "bc")), squares_of_evens(10))
