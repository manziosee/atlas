#!/usr/bin/env bash
# Create a private CA plus server and client certificates for local / staging
# mTLS. Production should use cert-manager, Vault PKI or your platform's CA.
#
#   ./scripts/gen-certs.sh [out_dir] [extra_dns_or_ip ...]
#
# Keys: ECDSA P-256. Validity: CA 365d, leaf 90d (short-lived on purpose).
set -euo pipefail

OUT="${1:-deploy/certs}"
shift || true
EXTRA=("$@")

mkdir -p "$OUT"
cd "$OUT"
umask 077

export MSYS_NO_PATHCONV=1   # Git Bash on Windows: keep "/CN=..." literal
# A stale OPENSSL_CONF (left by some installers) makes every openssl call fail.
if [[ -n "${OPENSSL_CONF:-}" && ! -f "$OPENSSL_CONF" ]]; then unset OPENSSL_CONF; fi

if [[ ! -f ca.key ]]; then
  openssl ecparam -name prime256v1 -genkey -noout -out ca.key
  openssl req -x509 -new -key ca.key -sha256 -days 365 \
    -subj "/O=Atlas/CN=Atlas Local CA" -out ca.crt \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
    -addext "keyUsage=critical,keyCertSign,cRLSign"
  echo "created CA"
fi

san="DNS:localhost,IP:127.0.0.1,IP:::1"
san+=",DNS:atlas-1,DNS:atlas-2,DNS:atlas-3"
san+=",DNS:atlas,DNS:atlas.atlas.svc,DNS:atlas.atlas.svc.cluster.local"
san+=",DNS:*.atlas-headless.atlas.svc.cluster.local"
for e in "${EXTRA[@]:-}"; do
  [[ -z "$e" ]] && continue
  if [[ "$e" =~ ^[0-9.:]+$ ]]; then san+=",IP:$e"; else san+=",DNS:$e"; fi
done

issue() { # name cn eku san
  local name="$1" cn="$2" eku="$3" altnames="${4:-}"
  openssl ecparam -name prime256v1 -genkey -noout -out "$name.key"
  openssl req -new -key "$name.key" -subj "/O=Atlas/CN=$cn" -out "$name.csr"
  {
    echo "basicConstraints=critical,CA:FALSE"
    echo "keyUsage=critical,digitalSignature"
    echo "extendedKeyUsage=$eku"
    [[ -n "$altnames" ]] && echo "subjectAltName=$altnames"
  } > "$name.ext"
  openssl x509 -req -in "$name.csr" -CA ca.crt -CAkey ca.key -CAcreateserial \
    -sha256 -days 90 -extfile "$name.ext" -out "$name.crt"
  rm -f "$name.csr" "$name.ext"
  echo "issued $name.crt ($eku)"
}

issue server atlas-node serverAuth "$san"
issue client atlas-cli  clientAuth

# Containers run as UID 65532 and mount this directory read-only, so the
# server key must be readable; the CA and client keys stay owner-only.
chmod 644 ca.crt server.crt client.crt server.key 2>/dev/null || true
chmod 600 ca.key client.key 2>/dev/null || true
echo "certificates written to $OUT (git-ignored)"
