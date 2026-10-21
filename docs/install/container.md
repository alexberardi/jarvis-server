# The jarvisd container image

One image, `jarvisd`, built for linux/amd64 and linux/arm64 from a `jarvisd-<v>-linux-<arch>.tar.gz`
archive (the same binary as the bare-metal install). It runs jarvisd in container mode: updates
come from pulling a new image, not from jarvisd's own updater.

## Build

`scripts/packaging/container/` holds the `Dockerfile` and the entrypoint:

```sh
scripts/container-build.sh dist v1.2.3 amd64 jarvisd:v1.2.3
```

`scripts/container-check.sh DIST VERSION [ARCH]` builds the image from `DIST/` (ARCH `amd64` or
`arm64`, by default the machine's) and checks it: the image's architecture, health within 60 s,
exactly ports 7700/7710/1884 listening, `/services` URLs with paths, jarvisd running as uid 99,
the setup token in `docker logs`, and the data surviving `docker rm` and a new `docker run`.

The base is `ubuntu:24.04` with `ca-certificates curl libgomp1 tini tzdata`. tini is PID 1;
`HEALTHCHECK` curls `/health` on 7700.

## Run

```sh
docker run -d --name jarvis --restart unless-stopped -p 7700:7700 -p 7710:7710 -p 1884:1884 \
  -v /srv/jarvis:/data ghcr.io/alexberardi/jarvisd:latest
docker logs jarvis   # "Setup token: ..." for the setup wizard on http://<host>:7710/
```

Ports: 7700 API (every service under its path, e.g. `/cc`), 7710 admin, 1884 MQTT.
`/data` holds everything (`jarvis.db`, `setup-token`, models, engines).

## Environment

| Variable | Default | |
|---|---|---|
| `JARVIS_HOME` | `/data` | jarvisd's home |
| `JARVIS_CONTAINER` | `docker` | container mode (Unraid's template sets `unraid`) |
| `JARVIS_NO_BROWSER` | `1` | never open a browser |
| `PUID` / `PGID` | `99` / `100` | the user jarvisd runs as (Unraid's `nobody:users`) |
| `NVIDIA_DRIVER_CAPABILITIES` | `compute,utility` | what the NVIDIA runtime mounts |

Any other `JARVIS_*` variable jarvisd reads works too (e.g. `JARVIS_MDNS=0`).

**PUID/PGID.** Started as root (the default), the entrypoint makes `$JARVIS_HOME` belong to
`PUID:PGID` (a recursive `chown` only when its owner differs) and runs jarvisd as that user
with no supplementary groups. A value that isn't a number stops the container; an empty one
means the default. Started with `--user`, it runs jarvisd as that user and changes nothing.

**GPU (NVIDIA).** With the [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)
on the host, add `--gpus all` (docker, compose) or
`--runtime=nvidia -e NVIDIA_VISIBLE_DEVICES=all` (Unraid). The toolkit mounts `nvidia-smi` and
the driver (`compute,utility`), and jarvisd detects the card. The CUDA llama-server downloads
with its own CUDA runtime into `/data/engines`. Without a GPU the image runs on the CPU.
AMD/Vulkan passthrough isn't supported yet.

## Tags and publishing

| Tag | |
|---|---|
| `:<v>` and `:latest` | every `v*` tag (prereleases included), linux/amd64 + linux/arm64 |
| `:<v>-amd64` and `:<v>-arm64` | the single-arch images they are made from |
| `:dev` | every push to main (multi-arch) |
| `:dev-amd64` and `:dev-arm64` | its single-arch images |

`release.yml` publishes the release tags and `container-dev.yml` publishes `:dev`. Each builds
and checks one image per architecture on a native runner, then
`scripts/container-publish.sh` joins them.

GHCR makes a new package private, so switch it to Public once at
<https://github.com/users/alexberardi/packages/container/jarvisd/settings> (Danger Zone →
Change visibility). The release's publish job warns while it is private
(`scripts/ghcr-public-check.sh`). The tag scheme is decided in
[2026-10-21-container-tags.md](../decisions/2026-10-21-container-tags.md).

## Unraid

The Community Applications template is `scripts/packaging/unraid/jarvis.xml` (its README
says how it is published); the install guide is the Unraid page in jarvis-docs
(`docs/getting-started/unraid.md`).

## GPU check (by hand, on an NVIDIA host)

CI has no GPU, so on a host with an NVIDIA card:

1. If a jarvisd already runs on the host, wait until it leaves room for a small model.
2. Run `docker run -d --name jarvis-gpu --gpus all -p 37700:7700 -p 37710:7710 -p 31884:1884
   -v $TMPDIR/jg:/data -e JARVIS_MDNS=0 ghcr.io/alexberardi/jarvisd:dev` (a release tag works
   too; the ports are chosen so an installed jarvisd is untouched).
3. Finish setup. The Hardware panel should list the card.
4. Download the `cuda` engine and a small GGUF. A chat should run on `cuda`.
5. Without `--gpus`: no GPU listed, the chat runs on the `cpu` engine, and there are no errors
   in `docker logs`.
