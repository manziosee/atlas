"""asyncio wrapper around :class:`AtlasClient`.

Requests run in worker threads (``asyncio.to_thread``), so the package keeps
its standard-library-only dependency footprint while never blocking the
event loop.
"""

from __future__ import annotations

import asyncio
import threading
from collections.abc import AsyncIterator, Callable
from typing import Any, TypeVar

from .client import AtlasClient, Event, Member, Page, ServiceInstance

T = TypeVar("T")

_DONE = object()


class AsyncAtlasClient:
    """Async counterpart of :class:`AtlasClient` with the same arguments."""

    def __init__(self, endpoint: str, token: str, **options: Any) -> None:
        self._client = AtlasClient(endpoint, token, **options)

    async def _call(self, fn: Callable[..., T], *args: Any, **kwargs: Any) -> T:
        return await asyncio.to_thread(fn, *args, **kwargs)

    async def register(self, name: str, address: str, **options: Any) -> ServiceInstance:
        return await self._call(self._client.register, name, address, **options)

    async def replace(self, service_id: str, name: str, address: str, **options: Any) -> ServiceInstance:
        return await self._call(self._client.replace, service_id, name, address, **options)

    async def update(self, service_id: str, **fields: Any) -> ServiceInstance:
        return await self._call(self._client.update, service_id, **fields)

    async def heartbeat(self, service_id: str) -> ServiceInstance:
        return await self._call(self._client.heartbeat, service_id)

    async def list_page(self, **options: Any) -> Page:
        return await self._call(self._client.list_page, **options)

    async def list_services(self, **filters: Any) -> list[ServiceInstance]:
        return await self._call(self._client.list_services, **filters)

    async def lookup(self, name: str, **options: Any) -> ServiceInstance:
        return await self._call(self._client.lookup, name, **options)

    async def get(self, service_id: str) -> ServiceInstance:
        return await self._call(self._client.get, service_id)

    async def delete(self, service_id: str) -> None:
        await self._call(self._client.delete, service_id)

    async def node(self) -> dict[str, Any]:
        return await self._call(self._client.node)

    async def members(self) -> list[Member]:
        return await self._call(self._client.members)

    async def credentials(self) -> list[dict[str, Any]]:
        return await self._call(self._client.credentials)

    async def backup(self, path: str) -> int:
        return await self._call(self._client.backup, path)

    async def evict_node(self, node_id: str) -> dict[str, Any]:
        return await self._call(self._client.evict_node, node_id)

    async def watch(self, **options: Any) -> AsyncIterator[Event]:
        """Async iterator over :meth:`AtlasClient.watch` events.

        The blocking stream runs on a daemon thread and hands events to the
        loop through a queue. Leaving the ``async for`` stops delivery; the
        thread exits at its next event or keepalive.
        """
        loop = asyncio.get_running_loop()
        queue: asyncio.Queue[Any] = asyncio.Queue(maxsize=256)
        stop = threading.Event()

        def pump() -> None:
            try:
                for event in self._client.watch(**options):
                    if stop.is_set():
                        return
                    asyncio.run_coroutine_threadsafe(queue.put(event), loop).result()
            except BaseException as exc:  # noqa: BLE001 - re-raised in the consumer
                if not stop.is_set():
                    asyncio.run_coroutine_threadsafe(queue.put(exc), loop)
                return
            asyncio.run_coroutine_threadsafe(queue.put(_DONE), loop)

        threading.Thread(target=pump, name="atlas-watch", daemon=True).start()
        try:
            while True:
                item = await queue.get()
                if item is _DONE:
                    return
                if isinstance(item, BaseException):
                    raise item
                yield item
        finally:
            stop.set()
