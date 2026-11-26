"""A small Lisp interpreter: closures, recursion, nested functions (long)."""
import math
import operator as op
import sys


class LispError(Exception):
    pass


class Symbol(str):
    __slots__ = ()


class Env(dict):
    """Environment: a dict with an outer link."""

    def __init__(self, params=(), args=(), outer=None):
        super().__init__()
        self.update(zip(params, args))
        self.outer = outer

    def find(self, name):
        if name in self:
            return self
        if self.outer is None:
            raise LispError(f"unbound symbol: {name}")
        return self.outer.find(name)


class Procedure:
    def __init__(self, params, body, env):
        self.params = params
        self.body = body
        self.env = env

    def __call__(self, *args):
        if len(args) != len(self.params):
            raise LispError(f"expected {len(self.params)} args, got {len(args)}")
        return evaluate(self.body, Env(self.params, args, self.env))


def tokenize(src):
    return src.replace("(", " ( ").replace(")", " ) ").replace("'", " ' ").split()


def atom(token):
    try:
        return int(token)
    except ValueError:
        try:
            return float(token)
        except ValueError:
            return Symbol(token)


def read_from(tokens):
    if not tokens:
        raise LispError("unexpected EOF")
    tok = tokens.pop(0)
    if tok == "(":
        items = []
        while tokens and tokens[0] != ")":
            items.append(read_from(tokens))
        if not tokens:
            raise LispError("missing )")
        tokens.pop(0)
        return items
    if tok == ")":
        raise LispError("unexpected )")
    if tok == "'":
        return [Symbol("quote"), read_from(tokens)]
    return atom(tok)


def parse(src):
    return read_from(tokenize(src))


def standard_env():
    env = Env()
    env.update(vars(math))
    env.update({
        "+": op.add,
        "-": op.sub,
        "*": op.mul,
        "/": op.truediv,
        ">": op.gt,
        "<": op.lt,
        ">=": op.ge,
        "<=": op.le,
        "=": op.eq,
        "abs": abs,
        "car": lambda xs: xs[0],
        "cdr": lambda xs: xs[1:],
        "cons": lambda x, xs: [x] + xs,
        "list": lambda *xs: list(xs),
        "null?": lambda xs: xs == [],
        "map": lambda f, xs: [f(x) for x in xs],
        "filter": lambda f, xs: [x for x in xs if f(x)],
        "length": len,
        "print": print,
    })
    return env


GLOBAL = standard_env()


def evaluate(x, env=GLOBAL):
    while True:
        if isinstance(x, Symbol):
            return env.find(x)[x]
        if not isinstance(x, list):
            return x
        head, *rest = x
        if head == "quote":
            return rest[0]
        if head == "if":
            test, then, other = rest
            x = then if evaluate(test, env) else other
            continue
        if head == "define":
            name, expr = rest
            env[name] = evaluate(expr, env)
            return None
        if head == "set!":
            name, expr = rest
            env.find(name)[name] = evaluate(expr, env)
            return None
        if head == "lambda":
            params, body = rest
            return Procedure(params, body, env)
        if head == "begin":
            for sub in rest[:-1]:
                evaluate(sub, env)
            x = rest[-1]
            continue
        if head == "let":
            bindings, body = rest
            names = [b[0] for b in bindings]
            vals = [evaluate(b[1], env) for b in bindings]
            env = Env(names, vals, env)
            x = body
            continue
        proc = evaluate(head, env)
        args = [evaluate(a, env) for a in rest]
        if isinstance(proc, Procedure):
            x, env = proc.body, Env(proc.params, args, proc.env)
            continue
        return proc(*args)


def to_string(value):
    if isinstance(value, list):
        return "(" + " ".join(map(to_string, value)) + ")"
    if value is True:
        return "#t"
    if value is False:
        return "#f"
    return str(value)


def run(src, env=GLOBAL):
    return evaluate(parse(src), env)


def make_repl(prompt="lisp> ", env=None):
    env = env if env is not None else standard_env()
    history = []

    def step(line):
        history.append(line)
        try:
            value = run(line, env)
        except LispError as exc:
            return f"error: {exc}"
        return None if value is None else to_string(value)

    def loop(stream=sys.stdin):
        for line in stream:
            line = line.strip()
            if not line:
                continue
            out = step(line)
            if out is not None:
                print(out)

    step.history = history
    step.loop = loop
    return step


def counter_program():
    return """
    (begin
      (define make-counter
        (lambda (n) (lambda () (begin (set! n (+ n 1)) n))))
      (define c (make-counter 0))
      (c) (c) (c))
    """


def fact_program(n):
    return f"""
    (begin
      (define fact (lambda (n) (if (<= n 1) 1 (* n (fact (- n 1))))))
      (fact {n}))
    """


def map_program():
    return "(map (lambda (x) (* x x)) (filter (lambda (x) (> x 2)) (list 1 2 3 4 5)))"


def self_test():
    results = {
        "counter": run(counter_program(), standard_env()),
        "fact": run(fact_program(10), standard_env()),
        "map": to_string(run(map_program(), standard_env())),
    }
    assert results["counter"] == 3
    assert results["fact"] == 3628800
    return results


if __name__ == "__main__":
    print(self_test())
    make_repl().loop()
