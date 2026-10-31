"""Small standard-library-only HTTP client for the Atlas v1 API."""

from __future__ import annotations

import json
import ssl
import time
from collections.abc import Iterator
from typing import Any, TypedDict, cast
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen

_UNSET: Any = object()


class AtlasError(RuntimeError):
    """Atlas rejected a request or could not be reached.

    Attributes:
        status: HTTP status, or ``None`` when the node was unreachable.
        code: Stable error code such as ``not_found``, ``not_owner`` or
            ``precondition_failed``. Branch on this, not on the message.
        request_id: ID of the request in the node's access log.
        owner_node: For ``not_owner``/``owner_alive``, the node that owns
            the service.
    """

    def __init__(
        self,
        message: str,
        status: int | None = None,
        *,
        code: str = "",
        request_id: str = "",
        owner_node: str = "",
    ) -> None:
        super().__init__(message)
        self.status = status
        self.code = code
        self.request_id = request_id
        self.owner_node = owner_node


class ServiceInstance(TypedDict, total=False):
    """A registered Atlas instance returned by registry and lookup APIs."""

    id: str
    name: str
    node_id: str
    address: str
    region: str
    version: str
    tags: list[str]
    health_check: str
    health: str
    revision: int
    ttl_seconds: int
    check_interval_seconds: int
    unhealthy_threshold: int
    healthy_threshold: int
    draining: bool
    updated_at: str
    checked_at: str
    heartbeat_at: str


class Member(TypedDict, total=False):
    """One node as seen by the node that answered."""

    id: str
    addr: str
    region: str
    state: str
    incarnation: int
    since: str


class Event(TypedDict, total=False):
    """A membership event (``member`` set) or registry event (``service`` set).

    Types: ``join``, ``update``, ``reap``, ``service.registered``,
    ``service.updated``, ``service.health``, ``service.deregistered``, and
    ``reset`` (missed events were not retained; re-read state).
    """

    id: str
    type: str
    kind: str
    at: str
    member: Member
    service: ServiceInstance


class HistoryEntry(TypedDict, total=False):
    """One recorded change. ``change`` is ``registered``, ``updated``,
    ``health`` or ``deregistered``; ``service`` is the record after it."""

    seq: int
    service_id: str
    name: str
    revision: int
    change: str
    at: str
    service: ServiceInstance


class HistoryPage(TypedDict):
    """One page of history, newest first; ``next_before`` is 0 on the last."""

    entries: list[HistoryEntry]
    next_before: int


class Page(TypedDict):
    """One page of a service listing; ``next_cursor`` is empty on the last."""

    services: list[ServiceInstance]
    next_cursor: str


