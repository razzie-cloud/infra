# Razcloud infrastructure

Docker Swarm stack for PostgreSQL, Dragonfly, database-broker, access-gateway,
files, Caddy Docker Proxy, and the whoami demo. Host configuration lives in
[`nixos/`](nixos/README.md).

Every image tag can be overridden through the shell environment:

| Variable | Default |
| --- | --- |
| `DATABASE_BROKER_IMAGE_TAG` | `v1.0.3` |
| `ACCESS_GATEWAY_IMAGE_TAG` | `v1.0.0` |
| `FILES_IMAGE_TAG` | `v1.0.0` |
| `POSTGRES_IMAGE_TAG` | `17-alpine` |
| `DRAGONFLY_IMAGE_TAG` | `latest` |
| `WHOAMI_IMAGE_TAG` | `latest` |
| `CADDY_DOCKER_PROXY_IMAGE_TAG` | `2.8-alpine` |

Run from this directory. Validate without deploying:

```sh
STACK_NAME=razzie-cloud DATABASE_BROKER_IMAGE_TAG=v1.0.3 \
  docker stack config -c docker-stack.yml >/dev/null
```

To deploy published images to an initialized Swarm:

```sh
DATABASE_BROKER_IMAGE_TAG=v1.0.3 ./deploy.sh
```

`deploy.sh` defaults to Docker context `razcloud` and stack `razzie-cloud`;
override with `DOCKER_CONTEXT` and `STACK_NAME`. It preserves existing external
database secrets and creates missing ones. Swarm reads exported or inline shell
variables; it does not automatically load a `.env` file. One stack file includes
all services.

Public routes are `auth.razzie.cloud`, `files.razzie.cloud`, and
`whoami.razzie.cloud`. For another domain, update DNS, Caddy labels, `ORIGIN`,
`RP_ID`, and `FORWARD_AUTH_ORIGINS` together. Back up PostgreSQL: files stores
credentials and crypt passwords there; object data stays in S3.
