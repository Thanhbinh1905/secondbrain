# Technical design

## The three layers

```
  ┌──────────────────────────────────────────────────────────────┐
  │  AGENT           runtime · coding assistant · automation   │
  │                  owns: natural language, date resolution,    │
  │                        capture proposals and confirmation,   │
  │                        composing the answer from brain +     │
  │                        backlog                               │
  └────────────────────────────┬─────────────────────────────────┘
                               │  structured arguments only
                               │  --when 2026-09-04T14:00+07:00
  ┌────────────────────────────▼─────────────────────────────────┐
  │  brain-axi       one static Go binary                        │
  │                  owns: parse · validate · query · render     │
  │                  never: a model call, a socket, hidden state │
  │                  delegates: git · curl/wget · gh/glab        │
  └────────────────────────────┬─────────────────────────────────┘
                               │  plain file I/O
  ┌────────────────────────────▼─────────────────────────────────┐
  │  VAULT           markdown + yaml frontmatter, git-versioned  │
  │                  the single source of truth                  │
  └──────────────────────────────────────────────────────────────┘
```

The arrows only point down.
The vault never knows about the CLI, and the CLI never knows about the agent.
Each layer is replaceable: a different agent, a different CLI, even a human with a text editor, and the layer below is unaffected.

## Repository layout

```
  secondbrain/
    cmd/brain-axi/          main, flag wiring, one file per command group
    internal/frontmatter/   parse and serialise, error positions preserved
    internal/timeref/       timezone normalisation, week boundaries, durations
    internal/unitext/       Unicode-aware folding, display width, slugs
    internal/vault/         open, resolve, atomic write, records, recurrence, batch ingest
    internal/forge/         pull-request URLs and status, through gh and glab
    internal/query/         agenda, ideas, tasks, search, links, people, due, brief
    internal/payload/       inject one validated JSON payload into a committed template
    internal/board/         the five-pane board: one model, two renderers
    internal/recap/         what a period produced: one model, two renderers
    internal/render/        axi text · json · dashboard frame
    internal/review/        the interactive triage screen
    internal/ics/           one-way RFC 5545 export
    internal/skill/         installs the tracked skill
    skills/secondbrain/     agent-facing skill, tracked and embedded
    templates/              the shape of each record type as documentation, plus
                            board.html and recap.html, which are code
    docs/
    install.sh
    vault/                  GITIGNORED - your brain, its own git repo
```

`internal/unitext` is not in the original module list.
It exists because three packages need the same two Unicode-aware primitives - diacritic folding for search and ids, display width for alignment - and a shared copy is better than three that can drift.

Tracked tooling and private data live in one repo, with the vault kept separate from the tool.
A checkout `brain-axi update` fast-forwards the tracked half; `vault/` is invisible to every upgrade path and can never be touched by one.

## Vault format

```
  vault/
    .brain/config.yml
    events/2026-09-04-platform-team-sync.md
    ideas/customer-referral.md
    tasks/migrate-staging-db.md
    notes/ci-capacity.md
    people/platform-team.md
    daily/2026-09-01.md
```

An event:

```yaml
---
type: event
id: platform-team-sync-20260904
title: Platform team sync
when: 2026-09-04T14:00:00+07:00
duration: 60m
status: scheduled
created: 2026-09-01
with: [platform-team]
---

Decide how to handle expired schedules.
```

An idea:

```yaml
---
type: idea
id: customer-referral
title: customer referral program
status: pending
created: 2026-08-09
touched: 2026-08-09
nudge_after: 14d
---

Customer referral program - define eligibility and incentive criteria before launch.
```

A task:

```yaml
---
type: task
id: migrate-staging-db
title: migrate the staging database to the new cluster
status: waiting
assignee: platform-team
created: 2026-08-01
touched: 2026-08-05
follow_up_after: 14d
---

Assigned to [[platform-team]]. Awaiting confirmation.
```

A person, and something waiting to be raised with them:

```yaml
---
type: person
id: platform-team
title: Platform team
created: 2026-08-01
---

The platform team.
```

```yaml
---
type: task
id: review-ci-capacity
title: review CI capacity with the platform team
status: open
created: 2026-09-02
touched: 2026-09-02
raise_with: [platform-team]
---
```

Any record may also carry the linking layer and the fleet bridge:

```yaml
links: [platform-team-sync-20260904]     # ids of other records
raise_with: [platform-team]              # people this is waiting to be raised with
raised: 2026-09-04                       # when it was, which takes it off their agenda
fleet_tasks: [PROJ-42]                   # external work items, written and never read back
shipped_at: 2026-09-04T15:40:00+07:00    # when the work landed; always carries an offset
shipped_pr: https://github.com/owner/repo/pull/14
```

Every one of them is validated on load. `links:` and `raise_with:` are format-validated only: an id
no record claims is reported by `doctor` with `path:line`, never rejected, which is the precedent
`with:` and `assignee:` already set. A duplicate or self-referential entry is a corrupt record,
because it states nothing that could ever become true. `shipped_pr` without `shipped_at` is refused
for the same reason a cached forge state without its timestamp is: a merge reference with no merge
time cannot be placed in any period. `raise_with` is refused on a person (a profile is who you raise
things with) and on a daily file (a whole day is not one item).

**Filename is human-facing; `id` is machine-facing.**
You can rename a file in Obsidian without breaking a single reference, because references resolve through `id`.
Ids are stable and never reused: a capture that would collide takes the nearest free numeric suffix rather than the existing id.

`title:` is stored in frontmatter.
It is not in the specification's illustrative examples, but the `ideas[n]{id,title,...}` output contract and the dashboard both need it, and deriving it from the filename would lose diacritics - the filename slug of *Platform team sync* is `platform-team-sync`.

### Status vocabularies

- Event: `scheduled` · `done` · `cancelled`
- Idea: `pending` · `building` · `shipped` · `dropped`
- Task: `open` · `waiting` · `done` · `dropped`

Closed and fixed.
An open vocabulary means the query layer can never make a confident statement about what is outstanding.
A note, a person and a daily file carry no status at all, and a status on one of them is a parse error.

### `add note` and the daily file

US-3 requires a quick note to land in today's daily file so it is never orphaned.
It therefore appends a timestamped bullet to `daily/<today>.md`, creating that file on the day's first note, and returns the daily file's id.
That keeps the capture to one file write, which is what every single-record operation in this tool is (see "Atomicity and multi-file batches" for the one command that writes many).

`notes/` holds standalone notes.
Nothing in the CLI writes there, because the CLI reference has no command that would; the directory exists for notes written by hand in Obsidian, and those are parsed, searched and shown like any other record.

### `add person`

The CLI reference lists `add event|idea|note`, and the scope table lists people profiles under the knowledge graph.
Without a way to create one, that feature would be reachable only by hand-editing, so `add person` exists.
It writes one file into `people/` and is otherwise an ordinary record.

### `task`, and the boundary it does not cross

A task is **a commitment the user has to remember to check.** It is not a delivery work item, it
never becomes one, and brain-axi still has no write path to any backlog. The separate work backlog
keeps owning delivery work under way; this kind owns the user's own memory of what they are waiting
on.

