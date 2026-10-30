# The container image of email-mcp.
#
# There is no "# syntax=" line on purpose: such a line makes BuildKit download
# a Dockerfile frontend image (the program that reads this file) by a tag that
# can change. Without it, BuildKit uses its built-in frontend. This file needs
# nothing more: ARG before FROM, FROM --platform and COPY --from are built in.
#
# For local use, install the binary instead (see the README). In a container,
# email-mcp cannot reach the keychain of your computer, and the address
# 127.0.0.1 inside the container is the container itself: the computer that runs
# the container cannot reach a server that listens there. So local HTTP mode and
# the web interface are reachable only from inside the container. The default
# command below serves MCP over standard input and output (stdio), which an MCP
# client can use with "docker run -i". This image is groundwork for hosted mode,
# which comes in milestone M3. DOCKER.md explains this in more detail.
#
# One Dockerfile serves two builds. The build argument BINARY_FROM chooses
# where the binary comes from:
#   - "source" (the default): "docker build ." compiles the binary from the
#     source code in the "source" stage.
#   - "release": GoReleaser has already built the binary (the same file as in
#     the signed release archives) and puts it in the build context at
#     <os>/<arch>/email-mcp. The "release" stage copies it.
# BuildKit builds only the stages that the final image needs, so a build never
# runs the stage that it does not use.

ARG BINARY_FROM=source

# The Go version is the latest stable release, the same as the "stable" Go of
# the CI. The digest pins the exact image (all platforms). To update it, run:
#   docker buildx imagetools inspect golang:<version>-trixie
FROM --platform=$BUILDPLATFORM golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385 AS source
WORKDIR /src
# Download the modules first, so Docker can reuse this layer while go.mod and
# go.sum do not change.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# TARGETOS and TARGETARCH come from BuildKit and describe the image that is
# being built. The compiler runs on the build machine ($BUILDPLATFORM) and
# cross-compiles, so no emulation is needed.
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/EmadMokhtar/email-mcp-go/internal/version.Version=${VERSION}" \
    -o /out/email-mcp ./cmd/email-mcp

FROM scratch AS release
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/email-mcp /out/email-mcp

FROM ${BINARY_FROM} AS binary

# The runtime image has no shell and no package manager, and it runs as the
# user "nonroot" (UID 65532). The digest pins the exact image. To update it,
# run:
#   docker buildx imagetools inspect gcr.io/distroless/static-debian12:nonroot
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=binary /out/email-mcp /email-mcp
USER nonroot:nonroot
ENTRYPOINT ["/email-mcp"]
# The default command serves MCP over stdio. An MCP client starts the container
# with "docker run -i" (-i keeps standard input open) and talks to it as it
# talks to a local binary. HTTP inside a container waits for hosted mode (M3):
# local HTTP mode listens only on a loopback address, and 127.0.0.1 here is the
# loopback address of the container, not of your computer. Publishing the port
# with "docker run -p" does not help either. Hosted mode adds a server that may
# listen on other addresses, behind TLS and sign-in.
CMD ["serve"]
