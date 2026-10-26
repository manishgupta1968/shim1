#!/bin/bash
# Runs the Shim 1 demo and its checks:
#   1. a signed ONDC /search arrives, Shim 1 (ONIX) checks it, a stub seller backend handles
#      it, and a signed on_search goes back to a stub buyer app
#   2. 25 more requests, most of them wrong in some way; each must get the right answer
#   3. four sample payloads (one is ONDC's own sample file)
#   4. optional: ONDC's own sample code checks our signatures (--with-referee)
#
# Usage: ./run-shim1-demo.sh [options]
#   --skip-build            reuse the server and plugins built earlier
#   --registry-mode MODE    stub (default): registry is a JSON file
#                           http: registry is a stub HTTP server that only answers signed lookups
#   --with-referee          also run tests/ondc-referee (needs internet the first time)
#   --keep-running          leave Shim 1 and the stubs running, so you can try things by hand
#
# Exit status is 0 only when every check passed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOGS="$(mktemp -d)"
PIDS=()
SKIP_BUILD=0; MODE=stub; REFEREE=0; KEEP=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-build) SKIP_BUILD=1 ;;
    --registry-mode) MODE="${2:-}"; shift ;;
    --with-referee) REFEREE=1 ;;
    --keep-running) KEEP=1 ;;
    -h|--help) sed -n '2,17p' "$0"; exit 0 ;;
    *) echo "Unknown option: $1 (try --help)"; exit 2 ;;
  esac
  shift
done
[[ "${MODE}" == "stub" || "${MODE}" == "http" ]] || { echo "--registry-mode must be stub or http"; exit 2; }

cleanup() {
  [[ "${KEEP}" == "1" ]] && return
  for pid in "${PIDS[@]:-}"; do kill "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT

if [[ "${SKIP_BUILD}" == "0" ]]; then
  echo "== Building ONIX, plugins and the demo program (first run takes a few minutes)"
  "${ROOT}/install-onix.sh" >"${LOGS}/build.log" 2>&1 || { tail -20 "${LOGS}/build.log"; exit 1; }
fi

cd "${ROOT}/beckn-onix"
go build -o shim1demo ./cmd/shim1demo

PORTS=(8081 9001 9002); [[ "${MODE}" == "http" ]] && PORTS+=(9003)
for port in "${PORTS[@]}"; do
  if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
    echo "Port ${port} is already in use. Stop whatever uses it and try again."; exit 1
  fi
done

echo "== Making demo keys (ONDC's 64-byte key form), the registry entries and the Shim 1 config [registry: ${MODE}]"
./shim1demo keys -keyformat ondc -registry "${MODE}"

echo "== Starting the stubs and Shim 1"
if [[ "${MODE}" == "http" ]]; then
  ./shim1demo registry >"${LOGS}/registry.log" 2>&1 & PIDS+=($!)
fi
./shim1demo backend >"${LOGS}/backend.log" 2>&1 & PIDS+=($!)
./shim1demo bap     >"${LOGS}/bap.log"     2>&1 & PIDS+=($!)
./server --config=config/shim1/generated/shim1.yaml >"${LOGS}/shim1.log" 2>&1 & PIDS+=($!)

for _ in $(seq 1 120); do
  curl -fsS http://localhost:8081/health >/dev/null 2>&1 && break
  sleep 0.5
done
curl -fsS http://localhost:8081/health >/dev/null || { echo "Shim 1 did not start:"; tail -20 "${LOGS}/shim1.log"; exit 1; }

FAILS=0

echo
echo "== 1. Scenarios"
./shim1demo client || FAILS=$((FAILS+1))

echo
echo "== 2. Sample payloads (see beckn-onix/config/shim1/samples/README.md)"
S=config/shim1/samples
run_sample() {  # file expectation
  if out=$(./shim1demo client --payload "$S/$1" --expect "$2" 2>&1); then
    printf 'PASS  %-46s expected %s\n' "$1" "$2"
  else
    printf 'FAIL  %-46s expected %s\n%s\n' "$1" "$2" "$out"; FAILS=$((FAILS+1))
  fi
}
run_sample 01-ondc-official-signing-sample.json nack
run_sample 02-ondc-sample-adapted-to-RET10.json ack
run_sample 03-typical-retail-1.2.0-search.json ack
run_sample 04-ondc-2.0.1-location-style-search.json nack

if [[ "${MODE}" == "http" ]]; then
  echo
  echo "== 3. Did Shim 1 really use the HTTP registry, with signed lookups?"
  STATS="$(curl -fsS http://localhost:9003/registry/stats)"
  num() { echo "${STATS}" | grep -o "\"$1\":[0-9]*" | cut -d: -f2; }
  echo "   lookups=$(num lookups) signed_ok=$(num signed_ok) refused_unsigned=$(num refused_unsigned) refused_bad_signature=$(num refused_bad_signature)"
  if [[ "$(num lookups)" -gt 0 && "$(num signed_ok)" == "$(num lookups)" ]]; then
    echo "PASS  every registry lookup was signed and accepted"
  else
    echo "FAIL  expected lookups > 0 and all of them signed and accepted"; FAILS=$((FAILS+1))
  fi
fi

if [[ "${REFEREE}" == "1" ]]; then
  echo
  echo "== 4. ONDC's own sample code as referee"
  "${ROOT}/tests/ondc-referee/run.sh" || FAILS=$((FAILS+1))
fi

echo
if [[ "${FAILS}" == "0" ]]; then
  echo "RESULT: ALL CHECKS PASSED"
else
  echo "RESULT: ${FAILS} PART(S) FAILED"
fi
echo "Logs: ${LOGS}"
if [[ "${KEEP}" == "1" ]]; then
  echo "Shim 1 (8081), seller backend (9001), buyer app (9002) are still running. Stop them with:"
  echo "  pkill -x server; pkill -x shim1demo"
fi
exit "${FAILS}"
