---
title: Install email-mcp
description: Put the email-mcp program on your computer and check that it runs.
weight: 10
code_areas:
  - cmd/email-mcp/main.go
  - internal/version/
  - .goreleaser.yaml
---

After this page, the `email-mcp` program is on your computer, and you have
checked that it runs. Choose one way to install it.

Use Homebrew on macOS and Linux. Use a release archive on Windows, or when you
want to check the signature yourself. A signature is a code that shows who made
a file, and that nobody changed it. Use Go or the source code if you are a
developer.

## Homebrew (macOS and Linux)

Homebrew (a package manager: a program that installs other programs) installs
a "cask" (a package of a ready-made program) from the tap (a repository of
Homebrew packages) `EmadMokhtar/homebrew-tap`. This command installs the
cask. The flag `--cask` says that the package is a ready-made program. The name
`emadmokhtar/tap/email-mcp` has the form `user/repo/cask`. Homebrew reads
`emadmokhtar/tap` as the tap `EmadMokhtar/homebrew-tap` on GitHub, and the
command also adds this tap, so you do not need to run `brew tap` first:

```sh
brew install --cask emadmokhtar/tap/email-mcp
```

When it ends without an error, go to [Check the install](#check-the-install).

The binaries (the program files) are not signed by Apple. So on macOS the cask
removes the "quarantine" mark (the flag that makes Gatekeeper, the macOS
program check, look at a downloaded program), and Gatekeeper does not check
`email-mcp`. Homebrew checks the SHA-256 (Secure Hash Algorithm with 256 bits:
a function that makes a short code from a file) of the archive that the cask
names, but you cannot check a cosign signature this way. Cosign is a tool that
signs files and checks the signatures. If you want to check the signature
yourself, use a release archive.

## A release archive

Download the archive (one file that holds the program) for your platform from
the [GitHub releases](https://github.com/EmadMokhtar/email-mcp-go/releases),
check it (see Verifying a release), unpack it, and put `email-mcp` in a folder
on your `PATH`. The `PATH` is the list of folders where the shell (the program
that reads your commands) looks for programs. There are archives for Linux
(amd64, arm64), macOS (Apple silicon, Intel) and Windows (amd64). The names of
the files use `linux`, `darwin` (macOS) and `windows`, and then `amd64` (Intel
and AMD processors) or `arm64` (Arm processors, such as Apple silicon).

### Unpack the archive and put the program on your PATH

On macOS and Linux, the archive is a `.tar.gz` file. This command unpacks it.
`-x` extracts the files, `-z` reads the gzip compression, and `-f` names the
file. Replace the name with the name of the file that you downloaded:

```sh
tar -xzf email-mcp_<version>_<os>_<arch>.tar.gz
```

The current folder now holds the program `email-mcp`. On Windows, the archive
is a `.zip` file: open it in the file explorer, and copy `email-mcp.exe` out of
it.

To see your `PATH`, run `echo $PATH` on macOS and Linux (a colon separates the
folders). In the Command Prompt on Windows, run `echo %PATH%` (a semicolon
separates the folders).

On macOS and Linux, put the program in `/usr/local/bin`. This folder belongs to
the administrator, so the command starts with `sudo` (it runs the command as the
administrator):

```sh
sudo cp email-mcp /usr/local/bin/
```

On Windows, no folder for your own programs is on the `PATH` yet. Make a
folder, such as `C:\Tools`, put `email-mcp.exe` in it, and add the folder to
the `PATH`: search for "environment variables" in the Windows settings.

### macOS: if macOS blocks the program

The binaries are not signed by Apple (see the note under Homebrew above). If
macOS blocks `email-mcp` when you run it, remove the quarantine mark, as the
cask does. Do this after you checked the archive. This command deletes the mark
`com.apple.quarantine` from the file. `-d` deletes one attribute, and `-r` also
goes into folders. Use the path where you put the program:

```sh
xattr -dr com.apple.quarantine /usr/local/bin/email-mcp
```

When the program is on your `PATH`, go to
[Check the install](#check-the-install).

## For developers

These two ways need Go 1.26 or newer (Go is a programming language). The second
way also needs git and make.

### Go

With Go 1.26 or newer, this command downloads the code, builds the program, and
installs it. `@latest` means the newest release:

```sh
go install github.com/EmadMokhtar/email-mcp-go/cmd/email-mcp@latest
```

When it ends without an error, go to [Check the install](#check-the-install).

### From the source code

These commands copy the code to your computer with git (the tool that copies a
code repository), move into the new folder, and build the program with make
(a tool that runs the build steps of a project):

```sh
git clone https://github.com/EmadMokhtar/email-mcp-go.git
cd email-mcp-go
make build        # writes bin/email-mcp
```

The last command writes the program to `bin/email-mcp`. Put that file in a
folder on your `PATH`, or run it with its full path.

## Docker

Docker is not for use on your own computer. In a container (a way to run a
program in its own closed space), email-mcp cannot reach your keychain (the
password store of your operating system), and you cannot reach its web pages
(the pages that email-mcp shows in your browser). The page Containers explains
why.

## Check the install

This command prints the name and the version of the program:

```sh
email-mcp version
```

You should see the word `email-mcp` and the version, for example
`email-mcp 0.6.0`. A program that you built from the source code, or with
`go install`, prints `email-mcp dev`.

If the shell says that it cannot find `email-mcp`, the folder that holds the
program is not on your `PATH`. Then see Troubleshooting.
