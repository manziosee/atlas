#!/usr/bin/env python3
"""Register an HTTP service with Atlas, or look up and call one."""

from __future__ import annotations

import json
import os
import signal
import socket
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import Request, urlopen

SERVICE_NAME = "example-python"
TTL_SECONDS = 15


class AtlasError(RuntimeError):
    def __init__(self, status: int, code: str, message: str) -> None:
        super().__init__(f"Atlas returned HTTP {status} ({code}): {message}")
        self.status = status
        self.code = code


def atlas_config() -> tuple[str, str]:
    endpoint = os.environ.get("ATLAS_ADDR", "http://127.0.0.1:8081").rstrip("/")
    token = os.environ.get("ATLAS_TOKEN", "")
    if not endpoint.startswith(("http://", "https://")):
        raise ValueError("ATLAS_ADDR must start with http:// or https://")
    if not token:
        raise ValueError("ATLAS_TOKEN must contain an Atlas API token")
    return endpoint, token


def atlas_request(
    endpoint: str,
    token: str,
    method: str,
    path: str,
    body: dict[str, object] | None = None,
    idempotency_key: str = "",
) -> dict[str, object]:
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Authorization": f"Bearer {token}", "Accept": "application/json"}
    if data is not None:
        headers["Content-Type"] = "application/json"
    if idempotency_key:
        headers["Idempotency-Key"] = idempotency_key
    request = Request(endpoint + path, data=data, headers=headers, method=method)
    try:
        with urlopen(request, timeout=5) as response:
            if response.status == 204:
                return {}
            return json.loads(response.read())
    except HTTPError as exc:
        try:
            error = json.loads(exc.read())
        except json.JSONDecodeError:
            error = {}
        raise AtlasError(exc.code, error.get("code", ""), error.get("error", str(exc))) from exc
    except (URLError, TimeoutError) as exc:
        raise RuntimeError(f"Atlas request failed: {exc}") from exc


def register(endpoint: str, token: str, address: str, idempotency_key: str) -> dict[str, object]:
    return atlas_request(
        endpoint,
        token,
        "POST",
        "/v1/services",
        {
            "name": SERVICE_NAME,
            "address": address,
            "ttl_seconds": TTL_SECONDS,
            "version": "1.0.0",
            "tags": ["example", "python"],
        },
        idempotency_key,
    )


def heartbeat_loop(
    endpoint: str,
    token: str,
    address: str,
    idempotency_key: str,
    registered: dict[str, object],
    stop: threading.Event,
) -> None:
    while not stop.wait(TTL_SECONDS / 3):
        try:
            atlas_request(endpoint, token, "POST", f"/v1/services/{registered['id']}/heartbeat")
        except AtlasError as exc:
            if exc.code == "not_found":
                try:
                    registered.update(register(endpoint, token, address, idempotency_key))
                    print(f"service expired; registered again as {registered['id']}", file=sys.stderr)
                except Exception as register_error:
                    print(f"re-registration failed: {register_error}", file=sys.stderr)
            else:
                print(f"heartbeat failed: {exc}", file=sys.stderr)
        except Exception as exc:
            print(f"heartbeat failed: {exc}", file=sys.stderr)


def serve() -> None:
    endpoint, token = atlas_config()
    address = os.environ.get("SERVICE_ADDRESS", "127.0.0.1:8086")
    try:
        host, port_text = address.rsplit(":", 1)
        port = int(port_text)
        if not host or not 1 <= port <= 65535:
            raise ValueError
    except ValueError as exc:
        raise ValueError(f"SERVICE_ADDRESS must be host:port, got {address!r}") from exc
    listen = os.environ.get("SERVICE_LISTEN_ADDR", f"0.0.0.0:{port}")
    listen_host, listen_port_text = listen.rsplit(":", 1)

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            if self.path == "/healthz":
                self.send_response(204)
                self.end_headers()
                return
            body = json.dumps({"service": SERVICE_NAME, "language": "Python"}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, format: str, *args: object) -> None:
            print(f"service request: {format % args}", file=sys.stderr)

    server = ThreadingHTTPServer((listen_host, int(listen_port_text)), Handler)
    server_thread = threading.Thread(target=server.serve_forever, daemon=True)
    server_thread.start()

    hostname = socket.gethostname()
    idempotency_key = f"{SERVICE_NAME}-{hostname}"
    try:
        registered = register(endpoint, token, address, idempotency_key)
    except Exception:
        server.shutdown()
        server.server_close()
        server_thread.join(timeout=5)
        raise
    stop = threading.Event()
    heartbeat = threading.Thread(
        target=heartbeat_loop,
        args=(endpoint, token, address, idempotency_key, registered, stop),
        daemon=True,
    )
    heartbeat.start()

    def request_shutdown(_signum: int, _frame: object) -> None:
        stop.set()

    signal.signal(signal.SIGINT, request_shutdown)
    signal.signal(signal.SIGTERM, request_shutdown)
    print(f"registered {SERVICE_NAME} at {address} as {registered['id']}", flush=True)
    try:
        while not stop.wait(0.5):
            pass
    finally:
        stop.set()
        heartbeat.join()
        server.shutdown()
        server.server_close()
        server_thread.join(timeout=5)
        try:
            atlas_request(endpoint, token, "DELETE", f"/v1/services/{quote(str(registered['id']), safe='')}")
        except AtlasError as exc:
            if exc.code != "not_found":
                raise
        print(f"deregistered {registered['id']}", flush=True)


def call_service() -> None:
    endpoint, token = atlas_config()
    selected = atlas_request(endpoint, token, "GET", f"/v1/lookup/{quote(SERVICE_NAME, safe='')}")
    address = selected["address"]
    request = Request(f"http://{address}/", headers={"Accept": "application/json"})
    try:
        with urlopen(request, timeout=5) as response:
            print(response.read().decode(), end="")
    except (HTTPError, URLError, TimeoutError) as exc:
        raise RuntimeError(f"call selected instance {address}: {exc}") from exc


if __name__ == "__main__":
    try:
        if len(sys.argv) > 1 and sys.argv[1] == "client":
            call_service()
        else:
            serve()
    except Exception as exc:
        print(f"example failed: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
