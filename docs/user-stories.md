# User stories

One primary persona: **the user** - a technical person who already lives inside an agent session all day, and who wants to stop holding schedule, ideas and commitments in their head.

A secondary persona, **the agent**, is a first-class consumer: half of these stories are satisfied through a CLI contract, not a human interface.

Each story records how it was exercised end to end against a real vault.
Those runs are the acceptance evidence, not a claim that the code looks right.

## Capture

### US-1 · Capture an event

> "I have a meeting with the platform team at 2pm this Thursday, remember that for me."

**Acceptance criteria**

- The event is persisted as a Markdown file with a timezone-qualified `when:`.
- The agent **echoes back the resolved absolute date and weekday** before considering the capture done.
- If the phrasing is ambiguous (for example "this Thursday" spoken on a Thursday), the agent asks one clarifying question instead of guessing.
- A capture never silently overwrites an existing event at the same slot; an overlap is reported.

**How it is satisfied**

The agent resolves the phrase and calls:

```
brain-axi add event "Platform team sync" --when 2026-09-04T14:00 --duration 60m --with platform-team
```

The reply carries `when: 2026-09-04T14:00:00+07:00 (Friday)`, which is the resolved absolute date and its weekday, and a `help` line reminding the agent to echo it back.
The echo-back rule and the ambiguity rule are mandatory in `skills/secondbrain/SKILL.md`.
An overlapping slot appears as `attention[1]: overlaps <id> at <time>` and the event is still stored (FR-12).
A new capture never takes an existing file's path or id: both get the nearest free suffix instead.

Covered by `TestCaptureGoldens/add-event`, `.../add-event-overlap`, `TestInitThenCaptureThenRecall` in `cmd/brain-axi/cli_test.go`, and `TestFreePathAndFreeIDNeverOverwrite` in `internal/vault/vault_test.go`.

### US-2 · Capture an idea

> "I just thought of a customer referral programme, save that."

**Acceptance criteria**

- Stored with `status: pending` and a creation date.
- Free-form body preserved verbatim - the tool never summarises what the user said.
- Returns a stable slug id the user or agent can refer to later.

**How it is satisfied**

```
brain-axi add idea "customer referral program"
```

writes `ideas/customer-referral-program.md` with `status: pending`, `created:` and `touched:` set to today, and reports `id: customer-referral-program`.
`--body` text is written through unchanged; nothing in the tool summarises or rewrites it.

Covered by `TestCaptureGoldens/add-idea` and `TestBuildRecordsProduceValidFiles`.

### US-3 · Capture without ceremony

> "Quick note: ask the Zürich datacentre team about CI capacity."

**Acceptance criteria**

- A note with no date, no type and no structure is still accepted in one call.
- It lands in today's daily file so it is never orphaned.
- Capture friction is the whole product.
  If a thought needs three questions before it can be stored, the user stops using the tool.

**How it is satisfied**

```
brain-axi add note "ask the Zürich datacentre team about CI capacity"
```

appends a timestamped bullet to `daily/<today>.md`, creating that file on the day's first note.
No flag is required and no question is asked.
This single-item note capture is one file write, so it never touches two files at once.

Covered by `TestAppendNoteLandsInTodaysDailyFile` and `TestCaptureGoldens/add-note`.

### US-11 · Hand over a whole meeting note

> "These are the ideas and follow-ups from today's planning meeting. Save the relevant items for me."

**Acceptance criteria**

- A raw `.txt` file, `.md` file, or unstructured block of text is read in full by the agent.
- The agent scopes candidate events, ideas, tasks, delegated follow-ups and notes, preserves the
  original wording and detail of included content, and identifies context or noise to omit.
- Before anything is persisted, the agent presents a concise proposal, asks only the necessary
  clarification or confirmation questions, and waits for confirmation.
- Only after confirmation does the agent generate the internal YAML batch and call the CLI.
- The YAML batch is an agent-generated intermediate payload, not a user input requirement.
- The CLI still never calls a model or parses free-form meeting notes.
- Ingest is atomic. A malformed entry anywhere fails the whole batch with `path:line: reason` and
  writes nothing at all.
- The post-write echo-back remains required: after ingest, everything stored is reported back grouped
  by section. It does not replace the pre-write proposal and confirmation gate.

**How it is satisfied**

