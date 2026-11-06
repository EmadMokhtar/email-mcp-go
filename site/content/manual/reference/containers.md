---
title: Containers
description: Learn why the container image of email-mcp is not for daily use on your own computer.
weight: 80
code_areas:
  - Dockerfile
  - internal/app/kek.go
  - cmd/email-mcp/serve.go
---

After this page, you know why you should not use the container image of
email-mcp on your own computer, and what the image is. A container is a way to
run a program in its own closed space. An image is the package that holds the
program, to run in a container.

**Short version: for use on your own computer, do not use the container.
Install the program instead (see [Install email-mcp][install]).**

The container image is for testing and for building on, not for daily use. A
hosted mode for many people (one server that many people use) is planned. It is
not available. The tech docs page Local development explains how to build the
image and try it.

## Why the container does not work for local use

1. **No keychain.** By default, email-mcp keeps its master key (the key that
   protects the stored passwords) in the keychain of your operating system (its
   password store). A container cannot reach the keychain of your computer.
   Inside the container you must use a key file instead, with
   `EMAIL_MCP_KEK=file:<path>`. `EMAIL_MCP_KEK` is an environment variable (a
   named setting that a program reads from its surroundings) that says where
   the master key is. Without it, `serve` stops with an error about the
   keychain. See [The master key](/manual/reference/master-key/).
2. **127.0.0.1 inside the container is not your computer.** Every container has
   its own network. Its loopback address (127.0.0.1) is reachable only from
   inside that container (see the sources below). So `email-mcp serve --http
   127.0.0.1:8765` inside the container is a server that nothing outside the
   container can connect to. That is why the default command of the image is
   `email-mcp serve`, which uses stdio (standard input and output) and needs no
   network. Local HTTP mode does not work inside a container. See [Local HTTP
   mode](/manual/reference/local-http-mode/).
3. **Publishing the port does not help.** `docker run -p 8765:8765` publishes a
   port of the container on your computer (see the sources below). email-mcp
   refuses to listen on any address that is not a loopback address:
   `serve --http 0.0.0.0:8765` stops at once. This is on purpose: local HTTP
   mode has no TLS (Transport Layer Security: no encryption), so it must never
   be open to a network.
4. **The web interface is not reachable either.** `email-mcp setup` and the
   approval links use the name `email-mcp.localhost`, which the server answers
   only on 127.0.0.1 inside the container. Your browser, outside the container,
   cannot open it.

## What the image is

- By default, the image starts `/email-mcp serve`: MCP (Model Context Protocol)
  over stdio. In the Dockerfile (the file that says how to build the image),
  `ENTRYPOINT` is `/email-mcp` and `CMD` is `serve`. An MCP client runs the
  image with `docker run -i` (`-i` keeps standard input open), as it would run
  a local binary.
- The release workflow publishes it as `ghcr.io/emadmokhtar/email-mcp` (the
  GitHub Container Registry) for `linux/amd64` and `linux/arm64`. The image has
  the same binary as the signed release archives, an SBOM (software bill of
  materials: a list of every module inside a binary) from Docker, and a GitHub
  build-provenance attestation (a signed record of which workflow built the
  image). The page [Verifying a
  release](/manual/reference/verify-a-release/#the-container-image) shows how to
  verify it.

## Sources

The statements about the network of a container, about publishing a port and
about the option `-i` come from the Docker documentation, checked on
2026-11-06:

- "Networking overview"
  (<https://docs.docker.com/engine/network/>): it says that `--dns=127.0.0.1`
  refers to the container's own loopback address.
- "Port publishing and mapping"
  (<https://docs.docker.com/engine/network/port-publishing/>): publishing maps a
  container port to a port on the Docker host.
- "docker container run"
  (<https://docs.docker.com/reference/cli/docker/container/run/>): `-i` keeps
  standard input open.

[install]: /manual/getting-started/install/