class AtlasClient:
    """Client for the Atlas HTTP API.

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

    # --- registry ---------------------------------------------------------

    def register(
        self,
        name: str,
        address: str,
        *,
        version: str = "",
        tags: list[str] | None = None,
        health_check: str = "",
        ttl_seconds: int = 0,
        check_interval_seconds: int = 0,
        unhealthy_threshold: int = 0,
        healthy_threshold: int = 0,
        draining: bool = False,
        service_id: str = "",
        idempotency_key: str = "",
    ) -> ServiceInstance:
        """Register an instance owned by the Atlas node receiving the call.

        Pass ``service_id`` or ``idempotency_key`` to make retries and
        redeploys update the same record instead of creating duplicates.
        Use either ``health_check`` or ``ttl_seconds`` with :meth:`heartbeat`.
        ``health_check`` is an ``http(s)://``, ``tcp://`` or ``grpc(s)://`` URL
        on ``address``; the interval and thresholds tune it (zero uses the
        node defaults: 3 failures to turn unhealthy, 2 successes to recover).
        """
        payload = _spec(
            name, address, id=service_id, version=version, tags=tags, health_check=health_check,
            ttl_seconds=ttl_seconds, check_interval_seconds=check_interval_seconds,
            unhealthy_threshold=unhealthy_threshold, healthy_threshold=healthy_threshold, draining=draining,
        )
        headers = {"Idempotency-Key": idempotency_key} if idempotency_key else None
        return cast(ServiceInstance, self._request("POST", "/v1/services", payload, headers=headers))

    def replace(
        self,
        service_id: str,
        name: str,
        address: str,
        *,
        version: str = "",
        tags: list[str] | None = None,
        health_check: str = "",
        ttl_seconds: int = 0,
        check_interval_seconds: int = 0,
        unhealthy_threshold: int = 0,
        healthy_threshold: int = 0,
        draining: bool = False,
        if_revision: int = 0,
    ) -> ServiceInstance:
        """Create or fully replace the service with ``service_id`` (PUT).

        With ``if_revision`` the call fails with code ``precondition_failed``
        if the service changed since that revision was read.
        """
        payload = _spec(
            name, address, version=version, tags=tags, health_check=health_check, ttl_seconds=ttl_seconds,
            check_interval_seconds=check_interval_seconds, unhealthy_threshold=unhealthy_threshold,
            healthy_threshold=healthy_threshold, draining=draining,
        )
        return cast(
            ServiceInstance,
            self._request("PUT", self._service_path(service_id), payload, headers=_if_match(if_revision)),
        )

    def update(
        self,
        service_id: str,
        *,
        name: str = _UNSET,
        address: str = _UNSET,
        version: str = _UNSET,
        tags: list[str] = _UNSET,
        health_check: str = _UNSET,
        ttl_seconds: int = _UNSET,
        check_interval_seconds: int = _UNSET,
        unhealthy_threshold: int = _UNSET,
        healthy_threshold: int = _UNSET,
        draining: bool = _UNSET,
        if_revision: int = 0,
    ) -> ServiceInstance:
        """Change only the given fields (PATCH). Any node accepts this; a node
        that does not own the service forwards it to the owner."""
        patch = {
            key: value
            for key, value in {
                "name": name,
                "address": address,
                "version": version,
                "tags": tags,
                "health_check": health_check,
                "ttl_seconds": ttl_seconds,
                "check_interval_seconds": check_interval_seconds,
                "unhealthy_threshold": unhealthy_threshold,
                "healthy_threshold": healthy_threshold,
                "draining": draining,
            }.items()
            if value is not _UNSET
        }
        return cast(
            ServiceInstance,
            self._request("PATCH", self._service_path(service_id), patch, headers=_if_match(if_revision)),
        )

    def drain(self, service_id: str) -> ServiceInstance:
        """Stop new traffic to an instance while it stays registered, for
        example before a deploy. Lookups, DNS and client balancers skip it."""
        return self.update(service_id, draining=True)

    def undrain(self, service_id: str) -> ServiceInstance:
        """Return a drained instance to rotation."""
        return self.update(service_id, draining=False)

    def heartbeat(self, service_id: str) -> ServiceInstance:
        """Refresh a TTL service. ``AtlasError.code == "not_found"`` means it
        expired; register it again."""
        return cast(ServiceInstance, self._request("POST", self._service_path(service_id) + "/heartbeat"))

    def list_page(
        self,
        *,
        name: str = "",
        region: str = "",
        health: str = "",
        version: str = "",
        node_id: str = "",
        tags: list[str] | None = None,
        cursor: str = "",
        limit: int = 0,
    ) -> Page:
        """Return one page of services; pass ``next_cursor`` to continue."""
        params: list[tuple[str, str]] = [
            (key, value)
            for key, value in (
                ("name", name),
                ("region", region),
                ("health", health),
                ("version", version),
                ("node_id", node_id),
                ("cursor", cursor),
            )
            if value
        ]
        params += [("tag", tag) for tag in tags or []]
        if limit:
            params.append(("limit", str(limit)))
        query = urlencode(params)
        body = self._request("GET", "/v1/services" + (f"?{query}" if query else ""))
        return {"services": body.get("services", []), "next_cursor": body.get("next_cursor", "")}

    def iter_services(self, **filters: Any) -> Iterator[ServiceInstance]:
        """Yield every matching service, fetching pages as needed."""
        cursor = ""
        while True:
            page = self.list_page(cursor=cursor, limit=1000, **filters)
            yield from page["services"]
            cursor = page["next_cursor"]
            if not cursor:
                return

    def list_services(
        self,
        *,
        name: str = "",
        region: str = "",
        health: str = "",
        version: str = "",
        node_id: str = "",
        tags: list[str] | None = None,
    ) -> list[ServiceInstance]:
        """List all active instances matching the filters (every page)."""
        return list(
            self.iter_services(name=name, region=region, health=health, version=version, node_id=node_id, tags=tags)
        )

    def lookup(
        self,
        name: str,
        *,
        region: str = "",
        key: str = "",
        tags: list[str] | None = None,
        stale: str = "",
        latency: str = "",
        tolerance: str = "",
    ) -> ServiceInstance:
        """Select a healthy instance using Atlas routing preferences.

        ``stale`` (``include``, ``prefer-live`` or ``exclude`` instances whose
        owning node is not alive), ``latency`` (``nearest`` or ``off``) and
        ``tolerance`` (a duration such as ``"10ms"``) override the node's
        routing policy for this lookup.
        """
        params = [
            (k, v)
            for k, v in (("region", region), ("key", key), ("stale", stale), ("latency", latency), ("tolerance", tolerance))
            if v
        ]
        params += [("tag", tag) for tag in tags or []]
        query = urlencode(params)
        path = f"/v1/lookup/{quote(name, safe='')}" + (f"?{query}" if query else "")
        return cast(ServiceInstance, self._request("GET", path))

    def get(self, service_id: str) -> ServiceInstance:
        """Fetch one service instance by ID."""
        return cast(ServiceInstance, self._request("GET", self._service_path(service_id)))

    def delete(self, service_id: str) -> None:
        """Deregister an instance. Any node accepts this once the owner has
        left the cluster; otherwise call the owner or use an admin token."""
        self._request("DELETE", self._service_path(service_id))

    def history(self, service_id: str, *, before: int = 0, limit: int = 0) -> HistoryPage:
        """Return recorded changes to one service, newest first, including
        after it was deregistered. Pass ``next_before`` to continue."""
        return self._history(f"{self._service_path(service_id)}/history", [], before, limit)

    def recent_history(self, *, name: str = "", before: int = 0, limit: int = 0) -> HistoryPage:
        """Return recent changes across services, or for one service name."""
        return self._history("/v1/history", [("name", name)] if name else [], before, limit)

    def iter_history(self, service_id: str) -> Iterator[HistoryEntry]:
        """Yield every recorded change to one service, newest first."""
        before = 0
        while True:
            page = self.history(service_id, before=before, limit=500)
            yield from page["entries"]
            before = page["next_before"]
            if not before:
                return

    def _history(self, path: str, params: list[tuple[str, str]], before: int, limit: int) -> HistoryPage:
        if before:
            params.append(("before", str(before)))
        if limit:
            params.append(("limit", str(limit)))
        query = urlencode(params)
        body = self._request("GET", path + (f"?{query}" if query else ""))
        return {"entries": body.get("entries", []), "next_before": body.get("next_before", 0)}

    # --- cluster ------------------------------------------------------------

    def node(self) -> dict[str, Any]:
        """Return the answering node's record, version and commit."""
        return cast(dict[str, Any], self._request("GET", "/v1/node"))

    def members(self) -> list[Member]:
        """Return the answering node's view of the cluster."""
        return cast(list[Member], self._request("GET", "/v1/members")["members"])

    def watch(
        self,
        *,
        kinds: list[str] | None = None,
        service: str = "",
        last_event_id: str = "",
        reconnect: bool = True,
    ) -> Iterator[Event]:
        """Yield membership and registry events as they happen.

        The generator reconnects after a dropped connection and resumes from
        the last event it yielded. Stop iterating (or close the generator) to
        end the stream. A ``reset`` event means missed events could not be
        replayed: re-read current state.
        """
        backoff = 0.5
        while True:
            delivered = False
            try:
                for event in self._stream(kinds or [], service, last_event_id):
                    delivered = True
                    if event.get("id"):
                        last_event_id = event["id"]
                    yield event
            except AtlasError as exc:
                if exc.status is not None or not reconnect:
                    raise
            if not reconnect:
                return
            backoff = 0.5 if delivered else min(backoff * 2, 30.0)
            time.sleep(backoff)

    # --- admin (admin scope) ---------------------------------------------

    def credentials(self) -> list[dict[str, Any]]:
        """List configured credentials by fingerprint, scopes and expiry."""
        return cast(list[dict[str, Any]], self._request("GET", "/v1/admin/credentials")["credentials"])

    def backup(self, path: str) -> int:
        """Save a consistent SQLite snapshot of the node's registry to ``path``.

        Returns the number of bytes written. The file must not exist.
        """
        request = self._build("POST", "/v1/admin/backup", None, {"Accept": "application/vnd.sqlite3"})
        written = 0
        try:
            with urlopen(request, timeout=max(self.timeout, 300), context=self._ssl_context) as response:
                with open(path, "xb") as out:
                    while chunk := response.read(64 * 1024):
                        out.write(chunk)
                        written += len(chunk)
        except HTTPError as exc:
            raise _error_from(exc) from exc
        except (URLError, TimeoutError) as exc:
            raise AtlasError(f"Atlas request failed: {exc}") from exc
        return written

    def evict_node(self, node_id: str) -> dict[str, Any]:
        """Remove a failed node from membership and delete its services."""
        return cast(dict[str, Any], self._request("DELETE", f"/v1/admin/nodes/{quote(node_id, safe='')}"))

    # --- transport ----------------------------------------------------------

    @staticmethod
    def _service_path(service_id: str) -> str:
        return f"/v1/services/{quote(service_id, safe='')}"

    def _build(self, method: str, path: str, body: Any, headers: dict[str, str] | None) -> Request:
        data = json.dumps(body).encode("utf-8") if body is not None else None
        merged = {"Accept": "application/json", "Authorization": f"Bearer {self.token}"}
        if data is not None:
            merged["Content-Type"] = "application/json"
        merged.update(headers or {})
        return Request(self.endpoint + path, data=data, method=method, headers=merged)

    def _request(
        self,
        method: str,
        path: str,
        body: dict[str, Any] | None = None,
        *,
        headers: dict[str, str] | None = None,
    ) -> Any:
        request = self._build(method, path, body, headers)
        try:
            with urlopen(request, timeout=self.timeout, context=self._ssl_context) as response:
                if response.status == 204:
                    return None
                return json.loads(response.read())
        except HTTPError as exc:
            raise _error_from(exc) from exc
        except (URLError, TimeoutError, json.JSONDecodeError) as exc:
            raise AtlasError(f"Atlas request failed: {exc}") from exc

    def _stream(self, kinds: list[str], service: str, last_event_id: str) -> Iterator[Event]:
        params = [(k, v) for k, v in (("kinds", ",".join(kinds)), ("service", service)) if v]
        query = urlencode(params)
        headers = {"Accept": "text/event-stream"}
        if last_event_id:
            headers["Last-Event-ID"] = last_event_id
        request = self._build("GET", "/v1/events" + (f"?{query}" if query else ""), None, headers)
        try:
            # The server sends a keepalive every 15 seconds; a longer silence
            # means the connection is gone.
            with urlopen(request, timeout=max(self.timeout, 45), context=self._ssl_context) as response:
                event_type = ""
                for raw in response:
                    line = raw.decode("utf-8").rstrip("\r\n")
                    if line.startswith("event: "):
                        event_type = line[len("event: ") :]
                    elif line.startswith("data: "):
                        event = cast(Event, json.loads(line[len("data: ") :]))
                        event.setdefault("type", event_type)
                        yield event
                    elif not line:
                        event_type = ""
        except HTTPError as exc:
            raise _error_from(exc) from exc
        except (URLError, TimeoutError, OSError, json.JSONDecodeError) as exc:
            raise AtlasError(f"Atlas event stream failed: {exc}") from exc


def _spec(name: str, address: str, **fields: Any) -> dict[str, Any]:
    """Build a registration body, omitting unset (zero) optional fields."""
    payload: dict[str, Any] = {"name": name, "address": address}
    payload.update({key: value for key, value in fields.items() if value})
    return payload


def _if_match(revision: int) -> dict[str, str] | None:
    return {"If-Match": f'"{revision}"'} if revision else None


def _error_from(exc: HTTPError) -> AtlasError:
    try:
        payload = json.loads(exc.read())
        return AtlasError(
            str(payload.get("error", exc.reason)),
            exc.code,
            code=str(payload.get("code", "")),
            request_id=str(payload.get("request_id", "")),
            owner_node=str(payload.get("owner_node", "")),
        )
    except (ValueError, AttributeError):
        return AtlasError(str(exc.reason), exc.code)
