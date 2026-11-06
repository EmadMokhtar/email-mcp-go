# Docs site foundation: design

This document holds the decisions that all three parts of the docs site share.
Three specs build on it:

- [User manual](2026-11-05-user-manual-design.md)
- [Tech docs](2026-11-05-tech-docs-design.md)
- [Landing page](2026-11-05-landing-page-design.md)

## Why

Today one 1,120-line `README.md` does every job. A new user cannot quickly see
what email-mcp does. A person who wants to install it has to read past release
notes for maintainers. A developer has no overview of how the code fits
together.

## Readers

Most readers are users:

1. They open the landing page to learn what email-mcp does.
2. They use the manual to install it and to set up an account.
3. They come back to the manual while they use it.

A developer who needs to understand the code opens the tech docs.

## Goals

- One site on GitHub Pages with three parts: landing page, user manual, tech
  docs.
- The manual and the tech docs stay true, because a pull request that changes
  behaviour cannot merge without dealing with them.
- After the first publish, the site always matches what people can install.
- The manual shows real screens of the program, and automation updates them when
  the web pages change.
- The site follows the design of the portal and loads nothing from other sites.

## Non-goals

- Docs for more than one version at a time. The site shows the latest release.
- Translations.
- Search, in the first version. The sidebar and the find command of the browser
  are enough for the size of the manual. (My call.)
- Analytics or cookies of any kind. (My call.)

## Anti-goals

These would make the work a failure, even if the site builds:

- The manual describes a feature that people cannot install yet.
- Content from the README is lost in the move.
- The site claims something the code does not do, or invents proof. `PRODUCT.md`
  says that no customers, testimonials or benchmarks exist.
- New text describes a weakness that is not fixed, or gives attack steps.
- The check on pull requests is so noisy that people stop reading it.

## Decisions

### Made by the owner

- GitHub Pages hosts the site.
- The manual and the tech docs must be updated with every pull request. A pull
  request that changes behaviour and skips them is blocked from merging.
- Hugo builds the site, with our own small templates and no theme.
- The site is published at each release, not at each merge.
- The landing page leads with option A, "Show the decision": the headline "Your
  agent asks. You decide." and a picture of the approval page.
- The first publish is one manual run from `main`. It can include work that is
  not released yet. Every later publish comes from a release.
- A private security review checks the pages that make security claims before
  launch. Its findings stay out of the repository, issues and pull requests.
- The landing page says that text the agent reads goes to the AI app.
- The user manual has screenshots from the real application. Automation updates
  them when the UI changes.

### Made by the author of this document

Each of these can change at review:

- The site source lives in a new folder, `site/`. `docs/` keeps internal specs
  and plans.
- The check has two labels, one for each part. See "The check on pull
  requests".
- The site goes live after the release succeeds, not when the tag is pushed.
- The landing page headline may be larger than the 1.625rem limit of the portal.
- The README is cut down last, after the site is live.
- Step 1 delivers all shared templates and shortcodes. Later steps change content
  only.
- A step that is too large to review may split into several pull requests.
- Screenshots are made at build time and are never committed.

## Site structure

```text
site/
  hugo.toml
  content/
    _index.md          landing page
    manual/            user manual
    tech/              tech docs
  layouts/             our own templates
  assets/ or static/   one stylesheet, images
  assets/screenshots/  made at build time, not in git
```

| Part | Address |
|---|---|
| Landing page | `/` |
| User manual | `/manual/` |
| Tech docs | `/tech/` |

The base address is `https://emadmokhtar.github.io/email-mcp-go/` unless the
owner's Pages settings give a custom domain. The build takes the base address
from `actions/configure-pages`, so it is not written in `hugo.toml`.

Every page has the same top menu: `email-mcp`, Manual, Tech docs, GitHub. Manual
and tech docs pages also have a sidebar.

The footer shows the released version, read from
`.release-please-manifest.json` at build time.

## Look

- The colours, type, spacing and rules come from `DESIGN.md`. Its values become
  CSS custom properties in one stylesheet.
- Light and dark follow the setting of the computer.
- No web fonts, no CDN, no script or style from another site.
- A page works when no script runs.
- There is no logo. The name is `email-mcp`. The browser tab icon reuses
  `internal/web/static/icon.svg`, and no page shows it. (My call.)
- Diagrams are inline SVG or images built before commit, not a script that draws
  them in the browser.

## Content rules

- Files under `site/content/` follow these parts of
  `.github/markdown.instructions.md`: no H1 in the body, H2 and H3 only, fenced
  code blocks with a language, alt text on images, and lines of 80 characters.
  Its front matter fields belong to another project and do not apply.
