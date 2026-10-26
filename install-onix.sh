#!/bin/bash
# Builds Beckn ONIX natively (no Docker) from the source in ./beckn-onix:
# build server, build plugins, extract schemas.
# Usage: ./install-onix.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${ROOT}/beckn-onix"

go mod download
go build -o server cmd/adapter/main.go
./install/build-plugins.sh
unzip -oq schemas.zip -x '__MACOSX/*'

echo "ONIX is built. To run it:"
echo "  1. Start Redis:  redis-server --daemonize yes"
echo "  2. Point the config at localhost:  sed 's#redis:6379#localhost:6379#' config/local-simple.yaml > /tmp/onix.yaml"
echo "  3. Start ONIX:   cd ${ROOT}/beckn-onix && ./server --config=/tmp/onix.yaml"
echo "  4. Check:        curl http://localhost:8081/health"