That boundary is what shapes the vocabulary. `waiting` is a first-class status rather than a flavour
of `open`, because something handed to somebody else is a different thing from something still on
their own desk. There is no assignment, no dispatch, and no notion of who is *doing* the work -
`assignee:` records who has it so the user knows who to ask, and it resolves through the same id rules
every other reference uses, so a delegated task is a node in the link graph.

`follow_up_after:` is the whole point of the feature and it is the same decay mechanism an idea's
`nudge_after:` is. They resolve through one function, `vault.Horizon`, rather than two that can
drift; they keep different spellings because they mean different things to the user - one is
"poke me about this thought", the other is "remember to check whether this actually happened". A
task that is `done` or `dropped` stops decaying, because nagging about something already finished is
exactly the noise that makes a user stop reading what comes back.

Tasks are surfaced everywhere ideas are, in their own block. `today`, `week` and `agenda` print a
`tasks[n]{due,id,title,assignee,status,flag}` block beside `events[n]`, never folded into it: an
event is an occurrence with a start, an end and a recurrence model behind it, and putting a due date
in that column would make the `events` contract mean two different things. Overdue and
past-horizon tasks are not clipped to the query's window - a deadline does not stop mattering
because the week moved on, and a delegated thing nobody has checked in three weeks has to be
impossible to miss rather than visible only on the day it was due.

## Batch ingest and the confirmation gate

The user may hand over a `.txt` file, a `.md` file, or an unstructured block of meeting notes.
The **agent** reads the complete input and scopes candidate events, ideas, tasks, delegated follow-ups
and notes.
It preserves the original wording and detail of included content in `body` or `text`, and identifies
context or noise that will be omitted.

Before persistence, the agent presents a concise proposal with the candidate records, resolved dates,
assignees and follow-up horizons, plus the context or noise to omit.
It asks only the clarification questions needed to resolve ambiguity and waits for the user's
confirmation.
The agent revises the proposal and asks again if the user changes its scope.
Only after confirmation does it generate the structured batch file with absolute timezone-qualified
dates and invoke `brain-axi add --batch <file>`.

The CLI still never calls a model or parses free-form meeting notes.
The YAML file is an internal intermediate payload generated by the agent after confirmation, not a
user-facing input requirement.

The format is one YAML document whose top-level keys are the sections the user named:

```yaml
# Agent-generated intermediate payload after the user's capture proposal was confirmed.
ideas:
  - title: review the cache expiration policy
    body: |
      The current approach may be worth revisiting when the table grows.
    nudge_after: 30d
tasks:
  - title: review the service capacity report
    body: |
      Confirm whether the report covers all production regions.
    due: 2026-09-05T17:00+07:00
    follow_up_after: 3d
delegated:
  - title: update the staging runbook
    body: |
      The operations team will update the runbook before the next release.
    assignee: operations-team
    follow_up_after: 14d
notes:
  - text: ask whether capacity is measured per project or organization
events:
  - title: Service planning follow-up
    when: 2026-09-09T14:00+07:00
    duration: 60m
    with: [operations-team]
```

| Section | Becomes | Required | Also takes |
| --- | --- | --- | --- |
| `ideas` | an `idea` | `title` | `body`, `id`, `status`, `nudge_after`, `links`, `raise_with` |
| `tasks` | a `task` | `title` | `body`, `id`, `status`, `assignee`, `due`, `follow_up_after`, `links`, `raise_with` |
| `delegated` | a `task` | `title`, `assignee` | the same as `tasks` |
| `notes` | bullets in today's daily file | `text` | - |
| `events` | an `event` | `title`, `when` | `body`, `id`, `status`, `duration`, `with`, `rrule`, `exceptions`, `links`, `raise_with` |

`links:` and `raise_with:` are behavior-affecting fields of the payload the user confirms.
The proposal shows them like every other persisted field, and an explicit event `id:` lets records produced by that meeting link back to it.

`delegated` is a `task` with an `assignee`, not a fifth record kind: what is being tracked is still
the user's own commitment to follow up. An entry there without an `assignee` is refused by name
rather than quietly stored as a plain task. A field a section does not have is refused too, so a
misremembered key is reported instead of ignored.

The proposal and confirmation gate belongs to the agent, while parsing and atomic validation belong to
the CLI.
Once confirmation has been received, the CLI sees only the structured YAML contract described here.
The gate does not alter single-item captures or the batch parser's validation rules.

YAML rather than a bespoke Markdown dialect, because the vault already speaks it. `gopkg.in/yaml.v3`
is already a dependency, it carries a line number on every node, and `internal/frontmatter` already
turns that into `path:line: reason` - so a hand-written batch gets the same positional error the
agent's would, for almost no new code. Duplicate section keys are rejected here for the same reason
duplicate frontmatter keys are: yaml.v3 accepts them silently, and a second `tasks:` block would
drop the first one's entries without a word. One caveat is honest to state: for an indentation error
yaml.v3 attributes the position to the line it gave up on rather than the line that was mistyped.
Its own position is still better than an invented one, and the message names the problem.

### Atomicity and multi-file batches

Batch ingest is the one command that writes multiple files, so the invariant is this:

- **Every single-record operation touches exactly one file and is atomic per file**, by NFR-3's
  write-then-rename. That is unchanged.
- **Batch ingest validates the entire input before writing anything.** Every entry is parsed, every
  vocabulary checked, every id and path resolved against the vault *and against the other entries in
  the same batch*, and every resulting document rendered and re-parsed as a record. A malformed entry
  anywhere means zero files are written, reported as `path:line: reason` with exit code 2. This is
  the guarantee, and it is complete for every failure the format can express.
- **An I/O failure partway through the write phase is reported file by file.** The tool names every
  file that landed and every file that did not, exits non-zero, and deletes nothing. `vault/`'s git
  history is the recovery path.

A filesystem offers no transaction across several files, so the third bullet is the honest limit of
the second. Rolling back by deleting the files already written was considered and rejected: it would
give this tool its first automatic delete, against the posture that made `rm` refuse without
`--yes`, and if the rollback delete failed on the same failing disk the resulting state would be
*less* well described than a plain report. Claiming an atomicity the code does not provide would be
worse than either.

Notes are the one section that could have made this worse. `AppendNote` does a read-modify-write per
note, so five notes would have been five writes of one file and five chances to fail halfway. A
batch accumulates every bullet into one document during validation instead, keeping the write phase
to one write per output file.

## Forge status

A user wants to mark a pull request against a record and have its status checked later,
across GitHub, GitLab.com and a self-hosted GitLab. NFR-2 is amended for exactly this and no more:
**brain-axi makes no network call of its own, and network reach is delegated to explicitly invoked
forge CLIs.**

The binary opens no socket and holds no token. `internal/forge` runs `gh` for GitHub and `glab` for
GitLab, both of which already hold their own hosts and credentials - which is the reason a
self-hosted host needs no configuration here at all. `glab mr view <n> -R https://host/project`
resolves the host through glab's own config, so brain-axi never learns about the self-hosted host.

**Detection keys on the path shape, not on the host alone.** A host is enough to recognise
`github.com`, but it can never tell a self-hosted GitLab apart from any other machine, and the
self-hosted work forge is exactly that case. `/pull/<n>` is unmistakably GitHub and
`/-/merge_requests/<n>` unmistakably GitLab, so hosted and self-hosted URLs resolve by one rule. A
host and a shape that disagree is a typo and is refused, because guessing there points a refresh at
the wrong forge entirely.

