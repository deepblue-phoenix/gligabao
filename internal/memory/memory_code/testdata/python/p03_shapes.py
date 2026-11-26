"""Abstract base classes and polymorphism."""
import math
from abc import ABC, abstractmethod
from dataclasses import dataclass, field


class Shape(ABC):
    @abstractmethod
    def area(self):
        raise NotImplementedError

    @abstractmethod
    def perimeter(self):
        raise NotImplementedError

    def describe(self):
        return f"{type(self).__name__} area={self.area():.2f} perimeter={self.perimeter():.2f}"

    def __lt__(self, other):
        return self.area() < other.area()


@dataclass
class Circle(Shape):
    radius: float

    def area(self):
        return math.pi * self.radius ** 2

    def perimeter(self):
        return 2 * math.pi * self.radius


@dataclass
class Rect(Shape):
    w: float
    h: float

    def area(self):
        return self.w * self.h

    def perimeter(self):
        return 2 * (self.w + self.h)


class Square(Rect):
    def __init__(self, side):
        super().__init__(side, side)


@dataclass
class Triangle(Shape):
    a: float
    b: float
    c: float
    tags: list = field(default_factory=list)

    def perimeter(self):
        return self.a + self.b + self.c

    def area(self):
        s = self.perimeter() / 2
        return math.sqrt(s * (s - self.a) * (s - self.b) * (s - self.c))


class Group(Shape):
    def __init__(self, *members):
        self.members = list(members)

    def area(self):
        return sum(m.area() for m in self.members)

    def perimeter(self):
        return sum(m.perimeter() for m in self.members)

    def __iter__(self):
        yield from self.members

    def largest(self):
        return max(self.members)


def biggest(shapes):
    return sorted(shapes, key=lambda s: s.area(), reverse=True)[0]


def report(shapes):
    lines = [s.describe() for s in shapes]
    lines.append(f"total={sum(s.area() for s in shapes):.2f}")
    return "\n".join(lines)


def main():
    shapes = [Circle(1.5), Rect(2, 3), Square(4), Triangle(3, 4, 5)]
    shapes.append(Group(*shapes[:2]))
    print(report(shapes))
    print(biggest(shapes))
