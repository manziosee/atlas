#!/usr/bin/env bash
# Fill empty [SECRET] values in .env with cryptographically random values.
# Existing non-empty values are never overwritten, so this is safe to re-run.
#
#   ./scripts/gen-secrets.sh [path/to/.env]
set -euo pipefail

ENV_FILE="${1:-.env}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [[ ! -f "$ENV_FILE" ]]; then
  cp "$ROOT/.env.example" "$ENV_FILE"
  echo "created $ENV_FILE from .env.example"
fi

command -v openssl >/dev/null || { echo "openssl is required" >&2; exit 1; }

rand_b64()   { openssl rand -base64 32 | tr -d '\n'; }
rand_token() { openssl rand -base64 36 | tr '+/' '-_' | tr -d '=\n'; }

# set_if_empty KEY VALUE
set_if_empty() {
  local key="$1" value="$2"
  if grep -qE "^${key}=$" "$ENV_FILE"; then
    # '|' delimiter: base64url/base64 values never contain it.
    sed -i.bak "s|^${key}=$|${key}=${value}|" "$ENV_FILE" && rm -f "$ENV_FILE.bak"
    echo "generated $key"
  fi
}

set_if_empty ATLAS_GOSSIP_KEY "$(rand_b64)"
token="$(rand_token)"
set_if_empty ATLAS_API_TOKENS "$token"
# The CLI token defaults to the first API token.
current_token="$(grep -E '^ATLAS_API_TOKENS=' "$ENV_FILE" | cut -d= -f2- | cut -d, -f1)"
set_if_empty ATLAS_TOKEN "$current_token"
set_if_empty GRAFANA_ADMIN_PASSWORD "$(rand_token)"

chmod 600 "$ENV_FILE" 2>/dev/null || true
echo "done — $ENV_FILE is git-ignored; keep it that way."
