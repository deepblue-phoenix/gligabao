"""Closures, nonlocal and function factories."""
import functools
import time


def make_counter(start=0, step=1):
    count = start

    def tick():
        nonlocal count
        count += step
        return count

    def reset():
        nonlocal count
        count = start

    def peek():
        return count

    tick.reset = reset
    tick.peek = peek
    return tick


def make_adders(n):
    return [lambda x, i=i: x + i for i in range(n)]


def compose(*funcs):
    def pipe(value):
        for fn in reversed(funcs):
            value = fn(value)
        return value

    return pipe


def curry(fn):
    arity = fn.__code__.co_argcount

    def collect(args):
        def inner(*more):
            merged = args + more
            if len(merged) >= arity:
                return fn(*merged)
            return collect(merged)

        return inner

    return collect(())


def memoize(fn):
    cache = {}

    @functools.wraps(fn)
    def wrapper(*args):
        if args not in cache:
            cache[args] = fn(*args)
        return cache[args]

    wrapper.cache = cache
    return wrapper


def timed(label):
    def decorator(fn):
        @functools.wraps(fn)
        def wrapper(*args, **kwargs):
            start = time.perf_counter()
            try:
                return fn(*args, **kwargs)
            finally:
                elapsed = time.perf_counter() - start
                print(f"{label}: {elapsed:.3f}s")

        return wrapper

    return decorator


@memoize
def fib(n):
    return n if n < 2 else fib(n - 1) + fib(n - 2)


@timed("sum")
@curry
def add3(a, b, c):
    return a + b + c


def counter_demo():
    c = make_counter(10, 5)
    c()
    c()
    c.reset()
    return c.peek()


def pipeline_demo():
    inc = lambda x: x + 1
    dbl = lambda x: x * 2
    both = compose(inc, dbl)
    return both(5), [f(1) for f in make_adders(3)]


if __name__ == "__main__":
    print(fib(20), counter_demo(), pipeline_demo())