Each forge's own spelling is mapped onto one closed vocabulary, so a query never has to know which
forge a record points at: state is `open` · `draft` · `merged` · `closed`, and the check rollup is
`passing` · `failing` · `pending` · `none`. A state that does not map is a loud failure, never a
value smuggled into the vault.

### Where the cache lives, and why the timestamp is not optional

The last known status is cached **in the linked record's own frontmatter**:

```yaml
forge_url: https://git.example.com/platform/service/-/merge_requests/42
forge_state: open
forge_checks: pending
forge_checked_at: 2026-08-20T10:15:00+07:00
```

This is derived data living in the source of truth, which the no-parallel-store rule would normally
forbid. It is acceptable here precisely because it is visible and hand-editable: you can see
it, can see how old it is, and can delete the three status keys with a text editor at the cost of
nothing but the next refresh. A side store would have none of those properties.

It is validated as strictly as anything the user typed, which is what keeps it from being a
second source of truth that can quietly disagree. `forge_url` must resolve; the state and the checks
must be in their vocabularies; `forge_checked_at` goes through `timeref.ParseStored` like every other
stored timestamp, so a naive one is a corrupt record. The three status keys are all-or-none, because
**a state without the time it was read cannot be told apart from a live one** - that is the single
way this feature could mislead, so the format makes it unrepresentable.
`internal/vault/testdata/corrupt/` asserts each of these file by file.

The timestamp is displayed everywhere the status is: in `pr`, in `show`, and in `--json` as both
`checked_at` and a `cached` flag. A refresh that fails falls back to the cache only out loud, naming
the failure and the age of what it is showing instead. A link that has never been checked reports
that, never a blank that reads as fine. Flat scalar keys rather than a list of mappings, because
`internal/frontmatter` deliberately handles scalars and string lists only, and one link per record is
what was asked for; a second pull request is a second record.

### Which commands may reach a forge

Only `pr --refresh`, `link --refresh` and `doctor`. `today`, `week`, `agenda`, `ideas`, `search`,
`brief` and the bare dashboard never do, and `TestOfflineCommandsNeverReachAForge` fails the build if
one of them ever runs a forge CLI - with a linked record present whose host is unreachable, which is
exactly the case that would hang. A second brain that cannot answer a question about today's schedule on a plane is
broken.

`doctor` is the exception among diagnostics because the question it answers - can this machine read
a linked forge - cannot be answered without asking. It reports which CLIs are installed,
probes each distinct linked host once with a short timeout, and keeps a missing CLI, an
unauthenticated host and an unreachable one as three different answers rather than one shrug.

## The linking layer, and people as records

A record's `links:` is a flat list of other record ids. There is no graph store: resolution walks
the vault like every other query, and the field is plain text a human can hand-edit and correct.

It is deliberately separate from the body's `[[wiki-links]]`, which round two already read.
`Record.Links` is the frontmatter field and `Record.BodyLinks` is the prose, because "this meeting
produced that idea" and "that idea mentions this meeting in passing" are different claims, and
`related` reports which of them pointed:

```
pointed_to_by[1]{id,type,via,resolved,title}:
  cache-schedule-expiry, idea, links, yes, cache the schedule expiry lookup
```

`query.PointsAt` is the single place that answers "does this record name that id", over all five
fields (`links`, `body`, `with`, `assignee`, `raise_with`), so backlinks, `doctor`'s dangling
report and the recap's meeting/idea attribution cannot drift apart.

### A person's workload is derived

`people/` was a name that a link resolved to. It is now a record kind that answers three questions:
what is assigned to them, what has closed, and what is waiting to be raised with them.

All three are derived from the other records. A task names its `assignee:`; an item names
`raise_with:`. Nothing about a person's workload is stored in the profile, for the same reason there
is no index: a copy of a fact in a second place is a copy that can disagree with the first. It also
mirrors the decision round two already made when it put `assignee` on the task rather than a list of
tasks on the person.

An item is on somebody's agenda while it names them in `raise_with:`, has no `raised:` date, and has
not closed. Two clearings, both ordinary edits, and both visible in the file. Because an agenda item
is only useful in the minutes before walking into a room with the person it is for, `today`, `week`
and the board carry it beside the event whenever that person is in the event's `with:` list;
`query.PersonAgendas` builds every person's agenda in one pass so a day with three meetings still
walks the vault once.

## Due, and why it is not brief

`brief` is a morning read: it names everything outstanding whether or not anything changed. `due`
answers one question - what needs attention right now - and prints nothing at all when nothing does,
because a command run every few minutes that always says something is a command whose output stops
being read.

All three of its categories are "this crossed a line", never "this exists":

| category | what it catches | window |
| --- | --- | --- |
| `delegated` | an open task with an assignee, past its follow-up horizon, untouched since | the record's `follow_up_after:`, else the vault's |
| `event` | a scheduled occurrence starting inside the window | `due_within:` |
| `dormant_idea` | an unclosed idea untouched past the dormancy window | `dormant_after:` |

Each is togglable on its own, and naming none reports all three. `dormant_after` is a separate knob
from `nudge_after` on purpose: the nudge horizon means "poke me at triage" and dormancy means "this
has stopped". Collapsing them would make `due` a second copy of `brief`.

The event category expands only the configured window rather than the whole day, so running this on
a short interval costs a walk and almost no recurrence work. It is measured on the same 5,501-file
fixture as `today`, against the same NFR-1 budget.

## The fleet bridge

Two narrow write commands let an external supervisor leave a mark on a record, in one direction
only.

`link fleet <record> --task <id>` appends to `fleet_tasks:`; `ship <record> --pr <url> --merged-at
<timestamp>` writes `shipped_at:` and `shipped_pr:` and moves the status to `shipped` for an idea or
`done` for a task. Both validate strictly and change nothing when they refuse.

brain-axi never reads a supervisor's state. It holds no endpoint, no token and no notion of a work
item beyond an id it was handed, which is what keeps the tool exactly as usable on a machine with no
supervisor at all. `fleet_tasks` is therefore written and never read back by any query; it exists so
a human opening the record can see what it belongs to.

`--merged-at` must carry an explicit UTC offset so a naive value is refused rather than guessed at.
The same instant is then stored in the vault zone with its offset explicit, like every other vault timestamp.
It is the one date in the vault that says when work actually landed, and a naive value there would
silently place a merge in the wrong period.

## The board and the recap

Both surfaces are built the same way, and the shape is the point: it is what stops an agent
re-authoring the UI on every run.

**One assembly path.** `board.Build` and `recap.Build` are the only places their contents are
decided. Both renderers take the result unchanged, and the HTML page carries the model verbatim, so
the framed and the published views cannot disagree.

**A committed template owns the markup.** `templates/board.html` and `templates/recap.html` own
layout, styling, pane order and every empty-state string, and are embedded from where they live
(`templates/embed.go`) so there is one tracked copy. A renderer substitutes one JSON payload for the
template's single `__BRAIN_AXI_DATA__` slot and generates nothing else; a test asserts the built
page is byte-for-byte the template with only that line replaced.

