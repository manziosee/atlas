"use strict";

const form = document.querySelector("#connect-form");
const tokenInput = document.querySelector("#token");
const errorBox = document.querySelector("#error");
const connectionDot = document.querySelector("#connection-dot");
const connectionLabel = document.querySelector("#connection-label");
const eventList = document.querySelector("#events");
let token = "";
let refreshTimer;
let streamController;
let lastEventId = "";

function cell(row, value, className) {
  const td = document.createElement("td");
  td.textContent = value ?? "—";
  if (className) td.className = className;
  row.append(td);
  return td;
}

function emptyRow(body, columns, text) {
  body.replaceChildren();
  const row = document.createElement("tr");
  const td = cell(row, text, "empty");
  td.colSpan = columns;
  body.append(row);
}

async function api(path) {
  const response = await fetch(path, { headers: { Authorization: `Bearer ${token}` }, cache: "no-store" });
  if (!response.ok) {
    if (response.status === 401) throw new Error("Token was rejected. Check the API token and reconnect.");
    const body = await response.json().catch(() => ({}));
    throw new Error(body.error ? `${body.error} (${body.code})` : `${path} returned HTTP ${response.status}.`);
  }
  return response.json();
}

// Service listings are paged; follow next_cursor to show every instance.
async function allServices() {
  const services = [];
  let cursor = "";
  do {
    const page = await api(`/v1/services?limit=1000${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`);
    services.push(...(page.services || []));
    cursor = page.next_cursor || "";
  } while (cursor);
  return services;
}

function renderMembers(members) {
  const body = document.querySelector("#members");
  body.replaceChildren();
  for (const member of members) {
    const row = document.createElement("tr");
    cell(row, member.id, "node-name");
    cell(row, member.region || "—");
    cell(row, member.addr, "address");
    cell(row, member.state, `state ${member.state}`);
    cell(row, member.incarnation);
    body.append(row);
  }
  if (!members.length) emptyRow(body, 5, "No nodes are currently visible.");
  document.querySelector("#member-count").textContent = members.length;
  document.querySelector("#stat-total").textContent = members.length;
  document.querySelector("#stat-alive").textContent = members.filter((m) => m.state === "alive").length;
  document.querySelector("#stat-attention").textContent = members.filter((m) => m.state === "suspect" || m.state === "dead").length;
}

function renderServices(services) {
  const body = document.querySelector("#services");
  body.replaceChildren();
  for (const service of services) {
    const row = document.createElement("tr");
    cell(row, service.name, "service-name");
    cell(row, service.region || "—");
    cell(row, service.address, "address");
    cell(row, service.health, `health ${service.health}`);
    body.append(row);
  }
  if (!services.length) emptyRow(body, 4, "No service instances are registered.");
  document.querySelector("#service-count").textContent = services.length;
  document.querySelector("#stat-services").textContent = services.length;
}

async function refresh() {
  const [membership, services] = await Promise.all([api("/v1/members"), allServices()]);
  renderMembers(membership.members || []);
  renderServices(services);
  document.querySelector("#last-update").textContent = `Updated ${new Date().toLocaleTimeString()}`;
  errorBox.hidden = true;
}

function addEvent(event) {
  const placeholder = eventList.querySelector(".empty");
  if (placeholder) eventList.replaceChildren();
  const row = document.createElement("li");
  const date = new Date(event.at);
  const time = document.createElement("span");
  time.className = "event-time";
  time.textContent = date.toLocaleTimeString();
  const type = document.createElement("span");
  type.className = "event-type";
  type.textContent = event.type;
  const detail = document.createElement("span");
  detail.className = "event-detail";
  const name = document.createElement("em");
  name.textContent = describeSubject(event);
  detail.append(name, document.createTextNode(` ${describeChange(event)}`));
  row.append(time, type, detail);
  eventList.prepend(row);
  while (eventList.children.length > 30) eventList.lastElementChild.remove();
  scheduleRefresh();
}

// Coalesce bursts of events into one reload.
let pendingRefresh;
function scheduleRefresh() {
  clearTimeout(pendingRefresh);
  pendingRefresh = setTimeout(() => refresh().catch(showError), 500);
}

function describeSubject(event) {
  if (event.service) return `${event.service.name} ${event.service.address}`;
  if (event.member) return event.member.id;
  return "cluster";
}

function describeChange(event) {
  switch (event.type) {
    case "join": return `joined · ${event.member?.region || "unknown region"}`;
    case "reap": return "left the view";
    case "update": return `changed state to ${event.member?.state}`;
    case "service.registered": return `registered on ${event.service?.node_id}`;
    case "service.updated": return "was updated";
    case "service.health": return `is now ${event.service?.health}`;
    case "service.deregistered": return "was deregistered";
    case "reset": return "missed events were not retained; state reloaded";
    default: return event.type;
  }
}

// streamEvents reads the SSE stream and reconnects with Last-Event-ID so no
// change is missed across brief disconnects.
async function streamEvents() {
  streamController = new AbortController();
  const signal = streamController.signal;
  const headers = { Authorization: `Bearer ${token}`, Accept: "text/event-stream" };
  if (lastEventId) headers["Last-Event-ID"] = lastEventId;
  try {
    const response = await fetch("/v1/events", { headers, signal });
    if (!response.ok || !response.body) throw new Error(`Event stream returned HTTP ${response.status}.`);
    connectionDot.classList.add("connected");
    connectionLabel.textContent = "Connected";
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let pending = "";
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      pending += decoder.decode(value, { stream: true });
      const lines = pending.split("\n");
      pending = lines.pop() || "";
      for (const line of lines) {
        if (line.startsWith("id: ")) lastEventId = line.slice(4);
        if (line.startsWith("data: ")) {
          try { addEvent(JSON.parse(line.slice(6))); } catch { /* Ignore malformed event data. */ }
        }
      }
    }
  } catch (error) {
    if (error.name !== "AbortError") showError(error);
  } finally {
    connectionDot.classList.remove("connected");
    connectionLabel.textContent = "Disconnected";
  }
  if (!signal.aborted) setTimeout(() => { if (!signal.aborted) streamEvents(); }, 3000);
}

function showError(error) {
  errorBox.textContent = error.message || "Could not connect to the Atlas node.";
  errorBox.hidden = false;
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  token = tokenInput.value.trim();
  lastEventId = "";
  if (streamController) streamController.abort();
  clearInterval(refreshTimer);
  try {
    await refresh();
    refreshTimer = setInterval(() => refresh().catch(showError), 10000);
    streamEvents();
  } catch (error) {
    connectionLabel.textContent = "Disconnected";
    showError(error);
  }
});
