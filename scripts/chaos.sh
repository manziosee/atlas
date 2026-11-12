#!/usr/bin/env bash
# Failure and network-impairment tests for the Docker Compose cluster.
#
#   ./scripts/chaos.sh [kill_rounds]
#
# The optional chaos-netem sidecars alone receive NET_ADMIN; Atlas nodes keep
# their normal cap_drop: ALL security posture.
set -euo pipefail

ROUNDS="${1:-1}"
TIMEOUT="${ATLAS_CHAOS_TIMEOUT:-90}"
IMPAIRMENT_SECONDS="${ATLAS_CHAOS_IMPAIRMENT_SECONDS:-12}"
# Every external command below is time-bounded: a hung docker exec or HTTP
# request must fail the run with a message, not stall until the job timeout.
DOCKER_TIMEOUT="${ATLAS_CHAOS_DOCKER_TIMEOUT:-60}"
NODES=(atlas-1 atlas-2 atlas-3)
OBSERVERS=(8081 8082 8083)
# shellcheck disable=SC1091
source .env
export ATLAS_TOKEN

members() { # port
  curl -fsS --max-time 5 -H "Authorization: Bearer $ATLAS_TOKEN" "http://127.0.0.1:$1/v1/members" \
    | grep -o '"state":"[a-z]*"' | sort | uniq -c
}

state_count() { # port state
  local output
  output="$(members "$1" 2>/dev/null || true)"
  awk -v state="\"state\":\"$2\"" '$2 == state { count += $1 } END { print count + 0 }' <<<"$output"
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

assert_no_dead() {
  local unavailable_port="${1:-}" port dead
  for port in "${OBSERVERS[@]}"; do
    [[ "$port" == "$unavailable_port" ]] && continue
    if ! curl -fsS --max-time 5 -H "Authorization: Bearer $ATLAS_TOKEN" "http://127.0.0.1:$port/v1/members" >/dev/null; then
      echo "observer API :$port was unavailable during impairment" >&2
      return 1
    fi
    dead="$(state_count "$port" dead)"
    if (( dead > 0 )); then
      echo "false dead declaration observed via :$port" >&2
      members "$port" >&2 || true
      return 1
    fi
  done
}

wait_for_all_alive() {
  local deadline=$((SECONDS + TIMEOUT)) port
  while (( SECONDS < deadline )); do
    for port in "${OBSERVERS[@]}"; do
      if (( $(state_count "$port" alive) < 3 )); then
        sleep 0.5
        continue 2
      fi
    done
    return 0
  done
  echo "cluster did not converge to three alive members" >&2
  for port in "${OBSERVERS[@]}"; do members "$port" >&2 || true; done
  return 1
}

# wait_for_stable requires every node to report exactly three alive members
# and no suspect, dead or left ones for a continuous window. A single snapshot is not
# enough: right after a partition heals, a node still gossips the "dead" views
# it formed while isolated until each peer refutes them, and starting an
# impairment then would blame that leftover churn on the impairment.
wait_for_stable() { # seconds
  local need="${1:-10}" deadline=$((SECONDS + 2 * TIMEOUT)) since=-1 port ok
  while (( SECONDS < deadline )); do
    ok=1
    for port in "${OBSERVERS[@]}"; do
      if (( $(state_count "$port" alive) != 3 || $(state_count "$port" suspect) != 0 || $(state_count "$port" dead) != 0 || $(state_count "$port" left) != 0 )); then
        ok=0
        break
      fi
    done
    if (( ok )); then
      (( since < 0 )) && since=$SECONDS
      (( SECONDS - since >= need )) && return 0
    else
      since=-1
    fi
    sleep 1
  done
  echo "cluster did not stay converged for ${need}s" >&2
  for port in "${OBSERVERS[@]}"; do members "$port" >&2 || true; done
  return 1
}

# The netem sidecars share their node's network namespace. Restarting a node
# (the kill rounds) gives it a new namespace and strands the old sidecar, so
# recreate them right before applying impairments.
refresh_netem_sidecars() {
  timeout 180 docker compose --profile chaos up -d --force-recreate --no-deps chaos-netem-1 chaos-netem-2 >/dev/null
}

netem() { # node ordinal, tc arguments
  local ordinal="$1"
  shift
  if ! timeout "$DOCKER_TIMEOUT" docker compose --profile chaos exec -T "chaos-netem-$ordinal" tc qdisc "$@" </dev/null; then
    echo "tc qdisc $* on chaos-netem-$ordinal failed or timed out after ${DOCKER_TIMEOUT}s" >&2
    return 1
  fi
}

clear_netem() {
  local ordinal
  for ordinal in 1 2; do
    netem "$ordinal" del dev eth0 root >/dev/null 2>&1 || true
  done
}

wait_for_owner() { # service name, expected node, [extra lookup query]
  local name="$1" expected="$2" extra="${3:-}" deadline=$((SECONDS + TIMEOUT)) owner
  while (( SECONDS < deadline )); do
    assert_no_dead
    owner="$(curl -fsS --max-time 5 -H "Authorization: Bearer $ATLAS_TOKEN" \
      "http://127.0.0.1:8083/v1/lookup/$name?latency=nearest$extra" 2>/dev/null \
      | jq -r '.node_id' 2>/dev/null || true)"
    if [[ "$owner" == "$expected" ]]; then return 0; fi
    sleep 1
  done
  echo "routing did not prefer $expected for $name; last owner was ${owner:-unavailable}" >&2
  return 1
}

