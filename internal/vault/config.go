// Package vault opens a vault, reads and validates its Markdown records, and
// writes them back atomically.
//
// Markdown is the only source of truth. There is no index, no cache and no
// state outside the vault directory (NFR-6), so a hand edit in Obsidian can
// never leave this package holding a stale view. The cost is a full walk per
// query, which the query layer keeps inside NFR-1 by skipping on directory and
// filename before parsing.
package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Thanhbinh1905/secondbrain/internal/frontmatter"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
)

// Layout constants. These names are part of the vault format.
const (
	BrainDir   = ".brain"
	ConfigName = "config.yml"

	EventsDir = "events"
	IdeasDir  = "ideas"
	TasksDir  = "tasks"
	NotesDir  = "notes"
	PeopleDir = "people"
	DailyDir  = "daily"
)

// RecordDirs are the directories walked by a query, in a stable order. A vault
// created before a directory existed simply does not have it, which recordPaths
// tolerates, so adding one here never breaks an older vault.
var RecordDirs = []string{EventsDir, IdeasDir, TasksDir, NotesDir, PeopleDir, DailyDir}

// EnvVault names the environment variable that overrides vault resolution.
const EnvVault = "BRAIN_AXI_VAULT"

// DefaultNudgeAfter is the idea nudge horizon a vault starts with.
const DefaultNudgeAfter = "14d"

// DefaultDueWithin is how far ahead `due` looks for an event that is about to
// start. It is minutes rather than days because that command answers "what
// needs attention right now" and is meant to be run on a short interval.
const DefaultDueWithin = "30m"

// DefaultDormantAfter is when an untouched idea is called dormant, by `due` and
// by `recap`.
//
// It is deliberately longer than DefaultNudgeAfter and deliberately a separate
// knob: the nudge horizon means "poke me about this the next time I triage",
// and dormancy means "this has effectively stopped". Collapsing them would make
// `due` a second copy of `brief`.
const DefaultDormantAfter = "30d"

// DefaultBacklogCmd is empty: a new vault has no footer segment rather than a
// command that fails wherever a backlog tool is not reachable. The
// config file carries a working example to uncomment.
const DefaultBacklogCmd = ""

// Config is the whole of .brain/config.yml. It is small on purpose: every key
// here is a decision the tool must not make on the user's behalf.
type Config struct {
	Timezone   string
	WeekStarts string
	NudgeAfter string
	// FollowUpAfter is the task follow-up horizon a task that sets none falls
	// back to. Empty means fall back to NudgeAfter, which is what every vault
	// written before this key existed does.
	FollowUpAfter string
	// DueWithin and DormantAfter are the two windows `due` reads. The third
	// category, a delegated task past its follow-up, uses FollowUpAfter.
	DueWithin    string
	DormantAfter string
	// BoardHTML is where `board --html` writes when no path is given, and the
	// board `doctor` checks. It is one stable path on purpose: an external
	// viewer's URL has to survive a rebuild.
	BoardHTML string
	// BoardOpenCmd is the command `board --open` hands the built file to. Like
	// BacklogCmd it is empty by default and the tool holds no idea of what a
	// viewer is; the whole integration seam is a file on disk.
	BoardOpenCmd string
	// BacklogCmd is a shell command printing one integer: how many items are
	// open in the user's work backlog. The dashboard footer shows it, and
	// nothing else reads it. The brain never writes to a backlog, so this is
	// the whole of the coupling: one number, no schema, and easy to delete.
	BacklogCmd string
}

// DefaultConfig is every default a new vault starts with except its timezone.
//
// Timezone is deliberately left empty, because it is the one key with no safe
// default: it stamps an explicit UTC offset onto every stored record, so a
// wrong value corrupts every timestamp quietly. The caller must supply one -
// `init` takes it from --timezone or from timeref.SystemZone - and Init refuses
// an empty zone loudly rather than picking one.
func DefaultConfig() Config {
	return Config{
		WeekStarts:   "mon",
		NudgeAfter:   DefaultNudgeAfter,
		DueWithin:    DefaultDueWithin,
		DormantAfter: DefaultDormantAfter,
		BacklogCmd:   DefaultBacklogCmd,
	}
}