**A versioned data contract.** `brain-board.v1` fixes five panes in one order - Today, This week,
Tasks, Ideas pending, Waiting on others - with fixed field names and types and its own empty-state
string per pane, so an empty week renders as an empty week rather than as a missing pane.
`brain-recap.v1` does the same for six blocks.

**Validation is fail-closed and happens first.** `Validate` refuses a wrong schema, a missing field,
a wrong type, an unknown pane or a pane with no empty-state string, with `path:line: reason` and
exit code 2. It runs on the generated payload before the page is built and again on the payload read
back out of the built page, and only then is the previous file replaced - by an atomic rename, so
there is no partial render. `doctor` runs the same validation against the file at `board_html:`, so
a page a person hand-edited or an older binary wrote is named with the line that is wrong.

**The payload cannot terminate its own script block.** `payload.Escape` replaces every `<` with its
`\u003c` string escape. `<` never appears in JSON syntax outside a string, so the document is
identical once parsed and a captured note containing `</script>` is inert.

**Writing a file is the entire integration seam.** brain-axi opens no socket and serves nothing. The
output path is caller-supplied (`--html`, or `board_html:` once) and rewritten in place, so an
external viewer's URL stays stable. `--open` hands that file to the configured `board_open_cmd`,
run as a command with the path appended rather than through a shell; a missing or failing viewer is
reported as itself, exits non-zero, and **keeps** the file that was already written.

### What a recap counts, and what it refuses to

Four rules, each with a test behind it.

1. **Outcomes, never activity.** There is no metric for a commit, a line or an hour, and the whole
   metric set is asserted so one cannot be added quietly. Ideas that shipped are listed by name.
2. **Unknown is never zero.** A `Metric` carries `Value *int` and `Known bool` separately. The vault
   records no date a pull request was opened, so that metric is unknown and says why; the contract
   refuses a metric marked known with no value, and one marked unknown that carries one.
3. **A slow period renders neutrally.** Nothing in the package evaluates. A test over an empty
   period asserts the output carries no evaluative or punitive wording.
4. **Comparison is against this vault's own previous equivalent period, and nothing else.** The
   payload names that span so a reader can check it.

Timestamp-backed outcomes remain reconstructable for any period: shipped ideas, merged pull
requests, commitments made, meetings held, and ideas linked to those meetings.
Commitments kept, delegated items done or still unchecked, and dormant ideas depend on mutable
status or touched dates.
For a closed past period those metrics are unknown, because storing lifecycle history would add a
new persistent subsystem and deriving them from today's state would rewrite history.
A current period reports every metric from the state that is true now.

Period boundaries use the vault's timezone and its configured week start. Month and quarter
arithmetic runs on the `(year, month)` pair through `timeref.Date.MonthStartAfter`, never through
`AddDate`, so the 31st of a month resolves to the span a calendar shows.

`--verify-forge` is the only part of either surface that reaches anything. It re-reads every linked
record through the same `gh`/`glab` delegation and reports where the record and the forge disagree;
without it the command makes no network call, which a test asserts.

### An annotation is input, never instruction

A review surface may let people annotate the published page. Those annotations are applied only by
running ordinary brain-axi commands: the board never writes to the vault, and building one leaves
every record byte-for-byte as it was.

An annotation confers no authority. It is not executed and it grants no permission - it is a
message from a person to whoever reads the page, and acting on it is an ordinary decision made with
the ordinary commands.

## Time handling

This is where calendar tools die, so it is settled before the first line of code.

- `.brain/config.yml` declares the vault timezone and the first day of the week.
  `init` takes that timezone from the host, through `timeref.SystemZone`, and refuses with an actionable message when the host cannot say rather than substituting one.
  It never reads `time.Local`, which is `"Local"` with no `$TZ` and silently `"UTC"` when `$TZ` names a zone that does not exist - and a silently wrong offset is written into every record the vault ever stores.
- A naive input time is normalised to that zone on write and stored with an explicit offset.
  A time is **never** stored without an offset.
- Comparisons happen on absolute instants; display happens in the vault zone.
- "This week" resolves against the configured first day, not against whatever the platform thinks a week is.
- Relative-phrase resolution (*"next Thursday at 2pm"*) belongs entirely to the agent.
  The CLI accepts only absolute times, so it has no relative-date bugs to have.

### What the standard library does, and what had to be built on top

These were measured against real zone data, not remembered.

`time.ParseInLocation` **does not report a local time that does not exist.**
`2026-03-08T02:30` in `America/New_York` comes back as `2026-03-08T01:30:00-05:00` with a nil error - the clock jumps over 02:30 that morning, and Go silently answers with a different instant.

It also **does not report an ambiguous local time.**
`2026-11-01T01:30` in the same zone comes back as `-04:00`, silently choosing the first of the two instants with that wall clock.

Both are unacceptable under FR-3 and NFR-4, so `timeref.Zone.Resolve` answers the underlying question directly: given a calendar-and-clock reading, which instants in this zone show it?
An empty answer means the reading does not exist and is rejected.
An answer longer than one means it is ambiguous, is rejected, and the error names both instants so the intended one can be passed explicitly.
A fast path handles the overwhelmingly common case in two probes: no UTC offset exceeds fourteen hours, so when the zone holds one offset across a forty-hour window there is at most one reading.

Two more measured behaviours shape the rest of the package:

`AddDate(0, 0, 1)` preserves the wall clock across a DST boundary and `Add(24 * time.Hour)` does not, so all day arithmetic is calendar arithmetic.
And `2026-01-31.AddDate(0, 1, 0)` is `2026-03-03`, not the end of February - month overflow normalises forward - so nothing here steps by months.

Counting days on instants is not the same as counting days on a calendar.
Samoa skipped 2011-12-30 entirely; Havana leaves midnight unvisited on a transition day.
`timeref.Date` is therefore a calendar page - a year, month and day with no zone - and everything measured in days is measured on it, through a location-free UTC anchor.
`Zone.StartOf(Date)` is the first instant that date actually has, which on a midnight-transition day is not 00:00.

`internal/timeref` carries property tests across eleven zones chosen for the shapes that break calendar tools: no DST, whole-hour DST, Lord Howe's thirty-minute shift, Kathmandu's `+05:45`, Chatham's `+12:45`, Havana's midnight transition, Dublin's negative DST, St Johns' `-03:30` with DST, and Samoa's skipped day.
They are the highest-value tests in the project and they found three real bugs in the first draft of this package.

### Durations

`timeref.Span` keeps calendar days apart from clock time, because adding fourteen days to a date must move the wall clock fourteen days, which is not always 336 hours.
It also keeps the spelling it was parsed from: somebody wrote `60m` and the format says `60m`, so a rewrite of the same file must not quietly turn it into `1h`.

## Recurrence

Stored as an RFC 5545 `rrule:` string plus an `exceptions:` list of dates, expanded only at query time.
Nothing is ever written to disk per occurrence, which keeps a series editable by hand in one file and keeps the corruption surface the size of one file rather than the size of a calendar.

Expansion uses `github.com/teambition/rrule-go` v1.8.2 rather than hand-written recurrence.
The decision was measured, not assumed:

