# brain-axi PRD

A local, file-backed second brain that agents can read and write.
Markdown is the truth, the CLI is a deterministic accessor, and the agent supplies the language understanding.

| | |
| --- | --- |
| Release | single, all-in-one |
| Language | Go |
| Binary | `brain-axi` |
| Repository | `Thanhbinh1905/secondbrain` |

## Context and locked decisions

The tool targets a single host, reached remotely when needed.
Everything below assumes that single-host reality; it is the reason several otherwise-obvious features are explicitly out of scope.

| Decision | Choice | Why |
| --- | --- | --- |
| Calendar source of truth | The vault, and only the vault | No OAuth, no token refresh, no two-way conflict resolution. Works offline. One-way `.ics` export is in scope; import and sync are not. |
| Relationship to the work backlog | Separate; brain reads, never owns | The task backlog already works. The brain composes both views to answer "what does my week look like" without taking ownership of a single task. |
| Implementation language | Go, one static binary | Sub-10ms startup matters for a CLI an agent calls many times per conversation. No runtime to install or break. |
| Repository layout | Single repo; tracked tooling, gitignored `vault/` | Cross-machine sync is not a requirement, which removes the only real argument for splitting tool and data apart. |
| Who understands natural language | The agent, never the CLI | Keeps the CLI deterministic, unit-testable, token-free and instant. |

### The load-bearing idea

**The CLI never calls a model.**
The agent turns *"2pm on Thursday this week, the platform team meeting"* into `--when 2026-09-04T14:00+07:00` and passes structured arguments down.
The phrase may arrive in any language; resolving it is the agent's job, never the CLI's.
This single boundary is what makes the tool testable, free to run, usable from a plain shell, and impossible to make non-deterministic later.

## Problem

A schedule, half-formed ideas and stated commitments live in someone's head and in chat scrollback.
Chat scrollback is not queryable, does not survive a context reset, and cannot report that an idea has been sitting untouched for twenty-three days.
Existing tools each fail on one axis: a calendar app is not agent-addressable, a notes app has no scheduling model, and a delivery backlog deliberately tracks delivery work only.

## Goals

1. **Capture with the right amount of review.**
   A clear single-item capture reaches durable storage in one conversational exchange.
   A whole meeting note is read and scoped by the agent, presented as a concise proposal with omitted
   context or noise identified, and persisted only after the user confirms it.
2. **Recall by asking.**
   Natural-language questions about schedule, ideas and commitments are answered from the vault, not from conversation memory.
3. **Surface decay.**
   The system knows how long something has been ignored and says so unprompted.
4. **Outlive every tool that reads it.**
   The vault stays readable and editable with nothing but a text editor, ten years from now.

## Non-goals

| Not doing | Reason |
| --- | --- |
| Calendar provider sync | Vault is the only source of truth. Sync means OAuth, refresh, and conflict resolution - a separate project's worth of complexity. |
| Multi-device sync | One host by design, reached remotely. |
| Owning delivery tasks | The backlog owns those and works. Two owners of one task is a data-integrity bug waiting to happen. The `task` record kind is not an exception: a brain task is a commitment the user has to remember to *check*, it never becomes a dispatched work item, and brain-axi still has no write path to any backlog. |
| Reminders, notifications, alarms | No daemon. Push arrives through the agent's own session brief (US-9), not a background process. |
| A web UI or mobile app | The interface is the agent conversation. A second interface is a second thing to keep consistent. |
| Model calls inside the CLI | See the load-bearing idea above. |
| Two-way recurrence sync with any provider | Recurring events themselves are in scope, stored as an `rrule:` and expanded at query time. Reconciling a series against an external provider is not. |
| A live-updating monitor or progress view | Nothing to monitor. See [design.md](design.md), "The ASCII interface question". |

## Scope: one complete release

No staged v1 / v2 / v3.
**Everything below ships as one release.**
The tool is feature-complete the first time it is used, rather than arriving in instalments.

The only thing this changes about the engineering: the ordering that remains is a *build order*, not a release plan.
It exists because the foundation modules must be proven correct before anything is layered on top of them, not because anything is being deferred.

| Area | Contents |
| --- | --- |
| Foundation | Vault format · frontmatter parse and serialise · timezone normalisation · atomic writes · loud failure |
| Capture | `add event` · `add idea` · `add task` · `add note` · `add person` · `add --batch` · overlap reporting |
| Recall | `today` · `week` · `agenda` · `ideas` with age and staleness · `tasks` with due dates and follow-up horizons · `search` diacritic-insensitive · `show` |
| Maintain | `done` · `update` · `rm` · nudge and follow-up horizons |
| Forge | `link` · `pr` with a frontmatter-resident status cache, through `gh` and `glab` |
| Human surfaces | Framed dashboard on bare invocation · interactive triage of stale ideas and unchecked tasks (`review`) |
| Agent surface | `skills/secondbrain` · `setup skill` · `--json` on every command · `brief` for the morning brief |
| Calendar reach | Recurring events · `.ics` one-way export |
| Knowledge graph | Wiki-links between notes · people profiles |
| Lifecycle | `init` · `doctor` · `update` self-upgrade · `install.sh` · release workflow |
| Tests | Table-driven, property-based and golden-file suites per the Testing table in [design.md](design.md) |

