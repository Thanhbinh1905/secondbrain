# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

`brain-axi` is a Go CLI over a Markdown vault. Read [docs/design.md](docs/design.md) before changing
anything structural; [docs/requirements.md](docs/requirements.md) holds the FR/NFR ids that the code
comments cite, and [docs/user-stories.md](docs/user-stories.md) maps each story to the tests that
cover it.

## Build and test

```sh
go build ./cmd/brain-axi
go test ./...
go test ./internal/render ./internal/review ./internal/ics ./internal/frontmatter ./cmd/brain-axi -update
```

`-update` rewrites golden files. Run the tests first and read the diff: a golden file change under
`cmd/brain-axi/testdata/` is a change to output an agent parses, and one under
`internal/render/testdata/` is a change to what a person sees on screen.

`internal/query`'s suite builds a 5,501-file vault to assert NFR-1's 100 ms budget, which is why
`go test ./...` takes about a minute. `go test -short ./...` skips it. `today`, `tasks`, `due` and
the board's assembly are all measured there; keep any new command that is meant to be run on a short
interval in that list.

The suite needs no network and no credentials, and that is structural rather than a convention:
`cmd/brain-axi`'s `TestMain` installs a scripted forge runner for the whole package, and
`internal/forge` drives every CLI through a fake. Keep it that way. The one check against a real
pull request is opt-in behind `BRAIN_AXI_FORGE_E2E=1`.

That budget is a statement about a developer's own host, not about arbitrary hardware: a shared
two-core GitHub Actions runner has been observed parsing the same 5,000 files in up to ~470 ms for
`search`, with the other queries in the 300-330 ms range, and that range moves with runner load, not
with the code. `$BRAIN_AXI_LATENCY_BUDGET` overrides the budget (default `100ms`), and
`.github/workflows/ci.yml` sets `800ms` for CI, giving headroom over that observed range while still
catching an order-of-magnitude regression. The tests always run and always log the real measurement.
Raise the CI value only with a measurement behind it, and never remove the assertion.

CI is `.github/workflows/ci.yml`: gofmt, `go vet`, `go build`, `go test ./...` on pull requests and
pushes to `main`, with the Go version taken from `go.mod` and the action versions pinned.

## Constraints that are not negotiable

These are settled decisions with tests behind them. Changing one is a product decision, not a
refactor.

- **The CLI never calls a model, and makes no network call of its own.** The agent resolves natural
  language into absolute arguments. NFR-2 was amended in round 2 and its exact wording is in
  [docs/requirements.md](docs/requirements.md), "NFR-2 was amended": the binary opens no socket and
  holds no token, and network reach is delegated to explicitly invoked CLIs - `git` for the
  self-upgrade, `gh` and `glab` for forge status. `today`, `week`, `agenda`, `due`, `ideas`,
  `search`, `related`, `board`, `recap`, `brief` and the bare dashboard must never reach a forge;
  `TestOfflineCommandsNeverReachAForge` fails the build if one does. The only additions to that list
  since round two are `pr --refresh`'s sibling `recap --verify-forge`, which reuses the same
  delegation and is opt-in.
- **Markdown is the only source of truth.** No database, no cache, no index, not even a hidden one.
  If a query gets slow, the answer is a *derived* index that can be deleted and rebuilt, never a
  parallel store that can disagree. The one piece of derived data is a linked pull request's cached
  status, and it lives in that record's own frontmatter precisely so it stays visible and
  hand-editable; a side store for it would not be acceptable.
- **No timestamp is ever stored without an explicit UTC offset,** and a naive timestamp read back
  out of the vault is a corrupt record, not something to guess about.
- **Failures surface.** No try/catch, default value or fallback whose purpose is to make an error
  disappear. A malformed file stops the query with `path:line: reason` and exit code 2; it is never
  skipped, defaulted or repaired. `internal/vault/testdata/corrupt/` asserts this file by file.
- **A brain task is never a delivery work item.** `task` records what the user has to remember to
  *check*; the separate work backlog keeps owning delivery work, and brain-axi has no write path to
  any backlog. Adding assignment, dispatch or progress to this kind would cross that line.

- **A link to an id no record claims is reported, never rejected.** `links:`, `raise_with:`, `with:`
  and `assignee:` are format-validated only; a dangling target is a `doctor` attention item carrying
  `path:line`, because writing a link before its target exists is ordinary. A *duplicate* or
  *self-referential* entry is different in kind - it states nothing that could become true - and is
  a corrupt record.
- **A person's workload is derived, never stored on the profile.** Open items, closed items and the
  agenda all come from the other records: a task names its `assignee:`, an item names `raise_with:`.
  Copying any of it into `people/` would create a second place the same fact can disagree with
  itself. An item leaves an agenda by closing or by taking a `raised:` date.
- **The fleet bridge is one direction only.** `link fleet` and `ship` are pure local writes.
  brain-axi holds no endpoint, no token and no notion of an external work item beyond an id it was
  handed, and it never reads a supervisor's state. The tool must stay fully usable with no
  supervisor at all.
