#!/bin/bash
# Checks Shim 1 against ONDC's OWN signing sample code (not ours), in both directions.
# It needs Shim 1 and the stubs to be running, started by:
#     ./run-shim1-demo.sh --keep-running          (or by hand, see docs/how-to-test-shim1.md)
#
# What it does the first time: shallow-clones ONDC's reference-implementations repository
# and makes a Python virtual environment with ONDC's dependencies, both under .cache/
# (ignored by Git). Needs: git, python3, internet access.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CACHE="${HERE}/.cache"
REPO="${CACHE}/reference-implementations"
mkdir -p "${CACHE}"

if [[ ! -d "${REPO}/.git" ]]; then
  echo "== Cloning ONDC's reference-implementations (one time, shallow)"
  GIT_LFS_SKIP_SMUDGE=1 git clone --depth 1 https://github.com/ONDC-Official/reference-implementations "${REPO}"
fi
if [[ ! -x "${CACHE}/venv/bin/python" ]]; then
  echo "== Making a Python environment with ONDC's dependencies (one time)"
  python3 -m venv "${CACHE}/venv"
  "${CACHE}/venv/bin/pip" install -q pynacl pycryptodomex fire cryptography
fi

export ONDC_PY_DIR="${REPO}/utilities/signing_and_verification/python"
exec "${CACHE}/venv/bin/python" "${HERE}/ondc_referee.py"
