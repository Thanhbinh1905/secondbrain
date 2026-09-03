package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Thanhbinh1905/secondbrain/internal/forge"
	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

var update = flag.Bool("update", false, "rewrite golden files")

// TestMain replaces the forge runner for the whole suite.
//
// No test may reach a network or need a token, and the guarantee has to be
// structural rather than a rule each test remembers: with this in place, a test
// that accidentally exercises a forge path gets a scripted answer instead of
// a real machine's credentials.
func TestMain(m *testing.M) {
	flag.Parse()
	runner = &scriptedForge{}
	os.Exit(m.Run())
}

// scriptedForge answers as gh and glab would, without either installed.
type scriptedForge struct {
	// missing names a CLI this machine is pretending not to have.
	missing map[string]bool
	// out maps a CLI name to the stdout it answers with.
	out map[string]string
	// fail maps a CLI name to the failure it answers with.
	fail map[string]error
	// calls records every invocation, so a test can prove an offline command
	// ran nothing at all.
	calls [][]string
}

func (f *scriptedForge) Look(name string) (string, error) {
	if f.missing[name] {
		return "", errors.New("executable file not found in $PATH")
	}
	return "/usr/bin/" + name, nil
}

func (f *scriptedForge) Run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if err, ok := f.fail[name]; ok {
		return nil, err
	}
	if out, ok := f.out[name]; ok {
		return []byte(out), nil
	}
	// auth status answers nothing and succeeds, which is what an authenticated
	// host looks like.
	return nil, nil
}

// useForge installs a scripted forge for one test and restores the suite's
// default afterwards.
func useForge(t *testing.T, f *scriptedForge) *scriptedForge {
	t.Helper()
	previous := runner
	runner = f
	t.Cleanup(func() { runner = previous })
	return f
}

const ghMergedPassing = `{"title":"feat: build brain-axi","state":"MERGED","isDraft":false,` +
	`"statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]}`

// fixtureVault copies the committed good vault into a temp directory, so a
// test that writes cannot disturb the fixture the other suites read.
func fixtureVault(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "..", "internal", "vault", "testdata", "good")
	dst := filepath.Join(t.TempDir(), "vault")
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	return dst
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// result is one CLI invocation's observable behaviour.
type result struct {
	Code   int
	Stdout string
	Stderr string
}