const configTemplate = `# brain-axi vault configuration.
# Hand-edit freely; the tool reads this file and holds no copy of it.
# The zone below is what every stored timestamp's UTC offset is written from.
# init took it from this machine unless --timezone named one. Changing it now
# changes how existing records are read, not what they say.
timezone: %s
# First day of the week, used by ` + "`week`" + ` and the dashboard.
week_starts: %s
# Default nudge horizon for an idea that does not set its own.
nudge_after: %s
# Default follow-up horizon for a task that does not set its own. Left out, a
# task falls back to nudge_after.
#   follow_up_after: 14d
# How far ahead the due command looks for an event that is about to start.
due_within: %s
# How long an idea may go untouched before due and recap call it dormant.
# Longer than nudge_after on purpose: nudge means "poke me at triage", dormant
# means "this has effectively stopped".
dormant_after: %s
# Optional. Where board --html writes when no path is given. One stable path,
# so an external viewer's URL survives a rebuild.
#   board_html: ~/secondbrain/board.html
# Optional. The command board --open hands the built file to. It is run as a
# command with the file path appended as its last argument, not as a shell line.
# The brain opens no socket and serves nothing; writing the file is the whole
# integration.
#   board_open_cmd: lavish-axi
# Optional. A shell command printing one integer: how many items are open in
# the work backlog. Only the dashboard footer reads it, and only to display it.
# The brain never writes to a backlog. Leave it empty to drop the footer segment.
#   backlog_cmd: tasks-axi list --state queued | sed -n 's/^count: //p'
backlog_cmd: %s
`

// Marshal renders the config file, comments included.
func (c Config) Marshal() []byte {
	backlog := c.BacklogCmd
	if backlog == "" {
		backlog = `""`
	} else {
		backlog = `"` + backlog + `"`
	}
	dueWithin := c.DueWithin
	if dueWithin == "" {
		dueWithin = DefaultDueWithin
	}
	dormantAfter := c.DormantAfter
	if dormantAfter == "" {
		dormantAfter = DefaultDormantAfter
	}
	return []byte(fmt.Sprintf(configTemplate, c.Timezone, c.WeekStarts, c.NudgeAfter, dueWithin, dormantAfter, backlog))
}

// parseConfig reads config.yml. Every failure names the file and the line.
func parseConfig(path string, raw []byte) (Config, error) {
	cfg := Config{}
	for i, line := range strings.Split(string(raw), "\n") {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return Config{}, &frontmatter.Error{Path: path, Line: lineNo, Msg: fmt.Sprintf("expected key: value, found %q", trimmed)}
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		value = strings.Trim(value, `"'`)
		switch key {
		case "timezone":
			cfg.Timezone = value
		case "week_starts":
			cfg.WeekStarts = value
		case "nudge_after":
			cfg.NudgeAfter = value
		case "follow_up_after":
			cfg.FollowUpAfter = value
		case "due_within":
			cfg.DueWithin = value
		case "dormant_after":
			cfg.DormantAfter = value
		case "board_html":
			cfg.BoardHTML = value
		case "board_open_cmd":
			cfg.BoardOpenCmd = value
		case "backlog_cmd":
			cfg.BacklogCmd = value
		default:
			return Config{}, &frontmatter.Error{Path: path, Line: lineNo, Msg: fmt.Sprintf("unknown configuration key %q: known keys are %s", key, strings.Join(ConfigKeys, ", "))}
		}
	}
	if cfg.Timezone == "" {
		return Config{}, &frontmatter.Error{Path: path, Line: 1, Msg: "missing required key \"timezone\""}
	}
	if cfg.WeekStarts == "" {
		return Config{}, &frontmatter.Error{Path: path, Line: 1, Msg: "missing required key \"week_starts\""}
	}
	if cfg.NudgeAfter == "" {
		cfg.NudgeAfter = DefaultNudgeAfter
	}
	if cfg.DueWithin == "" {
		cfg.DueWithin = DefaultDueWithin
	}
	if cfg.DormantAfter == "" {
		cfg.DormantAfter = DefaultDormantAfter
	}
	return cfg, nil
}

// ConfigKeys are every key config.yml accepts, in the order the file writes
// them. An unknown key is refused against this list rather than ignored.
var ConfigKeys = []string{
	"timezone", "week_starts", "nudge_after", "follow_up_after", "due_within",
	"dormant_after", "board_html", "board_open_cmd", "backlog_cmd",
}

