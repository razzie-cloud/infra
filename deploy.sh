#!/usr/bin/env bash
# No secret is ever interpolated by this shell, including under bash -x.
set +x
set -euo pipefail
exec python3 "$(dirname "$0")/deploy.py" "$@"
