# Scoped broker deployment

Use `make deploy` from infra (or the parent). The maintained helper renders the
appropriate stack; do not deploy docker-stack.yml directly. `BROKER_AUTH_MODE`
defaults explicitly to token. Secure deployments require supporting published
broker, gateway, Files and Notes images, the non-secret broker-clients.yml, and
operator PKI inputs. Docker control-plane administrators remain trusted.

## Images and TLS

The broker image contains /database-broker, /broker-token and /broker-start.
RazChess and GeoIP retain their upstream application layers, with /broker-start
copied onto them through Dockerfile.broker-app. Publish these two adapter images
before selecting them. By default their tags match DATABASE_BROKER_IMAGE_TAG;
RAZCHESS_BROKER_IMAGE and GEOIP_SERVER_BROKER_IMAGE can select explicit published
references. The adapter performs bounded, redirect-free, verified-TLS discovery
and a Redis PING before exec with --redis. Other upstream arguments are preserved.
It never invokes upstream discovery, whose error logs can expose URLs and whose
failure handling can select memory. Redis credentials exist in runtime arguments;
broker tokens do not. A later Redis outage can still affect upstream app behavior.

Build adapter layers using the selected broker image and the operator-selected
upstream versions (no application source fork):

```
make build-broker-apps
```

Publish the resulting images through the operator release process; deployment
verifies all selected images can be pulled by the target daemon before modifying Swarm resources. Parent
pins and Git tags alone do not publish images. Upstream overrides require rebuilding
and publishing matching adapter layers, then selecting their full references.

For a brand new installation, `make broker-pki` automates a dedicated internal CA,
broker certificate and Swarm TLS key secret using Python 3 and OpenSSL 3. Run it
before deployment (`make broker-pki deploy` runs both in order). It honors the
selected Docker context, deployment identity, BROKER_ORIGIN, BROKER_CA_FILE,
BROKER_TLS_CERT_FILE and BROKER_TLS_KEY_SECRET. The CA private key defaults to
`broker-ca-key.pem` beside the CA certificate; BROKER_CA_KEY_FILE overrides it.
The ignored `pki/` directory is created with owner-only access and the CA key
with mode 0600. Back up that key securely for renewal. The server private key
passes from process memory directly to Swarm secret stdin and is not saved locally.
The CA lasts ten years and the broker certificate lasts 397 days; renewal remains
an explicit operation. Repeating setup validates and reuses existing public inputs
and the secret, without rotating keys. Missing public inputs for an existing secret,
or a missing secret for an existing certificate, stop setup rather than replacing
credentials. Setup shares the deployment lock and can resume interrupted public
certificate publication after successful secret creation.

Alternatively, provision a certificate with `database-broker` in its SANs, its
issuing CA and a private key using an existing operator PKI. Set BROKER_CA_FILE,
BROKER_TLS_CERT_FILE and BROKER_TLS_KEY_SECRET. Create the TLS key as an
operator-managed Swarm secret, never in Git or .env; deployment verifies its
existence and mounts it only into the broker. Certificate/CA configs are
content-addressed. Renewal uses new public
certificate bytes and a new secret name when the private key changes. It is
independent of the token master key. No certificate verification is disabled.

BROKER_ORIGIN defaults to https://database-broker:8443. The broker remains private
on the internal overlay. The cached health endpoint has a separate loopback-only
HTTP listener at 8081 inside the broker container. There is no provisioning route
on that listener. Consumer CA trust is mounted explicitly, including into the
legacy-app adapter. Production token mode refuses a plaintext broker origin.
Isolated local Compose intentionally uses HTTP and independent development keys.

## Deployment lifecycle

The helper needs Python 3 (standard library only), Bash, Make, Docker Compose and
a Docker CLI providing `docker stack config`. DOCKER_CLI can select a compatible
client by absolute path without changing the system installation or active context.
Every Docker command targets DOCKER_CONTEXT; no global context changes occur.
`make config` validates interpolation, final references and the Swarm stack schema offline with
placeholder auth resource names. Deployment additionally validates policy with
/broker-token from the selected image, verifies the target is an active manager,
checks PKI certificate validity/hostname/chain with the same image and pulls all
selected images through that daemon, then validates the final stack before rollout.
This checks the server's platform and registry access and prepares its image cache;
it does not depend on local CLI support for OCI manifest indexes. Deployed mode is
rechecked under the lock so overlapping preflights cannot trigger a stale downgrade.

An atomically created stack-scoped Swarm config serializes deployment across
operator hosts. A concurrent deploy fails safely. A hard-killed deployment can
leave its lock: inspect that lock's ownership/run label and confirm no deployment
is active before explicitly removing it. Helpers terminate after at most 15
minutes. Ordinary failure, SIGINT, SIGHUP and SIGTERM remove only this run's helper
and lock; helper cleanup also waits for manager containers to stop. Readiness and
rollout waits are bounded by DEPLOY_TIMEOUT (5–900 seconds, default 300).

