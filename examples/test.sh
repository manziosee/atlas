#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
WORK="$(mktemp -d)"
ATLAS_ADDR="${ATLAS_ADDR:-${ATLAS_TEST_ENDPOINT:-http://127.0.0.1:8080}}"
ATLAS_TOKEN="${ATLAS_TOKEN:-${ATLAS_TEST_TOKEN:-}}"
export ATLAS_ADDR ATLAS_TOKEN

if [[ -z "$ATLAS_TOKEN" ]]; then
  echo "ATLAS_TOKEN or ATLAS_TEST_TOKEN must contain an Atlas API token" >&2
  exit 1
fi

service_pid=""
cleanup() {
  if [[ -n "$service_pid" ]] && kill -0 "$service_pid" 2>/dev/null; then
    kill -TERM "$service_pid" 2>/dev/null || true
    wait "$service_pid" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

wait_for_service() {
  local name="$1" deadline=$((SECONDS + 30))
  until curl -fsS -H "Authorization: Bearer $ATLAS_TOKEN" "$ATLAS_ADDR/v1/lookup/$name" >/dev/null 2>&1; do
    if (( SECONDS >= deadline )); then
      echo "service $name did not register" >&2
      return 1
    fi
    sleep 0.5
  done
}

verify_call() {
  local expected="$1"
  if ! python3 -c 'import json,sys; assert json.load(sys.stdin)["language"] == sys.argv[1]' "$expected"; then
    echo "lookup client did not call the $expected example service" >&2
    return 1
  fi
}

force_expire() {
  local name="$1" id
  id="$(curl -fsS -H "Authorization: Bearer $ATLAS_TOKEN" "$ATLAS_ADDR/v1/lookup/$name" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
  curl -fsS -X DELETE -H "Authorization: Bearer $ATLAS_TOKEN" "$ATLAS_ADDR/v1/services/$id" >/dev/null
  wait_for_service "$name"
}

stop_and_verify_deregistered() {
  local name="$1" deadline=$((SECONDS + 10))
  kill -TERM "$service_pid"
  wait "$service_pid"
  service_pid=""
  while (( SECONDS < deadline )); do
    count="$(curl -fsS -H "Authorization: Bearer $ATLAS_TOKEN" "$ATLAS_ADDR/v1/services?name=$name" \
      | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["services"]))')"
    if [[ "$count" == "0" ]]; then
      echo "$name cleanly deregistered"
      return 0
    fi
    sleep 0.5
  done
  echo "$name remained registered after shutdown" >&2
  return 1
}

go build -o "$WORK/example-go" "$ROOT/examples/go"
python3 -m py_compile "$ROOT/examples/python/service.py"

echo "starting Go example"
"$WORK/example-go" >"$WORK/go.log" 2>&1 &
service_pid=$!
wait_for_service example-go
"$WORK/example-go" client | verify_call Go
force_expire example-go
stop_and_verify_deregistered example-go

echo "starting Python example"
python3 "$ROOT/examples/python/service.py" >"$WORK/python.log" 2>&1 &
service_pid=$!
wait_for_service example-python
python3 "$ROOT/examples/python/service.py" client | verify_call Python
force_expire example-python
stop_and_verify_deregistered example-python

if ! command -v dotnet >/dev/null 2>&1; then
  if [[ "${ATLAS_EXAMPLES_REQUIRE_DOTNET:-}" == "1" ]]; then
    echo "dotnet is required for the C# example" >&2
    exit 1
  fi
  echo "skipping C# example: dotnet is not installed"
  exit 0
fi
dotnet build "$ROOT/examples/csharp" -c Release -o "$WORK/example-csharp" -nologo -v quiet

echo "starting C# example"
dotnet "$WORK/example-csharp/example-csharp.dll" >"$WORK/csharp.log" 2>&1 &
service_pid=$!
wait_for_service example-csharp
dotnet "$WORK/example-csharp/example-csharp.dll" client | verify_call 'C#'
force_expire example-csharp
stop_and_verify_deregistered example-csharp
