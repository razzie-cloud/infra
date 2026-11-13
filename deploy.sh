#!/usr/bin/env bash
# No secret is ever interpolated by this shell, including under bash -x.
set +x
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "$0")" && pwd)
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/razcloud-deploy.XXXXXXXX")
trap 'rm -rf -- "$build_dir"' EXIT
go build -o "$build_dir/deploy" "$script_dir"/cmd/infra-helper/{main,render,resources,deploy,pki}.go
"$build_dir/deploy" "$@" &
child=$!
# Wait for Go's bounded resource cleanup before returning from an interruption.
trap 'kill -TERM "$child" 2>/dev/null || true; wait "$child" || true; exit 1' INT TERM HUP
wait "$child"
