#!/usr/bin/env bash
set -euo pipefail

# Remote context & stack name
DOCKER_CONTEXT="${DOCKER_CONTEXT:-razcloud}"
STACK_NAME="${STACK_NAME:-razzie-cloud}"

# Simple idempotent secret helper
ensure_secret() {
  local name="$1"

  if docker --context "$DOCKER_CONTEXT" secret inspect "$name" >/dev/null 2>&1; then
    echo "Secret '$name' already exists, keeping it."
    return
  fi

  echo "Creating secret '$name'..."
  # 32 random bytes, base64-ish
  tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32 | \
    docker --context "$DOCKER_CONTEXT" secret create "$name" -
}

ensure_secret postgres_password
ensure_secret dragonfly_password
ensure_secret hanko_secret_key
ensure_secret oauth2_proxy_cookie_secret
ensure_secret oauth2_proxy_client_secret

echo "Deploying stack '$STACK_NAME' to context '$DOCKER_CONTEXT'..."
docker --context "$DOCKER_CONTEXT" stack deploy -c docker-stack.yml "$STACK_NAME"