// invoke runs the CLI in process against a vault with a fixed clock.
func invoke(t *testing.T, vaultRoot, now string, tty bool, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	t.Setenv(vault.EnvVault, vaultRoot)
	t.Setenv(EnvNow, now)
	env := Env{
		Stdin: os.Stdin, Stdout: &out, Stderr: &errOut,
		TTY: tty, Workdir: t.TempDir(),
	}
	if tty {
		env.Width = 64
	}
	code := Run(args, env)
	return result{Code: code, Stdout: out.String(), Stderr: errOut.String()}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (run go test ./cmd/brain-axi -update): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("%s drifted\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// TestReadCommandGoldens locks both output formats for every read command.
// The text format is an agent contract and must not drift silently.
func TestReadCommandGoldens(t *testing.T) {
	root := fixtureVault(t)
	cases := []struct {
		name string
		now  string
		args []string
	}{
		{"today", "2026-09-02T12:00", []string{"today"}},
		{"today-json", "2026-09-02T12:00", []string{"today", "--json"}},
		{"today-empty", "2026-09-06T10:00", []string{"today"}},
		{"week", "2026-09-02T12:00", []string{"week"}},
		{"week-json", "2026-09-02T12:00", []string{"week", "--json"}},
		{"agenda", "2026-09-02T12:00", []string{"agenda", "--from", "2026-08-31", "--to", "2026-09-13"}},
		{"ideas", "2026-09-02T12:00", []string{"ideas", "--status", "pending"}},
		{"ideas-json", "2026-09-02T12:00", []string{"ideas", "--status", "pending", "--json"}},
		{"ideas-stale", "2026-09-02T12:00", []string{"ideas", "--status", "pending", "--stale", "14d"}},
		{"search-folded", "2026-09-02T12:00", []string{"search", "krakow rollout"}},
		{"search-diacritics", "2026-09-02T12:00", []string{"search", "Kraków rollout"}},
		{"search-json", "2026-09-02T12:00", []string{"search", "referral", "--json"}},
		{"search-empty", "2026-09-02T12:00", []string{"search", "nothing here matches"}},
		{"show-event", "2026-09-02T12:00", []string{"show", "platform-team-sync-20260904"}},
		{"show-series", "2026-09-02T12:00", []string{"show", "standup"}},
		{"show-idea", "2026-09-02T12:00", []string{"show", "customer-referral"}},
		{"show-person", "2026-09-02T12:00", []string{"show", "platform-team"}},
		{"show-json", "2026-09-02T12:00", []string{"show", "customer-referral", "--json"}},
		{"brief", "2026-09-02T08:00", []string{"brief"}},
		{"brief-json", "2026-09-02T08:00", []string{"brief", "--json"}},
		{"dashboard-plain", "2026-09-02T12:00", nil},
		{"review-json", "2026-09-02T12:00", []string{"review", "--json"}},
		{"tasks", "2026-09-02T12:00", []string{"tasks"}},
		{"tasks-json", "2026-09-02T12:00", []string{"tasks", "--json"}},
		{"tasks-overdue", "2026-09-24T12:00", []string{"tasks", "--overdue"}},
		{"tasks-assignee", "2026-09-02T12:00", []string{"tasks", "--assignee", "platform-team"}},
		{"show-task", "2026-09-02T12:00", []string{"show", "migrate-staging-db"}},
		{"show-task-json", "2026-09-02T12:00", []string{"show", "migrate-staging-db", "--json"}},
		{"pr-cached", "2026-09-02T12:00", []string{"pr"}},
		{"pr-cached-json", "2026-09-02T12:00", []string{"pr", "--json"}},
		{"due", "2026-09-02T12:00", []string{"due"}},
		{"due-json", "2026-09-02T12:00", []string{"due", "--json"}},
		{"related-event", "2026-09-02T12:00", []string{"related", "platform-team-sync-20260904"}},
		{"related-json", "2026-09-02T12:00", []string{"related", "customer-referral", "--json"}},
		{"agenda-person", "2026-09-02T12:00", []string{"agenda", "platform-team"}},
		{"agenda-person-json", "2026-09-02T12:00", []string{"agenda", "platform-team", "--json"}},
		{"board-plain", "2026-09-02T12:00", []string{"board"}},
		{"recap-month", "2026-09-02T12:00", []string{"recap", "month"}},
		{"recap-week-json", "2026-09-02T12:00", []string{"recap", "week", "--json"}},
		{"usage", "2026-09-02T12:00", []string{"--help"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := invoke(t, root, tc.now, false, tc.args...)
			if got.Code != exitOK {
				t.Fatalf("exit %d, stderr: %s", got.Code, got.Stderr)
			}
			if got.Stderr != "" {
				t.Errorf("a successful read wrote to stderr: %s", got.Stderr)
			}
			golden(t, tc.name+".golden", got.Stdout)
			// No read command may leak a box-drawing character to a pipe.
			for _, r := range "╭╮╰╯│─├┤" {
				if strings.ContainsRune(got.Stdout, r) {
					t.Errorf("frame character %q reached a pipe", r)
				}
			}
			if strings.Contains(strings.Join(tc.args, " "), "--json") {
				var any any
				if err := json.Unmarshal([]byte(got.Stdout), &any); err != nil {
					t.Errorf("--json output is not valid JSON: %v", err)
				}
			}
		})
	}
}

// TestTTYDashboardGolden is the human surface: framed, aligned, and every line
// exactly the frame width even with mixed-width text in it.
func TestTTYDashboardGolden(t *testing.T) {
	root := fixtureVault(t)
	got := invoke(t, root, "2026-09-02T12:00", true)
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	golden(t, "dashboard-tty.golden", got.Stdout)
	for i, line := range strings.Split(strings.TrimRight(got.Stdout, "\n"), "\n") {
		if w := unitext.Width(line); w != 64 {
			t.Errorf("line %d is %d cells wide, want 64:\n%q", i, w, line)
		}
	}
}

// TestCaptureGoldens covers every add form, including the overlap report.
func TestCaptureGoldens(t *testing.T) {
	cases := []struct {
		name string
		now  string
		args []string
	}{
		{"add-event", "2026-09-01T09:00", []string{"add", "event", "Platform team sync in Zürich", "--when", "2026-09-11T14:00", "--duration", "60m", "--with", "platform-team", "--body", "Decide how expired schedules are handled."}},
		{"add-event-json", "2026-09-01T09:00", []string{"add", "event", "Platform team sync in Zürich", "--when", "2026-09-11T14:00", "--json"}},
		{"add-event-recurring", "2026-09-01T09:00", []string{"add", "event", "1-1 with the team", "--when", "2026-09-11T10:00", "--duration", "45m", "--rrule", "FREQ=WEEKLY;BYDAY=FR", "--exceptions", "2026-09-18,2026-09-25"}},
		{"add-event-overlap", "2026-09-01T09:00", []string{"add", "event", "overlapping slot", "--when", "2026-09-04T14:30", "--duration", "30m"}},
		{"add-idea", "2026-09-01T09:00", []string{"add", "idea", "merge the per-team vaults", "--nudge-after", "30d"}},
		{"add-note", "2026-09-01T09:12", []string{"add", "note", "ask the Zürich datacentre team about CI capacity"}},
		{"add-note-json", "2026-09-01T09:12", []string{"add", "note", "one more quick note", "--json"}},
		{"add-person", "2026-09-01T09:00", []string{"add", "person", "Datacentre team"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureVault(t)
			got := invoke(t, root, tc.now, false, tc.args...)
			if got.Code != exitOK {
				t.Fatalf("exit %d: %s", got.Code, got.Stderr)
			}
			golden(t, tc.name+".golden", got.Stdout)
			// Whatever was written must still walk cleanly.
			v, err := vault.OpenAt(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := v.Walk(); err != nil {
				t.Errorf("the capture left the vault unparseable: %v", err)
			}
		})
	}
}

func TestMutationGoldens(t *testing.T) {
	cases := []struct {
		name string
		args [][]string
	}{
		{"done-event", [][]string{{"done", "platform-team-sync-20260904"}}},
		{"done-idea", [][]string{{"done", "customer-referral"}}},
		{"update-status", [][]string{{"update", "customer-referral", "--status", "building"}}},
		{"update-set", [][]string{{"update", "customer-referral", "--set", "nudge_after=30d", "--set", "owner=platform-team"}}},
		{"update-set-explicit-touched", [][]string{{"update", "customer-referral", "--set", "touched=2026-09-02"}}},
		{"rm-confirmed", [][]string{{"rm", "calendar-export", "--yes"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureVault(t)
			var out strings.Builder
			for _, args := range tc.args {
				got := invoke(t, root, "2026-09-02T12:00", false, args...)
				if got.Code != exitOK {
					t.Fatalf("%v: exit %d: %s", args, got.Code, got.Stderr)
				}
				out.WriteString(got.Stdout)
			}
			golden(t, tc.name+".golden", out.String())
			v, err := vault.OpenAt(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := v.Walk(); err != nil {
				t.Errorf("the mutation left the vault unparseable: %v", err)
			}
		})
	}
}

// TestFailureGoldens locks the error text and the exit code for every way the
// tool is meant to refuse. Nothing here is allowed to exit zero.
func TestFailureGoldens(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"unknown-command", []string{"telepathy"}, exitUsage},
		{"unknown-add-kind", []string{"add", "reminder", "x"}, exitUsage},
		{"add-event-without-when", []string{"add", "event", "no time given"}, exitUsage},
		{"add-event-relative-date", []string{"add", "event", "x", "--when", "thursday this week"}, exitUsage},
		{"add-event-bad-rrule", []string{"add", "event", "x", "--when", "2026-09-11T10:00", "--rrule", "FREQ=NEVER"}, exitUsage},
		{"add-event-exceptions-without-rrule", []string{"add", "event", "x", "--when", "2026-09-11T10:00", "--exceptions", "2026-09-18"}, exitUsage},
		{"add-idea-unknown-status", []string{"add", "idea", "x", "--status", "maybe"}, exitUsage},
		{"add-duplicate-id", []string{"add", "idea", "x", "--id", "customer-referral"}, exitUsage},
		{"add-note-with-id", []string{"add", "note", "x", "--id", "nope"}, exitUsage},
		{"show-missing", []string{"show", "no-such-thing"}, exitUsage},
		{"ideas-unknown-status", []string{"ideas", "--status", "maybe"}, exitUsage},
		{"update-unknown-status", []string{"update", "customer-referral", "--status", "maybe"}, exitUsage},
		{"update-nothing-to-change", []string{"update", "customer-referral"}, exitUsage},
		{"update-cannot-change-id", []string{"update", "customer-referral", "--set", "id=other"}, exitUsage},
		{"rm-without-yes", []string{"rm", "customer-referral"}, exitUsage},
		{"agenda-without-range", []string{"agenda"}, exitUsage},
		{"agenda-bad-date", []string{"agenda", "--from", "01/09/2026", "--to", "2026-09-07"}, exitUsage},
		{"export-unknown-format", []string{"export", "csv"}, exitUsage},
		{"setup-unknown-target", []string{"setup", "hooks"}, exitUsage},
		{"extra-argument", []string{"today", "tomorrow"}, exitUsage},
		{"unknown-short-flag", []string{"-x"}, exitUsage},
		{"flag-without-value", []string{"search"}, exitUsage},
		{"add-task-unknown-status", []string{"add", "task", "x", "--status", "maybe"}, exitUsage},
		{"add-task-bad-assignee", []string{"add", "task", "x", "--assignee", "Platform Team"}, exitUsage},
		{"add-batch-missing-file", []string{"add", "--batch", "no-such-file.yml"}, exitUsage},
		{"add-batch-with-kind", []string{"add", "idea", "x", "--batch", "b.yml"}, exitUsage},
		{"link-not-a-forge-url", []string{"link", "customer-referral", "https://example.com/whatever"}, exitUsage},
		{"link-already-linked", []string{"link", "migrate-staging-db", "https://github.com/owner/repo/pull/9"}, exitUsage},
		{"pr-unlinked-record", []string{"pr", "customer-referral"}, exitUsage},
		{"related-missing", []string{"related", "no-such-thing"}, exitUsage},
		{"agenda-unknown-person", []string{"agenda", "nobody-at-all"}, exitUsage},
		{"recap-without-a-period", []string{"recap"}, exitUsage},
		{"recap-unknown-period", []string{"recap", "fortnight"}, exitUsage},
		{"recap-period-and-range", []string{"recap", "month", "--from", "2026-09-01", "--to", "2026-09-30"}, exitUsage},
		{"link-fleet-without-task", []string{"link", "fleet", "customer-referral"}, exitUsage},
		{"link-fleet-bad-task", []string{"link", "fleet", "customer-referral", "--task", "rm -rf /"}, exitUsage},
		{"link-fleet-missing-record", []string{"link", "fleet", "no-such-thing", "--task", "PROJ-42"}, exitUsage},
		{"ship-without-pr", []string{"ship", "customer-referral", "--merged-at", "2026-09-01T10:00:00+07:00"}, exitUsage},
		{"ship-naive-timestamp", []string{"ship", "customer-referral", "--pr", "https://github.com/owner/repo/pull/3",
			"--merged-at", "2026-09-01T10:00"}, exitUsage},
		{"ship-bad-url", []string{"ship", "customer-referral", "--pr", "https://example.com/whatever",
			"--merged-at", "2026-09-01T10:00:00+07:00"}, exitUsage},
		{"ship-wrong-kind", []string{"ship", "standup", "--pr", "https://github.com/owner/repo/pull/3",
			"--merged-at", "2026-09-01T10:00:00+07:00"}, exitUsage},
		{"ship-missing-record", []string{"ship", "no-such-thing", "--pr", "https://github.com/owner/repo/pull/3",
			"--merged-at", "2026-09-01T10:00:00+07:00"}, exitUsage},
		{"add-note-with-links", []string{"add", "note", "x", "--links", "customer-referral"}, exitUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureVault(t)
			got := invoke(t, root, "2026-09-02T12:00", false, tc.args...)
			if got.Code != tc.code {
				t.Errorf("exit %d, want %d; stderr: %s", got.Code, tc.code, got.Stderr)
			}
			if got.Stderr == "" {
				t.Error("a refusal wrote nothing to stderr")
			}
			golden(t, "err-"+tc.name+".golden", got.Stderr)
		})
	}
}

// TestCorruptVaultFailsLoudlyThroughTheCLI is NFR-4 end to end: the file is
// named with its line, the exit code says the data is wrong, and no partial
// answer is printed to stdout.
func TestCorruptVaultFailsLoudlyThroughTheCLI(t *testing.T) {
	root := filepath.Join("..", "..", "internal", "vault", "testdata", "corrupt")
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"today"}, {"week"}, {"ideas"}, {"search", "anything"}, {"brief"}, nil} {
		name := strings.Join(args, " ")
		if name == "" {
			name = "dashboard"
		}
		t.Run(name, func(t *testing.T) {
			got := invoke(t, abs, "2026-09-02T12:00", false, args...)
			if got.Code != exitData {
				t.Errorf("exit %d, want %d (a corrupt vault is a data error)", got.Code, exitData)
			}
			if got.Stdout != "" {
				t.Errorf("a partial answer was printed alongside the failure:\n%s", got.Stdout)
			}
			if !strings.Contains(got.Stderr, ".md:") {
				t.Errorf("the error does not name a file and a line: %s", got.Stderr)
			}
		})
	}
	// doctor reports the corruption rather than crashing on it, and still says
	// the vault does not parse.
	got := invoke(t, abs, "2026-09-02T12:00", false, "doctor")
	if got.Code != exitData {
		t.Errorf("doctor exit %d, want %d", got.Code, exitData)
	}
	if !strings.Contains(got.Stdout, "parse failed") {
		t.Errorf("doctor did not report the parse failure:\n%s", got.Stdout)
	}
}

