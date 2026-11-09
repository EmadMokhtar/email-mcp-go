---
title: Update email-mcp
description: Install a newer version of email-mcp, keep your accounts and settings, and go back to an older version if you need to.
weight: 45
code_areas:
  - .goreleaser.yaml
  - internal/version/
  - internal/store/migrate.go
  - internal/store/store.go
  - cmd/email-mcp/doctor.go
---

After this page, you can install a newer version of email-mcp. Your accounts,
your settings and the activity log stay. You do not connect your accounts
again.

## What an update changes

- An update replaces only the program file. The data folder (the folder where
  email-mcp keeps its data, see [Where the data lives][data]), the master key
  (the key that protects your stored passwords, see [The master key][key]) and
  the config of your AI (artificial intelligence) app stay as they are.
- A new version can need a new layout of the database. The first command of
  the new version that opens the data folder, such as `serve`, `setup` or
  `doctor`, changes the layout by itself. Each of these steps is a migration (a
  change of the database to the layout that a version needs). Each migration
  runs in one transaction (a group of changes that is saved completely or not
  at all). When a migration fails, the database stays as it was before that
  migration.
- After a newer version has opened the data folder, an older version refuses
  it. So to go back to an older version, you need a copy of the data folder
  that you made before the update. See
  [Go back to an older version](#go-back-to-an-older-version).
- email-mcp does not look for new versions by itself. You check for them.

## Find out if there is a new version

1. Run this command. It prints the name and the version of the program:

   ```sh
   email-mcp version
   ```

   You should see `email-mcp` and the version, for example `email-mcp 0.6.2`.
   A program that you built with `go install` or from the source code prints
   `email-mcp dev`. See [Go](#go) for how to find its version.
2. Compare it with the newest version on the
   [GitHub releases](https://github.com/EmadMokhtar/email-mcp-go/releases).
   GitHub can tell you about each new release: on the page of the repository
   (the project on GitHub), choose **Watch**, then **Custom**, tick
   **Releases**, and choose **Apply**.
3. Read the release notes (the list of changes of a release) of each version
   after yours. The file
   {{< src "CHANGELOG.md" >}} (the list of changes of each release) holds the
   same notes. While the version is below 1.0.0, a new middle number (such as
   0.6.0 after 0.5.2) can hold a breaking change (a change that can need a step
   from you). The notes list it under "⚠ BREAKING CHANGES". A new last number
   (such as 0.6.2 after 0.6.1) has no breaking change.

## Before you update

1. Stop every program that uses the data folder: `serve`, `setup`, and the AI
   apps that start email-mcp. A program that still runs keeps the old version
   in memory, and the old version does not know the new layout of the
   database. On Windows, a running program file cannot be replaced.

   A stop can take some time. email-mcp first sends the messages whose undo
   window (the time in which you can still cancel a send) ends within 5
   minutes. A stop usually takes a few seconds, and at most about 20 minutes.
   The messages that are left stay queued, and the new version sends them. See
   [When email-mcp stops][stops].
2. Check that no email-mcp program runs. On macOS and Linux, this command lists
   the running programs whose name contains `email-mcp`. `-l` prints the name
   of each program after its process ID (the number of a running program):

   ```sh
   pgrep -l email-mcp
   ```

   It prints nothing when no email-mcp runs. A line such as
   `29057 email-mcp` is a program that still runs: find the app or the
   terminal that started it, and stop it. In the Command Prompt on Windows,
   this command lists the running programs with the name `email-mcp.exe`.
   `/FI` filters the list:

   ```bat
   tasklist /FI "IMAGENAME eq email-mcp.exe"
   ```

   It prints "INFO: No tasks are running which match the specified criteria."
   when no email-mcp runs.
3. Copy the data folder. This step is optional, but without the copy you
   cannot go back to the older version. The commands below copy the folder to
   `email-mcp-backup` in your home folder. `-R` copies the folder and
   everything in it. `-p` keeps the modes (who may read and write a file) and
   the times of the files. The second path must not exist yet. On macOS, run:

   ```sh
   cp -Rp "$HOME/Library/Application Support/email-mcp" "$HOME/email-mcp-backup"
   ```

   On Linux, run this command. `${XDG_DATA_HOME:-$HOME/.local/share}` is the
   folder in `XDG_DATA_HOME`, or `~/.local/share` when `XDG_DATA_HOME` is not
   set:

   ```sh
   cp -Rp "${XDG_DATA_HOME:-$HOME/.local/share}/email-mcp" "$HOME/email-mcp-backup"
   ```

   The commands print nothing when they work. On Windows, type `%AppData%` in
   the address bar of the file explorer, and copy the folder `email-mcp` to
   another place. If you use `--data-dir` or `EMAIL_MCP_DATA_DIR`, copy that
   folder instead.

   The copy holds your encrypted passwords, so keep it private, as the data
   folder. The master key is not in the copy. The copy opens only with the
   master key that you used when you made it, so keep that key while you keep
   the copy.

## Install the new version

Use the same way that you used to install email-mcp. Each way below puts the
new version where the old one was. So the config of your AI app needs no
change.

### Homebrew

The first command updates the list of packages of Homebrew (a package manager:
a program that installs other programs), also the list of the tap (a repository
of Homebrew packages) `EmadMokhtar/homebrew-tap`. Without it, Homebrew may not
know the new version yet. The second command installs the new version of the
cask (a package of a ready-made program). `--cask` says that the package is a
ready-made program:

```sh
brew update
brew upgrade --cask emadmokhtar/tap/email-mcp
```

When it ends without an error, go to [After the update](#after-the-update).
When the newest version is already installed, Homebrew says "Not upgrading
email-mcp, the latest version is already installed".

The cask removes the macOS "quarantine" mark of the new version too, as it did
at the install (see [Install email-mcp][install-brew]). The path of the program,
such as `/opt/homebrew/bin/email-mcp`, stays the same. It is a link to the
program in a folder of Homebrew. Do not put the path of that folder, such as
`/opt/homebrew/Caskroom/email-mcp/0.6.2/email-mcp`, in the config of your AI
app: it holds the version number, so it changes with each version.

### A release archive

Do the steps of [A release archive][install-archive] again, with the archive
(the file that holds the program) of the new version: download it, check it,
unpack it, clear the quarantine mark on macOS, and copy the program to the same
folder as before. The copy replaces the old program. If you do not remember the
folder, `which email-mcp` prints the path of the program on macOS and Linux,
and `where email-mcp` prints it on Windows.

When the copy is done, go to [After the update](#after-the-update).

### Go

Run the same command as at the install. It downloads the code, builds the
program, and replaces the old program. `@latest` means the newest release. To
choose a version, write it instead, such as `@v0.6.2`:

```sh
go install github.com/EmadMokhtar/email-mcp-go/cmd/email-mcp@latest
```

A program that `go install` built prints `email-mcp dev` for `email-mcp
version`. To see its version, run this command on macOS and Linux. `-m` prints
the modules (the packages of Go code) that are built into the program:

```sh
go version -m "$(which email-mcp)"
```

The line that starts with `mod` holds the version, for example
`mod github.com/EmadMokhtar/email-mcp-go v0.6.2`. Then go to
[After the update](#after-the-update).

### From the source code

In the folder of the code, the first command brings the newest commits (saved
changes) of the branch `main` (the main line of the code). That code can hold
changes that are not in a release yet. The second command builds the program
with make (a tool that runs the build steps of a project):

```sh
git pull
make build        # writes bin/email-mcp
```

To build a release instead, run `git fetch --tags`, and then
`git checkout v0.6.2` (with the version that you want) before `make build`.
Copy `bin/email-mcp` to the same folder as before. Then go to
[After the update](#after-the-update).

### The container image

The container image is not for daily use on your own computer (see
[Containers](/manual/reference/containers/)). To test a new version, pull the
image with the tag (the name of one version of an image) of that version, and
use the new tag where you used the old one. The tag `latest` names the newest
version that is not a pre-release (a test version, such as `0.7.0-rc.1`). This
command downloads the image of one version:

```sh
docker pull ghcr.io/emadmokhtar/email-mcp:<version>
```

To check the new image, see [Verifying a
release](/manual/reference/verify-a-release/#the-container-image).

## After the update

1. Run `email-mcp version`. You should see the new version.
2. If your master key is a file, set the environment variable `EMAIL_MCP_KEK`
   (a named setting that a program reads from its surroundings) in this
   terminal first, with the same value as in the config of your AI app. Then
   run this command. It checks the install, and it opens the data folder, so it
   also runs the migrations of the new version:

   ```sh
   email-mcp doctor
   ```

   Each line should end with `ok`. If a line has `FAIL`, see
   [Troubleshooting](/manual/daily-use/troubleshooting/#start-with-doctor).
3. Start your AI apps again, and `serve` and `setup` if you use them. Each AI
   app now starts the new version.
4. If the release notes list a breaking change, do the steps that they name.

## Go back to an older version

After a newer version has opened the data folder, an older version refuses it.
Each command of the older version that opens the data folder then stops with
the error "this data was written by a newer email-mcp; install that version or
newer". The older version does not know the newer layout of the database, so
it changes nothing. To go
back, you need the copy of the data folder from
[Before you update](#before-you-update).

The copy does not hold what changed after you made it. For example, the
activity log of that time is missing, and so are the messages that wait in the
outbox (the queue of messages that wait to go out). So with the newer version,
first run `email-mcp outbox list`. If a message waits, let it go out, or cancel
it with `email-mcp outbox cancel <id>`. See [Activity and
undo](/manual/daily-use/activity-and-undo/).

1. Stop every program that uses the data folder, as in [Before you
   update](#before-you-update).
2. Install the older version. Use a release archive of that version, or
   `go install` with that version, such as `@v0.6.1`. Homebrew installs only
   the version that the tap names, which is the newest one. So remove the cask
   first with `brew uninstall --cask emadmokhtar/tap/email-mcp`, and install
   the older version from a release archive. The path of the program then
   changes, so also change it in the config of your AI app.
3. Put the copy back. On macOS, the first command moves the current data folder
   away to `email-mcp-newer` in your home folder, so you keep it. The second
   command copies the copy to the place of the data folder, and keeps the
   modes:

   ```sh
   mv "$HOME/Library/Application Support/email-mcp" "$HOME/email-mcp-newer"
   cp -Rp "$HOME/email-mcp-backup" "$HOME/Library/Application Support/email-mcp"
   ```

   On Linux, use `${XDG_DATA_HOME:-$HOME/.local/share}/email-mcp` as the path
   of the data folder. On Windows, rename the folder `email-mcp` in
   `%AppData%`, and copy your copy into `%AppData%` with the name `email-mcp`.
4. Run `email-mcp doctor`, with `EMAIL_MCP_KEK` set as in [After the
   update](#after-the-update). Each line should end with `ok`.

Without a copy, there are two ways. Install the newer version again, or start
with an empty data folder: move the data folder away, and connect your
accounts again (see [Connect your first
account](/manual/getting-started/connect-account/)). An empty data folder has
none of your accounts, settings or activity log.

[data]: /manual/reference/data-folder/
[key]: /manual/reference/master-key/
[install-brew]: /manual/getting-started/install/#homebrew-macos-and-linux
[install-archive]: /manual/getting-started/install/#a-release-archive
[stops]: /manual/daily-use/activity-and-undo/#when-email-mcp-stops