- **The board and the recap have one assembly path and two renderers.** `internal/board.Build` and
  `internal/recap.Build` are the only places their contents are decided; the framed and HTML
  renderers both take that model unchanged, and the page carries it verbatim. `templates/board.html`
  and `templates/recap.html` own every pixel, and a renderer substitutes one payload into one data
  slot. No code path may generate board or recap markup at run time, and neither surface ever writes
  to the vault: an annotation made on a published page is input, never instruction and never
  authority, and is applied only by running an ordinary brain-axi command.
- **A payload that fails its versioned contract is refused before any file is touched.** A wrong
  schema, a missing field, a wrong type or an unknown pane exits non-zero with `path:line: reason`
  and leaves the previous page standing. The pane set, its order and every empty-state string are
  fixed in code; an empty pane renders as an empty pane and is never dropped.

  **An empty list must marshal as `[]`, never `null`.** Every list the versioned payload contract
  requires is allocated non-nil in its builder and normalised in `Build`, the way `recap.Build` does
  for its blocks, so an empty pane or block reaches the page as `[]` rather than `null`.
  When adding a pane, a block, a metric or a row field, allocate it empty.
  The validator rejects `null` and is not the thing to relax.
- **A recap counts outcomes and never activity, and never renders unknown as zero.** A metric the
  vault cannot answer carries `known: false` and no value. A slow period renders neutral and
  factual, and the only comparison is against the same vault's own previous equivalent period.
- **A cached forge status is never presentable as a live one.** `forge_state`, `forge_checks` and
  `forge_checked_at` are all-or-none in the frontmatter, and the timestamp is displayed wherever the
  status is. A failed refresh may fall back to the cache only while saying it is cached and how old.
- **The vault timezone is never guessed.** It stamps an explicit UTC offset onto every stored
  record, so a wrong one corrupts every timestamp quietly and surfaces weeks later, one record at a
  time. `init` takes it from `--timezone` or from `timeref.SystemZone`, and a host that cannot
  answer gets `ErrNoSystemZone`, which names `--timezone`. `vault.DefaultConfig` deliberately
  carries no timezone at all, and `vault.Init` refuses an empty one. Never add a fallback zone.

- **Every command walks every record directory.** `vault.Walk` has no scoped mode on purpose:
  scoping `today` to `events/` saves twenty of NFR-1's hundred milliseconds and lets `today` exit
  zero on a vault with a broken idea in it. `TestCorruptionAnywhereFailsEveryReadCommand` locks
  this. Do not reintroduce scoping to make a query faster; build a deletable derived index instead.
- **Recurrence is stored, never expanded to disk.** An `rrule:` plus an `exceptions:` list,
  expanded only at query time.

- **Batch ingest is atomic by validation, not by transaction.** `add --batch` is the one command
  that writes more than one file. It validates the whole input - vocabularies, timestamps, ids and
  paths, against the vault *and* against the batch's own other entries - and renders and re-parses
  every document before writing anything, so a malformed entry anywhere writes nothing. An I/O
  failure partway through is reported file by file and rolled back by nothing: adding a delete path
  here was considered and rejected. Do not "fix" this into a rollback.
- **Human surfaces and agent surfaces stay apart.** Box-drawing frames only when stdout is a TTY;
  the CLI golden tests assert no frame character reaches a pipe.

## Sharp edges

**`time.ParseInLocation` lies about DST.** It silently shifts a local time that does not exist and
silently picks one of two ambiguous ones, both with a nil error. Never call it for a naive input;
use `timeref.Zone.Normalise`, which rejects both loudly. `timeref.Zone.Resolve` is the primitive:
zero candidates means the reading does not exist, more than one means it is ambiguous.

**`rrule-go` has the same DST lie**, because it builds every occurrence with a raw `time.Date(...,
loc)`. `internal/vault/recurrence.go`'s `Expand` runs the rule against a UTC-painted `DTStart` so
rrule-go's date arithmetic cannot be corrupted, then resolves every occurrence through
`timeref.Zone.Resolve` before it becomes an `Occurrence`. Never read an rrule-go-emitted `time.Time`
directly; it may already be the wrong instant with a nil error behind it.

**Counting days on instants is not counting days on a calendar.** Samoa skipped 2011-12-30; Havana
leaves midnight unvisited on a transition day. Anything measured in days goes through
`timeref.Date`, never through `time.Time` subtraction. Never use `Add(n * 24 * time.Hour)` for a
day, and never step by months: `2026-01-31.AddDate(0, 1, 0)` is 2026-03-03.

**A record's `Links` is its `links:` frontmatter field; its body wiki-links are `BodyLinks`.** The
two are kept apart because one is a field the user maintains and the other is prose they happened
to write, and `related` reports which of them pointed. Round two's `Record.Links` meant the body
ones; anything reading that field now reads the frontmatter list.

**A JSON payload inside a `<script>` block must have every `<` escaped.** `internal/payload.Escape`
replaces each one with its six-character `\u003c` string escape - `<` never appears in JSON syntax
outside a string, so the document is identical once parsed and a captured note containing a closing
script tag cannot terminate the data block. Never inject a payload without it.