// ExpandHome resolves a leading ~ in a hand-written path.
//
// config.yml is edited by hand and is not a shell, so somebody who writes
// ~/secondbrain/board.html means their home directory rather than a directory
// named "~" beside the working directory. An empty path stays empty; a home
// directory that cannot be resolved is an error rather than a silent literal.
func ExpandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~"+string(os.PathSeparator)) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q: this machine has no home directory (%v); write the absolute path", path, err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// ResolutionOrder describes, in order, where Open looks for a vault. It is
// reported verbatim when nothing is found, so the user can see what was
// tried rather than guessing.
func ResolutionOrder(explicit, workdir string) []string {
	var out []string
	if explicit != "" {
		out = append(out, fmt.Sprintf("--vault %s", explicit))
	}
	if env := os.Getenv(EnvVault); env != "" {
		out = append(out, fmt.Sprintf("$%s=%s", EnvVault, env))
	} else {
		out = append(out, fmt.Sprintf("$%s (unset)", EnvVault))
	}
	out = append(out, fmt.Sprintf("%s/%s and vault/%s/%s, walking up from %s", BrainDir, ConfigName, BrainDir, ConfigName, workdir))
	out = append(out, homeCandidates()...)
	return out
}

// homeCandidates are the default locations under the home directory, in the
// order ResolutionOrder reports them.
//
// The first is where `brain-axi init` run from the home directory puts a vault,
// which is what the README documents; the second is the original default. They
// are peers rather than a precedence: unlike the walk up from the working
// directory, neither is nearer to anything, so Open refuses when both exist
// instead of choosing one.
func homeCandidates() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, "vault"),
		filepath.Join(home, "secondbrain", "vault"),
	}
}

// NotFoundError reports that no vault was found, naming everything tried.
type NotFoundError struct {
	Tried []string
}

func (e *NotFoundError) Error() string {
	var sb strings.Builder
	sb.WriteString("no vault found; tried in order:")
	for _, t := range e.Tried {
		sb.WriteString("\n  - ")
		sb.WriteString(t)
	}
	sb.WriteString("\nrun `brain-axi init` to create one; a vault is never created implicitly")
	return sb.String()
}

// AmbiguousError reports that more than one of the default home locations
// holds a vault.
//
// Nothing orders those locations against each other, so choosing would mean
// reading and writing somebody's notes in whichever brain happened to sort
// first, and the mistake would only surface once the other one had gone quiet.
// Naming every candidate and both ways out costs one command; guessing is not
// recoverable from the user's side.
type AmbiguousError struct {
	Candidates []string
}

func (e *AmbiguousError) Error() string {
	var sb strings.Builder
	sb.WriteString("more than one vault in the default locations, and nothing orders them:")
	for _, c := range e.Candidates {
		sb.WriteString("\n  - ")
		sb.WriteString(c)
	}
	fmt.Fprintf(&sb, "\nname the one you mean with `--vault <path>` or $%s=<path>", EnvVault)
	return sb.String()
}

// Vault is an opened vault: its root, its config, and the calendar rules that
// follow from the config.
type Vault struct {
	Root   string
	Config Config
	Zone   timeref.Zone
	// NudgeAfter is the vault-wide default horizon, already parsed.
	NudgeAfter timeref.Span
	// FollowUpAfter is the vault-wide default task follow-up horizon. A vault
	// that does not set one gets NudgeAfter, so no existing vault's tasks move.
	FollowUpAfter timeref.Span
	// DueWithin and DormantAfter are `due`'s two configured windows.
	DueWithin    timeref.Span
	DormantAfter timeref.Span

	// walked memoises one walk for the life of this Vault value.
	//
	// This is not a cache in NFR-6's sense: it holds nothing on disk, lives
	// only as long as the process that made it, and is dropped by any write.
	// A single command asks the same question of the vault more than once - a
	// capture needs a free id, a free path and an overlap check - and paying
	// for three walks of the same unchanged files is waste, not safety.
	walked    []*Record
	walkedErr error
	haveWalk  bool
	walkMu    sync.Mutex
}

// ConfigPath is the absolute path of the vault's config file.
func (v *Vault) ConfigPath() string { return filepath.Join(v.Root, BrainDir, ConfigName) }

// isVaultRoot reports whether dir holds a vault config.
func isVaultRoot(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, BrainDir, ConfigName))
	return err == nil && st.Mode().IsRegular()
}

