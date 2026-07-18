"""No httpx calls — only calls on other identifiers with similar method names."""
import aiohttp


async def fetch(session, url):
    async with session.get(url) as resp:
        return await resp.json()


class AsyncClient:
    async def get(self, url):
        pass

    async def post(self, url, **kwargs):
        pass


client = AsyncClient()
