# email-mcp in a container

**Short version: for use on your own computer, do not use the container.
Install the binary instead (see the [README](README.md#install)).**

The container image exists as groundwork for hosted mode. Hosted mode lets many
people use one server on Linux, with TLS (encrypted connections) and sign-in.
It comes in milestone M3. Until then, the image is useful for testing and for
building on, not for daily use.

## Why the container does not work for local use

1. **No keychain.** By default, email-mcp keeps its master key (the key that
   protects the stored passwords) in the keychain of your operating system. A
   container cannot reach the keychain of your computer. Inside the container
   you must use a key file instead, with `EMAIL_MCP_KEK=file:<path>`. Without
   it, `serve` stops with an error about the keychain.
2. **127.0.0.1 inside the container is not your computer.** Every container has
   its own network. Its loopback address (127.0.0.1) is reachable only from
   inside that container. So `email-mcp serve --http 127.0.0.1:8765` inside the
   container is a server that nothing outside the container can connect to.
   That is why the default command of the image is `email-mcp serve`, which
   uses stdio (standard input and output) and needs no network. HTTP inside a
   container waits for hosted mode (M3).
3. **Publishing the port does not help.** `docker run -p 8765:8765` forwards to
   the network interface of the container, not to its loopback address. And
   email-mcp refuses to listen on any address that is not a loopback address:
   `serve --http 0.0.0.0:8765` stops at once. This is on purpose: local HTTP
   mode has no TLS, so it must never be open to a network.
4. **The web interface is not reachable either.** `email-mcp setup` and the
   approval links open a page on 127.0.0.1 inside the container. Your browser
   cannot open it.

## What the image is

- It is built from `gcr.io/distroless/static-debian12:nonroot`: no shell, no
  package manager, and it runs as the user `nonroot` (UID 65532).
- Both base images (the Go image that compiles and the distroless image that
  runs) are pinned by digest (a hash of the exact image), so a build always
  uses the same files.
- The Dockerfile has no `# syntax=` line, so BuildKit (the build engine of
  Docker) reads it with its built-in frontend. No other image is downloaded to
  read the Dockerfile.
- In the release, the BuildKit builder image (in
  `.github/workflows/release.yml`) and the Syft scanner image that makes the
  SBOM of the image (in `.goreleaser.yaml`) are pinned by digest too.
- `ENTRYPOINT` is `/email-mcp`. `CMD` is `serve`: MCP over stdio. An MCP
  client runs the image with `docker run -i` (`-i` keeps standard input open),
  as it would run a local binary.
- The release workflow publishes it as `ghcr.io/emadmokhtar/email-mcp` for
  `linux/amd64` and `linux/arm64`. The image has the same binary as the signed
  release archives, an SBOM from Docker, and a GitHub build-provenance
  attestation. The README shows how to verify it.

## Build it yourself

```sh
docker build -t email-mcp:dev .
docker run --rm email-mcp:dev version
```

The Dockerfile compiles the binary from the source code by default. The
release uses the same Dockerfile with `--build-arg BINARY_FROM=release`: then
it copies the binary that GoReleaser built, instead of compiling a new one.

## Try it

This starts the server inside the container with a key file in the home
folder of the container. It serves MCP over stdio, so it reads MCP messages
from standard input (`-i`) and waits for them. It has no account, so it is only
a smoke test:

```sh
docker run --rm -i -e EMAIL_MCP_KEK=file:/home/nonroot/kek.key email-mcp:dev
```

The data (the database and the key file) is lost when the container stops,
because it is not on a volume.
