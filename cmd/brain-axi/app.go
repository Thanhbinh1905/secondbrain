package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/render"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
	"golang.org/x/term"
)

// EnvNow overrides the clock. It exists so every golden test and every
// end-to-end run is reproducible; it is not a user-facing feature.
const EnvNow = "BRAIN_AXI_NOW"

// app is one invocation: its parsed flags, its output destination, and the
// vault it will open when a command needs one.
type app struct {
	command string
	args    []string
	flags   map[string]string
	// present records which flags appeared, so an empty value is not the same
	// as an absent flag.
	present map[string]bool

	// rawArgs is the untouched command line, so a repeatable flag such as
	// --set can be re-read; the flag map keeps only the last value.
	rawArgs []string

	// env is where this invocation reads and writes. It is a value rather than
	// os.Stdin/os.Stdout so a test can drive the whole CLI in process and
	// still describe a terminal.
	env Env
	out *render.Out

	workdir string
	nowRaw  string

	vault *vault.Vault
	now   time.Time
}

// boolFlags take no value.
var boolFlags = map[string]bool{
	"json": true, "help": true, "version": true, "yes": true,
	"claude": true, "codex": true, "no-git": true, "check": true,
	"all": true,
	// due's three categories, each independently togglable. None given means
	// every category, so the bare command is the whole question.
	"delegated": true, "events": true, "ideas": true,
	// open hands a built board to the configured external viewer.
	"open": true,
	// refresh and verify-forge are the only flags in this tool that cause a
	// network request, and they exist so that reaching a forge is always
	// something the user asked for rather than something a query did on their
	// behalf.
	"refresh": true, "force": true, "overdue": true, "verify-forge": true,
}

// Env is the process context one invocation runs in.
type Env struct {
	// Stdin is a real file because the review screen puts a terminal into raw
	// mode, which needs a file descriptor.
	Stdin  *os.File
	Stdout io.Writer
	Stderr io.Writer
	// TTY reports whether Stdout is a terminal. Frames are drawn only when it
	// is, so box characters never reach a pipe.
	TTY bool
	// Width is the terminal width in cells, or zero when unknown.
	Width int
	// Workdir is where vault resolution starts walking up from.
	Workdir string
}

// OSEnv builds an Env from the process's own files.
func OSEnv() (Env, error) {
	wd, err := os.Getwd()
	if err != nil {
		return Env{}, fmt.Errorf("resolving the working directory: %w", err)
	}
	e := Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Workdir: wd}
	if f, ok := any(os.Stdout).(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		e.TTY = true
		if w, _, err := term.GetSize(int(f.Fd())); err == nil {
			e.Width = w
		}
	}
	return e, nil
}

// newApp parses the command line. Flags may be written --key value or
// --key=value, matching the other axi tools.
func newApp(args []string, env Env) (*app, error) {
	a := &app{
		rawArgs: args,
		env:     env,
		flags:   map[string]string{}, present: map[string]bool{},
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-v" || arg == "-V" || arg == "--version":
			a.present["version"] = true
		case arg == "-h" || arg == "--help":
			a.present["help"] = true
		case strings.HasPrefix(arg, "--"):
			name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
			if name == "" {
				return nil, usageError("empty flag name in %q", arg)
			}
			if boolFlags[name] {
				if hasValue {
					return nil, usageError("flag --%s takes no value", name)
				}
				a.present[name] = true
				a.flags[name] = "true"
				continue
			}
			if !hasValue {
				if i+1 >= len(args) {
					return nil, usageError("flag --%s needs a value", name)
				}
				i++
				value = args[i]
			}
			a.present[name] = true
			a.flags[name] = value
		case strings.HasPrefix(arg, "-") && arg != "-":
			return nil, usageError("unknown short flag %q: this tool uses long flags", arg)
		default:
			if a.command == "" {
				a.command = arg
				continue
			}
			a.args = append(a.args, arg)
		}
	}

	a.workdir = env.Workdir
	a.nowRaw = a.flagOr("now", os.Getenv(EnvNow))
	a.out = &render.Out{W: env.Stdout, TTY: env.TTY, Width: env.Width, JSON: a.present["json"]}
	return a, nil
}

// stdout is where command output goes.
func (a *app) stdout() io.Writer { return a.env.Stdout }

func (a *app) flagOr(name, fallback string) string {
	if v, ok := a.flags[name]; ok {
		return v
	}
	return fallback
}

func (a *app) has(name string) bool { return a.present[name] }

