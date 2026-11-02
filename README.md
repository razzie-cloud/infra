# Razcloud infrastructure

Docker Swarm stack for PostgreSQL, Dragonfly, database-broker, access-gateway,
files, Caddy Docker Proxy, and the whoami demo. Host configuration lives in
[`nixos/`](nixos/README.md).

Copy `.env.example` to `.env` and set the three service image tags to published
releases. The example contains third-party image tags and operator defaults;
the stack requires every image tag explicitly. Keep release selections in the
ignored `.env` rather than updating stack files or documentation for each release.
In a parent razcloud checkout, `make latest-tags` discovers remote stable service
releases and initializes or updates this file automatically.

Run from this directory to validate without deploying:

```sh
make config
```

Deploy the selected, published images to an initialized Swarm:

```sh
make deploy
```

The Makefile loads `.env` and exports deployment settings. Use
`make ENV_FILE=/absolute/path config` to select another file; Make command-line
assignments override the file, for example `make deploy DOCKER_CONTEXT=my-swarm`.
Use simple `KEY=value` assignments without shell quoting or secrets in this file.
With the Docker Compose plugin, configuration can also be checked without a
daemon using `docker compose --env-file .env -f docker-stack.yml config --quiet`.

`deploy.sh` defaults to Docker context `razcloud` and stack `razzie-cloud`;
override with `DOCKER_CONTEXT` and `STACK_NAME`. It validates the stack, preserves
existing external database secrets, and creates missing ones before deploying.
When called directly, it needs exported or inline environment settings: Swarm
does not load `.env` automatically. One stack file includes all services.

Public routes are `auth.razzie.cloud`, `files.razzie.cloud`, and
`whoami.razzie.cloud`. For another domain, update DNS, Caddy labels, `ORIGIN`,
`RP_ID`, and `FORWARD_AUTH_ORIGINS` together. Back up PostgreSQL: files stores
credentials and crypt passwords there; object data stays in S3.