func TestDoctorReportsAmbiguousVaultAsAChoice(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(vault.EnvVault, "")
	t.Setenv(EnvNow, "2026-09-01T09:00")
	cfg := vault.DefaultConfig()
	cfg.Timezone = "Asia/Bangkok"
	first := filepath.Join(home, "vault")
	second := filepath.Join(home, "secondbrain", "vault")
	for _, root := range []string{first, second} {
		if _, err := vault.Init(root, cfg, false); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"doctor", "--json"}, Env{
		Stdin: os.Stdin, Stdout: &out, Stderr: &errOut, Workdir: outside,
	})
	if code != exitOK {
		t.Fatalf("doctor --json exit %d: %s", code, errOut.String())
	}
	var rep doctorReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("doctor --json emitted invalid JSON: %v\n%s", err, out.String())
	}
	if len(rep.Rows) == 0 || rep.Rows[0].Name != "vault" || rep.Rows[0].Detail != "choice required" {
		t.Fatalf("vault row = %+v", rep.Rows)
	}
	help := strings.Join(rep.Help, "\n")
	if strings.Contains(help, "brain-axi init") {
		t.Errorf("ambiguous vault recommends creating another one: %s", help)
	}
	for _, want := range []string{"--vault <path>", "$" + vault.EnvVault + "=<path>"} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not mention %q: %s", want, help)
		}
	}
	attention := strings.Join(rep.Attention, "\n")
	for _, want := range []string{first, second} {
		if !strings.Contains(attention, want) {
			t.Errorf("attention does not name %q: %s", want, attention)
		}
	}
}

// TestCorruptionAnywhereFailsEveryReadCommand: a broken idea must stop `today`
// just as surely as it stops `ideas`. No command holds a partial view of the
// vault, which is why no query scopes its walk.
func TestCorruptionAnywhereFailsEveryReadCommand(t *testing.T) {
	corruptions := map[string]string{
		"ideas/broken.md":  "---\ntype: idea\nid: broken\nstatus: pending\ncreated: 2026-09-01\n  touched: 2026-09-01\n---\n\nbody\n",
		"events/broken.md": "---\ntype: event\nid: broken\nwhen: 2026-09-04T14:00:00\nstatus: scheduled\ncreated: 2026-09-01\n---\n\nbody\n",
		"notes/broken.md":  "not frontmatter at all\n",
		"people/broken.md": "---\ntype: person\nid: Broken Id\ncreated: 2026-09-01\n---\n\nbody\n",
		"daily/broken.md":  "---\ntype: daily\nid: broken\ncreated: not-a-date\n---\n\nbody\n",
	}
	commands := [][]string{{"today"}, {"week"}, {"agenda", "--from", "2026-09-01", "--to", "2026-09-07"},
		{"ideas"}, {"search", "referral"}, {"brief"}, {"export", "ics"}, nil,
		{"due"}, {"board"}, {"recap", "month"}, {"agenda", "platform-team"}}
	for rel, body := range corruptions {
		for _, args := range commands {
			name := rel + " / " + strings.Join(args, " ")
			t.Run(name, func(t *testing.T) {
				root := fixtureVault(t)
				if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				got := invoke(t, root, "2026-09-02T12:00", false, args...)
				if got.Code != exitData {
					t.Errorf("exit %d, want %d; stdout: %s", got.Code, exitData, got.Stdout)
				}
				if got.Stdout != "" {
					t.Errorf("a partial answer was printed:\n%s", got.Stdout)
				}
				if !strings.Contains(got.Stderr, rel+":") {
					t.Errorf("the error does not name %s: %s", rel, got.Stderr)
				}
			})
		}
	}
}

// TestTouchedOnlyMovesWhenTheRecordDoes: a status change is the record moving,
// which is what touched: records. An arbitrary --set is not, because silently
// resetting the decay clock is how the vault becomes a write-only archive.
func TestTouchedOnlyMovesWhenTheRecordDoes(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantTouched string
	}{
		{"status-moves-it", []string{"update", "customer-referral", "--status", "building"}, "2026-09-02"},
		{"done-moves-it", []string{"done", "customer-referral"}, "2026-09-02"},
		{"set-does-not", []string{"update", "customer-referral", "--set", "owner=platform-team"}, "2026-08-09"},
		{"set-horizon-does-not", []string{"update", "customer-referral", "--set", "nudge_after=30d"}, "2026-08-09"},
		{"explicit-set-wins", []string{"update", "customer-referral", "--set", "touched=2026-08-20"}, "2026-08-20"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureVault(t)
			got := invoke(t, root, "2026-09-02T12:00", false, tc.args...)
			if got.Code != exitOK {
				t.Fatalf("exit %d: %s", got.Code, got.Stderr)
			}
			v, err := vault.OpenAt(root)
			if err != nil {
				t.Fatal(err)
			}
			r, err := v.Find("customer-referral")
			if err != nil {
				t.Fatal(err)
			}
			if r.Touched.String() != tc.wantTouched {
				t.Errorf("touched = %s, want %s\n%s", r.Touched, tc.wantTouched, got.Stdout)
			}
		})
	}
}

// TestEveryCommandSupportsJSON is FR-10. A command that only mostly emits JSON
// is a command an agent cannot compose with.
func TestEveryCommandSupportsJSON(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"dashboard", []string{"--json"}},
		{"today", []string{"today", "--json"}},
		{"week", []string{"week", "--json"}},
		{"agenda", []string{"agenda", "--from", "2026-09-01", "--to", "2026-09-07", "--json"}},
		{"ideas", []string{"ideas", "--json"}},
		{"search", []string{"search", "referral", "--json"}},
		{"show", []string{"show", "customer-referral", "--json"}},
		{"show-series", []string{"show", "standup", "--json"}},
		{"brief", []string{"brief", "--json"}},
		{"review", []string{"review", "--json"}},
		{"doctor", []string{"doctor", "--json"}},
		{"add-event", []string{"add", "event", "x", "--when", "2026-09-11T10:00", "--json"}},
		{"add-idea", []string{"add", "idea", "x", "--json"}},
		{"add-note", []string{"add", "note", "x", "--json"}},
		{"add-person", []string{"add", "person", "x", "--json"}},
		{"done", []string{"done", "customer-referral", "--json"}},
		{"update", []string{"update", "customer-referral", "--status", "building", "--json"}},
		{"rm", []string{"rm", "customer-referral", "--yes", "--json"}},
		{"tasks", []string{"tasks", "--json"}},
		{"add-task", []string{"add", "task", "x", "--json"}},
		{"link", []string{"link", "customer-referral", "https://github.com/owner/repo/pull/9", "--json"}},
		{"pr", []string{"pr", "--json"}},
		{"due", []string{"due", "--json"}},
		{"related", []string{"related", "customer-referral", "--json"}},
		{"agenda-person", []string{"agenda", "platform-team", "--json"}},
		{"board", []string{"board", "--json"}},
		{"recap", []string{"recap", "quarter", "--json"}},
		{"ship", []string{"ship", "shared-vault", "--pr", "https://github.com/owner/repo/pull/3",
			"--merged-at", "2026-09-01T10:00:00+07:00", "--json"}},
		{"link-fleet", []string{"link", "fleet", "customer-referral", "--task", "PROJ-42", "--json"}},
	}
	batchPath := batchFile(t, "ideas:\n  - title: from a batch\n")
	cases = append(cases, struct {
		name string
		args []string
	}{"add-batch", []string{"add", "--batch", batchPath, "--json"}})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureVault(t)
			got := invoke(t, root, "2026-09-02T12:00", false, tc.args...)
			if got.Code != exitOK {
				t.Fatalf("exit %d: %s", got.Code, got.Stderr)
			}
			var decoded any
			if err := json.Unmarshal([]byte(got.Stdout), &decoded); err != nil {
				t.Fatalf("--json emitted invalid JSON: %v\n%s", err, got.Stdout)
			}
			if strings.Contains(got.Stdout, `\u00`) {
				t.Errorf("--json escaped non-ASCII text:\n%s", got.Stdout)
			}
		})
	}
	// init and setup write outside the fixture, so they get their own roots.
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	for _, args := range [][]string{
		{"init", "--path", root, "--no-git", "--json"},
		{"setup", "skill", "--dir", filepath.Join(dir, "skills"), "--json"},
		{"export", "ics", "--out", filepath.Join(dir, "b.ics"), "--json"},
	} {
		got := invoke(t, root, "2026-09-02T12:00", false, args...)
		if got.Code != exitOK {
			t.Fatalf("%v: exit %d: %s", args, got.Code, got.Stderr)
		}
		var decoded any
		if err := json.Unmarshal([]byte(got.Stdout), &decoded); err != nil {
			t.Errorf("%v emitted invalid JSON: %v\n%s", args, err, got.Stdout)
		}
	}
}

