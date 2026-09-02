package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/frontmatter"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
)

func goodVault(t *testing.T) *Vault {
	t.Helper()
	v, err := OpenAt(filepath.Join("testdata", "good"))
	if err != nil {
		t.Fatalf("OpenAt(testdata/good): %v", err)
	}
	return v
}

// testConfig is DefaultConfig with a timezone named, because DefaultConfig
// deliberately carries none: the tests' stored timestamps are written at
// +07:00, and Asia/Bangkok is that offset with no DST to shift a boundary.
func testConfig() Config {
	cfg := DefaultConfig()
	cfg.Timezone = "Asia/Bangkok"
	return cfg
}

func at(t *testing.T, v *Vault, stamp string) time.Time {
	t.Helper()
	got, err := v.Zone.Normalise(stamp)
	if err != nil {
		t.Fatalf("Normalise(%q): %v", stamp, err)
	}
	return got
}

// TestCorruptVaultFailsLoudly is NFR-4's assertion. Each fixture is corrupt in
// exactly one way, and every one of them must produce path:line: reason.
// A file is never skipped, defaulted or repaired.
func TestCorruptVaultFailsLoudly(t *testing.T) {
	root := filepath.Join("testdata", "corrupt")
	v, err := OpenAt(root)
	if err != nil {
		t.Fatalf("the corrupt fixture's config must itself be valid: %v", err)
	}

	// Walking the whole vault fails; it does not return a partial view.
	records, err := v.Walk()
	if err == nil {
		t.Fatalf("walking a corrupt vault returned %d records and no error", len(records))
	}
	if records != nil {
		t.Errorf("walking a corrupt vault returned records alongside the error")
	}

	want := map[string]string{
		"events/bad-rrule.md":             `events/bad-rrule.md:8: invalid rrule "FREQ=NEVER": undefined frequency: NEVER; expected an RFC 5545 rule such as FREQ=WEEKLY;BYDAY=FR`,
		"events/naive-timestamp.md":       `events/naive-timestamp.md:5: timestamp "2026-09-04T14:00:00" has no UTC offset: a stored timestamp must always carry one (for example 2026-09-04T14:00:00+07:00)`,
		"events/unknown-status.md":        `events/unknown-status.md:6: unknown status "maybe" for a event: valid values are scheduled, done, cancelled`,
		"ideas/malformed-yaml.md":         `ideas/malformed-yaml.md:4: mapping values are not allowed in this context`,
		"ideas/missing-touched.md":        `ideas/missing-touched.md:2: an idea must have a touched date; it is what its age is measured from`,
		"ideas/unknown-type.md":           `ideas/unknown-type.md:2: unknown type "reminder": valid types are event, idea, task, note, person, daily`,
		"notes/bad-id.md":                 `notes/bad-id.md:3: id "Not A Valid Id" must be lower-case letters, digits, dot, underscore or hyphen, starting with a letter or digit`,
		"notes/duplicate-key.md":          `notes/duplicate-key.md:5: duplicate key "title", first defined on line 4`,
		"notes/no-frontmatter.md":         `notes/no-frontmatter.md:1: missing frontmatter: file must begin with "---"`,
		"notes/touched-before-created.md": `notes/touched-before-created.md:6: touched 2026-08-01 is before created 2026-09-01`,
		"notes/stray-when.md":             `notes/stray-when.md:6: a note must not have a when: timestamp`,
		"people/stray-rrule.md":           `people/stray-rrule.md:6: a person must not have an rrule`,

		"tasks/missing-touched.md": `tasks/missing-touched.md:2: a task must have a touched date; it is what its follow-up horizon is measured from`,
		"tasks/naive-due.md":       `tasks/naive-due.md:6: timestamp "2026-09-05T17:00:00" has no UTC offset: a stored timestamp must always carry one (for example 2026-09-05T17:00:00+07:00)`,
		"tasks/bad-assignee.md":    `tasks/bad-assignee.md:6: id "Platform Team" must be lower-case letters, digits, dot, underscore or hyphen, starting with a letter or digit`,

		"notes/stray-follow-up.md": `notes/stray-follow-up.md:7: a note must not have a follow_up_after`,
		// The forge cache is derived data living in the source of truth, which
		// is only acceptable while it is validated as strictly as anything the
		// user typed.
		"notes/forge-status-without-url.md":  `notes/forge-status-without-url.md:7: forge_state without a forge_url: there is nothing this status belongs to`,
		"notes/forge-status-without-time.md": `notes/forge-status-without-time.md:7: a cached forge status needs forge_state, forge_checks and forge_checked_at together; a status without the time it was read cannot be told apart from a live one`,
		"notes/bad-forge-url.md":             `notes/bad-forge-url.md:7: "https://github.com/owner/repo": is not a pull request or merge request URL; expected a path like /owner/repo/pull/12 or /group/project/-/merge_requests/12`,
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, dir := range entries {
		if !dir.IsDir() || dir.Name() == BrainDir {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, dir.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			rel := filepath.Join(dir.Name(), f.Name())
			raw, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.ParseRecord(filepath.Join(root, rel), rel, raw)
			if err == nil {
				t.Errorf("%s parsed cleanly but is in the corrupt fixture", rel)
				continue
			}
			seen++
			if got, ok := want[rel]; !ok {
				t.Errorf("%s has no expected message; got %v", rel, err)
			} else if err.Error() != got {
				t.Errorf("%s:\n got: %v\nwant: %s", rel, err, got)
			}
			// Every message is positional: path, line, reason.
			if _, ok := err.(*frontmatter.Error); !ok {
				t.Errorf("%s produced a non-positional error %T: %v", rel, err, err)
			}
		}
	}
	if seen != len(want) {
		t.Errorf("checked %d corrupt files, expected %d", seen, len(want))
	}
}

func TestDuplicateIDIsReportedNotGuessed(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if _, err := Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	v, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	body := "---\ntype: note\nid: twice\ntitle: t\ncreated: 2026-09-01\n---\n\nbody\n"
	for _, name := range []string{"notes/a.md", "notes/b.md"} {
		if err := v.WriteFile(name, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = v.Walk()
	if err == nil {
		t.Fatal("two files claiming one id were accepted")
	}
	dup, ok := err.(*DuplicateIDError)
	if !ok {
		t.Fatalf("got %T: %v", err, err)
	}
	if dup.ID != "twice" || len(dup.Paths) != 2 {
		t.Errorf("got %+v", dup)
	}
	for _, p := range []string{"notes/a.md", "notes/b.md"} {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error does not name %s: %v", p, err)
		}
	}
}

func TestVaultNotFoundNamesResolutionOrder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvVault, "")
	_, err := Open("", dir)
	if err == nil {
		t.Fatal("a vault was found where none exists")
	}
	nf, ok := err.(*NotFoundError)
	if !ok {
		t.Fatalf("got %T: %v", err, err)
	}
	if len(nf.Tried) < 3 {
		t.Errorf("only %d locations reported: %v", len(nf.Tried), nf.Tried)
	}
	msg := err.Error()
	for _, want := range []string{EnvVault, ".brain/config.yml", "brain-axi init", "never created implicitly"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q:\n%s", want, msg)
		}
	}
}