partition_container=""
partition_network=""
partition_alias=""
dump_diagnostics() {
  local port
  echo "---- diagnostics" >&2
  timeout 30 docker compose --profile chaos ps -a >&2 || true
  for port in "${OBSERVERS[@]}"; do
    echo "members via :$port" >&2
    members "$port" >&2 || echo "  (no response)" >&2
  done
  timeout 30 docker compose logs --no-color --tail=60 atlas-1 atlas-2 atlas-3 >&2 || true
}

restore_impairments() {
  local status=$?
  if (( status != 0 )); then dump_diagnostics; fi
  clear_netem
  if [[ -n "$partition_container" ]]; then
    docker network connect --alias "$partition_alias" "$partition_network" "$partition_container" >/dev/null 2>&1 || true
  fi
  timeout 60 docker compose --profile chaos stop chaos-netem-1 chaos-netem-2 >/dev/null 2>&1 || true
}
trap restore_impairments EXIT

# --no-deps keeps Compose from recreating the running nodes to attach the
# sidecars. A recreated node leaves gracefully, and until its replacement
# refutes the stale "left" record, peers never probe it, so a kill in round 1
# would never be declared dead.
echo "starting isolated netem sidecars"
timeout 300 docker compose --profile chaos build chaos-netem-1 chaos-netem-2 >/dev/null
timeout 180 docker compose --profile chaos up -d --no-deps chaos-netem-1 chaos-netem-2 >/dev/null
echo "waiting for initial cluster convergence"
wait_for_all_alive
wait_for_stable 10

for ((r = 1; r <= ROUNDS; r++)); do
  victim="${NODES[RANDOM % ${#NODES[@]}]}"
  observer_port=8081
  [[ "$victim" == "atlas-1" ]] && observer_port=8082

  echo "== round $r: killing $victim (observing via :$observer_port)"
  timeout "$DOCKER_TIMEOUT" docker compose kill -s SIGKILL "$victim" >/dev/null
  start=$(date +%s)
  wait_for_count "$observer_port" dead 1
  echo "   failure detected after $(( $(date +%s) - start ))s: $(members "$observer_port")"

  timeout "$DOCKER_TIMEOUT" docker compose start "$victim" >/dev/null
  start=$(date +%s)
  wait_for_all_alive
  echo "   recovery completed after $(( $(date +%s) - start ))s"
  wait_for_stable 10
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
echo "   partition detected after $(( $(date +%s) - start ))s"

docker network connect --alias "$victim" "$network" "$container"
partition_container=""
start=$(date +%s)
wait_for_all_alive
echo "   partition recovery completed after $(( $(date +%s) - start ))s"
wait_for_stable 10
refresh_netem_sidecars

for loss in 10 30; do
  wait_for_stable 10
  echo "== applying ${loss}% packet loss to atlas-1 for ${IMPAIRMENT_SECONDS}s"
  netem 1 replace dev eth0 root netem loss "${loss}%"
  start=$(date +%s)
  deadline=$((SECONDS + IMPAIRMENT_SECONDS))
  while (( SECONDS < deadline )); do
    assert_no_dead 8081
    sleep 1
  done
  impairment_elapsed=$(( $(date +%s) - start ))
  netem 1 del dev eth0 root
  recovery_start=$(date +%s)
  wait_for_all_alive
  echo "   no dead declarations during ${impairment_elapsed}s; recovered to 3 alive in $(( $(date +%s) - recovery_start ))s"
done

echo "== checking latency-aware routing under 200ms peer delay"
curl -fsS --max-time 5 -X POST -H "Authorization: Bearer $ATLAS_TOKEN" -H "Content-Type: application/json" \
  -d '{"id":"chaos-route-1","name":"chaos-route","address":"127.0.0.1:8085"}' \
  http://127.0.0.1:8081/v1/services >/dev/null
curl -fsS --max-time 5 -X POST -H "Authorization: Bearer $ATLAS_TOKEN" -H "Content-Type: application/json" \
  -d '{"id":"chaos-route-2","name":"chaos-route","address":"127.0.0.1:8086"}' \
  http://127.0.0.1:8082/v1/services >/dev/null

netem 2 replace dev eth0 root netem delay 200ms
wait_for_owner chaos-route atlas-1
echo "   baseline selected atlas-1 while atlas-2 was delayed"
netem 2 del dev eth0 root
netem 1 replace dev eth0 root netem delay 200ms
start=$(date +%s)
wait_for_owner chaos-route atlas-2
echo "   routing moved away from slow atlas-1 after $(( $(date +%s) - start ))s"

# Recovery means atlas-1 is eligible again. Its RTT average decays by 1/8 per
# probe, so after 200ms of delay it takes many probes to fall within the 5ms
# default of atlas-2; a 50ms tolerance confirms recovery without waiting for
# the average to settle completely.
start=$(date +%s)
netem 1 del dev eth0 root
wait_for_owner chaos-route atlas-1 "&tolerance=50ms"
echo "   routing recovered to atlas-1 after $(( $(date +%s) - start ))s"
curl -fsS --max-time 5 -X DELETE -H "Authorization: Bearer $ATLAS_TOKEN" http://127.0.0.1:8081/v1/services/chaos-route-1 >/dev/null
curl -fsS --max-time 5 -X DELETE -H "Authorization: Bearer $ATLAS_TOKEN" http://127.0.0.1:8082/v1/services/chaos-route-2 >/dev/null
wait_for_stable 10

echo "== flapping atlas-1 link (one second down, two seconds up; 5 cycles)"
start=$(date +%s)
for _ in {1..5}; do
  netem 1 replace dev eth0 root netem loss 100%
  assert_no_dead 8081
  sleep 1
  netem 1 del dev eth0 root
  sleep 2
done
wait_for_all_alive
assert_no_dead
echo "   cluster settled after flapping stopped; recovery took $(( $(date +%s) - start ))s"
clear_netem
