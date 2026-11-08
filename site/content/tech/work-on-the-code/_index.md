---
title: Work on the code
description: How to build, test and change email-mcp on your own computer, and how a release is made.
weight: 60
code_areas:
  - Makefile
  - .github/workflows/
  - internal/e2e/
  - internal/testutil/
  - test/interop/
  - .goreleaser.yaml
  - Dockerfile
---

These pages explain how to work on the code of email-mcp: how to build and
test it, which checks run on a pull request, and how a release is made and
published. Read them before your first change, and before you make a release.

- [Local development](/tech/work-on-the-code/local-development/): the make
  targets, the Go version, what CI (continuous integration: the checks that
  GitHub runs on each push) runs, the rules of the code, the steps to add a
  tool, a command, a migration or a package, and how to build the docs site and
  the container image.
- [Testing](/tech/work-on-the-code/testing/): the kinds of test, the test
  servers, the end-to-end tests, the build tags and the interop test.
- [Making a release](/tech/work-on-the-code/release-process/): the setup that
  is done once, the steps of each release, what to do when a release fails,
  and how the docs site is published.
