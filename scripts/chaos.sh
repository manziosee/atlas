#!/usr/bin/env bash
# Minimal chaos test for the docker compose cluster: stop a random node,
# watch the survivors declare it dead, restart it, watch it rejoin.
#
#   ./scripts/chaos.sh [rounds]
set -euo pipefail

ROUNDS="${1:-1}"
TIMEOUT="${ATLAS_CHAOS_TIMEOUT:-90}"
NODES=(atlas-1 atlas-2 atlas-3)
# shellcheck disable=SC1091
source .env
export ATLAS_TOKEN

members() { # port
  curl -fsS -H "Authorization: Bearer ${ATLAS_TOKEN}" "http://127.0.0.1:$1/v1/members" \
    | grep -o '"state":"[a-z]*"' | sort | uniq -c | tr '\n' ' '
}

state_count() { # port state
  local output
  output="$(members "$1" 2>/dev/null || true)"
  grep -o "\"state\":\"$2\"" <<<"$output" | wc -l | tr -d ' ' || true
}

wait_for_count() { # port state minimum
  local port="$1" state="$2" minimum="$3" deadline=$((SECONDS + TIMEOUT)) count
  while (( SECONDS < deadline )); do
    count="$(state_count "$port" "$state")"
    if (( count >= minimum )); then return 0; fi
    sleep 0.5
  done
  echo "timed out waiting for $minimum $state members via :$port; observed $count" >&2
  members "$port" >&2 || true
  return 1
}

partition_container=""
partition_network=""
partition_alias=""
restore_partition() {
  if [[ -n "$partition_container" ]]; then
    docker network connect --alias "$partition_alias" "$partition_network" "$partition_container" >/dev/null 2>&1 || true
  fi
}
trap restore_partition EXIT

echo "waiting for initial cluster convergence"
wait_for_count 8081 alive 3

for ((r = 1; r <= ROUNDS; r++)); do
  victim="${NODES[RANDOM % ${#NODES[@]}]}"
  observer_port=8081
  [[ "$victim" == "atlas-1" ]] && observer_port=8082

  echo "== round $r: killing $victim (observing via :$observer_port)"
  docker compose kill -s SIGKILL "$victim" >/dev/null
  start=$(date +%s)
  wait_for_count "$observer_port" dead 1
  echo "   declared dead after $(( $(date +%s) - start ))s: $(members "$observer_port")"

  docker compose start "$victim" >/dev/null
  start=$(date +%s)
  wait_for_count "$observer_port" alive 3
  echo "   rejoined after $(( $(date +%s) - start ))s: $(members "$observer_port")"
done

victim="atlas-3"
observer_port=8081
container="$(docker compose ps -q "$victim")"
if [[ -z "$container" ]]; then
  echo "could not find running container for $victim" >&2
  exit 1
fi
network="$(docker inspect --format '{{range $name, $v := .NetworkSettings.Networks}}{{$name}}{{end}}' "$container")"
if [[ -z "$network" ]]; then
  echo "could not find Compose network for $victim" >&2
  exit 1
fi

echo "== isolating $victim from $network while it continues running"
partition_container="$container"
partition_network="$network"
partition_alias="$victim"
docker network disconnect "$network" "$container"
start=$(date +%s)
wait_for_count "$observer_port" dead 1
echo "   partition detected after $(( $(date +%s) - start ))s: $(members "$observer_port")"

docker network connect --alias "$victim" "$network" "$container"
partition_container=""
start=$(date +%s)
wait_for_count "$observer_port" alive 3
echo "   partition healed after $(( $(date +%s) - start ))s: $(members "$observer_port")"
