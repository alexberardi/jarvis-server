# Run jarvisd in Docker

A Compose file that builds and runs `jarvisd` (the Jarvis server) in server mode only: `jarvisd serve`, with no node agent. The container
gets its own address on your LAN (macvlan) and uses an NVIDIA GPU by default. There is no published image yet, so the Dockerfile in
[`../build`](../build/Dockerfile) downloads the signed release archive, checks its SHA-256 and installs the binary on Debian 13 (trixie)
slim. It builds for `linux/amd64` and `linux/arm64`.

| File                       | Use                                                                                          |
| -------------------------- | -------------------------------------------------------------------------------------------- |
| `docker-compose.yml`       | The default: macvlan network (own LAN address), NVIDIA GPU.                                  |
| `docker-compose.bridge.yml` | The same on a Docker bridge with published ports, for hosts where macvlan is not possible.   |
| `.env`                     | Version, checksums, data folder and network settings for both.                               |

## What you need

| Item                   | Notes                                                                                                         |
| ---------------------- | ------------------------------------------------------------------------------------------------------------- |
| Docker with Compose v2 | `docker compose`, not the old `docker-compose`.                                                               |
| A 64-bit host          | amd64 or arm64. The engines `jarvisd` downloads need glibc 2.38 or newer, which the image provides.            |
| A free LAN address     | For macvlan: an unused IP in your LAN, outside the DHCP range.                                                |
| NVIDIA GPU (default)   | The NVIDIA driver and the NVIDIA Container Toolkit on the host. No GPU: see [GPU or CPU](#gpu-or-cpu).         |
| Disk                   | A few GB for the first models and engines in the data folder. A local language model adds more.                |
| Internet at first run  | `jarvisd` downloads engines (GitHub) and models (Hugging Face) when you choose them in the setup wizard.       |

## Start it

1. Edit the network settings in `.env` (`JARVIS_PARENT_IF`, `JARVIS_SUBNET`, `JARVIS_GATEWAY`, `JARVIS_IP`). The values in the file are examples.
2. Build and start it:

   ```bash
   cd docker/compose
   docker compose up -d --build
   docker compose ps          # wait for "healthy"
   docker exec jarvisd jarvisd setup-link
   ```

3. Open the printed link from another machine on your LAN and run the setup wizard. Point nodes and the mobile app at
   `http://<JARVIS_IP>:7700` (the config service); `jarvisd` also advertises itself over mDNS (`_jarvis-config._tcp`).

With macvlan the Docker host itself cannot reach the container's address (a Linux limitation), so use another machine, or add a macvlan
shim interface on the host if you need access from it.

## Settings (`.env`)

| Variable               | Default         | Meaning                                                                               |
| ---------------------- | --------------- | ------------------------------------------------------------------------------------- |
| `JARVISD_VERSION`      | `v0.1.0-rc17`   | Release to build.                                                                     |
| `JARVISD_SHA256_AMD64` | (pinned)        | SHA-256 of that release's `jarvisd-<version>-linux-amd64.tar.gz`.                     |
| `JARVISD_SHA256_ARM64` | (pinned)        | SHA-256 of that release's `jarvisd-<version>-linux-arm64.tar.gz`.                     |
| `JARVIS_DATA`          | `./data`        | Host folder for the database, signing key, models and engines. Back it up.             |
| `JARVIS_PARENT_IF`     | `eth0`          | Host LAN interface for macvlan (on Unraid, `br0` or a VLAN such as `br0.11`).          |
| `JARVIS_SUBNET`        | `192.168.1.0/24` | Your LAN subnet.                                                                     |
| `JARVIS_GATEWAY`       | `192.168.1.1`   | Your LAN gateway.                                                                     |
| `JARVIS_IP`            | `192.168.1.50`  | The container's address. It must be free and outside the DHCP range.                   |
| `JARVIS_API_PORT`, `JARVIS_ADMIN_PORT`, `JARVIS_MQTT_PORT` | `7700`, `7710`, `1884` | Host ports, used only by `docker-compose.bridge.yml`. |
| `TZ`                   | `UTC`           | Time zone, for example `Australia/Perth`.                                             |

The checksums come from the release's `SHA256SUMS` on the [releases page](https://github.com/alexberardi/jarvis-server/releases). The build
fails if the downloaded archive does not match.

## GPU or CPU

The compose file reserves the NVIDIA GPU for the container (the `deploy` block). With the language model hosted elsewhere, the GPU is
there for one job: **speech-to-text**. `jarvisd` runs whisper.cpp itself (there is no remote option), and Whisper is by far the heaviest thing
it runs locally. The GPU also serves a local language model if you choose one in the wizard instead of a remote endpoint.

| Component                                | Runs on                           | GPU helps?                                    |
| ---------------------------------------- | --------------------------------- | --------------------------------------------- |
| Speech-to-text (whisper.cpp)             | The GPU, or the CPU               | Yes, a lot (below)                            |
| Local language model (if you pick one)   | The GPU, or the CPU               | Yes                                           |
| Remote language model                    | Your own endpoint                 | Not used here                                 |
| Text-to-speech (Kokoro), embeddings, speaker recognition | The CPU in the container | No, these are small                  |

**What the GPU improves.** On one test machine (Intel i9-13900KF, 8 threads, NVIDIA RTX A5000), `large-v3-turbo` transcribed a 6 second
spoken clip with identical text both ways:

| Where        | Time for the 6 s clip                           |
| ------------ | ----------------------------------------------- |
| GPU (CUDA)   | 0.13 to 0.45 s (the first request was slowest)  |
| CPU          | 8.9 to 9.9 s                                    |

So on the CPU the same model took about 20 to 70 times longer, which adds roughly nine seconds between the end of a spoken command and the
start of any reply. A slower CPU (a NAS, a Raspberry Pi) will be slower again. These are single measurements on one machine, not a benchmark.

**No GPU? Fall back to the CPU.**

1. Delete the `deploy:` block from `docker-compose.yml` (or `docker-compose.bridge.yml`). With the block left in and no NVIDIA runtime on
   the host, the container is expected to fail to start.
2. In the setup wizard's Hardware step, choose the CPU for the engines.
3. In the speech-to-text step, choose a smaller model than `large-v3-turbo` (`small.en`, the speech model "Install recommended" picks, is what the project's own CPU
   tests use). Smaller models are much quicker on a CPU, at some cost in accuracy; they were not timed for this README, so check the
   delay yourself with a few spoken commands.

Text-to-speech, embeddings and speaker recognition need no change. Nodes, the mobile app and a remote language model are unaffected.

## Networking

**macvlan (default, recommended).** The container has its own LAN address, so:

- nodes and phones use that address directly, and the fixed ports 7700, 7710 and 1884 never clash with anything on the Docker host;
- mDNS discovery works, because `jarvisd` advertises `_jarvis-config._tcp` from its own interface (the log shows
  `mdns advertising ... addrs=[<JARVIS_IP>]`);
- `jarvisd` registers and listens on `JARVIS_IP`, so what it advertises is what clients can reach.

Notes:

- The Docker host cannot reach a macvlan container directly (see above).
- Docker refuses to create a second macvlan network with the same subnet on the same parent interface. If one already exists (Unraid creates
  `br0`, `br0.11` and similar), declare it instead of creating a new one: in the `networks:` section replace the `lan` definition with
  `external: true` and `name: <that network>`.
- Many Wi-Fi adapters will not carry extra MAC addresses, so macvlan generally needs a wired interface.

**Do not use host networking if you want mDNS.** With `network_mode: host`, `jarvisd` shares the host's network stack and its mDNS
advertisement does not work reliably: the host usually already runs its own responder (avahi on Unraid and most Linux desktops) on UDP 5353,
and the container's `_jarvis-config._tcp` record is not reliably seen by phones and nodes (host networking was not tested for this
README). Use macvlan, or enter the config URL by hand.

**Bridge (`docker-compose.bridge.yml`).** Publishes 7700, 7710 and 1884 on the host and works anywhere, including Docker Desktop. mDNS does not
cross a bridge, so nodes and phones need `http://<host>:7700` entered by hand, and the setup link prints the container's internal address:
use the host's address and keep the `#token=...` part.

```bash
docker compose -f docker-compose.bridge.yml up -d --build
```

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

## Stop and remove

```bash
docker compose down        # keeps the data folder
```

Deleting `JARVIS_DATA` removes the database and the downloaded models.
