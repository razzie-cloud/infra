# Razzie Cloud landing page

A brief static homepage designed for `https://razzie.cloud/`. It introduces the
project and links directly to:

- GitHub: <https://github.com/razzie-cloud>
- Files: <https://files.razzie.cloud/>
- Access Gateway: <https://auth.razzie.cloud/>

The page uses vendored PicoCSS, the new cloud mascot, and a small layout stylesheet.
All assets are local. It follows the browser's light/dark preference and works
without JavaScript, a build process, a backend, or third-party requests.
The links use ordinary navigation; no dynamic HTMX behavior is needed here.

## Local review

From this directory:

```sh
python3 -m http.server 8766 --bind 127.0.0.1
```

Visit <http://127.0.0.1:8766/>. Browser/devtools color-scheme emulation can switch
the theme. The service and GitHub links point to their real destinations.

Screenshots: [light desktop](previews/desktop-light.png),
[dark desktop](previews/desktop-dark.png), [light mobile](previews/mobile-light.png),
[dark mobile](previews/mobile-dark.png).

## Local stack

The parent Compose stack builds this directory as the `landing` service and
serves it through Caddy at <http://localhost:8080>. Files and sign-in links are
rewritten at build time to `files.localhost`, `notes.localhost`, and `auth.localhost`, including the
shared `LOCAL_PORT`. GitHub links keep their public destination.

From the parent checkout, after setting the usual local `.env` passwords:

```sh
docker compose up --build -d
```

After page, logo, or `LOCAL_PORT` changes, rebuild with
`docker compose up --build -d landing caddy`. A standalone Python preview uses
the source HTML's production service links instead.

## Swarm image

Swarm routes `https://razzie.cloud` publicly to `landing:8080`. It uses a published
image rather than a checkout bind mount, so tasks can run on any node. The image
serves only `index.html`, `landing.css`, and `assets/`; its HTML template and startup
script stay outside the web root. Previews and repository documentation are
excluded. The Pico license remains with the served assets.

Build and publish from the infra checkout using a new tag for each page update:

```sh
landing_tag=your-release-tag
docker build -t "ghcr.io/razzie-cloud/landing:$landing_tag" landing
docker push "ghcr.io/razzie-cloud/landing:$landing_tag"
```

Set `LANDING_IMAGE_TAG` in infra's ignored `.env` to that tag, run `make config`,
then use the normal `make deploy` workflow when ready. The registry image must
be pullable by the Swarm nodes. For a private package, log in to GHCR locally;
the deployment script forwards that login to Swarm. `make latest-tags` selects only the four Go
services and preserves your landing tag. Point the apex DNS record at the Caddy
ingress host; the existing proxy handles HTTPS. No gateway allowlist entry or
database is needed for the homepage.

Set `DOMAIN=example.com` in infra's `.env` to use another domain. The stack passes
it to the container, which renders Files/auth links, their display hostnames, and
canonical/Open Graph URLs at startup. The default is `razzie.cloud`; the same image
works for any domain without rebuilding. Publish an image containing this startup
script before using this setting with an older deployment.
The Dockerfile's `FILES_ORIGIN` and `AUTH_ORIGIN` build arguments remain available
for the local Compose stack's HTTP origins and shared port.

`assets/razzie-cloud-logo.png` is an exact copy of the parent brand master;
the Pico license is retained beside the vendored CSS.

## Verification

Headless Chromium checked both color schemes at widths of 320, 390, 768, 1024,
1440, and 1920 pixels, with no page overflow or browser errors. Desktop and mobile
screenshots were captured from the actual HTML. Checks covered successful local
asset loads, a single primary heading, the expected external destinations (GitHub, Files, Notes, sign-in),
keyboard skip navigation, and content/service links with JavaScript disabled.
Rendering makes no requests to third-party origins. Destination links were
verified in the document without signing in or contacting live services.

Local asset references, logo-copy hashes, and Git whitespace checks also passed.
Stack integration checks passed for both Compose configurations (including a
custom local port), Make's environment export, both image builds, Caddy config
validation, and HTTP requests through the actual local proxy. Checks verified
service links, asset content types and logo hashes, container health, and 404s
for previews, repository files, and missing paths. Test containers and their
network were removed afterward.

The installed Docker CLI lacks `docker stack config`; the Swarm file was checked
with `docker compose config` instead. No Swarm deployment or public DNS/TLS
acceptance ran, and no image was pushed. No Go service code changed.

The image also accepts `NOTES_ORIGIN` at build time for local Notes links.
