# Infrastructure development guide

## Scope and workflow

- This repository owns the Swarm stack, deployment script, and NixOS host
  configuration. In the Razzie Cloud parent checkout it is a Git submodule with
  its own history; commit infrastructure changes here before updating the
  parent's pinned commit. Keep service source changes in their owning repos.
- Inspect `git status`, staged and unstaged diffs, and untracked files before
  editing. Preserve existing work and keep changes scoped to the task.
- Publishing images, pushing commits, deploying, and changing a live host need
  task authorization. Do not run `deploy.sh`, `docker stack deploy`,
  `nixos-rebuild switch`, or change Docker contexts for routine validation.

## Stack invariants

- `docker-stack.yml` includes all services; no files overlay is needed. Platform
  and third-party image tags are required environment variables. The inactive
  RazChess, UUID, GeoIP, and 0x40hues apps use fixed stack defaults with optional overrides;
  `make latest-tags` must not query their remotes or replace those overrides.
  Keep variable names synchronized with `.env.example` and the Makefile exports.
  Store other release selections in ignored `.env`; keep other current versions
  out of stack files and all current versions out of README.md and AGENTS.md.
  Tags match published Git tags exactly,
  including any `v` prefix. Images must be published before deployment; changing
  a service checkout does not publish them.
- PostgreSQL, Valkey, and the broker use the `internal` overlay. Gateway, files, notes,
  RazChess, and GeoIP join `internal` and `web`; Caddy, Dozzle, whoami, UUID,
  0x40hues, and the public landing service join `web`. Only Caddy publishes
  ingress ports (80/443). Keep databases and broker APIs private.
- The separate `razzie-cloud/landing` repository owns the homepage image. Build and publish
  `ghcr.io/razzie-cloud/landing` before selecting `LANDING_IMAGE_TAG` in `.env`;
  export it in the Makefile. Parent `make latest-tags` discovers landing releases
  alongside gateway, broker, Files and Notes. Swarm serves it at `razzie.cloud`
  without auth.
  The Go HTTP service embeds assets and renders HTML in memory at startup;
  only the public page/assets and health endpoints are served. Config/source
  stay private, and the container runs as an unprivileged user. Local Compose
  builds the landing submodule with runtime localhost settings. `LANDING_APPS_FILE` supplies the optional JSON app
  list through a Docker config; `landing-apps.json` includes Files and Notes.
- Valkey requires 9.1.0+ with database ACLs and runs standalone. Database 0
  stores broker metadata; applications use the other 15 databases. Both AOF
  data and `/data/users.acl` persist in `valkey_data`; use a broker image that
  saves configured ACL files before publishing credentials. Preserve both in
  backups. Initial admin credentials are hashed only on the first volume use.
- Gateway and broker request two replicas. Preserve their shared
  backend configuration; no sticky sessions or application volumes are needed.
  Use images with migration/bootstrap/provisioning coordination. Older images
  need staged initialization; see `docs/operations.md#replicas` for upgrade and
  availability limits. Do not impose distinct-node placement on single-node installations.
- RazChess, UUID, GeoIP, and 0x40hues retain public routes without forward auth. RazChess
  stays at one replica because active sessions live in memory. RazChess and
  GeoIP use `--redis-from` with separate broker-managed Valkey instances; their
  Alpine entrypoints wait for broker `/readyz` before one-time discovery. Keep
  credentials out of arguments and environment files. UUID and 0x40hues need no database.
  The default RazChess loader uses `SCAN`, compatible with restricted Valkey
  ACLs. Older `KEYS` loaders cannot restore sessions. Preserve restricted ACLs.
  See `docs/operations.md#razchess-uuid-and-geoip`.
- Caddy configuration uses service deployment labels. Keep every files management path,
  including `/_auth/callback`, behind forward auth. Strip incoming identity
  headers before authentication and copy verified identity headers from the
  gateway before proxying to files. The separate shared-files host deliberately
  bypasses forward auth for `/s/*` and `/static/*` only, strips identity headers,
  and returns 404 for other paths. Its SHARE_ORIGIN must have a different hostname
  and is not a gateway FORWARD_AUTH_ORIGINS entry.
- Publish Caddy ports 80/443 in host mode to preserve client IPs for GeoIP.
  Public DNS must target the manager running Caddy; other nodes do not accept
  these ports through the Swarm routing mesh. Keep one Caddy task per ingress host.
  Verify application image tags exist in the registry before selecting defaults;
  a Git tag alone does not establish that its container image has been published.
