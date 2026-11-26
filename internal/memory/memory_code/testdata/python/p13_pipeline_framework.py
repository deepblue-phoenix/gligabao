"""A data pipeline framework: decorators, registries, nested closures (long)."""
import abc
import csv
import functools
import io
import itertools
import operator
import re
import statistics
from collections import OrderedDict, defaultdict
from typing import Any, Callable, Dict, Iterable, List

Row = Dict[str, Any]
Step = Callable[[Iterable[Row]], Iterable[Row]]

_REGISTRY: Dict[str, Callable] = {}


def register(name=None):
    def decorate(fn):
        key = name or fn.__name__
        if key in _REGISTRY:
            raise KeyError(f"duplicate step {key}")
        _REGISTRY[key] = fn
        return fn

    return decorate


def lookup(name):
    return _REGISTRY[name]


@register()
def select(*columns):
    def run(rows):
        for r in rows:
            yield {c: r[c] for c in columns}

    return run


@register()
def where(pred):
    def run(rows):
        return (r for r in rows if pred(r))

    return run


@register("rename")
def rename_columns(mapping):
    def run(rows):
        for r in rows:
            yield {mapping.get(k, k): v for k, v in r.items()}

    return run


@register()
def cast(**types):
    def run(rows):
        for r in rows:
            out = dict(r)
            for col, fn in types.items():
                out[col] = fn(out[col])
            yield out

    return run


@register()
def derive(name, fn):
    def run(rows):
        for r in rows:
            yield {**r, name: fn(r)}

    return run


@register()
def sort_by(column, reverse=False):
    def run(rows):
        return iter(sorted(rows, key=operator.itemgetter(column), reverse=reverse))

    return run


@register()
def group_agg(key, column, agg):
    def run(rows):
        groups = defaultdict(list)
        for r in rows:
            groups[r[key]].append(r[column])
        for k, vals in groups.items():
            yield {key: k, f"{agg.__name__}_{column}": agg(vals)}

    return run


@register()
def limit(n):
    return lambda rows: itertools.islice(rows, n)


class Source(abc.ABC):
    @abc.abstractmethod
    def rows(self) -> Iterable[Row]:
        ...


class CsvSource(Source):
    def __init__(self, text):
        self.text = text

    def rows(self):
        yield from csv.DictReader(io.StringIO(self.text))


class ListSource(Source):
    def __init__(self, data):
        self.data = list(data)

    def rows(self):
        return iter(self.data)


class Sink(abc.ABC):
    @abc.abstractmethod
    def write(self, rows: Iterable[Row]) -> Any:
        ...


class ListSink(Sink):
    def write(self, rows):
        return list(rows)


class CsvSink(Sink):
    def write(self, rows):
        rows = list(rows)
        if not rows:
            return ""
        buf = io.StringIO()
        w = csv.DictWriter(buf, fieldnames=list(rows[0]))
        w.writeheader()
        w.writerows(rows)
        return buf.getvalue()


class Pipeline:
    def __init__(self, source: Source, name="pipeline"):
        self.source = source
        self.name = name
        self.steps: List[Step] = []
        self.stats = OrderedDict()

    def pipe(self, step: Step):
        self.steps.append(step)
        return self

    def then(self, name, *args, **kwargs):
        return self.pipe(lookup(name)(*args, **kwargs))

    def _instrument(self, index, step):
        label = f"{index}:{getattr(step, '__name__', 'step')}"

        def counted(rows):
            n = 0
            for r in step(rows):
                n += 1
                yield r
            self.stats[label] = n

        return counted

    def run(self, sink: Sink = None):
        sink = sink or ListSink()
        stream = self.source.rows()
        for i, step in enumerate(self.steps):
            stream = self._instrument(i, step)(stream)
        return sink.write(stream)

    def __or__(self, step):
        return Pipeline(self.source, self.name).pipe(step) if not self.steps else self.pipe(step)


def clean_name(row):
    return re.sub(r"\s+", " ", row["name"]).strip().title()


def make_threshold(column, minimum):
    def check(row):
        return row[column] >= minimum

    return check


def make_bucketer(*edges):
    def bucket(row):
        for i, e in enumerate(edges):
            if row["score"] < e:
                return i
        return len(edges)

    return bucket


def build_report(text):
    pipe = Pipeline(CsvSource(text), "scores")
    pipe.then("cast", score=float, age=int)
    pipe.then("derive", "name", clean_name)
    pipe.then("derive", "bucket", make_bucketer(50, 80))
    pipe.then("where", make_threshold("age", 18))
    pipe.then("group_agg", "bucket", "score", statistics.mean)
    pipe.then("sort_by", "bucket")
    return pipe


DATA = """name,age,score
 ann  lee,30,91
bob,17,40
cy   d,22,65
dee,45,78
"""

if __name__ == "__main__":
    p = build_report(DATA)
    print(p.run(CsvSink()))
    print(dict(p.stats))