// Open finds and opens a vault. explicit comes from --vault and wins when set;
// otherwise the environment, then a walk up from workdir, then the default
// locations under the home directory, which are refused when more than one of
// them exists.
func Open(explicit, workdir string) (*Vault, error) {
	for _, candidate := range openCandidates(explicit, workdir) {
		if isVaultRoot(candidate) {
			return openAt(candidate)
		}
	}
	// The home fallbacks are reached only once every ordered step above has
	// missed, and they are peers, so more than one of them is an ambiguity
	// rather than a precedence question. This mirrors timeref.Zone.Normalise,
	// which refuses an ambiguous local time naming every reading it could
	// mean instead of taking one of them.
	var found []string
	for _, candidate := range homeCandidates() {
		if isVaultRoot(candidate) {
			found = append(found, candidate)
		}
	}
	switch len(found) {
	case 0:
		return nil, &NotFoundError{Tried: ResolutionOrder(explicit, workdir)}
	case 1:
		return openAt(found[0])
	default:
		return nil, &AmbiguousError{Candidates: found}
	}
}

// openCandidates are the ordered resolution steps: the ones where an earlier
// hit genuinely outranks a later one. The home fallbacks are not among them,
// because they have no such order.
func openCandidates(explicit, workdir string) []string {
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return
		}
		out = append(out, abs)
	}
	add(explicit)
	add(os.Getenv(EnvVault))
	dir := workdir
	if abs, err := filepath.Abs(workdir); err == nil {
		dir = abs
	}
	for {
		add(dir)
		add(filepath.Join(dir, "vault"))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return out
}

// OpenAt opens the vault rooted exactly at root, without any resolution.
func OpenAt(root string) (*Vault, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if !isVaultRoot(abs) {
		return nil, fmt.Errorf("%s is not a vault: %s is missing", abs, filepath.Join(BrainDir, ConfigName))
	}
	return openAt(abs)
}

func openAt(root string) (*Vault, error) {
	path := filepath.Join(root, BrainDir, ConfigName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading vault config: %w", err)
	}
	cfg, err := parseConfig(path, raw)
	if err != nil {
		return nil, err
	}
	zone, err := timeref.LoadZone(cfg.Timezone, cfg.WeekStarts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	nudge, err := timeref.ParseSpan(cfg.NudgeAfter)
	if err != nil {
		return nil, fmt.Errorf("%s: nudge_after: %w", path, err)
	}
	if !positiveSpan(nudge) {
		return nil, configValueError(path, raw, "nudge_after", "must be greater than zero")
	}
	followUp := nudge
	if cfg.FollowUpAfter != "" {
		followUp, err = timeref.ParseSpan(cfg.FollowUpAfter)
		if err != nil {
			return nil, fmt.Errorf("%s: follow_up_after: %w", path, err)
		}
		if !positiveSpan(followUp) {
			return nil, configValueError(path, raw, "follow_up_after", "must be greater than zero")
		}
	}
	dueWithin, err := timeref.ParseSpan(cfg.DueWithin)
	if err != nil {
		return nil, fmt.Errorf("%s: due_within: %w", path, err)
	}
	if !positiveSpan(dueWithin) {
		return nil, configValueError(path, raw, "due_within", "must be greater than zero")
	}
	dormant, err := timeref.ParseSpan(cfg.DormantAfter)
	if err != nil {
		return nil, fmt.Errorf("%s: dormant_after: %w", path, err)
	}
	if !positiveSpan(dormant) {
		return nil, configValueError(path, raw, "dormant_after", "must be greater than zero")
	}
	return &Vault{
		Root: root, Config: cfg, Zone: zone, NudgeAfter: nudge,
		FollowUpAfter: followUp, DueWithin: dueWithin, DormantAfter: dormant,
	}, nil
}

func positiveSpan(span timeref.Span) bool {
	return span.Days >= 0 && span.Clock >= 0 && (span.Days > 0 || span.Clock > 0)
}

func configValueError(path string, raw []byte, key, message string) error {
	line := 1
	for i, text := range strings.Split(string(raw), "\n") {
		parts := strings.SplitN(text, ":", 2)
		if strings.TrimSpace(parts[0]) == key {
			line = i + 1
			break
		}
	}
	return &frontmatter.Error{Path: path, Line: line, Msg: key + ": " + message}
}