func TestOpenWalksUpFromWorkdir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvVault, "")
	root := filepath.Join(dir, "secondbrain", "vault")
	if _, err := Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(dir, "secondbrain", "some", "nested", "dir")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	v, err := Open("", deep)
	if err != nil {
		t.Fatalf("Open from %s: %v", deep, err)
	}
	if resolved, _ := filepath.EvalSymlinks(v.Root); resolved != mustEval(t, root) {
		t.Errorf("resolved %s, want %s", v.Root, root)
	}
	// The environment wins over the walk.
	other := filepath.Join(dir, "other", "vault")
	if _, err := Init(other, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvVault, other)
	v, err = Open("", deep)
	if err != nil {
		t.Fatal(err)
	}
	if mustEval(t, v.Root) != mustEval(t, other) {
		t.Errorf("$%s ignored: resolved %s", EnvVault, v.Root)
	}
	// An explicit path wins over the environment.
	v, err = Open(root, deep)
	if err != nil {
		t.Fatal(err)
	}
	if mustEval(t, v.Root) != mustEval(t, root) {
		t.Errorf("--vault ignored: resolved %s", v.Root)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInitCreatesSkeletonAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	res, err := Init(root, testConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.AlreadyHere {
		t.Error("a fresh vault reported as already present")
	}
	for _, want := range append([]string{BrainDir + "/", ".gitignore", "README.md"}, dirsWithSlash()...) {
		if !contains(res.Created, want) {
			t.Errorf("init did not report creating %s: %v", want, res.Created)
		}
	}
	for _, d := range append([]string{BrainDir}, RecordDirs...) {
		if st, err := os.Stat(filepath.Join(root, d)); err != nil || !st.IsDir() {
			t.Errorf("%s missing after init", d)
		}
	}
	// Re-running init must not rewrite the user's config.
	custom := Config{Timezone: "Asia/Tokyo", WeekStarts: "sun", NudgeAfter: "30d", BacklogCmd: ""}
	before, err := os.ReadFile(filepath.Join(root, BrainDir, ConfigName))
	if err != nil {
		t.Fatal(err)
	}
	res, err = Init(root, custom, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadyHere {
		t.Error("re-init did not report the vault as already present")
	}
	after, err := os.ReadFile(filepath.Join(root, BrainDir, ConfigName))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("re-init rewrote the config:\n%s", after)
	}
	if _, err := Init(filepath.Join(dir, "bad"), Config{Timezone: "Mars/Olympus", WeekStarts: "mon", NudgeAfter: "14d"}, false); err == nil {
		t.Error("init accepted an unknown timezone")
	}
}

func dirsWithSlash() []string {
	out := make([]string, 0, len(RecordDirs))
	for _, d := range RecordDirs {
		out = append(out, d+"/")
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func TestConfigIsRoundTrippedAndValidated(t *testing.T) {
	cfg := Config{
		Timezone: "Asia/Bangkok", WeekStarts: "sun", NudgeAfter: "21d",
		DueWithin: DefaultDueWithin, DormantAfter: DefaultDormantAfter, BacklogCmd: "echo 3",
	}
	got, err := parseConfig("config.yml", cfg.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got != cfg {
		t.Errorf("got %+v want %+v", got, cfg)
	}
	// The windows added after a vault was written default rather than fail, so
	// a config from an older vault still opens. follow_up_after stays empty on
	// purpose: empty means "use nudge_after", which is what those vaults did.
	older, err := parseConfig("config.yml", []byte("timezone: Asia/Bangkok\nweek_starts: mon\nnudge_after: 21d\n"))
	if err != nil {
		t.Fatal(err)
	}
	if older.DueWithin != DefaultDueWithin || older.DormantAfter != DefaultDormantAfter || older.FollowUpAfter != "" {
		t.Errorf("an older config did not default the new windows: %+v", older)
	}
	bad := map[string]string{
		"timezone: Asia/Bangkok\nweek_starts: mon\nbogus: 1\n": `config.yml:3: unknown configuration key "bogus": known keys are ` + strings.Join(ConfigKeys, ", "),
		"week_starts: mon\n":       `config.yml:1: missing required key "timezone"`,
		"timezone: Asia/Bangkok\n": `config.yml:1: missing required key "week_starts"`,
		"timezone\n":               `config.yml:1: expected key: value, found "timezone"`,
	}
	for in, want := range bad {
		_, err := parseConfig("config.yml", []byte(in))
		if err == nil {
			t.Errorf("%q accepted", in)
			continue
		}
		if err.Error() != want {
			t.Errorf("%q:\n got: %v\nwant: %s", in, err, want)
		}
	}
	// A comment on the value line is stripped, because init writes them.
	got, err = parseConfig("config.yml", []byte("timezone: Asia/Bangkok # the vault zone\nweek_starts: mon\n"))
	if err != nil || got.Timezone != "Asia/Bangkok" {
		t.Errorf("got %+v %v", got, err)
	}
}

func TestGoodVaultParsesEveryRecord(t *testing.T) {
	v := goodVault(t)
	records, err := v.Walk()
	if err != nil {
		t.Fatalf("the good fixture must parse cleanly: %v", err)
	}
	if len(records) != 13 {
		t.Errorf("parsed %d records, want 13", len(records))
	}
	byID := map[string]*Record{}
	for _, r := range records {
		byID[r.ID] = r
	}
	ev := byID["platform-team-sync-20260904"]
	if ev == nil {
		t.Fatal("the Platform team sync event is missing")
	}
	if got := timeref.Format(ev.When); got != "2026-09-04T14:00:00+07:00" {
		t.Errorf("when = %s", got)
	}
	if ev.Title != "Platform team sync" {
		t.Errorf("title = %q", ev.Title)
	}
	if got := timeref.Format(v.End(ev)); got != "2026-09-04T15:00:00+07:00" {
		t.Errorf("end = %s", got)
	}
	if strings.Join(ev.With, ",") != "platform-team" {
		t.Errorf("with = %v", ev.With)
	}
	idea := byID["customer-referral"]
	if idea == nil || idea.Status != "pending" || !idea.HasTouched {
		t.Fatalf("idea = %+v", idea)
	}
	if got := v.AgeDays(idea, at(t, v, "2026-09-01T12:00")); got != 23 {
		t.Errorf("age = %d days, want 23", got)
	}
	if !v.PastHorizon(idea, at(t, v, "2026-09-01T12:00")) {
		t.Error("a 23-day-old idea with a 14d horizon is not past it")
	}
	if v.PastHorizon(byID["shared-vault"], at(t, v, "2026-09-01T12:00")) {
		t.Error("an 8-day-old idea with the 14d default horizon is past it")
	}
	// Wiki-links are read from the body and kept apart from the links: field,
	// because one is prose the user happened to write and the other is a
	// field they maintain.
	if got := strings.Join(byID["referral-pitch-review"].BodyLinks, ","); got != "customer-referral" {
		t.Errorf("body links = %q", got)
	}
	if got := strings.Join(byID["platform-team"].BodyLinks, ","); got != "customer-referral" {
		t.Errorf("person body links = %q", got)
	}
	// A note and a daily carry no status.
	if byID["ci-capacity"].Status != "" || byID["daily-2026-09-01"].Status != "" {
		t.Error("a note or daily record acquired a status")
	}
}

func TestParseLinks(t *testing.T) {
	cases := map[string][]string{
		"see [[a]] and [[b]]":            {"a", "b"},
		"[[a]] twice [[a]]":              {"a"},
		"labelled [[target|shown here]]": {"target"},
		"no links here":                  nil,
		"[[ padded ]]":                   {"padded"},
		"[[]] empty":                     nil,
		"nested [[[a]]]":                 {"a"},
		"code `[[a]]` still counts":      {"a"},
	}
	for body, want := range cases {
		got := ParseLinks(body)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("ParseLinks(%q) = %v, want %v", body, got, want)
		}
	}
}

func TestBodyLinkLineMatchesTheParsedTargetExactly(t *testing.T) {
	v := goodVault(t)
	raw := []byte("---\ntype: note\nid: body-lines\ntitle: body lines\ncreated: 2026-09-01\n---\n\n[[foobar]]\n[[ padded |label]]\n[[foo]]\n")
	r, err := v.ParseRecord("notes/body-lines.md", "notes/body-lines.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.LineOfBodyLink("foo"); got != 10 {
		t.Errorf("foo line = %d, want 10", got)
	}
	if got := r.LineOfBodyLink("padded"); got != 9 {
		t.Errorf("padded line = %d, want 9", got)
	}
}

func TestConfiguredWindowsMustBePositive(t *testing.T) {
	for _, key := range []string{"nudge_after", "follow_up_after", "due_within", "dormant_after"} {
		t.Run(key, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "vault")
			if _, err := Init(root, testConfig(), false); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, BrainDir, ConfigName)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, []byte("\n"+key+": 0m\n")...)
			if key == "nudge_after" || key == "due_within" || key == "dormant_after" {
				lines := strings.Split(string(raw), "\n")
				for i, line := range lines {
					if strings.HasPrefix(line, key+":") && line != key+": 0m" {
						lines[i] = "# " + line
					}
				}
				raw = []byte(strings.Join(lines, "\n"))
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = OpenAt(root)
			if err == nil || !strings.Contains(err.Error(), key+": must be greater than zero") || !strings.Contains(err.Error(), ":") {
				t.Errorf("non-positive %s was not rejected positionally: %v", key, err)
			}
		})
	}
}

