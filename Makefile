.DEFAULT_GOAL := help
ENV_FILE ?= .env
-include $(ENV_FILE)

DOCKER_CONTEXT ?= razcloud
DOCKER_CLI ?= docker
STACK_NAME ?= razzie-cloud
BROKER_AUTH_MODE ?= token
BROKER_DEPLOYMENT_ID ?= razzie-cloud-production
BROKER_CLIENTS_FILE ?= ./broker-clients.yml
BROKER_ORIGIN ?= https://database-broker:8443
BROKER_CA_FILE ?= ./pki/broker-ca.pem
BROKER_TLS_CERT_FILE ?= ./pki/broker.pem
BROKER_TLS_KEY_SECRET ?= broker_tls_key
DEPLOY_TIMEOUT ?= 300
export DOCKER_CONTEXT STACK_NAME DOMAIN
export DOCKER_CLI
export DATABASE_BROKER_IMAGE_TAG ACCESS_GATEWAY_IMAGE_TAG FILES_IMAGE_TAG NOTES_IMAGE_TAG
export LANDING_IMAGE_TAG LANDING_APPS_FILE
export RAZCHESS_IMAGE_TAG UUID_SERVER_IMAGE_TAG GEOIP_SERVER_IMAGE_TAG HUES_IMAGE_TAG
export POSTGRES_IMAGE_TAG VALKEY_IMAGE_TAG WHOAMI_IMAGE_TAG CADDY_DOCKER_PROXY_IMAGE_TAG
export DOZZLE_IMAGE_TAG CAST_SHARE_TTL MAX_UPLOAD_BYTES

.PHONY: help config deploy broker-pki

help:
	@printf '%s\n' 'Copy .env.example to .env and choose published service tags.' 'make broker-pki  Prepare a dedicated internal CA and broker TLS secret' 'make config  Validate and render the Swarm stack' 'make deploy  Create missing secrets and deploy the stack'

broker-pki:
	python3 ./prepare-broker-pki.py

config:
	bash ./deploy.sh --check

deploy:
	bash ./deploy.sh

export NOTES_S3_ALLOWED_PRIVATE_ENDPOINTS NOTES_S3_ALLOW_HTTP NOTES_SCAN_INTERVAL NOTES_INDEX_WORKERS

export BROKER_AUTH_MODE BROKER_DEPLOYMENT_ID BROKER_CLIENTS_FILE BROKER_ORIGIN
export BROKER_CA_FILE BROKER_TLS_CERT_FILE BROKER_TLS_KEY_SECRET DEPLOY_TIMEOUT
export BROKER_CA_KEY_FILE
export RAZCHESS_BROKER_IMAGE GEOIP_SERVER_BROKER_IMAGE

BUILD_DOCKER_CONTEXT ?= default

.PHONY: build-broker-apps
build-broker-apps:
	"$(DOCKER_CLI)" --context "$(BUILD_DOCKER_CONTEXT)" build -f Dockerfile.broker-app --build-arg "BROKER_IMAGE=ghcr.io/razzie-cloud/database-broker:$(DATABASE_BROKER_IMAGE_TAG)" --build-arg "APP_IMAGE=ghcr.io/razzie/razchess:$(RAZCHESS_IMAGE_TAG)" -t "$(or $(RAZCHESS_BROKER_IMAGE),ghcr.io/razzie-cloud/razchess-broker:$(DATABASE_BROKER_IMAGE_TAG))" .
	"$(DOCKER_CLI)" --context "$(BUILD_DOCKER_CONTEXT)" build -f Dockerfile.broker-app --build-arg "BROKER_IMAGE=ghcr.io/razzie-cloud/database-broker:$(DATABASE_BROKER_IMAGE_TAG)" --build-arg "APP_IMAGE=ghcr.io/razzie/geoip-server:$(GEOIP_SERVER_IMAGE_TAG)" -t "$(or $(GEOIP_SERVER_BROKER_IMAGE),ghcr.io/razzie-cloud/geoip-server-broker:$(DATABASE_BROKER_IMAGE_TAG))" .
