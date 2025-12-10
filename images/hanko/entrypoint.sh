#!/bin/sh
set -e

HANKO_SECRET_KEY="$(cat /run/secrets/hanko_secret_key)"

# Get connection strings from database-broker
POSTGRES_URI="$(wget -qO- "${DATABASE_BROKER_URI}/v1/instances/postgres/hanko/uri")"
REDIS_URI="$(wget -qO- "${DATABASE_BROKER_URI}/v1/instances/redis/hanko/uri")"

# Extract Redis address+password from URI
REDIS_ADDRESS="$(printf "%s" "$REDIS_URI" | sed -E "s#^redis://([^:]+:)?([^@]*)@([^/]+)/?.*#\\3#")"
REDIS_PASSWORD="$(printf "%s" "$REDIS_URI" | sed -nE "s#^redis://([^:]+:)?([^@]*)@.*#\\2#p")"

# Render config file
sed \
  -e "s|__POSTGRES_URI__|$POSTGRES_URI|g" \
  -e "s|__REDIS_ADDRESS__|$REDIS_ADDRESS|g" \
  -e "s|__REDIS_PASSWORD__|$REDIS_PASSWORD|g" \
  -e "s|__DOMAIN__|$DOMAIN|g" \
  -e "s|__HANKO_SECRET_KEY__|$HANKO_SECRET_KEY|g" \
  /config/config.template.yaml > /config/config.yaml

/hanko migrate up --config /config/config.yaml
exec /hanko serve all --config /config/config.yaml
