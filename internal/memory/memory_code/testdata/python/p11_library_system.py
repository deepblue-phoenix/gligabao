"""Library management: classes, closures, generators, decorators (long)."""
import datetime as dt
import functools
import json
import logging
from collections import Counter, defaultdict
from dataclasses import dataclass, field
from typing import Callable, Dict, Iterable, Iterator, List, Optional

log = logging.getLogger("library")

DEFAULT_LOAN_DAYS = 14
FINE_PER_DAY = 0.25


class LibraryError(Exception):
    """Base error."""


class NotAvailable(LibraryError):
    def __init__(self, book):
        super().__init__(f"{book.title} is not available")
        self.book = book


class LimitReached(LibraryError):
    pass


def audited(action):
    """Decorator factory that logs and counts calls."""
    counts = Counter()

    def decorator(fn):
        @functools.wraps(fn)
        def wrapper(self, *args, **kwargs):
            counts[action] += 1
            log.info("%s #%d", action, counts[action])
            result = fn(self, *args, **kwargs)
            self.audit_log.append((action, dt.datetime.now()))
            return result

        wrapper.counts = counts
        return wrapper

    return decorator


@dataclass(frozen=True)
class Book:
    isbn: str
    title: str
    author: str
    year: int = 0
    tags: tuple = ()

    def matches(self, term):
        term = term.lower()
        return term in self.title.lower() or term in self.author.lower()


@dataclass
class Member:
    member_id: int
    name: str
    loans: List["Loan"] = field(default_factory=list)
    limit: int = 3

    def active_loans(self):
        return [l for l in self.loans if l.returned is None]

    def can_borrow(self):
        return len(self.active_loans()) < self.limit


class PremiumMember(Member):
    def __init__(self, member_id, name):
        super().__init__(member_id, name, limit=10)

    def can_borrow(self):
        return True


@dataclass
class Loan:
    book: Book
    member: Member
    out: dt.date
    due: dt.date
    returned: Optional[dt.date] = None

    def overdue_days(self, today=None):
        end = self.returned or today or dt.date.today()
        return max((end - self.due).days, 0)

    def fine(self, today=None):
        return round(self.overdue_days(today) * FINE_PER_DAY, 2)


class Catalog:
    def __init__(self, books: Iterable[Book] = ()):
        self._books: Dict[str, Book] = {}
        self._copies = defaultdict(int)
        for b in books:
            self.add(b)

    def add(self, book, copies=1):
        self._books[book.isbn] = book
        self._copies[book.isbn] += copies

    def get(self, isbn):
        try:
            return self._books[isbn]
        except KeyError:
            raise LibraryError(f"unknown isbn {isbn}") from None

    def copies(self, isbn):
        return self._copies[isbn]

    def take(self, isbn):
        book = self.get(isbn)
        if self._copies[isbn] <= 0:
            raise NotAvailable(book)
        self._copies[isbn] -= 1
        return book

    def give_back(self, isbn):
        self._copies[isbn] += 1

    def search(self, term) -> Iterator[Book]:
        return (b for b in self._books.values() if b.matches(term))

    def by_tag(self):
        index = defaultdict(list)
        for b in self._books.values():
            for tag in b.tags:
                index[tag].append(b)
        return index

    def authors(self):
        return sorted({b.author for b in self._books.values()})


class Library:
    def __init__(self, catalog):
        self.catalog = catalog
        self.members = {}
        self.loans = []
        self.audit_log = []
        self._next_id = 1

    def join(self, name, premium=False):
        cls = PremiumMember if premium else Member
        member = cls(self._next_id, name) if premium else cls(self._next_id, name)
        self.members[member.member_id] = member
        self._next_id += 1
        return member

    @audited("borrow")
    def borrow(self, member, isbn, today=None):
        if not member.can_borrow():
            raise LimitReached(member.name)
        book = self.catalog.take(isbn)
        today = today or dt.date.today()
        loan = Loan(book, member, today, today + dt.timedelta(days=DEFAULT_LOAN_DAYS))
        member.loans.append(loan)
        self.loans.append(loan)
        return loan

    @audited("return")
    def give_back(self, loan, today=None):
        loan.returned = today or dt.date.today()
        self.catalog.give_back(loan.book.isbn)
        return loan.fine()

    def overdue(self, today=None):
        return [l for l in self.loans if l.returned is None and l.overdue_days(today)]

    def fines_by_member(self, today=None):
        totals = defaultdict(float)
        for loan in self.loans:
            totals[loan.member.name] += loan.fine(today)
        return dict(totals)

    def report(self):
        def line(loan):
            state = "out" if loan.returned is None else "back"
            return f"{loan.member.name}: {loan.book.title} [{state}]"

        return "\n".join(line(l) for l in self.loans)

    def export(self, fp):
        payload = [
            {"member": l.member.name, "isbn": l.book.isbn, "due": l.due.isoformat()}
            for l in self.loans
        ]
        json.dump(payload, fp, indent=2)


def make_reminder(library, days_before):
    """Closure that captures the library and the lead time."""
    sent = set()

    def remind(today):
        for loan in library.loans:
            key = id(loan)
            if loan.returned is None and key not in sent:
                if (loan.due - today).days <= days_before:
                    sent.add(key)
                    yield f"Dear {loan.member.name}, return {loan.book.title}"

    return remind


def make_searcher(catalog, *filters: Callable[[Book], bool]):
    def search(term):
        for b in catalog.search(term):
            if all(f(b) for f in filters):
                yield b

    return search


def seed():
    cat = Catalog([
        Book("1", "Dune", "Herbert", 1965, ("scifi",)),
        Book("2", "Emma", "Austen", 1815, ("classic",)),
        Book("3", "Neuromancer", "Gibson", 1984, ("scifi", "cyber")),
    ])
    cat.add(Book("4", "Persuasion", "Austen", 1817, ("classic",)), copies=2)
    return Library(cat)


def main():
    lib = seed()
    ann = lib.join("ann")
    bob = lib.join("bob", premium=True)
    today = dt.date(2024, 1, 1)
    first = lib.borrow(ann, "1", today)
    lib.borrow(bob, "3", today)
    remind = make_reminder(lib, 3)
    for msg in remind(today + dt.timedelta(days=12)):
        print(msg)
    print(lib.give_back(first, today + dt.timedelta(days=20)))
    print(lib.report(), lib.fines_by_member(today + dt.timedelta(days=30)))
    scifi = make_searcher(lib.catalog, lambda b: "scifi" in b.tags)
    print([b.title for b in scifi("e")], lib.catalog.authors())


if __name__ == "__main__":
    main()
