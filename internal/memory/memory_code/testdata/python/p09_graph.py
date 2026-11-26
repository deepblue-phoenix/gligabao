"""Graph algorithms with nested helpers."""
import heapq
from collections import deque


class Graph:
    def __init__(self, directed=False):
        self.directed = directed
        self.adj = {}

    def add_edge(self, a, b, w=1):
        self.adj.setdefault(a, {})[b] = w
        self.adj.setdefault(b, {})
        if not self.directed:
            self.adj[b][a] = w

    def nodes(self):
        return list(self.adj)

    def neighbors(self, n):
        return self.adj[n].items()

    def bfs(self, start):
        seen = {start}
        order = []
        queue = deque([start])
        while queue:
            cur = queue.popleft()
            order.append(cur)
            for nxt, _ in self.neighbors(cur):
                if nxt not in seen:
                    seen.add(nxt)
                    queue.append(nxt)
        return order

    def dfs(self, start):
        seen = set()
        order = []

        def visit(n):
            if n in seen:
                return
            seen.add(n)
            order.append(n)
            for nxt, _ in self.neighbors(n):
                visit(nxt)

        visit(start)
        return order

    def dijkstra(self, src):
        dist = {n: float("inf") for n in self.adj}
        dist[src] = 0
        heap = [(0, src)]
        while heap:
            d, n = heapq.heappop(heap)
            if d > dist[n]:
                continue
            for nxt, w in self.neighbors(n):
                nd = d + w
                if nd < dist[nxt]:
                    dist[nxt] = nd
                    heapq.heappush(heap, (nd, nxt))
        return dist

    def components(self):
        seen = set()
        comps = []
        for n in self.nodes():
            if n not in seen:
                comp = self.bfs(n)
                seen.update(comp)
                comps.append(comp)
        return comps

    def topo_sort(self):
        indeg = {n: 0 for n in self.adj}
        for n in self.adj:
            for m, _ in self.neighbors(n):
                indeg[m] += 1
        ready = deque(n for n, d in indeg.items() if d == 0)
        out = []
        while ready:
            n = ready.popleft()
            out.append(n)
            for m, _ in self.neighbors(n):
                indeg[m] -= 1
                if indeg[m] == 0:
                    ready.append(m)
        return out


def from_pairs(pairs, directed=False):
    g = Graph(directed)
    for a, b, *rest in pairs:
        g.add_edge(a, b, *rest)
    return g


def shortest(g, a, b):
    return g.dijkstra(a)[b]