- Dozzle uses a global service with `DOZZLE_MODE=swarm`, local Docker sockets,
  and per-node `dozzle_data` volumes. Its HTTP port must stay private. Every path
  on `logs.razzie.cloud`, including `/_auth/callback`, uses `/forward_auth/admin`.
  Strip incoming `X-Auth-Request-User`, `X-Auth-Request-Name`, and `Remote-*` headers
  before copying verified identity. Keep `DOZZLE_AUTH_HEADER_USER` mapped to the
  gateway UUID header and `DOZZLE_AUTH_HEADER_NAME` to its username header; no
  email is supplied. Keep shell/actions disabled. A read-only socket mount does
  not limit Docker API access. `DOZZLE_IMAGE_TAG` is operator-managed in `.env`.
- Change public DNS, TLS, Caddy host labels, gateway/files `ORIGIN`, files `SHARE_ORIGIN`, gateway
  `RP_ID`, and `FORWARD_AUTH_ORIGINS` together. Gateway trusts private proxy
  ranges because its ports are reachable only through the Docker networks.
  `DOMAIN` (default `razzie.cloud`) drives every public hostname and auth origin;
  export it in the Makefile. The landing image renders links and metadata from
  this setting at startup; preserve local Compose's runtime scheme and port settings.
- Swarm ignores `depends_on`. Preserve restart policies, the broker's bounded
  PostgreSQL connection timeout, and its cached `/readyz` health check on
  `SERVICE_PORT` (default `8080`). Gateway and files inherit `/readyz` probes
  from their images. Broker instance/URI endpoints can provision databases;
  do not use them for probes.
- `deploy.sh` runs from this directory and defaults to context `razcloud` and
  stack `razzie-cloud`. The Makefile loads `.env` (overridable with `ENV_FILE`)
  and exports settings; command-line Make assignments take precedence. The
  script validates configuration before creating missing external
  `postgres_password` and `valkey_password` secrets and preserves existing ones.
  It passes `--with-registry-auth` so nodes can pull operator-selected private images.
  Do not expose credentials or remove secrets, volumes, or obsolete services incidentally.
- Notes uses two replicas with broker PostgreSQL and River jobs. Users add S3
  connections and optionally rclone-encrypted notebooks through its UI, following
  Files. Connections, mappings and crypt recovery records are durable owner-scoped
  PostgreSQL settings; back them up. Index rows/jobs are derived from S3. No Notes
  provider Docker secrets, credential environment variables or secret volume are
  needed. Export NOTES_IMAGE_TAG and optional egress/queue settings through Make.
  Every Notes path uses forward auth; /v1/admin/* uses /forward_auth/admin.
  Strip X-Notes-Admin and identity input first, then issue the admin marker only
  after successful gateway admin validation. Keep callbacks protected; landing
  links and the gateway allowlist include Notes origins.
- Files keeps credentials and crypt recovery passwords in its broker-provisioned
  PostgreSQL database. Preserve backups; no files-specific persistent volume or
  secret is required. S3 access uses the web overlay and blocks private endpoints
  by default. For a legacy file-backed deployment, stop old files replicas,
  temporarily mount the old secret volume read-only into one new instance with
  `IMPORT_SECRETS_DIR`, verify import and a database backup, then remove that
  setting and mount. Keep the old volume until verification. See the files
  repository's `docs/reference.md#upgrading-an-existing-file-backed-installation`
  for migration details.

## Verification

- Validate shell changes with `bash -n deploy.sh`.
- Validate stack changes without deployment using `make config` with a populated
  `.env`, or `docker compose --env-file .env -f docker-stack.yml config --quiet`.
  Check required image tags and Make command-line overrides when changing image
  interpolation. Neither check requires a running daemon. For an example-only
  check, supply placeholder service tags through the environment with
  `docker compose --env-file .env.example -f docker-stack.yml config --quiet`.
- For auth/routing changes, inspect generated Caddy configuration and complete
  gateway/browser acceptance checks in an authorized test environment. Live S3
  acceptance uses dedicated disposable credentials and bucket/prefixes.
- Nix validation requires a Nix-capable environment. Full host evaluation also
  requires the absent host-provided `nixos/hardware-configuration.nix` and real
  networking settings; `networking.nix` has placeholders. Preserve
  `system.stateVersion` unless explicitly migrating the host. `nixos/harness.nix`
  is separate and is not imported by the main configuration.
- Report exactly which checks ran and any environment limitations.

The landing app list is a Docker config sourced from `LANDING_APPS_FILE`
(default `./landing-apps.json`). The example lists Files and Notes; use an empty
`apps` array for Sign in alone. See the [landing config reference](https://github.com/razzie-cloud/landing#configuration)
for fields and icon presets. Editing a Docker config requires a new config name
for an existing Swarm deployment; rename `landing_apps` in both config declaration
and service reference before deploying a changed file. `DOMAIN` also supplies
`LANDING_ORIGIN` to the gateway, Files and Notes for their cloud-home links.
