#!/usr/bin/env bash
set -euo pipefail

# Remote context & stack name
DOCKER_CONTEXT="${DOCKER_CONTEXT:-razcloud}"
export STACK_NAME="${STACK_NAME:-razzie-cloud}"

# Simple idempotent secret helper
ensure_secret() {
  local name="$1"

  if docker --context "$DOCKER_CONTEXT" secret inspect "$name" >/dev/null 2>&1; then
    echo "Secret '$name' already exists, keeping it."
    return
  fi

  echo "Creating secret '$name'..."
  # Read a bounded amount of randomness so pipefail cannot trip on SIGPIPE.
  od -An -N32 -tx1 -v /dev/urandom | tr -d ' \n' | \
    docker --context "$DOCKER_CONTEXT" secret create "$name" -
}

# Validate required tags and stack configuration before changing the Swarm.
docker compose -f docker-stack.yml config --quiet

ensure_secret postgres_password
ensure_secret valkey_password

echo "Deploying stack '$STACK_NAME' to context '$DOCKER_CONTEXT'..."
docker --context "$DOCKER_CONTEXT" stack deploy --with-registry-auth -c docker-stack.yml "$STACK_NAME"
