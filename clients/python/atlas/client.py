"""Small standard-library-only HTTP client for the Atlas v1 API."""

from __future__ import annotations

import json
import ssl
from typing import Any, TypedDict, cast
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen


class AtlasError(RuntimeError):
    """Atlas rejected a request or could not be reached."""

    def __init__(self, message: str, status: int | None = None) -> None:
        super().__init__(message)
        self.status = status


class ServiceInstance(TypedDict, total=False):
    """A registered Atlas instance returned by registry and lookup APIs."""

    id: str
    name: str
    node_id: str
    address: str
    region: str
    version: str
    tags: list[str]
    health: str
    revision: int
    updated_at: str


class AtlasClient:
    """Client for registration, listing, lookup, retrieval, and deletion.

    Args:
        endpoint: Base URL of one Atlas node, such as ``https://atlas:8080``.
        token: API credential with the scopes required by called operations.
        timeout: Per-request timeout in seconds.
        ca_file: Optional CA bundle for private TLS certificates.
        client_cert: Optional client certificate for mTLS.
        client_key: Private key corresponding to ``client_cert``.
    """

    def __init__(
        self,
        endpoint: str,
        token: str,
        *,
        timeout: float = 5.0,
        ca_file: str | None = None,
        client_cert: str | None = None,
        client_key: str | None = None,
    ) -> None:
        self.endpoint = endpoint.rstrip("/")
        if not self.endpoint.startswith(("http://", "https://")):
            raise ValueError("endpoint must use http:// or https://")
        if client_key and not client_cert:
            raise ValueError("client_key requires client_cert")
        if client_cert and not self.endpoint.startswith("https://"):
            raise ValueError("mTLS client certificates require an https:// endpoint")
        self.token = token
        self.timeout = timeout
        self._ssl_context: ssl.SSLContext | None = None
        if self.endpoint.startswith("https://"):
            self._ssl_context = ssl.create_default_context(cafile=ca_file)
            if client_cert:
                self._ssl_context.load_cert_chain(client_cert, client_key)

    def register(
        self,
        name: str,
        address: str,
        *,
        version: str = "",
        tags: list[str] | None = None,
        health_check: str = "",
    ) -> ServiceInstance:
        """Register an instance owned by the Atlas node receiving the call."""
        payload: dict[str, Any] = {"name": name, "address": address}
        if version:
            payload["version"] = version
        if tags:
            payload["tags"] = tags
        if health_check:
            payload["health_check"] = health_check
        return cast(ServiceInstance, self._request("POST", "/v1/services", payload))

    def list_services(
        self, *, name: str = "", region: str = "", health: str = ""
    ) -> list[ServiceInstance]:
        """List active instances, optionally filtered by name, region, health."""
        query = urlencode({k: v for k, v in {"name": name, "region": region, "health": health}.items() if v})
        path = "/v1/services" + (f"?{query}" if query else "")
        return cast(list[ServiceInstance], self._request("GET", path)["services"])

    def lookup(self, name: str, *, region: str = "", key: str = "") -> ServiceInstance:
        """Select a healthy instance using Atlas routing preferences."""
        query = urlencode({k: v for k, v in {"region": region, "key": key}.items() if v})
        path = f"/v1/lookup/{quote(name, safe='')}" + (f"?{query}" if query else "")
        return cast(ServiceInstance, self._request("GET", path))

    def get(self, service_id: str) -> ServiceInstance:
        """Fetch one service instance by ID."""
        return cast(ServiceInstance, self._request("GET", f"/v1/services/{quote(service_id, safe='')}"))

    def delete(self, service_id: str) -> None:
        """Delete an instance through its owning node."""
        self._request("DELETE", f"/v1/services/{quote(service_id, safe='')}")

    def _request(self, method: str, path: str, body: dict[str, Any] | None = None) -> Any:
        data = json.dumps(body).encode("utf-8") if body is not None else None
        request = Request(
            self.endpoint + path,
            data=data,
            method=method,
            headers={
                "Accept": "application/json",
                "Authorization": f"Bearer {self.token}",
                **({"Content-Type": "application/json"} if data is not None else {}),
            },
        )
        try:
            with urlopen(request, timeout=self.timeout, context=self._ssl_context) as response:
                if response.status == 204:
                    return None
                return json.loads(response.read())
        except HTTPError as exc:
            try:
                payload = json.loads(exc.read())
                message = payload.get("error", exc.reason)
            except (ValueError, AttributeError):
                message = exc.reason
            raise AtlasError(str(message), exc.code) from exc
        except (URLError, TimeoutError, json.JSONDecodeError) as exc:
            raise AtlasError(f"Atlas request failed: {exc}") from exc
