#!/usr/bin/env bash
# Minimal chaos test for the docker compose cluster: stop a random node,
# watch the survivors declare it dead, restart it, watch it rejoin.
#
#   ./scripts/chaos.sh [rounds]
set -euo pipefail

ROUNDS="${1:-1}"
NODES=(atlas-1 atlas-2 atlas-3)
# shellcheck disable=SC1091
source .env
export ATLAS_TOKEN

members() { # port
  curl -fsS -H "Authorization: Bearer ${ATLAS_TOKEN}" "http://127.0.0.1:$1/v1/members" \
    | grep -o '"state":"[a-z]*"' | sort | uniq -c | tr '\n' ' '
}

for ((r = 1; r <= ROUNDS; r++)); do
  victim="${NODES[RANDOM % ${#NODES[@]}]}"
  observer_port=8081
  [[ "$victim" == "atlas-1" ]] && observer_port=8082

  echo "== round $r: killing $victim (observing via :$observer_port)"
  docker compose kill -s SIGKILL "$victim" >/dev/null
  start=$(date +%s)
  until members "$observer_port" | grep -q '"state":"dead"'; do sleep 0.5; done
  echo "   declared dead after $(( $(date +%s) - start ))s: $(members "$observer_port")"

  docker compose start "$victim" >/dev/null
  start=$(date +%s)
  until ! members "$observer_port" | grep -qE '"state":"(dead|suspect)"'; do sleep 0.5; done
  echo "   rejoined after $(( $(date +%s) - start ))s: $(members "$observer_port")"
done