- `FREQ=MONTHLY;BYDAY=-1FR` gives 2026-09-25, 2026-10-30, 2026-11-27 - the last Friday of each month, which is the phrase class the PRD names as hard.
- `Between(from, to, true)` returns only the occurrences inside a window, which is exactly query-time expansion.
- Invalid input fails loudly: `FREQ=BOGUS` gives "undefined frequency: BOGUS", an empty rule gives "wrong format", and a rule with no `FREQ` says so.

The stored string is also the string an `.ics` file carries, so export needs no second serialiser.
Hand-writing `BYDAY`, `BYSETPOS`, `BYMONTHDAY`, `UNTIL` and `COUNT` with correct DST behaviour is a multi-week subproblem with no upside here.

rrule-go itself holds no DST behaviour at all: every occurrence it builds goes through a raw `time.Date(year, month, day, hour, min, sec, 0, loc)`, and Go's own `time.Date` silently normalises a wall clock a DST gap skips and silently picks one of the two candidates a fall-back repeat has, both with a nil error. A weekly 02:30 series recurring onto a spring-forward date in `America/New_York` silently comes back as `01:30 EST` if nothing checks it, and a series landing on a fall-back hour silently gets whichever of the two offsets `time.Date` happens to resolve. `brain-axi` never lets that reach a caller: `vault.Expand` runs the rule against a UTC-painted copy of `when:` so rrule-go's date arithmetic cannot be corrupted by a real zone's transitions, then resolves every occurrence's year/month/day/hour/min/sec through `timeref.Zone.Resolve` before it becomes an `Occurrence`. A reading with zero candidates (a gap) or more than one (an ambiguity) is a loud query-time error naming the file, the date, the local time, and the remedy - change the series time, or except that date - never a guess.

`DTSTART` inside a rule is rejected: the series starts at `when:`, and two sources of truth for a series start is exactly the disagreement this format avoids.
An unbounded rule cannot spin a query: the window is always bounded and a single series is capped at 10,000 occurrences inside one window.

## Query strategy

No index and no database.
Every command that reads the vault does a full walk-and-parse of every record directory.

That is a deliberate choice against an optimization the specification itself sanctions.
Scoping a date-bounded query to `events/` is easy, and it was built first: only `events/` can hold an event, so `today`, `week` and `agenda` need nothing else.
It was then removed, because it made `brain-axi today` exit zero on a vault with a broken idea file in it.
NFR-4 says no file is ever skipped, and a command that quietly holds a partial view of the vault is exactly the thing a user would come to trust wrongly.
The scoping bought about twenty milliseconds of the hundred NFR-1 allows.
That is not a trade worth making, so corruption anywhere is now visible from everywhere.

Two things do the work instead.

A **parallel walk**: parsing runs across all cores, with results and errors both collected by path index, so a parallel walk answers exactly what a serial one would - the same records in the same order, and the same first error.

And a **per-invocation memo**: one command often asks the same question of the vault more than once, because a capture needs a free id, a free path and an overlap check.
The first walk of a `Vault` value is remembered and any write drops it.
That is not a cache in NFR-6's sense: it holds nothing on disk, cannot outlive the process that made it, and cannot serve a view from before the command's own change.
It took a capture on a 5,000-file vault from 103 ms to 48 ms.

Measured on a 5,501-file vault, each from a freshly opened vault because that is what a real invocation is: today 46 ms, week 51 ms, ideas 47 ms, tasks 47 ms, search 70 ms, brief 48 ms, `today` with its task block 51 ms, and a capture's three questions 44 ms.
The budget is asserted in `TestQueryLatencyOnFiveThousandFiles` and `TestCaptureLatencyOnFiveThousandFiles`, which fail the build if any of them passes the budget - 100 ms by default, overridable with `$BRAIN_AXI_LATENCY_BUDGET` (see AGENTS.md).
Both reopen the vault per measurement: measuring a second query against the same `Vault` value would measure the memo, which no CLI run ever benefits from.
`TestCorruptionAnywhereFailsEveryReadCommand` asserts the other half: a broken file in any of the five record directories stops every read command, with exit code 2 and no partial answer.

If that ever stops being fast enough, the answer is a **derived** index, rebuildable from scratch at any moment and delete-safe.
Never a parallel store that can disagree with the Markdown.
The moment two things can disagree about what was written, neither can be trusted.

## Write strategy

Temporary file in the destination directory, `fsync`, `rename`, then `fsync` of the directory so the rename itself survives a power loss.
Atomic per file.
An interrupted write leaves the previous file standing and is never observed as a partial file.
Any failure before the rename removes the temporary file and never touches the file being replaced.

Every single-record operation touches exactly one file, so there is no cross-file transaction to get wrong.
Batch ingest is the one command that writes many, and it buys its atomicity with CLI validation before
the write phase rather than a filesystem transaction.
The "Atomicity and multi-file batches" section states exactly what it does and does not guarantee.

### What `touched:` means, and FR-7

FR-7 says `done` and `update` mutate only the frontmatter keys named on the command line.
`done <id>` names no keys and must still set `status`, so the requirement cannot be read as literally as that: it means do not rewrite a key you were not asked about.

`touched:` is the one key the tool sets without being asked, and only when the record actually moves - a status change through `done` or `update --status`.
An arbitrary `--set owner=platform-team` does not touch it, because silently resetting the decay clock every time a metadata key changes is exactly how the vault becomes the write-only archive the requirements name as an anti-measure.
`--set touched=<date>` overrides it explicitly, and every change including `touched` is listed in the command's own `changed` block, so nothing about it is silent.

The `review` screen touches on every decision, including `keep`: having looked at the idea is itself what resets its age.

## Loud failure

Every parse failure carries `path:line: reason` and a non-zero exit.
The first malformed file stops a walk; there is no skip-and-continue mode, and no partial answer is printed alongside the error.

Two things needed building rather than configuring:

`gopkg.in/yaml.v3` **accepts duplicate keys silently.**
A second brain must not, so the mapping node is walked and a repeated key is reported with both line numbers.

`yaml.v3`'s error line numbers are relative to the frontmatter fragment, so they are offset by the opening delimiter to name a real file line.
The exact strings are golden files, one per malformed shape: a wrong line number in an error message is a real defect, and a golden file is the only thing that keeps it from drifting.

Exit codes are part of the contract an agent scripts against:

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1 | a bad command line, a missing record, or a refused action |
| 2 | the vault's data does not make sense: malformed frontmatter, a naive timestamp, a duplicate id, an unknown status |

### Failure modes

| Failure | Behaviour |
| --- | --- |
| Vault not found | Exit non-zero naming the resolution order tried. Never silently create a vault. |
| More than one home vault | Exit non-zero naming every candidate and both ways to select one. Never guess. |
| Malformed frontmatter | `path:line: reason`, non-zero exit. The file is neither skipped nor repaired. |
| Unknown status value | Rejected against the closed vocabulary, listing the valid values. |
| Naive timestamp on read | Rejected as a corrupt record, not silently assumed to be vault-local. Silent assumption is how a meeting ends up an hour off. |
| Nonexistent or ambiguous local time on write | Rejected, naming the instants it could mean, so an explicit offset can be supplied. |
| Duplicate id | Both paths reported; the tool refuses to guess which is canonical. |
| Interrupted write | Impossible to observe, by NFR-3's write-then-rename. |
| Disk full | Rename never happens; the previous file stands. Reported, not swallowed. |
| Concurrent writers | Two agents writing different files never collide. Two writing the same id is last-rename-wins, and `vault/`'s git history is the recovery path. |
| A mutation that would corrupt a file | Re-parsed before it is written, and refused if it would not parse. |

