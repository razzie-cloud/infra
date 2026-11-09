# Infrastructure operations

See the [README](../README.md) for prerequisites and first deployment, and
[AGENTS.md](../AGENTS.md) for deployment invariants and verification.

## Image selection and deployment

Copy `.env.example` to `.env` and set the three platform service image tags to
published releases. The example contains third-party image tags and operator defaults;
RazChess, UUID, GeoIP and 0x40hues have fixed defaults with optional image tag overrides.
Other image tags must be supplied explicitly. Keep platform release selections
in the ignored `.env` rather than updating stack files or documentation for each release.
Set `LANDING_IMAGE_TAG` to a published `ghcr.io/razzie-cloud/landing` image built
from this repository's `landing/` directory. The [landing-page guide](../landing/README.md#swarm-image)
covers building and publishing it. Page and logo updates need a new image tag;
Swarm does not build images or read this checkout's assets from a bind mount.
In a parent Razzie Cloud checkout, `make latest-tags` discovers remote stable platform
releases and initializes or updates this file automatically. It does not query
RazChess, UUID, GeoIP or 0x40hues remotes or change their overrides.
It also leaves `LANDING_IMAGE_TAG` under operator control.

Run from this directory:

```sh
make config   # Validate without deploying
make deploy   # Create missing secrets and deploy
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

The public homepage at `razzie.cloud` proxies to the landing service on port 8080
without forward authentication. Point the apex DNS record at the Caddy ingress
host; Caddy manages its HTTPS certificate. It only joins the web overlay and has
no database, secret, volume, or published port of its own.

Other routes are `auth.razzie.cloud`, `files.razzie.cloud`,
`shared-files.razzie.cloud`, `logs.razzie.cloud`, and `whoami.razzie.cloud`. Create DNS for the share
hostname before deploying a sharing-capable files image. Its public `/s/*` and
`/static/*` routes bypass forward auth; all other paths return 404. Management
on `files.razzie.cloud` remains fully protected. `SHARE_ORIGIN` must use a separate
hostname and must not be added to `FORWARD_AUTH_ORIGINS`. Share password cookies
are host-only. Set `CAST_SHARE_TTL` in `.env` or through a Make override to adjust
temporary Cast playback access (default `6h`, positive and at most `24h`). Each
files replica caches share/storage metadata for 30 seconds; revocation is immediate locally and may take 30 seconds on other replicas.
For another domain, update DNS, Caddy labels, `ORIGIN`, `SHARE_ORIGIN`,
`RP_ID`, and `FORWARD_AUTH_ORIGINS` together. Back up PostgreSQL: files stores
credentials and crypt passwords there; object data stays in S3.

## RazChess, UUID and GeoIP

The public services retain their legacy hostnames: `chess.razzie.cloud`,
`uuid.razzie.cloud`, and `geoip.razzie.cloud`. They proxy to port 8080 without
gateway authentication and are outside `FORWARD_AUTH_ORIGINS`. Only Caddy
publishes ingress ports. Their images come from `ghcr.io/razzie`; optionally override
`RAZCHESS_IMAGE_TAG`, `UUID_SERVER_IMAGE_TAG`, and `GEOIP_SERVER_IMAGE_TAG` in `.env`.
Choose RazChess and GeoIP releases with `--redis-from` support.

Verify the chosen image tag exists in GHCR before deploying. Git tags and image
tags can differ; an unpublished image leaves Swarm tasks rejected with
`No such image` and Caddy returns HTTP 503.

Caddy publishes ports 80/443 in host mode so GeoIP receives the visitor's public
IP in `X-Forwarded-For`. The Swarm routing mesh replaces the peer address with
an internal IP, causing GeoIP's `/` lookup to fail with `IP ... is private`.
Point public DNS at the manager running Caddy; host-mode ports are available
only on that node. Keep one Caddy task per ingress host.

After moving DNS, certificate validation can temporarily reach the old server.
Repeated failures can rate-limit issuance. Check Caddy logs for the retry time
and successful issuance after DNS propagation; preserve the existing Caddy data
volume and let its automatic retries complete.

RazChess and GeoIP join both overlays and discover separate Valkey databases
through the private broker paths `/v1/instances/valkey/razchess/uri` and
`/v1/instances/valkey/geoip_server/uri`. UUID only joins `web` and needs no database.
No application password, new secret, or application volume is required.
The entrypoint waits for broker `/readyz` before invoking each Redis client's
one-time discovery; Swarm does not order service startup. If discovery or the
initial Redis connection still fails, these apps can continue without persistence
or caching. Check startup logs and restart affected tasks once the backend is
healthy. RazChess runs one replica because active game sessions live in memory.

The default RazChess release enumerates saved sessions with `SCAN` in
[`LoadSessions`](https://github.com/razzie/razchess/blob/v1.12/pkg/razchess/db.go),
which works with the broker's restricted Valkey ACLs. Older releases using
`KEYS` cannot restore sessions under that policy. Preserve restricted ACLs when
overriding the image tag.

The stack provisions new databases; it does not copy legacy Redis data. If old
chess sessions must be retained, arrange a separate data migration before routing
traffic to this stack. GeoIP can rebuild its cache. Existing legacy containers
and DNS records are untouched by configuration changes here.

## 0x40hues

0x40hues retains the public `hues.razzie.cloud` hostname, proxying to port 8080
without gateway authentication. It joins only `web` and needs no database,
secret, or persistent volume. Its image comes from `ghcr.io/razzie/0x40hues`;
`HUES_IMAGE_TAG` optionally overrides the fixed stack default. `make latest-tags`
does not query its remote or change the override.

## Switching from Dragonfly to Valkey

Select `VALKEY_IMAGE_TAG` from `.env.example` (9.1.0+ is required) and remove the
obsolete `DRAGONFLY_IMAGE_TAG` setting. Publish a broker image with Valkey support
and automatic `ACL SAVE` for configured ACL files, then select its tag in `.env`.
Validate with `make config` before an explicitly authorized deployment.

Deployment creates a separate `valkey_password` secret and `valkey_data` volume;
it keeps the old Dragonfly secret and volume. The gateway now discovers its URI
through `/v1/instances/valkey/access_gateway/uri`, while the broker uses database 0
for metadata and allocates from the other 15 databases. Increase `--databases` at
server startup if needed. Keep Valkey standalone and dedicated to this broker.

Valkey persists data with AOF and an every-second fsync, with ACLs stored
separately in `/data/users.acl`. Back up both together. The initial admin password
is hashed from `valkey_password` only when the ACL file is first created; changing
the secret alone does not rotate an existing volume's password.

Existing sessions are not migrated: users sign in again with their existing
PostgreSQL-backed accounts and passkeys. Restart both gateway replicas during the
switch so they fetch Valkey credentials. `stack deploy` does not prune the old
Dragonfly service; after verifying the new stack, remove that service explicitly.
Retain its secret and data until rollback is no longer needed. Do not restore
Dragonfly snapshots into the new Valkey volume.

## Replicas

Gateway and broker run two replicas each. Use published images
with coordinated startup and provisioning. Replicas can start together and share
backend state without sticky sessions or extra application volumes. Keep the same
image, environment, and backend connections on both replicas. Gateway rate limits
apply separately to each replica.

When upgrading from older images, pause new-instance provisioning and avoid
overlapping old and new initialization until all replicas support coordination.
Schema changes must remain compatible with any code still running. Keep the
broker's Valkey credential reservations and database ownership registry alongside
its published metadata and ACL file;
see the [broker reference](https://github.com/razzie-cloud/database-broker/blob/master/docs/reference.md#multiple-replicas)
for coordination and recovery details.

Both replicas may run on the same node. For host redundancy, check their placement
and allow capacity for both processes and their connection pools. PostgreSQL,
Valkey, and Caddy remain single instances, so backend or host failures can still
interrupt service. Valkey persists data with AOF and an every-second fsync, and
keeps ACLs in `/data/users.acl` on `valkey_data`. The broker saves that ACL file
before publishing credentials; back it up with the data. Initial admin credentials
are hashed from `valkey_password` only when the ACL file is first created. Changing
the secret alone does not rotate an existing volume's admin password.

## Dozzle

Dozzle is available at `https://logs.razzie.cloud`; create its DNS record before
deployment. In the parent checkout, `make latest-tags` adds the default
`DOZZLE_IMAGE_TAG` from `.env.example` to an existing `.env` when it is missing.
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