// TestDoctorReportsUnresolvableRecurrencesButKeepsGoing: doctor must surface
// a DST gap and a DST ambiguity in two different series as attention items
// naming the file and the date, while still printing every other check - a
// broken rrule must never abort the rest of the diagnosis (see
// internal/vault/recurrence.go and AGENTS.md "Sharp edges").
func TestDoctorReportsUnresolvableRecurrencesButKeepsGoing(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	got := invoke(t, root, "2026-01-01T00:00", false,
		"init", "--path", root, "--timezone", "America/New_York", "--week-starts", "sun", "--no-git")
	if got.Code != exitOK {
		t.Fatalf("init: exit %d: %s", got.Code, got.Stderr)
	}
	got = invoke(t, root, "2026-01-01T00:00", false,
		"add", "event", "gap series", "--when", "2026-03-01T02:30", "--rrule", "FREQ=WEEKLY;BYDAY=SU")
	if got.Code != exitOK {
		t.Fatalf("add gap series: exit %d: %s", got.Code, got.Stderr)
	}
	got = invoke(t, root, "2026-01-01T00:00", false,
		"add", "event", "ambiguous series", "--when", "2026-10-25T01:30", "--rrule", "FREQ=WEEKLY;BYDAY=SU")
	if got.Code != exitOK {
		t.Fatalf("add ambiguous series: exit %d: %s", got.Code, got.Stderr)
	}

	got = invoke(t, root, "2026-01-01T00:00", false, "doctor")
	if got.Code != exitOK {
		t.Fatalf("doctor: exit %d: %s", got.Code, got.Stderr)
	}
	for _, want := range []string{
		"gap-series", "2026-03-08", "does not exist",
		"ambiguous-series", "2026-11-01", "ambiguous",
		// Doctor kept reporting every other check.
		"vault", "config", "files", "git",
	} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("doctor output does not mention %q:\n%s", want, got.Stdout)
		}
	}

	got = invoke(t, root, "2026-01-01T00:00", false, "doctor", "--json")
	if got.Code != exitOK {
		t.Fatalf("doctor --json: exit %d: %s", got.Code, got.Stderr)
	}
	var rep doctorReport
	if err := json.Unmarshal([]byte(got.Stdout), &rep); err != nil {
		t.Fatalf("doctor --json emitted invalid JSON: %v\n%s", err, got.Stdout)
	}
	if rep.Fatal != "" {
		t.Errorf("a bad series made doctor fatal: %q", rep.Fatal)
	}
	joined := strings.Join(rep.Attention, "\n")
	if !strings.Contains(joined, "2026-03-08") || !strings.Contains(joined, "does not exist") {
		t.Errorf("attention does not name the gap: %v", rep.Attention)
	}
	if !strings.Contains(joined, "2026-11-01") || !strings.Contains(joined, "ambiguous") {
		t.Errorf("attention does not name the ambiguity: %v", rep.Attention)
	}
	names := map[string]bool{}
	for _, row := range rep.Rows {
		names[row.Name] = true
	}
	for _, want := range []string{"vault", "config", "files", "links", "ideas", "recurrence", "git", "skill", "backlog", "binary"} {
		if !names[want] {
			t.Errorf("doctor dropped the %q check", want)
		}
	}
}

