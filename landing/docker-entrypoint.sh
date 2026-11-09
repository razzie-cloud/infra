#!/bin/sh
set -eu

# Render from the image template on each start, including canonical/Open Graph URLs.
domain="${DOMAIN:-razzie.cloud}"
case "$domain" in
  *[!a-zA-Z0-9.-]*)
    echo 'DOMAIN must be a hostname without a scheme, port, or path' >&2
    exit 1
    ;;
esac
sed "s|razzie\\.cloud|$domain|g" /etc/landing/index.html > /srv/index.html

exec "$@"