## Vault resolution

In order, and reported verbatim when nothing is found:

1. `--vault <path>`
2. `$BRAIN_AXI_VAULT`
3. `.brain/config.yml` or `vault/.brain/config.yml`, walking up from the working directory
4. `~/vault` and `~/secondbrain/vault`

`init` writes `vault/` under the working directory, so step 4 has to include `~/vault`: run from the home directory, which is what the README documents, that is where the vault is.

Steps 1 to 3 are ordered, and an earlier hit outranks a later one.
Step 4 is not ordered, because nothing orders it: the walk up ranks by proximity to the working directory, and neither home location is nearer to anything.
So a machine holding both is an ambiguity rather than a precedence question, and it is refused, naming every candidate and both ways to settle it.
Picking one would read and write somebody's notes in whichever brain sorted first, and the mistake would only surface once the other had gone quiet.
This is the shape `timeref.Zone.Normalise` already uses for an ambiguous local time.

The refusal is in `vault.Open`, so it is the same for a command that reads and a command that writes.
Answering `today` out of the wrong brain is the failure that started this, and a resolution that changed with the subcommand would be worse than either answer.

A vault is never created implicitly.

## The ASCII interface question

The user asked whether a live monitoring interface makes sense and what it would monitor.
That honesty deserves an honest answer, and it comes in three parts.

**No - a live monitoring TUI would be wrong here.**
That kind of interface earns its place when a tool runs a long, multi-step pipeline with real intermediate state: review, test, lint, docs, push, CI.
Each step takes minutes, can fail, and the user genuinely cannot know where things stand without watching.
There is something to watch.
`brain-axi today` finishes in eight milliseconds.
A progress interface over an instant operation is theatre - and worse, it is machinery that has to be maintained, tested, and kept correct on every terminal, forever, in exchange for nothing.
The user's instinct was right: there is nothing to monitor.

**Yes - but what the user actually wants is a beautiful static render.**
The appeal of a composed terminal view is that it shows useful information in a dense, readable format instead of a wall of text.
That is achievable with zero TUI machinery: bare `brain-axi` prints one framed, aligned dashboard and exits.
No event loop, no alternate screen buffer, no redraw, no resize handling.
Golden-file testable.
Pipeable.

The frame below is what the tool prints, with sample data.
Its labels are the board's own vocabulary, so one concept is not called two things on two screens.

```
╭─ brain ───────────────────────────────── Friday, 2026-09-04 ─╮
│                                                              │
│  TODAY                                                       │
│    09:00 ─ 09:30   daily sync                 platform-team  │
│  ✓ 14:00 ─ 15:00   Platform team sync         platform-team  │
│  ▸ 14:30 ─ 15:30   review the product pitch deck             │
│                                                              │
│  THIS WEEK                                                   │
│    Mo ·  Tu ·  We ●●  Th ·  [Fr] ●●●+  Sa ·  Su ·            │
│    Fr  Platform team sync                                    │
│                                                              │
│  TASKS                                                       │
│    ● migrate-staging-db → platform-team       28d unchecked  │
│                                                              │
│  IDEAS PENDING                                               │
│    ● customer-referral                                  24d  │
│    ○ shared-vault                                        9d  │
│                                                              │
╰─ 4 open in backlog · 1 idea stale · 1 task needs attention ──╯
```

Everything on that frame is information the user cannot hold in their head: which event is next (`▸`), which they already completed (`✓`), how loaded each remaining day is, which day is today (`[Fr]`), and which idea has aged past its horizon (`●` against `○`).
The footer is the only place the work backlog appears, and it is read-only.

**And one place a real interactive screen does earn its keep.**
There is exactly one workflow with genuine interactive state: triaging stale ideas.
Six ideas, each needing a keep / start / drop / defer decision.
Through chat that is twelve exchanges of ceremony.
As one screen it is six keystrokes.

```
╭─ weekly review ────────────────────────────────────── 1 / 2 ─╮
│                                                              │
│  customer-referral                                      31d  │
│  customer referral program                                   │
│                                                              │
│  Define eligibility and incentive criteria before launch.    │
│                                                              │
│  created 2026-08-01 · touched 2026-08-01 · nudge 14d         │
│                                                              │
├──────────────────────────────────────────────────────────────┤
│  k keep   b build   d drop   s defer 30 days   q quit        │
╰──────────────────────────────────────────────────────────────╯
```

`review` is `golang.org/x/term` raw mode and nothing else: print a card, read one byte, write the decision, print the next card.
There is no event loop, no alternate screen buffer, no redraw and no resize handling, which is the same posture the dashboard takes.
Each decision is written immediately rather than at the end, so an interrupted session keeps every decision already made.
The decision logic is a pure function of the action, so it is tested without a terminal.

| Surface | Verdict |
| --- | --- |
| Framed static dashboard on bare invocation | **Built.** All of the appeal, none of the machinery. |
| Interactive stale-idea triage | **Built** - the one workflow with real interactive state. |
| Live-updating monitor / progress view | **Not built.** Nothing to monitor, and nothing planned that would create something to monitor. This is the one place the all-in-one scope decision does not apply, because the feature is wrong rather than merely later. |

### Human surfaces and agent surfaces are separate

Both frames are for a person, never for the agent.
When stdout is not a terminal the dashboard degrades to plain lines, the time-range separator becomes a plain hyphen, and no box-drawing character is emitted at all - asserted per command in the CLI golden tests.
Box characters are pure token cost with no information.

Agent-facing output is the axi house style: `name[n]{cols}:` blocks with two-space-indented comma rows, `attention[n]:` for what needs looking at, `help[n]:` for what to run next, and `--json` on every command.
Columns are aligned on display width, not byte length, so byte padding cannot push a column out of alignment.
A `render` golden test asserts that mixed Unicode and ASCII rows line up, and the dashboard goldens assert every drawn line is exactly the frame width in cells.

## Unicode text

Unicode text is a first-class case, not an afterthought.

Folding for search and slugs is NFD normalisation with combining marks dropped, plus explicit mappings for characters without canonical decompositions.
Folding is applied to both sides, so a query matches in either direction.
Decomposed input folds the same as precomposed, which matters because Obsidian on macOS writes decomposed text into files a Linux editor wrote precomposed.

Display width counts cells: combining marks take none, East Asian wide and fullwidth runes take two.

The interface itself is English on every surface, and there is no translation layer, no `language` setting and no locale machinery.
What a user typed is not interface: record titles, bodies and people names are rendered verbatim in whatever language they were written, which is precisely what the folding and width rules above exist to get right.
Dates the tool prints are ISO (`2006-01-02`), because a day/month/year ordering is itself a locale assumption.

## Agent integration

A tracked skill in `skills/secondbrain/`, installed by `brain-axi setup skill`.
The Markdown in that directory is the only copy: it is embedded into the binary from where it lives, so `setup skill` cannot install a version that disagrees with the tool it came from.

