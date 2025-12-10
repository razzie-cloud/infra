FROM alpine:latest
COPY --from=ghcr.io/teamhanko/hanko:latest /hanko /hanko
COPY config.template.yaml /config/config.template.yaml
COPY entrypoint.sh /entrypoint.sh
ENV DOMAIN="razzie.cloud"
ENV DATABASE_BROKER_URI="http://database-broker:8080"
ENTRYPOINT ["/entrypoint.sh"]