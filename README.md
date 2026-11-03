# Razcloud infrastructure

Deploy the razcloud platform with Docker Swarm and Caddy-managed HTTPS. The stack
includes access-gateway for passkey sign-in, database-broker for provisioning,
Files for S3 storage, PostgreSQL, Dragonfly, administrator-only Dozzle logs, and a
whoami demo. Optional [NixOS configuration](nixos/README.md) prepares the host.

To explore the platform on your own machine first, use the
[razcloud local demo](https://github.com/razzie-cloud/.github#try-it-locally).

## First deployment

You need an initialized Docker Swarm, a Docker context pointing at it, Docker
Compose, Bash, and Make. Point DNS at the ingress host and allow ports 80/443.
The default context is `razcloud` and stack name is `razzie-cloud`.

```sh
git clone https://github.com/razzie-cloud/infra.git
cd infra
cp .env.example .env
```

Set the three service image tags in `.env` to published releases. Keep image
selections in this ignored file. In the parent checkout, `make latest-tags` can
select stable service tags; confirm their images are published before deploying.

The stack uses `auth.razzie.cloud`, `files.razzie.cloud`,
`shared-files.razzie.cloud`, `logs.razzie.cloud`, and `whoami.razzie.cloud`.
For your domain, update DNS, Caddy labels, `ORIGIN`, `SHARE_ORIGIN`, `RP_ID`, and
`FORWARD_AUTH_ORIGINS` together. Keep the sharing hostname separate from management
and outside the gateway allowlist. Set `DOCKER_CONTEXT` in `.env` for your Swarm.

Run from this directory:

```sh
make config   # Validate configuration without deploying
make deploy   # Create missing database secrets and deploy the stack
```

On first start, read the gateway's `bootstrap.token_created` log event and use its
`bootstrap_token` at your auth URL to create the administrator and enroll a passkey.
Open your Files URL to connect S3 storage. Back up PostgreSQL: it contains accounts,
configuration, S3 credentials, and crypt recovery passwords; file contents remain in S3.

See [operations](docs/operations.md) for environment overrides, routing, Dozzle,
and upgrade details, or [AGENTS.md](AGENTS.md) for deployment invariants and checks.
