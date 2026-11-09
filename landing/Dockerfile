FROM caddy:2-alpine

ARG FILES_ORIGIN=https://files.razzie.cloud
ARG AUTH_ORIGIN=https://auth.razzie.cloud

COPY Caddyfile /etc/caddy/Caddyfile
COPY index.html landing.css /srv/
COPY assets/ /srv/assets/

# Keep one page source; local builds link to the local services and shared port.
RUN sed -i \
      -e "s|https://files.razzie.cloud|${FILES_ORIGIN}|g" \
      -e "s|https://auth.razzie.cloud|${AUTH_ORIGIN}|g" \
      -e "s|<small>files.razzie.cloud</small>|<small>${FILES_ORIGIN#*://}</small>|g" \
      -e "s|<small>auth.razzie.cloud</small>|<small>${AUTH_ORIGIN#*://}</small>|g" \
      /srv/index.html

EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/ || exit 1
