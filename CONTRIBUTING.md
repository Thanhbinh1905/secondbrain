# Contributing

Thanks for improving `brain-axi`.

## Before changing code

Read [AGENTS.md](AGENTS.md), [docs/design.md](docs/design.md), and the relevant
requirements and user-story entries. Markdown in `vault/` is the source of truth;
do not add a database, cache, index or hidden state. The CLI must remain deterministic,
offline by default, and free of model or network clients.

## Development

```sh
gofmt -w path/to/changed.go
go vet ./...
go build ./cmd/brain-axi
go test ./...
go test -race -short ./...
golangci-lint run --enable-only=staticcheck --tests=false ./...
```

The suite uses temporary vaults and scripted forge and download runners. It needs no
network or credentials. Run the relevant package tests first, then the full suite.
Golden files are output contracts: run tests before using `-update`, and inspect every
golden diff before committing it.

## Pull requests

Explain the user-facing behavior, the public seam tested, and any documentation or
format changes. Include the exact validation commands you ran. Keep changes focused,
keep failure messages actionable, and preserve the English interface.

Do not commit a vault, credentials, generated coverage output, or a manual changelog.
Releases are cut from `v*` tags by the workflow described in
[docs/release.md](docs/release.md).