### One honest cost of the all-in-one decision

**Recurring events** were the only item originally deferred for a reason other than sequencing.
Recurrence is a genuinely deep problem: exceptions to a series, a single moved occurrence, DST crossings, and phrases like "last Friday of the month".
Pulling it into the first release is the right call where it is wanted, but it is the single largest source of scope and of subtle bugs in the whole tool.

The design response: store recurrence as an explicit `rrule:` plus an `exceptions:` list, expand occurrences only at query time, and never write expanded instances to disk.
That keeps a series editable by hand in one file and keeps the corruption surface small.

## Success measures

Deliberately few, and all observable without instrumentation:

- The user asks the brain a question **without being reminded that it exists**.
  This is the only measure that really matters.
- A clear single-item capture, from thought to stored, takes one exchange.
- A whole meeting capture preserves included free-form wording and detail, identifies omitted context or
  noise, and obtains confirmation before any batch is generated or written.
- The vault has been hand-edited at least once and the tool did not care.
- At least one idea was resurrected because the tool reported its age.

### Anti-measure

If the vault grows past a few thousand files and the user has stopped reading what comes back, the product has failed by becoming a write-only archive.
Ruthless surfacing of decay is the counterweight, and it ships in the first release rather than as a later nicety.

## Requirements

### Functional