// TestAtomicWriteLeavesNoPartialFile: after a write, the destination is either
// the old bytes or the new bytes, and no temporary file is left behind (NFR-3).
func TestAtomicWriteLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if _, err := Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	v, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	rel := "notes/atomic.md"
	first := []byte("---\ntype: note\nid: atomic\ntitle: one\ncreated: 2026-09-01\n---\n\nfirst\n")
	if err := v.WriteFile(rel, first); err != nil {
		t.Fatal(err)
	}
	second := []byte("---\ntype: note\nid: atomic\ntitle: two\ncreated: 2026-09-01\n---\n\nsecond\n")
	if err := v.WriteFile(rel, second); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(second) {
		t.Errorf("got %q", got)
	}
	entries, err := os.ReadDir(filepath.Join(root, "notes"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("a temporary file survived the write: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("notes/ holds %d entries after two writes to one path", len(entries))
	}
}

func TestFreePathAndFreeIDNeverOverwrite(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if _, err := Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	v, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	rel := "notes/taken.md"
	if err := v.WriteFile(rel, []byte("---\ntype: note\nid: taken\ntitle: t\ncreated: 2026-09-01\n---\n\nbody\n")); err != nil {
		t.Fatal(err)
	}
	got, err := v.FreePath(rel)
	if err != nil {
		t.Fatal(err)
	}
	if got != "notes/taken-2.md" {
		t.Errorf("FreePath = %q, want notes/taken-2.md", got)
	}
	id, err := v.FreeID("taken")
	if err != nil {
		t.Fatal(err)
	}
	if id != "taken-2" {
		t.Errorf("FreeID = %q, want taken-2", id)
	}
	if id, err := v.FreeID("untaken"); err != nil || id != "untaken" {
		t.Errorf("FreeID(untaken) = %q %v", id, err)
	}
}

func TestValidateID(t *testing.T) {
	for _, ok := range []string{"a", "a1", "platform-team-sync-20260904", "daily-2026-09-01", "a.b_c-d", "0start"} {
		if err := ValidateID(ok); err != nil {
			t.Errorf("ValidateID(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "A", "Not Valid", "-leading", ".leading", "has space", "Zürich", "with/slash", "with:colon"} {
		if err := ValidateID(bad); err == nil {
			t.Errorf("ValidateID(%q) accepted", bad)
		}
	}
}

func TestAppendNoteLandsInTodaysDailyFile(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if _, err := Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	v, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	now := at(t, v, "2026-09-01T09:12")
	rel, id, created, err := v.AppendNote(now, "ask the Zürich datacentre team about CI capacity")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "daily/2026-09-01.md" || id != "daily-2026-09-01" || !created {
		t.Errorf("rel=%q id=%q created=%v", rel, id, created)
	}
	// A second note the same day appends rather than replacing.
	later := at(t, v, "2026-09-01T14:30")
	rel2, id2, created2, err := v.AppendNote(later, "review the Kraków rollout schedule next week")
	if err != nil {
		t.Fatal(err)
	}
	if rel2 != rel || id2 != id || created2 {
		t.Errorf("second note went to rel=%q created=%v", rel2, created2)
	}
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	want := "---\ntype: daily\nid: daily-2026-09-01\ntitle: 2026-09-01\ncreated: 2026-09-01\ntouched: 2026-09-01\n---\n\n- 09:12 +07:00 ask the Zürich datacentre team about CI capacity\n- 14:30 +07:00 review the Kraków rollout schedule next week\n"
	if string(raw) != want {
		t.Errorf("got:\n%q\nwant:\n%q", raw, want)
	}
	// The file it wrote is a valid record.
	rec, err := v.ParseRecord(filepath.Join(root, rel), rel, raw)
	if err != nil {
		t.Fatalf("AppendNote wrote an invalid record: %v", err)
	}
	if rec.Kind != KindDaily {
		t.Errorf("kind = %q", rec.Kind)
	}
	// A note on the next day starts a new file.
	tomorrow := at(t, v, "2026-09-02T08:00")
	rel3, _, created3, err := v.AppendNote(tomorrow, "a new day")
	if err != nil {
		t.Fatal(err)
	}
	if rel3 != "daily/2026-09-02.md" || !created3 {
		t.Errorf("rel=%q created=%v", rel3, created3)
	}
}

func TestAppendNoteRefusesToBuryCorruption(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if _, err := Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	v, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	rel := "daily/2026-09-01.md"
	corrupt := []byte("---\ntype: daily\nid: daily-2026-09-01\ncreated: not-a-date\n---\n\n- old\n")
	if err := v.WriteFile(rel, corrupt); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := v.AppendNote(at(t, v, "2026-09-01T09:00"), "new note"); err == nil {
		t.Fatal("appending to a corrupt daily file was accepted")
	}
	got, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(corrupt) {
		t.Errorf("the corrupt file was modified:\n%s", got)
	}
}

func TestBuildRecordsProduceValidFiles(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if _, err := Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	v, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	created := v.Zone.DateOf(at(t, v, "2026-09-01T10:00"))
	span, _ := timeref.ParseSpan("60m")

	relEv, docEv, err := v.BuildEvent(NewEvent{
		ID: "platform-team-sync-20260904", Title: "Platform team sync",
		When: at(t, v, "2026-09-04T14:00"), Duration: span,
		With: []string{"platform-team"}, Created: created,
		Body: "Decide how expired schedules are handled.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if relEv != "events/2026-09-04-platform-team-sync.md" {
		t.Errorf("event path = %q", relEv)
	}
	if err := v.Save(relEv, docEv); err != nil {
		t.Fatal(err)
	}
	// An event's filename is the folded slug of its title, so a title carrying
	// diacritics still yields an ASCII path. Built and not saved, because the
	// record count of this vault is asserted below.
	relAccented, _, err := v.BuildEvent(NewEvent{
		ID: "sao-paulo-rollout-20260904", Title: "São Paulo rollout review",
		When: at(t, v, "2026-09-04T16:00"), Duration: span, Created: created,
	})
	if err != nil {
		t.Fatal(err)
	}
	if relAccented != "events/2026-09-04-sao-paulo-rollout-review.md" {
		t.Errorf("accented event path = %q", relAccented)
	}

	relIdea, docIdea, err := v.BuildIdea(NewIdea{ID: "customer-referral", Title: "customer referral program", Created: created})
	if err != nil {
		t.Fatal(err)
	}
	if relIdea != "ideas/customer-referral.md" {
		t.Errorf("idea path = %q", relIdea)
	}
	if err := v.Save(relIdea, docIdea); err != nil {
		t.Fatal(err)
	}

	relPerson, docPerson, err := v.BuildPerson(NewPerson{ID: "platform-team", Title: "Platform team", Created: created})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Save(relPerson, docPerson); err != nil {
		t.Fatal(err)
	}

	relNote, docNote, err := v.BuildNote(NewNote{ID: "ci-capacity", Title: "ask about CI capacity", Created: created, Tags: []string{"infra"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Save(relNote, docNote); err != nil {
		t.Fatal(err)
	}

	records, err := v.Walk()
	if err != nil {
		t.Fatalf("the records add wrote must parse: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("wrote 4 records, walked %d", len(records))
	}
	raw, err := os.ReadFile(filepath.Join(root, relEv))
	if err != nil {
		t.Fatal(err)
	}
	want := "---\ntype: event\nid: platform-team-sync-20260904\ntitle: Platform team sync\nwhen: 2026-09-04T14:00:00+07:00\nduration: 60m\nstatus: scheduled\ncreated: 2026-09-01\nwith: [platform-team]\n---\n\nDecide how expired schedules are handled.\n"
	if string(raw) != want {
		t.Errorf("event file:\n got %q\nwant %q", raw, want)
	}
	// Status vocabularies are enforced at build time too.
	if _, _, err := v.BuildEvent(NewEvent{ID: "x", Title: "x", When: at(t, v, "2026-09-04T14:00"), Status: "maybe", Created: created}); err == nil {
		t.Error("BuildEvent accepted an unknown status")
	}
	if _, _, err := v.BuildIdea(NewIdea{ID: "y", Title: "y", Status: "maybe", Created: created}); err == nil {
		t.Error("BuildIdea accepted an unknown status")
	}
	if _, _, err := v.BuildEvent(NewEvent{ID: "Bad Id", Title: "x", When: at(t, v, "2026-09-04T14:00"), Created: created}); err == nil {
		t.Error("BuildEvent accepted an invalid id")
	}
	if _, _, err := v.BuildEvent(NewEvent{ID: "z", Title: "x", When: at(t, v, "2026-09-04T14:00"), RRule: "FREQ=NEVER", Created: created}); err == nil {
		t.Error("BuildEvent accepted an invalid rrule")
	}
}

// TestWalkReadsEveryDirectory is the guarantee behind NFR-4: no command sees a
// partial vault, so corruption anywhere is visible from anywhere. Scoping a
// walk to the directory a query "needs" would let `today` exit zero on a vault
// with a broken idea in it.
func TestWalkReadsEveryDirectory(t *testing.T) {
	v := goodVault(t)
	records, err := v.Walk()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range records {
		seen[filepath.Dir(r.Rel)] = true
	}
	for _, dir := range RecordDirs {
		if !seen[dir] {
			t.Errorf("a walk did not read %s/", dir)
		}
	}
}

// TestWalkIsMemoisedForOneInvocationAndDroppedByAWrite: a single command asks
// the same question more than once - a capture needs a free id, a free path and
// an overlap check - so the walk is paid for once. It is not a cache: a write
// drops it, so nothing can read a view from before its own change.
func TestWalkIsMemoisedForOneInvocationAndDroppedByAWrite(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if _, err := Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	v, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := v.Walk()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 {
		t.Fatalf("a fresh vault walked %d records", len(first))
	}
	// A file written behind the tool's back is not seen, because the walk was
	// already paid for. That is the point: one command, one view.
	if err := os.WriteFile(filepath.Join(root, "notes", "sneaky.md"),
		[]byte("---\ntype: note\nid: sneaky\ntitle: t\ncreated: 2026-09-01\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := v.Walk()
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("the memoised walk was not reused: %d records", len(again))
	}
	// The tool's own write drops it, so the next read sees everything.
	if err := v.WriteFile("notes/own.md",
		[]byte("---\ntype: note\nid: own\ntitle: t\ncreated: 2026-09-01\n---\n\nbody\n")); err != nil {
		t.Fatal(err)
	}
	after, err := v.Walk()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Errorf("after a write the walk saw %d records, want 2", len(after))
	}
	// Removing does the same.
	if err := v.Remove("notes/own.md"); err != nil {
		t.Fatal(err)
	}
	after, err = v.Walk()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Errorf("after a removal the walk saw %d records, want 1", len(after))
	}
	// A fresh Vault value always starts from the files.
	fresh, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	records, err := fresh.Walk()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Errorf("a fresh vault saw %d records, want 1", len(records))
	}
}