`--claude`, `--codex` and `--pi` are the known agents, `--dir <path>` covers anything else, and naming none installs into every known directory that already exists.
Each agent resolves its own skills directory rather than declaring one relative to the home directory, because pi's is not expressible that way: it sits under an agent directory that `$PI_CODING_AGENT_DIR` moves wholesale.
`doctor` reports every known agent's copy, so a completed installation into a known agent's directory never looks the same as a missing one.

The skill teaches the agent five things and nothing more:

1. **When** to reach for the brain rather than answering from conversation memory.
2. **How** to resolve natural-language relative dates into absolute timezone-qualified arguments.
3. **The pre-write capture gate:** read a raw meeting note in full, scope candidate records, preserve
   included wording and detail, identify omitted context or noise, present the proposal, ask only
   necessary clarifications, and wait for confirmation before generating YAML or invoking the CLI.
4. **How to write a batch file** after confirmation: which sections exist, and that every date in one
   is absolute.
5. **The post-write echo-back rule:** read every stored row back to the user, grouped by section.
   This reports what was persisted and does not replace the pre-write confirmation gate.

The host agent's own instruction surface is not modified at all.
This is a genuine add-on: uninstall the skill and delete the repo, and nothing else changes.

## Install and self-upgrade

There are three ways in, and the difference between them is the whole of this section: what a binary reports as its version, and what an upgrade of it even means.

`install.sh` has two modes.
Piped from the web it downloads the newest release asset for `$(uname -s)`/`$(uname -m)`, verifies it against the release's published `checksums.txt`, and installs it; run from inside a checkout it builds that checkout instead.
`--release` and `--checkout` demand a mode outright rather than relying on detection, and detection itself keys on whether `$0` is a file inside a clone of this repository, so a script piped through `sh` while the shell happens to sit in a checkout still downloads.
Both modes install to `~/.brain-axi/bin`, link into the first of `~/.local/bin` or `/usr/local/bin`, and refuse to install a binary that does not run.
A download that fails its checksum, a platform the release does not publish, a missing `curl`/`wget` or `sha256sum`/`shasum`, and a repository with no release at all are each a loud refusal naming the concrete problem.
There is deliberately no fall back from a failed download to a source build: a user who piped the script has no checkout and may have no Go, and a silent mode switch would hide a broken release pipeline.
`go install github.com/Thanhbinh1905/secondbrain/cmd/brain-axi@latest` is the third way, and it needs nothing from this repository beyond the module path being correct.

### Reporting a version

`main.version` is an ldflags slot, and `go install` applies no ldflags, so it is only the first of three answers.
`resolveVersion` takes the stamped value, then `runtime/debug.ReadBuildInfo`'s `Main.Version` when it is a real module version rather than `(devel)`, then the `vcs.revision` build setting shortened to twelve characters with `-dirty` appended when `vcs.modified` is true.
`dev` means genuinely nothing was available, and is never a stand-in for something that was: a binary that reports `dev` while its own build information names the commit is lying to a user who cannot check it.
The value is resolved once into `buildVersion`, which is what `--version`, `doctor` and every upgrade report read.

### Upgrading

The install method is recorded, never inferred.
`install.sh` writes `~/.brain-axi/bin/.brain-axi-install` holding `checkout` or `release`, and a binary with no record at all is a `go install`.
A record naming something unknown, or one that cannot be read, is a refusal: guessing here means either replacing a binary the user did not install that way, or telling them to run a command that cannot work.
An explicit `--source` or `$BRAIN_AXI_SOURCE` outranks any record, because that is the user naming a checkout.
With no method record, a binary that `.brain-axi-source` names a checkout for, or that sits inside a clone of this repository, is a checkout install from before the method was recorded, and keeps upgrading exactly as it always did.

- **checkout** - fast-forward the recorded checkout, rebuild into a temporary file beside the installed binary, run `--version` on the result, and only then rename it into place.
  It refuses to run against a checkout with uncommitted changes.
  `--check` fetches and compares against `@{upstream}`.
- **release** - download the newest asset for this platform beside the installed binary, verify it against the published `checksums.txt`, run `--version` on it, and only then rename it into place.
  `--check` downloads only the release's one-line `version.txt` rather than a whole binary.
- **go install** - print the `go install` command that upgrades this installation and exit zero, because nothing is wrong.
  The Go toolchain placed that binary and is what replaces it; a tool that shelled out to replace itself behind the user's back would be doing something the user can do plainly.

Every path that replaces the binary runs `--version` on the replacement and keeps the old binary if that fails, and assembles the replacement beside the target so the rename that installs it stays on one device and stays atomic.

`releaseAssets` maps a Go platform to its asset name, and the names are shaped exactly like `uname -s` and `uname -m` output so `install.sh` computes one by interpolation and carries no platform table it has to keep in sync.
That map and `.github/workflows/release.yml` are the only two places the shape lives, and `TestReleaseAssetsMatchWorkflow` fails the build if they disagree.

On NFR-6's "no state outside the vault directory": two files are written outside it, both in `~/.brain-axi/bin` - `.brain-axi-source` holding the path of the checkout, and `.brain-axi-install` naming the method.
They are installation metadata rather than tool state - no command that reads or writes the vault consults either - and deleting them costs nothing but an explicit `--source` on the next upgrade, or a `go install` message for a binary that did not come from one.

`brain-axi update <id>` is the record mutation.
The two cannot be confused, because the self-upgrade takes no positional argument - which is exactly how the CLI reference spells them.

On NFR-2 as amended - no network call of its own, network reach delegated to explicitly invoked CLIs: the binary links no network client and holds no token, and the query and capture paths open no socket.
The fetch inside an explicitly requested upgrade is delegated to `git` for a checkout install and to `curl` or `wget` for a release install, and forge status to `gh` or `glab` (see "Forge status").
A host with neither download tool is told so and refused, rather than the binary growing an HTTP client to cover it.
Nothing else in the tool reaches anything.

## Testing

| Layer | Approach |
| --- | --- |
| frontmatter | Table-driven round-trip tests, including special YAML scalars and Unicode text. Golden files for error position reporting, one per malformed shape. |
| timeref | Property tests across eleven zones covering DST-free, whole-hour DST, thirty-minute DST, quarter-hour base offsets, midnight transitions, negative DST and a skipped calendar day. Plus known-answer tests pinning each measured stdlib behaviour. |
| unitext | Bidirectional Unicode folding, decomposed input, display width against byte length, slug generation. |
| vault | Fixture vaults committed to the repo, including a deliberately corrupt one asserting the loud failure of NFR-4 file by file. Atomic-write and never-overwrite assertions. Batch ingest: every section stored by one batch, a malformed entry anywhere writing nothing however many good entries preceded it, self-collision within one batch, and a forced mid-write I/O failure asserting the file-by-file report. |
| forge | URL detection across GitHub, GitLab.com and a self-hosted host, and refusal of everything that is not a change proposal. Status mapping for both CLIs from recorded payloads, through a fake runner, so the suite needs no network and no token. Missing CLI, unauthenticated host and unreachable host as three distinct answers. |
| query | Chronology, next-event flagging, recurrence expansion with exceptions, age and staleness, task ordering and filters, closed tasks no longer decaying, search ranking, link resolution, and a 5,501-file latency budget. |
| render | Golden dashboard frames, with a Unicode-text case asserting column alignment and a per-line cell-width assertion. |
| review | Golden card, every action's plan, immediate-write behaviour, and an exhausted-input case. |
| ics | Golden stream plus RFC 5545 structure assertions: CRLF endings, the 75-octet fold, unfolding round-trip, and one component per series. |
| CLI | Golden files on both text and `--json` output for every command, plus a golden per refusal with its exit code. The text format is an agent contract and must not drift silently. A suite-wide scripted forge runner, installed in `TestMain`, makes it structurally impossible for a test to reach a network or need a token; the real-forge check against this repository's own merged pull request is opt-in behind `$BRAIN_AXI_FORGE_E2E`. `TestOfflineCommandsNeverReachAForge` asserts that no offline command runs a forge CLI even with an unreachable linked record in the vault. |

