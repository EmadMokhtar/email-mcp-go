---
title: Making a release
description: How a release of email-mcp is made, step by step, what it publishes, what to do when it fails, and how the docs site is published.
weight: 30
code_areas:
  - .github/workflows/release-please.yml
  - .github/workflows/release.yml
  - .github/workflows/pages.yml
  - .goreleaser.yaml
  - release-please-config.json
  - .release-please-manifest.json
  - server.json
  - CHANGELOG.md
  - Dockerfile
  - internal/version/version.go
---

This page is for maintainers: the people who make the releases of email-mcp. It
explains what must be set up once, the steps of each release, how the version
goes up, and what to do when a release fails. Releases come from
[release-please](https://github.com/googleapis/release-please), which reads the
[Conventional Commits](https://www.conventionalcommits.org/) on `main` (commit
messages in the format `type(scope): description`). The workflow
{{< src ".github/workflows/release-please.yml" >}} keeps one release pull
request open, and {{< src ".github/workflows/release.yml" >}} builds and
publishes every tag that starts with `v`. A workflow is a list of steps that
GitHub Actions (the automation service of GitHub) runs. A tag is a name that
marks one commit, such as `vX.Y.Z`.

## What a release makes

GoReleaser, a tool that builds and publishes Go programs, makes the release. Its
settings are in {{< src ".goreleaser.yaml" >}}:

- archives of the program for five platforms: Linux on amd64 and arm64, macOS
  on arm64 and amd64, and Windows on amd64. Each one is a `.tar.gz`, or a
  `.zip` for Windows, named `email-mcp_<version>_<os>_<arch>`, with `LICENSE`
  and `README.md`;
- one SBOM (software bill of materials: the list of every module inside a
  binary) for each archive, made with Syft (a tool that lists the modules of a
  program);
- `checksums.txt`, with the SHA-256 (Secure Hash Algorithm, 256 bits) checksum
  of every archive and every SBOM, and its cosign signature bundle
  `checksums.txt.sigstore.json` (cosign is a tool that signs files). cosign
  signs "keyless": it gets a short-lived certificate from Sigstore (a public
  service that gives such certificates) for the identity of the release
  workflow, so there is no long-lived signing key to look after. The signature
  shows which workflow, at which tag, made the files. It is as strong as the
  control of that workflow, of the tags and of the `release` environment;
- the Homebrew cask (the install recipe of Homebrew) in the tap
  `EmadMokhtar/homebrew-tap` (a repository that holds Homebrew recipes);
- the container image `ghcr.io/emadmokhtar/email-mcp` on GHCR (GitHub Container
  Registry), for `linux/amd64` and `linux/arm64`;
- GitHub build-provenance attestations (signed records that say which workflow
  built which file from which commit) for every file in `checksums.txt` and
  for the image. The image has an attestation. It has no cosign signature.

The binary is static (`CGO_ENABLED=0`: it needs no system library to run), and
`-trimpath` removes the file paths of the build machine from it. The binaries
take the time of the commit, not the time of the build (`mod_timestamp`). These
settings remove two common sources of difference between builds. They do not
promise that two builds of the same commit are identical bit for bit. To check
a download, use the signature and the attestation (see [Verifying a
release][verify]), not a build of your own. Before the build,
`go mod tidy -diff` stops the release when `go.mod` or `go.sum` is not tidy
(when they list a module that the code does not need, or miss one that it
needs).

The version comes from the tag. GoReleaser sets it with this linker flag (an
option that sets the value of a variable when the binary is built), where
`{{ .Version }}` is the version of the tag without the `v`:

```text
-X github.com/EmadMokhtar/email-mcp-go/internal/version.Version={{ .Version }}
```

`Version` in {{< src "internal/version/version.go" >}} is `dev` for a build
that does not set it. `email-mcp version` prints it, and the server sends it to
MCP (Model Context Protocol) clients.

The manual has the user's side:
[Install email-mcp](/manual/getting-started/install/) and
[Verifying a release][verify].

## Once, before the first release

An environment is a set of rules and secrets of the repository that a job
names: GitHub gives the secrets only to a job that names it, and only when the
rules allow the run.

1. Make the tap repository `EmadMokhtar/homebrew-tap` on GitHub.
2. Make a fine-grained personal access token that may write the contents of
   that repository only.
3. In this repository, under **Settings → Environments**, make an environment
   named `release`. Allow only tags that match `v*` to deploy to it. Under
   "Required reviewers", add yourself, so that every release waits for your
   approval. Add the token there as the secret `HOMEBREW_TAP_GITHUB_TOKEN`.
   Only a job that uses this environment can read the token, and only after
   the tag rule and the approval. Keep the list of people who can write to
   the repository short.
4. Make a ruleset named "release tags" for the tags that match `v*`. Turn on
   "Restrict updates", "Restrict deletions" and "Block force pushes". The
   ruleset then blocks moving and deleting a published tag.
5. Give release-please its own token through a GitHub App (an account for a
   program, with the permissions that its owner gives it). The `GITHUB_TOKEN`
   of the workflow is not enough: GitHub starts no workflow for a pull request
   or a tag that `GITHUB_TOKEN` creates. Then the required checks would never
   run on the release pull request, and the tag would not start
   `release.yml`.
   1. Under your account, **Settings → Developer settings → GitHub Apps → New
      GitHub App**, make an App, for example `email-mcp-release-please`. Turn
      off **Webhook**. Under **Repository permissions** set **Contents** and
      **Pull requests** to "Read and write" and leave everything else at "No
      access". Under "Where can this GitHub App be installed?" choose "Only on
      this account".
   2. On the page of the App, note the **Client ID**. Under **Private keys**,
      generate a key. GitHub downloads it as a `.pem` file.
   3. **Install App** → install it on your account, with "Only select
      repositories" and only `EmadMokhtar/email-mcp-go`.
6. Make a second environment named `release-please`. Allow only the branch
   `main` to deploy to it. Add two secrets there:
   `RELEASE_PLEASE_APP_CLIENT_ID` (the Client ID) and
   `RELEASE_PLEASE_APP_PRIVATE_KEY` (the whole content of the `.pem` file).
   Then delete the `.pem` file. The workflow makes a token for each run. It is
   valid for one hour and is revoked when the job ends.

   A simpler alternative is a fine-grained personal access token, with access
   to this repository only and **Contents** and **Pull requests** set to "Read
   and write". Add it to the environment as the secret
   `RELEASE_PLEASE_TOKEN`. In `release-please.yml`, remove the step "get a
   token of the GitHub App" and set the `token` of release-please to
   `${{ secrets.RELEASE_PLEASE_TOKEN }}`. The cost: the token lives until it
   expires, so you must renew it, and the release pull requests show your
   name as the author. Choose the shortest expiry that you can renew.
7. For the docs site, under **Settings → Pages**, set the source to "GitHub
   Actions".
8. In the environment `github-pages`, allow the tags that match `v*` to deploy.
   By default that environment accepts only the default branch, so a run of
   `pages.yml` on a tag cannot deploy without this.

## For each release

1. Merge pull requests into `main` as usual. Their titles must be Conventional
   Commits, because a squash merge makes the title the commit message.
2. release-please opens or updates the pull request
   `chore(main): release X.Y.Z`. It adds the changes to the changelog
   {{< src "CHANGELOG.md" >}} (the list of changes of each release) and sets the
   version in {{< src ".release-please-manifest.json" >}} and
   {{< src "server.json" >}}.
3. When you want to release, merge that pull request. release-please then
   pushes the tag `vX.Y.Z` and creates a draft GitHub release with the
   changelog. Only maintainers can see a draft release, but the tag is public
   from that moment: `go install ...@vX.Y.Z` works, and the ruleset "release
   tags" blocks moving and deleting it. So merge the release pull request only
   when `main` is ready to ship. Run `make test` and `make vuln` on `main`
   first.
4. The tag starts `release.yml`. GitHub asks you to approve the deployment to
   the `release` environment. Before you approve, open the run and check the
   commit and the workflow file. The release files, the image and the cask are
   published only after your approval. With one maintainer, leave "Prevent
   self-review" off: the approval is then a pause to check, not a review by
   a second person.

The release notes come from one place: release-please writes them into the
draft from `CHANGELOG.md`, and GoReleaser keeps them (`release.mode:
keep-existing` in `.goreleaser.yaml`) while it adds its files. GoReleaser finds
the draft by its name, which is the tag (`use_existing_draft` and
`name_template`), and {{< src "release-please-config.json" >}} sets
`include-v-in-release-name` for the same reason.

{{< diagram "release-flow" >}}

The drawing shows the steps of this list, the jobs of `release.yml` (see
[What the release workflow does](#what-the-release-workflow-does)), the three
cases of [When the release fails](#when-the-release-fails), and the site (see
[How the site is published](#how-the-site-is-published)).

## How the version goes up

While the version is below 1.0.0, `fix:` and `perf:` raise the patch number
(0.1.0 → 0.1.1). `feat:` and breaking changes (`feat!:` or a
`BREAKING CHANGE:` footer) raise the minor number (0.1.0 → 0.2.0). Dependabot
updates of Go modules come as `fix(deps):`, so a security fix in a dependency
leads to a proposed patch release, which you then merge (see
[Dependabot](/tech/work-on-the-code/local-development/#dependabot)). Other
types, such as `docs:`, `ci:` and the `build(deps):` pull requests of
Dependabot for actions and Docker images, make no release alone and do not
show in the changelog. To choose the version yourself, for example a
pre-release, put the footer `Release-As: 0.2.0-rc.1` in the message of a
commit on `main`.

`bump-minor-pre-major` in `release-please-config.json` is the setting that
makes a breaking change raise the minor number, and not the major number,
below 1.0.0.

## What the release workflow does

`release.yml` runs `go vet`, the tests and `govulncheck` (a check for known
vulnerabilities), and checks that `server.json` has the version of the tag.
Then GoReleaser builds and signs the files, uploads them into the draft
release and publishes it, together with the Homebrew cask and the image. The
workflow adds the attestations. A tag with a pre-release part, such as
`v0.2.0-rc.1`, makes a GitHub pre-release, and does not update the tap or the
`latest` image tag.

The workflow has two jobs:

| Job | Permissions | Steps |
|---|---|---|
| `vet, test and govulncheck` (`check`) | `contents: read` | `go vet ./...`, `go test -race ./...`, govulncheck, and the version check of `server.json` |
| `GoReleaser` (`release`), after `check`, in the environment `release` | `contents: write`, `packages: write`, `id-token: write`, `attestations: write` | install cosign and Syft; set up Buildx (the Docker tool that builds an image for several platforms); sign in to GHCR; GoReleaser; check the signature of `checksums.txt` as the manual tells users to; attest the release files; find the one digest of the image; attest the image |

The token of the workflow may only read, and only the release job gets the
write permissions that it needs. Two releases of one tag never run at the same
time (`concurrency`). Both jobs use no module cache, so a release never uses
files that another workflow run wrote. The versions of GoReleaser, cosign,
Syft and govulncheck are pinned by version in the workflow, and the actions
are pinned by commit SHA (the hash that names one commit). The Go version is
not pinned: the jobs install the newest stable Go, to get the fixes of the
standard library. A pin also stops fixes from arriving by themselves, so
update the pins on purpose and read what changed.

## When the release fails

- **Before GoReleaser publishes the release** (the checks, the build, the
  image, or the upload into the draft): fix the cause, for example an expired
  token, and re-run the failed jobs. GoReleaser finds the same draft again and
  replaces the files that the failed run had uploaded
  (`replace_existing_artifacts`).
- **When the code must change** (a test or `govulncheck` fails, or
  `server.json` does not have the version of the tag): the tag cannot be
  moved, so this version cannot be released. Merge the fix into `main` as a
  `fix:` pull request. release-please then proposes the next patch version.
  Delete the old draft release by hand.
- **After GoReleaser published the release** (the Homebrew cask comes after
  the release inside GoReleaser, and the workflow checks the signature and adds
  the attestations after GoReleaser): a re-run finds no draft and stops. First
  turn the release back into a draft with `gh release edit vX.Y.Z --draft=true`,
  then re-run the failed jobs.

A tag pushed by hand still makes a release: GoReleaser then creates the
release itself. Set `version` in `server.json` and in
`.release-please-manifest.json` to the version first. Otherwise release-please
counts from the old version and proposes a release that already exists.

## Try the build on your computer

To try the build on your computer without publishing anything, run these two
commands. `go run tool@version` downloads and builds that version of
GoReleaser the first time, so there is nothing to install. The first command
checks `.goreleaser.yaml`. The second builds a snapshot (a build with a test
version, not from a tag): `--clean` deletes the folder `dist/` first, and
`--skip=sign,sbom,publish` leaves out the signature, the SBOMs and every
upload. cosign and Syft are not needed for this:

```sh
go run github.com/goreleaser/goreleaser/v2@v2.19.0 check
go run github.com/goreleaser/goreleaser/v2@v2.19.0 release --snapshot --clean --skip=sign,sbom,publish
```

The files are in `dist/`. A snapshot also builds the image with the local
image store of Docker, so Docker must run. The version of GoReleaser in these
commands is the version of `release.yml` and `ci.yml`.

## The container image

- It is built from `gcr.io/distroless/static-debian12:nonroot`: no shell, no
  package manager, and it runs as the user `nonroot` (UID 65532; a UID is the
  number of a user).
- Both base images (the Go image that compiles and the distroless image that
  runs) are pinned by digest (a hash of the exact image), so a build always
  uses the same files.
- The Dockerfile has no `# syntax=` line, so BuildKit (the build engine of
  Docker) reads it with its built-in frontend. No other image is downloaded to
  read the Dockerfile.
- In the release, the BuildKit builder image (in
  `.github/workflows/release.yml`) and the Syft scanner image that makes the
  SBOM of the image (in `.goreleaser.yaml`) are pinned by digest too.

The release builds the image from the same {{< src "Dockerfile" >}} as a
local build, with `--build-arg BINARY_FROM=release` (`dockers_v2` in
`.goreleaser.yaml`). The build argument `BINARY_FROM` chooses the stage that
gives the binary. The default, `source`, compiles it. `release` copies the
binary that GoReleaser built and put in the build context at
`<os>/<arch>/email-mcp`, so the image holds the same file as the signed
archives. That stage only copies files, so the release needs no emulator for
the second platform. The image gets two tags: the version, and `latest` when
the version is not a pre-release.

The SBOM of the image comes from the flag `--attest=type=sbom` with the scanner
image pinned by digest, and not from the option `sbom: true`, which would run a
scanner image by a tag that can change. A snapshot leaves the flag out, because
the local image store cannot keep an SBOM. Dependabot does not update the
builder image or the scanner image: the comments in `release.yml` and
`.goreleaser.yaml` give the command to look up a digest.

## How the site is published

{{< src ".github/workflows/pages.yml" >}} builds the docs site with
`make site` (the images, Hugo and the site check) and deploys it to GitHub
Pages. It has two jobs:

- `build` (`contents: read`, `pages: read`) reads the address of the site from
  the settings of Pages, and runs `make site` with it. A run on a tag points the
  links to the code at the tag, and any other run points them at the commit.
  The step stops when the address is empty. Then it uploads `site/public`.
- `deploy` runs in the environment `github-pages`, with `pages: write` and
  `id-token: write`, and deploys the upload.

`release.yml` has no job that calls `pages.yml`, so a release does not publish
the site. A maintainer starts `pages.yml` by hand (`workflow_dispatch`) on the
branch or the tag to publish. It publishes whatever is on that branch or tag,
also work that is not released. The environment `github-pages` accepts the
default branch, and the tags of step 8 of
[Once, before the first release](#once-before-the-first-release). `pages.yml`
also accepts `workflow_call`, so another workflow can call it. The calling job
must give it `pages: write` and `id-token: write`, because a called workflow
cannot get more permissions than its caller gives it. Two deploys never run at
the same time, and a deploy that has started is not cancelled, so the site is
not left half published.

## The MCP Registry

`server.json` describes the server for the
[official MCP Registry](https://registry.modelcontextprotocol.io) as
`io.github.EmadMokhtar/email-mcp`. It lists no package on purpose. The registry
supports npm, PyPI, NuGet, Cargo, OCI (container images) and MCPB (MCP Bundle)
packages. None of them fits a local Go binary that needs your keychain (the
password store of the operating system) and the `email-mcp setup` command: an
OCI image runs in a container, which has no keychain and no reachable web pages
(see [Containers](/manual/reference/containers/)), and one MCPB bundle cannot
choose between the Linux amd64 and arm64 binaries. So the entry points to the
README of the repository for the install (`websiteUrl`). Publishing to the
registry is a manual step with `mcp-publisher publish`, after the release.

## Related pages

- [Local development](/tech/work-on-the-code/local-development/)
- [Testing](/tech/work-on-the-code/testing/)
- [Install email-mcp](/manual/getting-started/install/) in the user manual
- [Verifying a release][verify] in the user manual

[verify]: /manual/reference/verify-a-release/
