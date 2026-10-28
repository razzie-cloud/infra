# Files deployment overlay

`files-stack.yml` adds the files service and the missing access-gateway service
to the base stack. It preserves the existing base-stack edits and does not change
the deployment script. No images or services are published by adding this file.

Before a release, publish immutable files and gateway images, and set
`FILES_IMAGE` and `ACCESS_GATEWAY_IMAGE` to their tags/digests. The example public
origins are `https://files.razzie.cloud` and `https://auth.razzie.cloud`; update
DNS, TLS, RP_ID, ORIGIN, and FORWARD_AUTH_ORIGINS together for another deployment.

The files backend has no published port. Its ordered Caddy route strips incoming
identity headers before forward auth and reverse proxy, including callback paths.
The web overlay supplies outbound S3 HTTPS access; database traffic stays on the
internal overlay. The default policy blocks private S3 endpoints.

Files stores credentials and crypt recovery passwords in its broker-provisioned
PostgreSQL database. Back up that database; there is no files-specific persistent
volume or storage-node placement constraint. Replicas can share PostgreSQL without
sharing a host directory. The container still runs as UID/GID 10001. Existing
external PostgreSQL/Dragonfly passwords remain base-stack Docker secrets.

For a previously deployed file-backed version, stop the old files replicas and
run one new instance with the old secret volume temporarily mounted read-only and
`IMPORT_SECRETS_DIR` set to its mount path. Verify import and a database backup,
then remove the import setting and mount. See `files/README.md` in the parent
checkout for migration details. Do not delete the old volume before that verification.

Validate the merged configuration without deploying:

```sh
STACK_NAME=razzie-cloud \
FILES_IMAGE=ghcr.io/razzie-cloud/files:YOUR_PUBLISHED_TAG \
ACCESS_GATEWAY_IMAGE=ghcr.io/razzie-cloud/access-gateway:YOUR_PUBLISHED_TAG \
docker stack config -c docker-stack.yml -c files-stack.yml >/dev/null
```

An authorized deployment must supply **both** stack files. `deploy.sh` currently
deploys only the base file; running it alone does not include files. Validate the
generated Caddy configuration and complete the gateway/browser acceptance checks
before enabling the public files origin. Live Backblaze acceptance needs dedicated
disposable bucket/prefix credentials; local tests never use production storage.