func (a *app) intFlag(name string, fallback int) (int, error) {
	raw, ok := a.flags[name]
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, usageError("flag --%s needs a whole number, got %q", name, raw)
	}
	return n, nil
}

// listFlag reads a comma-separated flag into trimmed, non-empty parts.
func (a *app) listFlag(name string) []string {
	raw, ok := a.flags[name]
	if !ok {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// dispatch routes to a command.
func (a *app) dispatch() error {
	if a.has("version") {
		fmt.Fprintf(a.stdout(), "brain-axi %s\n", version)
		return errQuiet
	}
	if a.has("help") {
		return a.helpFor(a.command)
	}
	switch a.command {
	case "":
		return a.cmdDashboard()
	case "today":
		return a.cmdToday()
	case "week":
		return a.cmdWeek()
	case "agenda":
		// No id means a date range; an id means that person's agenda. They
		// cannot be confused, because a date range takes no positional
		// argument and a person's agenda takes exactly one.
		if len(a.args) > 0 {
			return a.cmdPersonAgenda()
		}
		return a.cmdAgenda()
	case "add":
		return a.cmdAdd()
	case "tasks":
		return a.cmdTasks()
	case "due":
		return a.cmdDue()
	case "related":
		return a.cmdRelated()
	case "board":
		return a.cmdBoard()
	case "recap":
		return a.cmdRecap()
	case "ship":
		return a.cmdShip()
	case "link":
		return a.cmdLink()
	case "pr":
		return a.cmdPR()
	case "ideas":
		return a.cmdIdeas()
	case "search":
		return a.cmdSearch()
	case "show":
		return a.cmdShow()
	case "done":
		return a.cmdDone()
	case "update":
		// No id means the self-upgrade; an id means a record mutation. The CLI
		// reference spells both, and they cannot be confused because the
		// self-upgrade takes no positional argument.
		if len(a.args) == 0 {
			if a.present["status"] || a.present["set"] {
				return usageError("update needs a positional id when --status or --set is given")
			}
			return a.cmdSelfUpdate()
		}
		return a.cmdUpdate()
	case "rm":
		return a.cmdRemove()
	case "review":
		return a.cmdReview()
	case "export":
		return a.cmdExport()
	case "brief":
		return a.cmdBrief()
	case "init":
		return a.cmdInit()
	case "setup":
		return a.cmdSetup()
	case "doctor":
		return a.cmdDoctor()
	default:
		return unknownCommand(a.command)
	}
}

// openVault resolves and opens the vault, and fixes the clock. Every command
// that touches records calls it, and none of them creates a vault implicitly.
func (a *app) openVault() error {
	if a.vault != nil {
		return nil
	}
	v, err := vault.Open(a.flagOr("vault", ""), a.workdir)
	if err != nil {
		return err
	}
	a.vault = v
	now, err := a.resolveNow(v.Zone)
	if err != nil {
		return err
	}
	a.now = now
	return nil
}

// resolveNow honours --now or $BRAIN_AXI_NOW, so a run is reproducible.
func (a *app) resolveNow(zone timeref.Zone) (time.Time, error) {
	if a.nowRaw == "" {
		return time.Now().In(zone.Loc), nil
	}
	now, err := zone.Normalise(a.nowRaw)
	if err != nil {
		return time.Time{}, usageError("--now: %v", err)
	}
	return now, nil
}

func (a *app) engine() *query.Engine { return query.New(a.vault, a.now) }

// requireArgs reads exactly n positional arguments.
func (a *app) requireArgs(n int, what string) error {
	if len(a.args) < n {
		return usageError("%s: expected %d argument(s), got %d", what, n, len(a.args))
	}
	if len(a.args) > n {
		return usageError("%s: unexpected extra argument(s) %s", what, strings.Join(a.args[n:], " "))
	}
	return nil
}

// spanFlag reads a duration flag such as 60m or 14d.
func (a *app) spanFlag(name string) (timeref.Span, bool, error) {
	raw, ok := a.flags[name]
	if !ok {
		return timeref.Span{}, false, nil
	}
	span, err := timeref.ParseSpan(raw)
	if err != nil {
		return timeref.Span{}, false, usageError("flag --%s: %v", name, err)
	}
	return span, true, nil
}

// dateFlag reads a YYYY-MM-DD flag.
func (a *app) dateFlag(name string) (timeref.Date, bool, error) {
	raw, ok := a.flags[name]
	if !ok {
		return timeref.Date{}, false, nil
	}
	d, err := timeref.ParseDateOnly(raw)
	if err != nil {
		return timeref.Date{}, false, usageError("flag --%s: %v", name, err)
	}
	return d, true, nil
}
