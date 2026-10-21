# Run jarvisd in Docker

A Compose file that builds and runs `jarvisd` (the Jarvis server) in server mode only: `jarvisd serve`, with no node agent.
There is no published image yet, so the Dockerfile in [`../build`](../build/Dockerfile) downloads the signed release archive, checks its
SHA-256 and installs the binary on Debian 13 (trixie) slim. It builds for `linux/amd64` and `linux/arm64`.

## What you need

| Item                  | Notes                                                                                                         |
| --------------------- | ------------------------------------------------------------------------------------------------------------- |
| Docker with Compose v2 | `docker compose`, not the old `docker-compose`.                                                              |
| A 64-bit host         | amd64 or arm64. The engines `jarvisd` downloads need glibc 2.38 or newer, which the image provides.            |
| Disk                  | A few GB for the first models and engines in the data folder. A local language model adds more.               |
| Internet at first run | `jarvisd` downloads engines (GitHub) and models (Hugging Face) when you choose them in the setup wizard.       |
| Optional: NVIDIA GPU  | The NVIDIA Container Toolkit, then uncomment the `deploy` block in `docker-compose.yml`.                        |

## Start it

```bash
cd docker/compose
docker compose up -d --build
docker compose ps          # wait for "healthy"
docker exec jarvisd jarvisd setup-link
```

Open the printed link in a browser to run the setup wizard. Inside a bridge-network container the link shows the container's address
(for example `192.168.0.2`); use the Docker host's address instead and keep the `#token=...` part. Point nodes and the mobile app at
`http://<host>:7700` (the config service).

## Settings (`.env`)

| Variable                | Default       | Meaning                                                                              |
| ----------------------- | ------------- | ------------------------------------------------------------------------------------ |
| `JARVISD_VERSION`       | `v0.1.0-rc17` | Release to build, for example `v0.1.0-rc17`.                                         |
| `JARVISD_SHA256_AMD64`  | (pinned)      | SHA-256 of that release's `jarvisd-<version>-linux-amd64.tar.gz`.                    |
| `JARVISD_SHA256_ARM64`  | (pinned)      | SHA-256 of that release's `jarvisd-<version>-linux-arm64.tar.gz`.                    |
| `JARVIS_DATA`           | `./data`      | Host folder for the database, signing key, models and engines. Back it up.            |
| `JARVIS_API_PORT`       | `7700`        | Config service: nodes, phones and the recipes app.                                   |
| `JARVIS_ADMIN_PORT`     | `7710`        | Admin and setup wizard.                                                              |
| `JARVIS_MQTT_PORT`      | `1884`        | MQTT broker for voice nodes (not 1883).                                              |
| `TZ`                    | `UTC`         | Time zone, for example `Australia/Perth`.                                            |

The checksums come from the release's `SHA256SUMS` on the [releases page](https://github.com/alexberardi/jarvis-server/releases). The build
fails if the downloaded archive does not match.

## Upgrade

1. Stop it and copy the data folder (`docker compose stop`, then copy `JARVIS_DATA`).
2. Set the new `JARVISD_VERSION` and both checksums in `.env`.
3. `docker compose up -d --build`. `jarvisd` migrates its database on start.

## Multi-arch image

`docker compose build` builds for the machine you run it on. To build one image that runs on both amd64 and arm64 (for example to push to
your own registry for a Raspberry Pi), use Buildx with the same build arguments:

```bash
cd docker/compose
set -a; . ./.env; set +a
docker buildx build ../build \
  --platform linux/amd64,linux/arm64 \
  --build-arg JARVISD_VERSION --build-arg JARVISD_SHA256_AMD64 --build-arg JARVISD_SHA256_ARM64 \
  -t registry.example.com/jarvisd:"$JARVISD_VERSION" --push
```

A multi-platform build on an amd64 machine needs QEMU (`docker run --privileged --rm tonistiigi/binfmt --install arm64`) or a remote arm64
builder. Each architecture also builds natively on its own host without any of that.

## Networking notes

- The default is a bridge network with published ports, which works anywhere. mDNS discovery (`_jarvis-config._tcp`) does not cross a bridge,
  so phones and nodes need the config URL (`http://<host>:7700`) entered by hand.
- For mDNS on a plain Linux host, switch the service to `network_mode: host` and remove its `ports:` section. A macvlan or ipvlan address on
  your LAN also works and keeps the container's own IP.

## Stop and remove

```bash
docker compose down        # keeps the data folder
```

Deleting `JARVIS_DATA` removes the database and the downloaded models.