- Front matter has `title`, `description` and `weight`. Manual and tech docs
  pages also have `code_areas`, the repository paths that the page covers. It
  is required, and the build fails when a path does not exist. Tech docs pages
  show it, and manual pages do not. `aliases` is allowed when a page moves. (My
  call.)
- The language stays as simple as the README: short sentences, plain verbs, and
  a definition after each technical term.
- Each part has its own glossary page.
- Every fact comes from the code or from an existing document. If the code and a
  document disagree, the code wins, and the difference is reported.
- Moved text follows the same rule, and that includes the security text. A
  sentence that no longer matches the code is corrected, and the pull request
  lists the correction. (My call.)
- Public pages describe what the code does, and they give no attack steps. New
  text does not describe weaknesses that are not fixed.
- Limits that the README already publishes, such as "Known limitations", stay,
  because hiding them would mislead users. (My call.)
- All three parts name level 5 the same way: "Delete", the name in the portal,
  with the explanation that it moves mail to the Trash and never deletes mail
  for good. (My call.)
- Sizes use the exact unit: "10 MiB" for a limit of `10 << 20` bytes. (My call.)

## Screenshots

The manual shows real screens, taken from the running program. Nobody takes them
by hand.

**How they are made.** `make screenshots` runs a Go test behind the build tag
`screenshots`, in `internal/e2e`. That package already opens the whole program
with `app.Open`, as `serve` does: the real database, the real web pages and a
real MCP client, with the test mail servers `imaptest` and `smtptest` on the
loopback address. So no Docker and no outside network are needed.

The test works in this order:

1. It fills the program through the real paths. It connects a sample account,
   and an MCP client calls real tools, so a send waits for approval.
2. A headless Chrome (Chrome with no window) opens a real sign-in link from
   `NewSetupURL`.
3. It opens each page and clicks through the real forms.
4. It saves each screen in light and dark.

The Go library chromedp, which controls Chrome, drives the browser. It fits
because the repository has no Node setup. It is a test dependency, so it does not
enter the released program. (My call.) CI uses the Chrome that the
`ubuntu-latest` runner already has.

**How they stay up to date.** The images are not in git. Every build makes them
again:

- The publish workflow makes them from the same commit that it publishes. So the
  manual always shows the screens of that release.
- The build job makes them on every pull request. A UI change that breaks a shot,
  such as a renamed button or a moved page, fails that required check. So the fix
  goes in the same pull request.
- The build job uploads the images as a workflow artifact (files that a run keeps
  for download). A reviewer can open them and see the new look.

(My call.) The other option was to commit the images and let a bot update them.
It was not chosen for three reasons. A bot would need write access to the
repository, and every other workflow is read-only by default. GitHub starts no
workflow for a commit made with `GITHUB_TOKEN`, so the required checks would not
run on the bot's commit. And each UI change would add binary files to the git
history.

A failed capture stops the publish, but not the release, because publishing is
the last job. The site then keeps its previous version.

**Rules.**

- Sample data only: `example.com` addresses and made-up text. No real account,
  password or token. The data ends with the test.
- A shortcode `screenshot`, with `name` and `alt`, prints both versions in a
  `<picture>` element, so the image follows light and dark. A missing image fails
  the build, and so does an image that no page uses.
- The steps in the text never depend on the image alone. A reader with no image
  can follow every step.
- Shots show the web pages of email-mcp only. Terminal output stays as text, and
  the windows of other apps are not shown.
- `go test ./...` does not run this test, because of the build tag. CI vets the
  tag, as it does for `interop`.

## Publishing

- A new workflow, `.github/workflows/pages.yml`, makes the screenshots, builds
  the site and deploys it. It offers `workflow_call` and `workflow_dispatch`.
- `release.yml` calls it as a last job, after the release job succeeds. So a
  failed release publishes nothing, and the site never points to files that do
  not exist yet.
- `workflow_dispatch` is for the first launch and for urgent fixes. It publishes
  whatever is on the chosen branch or tag, including work that is not released.
- The workflow follows the rules of the existing workflows: read-only
  permissions by default, actions pinned by full commit SHA, and no `${{ }}`
  value inside a `run:` script.
- The Hugo version is pinned in `.hugo-version`, in the same way as
  `.go-version`. The download is checked against the published checksum.
- `make screenshots` makes the images, and it needs Chrome. `make site` runs it
  and then builds the site. `make site-serve` starts a local preview.

## The check on pull requests

Two new jobs run on every pull request. They use no path filter, because a
required check that does not run blocks the merge forever.

**Build.** The job makes the screenshots, and then Hugo builds the site. A failed
screenshot, a broken internal link or a build warning fails the job.

**Docs check.** It looks at which files the pull request changes.

