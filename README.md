# Razzie Cloud infrastructure

Deploy the Razzie Cloud platform with Docker Swarm and Caddy-managed HTTPS. The stack
includes access-gateway for passkey sign-in, database-broker for provisioning,
Files for S3 storage, public RazChess, UUID, GeoIP and 0x40hues apps, PostgreSQL, Valkey,
administrator-only Dozzle logs, and a whoami demo. Optional
[NixOS configuration](nixos/README.md) prepares the host.

To explore the platform on your own machine first, use the
[Razzie Cloud local demo](https://github.com/razzie-cloud/.github#try-it-locally).

The public [landing page](landing/README.md) is served at `razzie.cloud`, with
links to GitHub, Files, and sign-in. Its assets are packaged in a static Caddy
image; build and publish it before the first deployment and after page changes.

## First deployment

You need an initialized Docker Swarm, a Docker context pointing at it, Docker
Compose, Bash, and Make. Point DNS at the ingress host and allow ports 80/443.
The default context is `razcloud` and stack name is `razzie-cloud`.

```sh
git clone https://github.com/razzie-cloud/infra.git
cd infra
cp .env.example .env
```

Set the three platform service image tags in `.env` to published releases. Keep
image selections in this ignored file. In the parent checkout, `make latest-tags` can
select stable platform service tags; confirm their images are published before
deploying. Also set `LANDING_IMAGE_TAG` to a published homepage image built from
`landing/`; see its [build instructions](landing/README.md#swarm-image).
`make latest-tags` preserves that selection and only discovers the three Go service tags.
RazChess, UUID, GeoIP and 0x40hues use fixed defaults from the stack; their
image tag variables in `.env.example` allow explicit overrides and are left
unchanged by `make latest-tags`.

By default, the stack uses `razzie.cloud`, `auth.razzie.cloud`, `files.razzie.cloud`,
`shared-files.razzie.cloud`, `logs.razzie.cloud`, `whoami.razzie.cloud`,
`chess.razzie.cloud`, `uuid.razzie.cloud`, `geoip.razzie.cloud`, and `hues.razzie.cloud`.
Set `DOMAIN=example.com` in `.env` to use another base hostname (no scheme, port,
or trailing slash). It defaults to `razzie.cloud` and updates all Caddy host labels,
service origins, the passkey relying-party ID, the gateway allowlist, and homepage
links and metadata. Point the apex and listed subdomains at your ingress host.
The sharing hostname stays separate from management and outside the gateway
allowlist. You can also override it with `make config DOMAIN=example.com` or
`make deploy DOMAIN=example.com`. Set `DOCKER_CONTEXT` in `.env` for your Swarm.

Run from this directory:

```sh
make config   # Validate configuration without deploying
make deploy   # Create missing database secrets and deploy the stack
```

Gateway and broker run two replicas each.

On first start, read the gateway's `bootstrap.token_created` log event and use its
`bootstrap_token` at your auth URL to create the administrator and enroll a passkey.
Open your Files URL to connect S3 storage. Back up PostgreSQL: it contains accounts,
configuration, S3 credentials, and crypt recovery passwords; file contents remain in S3.

See [operations](docs/operations.md) for environment overrides, routing, Dozzle,
and upgrade details, or [AGENTS.md](AGENTS.md) for deployment invariants and checks.
