"""Integration tests against a running Atlas node.

Set ATLAS_TEST_ENDPOINT and ATLAS_TEST_TOKEN (an admin token) to run them;
CI starts a single node for this. Without those variables the tests skip.
"""

from __future__ import annotations

import asyncio
import os
import tempfile
import threading
import time
import unittest
import uuid

from atlas import AsyncAtlasClient, AtlasClient, AtlasError

ENDPOINT = os.environ.get("ATLAS_TEST_ENDPOINT", "")
TOKEN = os.environ.get("ATLAS_TEST_TOKEN", "")


def unique(prefix: str) -> str:
    return f"{prefix}-{uuid.uuid4().hex[:8]}"


@unittest.skipUnless(ENDPOINT and TOKEN, "set ATLAS_TEST_ENDPOINT and ATLAS_TEST_TOKEN")
class AtlasClientTest(unittest.TestCase):
    def setUp(self) -> None:
        self.client = AtlasClient(ENDPOINT, TOKEN)

    def test_node_and_members(self) -> None:
        node = self.client.node()
        self.assertTrue(node["node"]["id"])
        self.assertIn(node["node"]["id"], [m["id"] for m in self.client.members()])

    def test_idempotent_registration_update_and_delete(self) -> None:
        name = unique("py")
        first = self.client.register(name, "10.9.0.1:80", idempotency_key=f"deploy-{name}", tags=["py"])
        again = self.client.register(name, "10.9.0.1:80", idempotency_key=f"deploy-{name}", tags=["py"])
        self.assertEqual(first["id"], again["id"])
        self.assertEqual(again["revision"], 1)

        updated = self.client.update(first["id"], version="v2", if_revision=1)
        self.assertEqual(updated["version"], "v2")
        self.assertEqual(updated["address"], "10.9.0.1:80")
        with self.assertRaises(AtlasError) as stale:
            self.client.update(first["id"], version="v3", if_revision=1)
        self.assertEqual(stale.exception.code, "precondition_failed")
        self.assertEqual(stale.exception.status, 412)

        replaced = self.client.replace(first["id"], name, "10.9.0.2:80", if_revision=updated["revision"])
        self.assertEqual(replaced["address"], "10.9.0.2:80")
        self.assertNotIn("tags", replaced)  # PUT replaces the whole spec
        self.assertEqual(self.client.lookup(name)["id"], first["id"])

        self.client.delete(first["id"])
        with self.assertRaises(AtlasError) as missing:
            self.client.get(first["id"])
        self.assertEqual(missing.exception.code, "not_found")
        self.assertTrue(missing.exception.request_id)

    def test_ttl_heartbeat(self) -> None:
        service = self.client.register(unique("ttl"), "10.9.1.1:80", ttl_seconds=30)
        self.assertEqual(service["health"], "unknown")
        self.assertEqual(self.client.heartbeat(service["id"])["health"], "healthy")
        plain = self.client.register(unique("plain"), "10.9.1.2:80")
        with self.assertRaises(AtlasError) as no_ttl:
            self.client.heartbeat(plain["id"])
        self.assertEqual(no_ttl.exception.code, "no_ttl")

    def test_health_check_options_and_draining(self) -> None:
        name = unique("drain")
        tcp = self.client.register(
            name, "10.9.5.1:5432", health_check="tcp://10.9.5.1:5432",
            check_interval_seconds=10, unhealthy_threshold=4, healthy_threshold=1,
        )
        self.assertEqual(tcp["check_interval_seconds"], 10)
        self.assertEqual(tcp["unhealthy_threshold"], 4)
        other = self.client.register(name, "10.9.5.2:5432")
        self.assertTrue(self.client.drain(tcp["id"])["draining"])
        for _ in range(4):
            self.assertEqual(self.client.lookup(name)["id"], other["id"])
        self.assertNotIn("draining", self.client.undrain(tcp["id"]))
        with self.assertRaises(AtlasError) as bad:
            self.client.register(unique("bad"), "10.9.5.3:80", unhealthy_threshold=2)
        self.assertEqual(bad.exception.code, "validation_failed")

    def test_history_and_lookup_overrides(self) -> None:
        name = unique("hist")
        created = self.client.register(name, "10.9.6.1:80")
        self.client.update(created["id"], version="2")
        page = self.client.history(created["id"], limit=1)
        self.assertEqual(page["entries"][0]["change"], "updated")
        self.assertTrue(page["next_before"])
        changes = [entry["change"] for entry in self.client.iter_history(created["id"])]
        self.assertEqual(changes, ["updated", "registered"])
        self.assertEqual(len(self.client.recent_history(name=name)["entries"]), 2)

        picked = self.client.lookup(name, stale="exclude", latency="off", tolerance="5ms")
        self.assertEqual(picked["id"], created["id"])
        with self.assertRaises(AtlasError) as bad:
            self.client.lookup(name, stale="sometimes")
        self.assertEqual(bad.exception.code, "validation_failed")

    def test_paging_and_filters(self) -> None:
        name = unique("page")
        for i in range(5):
            self.client.register(name, f"10.9.2.{i + 1}:80", tags=["even" if i % 2 == 0 else "odd"])
        page = self.client.list_page(name=name, limit=2)
        self.assertEqual(len(page["services"]), 2)
        self.assertTrue(page["next_cursor"])
        self.assertEqual(len(list(self.client.iter_services(name=name))), 5)
        self.assertEqual(len(self.client.list_services(name=name, tags=["even"])), 3)
        self.assertEqual(self.client.lookup(name, tags=["odd"])["name"], name)

    def test_admin_operations(self) -> None:
        credentials = self.client.credentials()
        self.assertTrue(any(c["current"] for c in credentials))
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "atlas.db")
            self.assertGreater(self.client.backup(path), 0)
            with open(path, "rb") as snapshot:
                self.assertEqual(snapshot.read(15), b"SQLite format 3")
        with self.assertRaises(AtlasError) as unknown:
            self.client.evict_node("no-such-node")
        self.assertEqual(unknown.exception.code, "not_found")

    def test_watch_receives_service_events(self) -> None:
        name = unique("watch")

        def register_later() -> None:
            time.sleep(0.5)
            AtlasClient(ENDPOINT, TOKEN).register(name, "10.9.3.1:80")

        threading.Thread(target=register_later, daemon=True).start()
        for event in self.client.watch(kinds=["service"], service=name):
            self.assertEqual(event["type"], "service.registered")
            self.assertEqual(event["service"]["name"], name)
            self.assertTrue(event["id"])
            break

        reset = next(self.client.watch(last_event_id="elsewhere.1", reconnect=False))
        self.assertEqual(reset["type"], "reset")


