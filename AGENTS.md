# Infrastructure development guide

## Scope and workflow

- This repository owns the Swarm stack, deployment script, and NixOS host
  configuration. In the razcloud parent checkout it is a Git submodule with
  its own history; commit infrastructure changes here before updating the
  parent's pinned commit. Keep service source changes in their owning repos.
- Inspect `git status`, staged and unstaged diffs, and untracked files before
  editing. Preserve existing work and keep changes scoped to the task.
- Publishing images, pushing commits, deploying, and changing a live host need
  task authorization. Do not run `deploy.sh`, `docker stack deploy`,
  `nixos-rebuild switch`, or change Docker contexts for routine validation.

## Stack invariants

- `docker-stack.yml` includes all services; no files overlay is needed. Image
  tags are required environment variables with no version defaults in the stack.
  Keep variable names synchronized with `.env.example` and the Makefile exports.
  Store release selections in ignored `.env`; do not add current service versions
  to stack files, README.md, or AGENTS.md. Tags match published Git tags exactly,
  including any `v` prefix. Images must be published before deployment; changing
  a service checkout does not publish them.
- PostgreSQL, Dragonfly, and the broker use the `internal` overlay. Gateway and
  files join `internal` and `web`; Caddy and whoami join `web`. Only Caddy
  publishes ingress ports (80/443). Keep databases and broker APIs private.
- Caddy configuration uses service deployment labels. Keep every files path,
  including `/_auth/callback`, behind forward auth. Strip incoming identity
  headers before authentication and copy verified identity headers from the
  gateway before proxying to files.
- Change public DNS, TLS, Caddy host labels, gateway/files `ORIGIN`, gateway
  `RP_ID`, and `FORWARD_AUTH_ORIGINS` together. Gateway trusts private proxy
  ranges because its ports are reachable only through the Docker networks.
- Swarm ignores `depends_on`. Preserve restart policies, the broker's bounded
  PostgreSQL connection timeout, and its cached `/readyz` health check on
  `SERVICE_PORT` (default `8080`). Gateway and files inherit `/readyz` probes
  from their images. Broker instance/URI endpoints can provision databases;
  do not use them for probes.
- `deploy.sh` runs from this directory and defaults to context `razcloud` and
  stack `razzie-cloud`. The Makefile loads `.env` (overridable with `ENV_FILE`)
  and exports settings; command-line Make assignments take precedence. The
  script validates configuration before creating missing external
  `postgres_password` and `dragonfly_password` secrets and preserves existing ones.
  Do not expose credentials or remove secrets, volumes, or obsolete services incidentally.
- Files keeps credentials and crypt recovery passwords in its broker-provisioned
  PostgreSQL database. Preserve backups; no files-specific persistent volume or
  secret is required. S3 access uses the web overlay and blocks private endpoints
  by default. For a legacy file-backed deployment, stop old files replicas,
  temporarily mount the old secret volume read-only into one new instance with
  `IMPORT_SECRETS_DIR`, verify import and a database backup, then remove that
  setting and mount. Keep the old volume until verification. See the files
  repository's `README.md` for migration details.

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
