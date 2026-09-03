// Command brain-axi is a deterministic accessor over a Markdown vault.
//
// It never calls a model, and it makes no network call of its own. The agent
// driving it resolves natural language - "next Thursday at 2pm" - into absolute
// structured arguments, and passes them down. That single boundary is what
// makes this tool testable, free to run, usable from a plain shell, and
// impossible to make non-deterministic later.
//
// Forge reach is delegated, never built in: `brain-axi pr --refresh` and
// `brain-axi doctor` run the operator's own gh and glab, which already hold the
// hosts and the credentials. Nothing else in the tool reaches anything.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/Thanhbinh1905/secondbrain/internal/frontmatter"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// version is the ldflags slot: install.sh and the release workflow stamp it
// with -ldflags "-X main.version=...". A `go install` applies no ldflags, so it
// is only the first of three possible answers.
var version = "dev"

// buildVersion is what every surface reports. It is resolved once, from the
// ldflags slot and then from the build information the toolchain embedded, so
// that a binary installed by `go install` reports the module version it was
// built from rather than claiming to be a development build.
var buildVersion = resolveVersion(version, debug.ReadBuildInfo)

// shortRevision is how many characters of a commit hash a version carries.
// install.sh shortens to the same width, so a checkout install and a build
// stamped only by the Go toolchain report the same shape.
const shortRevision = 12

// resolveVersion reports the first answer that is real, and "dev" only when
// there genuinely is none. Reporting "dev" for a binary that knows perfectly
// well which commit it came from is a lie the user cannot check.
//
// The order is: the stamped ldflags value; then the module version the
// toolchain recorded, which a module-proxy install has and which is either a
// released tag or a pseudo-version naming the commit; then the VCS revision it
// recorded, which a build from a checkout has, shortened and suffixed the way
// install.sh does it.
func resolveVersion(stamped string, buildInfo func() (*debug.BuildInfo, bool)) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}
	info, ok := buildInfo()
	if !ok || info == nil {
		return "dev"
	}
	// "(devel)" is what a build from a checkout records, and it says nothing.
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if revision == "" {
		return "dev"
	}
	if len(revision) > shortRevision {
		revision = revision[:shortRevision]
	}
	if modified == "true" {
		revision += "-dirty"
	}
	return revision
}

// Exit codes. They are part of the contract an agent scripts against.
const (
	exitOK = 0
	// exitUsage is a bad command line, a missing record, or a refused action.
	exitUsage = 1
	// exitData is a vault that does not make sense: malformed frontmatter, a
	// naive timestamp, a duplicate id, an unknown status.
	exitData = 2
)

func main() {
	env, err := OSEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitUsage)
	}
	os.Exit(Run(os.Args[1:], env))
}

// Run is the whole CLI: parse, dispatch, and turn any failure into an exit
// code. It takes its environment as a value so a test can drive it in process.
func Run(args []string, env Env) int {
	app, err := newApp(args, env)
	if err != nil {
		return report(env.Stderr, err)
	}
	if err := app.dispatch(); err != nil {
		return report(env.Stderr, err)
	}
	return exitOK
}

// report writes an error and picks its exit code. Nothing is ever swallowed.
func report(w io.Writer, err error) int {
	if errors.Is(err, errQuiet) {
		return exitOK
	}
	fmt.Fprintf(w, "error: %v\n", err)
	var perr *frontmatter.Error
	if errors.As(err, &perr) {
		return exitData
	}
	var dup *vault.DuplicateIDError
	if errors.As(err, &dup) {
		return exitData
	}
	var code *codedError
	if errors.As(err, &code) {
		return code.code
	}
	return exitUsage
}

// errQuiet ends a command successfully with nothing more to say. --help and
// --version use it.
var errQuiet = errors.New("done")

