"""Inventory with mixins, metaclass and operators."""
import json
from collections import defaultdict
from enum import Enum


class Category(Enum):
    TOOL = "tool"
    FOOD = "food"
    TOY = "toy"


class Registry(type):
    classes = {}

    def __new__(mcs, name, bases, ns):
        cls = super().__new__(mcs, name, bases, ns)
        mcs.classes[name] = cls
        return cls


class JsonMixin:
    def to_json(self):
        return json.dumps(self.__dict__, default=str)

    @classmethod
    def from_json(cls, text):
        return cls(**json.loads(text))


class ComparableMixin:
    def key(self):
        raise NotImplementedError

    def __eq__(self, other):
        return self.key() == other.key()

    def __lt__(self, other):
        return self.key() < other.key()


class Item(JsonMixin, ComparableMixin, metaclass=Registry):
    def __init__(self, sku, name, price, qty=0):
        self.sku = sku
        self.name = name
        self.price = price
        self.qty = qty

    def key(self):
        return self.sku

    def total(self):
        return self.price * self.qty


class Perishable(Item):
    def __init__(self, sku, name, price, qty=0, days=7):
        super().__init__(sku, name, price, qty)
        self.days = days

    def total(self):
        base = super().total()
        return base * 0.5 if self.days < 2 else base


class Inventory:
    def __init__(self):
        self._items = {}
        self._by_cat = defaultdict(list)

    def add(self, item, cat=Category.TOOL):
        self._items[item.sku] = item
        self._by_cat[cat].append(item.sku)
        return self

    def __getitem__(self, sku):
        return self._items[sku]

    def __len__(self):
        return len(self._items)

    def __contains__(self, sku):
        return sku in self._items

    def __add__(self, other):
        merged = Inventory()
        for inv in (self, other):
            for item in inv._items.values():
                merged.add(item)
        return merged

    def value(self):
        return sum(i.total() for i in self._items.values())

    def cheapest(self, cat):
        skus = self._by_cat[cat]
        return min((self._items[s] for s in skus), key=lambda i: i.price)

    def dump(self):
        return json.dumps({k: v.to_json() for k, v in self._items.items()})


def build():
    inv = Inventory()
    inv.add(Item("h1", "hammer", 12.5, 4)).add(Perishable("a1", "apple", 0.4, 100, 1), Category.FOOD)
    return inv
