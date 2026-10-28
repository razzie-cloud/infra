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

The secret volume holds credentials and crypt recovery passwords, separate from
PostgreSQL. Use protected/encrypted host storage, back up metadata and secret
files together, and select exactly one node with `files_storage=true`. The overlay
places the single files replica there. The files container runs as UID/GID 10001.
Configure shared protected storage before scaling or relocating it. Existing
external PostgreSQL/Dragonfly secrets are supplied by the base stack.

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