@unittest.skipUnless(ENDPOINT and TOKEN, "set ATLAS_TEST_ENDPOINT and ATLAS_TEST_TOKEN")
class AsyncAtlasClientTest(unittest.TestCase):
    def test_async_round_trip_and_watch(self) -> None:
        async def scenario() -> None:
            client = AsyncAtlasClient(ENDPOINT, TOKEN)
            name = unique("async")
            created = await client.register(name, "10.9.4.1:80")
            self.assertEqual((await client.get(created["id"]))["name"], name)
            self.assertTrue((await client.members()))

            async def register_later() -> None:
                await asyncio.sleep(0.5)
                await client.register(name, "10.9.4.2:80")

            task = asyncio.create_task(register_later())
            async for event in client.watch(kinds=["service"], service=name):
                self.assertEqual(event["service"]["address"], "10.9.4.2:80")
                break
            await task
            await client.delete(created["id"])

        asyncio.run(asyncio.wait_for(scenario(), timeout=20))


class OfflineTest(unittest.TestCase):
    def test_rejects_bad_endpoint(self) -> None:
        with self.assertRaises(ValueError):
            AtlasClient("ftp://example", "t")

    def test_unreachable_node_raises_without_status(self) -> None:
        client = AtlasClient("http://127.0.0.1:9", "t", timeout=0.5)
        with self.assertRaises(AtlasError) as failure:
            client.node()
        self.assertIsNone(failure.exception.status)


if __name__ == "__main__":
    unittest.main()
