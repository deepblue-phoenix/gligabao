"""A tiny expression tokenizer, parser and evaluator."""
import re
from collections import namedtuple

Token = namedtuple("Token", "kind value pos")

TOKEN_RE = re.compile(r"\s*(?:(\d+\.?\d*)|(.))")


class ParseError(Exception):
    pass


def tokenize(text):
    pos = 0
    out = []
    while pos < len(text):
        m = TOKEN_RE.match(text, pos)
        if not m:
            break
        num, op = m.groups()
        if num is not None:
            out.append(Token("num", float(num), m.start(1)))
        elif op:
            out.append(Token("op", op, m.start(2)))
        pos = m.end()
    out.append(Token("eof", None, pos))
    return out


class Parser:
    def __init__(self, tokens):
        self.tokens = tokens
        self.i = 0

    def peek(self):
        return self.tokens[self.i]

    def advance(self):
        tok = self.tokens[self.i]
        self.i += 1
        return tok

    def expect(self, value):
        tok = self.advance()
        if tok.value != value:
            raise ParseError(f"expected {value!r} at {tok.pos}")
        return tok

    def parse(self):
        node = self.expr()
        if self.peek().kind != "eof":
            raise ParseError("trailing input")
        return node

    def expr(self):
        node = self.term()
        while self.peek().value in ("+", "-"):
            op = self.advance().value
            node = (op, node, self.term())
        return node

    def term(self):
        node = self.factor()
        while self.peek().value in ("*", "/"):
            op = self.advance().value
            node = (op, node, self.factor())
        return node

    def factor(self):
        tok = self.advance()
        if tok.kind == "num":
            return tok.value
        if tok.value == "(":
            node = self.expr()
            self.expect(")")
            return node
        if tok.value == "-":
            return ("neg", self.factor())
        raise ParseError(f"unexpected {tok.value!r}")


OPS = {
    "+": lambda a, b: a + b,
    "-": lambda a, b: a - b,
    "*": lambda a, b: a * b,
    "/": lambda a, b: a / b,
}


def evaluate(node):
    if isinstance(node, float):
        return node
    if node[0] == "neg":
        return -evaluate(node[1])
    op, left, right = node
    return OPS[op](evaluate(left), evaluate(right))


def calc(text):
    return evaluate(Parser(tokenize(text)).parse())


if __name__ == "__main__":
    print(calc("1 + 2 * (3 - 4) / 5"))