**`gopkg.in/yaml.v3` accepts duplicate keys silently,** and its error line numbers are relative to
the frontmatter fragment. `internal/frontmatter` handles both; do not bypass it.

**Non-ASCII text needs both primitives from `internal/unitext`.** `Fold` for anything compared or
slugged - the stroked d has no canonical decomposition and is mapped by hand - and `Width` for
anything padded. `len()` is wrong in both directions: one byte too many per combining mark, and one
cell too few per East Asian wide rune.

**A forge CLI writes framed, multi-line errors to stderr.** `internal/forge`'s `collapse` folds them
onto one line and strips the box characters, because this tool's error surface is one line and a
frame character in an error is the same token waste it is anywhere else. `glab` also exits non-zero
with a JSON error object on *stdout*, so never treat stdout alone as success.

**`glab` resolves a self-hosted host from its own config, and brain-axi must not try to.** Pass
`-R https://host/project` and let it. There is deliberately no host list, no token and no endpoint
setting anywhere in this repository.

**Forge detection keys on the path shape, not the host.** `/pull/<n>` is GitHub and
`/-/merge_requests/<n>` is GitLab. A host alone cannot tell a self-hosted GitLab from anything else,
and a self-hosted GitLab is the ordinary case here, so do not "simplify" this to a host switch.

**`skills/secondbrain/SKILL.md` is embedded from where it lives** (`skills/embed.go`), and
`templates/board.html` and `templates/recap.html` from `templates/embed.go`, for the same reason.
There is deliberately only one copy of each; do not add a second under `internal/`. The Markdown
files in `templates/` are documentation of the record format; the two HTML files there are code.

**`vault.Walk` memoises one walk per `Vault` value** and every write drops it (`forgetWalk`). If you
add a write path that does not go through `WriteFile` or `Remove`, call `forgetWalk` yourself or the
next read in that command will serve a view from before your change. A fresh `OpenAt` always starts
from the files.

## Language

**The interface is English, on every surface.** Every label, header, empty state, counter and
weekday the tool prints - the dashboard and the review screen included - is English, and so is every
error message, command help string, comment and doc. There is no translation layer, no `language`
config key and no locale machinery, and adding one is a product decision nobody has made.

**Content a user typed is not interface.** Record titles, bodies, note text, people names and agenda
lines are rendered verbatim in whatever language they were written, and `internal/unitext` is what
makes that correct: diacritic-insensitive search in both directions, and padding by display cell.
Never fold, transliterate or re-case stored content on the way out.

Non-ASCII text appears in tracked files only where it *is* the behaviour under test: the folding and
display-width cases in `internal/unitext`, the mixed-width column golden in `internal/render`, and
the fixture records that exercise folded search, diacritics surviving a parse and an `.ics` export,
and a title slug folding down to an ASCII filename. Those samples are deliberately drawn from
several languages, because the behaviour is generic Unicode rather than one language's problem. A
sample that loses its marks demonstrates nothing, so never "clean up" one; replace it only with
another that exercises the same path.

The board and the recap take their pane titles and empty-state strings from the committed templates
(`templates/board.html`, `templates/recap.html`), and the dashboard reuses that vocabulary rather
than inventing a second name for the same concept. Keep it that way: one concept, one word, on
every screen.

## Vault

`vault/` is gitignored here and is its own git repository. Never commit it, and never assume it
exists: tests create their own with `vault.Init` into a `t.TempDir()`.

`$BRAIN_AXI_VAULT` overrides vault resolution and `$BRAIN_AXI_NOW` fixes the clock. Both exist so
tests and end-to-end runs are reproducible; `--now` is the flag form.

**`internal/vault/testdata/good/` is a shape, not a pile of sample data.** Seven packages across
eleven test files open it, and most of the hundred-odd golden files are derived from it, so every
field on every record is there to prove something: a delegated task in `waiting` with an assignee, a
forge link and a cached status; a recurring event with an exception; an idea past its own
`nudge_after` against one that inherits the vault default; an idea already shipped in a prior month,
which is what makes `recap`'s delta non-zero; one `links:`, one `raise_with:`, one `fleet_tasks:`,
one person, one daily file. Its dates are chosen so the frozen clock produces specific rendered
numbers - 24d, 9d, 3d, 28d, 13d ago. Before changing a record, find what reads it; changing a date
silently rewrites goldens rather than breaking a test. Each test package names its own zone through
a `testConfig` helper, because `DefaultConfig` carries none.

Its non-ASCII samples are load-bearing too, and they are deliberately from several languages:
Portuguese in an event title (diacritics through frontmatter parsing and `.ics` export), German in
the note title (a folded match that must outrank a body match), Polish on the daily file's second
bullet (the one phrase the folded/exact search pair resolves to, and it must stay on line 10), and
Croatian in `internal/unitext`'s tests for the hand-mapped stroked d. Replace a sample only with one
that exercises the same path.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