## Build order

The order below is why the two modules that carry every real bug were proven before anything was layered on them.

1. `internal/frontmatter` and `internal/timeref` with their full test suites.
2. `internal/unitext`, then `internal/vault`: resolution, atomic write, loud failure.
3. `init`, then `add`, then `show`.
4. `today`, `week`, `agenda`, `ideas`, `search`.
5. `done`, `update`, `rm`, nudge horizons.
6. Recurring events, on a proven time layer.
7. The dashboard render, then the interactive `review` triage screen.
8. Wiki-links, people profiles, `.ics` export.
9. The skill, `setup skill`, `doctor`, `update` self-upgrade, `install.sh`, the release workflow.
10. A real agent session exercising every user story end to end, across the supported natural-language inputs.
11. The `task` kind on the proven record and decay layers, then its surfaces.
12. Batch ingest, on the proven record builders.
13. `internal/forge` and the `link`/`pr` commands, last, because they are the only part that leaves the machine.

## Requirement coverage

Every FR and NFR, and where it lives.

| ID | Where |
| --- | --- |
| FR-1 | `internal/vault/init.go` `Init`; `internal/vault/config.go` `Open`; `cmd/brain-axi/lifecycle.go` `cmdInit` |
| FR-2 | `internal/vault/init.go` `BuildEvent`/`BuildIdea`/`BuildNote`/`BuildPerson`, `AppendNote`; `cmd/brain-axi/capture.go` |
| FR-3 | `internal/timeref/timeref.go` `Zone.Normalise`, `ParseStored` |
| FR-4 | `internal/query/query.go` `Agenda`, `Today`, `Week`; `cmd/brain-axi/recall.go` |
| FR-5 | `internal/query/query.go` `Ideas`; `internal/vault/record.go` `AgeDays`, `PastHorizon` |
| FR-6 | `internal/query/query.go` `Search`; `internal/unitext/unitext.go` `Fold` |
| FR-7 | `internal/frontmatter/frontmatter.go` `Set`/`Bytes`; `cmd/brain-axi/capture.go` `applyChanges` |
| FR-8 | `cmd/brain-axi/capture.go` `cmdRemove` |
| FR-9 | `internal/render/dashboard.go`; `cmd/brain-axi/recall.go` `cmdDashboard` |
| FR-10 | `internal/render/render.go` `Emit`; every `cmd*` function |
| FR-11 | `internal/skill/skill.go`; `cmd/brain-axi/lifecycle.go` `cmdSetup` |
| FR-12 | `cmd/brain-axi/capture.go` `findOverlaps` |
| FR-13 | `cmd/brain-axi/selfupdate.go` `cmdSelfUpdate`, `resolveMethod`, `updateFromCheckout`, `updateFromRelease`, `reportGoInstall`; `cmd/brain-axi/main.go` `resolveVersion` |
| FR-14 | `internal/review/review.go`; `cmd/brain-axi/lifecycle.go` `cmdReview` |
| FR-15 | `internal/vault/record.go` `KindTask`/`TaskStatuses`/`parseTaskFields`/`Horizon`; `internal/vault/init.go` `BuildTask`; `internal/query/query.go` `Tasks`; `cmd/brain-axi/capture.go` `addTask`; `cmd/brain-axi/recall.go` `cmdTasks`/`taskBlock` |
| FR-16 | `internal/forge/forge.go` `Detect`/`Fetch`/`Reachable`; `internal/vault/record.go` `parseForgeFields`; `cmd/brain-axi/forge.go` `cmdLink`/`cmdPR` |
| FR-17 | `skills/secondbrain/SKILL.md`; `internal/vault/batch.go` `ParseBatch`/`Write`; `cmd/brain-axi/capture.go` `addBatch`/`reportBatch` |
| FR-18 | `internal/vault/record.go` `parseLinkFields`; `internal/query/query.go` `Links`/`PointsAt`; `cmd/brain-axi/people.go` `cmdRelated`; `cmd/brain-axi/capture.go` `linksTo`; `cmd/brain-axi/lifecycle.go` `diagnose` |
| FR-19 | `internal/query/people.go` `Person`/`PersonAgenda`/`PersonAgendas`/`AttendeeAgendas`; `cmd/brain-axi/people.go` `cmdPersonAgenda`; `cmd/brain-axi/recall.go` `writePersonBlocks`/`raiseRows` |
| FR-20 | `internal/query/due.go` `Due`; `cmd/brain-axi/people.go` `cmdDue`; windows in `internal/vault/config.go` |
| FR-21 | `internal/vault/record.go` `parseFleetFields`/`ValidateFleetTaskID`; `cmd/brain-axi/forge.go` `cmdLinkFleet`/`cmdShip`; `internal/query/query.go` `Shipped` |
| FR-22 | `internal/board/board.go`; `templates/board.html`; `internal/payload`; `cmd/brain-axi/surfaces.go` `cmdBoard` |
| FR-23 | `internal/recap/recap.go`; `templates/recap.html`; `cmd/brain-axi/surfaces.go` `cmdRecap`; `internal/timeref/timeref.go` `MonthStartAfter`/`QuarterStartAfter` |
| NFR-1 | The parallel walk in `internal/vault/store.go`; asserted by `TestQueryLatencyOnFiveThousandFiles`, which measures `today`, `tasks`, `due` and the board's assembly |
| NFR-2 | One Go binary, `time/tzdata` embedded, no network client linked and no token held. Delegation only: `git` for a checkout self-upgrade, `curl`/`wget` for a release one, `gh`/`glab` for forge status and for `recap --verify-forge`, each behind an explicit command. The board and the recap write a file and serve nothing. Asserted by `TestOfflineCommandsNeverReachAForge` and `TestRecapReachesNothingWithoutVerifyForge` |
| NFR-3 | `internal/vault/store.go` `WriteFile` |
| NFR-4 | `internal/frontmatter` error positions; `internal/vault/record.go` validation; exit code 2 |
| NFR-5 | Plain Markdown with YAML frontmatter, unknown keys preserved, no tool-only files inside record directories |
| NFR-6 | No cache, no index, no lock. The files outside the vault are installation metadata: `.brain-axi-source` records a checkout path and `.brain-axi-install` records the install method beside the binary. Only lifecycle installation and `brain-axi update` use them; no command that reads or writes the vault consults either file. |
| NFR-7 | `internal/unitext` width and folding; the Unicode alignment golden tests |

Nothing in the specification is unimplemented.
