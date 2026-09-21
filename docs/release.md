# Releases and support scope

This document records what the first release promises and what it does not. The
release workflow is `.github/workflows/release.yml`; a pushed `v*` tag builds and
publishes the assets consumed by `install.sh` and `brain-axi update`.

## Published path

`v0.1.0` is the first published release. The installer and release update path use
GitHub's `releases/latest/download` URLs, so they resolve to a published release
rather than a branch artifact. The workflow publishes these assets:

- `brain-axi_Linux_x86_64`
- `brain-axi_Linux_aarch64`
- `brain-axi_Darwin_x86_64`
- `brain-axi_Darwin_arm64`
- `checksums.txt`
- `version.txt`

`install.sh` derives the local asset name from `uname -s` and `uname -m`. The Go
release map and the workflow are covered by `TestReleaseAssetsMatchWorkflow`.
Release and update tests replace the download runner with a scripted local fake, so
normal tests never depend on GitHub or a network.

## Verification boundary

The installer and updater fetch the checksum manifest before the binary, require the
current platform to be listed, compare the SHA-256 digest, and run the staged binary
before replacing the installed one. These checks catch truncation, transfer errors,
and a mismatched asset. **checksums are integrity checks, not signatures**: they do
not authenticate the publisher or provide a cryptographic release signature.

The project does not add a signing key, key-distribution channel, or security promise
that it cannot operate. If signed release provenance becomes a requirement, it should
be designed as a separate release decision rather than treating `checksums.txt` as a
signature.

## Supported platforms

The published binary support is Linux amd64 and arm64, and macOS amd64 and arm64.
**Windows is not supported** by the release workflow or installer. The CLI uses Unix
commands for its explicit upgrade paths and has no Windows packaging promise. A
Windows port would need a separate platform decision covering installation, atomic
replacement, and delegated upgrades.

## Concurrency and lifecycle scope

Writes use a temporary file followed by an atomic rename. There is no cross-process file lock.
This is intentional: the product is a local Markdown vault with no
background daemon, and adding a lock would create coordination state that editors and
other tools would need to understand. Do not run concurrent writers against the same
record. If multi-process writes become a supported workflow, add and test a lock in
the vault boundary rather than a hidden home-directory lock.

GitHub release notes are generated from tags and pull requests; this repository does
not maintain a hand-edited `CHANGELOG.md`. That avoids a second release-history source
of truth. If generated changelog files become necessary, add them as a release
workflow output with an explicit format and ownership decision.
