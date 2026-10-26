#!/bin/bash
# Installs Beckn ONIX natively (no Docker): clone, build server, build plugins, extract schemas.
# Usage: ./install-onix.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DIR="${ROOT}/beckn-onix"

if [ ! -d "${DIR}/.git" ]; then
    git clone https://github.com/beckn/beckn-onix.git "${DIR}"
fi

cd "${DIR}"
go mod download
go build -o server cmd/adapter/main.go
./install/build-plugins.sh
unzip -oq schemas.zip

echo "ONIX is built. To run it:"
echo "  1. Start Redis:  redis-server --daemonize yes"
echo "  2. Point the config at localhost:  sed 's#redis:6379#localhost:6379#' config/local-simple.yaml > /tmp/onix.yaml"
echo "  3. Start ONIX:   cd ${DIR} && ./server --config=/tmp/onix.yaml"
echo "  4. Check:        curl http://localhost:8081/health"
