.DEFAULT_GOAL := help
ENV_FILE ?= .env
-include $(ENV_FILE)

DOCKER_CONTEXT ?= razcloud
STACK_NAME ?= razzie-cloud
export DOCKER_CONTEXT STACK_NAME
export DATABASE_BROKER_IMAGE_TAG ACCESS_GATEWAY_IMAGE_TAG FILES_IMAGE_TAG
export LANDING_IMAGE_TAG
export RAZCHESS_IMAGE_TAG UUID_SERVER_IMAGE_TAG GEOIP_SERVER_IMAGE_TAG HUES_IMAGE_TAG
export POSTGRES_IMAGE_TAG VALKEY_IMAGE_TAG WHOAMI_IMAGE_TAG CADDY_DOCKER_PROXY_IMAGE_TAG
export DOZZLE_IMAGE_TAG CAST_SHARE_TTL

.PHONY: help config deploy

help:
	@printf '%s\n' 'Copy .env.example to .env and choose published service tags.' 'make config  Validate and render the Swarm stack' 'make deploy  Create missing secrets and deploy the stack'

config:
	docker compose -f docker-stack.yml config --quiet

deploy:
	bash ./deploy.sh