// TestReviewRefusesWithoutATerminal: the triage screen is a human surface, so
// it must say so rather than half-running down a pipe.
func TestReviewRefusesWithoutATerminal(t *testing.T) {
	root := fixtureVault(t)
	got := invoke(t, root, "2026-09-02T12:00", false, "review")
	if got.Code != exitUsage {
		t.Errorf("exit %d, want %d", got.Code, exitUsage)
	}
	for _, want := range []string{"needs a terminal", "brain-axi ideas", "review --json"} {
		if !strings.Contains(got.Stderr, want) {
			t.Errorf("the refusal does not mention %q: %s", want, got.Stderr)
		}
	}
	// With nothing stale it says so instead, and exits zero.
	got = invoke(t, root, "2026-08-10T12:00", false, "review")
	if got.Code != exitOK {
		t.Errorf("exit %d with nothing to triage: %s", got.Code, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "nothing to triage") {
		t.Errorf("stdout: %s", got.Stdout)
	}
}

func TestVersionAndHelp(t *testing.T) {
	root := fixtureVault(t)
	for _, flag := range []string{"-v", "-V", "--version"} {
		got := invoke(t, root, "", false, flag)
		if got.Code != exitOK {
			t.Errorf("%s: exit %d", flag, got.Code)
		}
		if !strings.HasPrefix(got.Stdout, "brain-axi ") {
			t.Errorf("%s printed %q", flag, got.Stdout)
		}
	}
	for _, command := range append([]string{""}, commandNames...) {
		args := []string{"--help"}
		if command != "" {
			args = []string{command, "--help"}
		}
		got := invoke(t, root, "", false, args...)
		if got.Code != exitOK {
			t.Errorf("%v: exit %d: %s", args, got.Code, got.Stderr)
		}
		if !strings.HasPrefix(got.Stdout, "usage: brain-axi") {
			t.Errorf("%v printed %q", args, firstLine(got.Stdout))
		}
	}
}

// TestUsageNamesEveryCommand keeps the help text honest as commands change.
func TestUsageNamesEveryCommand(t *testing.T) {
	for _, name := range commandNames {
		if !strings.Contains(usage, name) {
			t.Errorf("the usage text does not mention %q", name)
		}
		if _, ok := commandHelp[name]; !ok {
			t.Errorf("%q has no --help text", name)
		}
	}
	for name := range commandHelp {
		found := false
		for _, c := range commandNames {
			if c == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("--help text exists for %q, which is not a command", name)
		}
	}
}

func TestInitThenCaptureThenRecall(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	// The zone is named so this test's stored offsets are the same on every
	// machine; TestInitTakesItsTimezoneFromTheMachine covers the default.
	got := invoke(t, root, "2026-09-01T09:00", false, "init", "--path", root, "--no-git", "--timezone", "Asia/Bangkok")
	if got.Code != exitOK {
		t.Fatalf("init: exit %d: %s", got.Code, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Asia/Bangkok") {
		t.Errorf("init did not report the timezone:\n%s", got.Stdout)
	}
	// A vault is never created implicitly, so init on a fresh directory is the
	// only way the next command can work.
	got = invoke(t, root, "2026-09-01T09:00", false,
		"add", "event", "Platform team sync in Zürich", "--when", "2026-09-04T14:00", "--duration", "60m")
	if got.Code != exitOK {
		t.Fatalf("add: exit %d: %s", got.Code, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "2026-09-04T14:00:00+07:00 (Friday)") {
		t.Errorf("add did not echo the resolved absolute date and weekday:\n%s", got.Stdout)
	}
	got = invoke(t, root, "2026-09-04T13:00", false, "today")
	if got.Code != exitOK {
		t.Fatalf("today: exit %d: %s", got.Code, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Platform team sync in Zürich") {
		t.Errorf("today did not find the captured event:\n%s", got.Stdout)
	}
	if !strings.Contains(got.Stdout, "next") {
		t.Errorf("today did not flag the next event:\n%s", got.Stdout)
	}
}

func TestExportICSToFileAndStdout(t *testing.T) {
	root := fixtureVault(t)
	out := filepath.Join(t.TempDir(), "brain.ics")
	got := invoke(t, root, "2026-09-02T12:00", false, "export", "ics", "--out", out)
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "BEGIN:VCALENDAR\r\n") {
		t.Errorf("the file is not an iCalendar stream: %q", firstLine(string(data)))
	}
	// Streaming to stdout gives the same bytes.
	got = invoke(t, root, "2026-09-02T12:00", false, "export", "ics")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	if got.Stdout != string(data) {
		t.Error("streaming to stdout and writing to a file disagree")
	}
	// A bounded export keeps a series that can reach the window.
	got = invoke(t, root, "2026-09-02T12:00", false, "export", "ics", "--from", "2026-09-04", "--to", "2026-09-04", "--out", out)
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	data, err = os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "UID:standup@brain-axi") {
		t.Error("a bounded export dropped a series that recurs into the window")
	}
	if strings.Contains(string(data), "UID:one-on-one-20260905@") {
		t.Error("a bounded export kept an event outside the window")
	}
	if got := invoke(t, root, "2026-09-02T12:00", false, "export", "ics", "--from", "2026-09-04"); got.Code == exitOK {
		t.Error("export accepted --from without --to")
	}
}

// batchFile writes a batch and returns its path.
func batchFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meeting.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const meetingBatch = `# Platform team meeting, resolved by the agent from a raw note.
ideas:
  - title: cache the schedule expiry lookup
    nudge_after: 30d
tasks:
  - title: review CI capacity again
    due: 2026-09-05T17:00
    follow_up_after: 3d
delegated:
  - title: migrate the staging database to the new cluster
    assignee: platform-team
    follow_up_after: 14d
notes:
  - text: ask infrastructure whether CI capacity is per project
events:
  - title: Platform team follow-up
    when: 2026-09-09T14:00
    duration: 60m
    with: [platform-team]
`

// TestBatchIngestEchoesBackEverySection is the echo-back rule applied to a
// batch: a whole meeting resolved in one go must be reported back grouped by
// section, so a misread is corrected now rather than in three weeks.
func TestBatchIngestEchoesBackEverySection(t *testing.T) {
	root := fixtureVault(t)
	path := batchFile(t, meetingBatch)
	got := invoke(t, root, "2026-09-02T09:30", false, "add", "--batch", path)
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	// The batch path is a temp directory, so the golden is the output with the
	// path line dropped rather than a machine-specific file name.
	var kept []string
	for _, line := range strings.Split(got.Stdout, "\n") {
		if strings.HasPrefix(line, "batch: ") {
			continue
		}
		kept = append(kept, line)
	}
	golden(t, "add-batch.golden", strings.Join(kept, "\n"))

	for _, section := range []string{"ideas[", "tasks[", "delegated[", "notes[", "events["} {
		if !strings.Contains(got.Stdout, section) {
			t.Errorf("the echo-back does not group by %s:\n%s", section, got.Stdout)
		}
	}
	// Every stored record is real and the vault still parses.
	v, err := vault.OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Walk(); err != nil {
		t.Fatalf("the batch left the vault unparseable: %v", err)
	}
	for _, id := range []string{
		"cache-the-schedule-expiry-lookup",
		"review-ci-capacity-again",
		"migrate-the-staging-database-to-the-new-cluster",
		"platform-team-follow-up-20260909",
		"daily-2026-09-02",
	} {
		if _, err := v.Find(id); err != nil {
			t.Errorf("the batch did not store %s: %v", id, err)
		}
	}
}

// TestMalformedBatchThroughTheCLIWritesNothing: the atomicity promise as the
// user experiences it - one bad entry, nothing stored, and a path:line:
// reason pointing at the line to fix.
func TestMalformedBatchThroughTheCLIWritesNothing(t *testing.T) {
	root := fixtureVault(t)
	before := recordSnapshot(t, root)

	path := batchFile(t, `ideas:
  - title: a perfectly good idea
  - title: another good one
tasks:
  - title: a good task
    due: 2026-09-05T17:00
delegated:
  - title: handed to nobody at all
    follow_up_after: 7d
events:
  - title: a good event
    when: 2026-09-09T14:00
`)
	got := invoke(t, root, "2026-09-02T09:30", false, "add", "--batch", path)
	if got.Code != exitData {
		t.Errorf("exit %d, want %d: a malformed batch is a data error", got.Code, exitData)
	}
	if got.Stdout != "" {
		t.Errorf("a partial answer was printed alongside the refusal:\n%s", got.Stdout)
	}
	if !strings.Contains(got.Stderr, "meeting.yml:8:") {
		t.Errorf("the error does not name the batch file and the line: %s", got.Stderr)
	}
	if !strings.Contains(got.Stderr, `a delegated entry needs an "assignee"`) {
		t.Errorf("the error does not explain the problem: %s", got.Stderr)
	}
	if after := recordSnapshot(t, root); after != before {
		t.Errorf("a refused batch changed the vault:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// recordSnapshot is every record file and its bytes, so a test can prove that a
// refused command changed nothing at all.
func recordSnapshot(t *testing.T, root string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(&sb, "%s\n%s\n", rel, data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// TestOfflineCommandsNeverReachAForge is the rule that keeps this a second
// brain rather than a network client: a linked record must not turn `today`
// into something that fails on a plane.
func TestOfflineCommandsNeverReachAForge(t *testing.T) {
	root := fixtureVault(t)
	// The fixture already holds a task linked to a self-hosted GitLab this
	// machine cannot reach, which is exactly the case that would hang.
	f := useForge(t, &scriptedForge{fail: map[string]error{
		"gh":   errors.New("gh must not run here"),
		"glab": errors.New("glab must not run here"),
	}})
	commands := [][]string{
		{"today"}, {"week"}, {"agenda", "--from", "2026-09-01", "--to", "2026-09-30"},
		{"ideas"}, {"tasks"}, {"search", "capacity"}, {"brief"}, {"show", "migrate-staging-db"},
		{"export", "ics"}, {"review", "--json"}, {"pr"}, nil,
		// Round three's read commands are held to the same rule: a linked
		// record must not turn any of them into something that fails on a
		// plane. recap reaches a forge only when --verify-forge asks it to.
		{"due"}, {"related", "platform-team-sync-20260904"}, {"agenda", "platform-team"},
		{"board"}, {"recap", "month"}, {"recap", "week", "--json"},
		{"recap", "--from", "2026-09-01", "--to", "2026-09-30"},
		{"show", "platform-team"},
	}
	for _, args := range commands {
		name := strings.Join(args, " ")
		if name == "" {
			name = "dashboard"
		}
		t.Run(name, func(t *testing.T) {
			got := invoke(t, root, "2026-09-02T12:00", false, args...)
			if got.Code != exitOK {
				t.Fatalf("exit %d: %s", got.Code, got.Stderr)
			}
			if len(f.calls) != 0 {
				t.Errorf("%v shelled out to a forge: %v", args, f.calls)
			}
		})
	}
}

// TestStaleStatusIsNeverPresentableAsLive: wherever a cached forge status is
// shown, the time it was read is shown with it.
func TestStaleStatusIsNeverPresentableAsLive(t *testing.T) {
	root := fixtureVault(t)
	cases := []struct {
		name string
		args []string
		now  string
		want []string
	}{
		{
			name: "pr, the day after it was read",
			args: []string{"pr"}, now: "2026-08-21T10:15",
			want: []string{"2026-08-20T10:15:00+07:00", "1d ago", "cached"},
		},
		{
			name: "pr, hours after it was read",
			args: []string{"pr"}, now: "2026-08-20T15:15",
			want: []string{"2026-08-20T10:15:00+07:00", "5h ago", "cached"},
		},
		{
			name: "pr, a fortnight after it was read",
			args: []string{"pr"}, now: "2026-09-02T12:00",
			want: []string{"2026-08-20T10:15:00+07:00", "13d ago", "cached"},
		},
		{
			name: "show",
			args: []string{"show", "migrate-staging-db"}, now: "2026-09-02T12:00",
			want: []string{"2026-08-20T10:15:00+07:00", "13d ago", "cached"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := invoke(t, root, tc.now, false, tc.args...)
			if got.Code != exitOK {
				t.Fatalf("exit %d: %s", got.Code, got.Stderr)
			}
			for _, want := range tc.want {
				if !strings.Contains(got.Stdout, want) {
					t.Errorf("output does not carry %q:\n%s", want, got.Stdout)
				}
			}
		})
	}

	// --json says it too, as a field rather than as prose.
	got := invoke(t, root, "2026-09-02T12:00", false, "pr", "--json")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	var payload struct {
		Refreshed bool `json:"refreshed"`
		PRs       []struct {
			State     string `json:"state"`
			CheckedAt string `json:"checked_at"`
			Age       string `json:"checked_age"`
			Cached    bool   `json:"cached"`
		} `json:"pull_requests"`
	}
	if err := json.Unmarshal([]byte(got.Stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Refreshed {
		t.Error("pr without --refresh reported itself as refreshed")
	}
	if len(payload.PRs) != 1 {
		t.Fatalf("expected one linked record, got %d", len(payload.PRs))
	}
	row := payload.PRs[0]
	if !row.Cached {
		t.Error("a status read out of a file is not marked cached")
	}
	if row.CheckedAt == "" || row.Age == "" {
		t.Errorf("a cached status carries no timestamp: %+v", row)
	}
}

// TestRefreshWritesTheCacheIntoTheRecordItself: the cache is derived data in
// the source of truth, so it must land in the record's own frontmatter and
// nowhere else.
func TestRefreshWritesTheCacheIntoTheRecordItself(t *testing.T) {
	root := fixtureVault(t)
	useForge(t, &scriptedForge{out: map[string]string{"gh": ghMergedPassing}})

	got := invoke(t, root, "2026-09-02T12:00", false,
		"link", "customer-referral", "https://github.com/Thanhbinh1905/secondbrain/pull/1", "--refresh")
	if got.Code != exitOK {
		t.Fatalf("link --refresh: exit %d: %s", got.Code, got.Stderr)
	}

	raw, err := os.ReadFile(filepath.Join(root, "ideas", "customer-referral.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"forge_url: https://github.com/Thanhbinh1905/secondbrain/pull/1",
		"forge_state: merged",
		"forge_checks: passing",
		"forge_checked_at: 2026-09-02T12:00:00+07:00",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the record's frontmatter is missing %q:\n%s", want, raw)
		}
	}
	// The body survives byte for byte, and no side store appeared.
	if !strings.Contains(string(raw), "Every referrer earns a share of the reward.") {
		t.Errorf("the body did not survive the cache write:\n%s", raw)
	}
	v, err := vault.OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Walk(); err != nil {
		t.Fatalf("the cache write left the vault unparseable: %v", err)
	}
	// A later read with no --refresh serves that cache, out loud.
	got = invoke(t, root, "2026-09-09T12:00", false, "pr", "customer-referral")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	for _, want := range []string{"merged", "passing", "2026-09-02T12:00:00+07:00", "7d ago", "cached"} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("the cached read does not carry %q:\n%s", want, got.Stdout)
		}
	}
}

// TestForgeFailuresAreReportedAsThemselves: never render unknown as fine, and
// never fall back to a cached value without saying it is cached and how old.
func TestForgeFailuresAreReportedAsThemselves(t *testing.T) {
	t.Run("a missing CLI names the concrete requirement", func(t *testing.T) {
		root := fixtureVault(t)
		useForge(t, &scriptedForge{missing: map[string]bool{"glab": true}})
		got := invoke(t, root, "2026-09-02T12:00", false, "pr", "--refresh")
		if got.Code == exitOK {
			t.Error("a missing forge CLI exited zero")
		}
		for _, want := range []string{"glab is not on PATH", "git.example.com", "auth login"} {
			if !strings.Contains(got.Stdout+got.Stderr, want) {
				t.Errorf("output does not mention %q:\n%s%s", want, got.Stdout, got.Stderr)
			}
		}
	})

	t.Run("an unreachable host falls back only out loud", func(t *testing.T) {
		root := fixtureVault(t)
		useForge(t, &scriptedForge{fail: map[string]error{
			"glab": &forge.CLIError{CLI: "glab", Err: errors.New("exit status 1"),
				Stderr: "X dial tcp 203.0.113.10:443: i/o timeout"},
		}})
		got := invoke(t, root, "2026-09-02T12:00", false, "pr", "--refresh")
		if got.Code == exitOK {
			t.Error("an unreachable forge exited zero")
		}
		combined := got.Stdout + got.Stderr
		for _, want := range []string{
			"i/o timeout",
			// The fallback says both that it is cached and how old it is.
			"cached status from 2026-08-20T10:15:00+07:00",
			"13d ago",
		} {
			if !strings.Contains(combined, want) {
				t.Errorf("output does not carry %q:\n%s", want, combined)
			}
		}
		// The stale value was not quietly rewritten as if it were fresh.
		raw, err := os.ReadFile(filepath.Join(root, "tasks", "migrate-staging-db.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "forge_checked_at: 2026-08-20T10:15:00+07:00") {
			t.Errorf("a failed refresh moved the cache timestamp:\n%s", raw)
		}
	})

	t.Run("a never-checked link is never rendered as fine", func(t *testing.T) {
		root := fixtureVault(t)
		f := useForge(t, &scriptedForge{fail: map[string]error{
			"gh": &forge.CLIError{CLI: "gh", Err: errors.New("exit status 1"), Stderr: "X could not resolve host"},
		}})
		got := invoke(t, root, "2026-09-02T12:00", false,
			"link", "customer-referral", "https://github.com/owner/repo/pull/9")
		if got.Code != exitOK {
			t.Fatalf("plain link: exit %d: %s", got.Code, got.Stderr)
		}
		if len(f.calls) != 0 {
			t.Errorf("attaching a link reached the network: %v", f.calls)
		}
		got = invoke(t, root, "2026-09-02T12:00", false, "pr", "customer-referral", "--refresh")
		if got.Code == exitOK {
			t.Error("a failed first check exited zero")
		}
		combined := got.Stdout + got.Stderr
		if !strings.Contains(combined, "never been checked") {
			t.Errorf("output does not say the link has never been checked:\n%s", combined)
		}
		if strings.Contains(combined, "passing") || strings.Contains(combined, "merged") {
			t.Errorf("an unknown status was rendered as a real one:\n%s", combined)
		}
	})
}

// TestDoctorReportsForgeReach: which CLIs are here, and whether each linked
// host can actually be read from this machine.
func TestDoctorReportsForgeReach(t *testing.T) {
	root := fixtureVault(t)

	t.Run("an unreachable linked host is an attention item", func(t *testing.T) {
		useForge(t, &scriptedForge{fail: map[string]error{
			"glab": &forge.CLIError{CLI: "glab", Err: errors.New("exit status 1"),
				Stderr: "X git.example.com has not been authenticated with glab"},
		}})
		got := invoke(t, root, "2026-09-02T12:00", false, "doctor", "--json")
		if got.Code != exitOK {
			t.Fatalf("exit %d: %s", got.Code, got.Stderr)
		}
		var rep doctorReport
		if err := json.Unmarshal([]byte(got.Stdout), &rep); err != nil {
			t.Fatal(err)
		}
		names := map[string]doctorRow{}
		for _, row := range rep.Rows {
			names[row.Name] = row
		}
		forgeRow, ok := names["forge"]
		if !ok {
			t.Fatal("doctor has no forge check")
		}
		for _, want := range []string{"gh present", "glab present", "git.example.com"} {
			if !strings.Contains(forgeRow.Detail, want) {
				t.Errorf("the forge check does not report %q: %s", want, forgeRow.Detail)
			}
		}
		if forgeRow.OK {
			t.Error("an unreachable linked host was reported as ok")
		}
		if _, ok := names["tasks"]; !ok {
			t.Error("doctor has no tasks check")
		}
		joined := strings.Join(rep.Attention, "\n")
		if !strings.Contains(joined, "git.example.com cannot be read from this machine") {
			t.Errorf("attention does not name the unreachable host: %v", rep.Attention)
		}
		if !strings.Contains(joined, "past its 14d follow-up horizon") {
			t.Errorf("attention does not name the unchecked task: %v", rep.Attention)
		}
	})

	t.Run("a missing CLI is reported as missing", func(t *testing.T) {
		useForge(t, &scriptedForge{missing: map[string]bool{"glab": true}})
		got := invoke(t, root, "2026-09-02T12:00", false, "doctor")
		if got.Code != exitOK {
			t.Fatalf("exit %d: %s", got.Code, got.Stderr)
		}
		if !strings.Contains(got.Stdout, "glab missing") {
			t.Errorf("doctor does not report the missing CLI:\n%s", got.Stdout)
		}
		if !strings.Contains(got.Stdout, "gh present") {
			t.Errorf("doctor does not report the CLI that is here:\n%s", got.Stdout)
		}
	})
}

// TestDanglingAssigneeIsReportedNotRejected: a task's assignee only ever
// format-validates against ValidateID, matching how an event's with: behaves.
// A well-formed but nonexistent assignee must still parse - and must not be
// reported nowhere - so doctor surfaces it as an unresolved link and `show`
// makes it visible as unresolved, exactly as a dangling with: entry already
// is.
func TestDanglingAssigneeIsReportedNotRejected(t *testing.T) {
	root := fixtureVault(t)
	got := invoke(t, root, "2026-09-02T12:00", false,
		"add", "task", "chase a ghost", "--assignee", "nobody-exists")
	if got.Code != exitOK {
		t.Fatalf("a well-formed but nonexistent assignee was rejected: exit %d: %s", got.Code, got.Stderr)
	}

	doctorGot := invoke(t, root, "2026-09-02T12:00", false, "doctor", "--json")
	if doctorGot.Code != exitOK {
		t.Fatalf("doctor: exit %d: %s", doctorGot.Code, doctorGot.Stderr)
	}
	var rep doctorReport
	if err := json.Unmarshal([]byte(doctorGot.Stdout), &rep); err != nil {
		t.Fatalf("doctor --json emitted invalid JSON: %v\n%s", err, doctorGot.Stdout)
	}
	joined := strings.Join(rep.Attention, "\n")
	if !strings.Contains(joined, "nobody-exists") {
		t.Errorf("doctor's attention does not mention the dangling assignee: %v", rep.Attention)
	}
	names := map[string]doctorRow{}
	for _, row := range rep.Rows {
		names[row.Name] = row
	}
	if linksRow, ok := names["links"]; !ok || linksRow.OK {
		t.Errorf("doctor's links check did not flag the dangling assignee: %+v", names["links"])
	}

	showGot := invoke(t, root, "2026-09-02T12:00", false, "show", "chase-a-ghost", "--json")
	if showGot.Code != exitOK {
		t.Fatalf("show: exit %d: %s", showGot.Code, showGot.Stderr)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(showGot.Stdout), &obj); err != nil {
		t.Fatalf("show --json emitted invalid JSON: %v\n%s", err, showGot.Stdout)
	}
	profile, ok := obj["assignee_profile"].(map[string]any)
	if !ok {
		t.Fatalf("show --json has no assignee_profile: %v", obj)
	}
	if resolved, _ := profile["resolved"].(bool); resolved {
		t.Error("show reports a nonexistent assignee as resolved")
	}
	textGot := invoke(t, root, "2026-09-02T12:00", false, "show", "chase-a-ghost")
	if textGot.Code != exitOK {
		t.Fatalf("show: exit %d: %s", textGot.Code, textGot.Stderr)
	}
	if !strings.Contains(textGot.Stdout, "nobody-exists") || !strings.Contains(textGot.Stdout, "no") {
		t.Errorf("show's text output does not make the unresolved assignee visible:\n%s", textGot.Stdout)
	}
}

// TestRealForgeStatus checks a genuine pull request through the real gh, rather
// than a scripted answer. It needs network and an authenticated gh, so it is
// opt-in: set BRAIN_AXI_FORGE_E2E=1 to run it.
//
//	BRAIN_AXI_FORGE_E2E=1 go test ./cmd/brain-axi -run TestRealForgeStatus -v
func TestRealForgeStatus(t *testing.T) {
	if os.Getenv("BRAIN_AXI_FORGE_E2E") != "1" {
		t.Skip("set BRAIN_AXI_FORGE_E2E=1 to check a real pull request through the real gh")
	}
	previous := runner
	runner = forge.Exec
	t.Cleanup(func() { runner = previous })

	root := fixtureVault(t)
	const url = "https://github.com/Thanhbinh1905/secondbrain/pull/1"
	got := invoke(t, root, "", false, "link", "customer-referral", url, "--refresh")
	if got.Code != exitOK {
		t.Fatalf("link --refresh: exit %d: %s", got.Code, got.Stderr)
	}
	// This repository's own PR 1 is merged and its CI passed.
	for _, want := range []string{"merged", "passing"} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("the real status does not report %q:\n%s", want, got.Stdout)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "ideas", "customer-referral.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "forge_state: merged") {
		t.Errorf("the real status was not cached into the record:\n%s", raw)
	}
}

// TestLinkKeepsTheLinkWhenARefreshFails: attaching is offline and the URL is
// valid, so a forge this machine cannot reach must not cost the user the
// link. A self-hosted forge may be unreachable from outside its network.
func TestLinkKeepsTheLinkWhenARefreshFails(t *testing.T) {
	root := fixtureVault(t)
	useForge(t, &scriptedForge{fail: map[string]error{
		"glab": &forge.CLIError{CLI: "glab", Err: errors.New("exit status 1"),
			Stderr: "X dial tcp 203.0.113.10:443: i/o timeout"},
	}})
	const url = "https://git.example.com/platform/service/-/merge_requests/77"
	got := invoke(t, root, "2026-09-02T12:00", false, "link", "review-backup-policy", url, "--refresh")
	if got.Code == exitOK {
		t.Error("a failed refresh exited zero")
	}
	combined := got.Stdout + got.Stderr
	for _, want := range []string{"i/o timeout", "the link was stored"} {
		if !strings.Contains(combined, want) {
			t.Errorf("output does not carry %q:\n%s", want, combined)
		}
	}
	// The link is on the record, and it carries no status it never obtained.
	v, err := vault.OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := v.Find("review-backup-policy")
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasForge || r.Forge.URL != url {
		t.Fatalf("the link was lost: %+v", r.Forge)
	}
	if r.Forge.HasStatus {
		t.Errorf("a failed refresh invented a status: %+v", r.Forge)
	}
}

// TestLinkRefusesToSilentlyReplaceALink, and drops the old cache when it does
// replace one: a status that described a different pull request is worse than
// no status at all.
func TestLinkReplacementDropsTheOldCache(t *testing.T) {
	root := fixtureVault(t)
	const replacement = "https://github.com/owner/repo/pull/42"

	got := invoke(t, root, "2026-09-02T12:00", false, "link", "migrate-staging-db", replacement)
	if got.Code != exitUsage {
		t.Errorf("replacing a link without --force exited %d", got.Code)
	}
	if !strings.Contains(got.Stderr, "--force") {
		t.Errorf("the refusal does not name --force: %s", got.Stderr)
	}

	got = invoke(t, root, "2026-09-02T12:00", false, "link", "migrate-staging-db", replacement, "--force")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	raw, err := os.ReadFile(filepath.Join(root, "tasks", "migrate-staging-db.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "forge_url: "+replacement) {
		t.Errorf("the new link was not stored:\n%s", raw)
	}
	for _, gone := range []string{"forge_state:", "forge_checks:", "forge_checked_at:"} {
		if strings.Contains(string(raw), gone) {
			t.Errorf("%s survived a link replacement; it described a different change:\n%s", gone, raw)
		}
	}
	if !strings.Contains(got.Stdout, "cached status was dropped") {
		t.Errorf("the drop was not reported:\n%s", got.Stdout)
	}
}

// TestInitTakesItsTimezoneFromTheMachine: the vault timezone stamps an explicit
// UTC offset onto every stored record, so `init` with no --timezone must write
// the machine's own zone rather than one the tool picked. A meeting entered as
// 2pm and stored at the wrong offset is a corruption nobody notices for weeks.
func TestInitTakesItsTimezoneFromTheMachine(t *testing.T) {
	t.Setenv("TZ", "Europe/Lisbon")
	root := filepath.Join(t.TempDir(), "vault")
	got := invoke(t, root, "2026-09-01T09:00", false, "init", "--path", root, "--no-git")
	if got.Code != exitOK {
		t.Fatalf("init: exit %d: %s", got.Code, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "timezone: Europe/Lisbon") {
		t.Errorf("init did not write the machine's zone:\n%s", got.Stdout)
	}
	// It is the vault's zone, not just a line of output: what init writes is
	// what the next command reads back.
	v, err := vault.OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if v.Config.Timezone != "Europe/Lisbon" {
		t.Errorf("vault timezone = %q", v.Config.Timezone)
	}
	// An explicit --timezone still wins over the machine.
	other := filepath.Join(t.TempDir(), "vault")
	got = invoke(t, other, "2026-09-01T09:00", false, "init", "--path", other, "--no-git", "--timezone", "Pacific/Auckland")
	if got.Code != exitOK {
		t.Fatalf("init --timezone: exit %d: %s", got.Code, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "timezone: Pacific/Auckland") {
		t.Errorf("--timezone was not honoured:\n%s", got.Stdout)
	}
}

// TestInitRefusesWhenTheMachineZoneIsUnknown: an unresolvable zone is a loud,
// actionable message, never a substituted guess. A $TZ that names no zone in
// the database is the reachable case - a typo, or a container with a bad
// environment - and it is the one that matters most, because Go's own
// time.Local silently becomes UTC there rather than failing.
func TestInitRefusesWhenTheMachineZoneIsUnknown(t *testing.T) {
	t.Setenv("TZ", "Not/AZone")
	root := filepath.Join(t.TempDir(), "vault")
	got := invoke(t, root, "2026-09-01T09:00", false, "init", "--path", root, "--no-git")
	if got.Code == exitOK {
		t.Fatalf("init succeeded with an unresolvable zone:\n%s", got.Stdout)
	}
	for _, want := range []string{"cannot determine this machine's timezone", "--timezone"} {
		if !strings.Contains(got.Stderr, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, got.Stderr)
		}
	}
	// Nothing was written, so a retry with --timezone is a clean first run.
	if _, err := os.Stat(filepath.Join(root, ".brain", "config.yml")); err == nil {
		t.Error("a config was written despite the refusal")
	}
	got = invoke(t, root, "2026-09-01T09:00", false, "init", "--path", root, "--no-git", "--timezone", "Europe/Lisbon")
	if got.Code != exitOK {
		t.Fatalf("init after the refusal: exit %d: %s", got.Code, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "timezone: Europe/Lisbon") {
		t.Errorf("the retry did not write the named zone:\n%s", got.Stdout)
	}
}

func TestInitExistingVaultDoesNotNeedSystemZone(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vault")
	got := invoke(t, root, "2026-09-01T09:00", false, "init", "--path", root, "--no-git", "--timezone", "Europe/Lisbon")
	if got.Code != exitOK {
		t.Fatalf("first init: exit %d: %s", got.Code, got.Stderr)
	}
	configPath := filepath.Join(root, vault.BrainDir, vault.ConfigName)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("TZ", "Not/AZone")
	got = invoke(t, root, "2026-09-01T09:00", false, "init", "--path", root, "--no-git")
	if got.Code != exitOK {
		t.Fatalf("re-init: exit %d: %s", got.Code, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "timezone: Europe/Lisbon") {
		t.Errorf("re-init did not report the existing zone:\n%s", got.Stdout)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("re-init rewrote the config:\n%s", after)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"timezone", []string{"--timezone", "Not/AZone"}},
		{"week-starts", []string{"--week-starts", "not-a-day"}},
		{"nudge-after", []string{"--nudge-after", "invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"init", "--path", root, "--no-git"}, tc.args...)
			got := invoke(t, root, "2026-09-01T09:00", false, args...)
			if got.Code == exitOK {
				t.Fatalf("re-init accepted invalid --%s", tc.name)
			}
			after, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("re-init rewrote the config:\n%s", after)
			}
		})
	}
}

// TestInitDoesNotClaimGitProtectsAnythingItDoesNotYet: `git init` makes a
// repository with no commits, which protects nothing. Reporting it as
// "initialised" beside a lone missing-remote warning tells the operator the
// local side is already safe, so the one thing that would make it safe - the
// first commit - never gets made. brain-axi does not make that commit itself:
// committing somebody's notes on their behalf is not this tool's call. It says
// so instead, in doctor's own words.
func TestInitDoesNotClaimGitProtectsAnythingItDoesNotYet(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := filepath.Join(t.TempDir(), "vault with space")
	got := invoke(t, root, "2026-09-01T09:00", false, "init", "--path", root)
	if got.Code != exitOK {
		t.Fatalf("init: exit %d: %s", got.Code, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "no commits yet") {
		t.Errorf("init did not say the new repository is empty:\n%s", got.Stdout)
	}
	// The gap has to be actionable, not merely stated, and it belongs beside
	// the missing-remote gap rather than buried in the git line.
	const commandPrefix = "empty repository protects nothing; `"
	start := strings.Index(got.Stdout, commandPrefix)
	if start < 0 {
		t.Fatalf("init did not name the command that closes the gap:\n%s", got.Stdout)
	}
	command := got.Stdout[start+len(commandPrefix):]
	end := strings.Index(command, "` starts the history")
	if end < 0 {
		t.Fatalf("init emitted an unterminated recovery command:\n%s", got.Stdout)
	}
	command = command[:end]
	// doctor raises the same gap in the same words: it is the surface the
	// README points at for what is missing, and an empty repository is the
	// larger of the two durability gaps it reports.
	doc := invoke(t, root, "2026-09-01T09:00", false, "doctor")
	if doc.Code != exitOK {
		t.Fatalf("doctor: exit %d: %s", doc.Code, doc.Stderr)
	}
	if !strings.Contains(doc.Stdout, "no commits yet") {
		t.Errorf("doctor did not report the empty repository:\n%s", doc.Stdout)
	}
	if !strings.Contains(doc.Stdout, "vault has no commits - an empty repository protects nothing") {
		t.Errorf("doctor did not raise the empty repository as attention:\n%s", doc.Stdout)
	}
	if !strings.Contains(doc.Stdout, "git -C '"+root+"' remote add origin <private repo>") {
		t.Errorf("doctor did not quote the vault path in the remote command:\n%s", doc.Stdout)
	}

	// The claim is about this repository, so it has to be true of it.
	out, err := exec.Command("git", "-C", root, "rev-list", "--count", "--all").Output()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.TrimSpace(string(out)); n != "0" {
		t.Errorf("init made %s commit(s) in the vault; it must never commit for the user", n)
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=brain-axi test",
		"GIT_AUTHOR_EMAIL=brain-axi@example.invalid",
		"GIT_COMMITTER_NAME=brain-axi test",
		"GIT_COMMITTER_EMAIL=brain-axi@example.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("printed recovery command failed: %v: %s\n%s", err, out, command)
	}
	out, err = exec.Command("git", "-C", root, "rev-list", "--count", "--all").Output()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.TrimSpace(string(out)); n != "1" {
		t.Errorf("printed recovery command left %s commits, want 1", n)
	}
	// --json carries the same fact, because an agent reads that and not the
	// rendered attention list.
	fresh := filepath.Join(t.TempDir(), "vault")
	got = invoke(t, fresh, "2026-09-01T09:00", false, "init", "--path", fresh, "--json")
	if got.Code != exitOK {
		t.Fatalf("init --json: exit %d: %s", got.Code, got.Stderr)
	}
	var payload struct {
		GitInitialised bool `json:"git_initialised"`
		GitCommits     int  `json:"git_commits"`
		Known          bool `json:"git_commits_known"`
	}
	if err := json.Unmarshal([]byte(got.Stdout), &payload); err != nil {
		t.Fatalf("init --json: %v:\n%s", err, got.Stdout)
	}
	if !payload.GitInitialised {
		t.Fatalf("init --json did not initialise a repository:\n%s", got.Stdout)
	}
	if !payload.Known || payload.GitCommits != 0 {
		t.Errorf("init --json reported %d commit(s), known=%v", payload.GitCommits, payload.Known)
	}
}

func TestInitReportsExistingRepositoryCommitCount(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := filepath.Join(t.TempDir(), "vault")
	got := invoke(t, root, "2026-09-01T09:00", false, "init", "--path", root, "--timezone", "Europe/Lisbon")
	if got.Code != exitOK {
		t.Fatalf("init: exit %d: %s", got.Code, got.Stderr)
	}
	for i := 1; i <= 3; i++ {
		path := filepath.Join(root, fmt.Sprintf("commit-%d.txt", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", root, "add", "-A").CombinedOutput(); err != nil {
			t.Fatalf("git add: %v: %s", err, out)
		}
		if out, err := exec.Command("git", "-C", root, "-c", "user.name=brain-axi test", "-c", "user.email=brain-axi@example.invalid", "commit", "--quiet", "-m", fmt.Sprintf("commit %d", i)).CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v: %s", err, out)
		}
	}
	got = invoke(t, root, "2026-09-01T09:00", false, "init", "--path", root)
	if got.Code != exitOK {
		t.Fatalf("re-init: exit %d: %s", got.Code, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "already a git repository, 3 commits") {
		t.Errorf("re-init did not report the existing commit count:\n%s", got.Stdout)
	}
}
