"""Async workers, context managers and queues."""
import asyncio
import contextlib
import random


class RateLimiter:
    def __init__(self, per_second):
        self.delay = 1.0 / per_second
        self._lock = asyncio.Lock()

    async def __aenter__(self):
        await self._lock.acquire()
        await asyncio.sleep(self.delay)
        return self

    async def __aexit__(self, exc_type, exc, tb):
        self._lock.release()
        return False


@contextlib.asynccontextmanager
async def session(name):
    print("open", name)
    try:
        yield {"name": name}
    finally:
        print("close", name)


async def fetch(url, limiter):
    async with limiter:
        await asyncio.sleep(random.random() / 10)
        return url, len(url)


async def worker(name, queue, results):
    while True:
        url = await queue.get()
        try:
            results.append(await fetch(url, RateLimiter(50)))
        except Exception as exc:
            results.append((url, exc))
        finally:
            queue.task_done()


async def ticker(n):
    for i in range(n):
        yield i
        await asyncio.sleep(0)


async def collect(n):
    return [i async for i in ticker(n)]


class Crawler:
    def __init__(self, urls, workers=3):
        self.urls = urls
        self.workers = workers
        self.results = []

    async def run(self):
        queue = asyncio.Queue()
        for u in self.urls:
            queue.put_nowait(u)
        tasks = [
            asyncio.create_task(worker(f"w{i}", queue, self.results))
            for i in range(self.workers)
        ]
        await queue.join()
        for t in tasks:
            t.cancel()
        return self.results

    async def run_with_timeout(self, seconds):
        return await asyncio.wait_for(self.run(), seconds)


async def main():
    async with session("crawl") as s:
        crawler = Crawler(["a.com", "bb.org", "ccc.net"])
        res = await crawler.run_with_timeout(5)
        print(s["name"], res, await collect(3))
    done = await asyncio.gather(fetch("x", RateLimiter(10)), fetch("y", RateLimiter(10)))
    return done


if __name__ == "__main__":
    asyncio.run(main())