// codedError carries a specific exit code.
type codedError struct {
	code int
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func dataError(format string, a ...any) error {
	return &codedError{code: exitData, err: fmt.Errorf(format, a...)}
}

func usageError(format string, a ...any) error {
	return &codedError{code: exitUsage, err: fmt.Errorf(format, a...)}
}

const usage = `usage: brain-axi [command] [args] [flags]
commands[25]:
  (none)=dashboard, today, week, agenda, due, add, ideas, tasks, search, show,
  related, board, recap, done, update, link, ship, pr, rm, review, export,
  brief, init, setup, doctor
flags[6]:
  --json (machine-readable output), --vault <path> (after command),
  --now <timestamp> or $BRAIN_AXI_NOW (fixed clock, for tests),
  --limit <n> (search), --help, -v/-V/--version
examples:
  brain-axi
  brain-axi today
  brain-axi week --json
  brain-axi agenda --from 2026-09-01 --to 2026-09-07
  brain-axi agenda platform-team    # what is waiting to be raised with them
  brain-axi due
  brain-axi due --delegated --json
  brain-axi add event "Platform team sync" --when 2026-09-04T14:00 --duration 60m --with platform-team
  brain-axi add event "standup" --when 2026-09-02T09:00 --duration 30m --rrule FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR
  brain-axi add idea "customer referral program" --status pending
  brain-axi add task "review CI capacity" --due 2026-09-05T17:00 --follow-up-after 7d
  brain-axi add task "migrate the staging database" --assignee platform-team --follow-up-after 14d
  brain-axi add note "ask the infrastructure team about CI capacity"
  brain-axi add person "Platform team"
  brain-axi add --batch meeting-2026-09-04.yml
  brain-axi ideas --status pending --stale 14d
  brain-axi tasks --assignee platform-team
  brain-axi search "zurich"          # diacritic-insensitive: finds "Zürich"
  brain-axi show customer-referral
  brain-axi related platform-team-sync-20260904
  brain-axi board
  brain-axi board --html ~/secondbrain/board.html
  brain-axi recap month
  brain-axi recap --from 2026-07-01 --to 2026-09-30 --json
  brain-axi done platform-team-sync-20260904
  brain-axi update customer-referral --status building
  brain-axi link migrate-staging https://github.com/owner/repo/pull/12
  brain-axi link fleet migrate-staging --task PROJ-42
  brain-axi ship customer-referral --pr https://github.com/owner/repo/pull/12 --merged-at 2026-09-02T11:30:00+07:00
  brain-axi pr --refresh
  brain-axi rm customer-referral --yes
  brain-axi review
  brain-axi export ics --out brain.ics
  brain-axi brief
  brain-axi init --timezone Europe/Lisbon
  brain-axi setup skill --claude
  brain-axi doctor
"built-in":
  update: self-upgrade, with no id argument
  "update <id>": change a record's frontmatter
notes:
  the agent resolves relative dates; this tool accepts only absolute timestamps
  every timestamp is stored with an explicit UTC offset
  only "pr", "link --refresh", "recap --verify-forge" and "doctor" ever reach a
  forge; every other command reads files and works offline
  the board and the recap are written to a file; this tool opens no socket and
  serves nothing
help[2]:
  - "Run ` + "`" + `brain-axi <command> --help` + "`" + ` for one command's flags"
  - Run ` + "`" + `brain-axi doctor` + "`" + ` to check the vault
`

// helpFor prints one command's own usage.
var commandHelp = map[string]string{
	"today": "usage: brain-axi today [--json]\nReturns today's events in order, flagging the next one, plus any task due, overdue or unchecked.\n",
	"week":  "usage: brain-axi week [--json]\nReturns this week's events, against the vault's first day of the week, plus the tasks that fall in it.\n",
	"agenda": `usage: brain-axi agenda --from <date> --to <date> [--json]
       brain-axi agenda <person> [--json]
With a range, returns the events in it; dates are YYYY-MM-DD and both ends are included.
With a person id, returns what is waiting to be raised with them, longest-waiting first.
`,
	"due": `usage: brain-axi due [--delegated] [--events] [--ideas] [--json]
Reports only what needs attention right now, and prints nothing at all when nothing does.
Categories: a delegated task past its follow-up horizon, an event starting inside due_within,
an idea untouched past dormant_after. Naming none of them reports all three.
Reads files only, writes nothing, and is cheap enough to run on a short interval.
`,
	"related": `usage: brain-axi related <id> [--json]
Everything this record points at, and everything that points at it, with the field that did the
pointing. An id no record claims is reported unresolved rather than dropped.
`,
	"board": `usage: brain-axi board [--html <path>] [--open] [--json]
Five panes, always in this order: Today, This week, Tasks, Ideas pending, Waiting on others.
Framed on a terminal, plain down a pipe, and a self-contained HTML file with --html.
--open hands that file to the configured board_open_cmd, and keeps the file even when that fails.
The payload is validated against brain-board.v1 before any existing file is touched.
`,
	"recap": `usage: brain-axi recap <week|month|quarter> [--html <path>] [--verify-forge] [--json]
       brain-axi recap --from <date> --to <date> [flags]
What the period produced, counted from outcomes and compared only against this vault's own
previous equivalent period. A value the vault cannot supply reads unknown, never zero.
--verify-forge is the only part that reaches a forge, through the same gh and glab delegation.
`,
	"ship": `usage: brain-axi ship <id> --pr <url> --merged-at <timestamp> [--force] [--json]
Records that a record's work landed: shipped_at, shipped_pr, and the status move to shipped or done.
Only an idea, a task or a note can ship. The timestamp must carry an explicit UTC offset, because
every period report counts from it. --force replaces an existing ship record.
This is a local write; nothing is read back from any external system.
`,
	"add": `usage: brain-axi add <event|idea|task|note|person> <text> [flags]
       brain-axi add --batch <file>
event flags:
  --when <timestamp>   required; absolute, naive times are normalised to the vault zone
  --duration <span>    60m, 90m, 2h30m
  --with <ids>         comma-separated people ids
  --rrule <rule>       RFC 5545, for example FREQ=WEEKLY;BYDAY=FR
  --exceptions <dates> comma-separated YYYY-MM-DD dates skipped by the rule
  --status <status>    scheduled (default), done, cancelled
idea flags:
  --status <status>    pending (default), building, shipped, dropped
  --nudge-after <span> how long before this idea is nudged; default is the vault's
task flags:
  --due <timestamp>        absolute, like an event's --when
  --assignee <id>          a people/ record this was handed to
  --follow-up-after <span> how long before you are reminded to check on it
  --status <status>        open (default), waiting (default with --assignee), done, dropped
batch:
  --batch <file>       ingest a whole batch file; every record is stored or none is
shared flags:
  --id <id>            override the generated id
  --body <text>        body text; the tool never summarises it
  --tags <tags>        comma-separated tags (note only)
  --links <ids>        comma-separated record ids this one points at
  --raise-with <ids>   comma-separated people to raise this with; it lands on
                       their agenda until it is raised or the record closes
notes:
  a note lands in today's daily file so it is never orphaned
  a task is something to remember to check, never a delivery work item
`,
	"ideas":  "usage: brain-axi ideas [--status <status>] [--stale <span>] [--json]\nEvery row carries its age.\n",
	"tasks":  "usage: brain-axi tasks [--status <status>] [--assignee <id>] [--overdue] [--all] [--json]\nOutstanding commitments by default; --all includes done and dropped, --overdue keeps only what is past its follow-up horizon.\n",
	"search": "usage: brain-axi search <text> [--limit <n>] [--json]\nMatches with and without diacritics in both directions.\n",
	"show":   "usage: brain-axi show <id> [--json]\nPrints one record with its links and backlinks. A cached forge status is shown with the time it was read.\n",
	"done":   "usage: brain-axi done <id> [--json]\nSets status to done for an event or a task, shipped for an idea.\n",
	"update": `usage: brain-axi update <id> [--status <status>] [--set key=value ...] [--json]
       brain-axi update [--check] [--source <path>] [--json]
With an id, changes only the named frontmatter keys, plus touched: when the status moves.
With no id, upgrades the binary the way it was installed: a checkout install fast-forwards its
clone and rebuilds, a release install downloads and verifies the newest published binary, and a
` + "`go install`" + ` install is told the one command that upgrades it. Every path verifies that the
replacement runs before it replaces anything, and --check reports without upgrading.
`,
	"link": `usage: brain-axi link <id> <forge-url> [--refresh] [--force] [--json]
       brain-axi link fleet <id> --task <external-id> [--json]
Attaches a GitHub pull request or GitLab merge request to a record, self-hosted GitLab included.
Attaching is offline; --refresh also reads the status through gh or glab. --force replaces an existing link.
"link fleet" records a reference to an external supervisor's work item. It is written and never
read back: brain-axi holds no supervisor state and stays fully usable with no supervisor at all.
`,
	"pr":     "usage: brain-axi pr [id] [--refresh] [--json]\nReports linked records' forge status from the cache in their own frontmatter, always with the time it was read.\nOnly --refresh reaches a forge, through gh or glab; every other command in this tool stays offline.\n",
	"rm":     "usage: brain-axi rm <id> --yes\nRefuses without --yes.\n",
	"review": "usage: brain-axi review [--stale <span>] [--json]\nInteractive triage of stale ideas and unchecked tasks. Needs a terminal.\n",
	"export": "usage: brain-axi export ics [--out <path>] [--from <date>] [--to <date>]\nOne-way iCalendar export of events. Import and sync are out of scope.\n",
	"brief":  "usage: brain-axi brief [--days <n>] [--json]\nThe brain section for a session brief: today, what is due, what has gone stale, what nobody has checked.\n",
	"init":   "usage: brain-axi init [--path <dir>] [--timezone <zone>] [--week-starts <day>] [--nudge-after <span>] [--no-git]\nCreates the vault skeleton. Never overwrites an existing config.\n--timezone defaults to this machine's zone, and init refuses rather than guessing one it cannot determine; every stored timestamp carries that zone's offset.\n",
	"setup":  "usage: brain-axi setup skill [--claude] [--codex] [--pi] [--dir <path>]\nInstalls the agent-facing skill.\n",
	"doctor": "usage: brain-axi doctor [--json]\nReports the vault, the config, the parse state, tasks, forge reach, git, the skill and the binary.\n",
}

func (a *app) helpFor(command string) error {
	if text, ok := commandHelp[command]; ok {
		fmt.Fprint(a.stdout(), text)
		return errQuiet
	}
	fmt.Fprint(a.stdout(), usage)
	return errQuiet
}

// commandNames lists every dispatchable command, for the unknown-command error.
var commandNames = []string{
	"today", "week", "agenda", "due", "add", "ideas", "tasks", "search", "show",
	"related", "board", "recap", "done", "update", "link", "ship", "pr", "rm",
	"review", "export", "brief", "init", "setup", "doctor",
}

func unknownCommand(name string) error {
	return usageError("unknown command %q: known commands are %s", name, strings.Join(commandNames, ", "))
}
