#!/bin/bash
# Runs the Shim 1 demo: a signed ONDC /search arrives, Shim 1 (ONIX) checks it,
# a stub seller backend handles it, and a signed on_search goes back to a stub
# buyer app. Then 16 wrong requests are sent and each must be rejected.
#
# Usage: ./run-shim1-demo.sh [--skip-build]
#   --skip-build   reuse the server and plugins built earlier by install-onix.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOGS="$(mktemp -d)"
PIDS=()

cleanup() {
  for pid in "${PIDS[@]:-}"; do kill "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT

if [[ "${1:-}" != "--skip-build" ]]; then
  echo "== Building ONIX, plugins and the demo program (first run takes a few minutes)"
  "${ROOT}/install-onix.sh" >"${LOGS}/build.log" 2>&1 || { tail -20 "${LOGS}/build.log"; exit 1; }
fi

cd "${ROOT}/beckn-onix"
go build -o shim1demo ./cmd/shim1demo

for port in 8081 9001 9002; do
  if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
    echo "Port ${port} is already in use. Stop whatever uses it and try again."; exit 1
  fi
done

echo "== Making demo keys, the stub registry and the Shim 1 config"
./shim1demo keys

echo "== Starting the stub seller backend (9001), stub buyer app (9002) and Shim 1 (8081)"
./shim1demo backend >"${LOGS}/backend.log" 2>&1 & PIDS+=($!)
./shim1demo bap     >"${LOGS}/bap.log"     2>&1 & PIDS+=($!)
./server --config=config/shim1/generated/shim1.yaml >"${LOGS}/shim1.log" 2>&1 & PIDS+=($!)

for _ in $(seq 1 50); do
  curl -fsS http://localhost:8081/health >/dev/null 2>&1 && break
  sleep 0.2
done
curl -fsS http://localhost:8081/health >/dev/null || { echo "Shim 1 did not start:"; tail -20 "${LOGS}/shim1.log"; exit 1; }

echo "== Running the scenarios"
status=0
./shim1demo client || status=$?

echo
echo "== What the stubs saw for the valid /search"
grep -E "seller backend\] (got|sent)|buyer app\] got" "${LOGS}"/backend.log "${LOGS}"/bap.log | sed "s#${LOGS}/##" | head -6 || true
echo
echo "Logs are in ${LOGS}"
exit "${status}"
