# secondbrain

`brain-axi` is a local, file-backed second brain for agents and people. Markdown in
`vault/` is the source of truth. The CLI parses, validates, queries and renders it; it
never calls a model and makes no network call of its own.

## Install

### Published release

The repository publishes release `v0.1.0` and the release workflow keeps the latest
release assets at the URLs used below:

```sh
curl -fsSL https://raw.githubusercontent.com/Thanhbinh1905/secondbrain/main/install.sh | sh
```

The installer downloads the newest published release, checks the asset against
`checksums.txt`, verifies that it runs, and then installs it under
`~/.brain-axi/bin`. It links the command into the first usable directory from
`~/.local/bin` and `/usr/local/bin`. Read [install.sh](install.sh) before piping it
into a shell.

Release binaries are published for Linux amd64 and arm64, and macOS amd64 and arm64.
Windows is not supported. The checksum manifest detects transfer or storage errors;
checksums are integrity checks, not signatures. See [docs/release.md](docs/release.md)
for release scope and verification details.

### Go toolchain

```sh
go install github.com/Thanhbinh1905/secondbrain/cmd/brain-axi@latest
```

This project supports Go 1.22 and newer. A `go install` binary reports the module
version and `brain-axi update` prints the same command to upgrade it.

### Checkout

```sh
git clone https://github.com/Thanhbinh1905/secondbrain.git
cd secondbrain
./install.sh --checkout
```

A checkout install records the source directory. `brain-axi update` fast-forwards
that checkout and rebuilds it. The script never commits your changes.

## Start a vault

Run this from the directory where you want `vault/` to live. Running it from `$HOME`
creates `~/vault`.

```sh
brain-axi init
brain-axi setup skill --claude
brain-axi doctor
```

Use an explicit timezone when needed:

```sh
brain-axi init --timezone Europe/Lisbon
```

Every command accepts `--vault <path>`, and `$BRAIN_AXI_VAULT` is the equivalent
environment override. `init` creates the vault's own Git repository but does not make
a commit. `doctor` reports an empty history and a missing remote until you decide how
to protect the notes.

## Capture and recall

The agent resolves natural language into absolute arguments. The CLI accepts those
structured arguments and echoes the result:

```sh
brain-axi add event "Platform team sync" \
  --when 2026-09-04T14:00 --duration 60m --with platform-team
brain-axi add idea "customer referral program"
brain-axi add task "review the service capacity report" --due 2026-09-05T17:00
brain-axi add note "ask the infrastructure team about CI capacity"
brain-axi add link "https://example.com/a-good-read" --title "a good read"

brain-axi today
brain-axi week
brain-axi ideas --status pending
brain-axi tasks
brain-axi search "zurich"
brain-axi due
```

A task is a commitment to remember to check. It is not a delivery work item, and
brain-axi never writes to a work backlog. A saved link is never fetched, so capture
works offline.

Every command also supports `--json` for agents. The full meeting-note flow is owned
by the agent: it proposes candidate records, waits for confirmation, then passes a
validated YAML batch to `brain-axi add --batch`. A malformed entry writes nothing.

## A quick visual check

The board is a terminal surface with a stable five-pane vocabulary. Piped output is
plain text, while a terminal can show the framed version:

```text
$ brain-axi board
TODAY
  09:00-09:30  daily sync                 recurring
  14:00-15:00  Platform team sync         next
THIS WEEK
  2026-09-04 14:00  Platform team sync
TASKS
  2026-09-05  review the service capacity report   open
IDEAS PENDING
  24d  customer referral program            stale-24d
WAITING ON OTHERS
  -  migrate the staging database            unchecked-28d
```

`brain-axi board --html path.html` writes a self-contained page from the same model.
`brain-axi ideas --html path.html` writes a filtered ideas review page. The CLI writes
files only; it does not start a server. A configured `--open` command is an explicit
handoff to an external viewer.

## Design boundaries

- Markdown is the only source of truth. There is no database, cache or hidden index.
- Writes use atomic replacement. There is no cross-process file lock, so coordinate
  concurrent writers when an editor or another process writes the same record.
- Timestamps are stored with an explicit UTC offset. Ambiguous or nonexistent local
  times are rejected rather than guessed.
- Read and capture commands stay offline. Explicit `pr --refresh`,
  `recap --verify-forge`, `doctor`, checkout updates and release updates delegate to
  the operator's `gh`, `glab`, `git`, `curl` or `wget` commands.
- Cached forge status is stored in the linked record with the time it was read and is
  never presented as live status.
- `checksums.txt` is not a signing system. The release process does not promise
  cryptographic publisher authentication.

Read [docs/design.md](docs/design.md) before changing structure. The requirements and
story-to-test map are [docs/requirements.md](docs/requirements.md) and
[docs/user-stories.md](docs/user-stories.md).

## Development

```sh
go build ./cmd/brain-axi
go test ./...
go test ./internal/render ./internal/review ./internal/ics ./internal/frontmatter ./cmd/brain-axi -update
```

Tests use temporary vaults and scripted forge and download runners. They need no
network or credentials. CI runs formatting, vet, staticcheck, race tests, the complete
suite and a 75% total coverage guard.

See [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md), and
[docs/release.md](docs/release.md) for project lifecycle and release guidance.

## License

MIT. See [LICENSE](LICENSE).