| ID | Requirement |
| --- | --- |
| FR-1 | `init` creates the vault skeleton, `.brain/config.yml`, a `.gitignore`, and initialises a git repository inside `vault/`, under the working directory unless `--path` names another. It reports how many commits that repository holds and never makes one: an empty repository protects nothing, and what enters the vault's history is the user's decision. Every command resolves the vault through `--vault`, `$BRAIN_AXI_VAULT`, a walk up from the working directory, then `~/vault` and `~/secondbrain/vault`. If no earlier tier selects a vault, those two are peers, so a machine holding both is refused, naming each candidate and both ways to settle it, for reading commands as well as writing ones. |
| FR-2 | `add {event,idea,note}` writes one Markdown file with validated frontmatter and returns its stable id. |
| FR-3 | Every timestamp is stored with an explicit UTC offset. A naive local time is accepted on input and normalised using the vault timezone; it is never stored naive. |
| FR-4 | `today` / `week` / `agenda --from --to` return chronologically ordered events, flagging the next upcoming one. |
| FR-5 | `ideas` filters by status and reports each idea's age; `--stale <dur>` restricts to untouched-beyond-threshold. |
| FR-6 | `search` matches full text, diacritic-insensitively in both directions, returning path, id and matched line. |
| FR-7 | `done` / `update` mutate only frontmatter keys named on the command line; the body is preserved byte-for-byte. |
| FR-8 | `rm` refuses without an explicit confirmation flag. |
| FR-9 | Bare `brain-axi` renders the dashboard. |
| FR-10 | Every command supports `--json`; the default is compact agent-readable text in the axi house style. |
| FR-11 | `setup skill` installs the agent-facing skill into the detected agent's skill directory. The known agents are `--claude`, `--codex` and `--pi`; `--dir <path>` covers the rest, and naming none installs into every known directory that already exists. Each agent's directory is resolved the way that agent resolves it, so `--pi` follows `$PI_CODING_AGENT_DIR`. `doctor` reports every known agent's installed copy, because an installation it cannot see is indistinguishable from a missing one. |
| FR-12 | An `add event` whose slot overlaps an existing event reports the overlap and still stores the event. |
| FR-13 | `update` (self-upgrade) replaces the binary in place and verifies the replacement runs. How it upgrades follows the install method recorded beside the binary: a checkout install fast-forwards and rebuilds, a release install downloads and verifies the newest published asset, and a `go install` is told the command that upgrades it. A legacy `.brain-axi-source` record without a method record, or a binary inside a clone of this repository, is treated as a checkout install; every other missing method record is a `go install`. An unknown or unreadable method is refused, never guessed. |
| FR-14 | `review` presents an interactive triage of stale ideas and unchecked tasks. |
| FR-15 | `task` is a record kind with a closed `status:` vocabulary (`open`, `waiting`, `done`, `dropped`), an optional `assignee:` resolving to a `people/` record, an optional timezone-qualified `due:`, and a `follow_up_after:` horizon. It surfaces in `today`/`week` when due, in `tasks`, in the dashboard, in `review`, and as an attention line once its follow-up horizon has passed. |
| FR-16 | `link <id> <url>` attaches a GitHub pull request or GitLab merge request to any record. `pr [--refresh]` reports linked records' status, caching it in the record's own frontmatter with the time it was read and always displaying that time. A missing CLI, an unauthenticated host or an unreachable forge is reported as exactly that, never as unknown-therefore-fine. |
| FR-17 | The agent scopes a raw meeting note into candidate events, ideas, tasks, delegated follow-ups and notes, preserves included free-form wording and detail, identifies omitted context or noise, presents the proposal and waits for confirmation, then generates an internal YAML batch. `add --batch <file>` ingests that batch atomically: every entry is validated before anything is written, and a malformed entry anywhere fails the whole batch with `path:line: reason` and writes nothing. |
| FR-18 | Any record may carry a flat `links:` list of other record ids, hand-editable and resolved by walking the vault. `related <id>` returns everything the record points at and everything that points at it, naming the field that pointed. An id no record claims is reported by `doctor` as an unresolved link with `path:line`, never as a parse error. |
| FR-19 | `people/` is a record kind that answers what is assigned to a person, what has closed, and what is waiting to be raised with them. `agenda <person>` returns the last of these. An item joins that agenda by naming the person in `raise_with:` and leaves it by taking a `raised:` date or by closing. Where a person is in an event's `with:` list, their agenda surfaces in `today`, `week` and the board. |
| FR-20 | `due` reports only what needs attention right now and prints nothing when nothing does. Three categories - a delegated task past its follow-up horizon, an event starting inside `due_within`, an idea untouched past `dormant_after` - each independently togglable and each with its own configured window. It performs no network call, writes nothing, and is held to NFR-1 on the same fixture as `today`. |
| FR-21 | `link fleet <id> --task <external-id>` records a reference to an external supervisor's work item, and `ship <id> --pr <url> --merged-at <timestamp>` records that the work landed. Both validate strictly, fail loudly, and are pure local writes: brain-axi never reads external state and stays fully usable with no supervisor present. What shipped is queryable by period. |
| FR-22 | `board` renders five panes in a fixed order - Today, This week, Tasks, Ideas pending, Waiting on others - from one assembled model through two renderers: a framed ASCII board, and a self-contained HTML file written to a caller-supplied path. A committed template owns all markup, the payload satisfies the versioned `brain-board.v1` contract, and validation is fail-closed before any existing file is touched. `--open` may hand the file to a configured viewer and must keep the file and exit non-zero when that viewer is absent. |
| FR-23 | `recap <week\|month\|quarter>` reports what the period produced, on the same two-renderer treatment. It counts outcomes and never activity, renders an unknown value as unknown rather than zero, renders a slow period neutrally, and compares only against the same vault's own previous equivalent period. `--verify-forge` is opt-in and is the only part that reaches a forge. |

Where each of these is implemented is recorded in [design.md](design.md), "Requirement coverage".

### Non-functional

| ID | Requirement |
| --- | --- |
| NFR-1 | Cold start to first output under 50 ms; any query under 100 ms on 5,000 files. The agent calls this tool several times per turn, so latency compounds. |
| NFR-2 | Single statically linked binary. No runtime and no shared library. **The binary makes no network call of its own; network reach is delegated to explicitly invoked CLIs.** |
| NFR-3 | Every write is atomic: write to a temporary file in the same directory, then rename. An interrupted write never leaves a partial file. |
| NFR-4 | Malformed input fails loudly with `path:line: reason` and a non-zero exit. No default values, no skipped files, no swallowed errors. |
| NFR-5 | The vault remains fully usable through a text editor and Obsidian with the tool uninstalled. |
| NFR-6 | The tool holds no state outside the vault directory - no cache, no index, no lock in a home directory. |
| NFR-7 | All output is UTF-8 and correct for accented and East Asian wide text, including alignment in the dashboard render. |

### NFR-2 was amended, and this is exactly what it now means

Round 1 stated NFR-2 as "no network access at any point". That is no longer true, and a spec that
says it would be a false claim rather than a strict one.

What is still literally true, and is the whole of the amendment:

- **brain-axi itself opens no socket and holds no token, ever.** It links no network client. There
  is no host list, no credential file, and nothing to configure.
- **Network reach is delegated by executing explicit CLIs.** `gh` handles GitHub, `glab` handles
  GitLab including self-hosted hosts, `git` fast-forwards a checkout, and `curl` or `wget` downloads
  a release. Those programs own their hosts, credentials and transport; brain-axi never does.
