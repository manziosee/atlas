#!/usr/bin/env bash
# Start a single development node for client integration tests and wait until
# it is ready. Used by the client test jobs in CI; also works locally.
#
#   ./scripts/start-test-node.sh [workdir]
#
# Prints the API endpoint and a fresh admin token as shell assignments; in
# GitHub Actions it also exports ATLAS_TEST_ENDPOINT and ATLAS_TEST_TOKEN to
# later steps and masks the token.
set -euo pipefail

work="${1:-${RUNNER_TEMP:-$(mktemp -d)}}"
mkdir -p "$work"
go build -o "$work/atlas-node" ./cmd/atlas-node
go build -o "$work/atlas-cli" ./cmd/atlas-cli

token="$("$work/atlas-cli" tokengen)"
endpoint="http://127.0.0.1:8080"
if [[ -n "${GITHUB_ENV:-}" ]]; then
  echo "::add-mask::$token"
  {
    echo "ATLAS_TEST_TOKEN=$token"
    echo "ATLAS_TEST_ENDPOINT=$endpoint"
  } >> "$GITHUB_ENV"
fi

ATLAS_GOSSIP_KEY="$("$work/atlas-cli" keygen)" \
ATLAS_API_TOKENS="$token" \
ATLAS_NODE_ID=ci-node \
ATLAS_DB_PATH="$work/atlas.db" \
ATLAS_HTTP_ADDR=127.0.0.1:8080 \
ATLAS_GRPC_ADDR=127.0.0.1:8082 \
ATLAS_METRICS_ADDR=127.0.0.1:9090 \
ATLAS_GOSSIP_BIND_ADDR=127.0.0.1:7946 \
ATLAS_RATE_LIMIT_RPS=500 \
ATLAS_RATE_LIMIT_BURST=1000 \
  nohup "$work/atlas-node" > "$work/node.log" 2>&1 &

deadline=$((SECONDS + 60))
until curl -fsS "$endpoint/readyz" >/dev/null 2>&1; do
  if (( SECONDS >= deadline )); then
    cat "$work/node.log" >&2
    exit 1
  fi
  sleep 1
done
echo "ATLAS_TEST_ENDPOINT=$endpoint"
if [[ -z "${GITHUB_ENV:-}" ]]; then
  echo "ATLAS_TEST_TOKEN=$token"
fi
