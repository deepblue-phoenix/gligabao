"""Bank accounts with inheritance and properties."""
import logging
from decimal import Decimal
from typing import List, Optional

log = logging.getLogger(__name__)


class InsufficientFunds(Exception):
    """Raised when a withdrawal is too large."""

    def __init__(self, needed, available):
        super().__init__(f"need {needed}, have {available}")
        self.needed = needed
        self.available = available


class Account:
    """A plain account."""

    count = 0

    def __init__(self, owner: str, balance: Decimal = Decimal("0")):
        self.owner = owner
        self._balance = balance
        self.history: List[tuple] = []
        Account.count += 1

    @property
    def balance(self) -> Decimal:
        return self._balance

    @balance.setter
    def balance(self, value):
        if value < 0:
            raise ValueError("negative balance")
        self._balance = value

    @classmethod
    def open(cls, owner):
        acct = cls(owner)
        log.info("opened %s", owner)
        return acct

    @staticmethod
    def validate(amount):
        return amount > 0

    def deposit(self, amount):
        if not self.validate(amount):
            raise ValueError(amount)
        self._balance += amount
        self.record("deposit", amount)

    def withdraw(self, amount):
        if amount > self._balance:
            raise InsufficientFunds(amount, self._balance)
        self._balance -= amount
        self.record("withdraw", amount)

    def record(self, kind, amount):
        self.history.append((kind, amount))

    def __repr__(self):
        return "Account(%r, %s)" % (self.owner, self._balance)


class Savings(Account):
    rate = Decimal("0.02")

    def add_interest(self):
        gain = self._balance * self.rate
        self.deposit(gain)
        return gain


class Checking(Account):
    def __init__(self, owner, overdraft=0):
        super().__init__(owner)
        self.overdraft = overdraft

    def withdraw(self, amount):
        if amount > self._balance + self.overdraft:
            raise InsufficientFunds(amount, self._balance)
        self._balance -= amount
        self.record("withdraw", amount)


def transfer(src: Account, dst: Account, amount) -> Optional[Exception]:
    try:
        src.withdraw(amount)
        dst.deposit(amount)
    except InsufficientFunds as exc:
        log.warning("transfer failed: %s", exc)
        return exc
    finally:
        log.debug("transfer done")
    return None


def main():
    a = Savings.open("ann")
    b = Checking("bob", overdraft=50)
    a.deposit(Decimal("100"))
    print(transfer(a, b, Decimal("30")), a, b)
    a.add_interest()


if __name__ == "__main__":
    main()