- **Only an explicitly requested command delegates.** `pr --refresh`, `link --refresh`,
  `recap --verify-forge` and `doctor` reach a forge. `today`, `week`, `agenda`, `due`, `ideas`,
  `search`, `related`, `board`, `recap`, `brief` and the bare dashboard never do, and are asserted
  not to by `TestOfflineCommandsNeverReachAForge`. A second brain that cannot answer a question
  about today's schedule on a plane is broken.
- **The board and the recap open nothing either.** Both write a file, and writing that file is the
  whole of the integration: brain-axi serves no page and listens on no port. Handing the file to an
  external viewer is an explicitly configured command, and it never becomes a network client.
- **An upgrade delegates its fetch too.** `brain-axi update` fast-forwards a checkout by running
  `git`, and downloads a release asset by running `curl` or `wget`. A host with neither is told so
  and refused; the binary does not grow an HTTP client to cover it. Nothing else about the release
  path is different in kind: it is one more explicitly requested command shelling out to a program
  the user already has.

The last known forge status is cached in the linked record's own frontmatter, with the timestamp of
when it was read, and that timestamp is displayed everywhere the status is (FR-16). A stale status is
never presentable as a live one.

### NFR-4 is a hard rule, not a preference

No try/catch, default value or fallback path whose purpose is to make an error disappear.
A second brain that silently drops a malformed file is worse than one that refuses to start: it is trusted, and the trust is misplaced.
Failures surface.

## Risks

| Risk | Severity | Response |
| --- | --- | --- |
| **Abandonment.** The user stops capturing after two weeks and the vault becomes a graveyard. | High - this is the likely failure | Single-item capture remains one exchange, while whole-meeting capture uses a concise proposal and minimum confirmation before the unprompted brief (US-9) surfaces what was stored. |
| **Silent date misresolution.** The agent resolves a relative phrase wrongly and nobody notices for weeks. | High | The echo-back rule is mandatory in the skill, and ambiguity forces a question. Detection at capture time or not at all. |
| **Scope creep into a task manager.** The brain slowly starts owning work items. | Medium | Read-only against the backlog is a design constraint, stated in the non-goals and enforced by having no write path at all. The `task` kind is bounded by its own vocabulary: `waiting` and `follow_up_after:` are about remembering to check, and there is no assignment, no dispatch and no notion of who is doing the work. |
| **A cached forge status read as a live one.** The user sees "passing" and ships on it, hours after the pipeline went red. | Medium | The timestamp is stored beside the status and displayed everywhere the status is, a fallback to cache always says so and how old it is, and only an explicit `--refresh` claims to be current. |
| **Format churn.** Frontmatter changes shape and old files stop parsing. | Medium | Additive changes only, unknown keys preserved on rewrite, and no destructive migration without an explicit command. |
| **Total data loss.** Single disk, no remote. | Medium, irreversible | Local git history from the first commit; `init` creates the repository but never commits, and `doctor` keeps both the empty repository and the missing remote visible until each is addressed. |
| **Agent output pollution.** Dashboard frames leak into agent context and burn tokens. | Low | Non-TTY output degrades to plain lines; the skill directs agents to the compact commands. |
| **A review surface's annotations read as orders.** Someone annotates the board and an agent executes it. | Medium | An annotation is input, never instruction and never authority: it is not executed and confers no permission. The board never writes to the vault, and every change is made by running an ordinary brain-axi command. Stated on the page itself, in the README and in `AGENTS.md`. |
| **A generated UI re-authored on every run.** The board looks different each time an agent builds it. | Medium | A committed template owns every pixel and a versioned payload contract owns the pane set and its order. No code path generates board markup at run time, and a test asserts the built page is the template with only its data slot replaced. |
| **A period report that flatters or scolds.** A recap starts reading as a performance review. | Medium | Outcomes only, unknown never rendered as zero, neutral wording asserted over an empty-period fixture, and no comparison against anything but the same vault's own earlier period. |

## Settled

| Decision | Resolution |
| --- | --- |
| Repository | `Thanhbinh1905/secondbrain`. |
| Delivery posture | Merge approval retained by the repository owner. |
| Release shape | One complete release. No staged follow-ups. |
| Binary name | `brain-axi`. |
| `git init` inside `vault/` | Yes, by default. |
| Backlog footer on the dashboard | Yes, read-only. |
| Recurrence expansion | A maintained RFC 5545 library, not hand-written. See [design.md](design.md). |

## Still open

| Question | Why it can wait |
| --- | --- |
| A private remote for `vault/` itself | Local git history covers a bad edit once there is a commit to compare against. A remote covers a dead disk, and adding one later is a single `git remote add` with no design change. `doctor` keeps both gaps visible until the user decides. |