The agent reads the complete raw note and presents the proposed records and omitted context or noise.
It resolves only the ambiguities that need clarification and waits for confirmation.
After confirmation, it writes one internal YAML document with `ideas`, `tasks`, `delegated`, `notes`
and `events` sections (the format is in [design.md](design.md), "Batch ingest and the confirmation
gate"), then calls:

```
brain-axi add --batch meeting-2026-09-02.yml
```

Every entry is parsed, every vocabulary checked, every id and path resolved against the vault and
against the other entries, and every resulting document re-parsed as a record - all before a single
file is written.
The report is one block per section that the batch actually used, each row carrying the stored id, the
title, the resolved detail and the path, and a `help` line telling the agent to read them all back.

The agent-owned proposal and confirmation gate is not covered by automated tests.
The tests below cover CLI batch validation, writes and reporting after the YAML exists.
Covered by `TestBatchStoresEverySectionInOnePass`, `TestMalformedBatchWritesNothing` (thirteen ways
to be malformed, each asserting its own line number and that nothing was written),
`TestMalformedBatchWritesNothingEvenWithManyGoodEntriesFirst`,
`TestBatchWriteFailurePartwayThroughNamesEveryFile`, and `TestBatchIngestEchoesBackEverySection`.

## Recall

### US-4 · Today

> "Do I have anything on today?"

**Acceptance criteria**

- Returns today's events in chronological order with times in the vault's timezone.
- Marks which event is next relative to the current clock.
- An empty day answers "nothing scheduled" rather than printing an empty table.
- Answers in under 100 ms on a vault of 5,000 files.

**How it is satisfied**

`brain-axi today` prints an `events[n]{when,id,title,with,flag}` block in chronological order, with `next` in the flag column on the first event that has not finished yet, and repeats it as `attention[1]: next: ...`.
An empty day prints `events[0]: nothing scheduled today`.
Latency is asserted, not assumed: `TestQueryLatencyOnFiveThousandFiles` builds a 5,501-file vault and fails the build over 100 ms.
Measured there, each from a freshly opened vault: today 46 ms, week 51 ms, ideas 47 ms, tasks 47 ms, search 70 ms, and `today` together with the task block beside it 51 ms.

Covered by `TestTodayIsChronologicalAndFlagsNext`, `TestReadCommandGoldens/today`, `.../today-empty`, and `TestQueryLatencyOnFiveThousandFiles`.

### US-5 · Pending ideas

> "Do I have any ideas still pending?"

**Acceptance criteria**

- Lists ideas with `status: pending`, newest-touched last.
- **Every row carries its age.**
  Age is what turns a list of notes into a second brain - it is the signal the user cannot produce from memory.
- `--stale 14d` filters to ideas untouched beyond a threshold.

**How it is satisfied**

`brain-axi ideas --status pending` prints `ideas[n]{id,title,status,age,touched}` sorted most-neglected first, so the newest-touched row reads last.
Every row carries its age in days.
An idea past its nudge horizon - its own `nudge_after:` if it has one, otherwise the vault's default - is named under `attention`.
`--stale 14d` drops everything younger than the threshold.

Covered by `TestIdeasCarryAgeAndStaleness`, `TestIdeaHorizonUsesOwnValueThenVaultDefault`, and `TestReadCommandGoldens/ideas`, `.../ideas-stale`.

### US-6 · The week ahead

> "What do I have to get done this week?"

**Acceptance criteria**

- `brain-axi week` returns the vault's side: events and dated commitments.
- The *answer* the user hears also folds in live work from a task backlog, composed by the agent from two sources.
- The brain never claims ownership of a backlog task, and never writes to it.

**How it is satisfied**

`brain-axi week` resolves the week against the vault's configured first day, not the platform's, and expands recurring series into the window.
The agent composes the spoken answer from this and from the backlog; the skill says so explicitly.
The brain has no write path to any backlog at all.
The dashboard footer can show a read-only open-item count through the optional `backlog_cmd` config key, which the tool only ever reads.

Covered by `TestWeekUsesConfiguredFirstDay`, `TestPropertyWeekBoundaries`, and `TestReadCommandGoldens/week`.

### US-7 · Search

> "What did I write about the referral programme?"

**Acceptance criteria**

- Full-text across every vault file, results ranked with the matched line as context.
- Matches with and without diacritics in both directions - searching `zurich` finds *Zürich*, and so does `Zürich`.

**How it is satisfied**

`brain-axi search <text>` walks every file and returns `hits[n]{id,kind,where,line}`, where `where` is `path:line` for a body match so the user can open straight to it.
Folding is NFD normalisation with combining marks dropped and the stroked d mapped by hand, which is why the match works in both directions.
Ranking is: a diacritic-exact match before a folded one, then id before title before tags before body, then most recently touched.

Verified against a real vault: `search "krakow rollout"` and `search "Kraków rollout"` both return the daily file holding *review the Kraków rollout schedule next week*, and `search "pitch deck"` returns the event titled *review the São Paulo referral pitch deck*.

Covered by `TestSearchMatchesDiacriticsBothWays`, `TestSearchLineNumbersPointIntoTheFile`, `TestFoldMatchesDiacriticsBothDirections`, and `TestReadCommandGoldens/search-folded`, `.../search-diacritics`.

## Maintain

### US-8 · Close the loop

> "The platform team meeting is done. Drop the referral idea."

**Acceptance criteria**

- `done` and `update` mutate frontmatter in place, preserving the body byte-for-byte.
- No destructive default: `rm` requires an explicit confirmation flag.

**How it is satisfied**

`brain-axi done <id>` sets an event to `done` and an idea to `shipped`.
`brain-axi update <id> --status <status>` and `--set key=value` change only the keys named on the command line.
Both go through a `yaml.Node` rewrite, so unknown keys, key order and flow style all survive, and the body is the same bytes it was read as.
The result is re-parsed before it is written: a mutation is never the thing that makes a file unreadable.
`rm` refuses without `--yes` and names the record it would delete.

Covered by `TestBodyPreservedByteForByteAcrossMutation`, `TestMutationGoldens`, `TestFailureGoldens/rm-without-yes`, and `TestRunAppliesEachDecisionImmediately`.

### US-9 · Be reminded without asking

> The user opens a session and is told what they were about to forget.

**Acceptance criteria**

- The morning fleet brief gains a brain section: today's events, ideas past their nudge horizon, commitments coming due.
- This is the story that makes it a second brain rather than a notes CLI. Everything else is pull; this is the first push.

**How it is satisfied**

`brain-axi brief` is that section: `today`, `upcoming` for the next seven days (`--days` to change it), `due_tasks`, `unchecked_tasks`, and `stale_ideas` with each one's age and horizon, plus an `attention` line per stale idea and per unchecked task.
`--json` gives the same thing for an agent composing a longer brief.
The skill tells the agent to read it unprompted at the start of a session.

Covered by `TestBriefSurfacesStaleAndUpcoming`, `TestBriefCarriesDueAndUncheckedTasks`, and `TestReadCommandGoldens/brief`, `.../brief-json`.

### US-10 · Edit by hand

> The user opens the vault in Obsidian on a Sunday and rewrites half of it.

**Acceptance criteria**

- The tool holds no cache or index that a hand edit can invalidate.
- Malformed frontmatter after a hand edit produces `path:line: reason` and a non-zero exit, never a silently skipped file.

**How it is satisfied**

There is no index, no cache and no state outside the vault directory, so there is nothing a hand edit can invalidate.
Every query is a fresh walk of the Markdown.

A malformed file stops the query with `path:line: reason` and exit code 2, and no partial answer is printed.
`internal/vault/testdata/corrupt/` is a fixture vault where each file is corrupt in exactly one way - a naive timestamp, an unknown status, a bad rrule, a missing `touched:`, malformed YAML, an unknown type, no frontmatter at all, a duplicate key, an invalid id, and a `touched:` before its `created:` - and every one of the ten error strings is asserted verbatim.

Covered by `TestCorruptVaultFailsLoudly`, `TestCorruptVaultFailsLoudlyThroughTheCLI`, `TestParseErrorPositions` (golden files per malformed shape), and `TestDuplicateIDIsReportedNotGuessed`.

### US-12 · Remember to check on something

> "I asked the platform team to migrate the staging database three weeks ago. Did anyone follow up?"

**Acceptance criteria**

- A `task` records a commitment the user has to remember to check, with an optional `assignee:`,
  an optional timezone-qualified `due:`, and a `follow_up_after:` horizon.
- It is never a delivery work item. The separate work backlog keeps owning those, and brain-axi
  still has no write path to any backlog.
- It surfaces in `today`/`week` when due, in the dashboard, in `review`, and as an attention line
  once its follow-up horizon has passed.
- A delegated task nobody has checked in three weeks is impossible to miss.

**How it is satisfied**

```
brain-axi add task "migrate the staging database" --assignee platform-team --follow-up-after 14d
```

stores a task with `status: waiting`, because something handed to somebody else is not on the
user's own desk. Its age is measured from `touched:` through the same `vault.Horizon` an idea's
`nudge_after:` resolves through.

Twenty-eight days later it appears in `today`, `week`, `tasks`, `brief`'s `unchecked_tasks`, the
dashboard's *TASKS* section, `doctor`, and the `review` triage screen - and every one of
those carries the attention line *delegated to platform-team and not checked for 28d, past its 14d
follow-up horizon*. Overdue and past-horizon tasks are never clipped to a query's window, so the
week moving on does not make one disappear.

Covered by `TestTaskFollowUpHorizon`, `TestTasksAreOrderedByWhatNeedsAttentionFirst`,
`TestTaskFilters`, `TestClosedTasksStopDecaying`, `TestBriefCarriesDueAndUncheckedTasks`,
`TestTaskTriageWritesTaskKeys`, `TestTaskCardShowsWhoHasItAndWhenItWasDue`, and
`TestReadCommandGoldens/tasks`, `.../tasks-overdue`, `.../show-task`.

### US-13 · Mark a pull request against a record and check it later

> "Mark that PR against this task for me." Later: "Did it land?"

**Acceptance criteria**

- A forge URL attaches to any record. GitHub, GitLab.com and self-hosted GitLab all work, and the
  self-hosted host needs no configuration in brain-axi.
- brain-axi opens no socket and holds no token. Reach is delegated to the operator's own `gh` and
  `glab`, and only when explicitly asked.
- `today`, `week`, `agenda`, `ideas`, `search` and the bare dashboard never reach a forge.
- The last known status is cached in the record's own frontmatter with the time it was read, and
  that time is displayed wherever the status is.
- A missing CLI, an unauthenticated host or an unreachable forge is reported as exactly that.

**How it is satisfied**

```
brain-axi link migrate-staging-db https://git.example.com/platform/service/-/merge_requests/42
brain-axi pr --refresh
```

`link` is offline: it resolves the URL's shape and writes `forge_url:`. `pr` reads the cache and
says so; `pr --refresh` runs `gh pr view <url> --json ...` or
`glab mr view <n> -R https://host/project -F json`, maps the answer onto one closed vocabulary, and
writes `forge_state:`, `forge_checks:` and `forge_checked_at:` back into the record's own
frontmatter.

Verified against a real pull request, not only a fake: this repository's own merged PR 1 reports
`merged` / `passing` through the real `gh`, and a self-hosted GitLab host - reachable
only from inside its network - reports *glab failed: no answer within 30s (is the host reachable
from this machine?)* with a non-zero exit, never an empty status that reads as fine.

Covered by `TestDetectAcrossEveryHostShape`, `TestDetectRefusesRatherThanGuesses`,
`TestFetchGitHub`, `TestFetchGitLabIncludingSelfHosted`, `TestMissingCLINamesTheConcreteRequirement`,
`TestUnreachableHostIsReportedAsItself`, `TestOfflineCommandsNeverReachAForge`,
`TestStaleStatusIsNeverPresentableAsLive`, `TestRefreshWritesTheCacheIntoTheRecordItself`,
`TestForgeFailuresAreReportedAsThemselves`, `TestDoctorReportsForgeReach`, and the opt-in
`TestRealForgeStatus`.

### US-14 · Ask what a meeting produced, and what involves a person

> "Which meeting did that idea come out of?" Later: "What is connected to the platform team?"

**Acceptance criteria**

- A flat `links:` list of record ids sits in frontmatter, as plain text a human can hand-edit.
- `related <id>` returns everything the record points at and everything that points at it, naming
  the field that pointed.
- A link to an id no record claims is reported by `doctor` with `path:line`, never as a parse error.
- There is no graph store; resolution walks the vault like every other query.

**How it is satisfied**

An agent capturing a meeting's outcome writes `links: [platform-team-sync-20260904]` on the idea it
produced, through `add --links` or a batch entry's own `links:`. `related` then answers both
directions, and reports *via* `links` rather than `body`, so an idea a meeting produced is
distinguishable from one that merely mentions it in passing. A person's profile answers the second
question the same way, because `with:`, `assignee:` and `raise_with:` all resolve through the same
`query.PointsAt`.

Covered by `TestFrontmatterLinksResolveAndDangle`, `TestLinksAndBacklinks`,
`TestRelatedAnswersBothDirections`, `TestALinkToNothingIsReportedNotRejected`,
`TestDoctorNamesEveryUnresolvedLinkWithItsLine`, and `TestReadCommandGoldens/related-event`.

### US-15 · Know what to raise before walking into the room

> "I am about to meet the platform team. Is there anything I need to raise with them?"

**Acceptance criteria**

- A `people/` record answers what is assigned to them, what has closed, and what is waiting to be
  raised with them.
- `agenda <person>` returns the last of these, longest-waiting first.
- An item joins the agenda through `raise_with:` and leaves it by taking a `raised:` date or by
  closing.
- Where that person is in an event's `with:` list, their agenda surfaces in `today`, `week` and the
  board, beside the event.

**How it is satisfied**

Nothing about a person's workload is stored in their profile; all of it is derived from the records
that name them, for the same reason there is no index. `brain-axi agenda platform-team` is the
thirty-second read before the meeting, and `today` carries the same item in a `raise_with[...]`
block beside the event it belongs to, so the user does not have to remember to ask.

Covered by `TestAnAgendaSurfacesBesideTheMeetingItIsFor`, `TestAgendaTakesAPersonOrARange`,
`TestAnItemLeavesTheAgendaWhenItIsRaisedOrCloses`, `TestAPersonRecordCarriesWhatTheyAreHolding`,
and `TestReadCommandGoldens/agenda-person`.

### US-16 · Be interrupted only when something needs attention

> "Do not tell me anything unless something actually needs me."

**Acceptance criteria**

- One command answers one question, and prints nothing at all when nothing needs attention.
- Three categories, each independently togglable and each with its own configured window.
- The delegated category names the person and how long it has been.
- It is cheap enough to run on a short interval, performs no network call, and writes nothing.

**How it is satisfied**

`due` is the push half of the product made unattended. Every category is "this crossed a line",
never "this exists", which is what keeps it distinct from `brief`. Silence is the ordinary answer,
so anything it does print is worth interrupting for. On the 5,501-file fixture it answers in about
52 ms, inside the same NFR-1 budget `today` is held to.

Covered by `TestDueReportsOnlyWhatCrossedALine`, `TestEachDueCategoryIsIndependentlyTogglable`,
`TestNothingDueIsAnEmptyAnswerNotAnError`, `TestDueWindowsComeFromTheConfig`, `TestDueWritesNothing`,
`TestDueSaysNothingWhenNothingIsDue`, `TestDueNamesThePersonAndHowLong`,
`TestDueWritesNothingThroughTheCLI`, and `TestQueryLatencyOnFiveThousandFiles`.

### US-17 · Record that something shipped, and see the period it shipped in

> "This one is merged." Later: "What did we ship last quarter?"

**Acceptance criteria**

- Two narrow write commands record an external work item's id and a merge, both validating strictly
  and failing loudly.
- brain-axi never reads external state, and stays fully usable with no supervisor present.
- What shipped is queryable by period, listed by name.

**How it is satisfied**

`ship` writes `shipped_at:` and `shipped_pr:` and moves the status, exactly as `done` does. The
merge time must carry an explicit UTC offset, because it is the one date in the vault that says when
work actually landed and a naive value would place a merge in the wrong period. `recap` then lists
what shipped by name, and `ideas --json` carries each record's own `shipped_at`.

Covered by `TestTheFleetBridgeIsOneDirectionOnly`, `TestLinkFleetRecordsTheReferenceOnTheRecord`,
`TestShipRecordsWhenTheWorkLanded`, `TestShipRefusesEveryMalformedInput`,
`TestLinkFleetRefusesEveryMalformedInput`, and `TestWhatShippedIsQueryableInAPeriod`.

### US-18 · One board that looks the same every time

> "Give me one page I can see the whole week on, and do not change the layout every time."

**Acceptance criteria**

- Five panes, always in the same order, each with its own empty-state string.
- One data assembly path and two renderers that cannot disagree.
- A committed template owns the markup; nothing generates it at run time.
- A payload off contract is refused with `path:line: reason` before the existing file is touched.
- The output path is caller-supplied and rewritten in place; `--open` keeps the file when the viewer
  is absent.

**How it is satisfied**

The fixed template stops an agent from re-authoring the UI on every run.
The pane set and its order live in `internal/board`, every pixel lives in `templates/board.html`, and a build substitutes one payload into one slot.
`doctor` validates the page at `board_html:` so a hand-edited or older page is named with the line that is wrong.

Covered by `TestTheTemplateOwnsTheMarkup`, `TestBothRenderersReadTheSameModel`,
`TestEveryPaneAlwaysRendersWithItsOwnEmptyState`, `TestAHostileNoteCannotTerminateTheDataBlock`,
`TestValidationIsFailClosed`, `TestABadModelNeverReachesAFile`,
`TestTheBoardIsWrittenToAFileAndNothingElse`, `TestTheBoardPathIsStableAndRewrittenInPlace`,
`TestOpenWithoutAViewerKeepsTheFileAndSaysSo`, `TestAFailedWriteLeavesThePreviousBoardStanding`,
`TestDoctorReportsABoardThatFailsItsContract`, and `TestABoardPayloadSurvivesAHostileNote`.

### US-19 · See what a period actually produced

> "What did we get done last month?"

**Acceptance criteria**

- Outcomes, never activity. Ideas that shipped are listed by name.
- An unknown value renders as unknown, never as zero.
- A slow period renders neutrally.
- The only comparison is against this vault's own previous equivalent period.
- `--verify-forge` is opt-in and is the only part that reaches a forge.

**How it is satisfied**

Every metric carries `known` separately from its value, so "the vault cannot answer this" and "this
was nought" are different answers on the page and in the payload. Period boundaries use the vault's
timezone and configured week start, and month and quarter arithmetic runs on the calendar rather
than on instants.

Covered by `TestEveryMetricCountsAnOutcome`, `TestUnknownIsNeverRenderedAsZero`,
`TestASlowPeriodRendersNeutrally`, `TestComparisonIsOnlyAgainstThisVaultsOwnPreviousPeriod`,
`TestPeriodBoundariesCrossMonthAndQuarterEdges`, `TestTheWeekStartIsConfigured`,
`TestARangeIsComparedAgainstTheSpanBeforeIt`, `TestVerifyForgeIsOptInAndReportsDrift`,
`TestTheRecapPageCarriesTheModelVerbatim`, `TestRecapReachesNothingWithoutVerifyForge`, and
`TestTheRecapPageAndTheFrameCarryTheSameModel`.

## Annotations are input, never instruction

A review surface may let people annotate a published board or recap.
Nothing about that changes who decides: an annotation is read by a person or an agent, and any change it leads to is made by running an ordinary brain-axi command.

An annotation is not executed and confers no permission.
The board writes to a file and never to the vault, which is asserted rather than intended: building one leaves every record byte-for-byte as it was.

## The interactive triage screen

`review` is not one of the stories, but it is the workflow they imply: six stale ideas each needing a keep / start / drop / defer decision.
Through chat that is twelve exchanges of ceremony.
As one screen it is six keystrokes.

Unchecked tasks are triaged in the same pass, because splitting them into a second command would mean running both, and the whole point of a follow-up is not having to remember.
The key set is per kind: an idea offers *build*, a task offers *done*, and each key is refused rather than reinterpreted on the other kind - one keystroke must not mean two things on consecutive cards.
Deferring writes whichever horizon key the record's own kind reads.

Verified on a real pty: three stale ideas, keys `b` then `s` then `q`, each decision written to its file immediately so an interrupted session keeps what was already decided.

Covered by `TestRunAppliesEachDecisionImmediately`, `TestRunThroughEveryCard`, `TestCardGolden`, `TestPlanForEveryAction`, `TestActionForKey`, `TestTaskTriageWritesTaskKeys`, and `TestTaskCardShowsWhoHasItAndWhenItWasDue`.