- A change counts as behaviour when it touches a non-test Go file in `cmd/` or
  `internal/`, or any file in `internal/web/templates/` or
  `internal/web/static/`. Test files, `internal/testutil`, `internal/e2e` and
  `internal/thirdparty` do not count.
- A change that alters only the look of a page can use the label, because the
  screenshots update by themselves.
- If behaviour changed, the pull request must either change a file under
  `site/content/manual/` or carry the label `no-manual-change`.
- It must also either change a file under `site/content/tech/` or carry the
  label `no-tech-docs-change`.
- The landing page is outside this rule.
- The job runs again when a label is added or removed.
- The failure message names the missing part and the label that would clear it.

Two labels, not one, because a single label would let a feature update the tech
docs and leave the manual out of date. Only people with write access can add
labels, so an outside contributor cannot clear the check alone.

A pull request template adds two checkboxes as a reminder. It links to the tech
docs page `local-development`, which holds both maps from code area to page
after the cutover. So an author knows which page to change. (My call.)

## Moving the existing docs

- User content from `README.md`, `DOCKER.md` and `docs/harnesses.md` moves to
  the manual.
- "Making a release" and "Development" move to the tech docs.
- `README.md` becomes a short page: what the tool is, the install command, and
  links to the site. It stays in the release archives through
  `.goreleaser.yaml`.
- `DOCKER.md` and `docs/harnesses.md` are deleted after the move.
- Comments that say "see the README" change to point to the new place. They are
  in `.github/workflows/`, `.goreleaser.yaml`, `Dockerfile` and
  `internal/web/settings.go`.
- `websiteUrl` in `server.json` changes to the site address.

## Order of work

Each step is one pull request, except step 5. A step that is too large to review
may split into several pull requests, each with its own issue. The tech docs
split by sidebar group.

1. Foundation: Hugo, all shared templates and shortcodes (sidebar, previous and
   next links, `src`, `diagram`, `screenshot`, the display and the check of
   `code_areas`), the screenshot test with one shot, stylesheet, build job,
   publish workflow, Make targets, and placeholder pages for `/manual/` and
   `/tech/`, so the menu links work.
2. User manual content.
3. Tech docs content.
4. Landing page.
5. Launch, with no new content pull request unless a check below needs one:
   - Catch-up check. List every change to `README.md`, `DOCKER.md` and
     `docs/harnesses.md`, and every `fix` or `feat` commit, since the commit that
     the specs were checked against (`e254516`). Bring the site up to date.
   - Private security review of the pages that make security claims. A page that
     does not pass waits for a later release.
   - The owner does the steps below, and one manual run from `main` publishes
     the site.
6. Cutover: shorten the README, delete the moved files, fix the comments, add
   the docs check and the pull request template.

Steps 2 to 4 change content only, except that step 2 also adds its shots to the
screenshot test. A link to a page that does not exist yet waits for the pull
request that adds that page, because a broken internal link fails the build.

The docs check comes last on purpose. Before that, the folders it checks are
still being filled, and the catch-up check does its job. The README stays whole
until step 6, so no reader meets a dead link.

## Checks by people

Some definitions of done name a person. These rules apply to all three parts.
(My call.)

- A fresh agent that has not read the project files may stand in for a new
  reader.
- A walk-through of a how-to page uses the test mail servers in `test/interop`,
  not a real account.
- "Checked against the code or a real build" covers the commands and buttons of
  email-mcp. Facts about other apps keep their date and are not checked again.
- An agent checks the accessibility tree of each page. The owner may add a
  VoiceOver pass at review.

## Steps only the owner can do

| When | Step |
|---|---|
| After step 1 | Add the build job to the required checks of `main`. |
| Before step 5 | Set the Pages source to "GitHub Actions". |
| Before step 5 | Allow tags that match `v*` to deploy to the `github-pages` environment. By default it accepts the default branch only, so a release would fail to deploy without this. |
| Before step 6 | Create the labels `no-manual-change` and `no-tech-docs-change`. |
| After step 6 merges | Add the docs check to the required checks of `main`. A required check that has no job blocks every merge, so this comes last. |

## Rules for each pull request

- One GitHub issue for each pull request, assigned to `EmadMokhtar`, with
  `Closes #N` in the body.
- The pull request is assigned to `EmadMokhtar`.
- Commit messages and pull request titles use Conventional Commits. Content is
  `docs:`, and workflow changes are `ci:`.

## Left to the builder

- The exact Hugo templates and the structure of the stylesheet.
- How the docs check reads the changed files and the labels.
- How the version gets from the manifest into the footer.
- The sample data, the width and the crop of each screenshot.
- `assets/` or `static/`.
