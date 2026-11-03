# Razcloud infrastructure

Docker Swarm stack for PostgreSQL, Dragonfly, database-broker, access-gateway,
files, Dozzle, Caddy Docker Proxy, and the whoami demo. Host configuration lives in
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

Public routes are `auth.razzie.cloud`, `files.razzie.cloud`,
`shared-files.razzie.cloud`, `logs.razzie.cloud`, and `whoami.razzie.cloud`. Create DNS for the share
hostname before deploying a sharing-capable files image. Its public `/s/*` and
`/static/*` routes bypass forward auth; all other paths return 404. Management
on `files.razzie.cloud` remains fully protected. `SHARE_ORIGIN` must use a separate
hostname and must not be added to `FORWARD_AUTH_ORIGINS`. Share password cookies
are host-only. Each files replica caches share/storage metadata for 30 seconds;
revocation is immediate locally and may take 30 seconds on other replicas.
For another domain, update DNS, Caddy labels, `ORIGIN`, `SHARE_ORIGIN`,
`RP_ID`, and `FORWARD_AUTH_ORIGINS` together. Back up PostgreSQL: files stores
credentials and crypt passwords there; object data stays in S3.

Dozzle is available at `https://logs.razzie.cloud`; create its DNS record before
deployment and add `DOZZLE_IMAGE_TAG` from `.env.example` to existing `.env` files.
Use a gateway image with `/forward_auth/admin` support. Every Dozzle path,
including `/_auth/callback` and streaming APIs, uses that endpoint: signed-in
regular users receive 403, and the current admin role is checked on every request.
Its origin is included in `FORWARD_AUTH_ORIGINS`. Dozzle's HTTP port stays private;
only Caddy publishes ingress ports.

The [forward-proxy provider](https://dozzle.dev/guide/authentication/forward-proxy)
accepts configurable identity headers. We map `DOZZLE_AUTH_HEADER_USER` to
`X-Auth-Request-User` (account UUID) and `DOZZLE_AUTH_HEADER_NAME` to
`X-Auth-Request-Name` (username), using the gateway's existing response headers.
Email is optional and is not supplied. Caddy strips client-supplied identity and
`Remote-*` headers, including Dozzle's default email, filter, and roles headers,
before copying verified identity. Gateway authorization controls who may enter;
Dozzle container actions and shell access are disabled.

Dozzle uses [Swarm mode](https://dozzle.dev/guide/swarm-mode) with a global service
to collect logs from every node and discover peers over the web overlay. Each
node mounts its local Docker socket and has its own `dozzle_data` volume for
settings; settings are not shared between nodes. The read-only socket mount does
not restrict Docker API operations. Dozzle and the overlay must remain trusted;
do not publish its port or expose the Docker socket.