A fresh key is generated from 32 random bytes, passed directly to secret creation
stdin, and never written locally, logged or exported. The versioned master secret
is mounted only into the broker and a temporary token generator. A persistent
identity config records its Swarm ID. Missing/replaced masters stop deployment;
recovery requires a new key_id. Existing database-password secrets retain their
historical behavior and are never overwritten.

Policy bytes select a new content-addressed config; formatting changes affect the
config name, not token derivation. Derived secret names include stack, a hash of
deployment identity, protocol, key version, client and generation. Ownership labels
bind the full deployment and master Swarm ID. Matching secrets are reused without
reading their values; conflicts stop deployment. An unchanged deployment or
permission-only edit does not create a token-generation service.
Resource names longer than Swarm's 64-character limit are shortened using a hash
of the full name. Existing names within the limit remain unchanged, including
previously reserved master identities and tokens.

When tokens are missing, one temporary generator is pinned to the manager reached
by the context (including SSH). It has only the policy and active master, no app
networks, backend credentials, ports or Docker socket. Inherited health checks
and logging are disabled. Non-TTY exec output is checked for a successful exit and
exact envelope before passing it from process memory to secret creation stdin.
No secret bytes enter service specs, config files, labels, environment files or
logs. Client secrets mount at stable paths; Files/Notes mounts are readable only
by UID/GID 10001. No master is mounted into applications.

For a fresh secure stack, broker policy/key/TLS readiness converges before consumers
start. For an existing tokenless stack, the helper upgrades the broker in explicit
none mode while retaining legacy HTTP 8080, rolls consumers onto authenticated
HTTPS 8443, checks readiness, then enables token enforcement and removes legacy
HTTP. This migration retains the old exposure until final cutover. An installation
already enforcing tokens never enters none mode. A failed stage stops the command;
there is no automatic downgrade. Rerunning resumes from reusable resources.

Policy additions/expansions reach all broker replicas before consumers. Removed
or disabled initial clients have their consumers scaled to zero. The helper waits
for task specifications and available container health, then runs a separate
short-lived readiness probe against every broker and first-party consumer task.
Final acceptance verifies trusted TLS, 401 without authentication and 403 for
listing/forbidden pairs against every broker replica directly (preserving TLS
hostname verification). These probes never call an allowed provisioning endpoint.
The probe helper has CA trust and at most one app token, no master key.

## Explicit legacy operation

`BROKER_AUTH_MODE=none make deploy` selects tokenless discovery and omits policy,
key, client-secret and PKI preparation/mounts. The bounded legacy-app adapter is
still required to prevent startup on discovery failure. No token files are needed.
An existing enforcing installation is protected against downgrade even if none
is selected: a deliberate security rollback requires a separately reviewed manual
procedure, not automatic recovery. Broker none mode warns once and ignores any
client/token query parameters. Health remains unauthenticated in both modes.

## Rotation, revocation and recovery

For a client rotation, increment its generation and set accepted_generations to
old and new values. Deploy, verify consumer convergence, then remove the old
value and deploy again. For compromise response, remove acceptance first and
accept a controlled interruption. Removing a client or setting enabled: false
revokes it on every converged broker; original app mappings are also stopped.

For a master rotation, choose a new key_id and accepted_key_ids containing old
and new IDs. Deploy to create the new Swarm-only master and distribute derived
app tokens. After convergence, remove the old ID and deploy again. Absent overlap
fields, only current key/generation is accepted. Up to four unique values including
the active value are permitted, to keep overlap bounded. Ordinary policy edits
require no token rotation. Never restore revoked policy/secrets during rollback.

Do not delete old secrets/configs during deployment. Garbage collection is a
separate, reference-aware operation, including running service tasks and intended
rollback state. Back up Swarm manager state securely as well as PostgreSQL and
Valkey AOF/ACL state. Losing a master requires a new version and token redistribution,
not recreating application databases. Revoke and rotate already-issued database
credentials separately when needed: broker token revocation does not revoke them.
Bearer tokens remain replayable within their current grants if stolen.

## Verification

`python3 -m unittest discover -s tests -v` runs isolated fake-Docker deployment
and real-OpenSSL PKI setup checks: bootstrap/redeploy, permission-only changes, added clients, failed/truncated
or interrupted generation, partial state, lost/replaced masters, custom remote
context, rotations/revocation, staged upgrades, ownership conflicts and explicit
none mode, internal CA setup/reuse, interrupted certificate publication and missing
PKI inputs. Fixtures consume secret stdin without retaining it. `bash -n deploy.sh`
and `make config` validate syntax/interpolation without a live daemon.

A disposable Swarm acceptance run is still required before production release:
fresh bootstrap, migration, repeated deploys, backend-side-effect denial, revocation
on every replica, verified TLS/renewal, worker health and helper cleanup. Audit
specs/logs for secrets and verify master mounts remain confined to the broker and
temporary generator. Ordinary source validation does not deploy to production.
