---
title: Verifying a release
description: Check the signature and the checksum of a release that you downloaded.
weight: 70
code_areas:
  - .goreleaser.yaml
  - .github/workflows/release.yml
---

After this page, you can check that a release archive that you downloaded is the
file that the release workflow of email-mcp built. An archive is one file that
holds the program. A workflow is a list of steps that GitHub Actions (the
automation service of GitHub) runs. The workflow that builds and signs a
release is {{< src ".github/workflows/release.yml" >}}. Do the checks in a
terminal (a window where you type commands).

Each release is built by GitHub Actions from a tag (a name that marks one
version of the code). It comes with:

- `checksums.txt`: the SHA-256 (Secure Hash Algorithm with 256 bits) checksum of
  every archive and every SBOM. A checksum is a short code that a function
  makes from the content of a file, so another content gives another code;
- `checksums.txt.sigstore.json`: a cosign (a tool that signs files and checks
  signatures) signature bundle (the file that holds the signature and its
  certificate) for `checksums.txt`;
- one SBOM (software bill of materials: the list of every module in the
  binary) per archive, made with Syft (a tool that lists the modules of a
  program);
- GitHub build-provenance attestations: signed records that say which workflow
  built which file from which commit.

cosign signs "keyless": the signature uses a short-lived certificate for the
identity of the release workflow, issued by Sigstore (a public service that
gives such certificates). There is no long-lived key to steal.

Where a command below writes `<version>`, write the version of the release that
you downloaded, such as 0.6.0. The tag of a release is the version with a `v` in
front, so the tag of version 0.6.0 is v0.6.0. The commands check the archive for
macOS on Apple silicon, `darwin_arm64`. Use the name of your own archive.

## 1. Check the signature of the checksums file

Download the archive, `checksums.txt` and `checksums.txt.sigstore.json` from the
[GitHub releases](https://github.com/EmadMokhtar/email-mcp-go/releases) page.
Install [cosign](https://docs.sigstore.dev/cosign/system_config/installation/).
Put the archive, `checksums.txt` and `checksums.txt.sigstore.json` in one
folder. The next command checks the signature of `checksums.txt`. The release
workflow runs the same command. `--certificate-identity` is the identity that
must have signed the file: the release workflow, at the tag of the release.
`--certificate-oidc-issuer` is the service that vouches for that identity:
GitHub Actions. `--bundle` is the file that holds the signature and the
certificate. Replace `<version>` with the version of the release:

```sh
cosign verify-blob \
  --certificate-identity 'https://github.com/EmadMokhtar/email-mcp-go/.github/workflows/release.yml@refs/tags/v<version>' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --bundle checksums.txt.sigstore.json \
  checksums.txt
```

It must print `Verified OK`.

## 2. Then check the archive against the checksums file

Run this in the same folder. `--check` reads the checksums from the file, and
`--ignore-missing` skips the archives that you did not download:

```sh
sha256sum --ignore-missing --check checksums.txt
```

It prints the name of your archive and `OK`.

## 3. Optional: check the provenance attestation

Use the GitHub CLI (the `gh` program: a command line tool for GitHub). The
command checks that GitHub holds a signed record for this file. `--repo` is the
repository that must have built the file. When the check works, the command ends
with no error. When it does not, it ends with an error:

```sh
gh attestation verify email-mcp_<version>_darwin_arm64.tar.gz --repo EmadMokhtar/email-mcp-go
```

## The container image

The container image has its own attestation in the registry. A container image
is a package that holds a program, to run in a container. `oci://` names an
image in a registry (a server that stores container images). See
[Containers](/manual/reference/containers/) for what the image is.

```sh
gh attestation verify oci://ghcr.io/emadmokhtar/email-mcp:<version> --repo EmadMokhtar/email-mcp-go
```
