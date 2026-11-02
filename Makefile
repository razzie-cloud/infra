.DEFAULT_GOAL := help
ENV_FILE ?= .env
-include $(ENV_FILE)

DOCKER_CONTEXT ?= razcloud
STACK_NAME ?= razzie-cloud
export DOCKER_CONTEXT STACK_NAME
export DATABASE_BROKER_IMAGE_TAG ACCESS_GATEWAY_IMAGE_TAG FILES_IMAGE_TAG
export POSTGRES_IMAGE_TAG DRAGONFLY_IMAGE_TAG WHOAMI_IMAGE_TAG CADDY_DOCKER_PROXY_IMAGE_TAG

.PHONY: help config deploy

help:
	@printf '%s\n' 'Copy .env.example to .env and choose published service tags.' 'make config  Validate and render the Swarm stack' 'make deploy  Create missing secrets and deploy the stack'

config:
	docker stack config -c docker-stack.yml

deploy:
	bash ./deploy.sh
