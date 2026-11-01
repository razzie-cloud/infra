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
  tags use `${SERVICE_IMAGE_TAG:-default}` interpolation. Keep the variable
  names and defaults documented in `README.md` synchronized with the stack.
  Broker tags now include the `v` prefix (`v1.0.3`). Referenced images must be
  published before deployment; changing a service checkout does not publish it.
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
  PostgreSQL connection timeout, and its metadata-only health check. Broker
  instance/URI endpoints can provision databases; do not use them for probes.
- `deploy.sh` runs from this directory and defaults to context `razcloud` and
  stack `razzie-cloud`. It creates missing external `postgres_password` and
  `dragonfly_password` secrets and preserves existing ones. Do not expose
  credentials or remove secrets, volumes, or obsolete services incidentally.
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
- Validate stack changes without deployment:
  `STACK_NAME=razzie-cloud docker stack config -c docker-stack.yml >/dev/null`.
  Check both default image tags and environment overrides when changing image
  interpolation. This requires the Docker CLI, not a running daemon.
- For auth/routing changes, inspect generated Caddy configuration and complete
  gateway/browser acceptance checks in an authorized test environment. Live S3
  acceptance uses dedicated disposable credentials and bucket/prefixes.
- Nix validation requires a Nix-capable environment. Full host evaluation also
  requires the absent host-provided `nixos/hardware-configuration.nix` and real
  networking settings; `networking.nix` has placeholders. Preserve
  `system.stateVersion` unless explicitly migrating the host. `nixos/harness.nix`
  is separate and is not imported by the main configuration.
- Report exactly which checks ran and any environment limitations.
