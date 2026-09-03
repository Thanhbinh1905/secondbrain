# secondbrain

A local, file-backed second brain with an agent-facing CLI (`brain-axi`).

Markdown in `vault/` is the single source of truth.
The CLI is a deterministic accessor over it: it never calls a model, and it makes no network call of
its own. Natural language understanding belongs to the agent that drives it.

## Quickstart

### Install

```sh
curl -fsSL https://raw.githubusercontent.com/Thanhbinh1905/secondbrain/main/install.sh | sh
```

No Go toolchain and no checkout.
The script downloads the newest release binary for your platform, verifies it against the release's
published `checksums.txt`, and refuses to install anything that does not match or does not run.
It is a couple of hundred lines of POSIX shell, and
[reading it first](https://raw.githubusercontent.com/Thanhbinh1905/secondbrain/main/install.sh) is a
reasonable thing to do with anything you pipe into a shell.

Both `install.sh` modes install to `~/.brain-axi/bin` and link the binary into the first of
`~/.local/bin` or `/usr/local/bin`.
`$BRAIN_AXI_INSTALL_DIR` and `$BRAIN_AXI_LINK_DIR` override those locations.
`go install` writes to `$GOBIN`, or to `$GOPATH/bin` when `$GOBIN` is unset, and creates no link.

#### With Go

```sh
go install github.com/Thanhbinh1905/secondbrain/cmd/brain-axi@latest
```

#### From a checkout, for contributors

```sh
git clone git@github.com:Thanhbinh1905/secondbrain.git
cd secondbrain
./install.sh
```

Run from inside a checkout the script builds it rather than downloading, and records the checkout so
an upgrade knows what to fast-forward. It needs Go 1.26 or newer. `./install.sh --release` and
`./install.sh --checkout` demand a mode outright, so nothing depends on that detection.

#### What each path reports, and how each one upgrades

The three differ, and the binary knows which one it is: `install.sh` records the method beside the
binary.
A legacy source record or a binary inside a checkout remains a checkout install; another binary with
no method record is treated as `go install`.
A record naming something else is refused rather than guessed at, because guessing means either
replacing a binary you did not install that way or printing a command that cannot work.

| Installed by | `brain-axi --version` | `brain-axi update` |
| --- | --- | --- |
| the piped script | the release tag, `v1.2.3` | downloads the newest release, verifies its checksum, replaces |
| `go install` | the module version the toolchain recorded: a tag, or a pseudo-version naming the commit | prints the `go install` command that upgrades it, and exits zero - nothing is wrong |
| a checkout | the checkout's short commit, with `-dirty` when tracked files had unstaged changes | fast-forwards the checkout, rebuilds, replaces |

`--check` reports on any of them without upgrading. Every path that replaces the binary runs
`--version` on the replacement first and keeps the old one if that fails.

### Create the vault

```sh
brain-axi init
brain-axi setup skill --claude
brain-axi doctor
```

`init` creates `vault/` under the working directory with its skeleton, its config and its own git
repository, so run it from your home directory and the vault is `~/vault`. `--path` puts it
somewhere else. It never overwrites an existing config, so re-running it is safe.

Every other command finds that vault from anywhere. Resolution is `--vault`, then
`$BRAIN_AXI_VAULT`, then a walk up from the working directory, then `~/vault` and
`~/secondbrain/vault`. If no explicit or nearer vault has already settled the resolution, those
last two are peers, so a machine holding both gets a refusal naming each one rather than a silent
choice between two brains.
Set `--vault <path>` or `$BRAIN_AXI_VAULT=<path>` to name the one you mean.

It takes the timezone from your machine and reports the zone it settled on, because that zone is
what every stored timestamp's UTC offset is written from. Name one yourself with
`brain-axi init --timezone Europe/Lisbon`, or set it in `.brain/config.yml` afterwards. On a machine
whose zone cannot be determined, `init` says so and asks for `--timezone` rather than guessing: a
wrong offset corrupts every record quietly, and a refusal costs one command.

`git init` leaves a repository holding nothing, so `init` reports the new one as `initialised, no
commits yet` and names the commit that starts the history. It never makes that commit for you:
what goes into your vault's history is your call.

`setup skill` installs the agent-facing skill that teaches your agent when to reach for the brain,
how to resolve relative dates, how to review and confirm whole-meeting captures, and the post-write
echo-back rule. It knows `--claude`, `--codex` and `--pi`, installs into every one of those whose
directory already exists when you name none, and takes `--dir <path>` for anything else. `--pi`
follows `$PI_CODING_AGENT_DIR` when that is set. `doctor` reports every known agent's copy, so a
completed installation into one of those directories never looks the same as a missing one, and
tells you what is missing:

```
$ brain-axi doctor
vault:      /home/you/vault  ok
config:     Asia/Bangkok, week starts mon, nudge 14d  ok
files:      0 parsed, 0 malformed
links:      all resolved  ok
ideas:      none past their nudge horizon  ok
recurrence: every series resolves in the next year  ok
tasks:      none overdue, none past their follow-up horizon  ok
board:      no board_html configured; `brain-axi board --html <path>` writes one anywhere
forge:      gh present, glab present; no record is linked to a pull request
git:        uncommitted changes, no commits yet, no remote configured
skill:      /home/you/.claude/skills/secondbrain  installed
backlog:    no backlog_cmd configured; the dashboard footer omits it
binary:     v1.0.0
attention[2]:
  - vault has no commits - an empty repository protects nothing; `git -C '/home/you/vault' add -A && git -C '/home/you/vault' commit -m "vault"` starts the history
  - vault has no remote - a disk failure loses it; `git -C '/home/you/vault' remote add origin <private repo>` closes the gap
```

Those two are the durability gaps `init` cannot close for you, in the order they matter: local
history protects against a bad edit, a remote against a dead disk, and neither exists until you
make it.

### Capture something

The agent resolves the date; the CLI takes absolute arguments only.

```sh
brain-axi add event "Platform team sync" --when 2026-09-04T14:00 --duration 60m --with platform-team
brain-axi add idea "customer referral program"
brain-axi add task "migrate the staging database" --assignee platform-team --follow-up-after 14d
brain-axi add note "ask the infrastructure team about CI capacity"
```

An event capture reports the resolved absolute date and its weekday, which is what the agent echoes
back to you before treating the capture as done.

```
$ brain-axi add event "Platform team retrospective" --when 2026-09-11T16:00 --duration 45m
added: event
id: platform-team-retrospective-20260911
path: events/2026-09-11-platform-team-retrospective.md
title: Platform team retrospective
when: 2026-09-11T16:00:00+07:00
duration: 45m
help[2]:
  - Run `brain-axi show platform-team-retrospective-20260911` to check it
  - Echo the resolved absolute date and weekday back to the user
```

### Capture a whole meeting at once

Give your agent a raw `.txt` file, `.md` file, or unstructured block of meeting notes.
It reads the complete input and first presents a concise capture proposal with candidate events,
ideas, tasks, delegated follow-ups and notes.
The proposal also identifies context or noise that will be omitted and preserves the original wording
and detail of included content.
The agent asks only the necessary clarification questions, then waits for your confirmation.

The YAML below is an agent-generated intermediate payload after confirmation.
It is not a format you need to provide:

```yaml
# Agent-generated after the capture proposal is confirmed.
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

After you confirm the proposal, the agent generates that intermediate file and runs:

```
$ brain-axi add --batch meeting-2026-09-02.yml
batch: meeting-2026-09-02.yml
stored: 5
ideas[1]{id,title,detail,path}:
  review-the-cache-expiration-policy, review the cache expiration policy, horizon 30d, ideas/review-the-cache-expiration-policy.md
tasks[1]{id,title,detail,path}:
  review-the-service-capacity-report, review the service capacity report, "due 2026-09-05T17:00:00+07:00, follow up after 3d", tasks/review-the-service-capacity-report.md
delegated[1]{id,title,detail,path}:
  update-the-staging-runbook, update the staging runbook, "assignee operations-team, follow up after 14d", tasks/update-the-staging-runbook.md
notes[1]{id,title,detail,path}:
  daily-2026-09-02, 2026-09-02, 1 bullet(s) appended to the daily file, daily/2026-09-02.md
events[1]{id,title,detail,path}:
  service-planning-follow-up-20260909, Service planning follow-up, 2026-09-09T14:00:00+07:00, events/2026-09-09-service-planning-follow-up.md
help[2]:
  - Read every stored row back to the user, grouped by section, so a misread is corrected now rather than in three weeks
  - Run `brain-axi show <id>` for any row they question
```

Ingest is atomic. Every entry is validated - vocabularies, timestamps, ids and paths, against the
vault and against the other entries - before a single file is written, so a malformed entry anywhere
fails the whole batch with `path:line: reason` and stores nothing.
The post-write report is still read back grouped by section, but it is not a substitute for the
pre-write proposal and confirmation gate.

### The four questions

These are the questions the tool exists to answer.

**"What is on today?"** - in order, with the next event flagged, and any task due, overdue or
unchecked alongside it.

```
$ brain-axi today
range: 2026-09-04..2026-09-04
events[3]{when,id,title,with,flag}:
  09:00-09:30, daily-sync-20260902,                     daily sync,                     platform-team, recurring
  14:00-15:00, platform-team-sync-20260904,             Platform team sync,             platform-team, next
  14:30-15:30, review-the-product-pitch-deck-20260904,  review the product pitch deck,   -,            -
tasks[1]{due,id,title,assignee,status,flag}:
  "-", migrate-staging-db, migrate the staging database, platform-team, waiting, unchecked-28d
attention[3]:
  - next: platform-team-sync-20260904 at 14:00 04/09
  - overlap: platform-team-sync-20260904 and review-the-product-pitch-deck-20260904 both run at 14:30 04/09
  - migrate-staging-db: delegated to platform-team and not checked for 28d, past its 14d follow-up horizon
```

**"Which ideas are still pending?"** - with the age of each one, which is the thing you cannot
produce from memory.

```
$ brain-axi ideas --status pending
ideas[3]{id,title,status,age,touched}:
  customer-referral, customer referral program,             pending, 24d, 2026-08-11
  shared-vault,      share a vault across a team,           pending,  9d, 2026-08-26
  calendar-export,   export the calendar as .ics,           pending,  3d, 2026-09-01
attention[1]: customer-referral past its 14d nudge horizon
```

**"What am I waiting on?"** - commitments to follow up, most urgent first. A brain task is something
you have to remember to *check*; it never becomes a dispatched work item, and this tool never writes
to any backlog.

```
$ brain-axi tasks
tasks[2]{due,id,title,assignee,status,flag}:
  2026-09-05T17:00:00+07:00, review-ci-capacity, review CI capacity,          -,            open,    -
  "-",                       migrate-staging-db, migrate the staging database, platform-team, waiting, unchecked-28d
attention[1]: migrate-staging-db: delegated to platform-team and not checked for 28d, past its 14d follow-up horizon
```

**"What does my week look like?"** - the vault's side of the week, against your configured first
day. Your agent folds in live work from the task backlog; the brain never owns a backlog task and
never writes to one.

```
$ brain-axi week
range: 2026-08-31..2026-09-06
events[5]{when,id,title,with,flag}:
  2026-09-02 09:00-09:30, daily-sync-20260902,                     daily sync,                     platform-team, recurring
  2026-09-03 09:00-09:30, daily-sync-20260902,                     daily sync,                     platform-team, recurring
  2026-09-04 09:00-09:30, daily-sync-20260902,                     daily sync,                     platform-team, recurring
  2026-09-04 14:00-15:00, platform-team-sync-20260904,             Platform team sync,             platform-team, next
  2026-09-04 14:30-15:30, review-the-product-pitch-deck-20260904,  review the product pitch deck,   -,            -
tasks[2]{due,id,title,assignee,status,flag}:
  2026-09-05T17:00:00+07:00, review-ci-capacity, review CI capacity,          -,            open,    -
  "-",                       migrate-staging-db, migrate the staging database, platform-team, waiting, unchecked-28d
```

### Unicode-aware search and terminal output

The vault preserves UTF-8 text, including names and notes with diacritics.
Search matches text with and without diacritics in both directions.
Column alignment counts display cells rather than bytes, so wide and combining characters do not
break tables or dashboard frames.

The dashboard and review screen are human-facing terminal surfaces.
Piped output stays plain and machine-readable, so box-drawing characters never reach an agent's
context.

## Only when something needs you

`brain-axi due` answers exactly one question - what needs attention right now - and prints nothing
at all when nothing does. It reads files, writes nothing, reaches nothing, and is cheap enough to
run on a short interval: on a 5,501-file vault it answers in about 52 ms, inside the same 100 ms
budget `today` is held to.

```
$ brain-axi due
due[2]{category,id,who,title,why}:
  delegated, migrate-staging-db,         platform-team, migrate the staging database, "platform-team has had this for 28d with no update, past its 14d follow-up horizon"
  event,     platform-team-sync-20260904, "-",          Platform team sync,            "starts in 10m, at 14:00"
```

Three categories, each with its own window in `.brain/config.yml` and each togglable on its own with
`--delegated`, `--events` and `--ideas`. Naming none of them reports all three.

| category | what it catches | window |
| --- | --- | --- |
| `delegated` | a task handed to a named person, past its follow-up horizon, with no update since | the record's `follow_up_after:`, or the vault's |
| `event` | an occurrence about to start | `due_within:`, 30 minutes by default |
| `dormant_idea` | an idea nobody has touched in a long time | `dormant_after:`, 30 days by default |

The delegated category is the one worth building the command for, so its line names the person and
how long it has been. `--json` carries the same fields structurally:

```
$ brain-axi due --json
{
  "due": [
    {
      "category": "delegated",
      "id": "migrate-staging-db",
      "kind": "task",
      "title": "migrate the staging database",
      "reason": "platform-team has had this for 28d with no update, past its 14d follow-up horizon",
      "person": "platform-team",
      "days": 28,
      "window": "14d",
      "path": "tasks/migrate-staging-db.md"
    },
    {
      "category": "event",
      "id": "platform-team-sync-20260904",
      "kind": "event",
      "title": "Platform team sync",
      "reason": "starts in 10m, at 14:00",
      "start": "2026-09-04T14:00:00+07:00",
      "minutes_until": 10,
      "window": "30m",
      "path": "events/2026-09-04-platform-team-sync.md"
    }
  ],
  "now": "2026-09-04T13:50:00+07:00"
}
```

`dormant_after` is deliberately a different knob from `nudge_after`. The nudge horizon means "poke
me about this the next time I triage"; dormancy means "this has effectively stopped". Collapsing
them would make `due` a second copy of `brief`.

## Links, people, and what a meeting produced

Any record can carry a flat `links:` list of other record ids. It is plain text you can hand-edit
and correct, there is no graph store, and resolution walks the vault like everything else.

```yaml
---
type: idea
id: cache-schedule-expiry
title: cache the schedule expiry lookup
links: [platform-team-sync-20260904]
---
```

`brain-axi related <id>` reads both directions at once, naming the field that did the pointing -
because "this meeting produced that idea" and "that idea mentions this meeting in passing" are
different answers:

```
$ brain-axi related platform-team-sync-20260904
id: platform-team-sync-20260904
type: event
path: events/2026-09-04-platform-team-sync.md
points_to[1]{id,type,via,resolved,title}:
  platform-team, person, with, yes, Platform team
pointed_to_by[1]{id,type,via,resolved,title}:
  cache-schedule-expiry, idea, links, yes, cache the schedule expiry lookup
```

A link to an id no record claims is reported, never rejected: writing a link before its target
exists is ordinary. `doctor` names every one of them with the file and the line to fix it on, the
same way it already treats a dangling `with:` or `assignee:`.

### People are records, not just names

A `people/` record now answers what somebody is holding, what has closed, and what is waiting to be
raised with them. All three are derived from the other records: nothing about a person's workload is
stored in the profile, because a copy of a fact in a second place is a copy that can disagree with
the first.

```
$ brain-axi show platform-team
id: platform-team
type: person
path: people/platform-team.md
title: Platform team
created: 2026-08-01
backlinks[4]{id,type,via,title}:
  ask-ci-capacity,             task,  raise_with, ask about CI capacity
  platform-team-sync-20260904, event, with,       Platform team sync
  migrate-staging-db,         task,  assignee,   migrate the staging database
  daily-sync-20260902,        event, with,       daily sync
open_items[1]{due,id,title,assignee,status,flag}:
  "-", migrate-staging-db, migrate the staging database, platform-team, waiting, unchecked-28d
closed_items[0]: nothing assigned to them has closed
agenda[1]{id,type,title,waiting}:
  ask-ci-capacity, task, ask about CI capacity, 2d
```

A record joins somebody's agenda by naming them in `raise_with:`, which `add` writes for you:

```sh
brain-axi add task "ask about CI capacity" --raise-with platform-team
```

`brain-axi agenda <person>` is the thirty-second read before you walk into the room with them:

```
$ brain-axi agenda platform-team
person: platform-team
title: Platform team
path: people/platform-team.md
agenda[1]{id,type,title,waiting}:
  ask-ci-capacity, task, ask about CI capacity, 2d
```

An item leaves the agenda two ways, both ordinary edits: the record closes, or it picks up a
`raised:` date (`brain-axi update <id> --set raised=2026-09-04`). And because an agenda item is only
useful next to the meeting it is for, `today`, `week` and the board carry it beside the event
whenever that person is in the event's `with:` list:

```
$ brain-axi today
range: 2026-09-04..2026-09-04
events[3]{when,id,title,with,flag}:
  09:00-09:30, daily-sync-20260902,                         daily sync,                    platform-team, recurring
  14:00-15:00, platform-team-sync-20260904,                 Platform team sync,            platform-team, next
  14:30-15:30, review-the-product-pitch-deck-20260904,      review the product pitch deck, -,             -
tasks[1]{due,id,title,assignee,status,flag}:
  "-", migrate-staging-db, migrate the staging database, platform-team, waiting, unchecked-28d
raise_with[1]{person,id,type,title,waiting}:
  platform-team, ask-ci-capacity, task, ask about CI capacity, 2d
```

## Pull requests, without a network client

Attach a GitHub pull request or a GitLab merge request to any record, then check it when you want
to:

```sh
brain-axi link migrate-staging-db https://github.com/owner/repo/pull/12
brain-axi pr --refresh
```

```
$ brain-axi pr
pull_requests[1]{id,state,checks,checked,url}:
  migrate-staging-db, merged, passing, 2026-09-04T13:30:00+07:00 (13d ago), https://github.com/owner/repo/pull/12
attention[1]: every status above is cached, as of the time in its checked column; pass --refresh to read the forges now
help[2]:
  - Run `brain-axi pr --refresh` to read every linked forge now
  - Run `brain-axi doctor` to check which forges this machine can reach
```

Three properties make this safe to rely on:

- **The binary opens no socket and holds no token.** Reach is delegated to your own already
  authenticated `gh` and `glab`. Self-hosted GitLab works because `glab` resolves the host from its
  own configuration; `brain-axi` never learns about it.
- **`today`, `week`, `agenda`, `ideas`, `search` and the bare dashboard never reach a forge.** Only
  an explicit `pr`, a `--refresh` or `doctor` does. A second brain that cannot tell you your day on
  a plane is broken.
- **A cached status always shows when it was read.** It lives in the record's own frontmatter, where
  you can see it and edit it. A refresh that fails falls back to the cache only out loud, naming
  both the failure and the age of what it is showing, and a missing `gh`, an unauthenticated host or
  an unreachable forge is reported as exactly that - never as an empty status that reads as fine.

### The fleet bridge

Two narrow write commands let an external supervisor leave a mark on a record. Both are pure local
writes. brain-axi never reads a supervisor's state - it holds no endpoint, no token and no idea of
what a work item is beyond an id it was handed - so the tool stays exactly as usable on a machine
with no supervisor at all.

```sh
brain-axi link fleet migrate-staging-db --task PROJ-42
brain-axi ship calendar-export --pr https://github.com/owner/repo/pull/14 --merged-at 2026-09-04T15:40:00+07:00
```

```
$ brain-axi ship calendar-export --pr https://github.com/owner/repo/pull/14 --merged-at 2026-09-04T15:40:00+07:00
updated: calendar-export
path: ideas/calendar-export.md
changed[4]{key,from,to}:
  shipped_at, (unset),    2026-09-04T15:40:00+07:00
  shipped_pr, (unset),    https://github.com/owner/repo/pull/14
  status,     pending,    shipped
  touched,    2026-09-01, 2026-09-04
```

Both validate strictly and fail loudly on a malformed url, id or timestamp, and on a record that
does not exist. `--merged-at` must carry an explicit UTC offset: it is the one date in the vault
that says when work actually landed, and every period report counts from it. What shipped is
queryable by period, by name, through `recap`.

## The board

`brain-axi board` is five panes, always in this order: **Today**, **This week**, **Tasks**, **Ideas
pending**, **Waiting on others**. Each has its own empty-state string, so an empty week renders as
an empty week and never as a missing pane.

```
$ brain-axi board
brain-axi board - 2026-09-04T13:50:00+07:00
TODAY
  09:00-09:30  daily sync                        recurring
    - 2d waiting  ask about CI capacity  raise with platform-team
  14:00-15:00  Platform team sync                     next
  14:30-15:30  review the product pitch deck
THIS WEEK
  2026-09-02 09:00-09:30  daily sync             recurring
    - 2d waiting  ask about CI capacity  raise with platform-team
  2026-09-03 09:00-09:30  daily sync             recurring
  2026-09-04 09:00-09:30  daily sync             recurring
  2026-09-04 14:00-15:00  Platform team sync          next
  2026-09-04 14:30-15:30  review the product pitch deck
TASKS
  2026-09-05  review CI capacity                      open
           -  ask about CI capacity                   open
IDEAS PENDING
  24d  customer referral                          stale-24d
   9d  share a vault across a team             horizon 14d
   3d  export the calendar as .ics             horizon 14d
   0d  cache the schedule expiry lookup        horizon 14d
WAITING ON OTHERS
  -  migrate the staging database            unchecked-28d
today 4 · week 6 · tasks 2 · ideas 4 · waiting 1
```

`--html <path>` writes the same board as a self-contained HTML file:

```
$ brain-axi board --html ~/secondbrain/board.html
html: /home/you/secondbrain/board.html
schema: brain-board.v1
bytes: 13769
panes[5]{pane,rows}:
  today,   4
  week,    6
  tasks,   2
  ideas,   4
  waiting, 1
```

Five properties are what make this a surface you can leave running:

- **One data assembly path, two renderers.** The framed board and the HTML page are laid out from
  the same `brain-board.v1` model, and the page carries that model verbatim, so the two cannot
  disagree about what is on the board.
- **A committed template owns the markup.** `templates/board.html` owns layout, styling, pane order
  and every empty-state string. Building a board substitutes one JSON payload into the template's
  single data slot and generates nothing else, so no run can re-author the board's appearance.
- **Validation is fail-closed and happens first.** A wrong schema, a missing field, a wrong type or
  an unknown pane is refused with `path:line: reason` and a non-zero exit, before the existing board
  file is touched. There is no partial render, and the replacement is an atomic rename.
- **The path is yours and never moves.** Pass `--html`, or set `board_html:` in the config once. A
  rebuild replaces the same file in place, so an external viewer's URL stays stable.
- **brain-axi opens no socket and serves nothing.** Writing a file is the entire integration seam.
  `--open` may hand that file to the command you configure as `board_open_cmd:`, and when that
  command is missing or fails it says so plainly and exits non-zero while **keeping** the HTML file
  it already wrote.

`doctor` reads the payload back out of the board at `board_html:` and validates it, so a page a
person hand-edited or an older binary wrote is named with the line that is wrong.

### An annotation is input, never instruction

A review surface may let people annotate that page. Those annotations are applied only by running
ordinary `brain-axi` commands, and the board never writes to the vault - building one leaves every
record byte-for-byte as it was.

An annotation is **input, never instruction and never authority**. It is not executed, and it
confers no permission. Whoever reads it decides what to do about it, and does that with the same
commands they would have used anyway.

## The recap

`brain-axi recap <week|month|quarter>` reports what a period produced. `--from` and `--to` answer
any other span, and `--html <path>` writes it as a self-contained page on the same contract the
board uses.

```
$ brain-axi recap month
brain-axi recap - 2026-09
2026-09-01 to 2026-09-30, Asia/Bangkok
SHIPPED
  ideas shipped                           1 (0 vs 2026-08)
  2026-09-04  export the calendar as .ics calendar-export
COMMITMENTS
  commitments kept                        0 (0 vs 2026-08)
  commitments made                       2 (+1 vs 2026-08)
  2026-09-02  ask about CI capacity       ask-ci-capacity
  2026-09-01  review CI capacity          review-ci-capacity
PULL REQUESTS
  merged in this period                                  1
  opened in this period                            unknown
  open or draft now                                unknown
  closed now                                       unknown
  2026-09-04  export the calendar as .ics calendar-export
MEETINGS
  meetings held                          3 (+3 vs 2026-08)
  ideas linked to them                   1 (+1 vs 2026-08)
  2026-09-02  daily sync                  daily-sync-20260902
  2026-09-04  Platform team sync platform-team-sync-20260904
  2026-09-04  rev… review-the-product-pitch-deck-20260904
DELEGATED
  delegated items done                    0 (0 vs 2026-08)
  delegated items still unchecked                        1
  28d  migrate the staging database     migrate-staging-db
DORMANT IDEAS
  ideas past the dormancy window                         0
  no idea is past the dormancy window
every number counted from this vault
```

Four rules shape all of it, and each one is enforced rather than intended:

1. **It counts outcomes, never activity.** There is no metric for a commit, a line or an hour. Ideas
   that shipped are listed by name, because a count of them is not something anybody can act on.
2. **An unknown value renders as unknown, never as zero.** The vault records no date a pull request
   was opened, so that number reads `unknown` and says why. A period with no merges reads `0`. In
   the run above, `open or draft now` is unknown too, because no linked record has ever been checked
   - which is not the same as none being open.
3. **A slow period renders neutrally.** Nothing here evaluates. An empty month is reported as an
   empty month, with no encouragement, no judgement and nothing that reads as a target missed.
4. **The only comparison is against this vault's own previous equivalent period.** There is no
   external benchmark and no target. The payload names the span it compared against, so you can
   check it.

Timestamp-backed outcomes remain reconstructable for any period: ideas shipped, merged pull
requests, commitments made, meetings held, and ideas linked to those meetings.
Commitments kept, delegated items done or still unchecked, and dormant ideas depend on mutable
status or touched dates, so a closed past period reports them as `unknown` instead of rewriting
history from today's state.
A current period reports every metric from the state that is true now.

Period boundaries use the vault's timezone, and the week start is the one you configured. Month and
quarter arithmetic runs on the calendar rather than on instants, so the 31st of a month resolves to
the span a calendar shows.

`--verify-forge` is opt-in. With it, the recap re-reads every linked pull request through the same
`gh` and `glab` delegation everything else uses and reports where the record and the forge disagree.
Without it the command makes no network call at all.

## Everything else

```
brain-axi agenda --from 2026-09-01 --to 2026-09-07   any date range
brain-axi agenda platform-team                       what is waiting to be raised with them
brain-axi show <id>                                  one record, its links and its backlinks
brain-axi related <id>                               both directions of the link graph
brain-axi due                                        what needs attention right now, or silence
brain-axi board                                      five panes, framed or as HTML
brain-axi recap month                                what the period produced
brain-axi done <id>                                  event -> done, task -> done, idea -> shipped
brain-axi ship <id> --pr <url> --merged-at <ts>      record that the work landed
brain-axi link fleet <id> --task <external-id>       note an external work item on a record
brain-axi update <id> --status building              change only the keys you name
brain-axi update <id> --set links=a,b                a list key is written as a list
brain-axi rm <id> --yes                              refuses without --yes
brain-axi brief                                      today, what is due, what has gone stale
brain-axi export ics --out brain.ics                 one-way iCalendar export
brain-axi update [--check]                           self-upgrade the way it was installed
brain-axi <command> --help                           one command's flags
```

Every command takes `--json`.

## The vault

```
vault/
  .brain/config.yml     timezone, first day of the week, and the decay windows
  events/               dated commitments; when: always carries a UTC offset
  ideas/                half-formed things, with a touched: date so age is visible
  tasks/                commitments to follow up, with an optional assignee: and due:
  notes/                standalone notes
  people/               who the events are with, and what is waiting to be raised
  daily/                one file per day; `add note` appends here
```

Any record may also carry `links:` (ids of other records), `raise_with:` (people it is waiting to be
raised with, cleared by a `raised:` date or by closing), `fleet_tasks:` (external work items), and
`shipped_at:` with `shipped_pr:` (when the work landed and what landed it). All of them are plain
text you can hand-edit, and all of them are validated on every read.

`.brain/config.yml` carries `timezone`, `week_starts`, `nudge_after`, and the round-three windows:
`follow_up_after` (a task's default horizon; falls back to `nudge_after`), `due_within` and
`dormant_after` for `due` and `recap`, plus the optional `board_html` and `board_open_cmd`. An
unknown key is refused with `path:line: reason` rather than ignored.

There is no database, no cache and no index - not even a hidden one. Edit any file in Obsidian or a
text editor, with or without this tool installed, and nothing gets out of step. A malformed file
after a hand edit produces `path:line: reason` and a non-zero exit; it is never skipped, defaulted
or silently repaired.

The one piece of derived data is a linked pull request's last known status, which lives in that
record's own frontmatter next to the time it was read. That is deliberate: it is visible,
hand-editable, and deleting it costs nothing but the next refresh. The board and the recap are
rendered files rather than stored state: delete either and the next command rebuilds it.

`vault/` is gitignored by this repository and is its own git repository, so upgrading the tool can
never touch your data.

## Documentation

[docs/design.md](docs/design.md) is the contributor document: layers, vault format, the batch
format, time handling, recurrence, forge status, failure modes and testing. Read it before changing
anything structural.

## Development

```sh
go build ./cmd/brain-axi
go test ./...
go test ./internal/render ./internal/review ./internal/ics ./internal/frontmatter ./cmd/brain-axi -update   # refresh golden files
```

Golden files are the contract for both the agent-facing text format and the framed human surfaces.
Run tests before regenerating them, and read the diff: a change in a golden file is a change in
something an agent parses.

The test suite needs no network and no credentials. A scripted forge runner is installed for the
whole CLI suite, so no test can reach a real forge by accident; the check against a real pull
request is opt-in behind `BRAIN_AXI_FORGE_E2E=1`. The release download path is held to the same
rule: its download is delegated to `curl` or `wget`, and the tests supply their own.

A release is cut by pushing a `v*` tag. `.github/workflows/release.yml` builds `linux/amd64`,
`linux/arm64`, `darwin/amd64` and `darwin/arm64`, stamps the tag as the version, publishes a
`checksums.txt` manifest beside the binaries, and attaches everything to that tag's release. Asset
names are `brain-axi_$(uname -s)_$(uname -m)` as printed on the target, so `install.sh` computes one
by interpolation and carries no platform table of its own; a test fails the build if the workflow
and the binary ever disagree about that set.

## Licence

MIT. The full text is in [LICENSE](LICENSE).
