package vault

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/frontmatter"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
)

// batchVault is an empty vault with a known clock, for the batch suite.
func batchVault(t *testing.T) *Vault {
	t.Helper()
	root := filepath.Join(t.TempDir(), "vault")
	if _, err := Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	v, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func batchNow(t *testing.T, v *Vault) time.Time {
	t.Helper()
	now, err := v.Zone.Normalise("2026-09-02T09:30")
	if err != nil {
		t.Fatal(err)
	}
	return now
}

// markdownFiles lists every record file in the vault, so a test can prove that
// a refused batch changed nothing.
func markdownFiles(t *testing.T, v *Vault) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(v.Root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		rel, err := filepath.Rel(v.Root, p)
		if err != nil {
			return err
		}
		if rel == "README.md" {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

const wholeMeeting = `# Platform team meeting, resolved by the agent from a raw note.
ideas:
  - title: cache the schedule expiry lookup
    body: Might not be worth it until the table is bigger.
    nudge_after: 30d
tasks:
  - title: review CI capacity again
    due: 2026-09-05T17:00
    follow_up_after: 3d
delegated:
  - title: migrate the staging database
    assignee: platform-team
    follow_up_after: 14d
notes:
  - text: ask infrastructure whether CI capacity is per project
  - text: review the Kraków rollout schedule next week
events:
  - title: Platform team follow-up
    when: 2026-09-09T14:00
    duration: 60m
    with: [platform-team]
`

// TestBatchStoresEverySectionInOnePass is the whole feature: one messy meeting
// resolved by the agent into one file, and every part of it captured.
func TestBatchStoresEverySectionInOnePass(t *testing.T) {
	v := batchVault(t)
	now := batchNow(t, v)

	batch, err := v.ParseBatch("meeting.yml", []byte(wholeMeeting), now)
	if err != nil {
		t.Fatalf("parsing a good batch: %v", err)
	}
	// Nothing is written until Write is called: validation is what makes the
	// ingest atomic.
	if files := markdownFiles(t, v); len(files) != 0 {
		t.Errorf("parsing alone wrote %v", files)
	}
	if err := v.WriteBatch(batch); err != nil {
		t.Fatalf("writing a good batch: %v", err)
	}

	bySection := map[string][]BatchEntry{}
	for _, e := range batch.Entries {
		bySection[e.Section] = append(bySection[e.Section], e)
	}
	for _, section := range BatchSections {
		if len(bySection[section]) != 1 {
			t.Errorf("section %q produced %d entries, want 1", section, len(bySection[section]))
		}
	}

	records, err := v.Walk()
	if err != nil {
		t.Fatalf("the batch left the vault unparseable: %v", err)
	}
	byID := map[string]*Record{}
	for _, r := range records {
		byID[r.ID] = r
	}

	idea := byID["cache-the-schedule-expiry-lookup"]
	if idea == nil || idea.Kind != KindIdea {
		t.Fatalf("the idea was not stored as an idea: %+v", idea)
	}
	if got := v.Horizon(idea).String(); got != "30d" {
		t.Errorf("the idea's nudge horizon is %s, want 30d", got)
	}

	task := byID["review-ci-capacity-again"]
	if task == nil || task.Kind != KindTask {
		t.Fatalf("the task was not stored as a task: %+v", task)
	}
	if !task.HasDue {
		t.Error("the task lost its due date")
	} else if got := task.Due.Format(time.RFC3339); got != "2026-09-05T17:00:00+07:00" {
		t.Errorf("due = %s, want the naive time normalised into the vault zone", got)
	}
	if task.Status != "open" {
		t.Errorf("an undelegated task defaulted to %q, want open", task.Status)
	}

	delegated := byID["migrate-the-staging-database"]
	if delegated == nil || delegated.Assignee != "platform-team" {
		t.Fatalf("the delegated task lost its assignee: %+v", delegated)
	}
	if delegated.Status != "waiting" {
		t.Errorf("a delegated task defaulted to %q, want waiting: the user is not the one doing it", delegated.Status)
	}
	if got := v.Horizon(delegated).String(); got != "14d" {
		t.Errorf("follow-up horizon is %s, want 14d", got)
	}

	daily := byID[DailyID(v.Zone.DateOf(now))]
	if daily == nil {
		t.Fatal("the notes did not reach a daily file")
	}
	for _, want := range []string{"ask infrastructure whether CI capacity is per project", "review the Kraków rollout schedule next week"} {
		if !strings.Contains(daily.Body, want) {
			t.Errorf("the daily file is missing %q:\n%s", want, daily.Body)
		}
	}

	event := byID["platform-team-follow-up-20260909"]
	if event == nil || !event.HasWhen {
		t.Fatalf("the event was not stored: %+v", event)
	}
	if got := event.When.Format(time.RFC3339); got != "2026-09-09T14:00:00+07:00" {
		t.Errorf("when = %s, want the naive time normalised into the vault zone", got)
	}
}

func TestBatchEchoUsesTheTaskFollowUpDefault(t *testing.T) {
	v := batchVault(t)
	v.NudgeAfter, _ = timeref.ParseSpan("14d")
	v.FollowUpAfter, _ = timeref.ParseSpan("30d")
	batch, err := v.ParseBatch("meeting.yml", []byte("tasks:\n  - title: follow this up\n"), batchNow(t, v))
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Entries) != 1 || !strings.Contains(batch.Entries[0].Detail, "follow up after 30d") {
		t.Errorf("batch echo = %+v, want the 30d task default", batch.Entries)
	}
}

// TestBatchNotesBecomeOneWriteOfTheDailyFile: five notes must not be five
// read-modify-write cycles of the same file, which would be five chances to
// fail halfway through one section.
func TestBatchNotesBecomeOneWriteOfTheDailyFile(t *testing.T) {
	v := batchVault(t)
	now := batchNow(t, v)
	src := "notes:\n  - text: one\n  - text: two\n  - text: three\n"
	batch, err := v.ParseBatch("b.yml", []byte(src), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Entries) != 1 {
		t.Fatalf("three notes produced %d entries, want one daily file", len(batch.Entries))
	}
	if err := v.WriteBatch(batch); err != nil {
		t.Fatal(err)
	}
	r, err := v.Find(DailyID(v.Zone.DateOf(now)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"one", "two", "three"} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("the daily file lost %q:\n%s", want, r.Body)
		}
	}
}

// TestBatchAppendsToAnExistingDailyFile: a second batch on the same day adds to
// the day rather than replacing it.
func TestBatchAppendsToAnExistingDailyFile(t *testing.T) {
	v := batchVault(t)
	now := batchNow(t, v)
	for _, text := range []string{"first batch", "second batch"} {
		batch, err := v.ParseBatch("b.yml", []byte("notes:\n  - text: "+text+"\n"), now)
		if err != nil {
			t.Fatal(err)
		}
		if err := v.WriteBatch(batch); err != nil {
			t.Fatal(err)
		}
	}
	r, err := v.Find(DailyID(v.Zone.DateOf(now)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first batch", "second batch"} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("the daily file lost %q:\n%s", want, r.Body)
		}
	}
}

// TestMalformedBatchWritesNothing is the atomicity guarantee the user was
// promised: a bad entry anywhere means the whole batch is refused and the vault
// is untouched, however many good entries came before it.
func TestMalformedBatchWritesNothing(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
		line int
	}{
		{
			name: "a delegated entry with no assignee",
			src: `ideas:
  - title: a perfectly good idea
delegated:
  - title: handed to nobody
    follow_up_after: 7d
`,
			want: `a delegated entry needs an "assignee"`,
			line: 4,
		},
		{
			name: "an event with a relative date",
			src: `tasks:
  - title: a perfectly good task
events:
  - title: standup
    when: thursday this week
`,
			want: `when: cannot read "thursday this week" as a timestamp`,
			line: 5,
		},
		{
			name: "an event with no when",
			src: `ideas:
  - title: fine
events:
  - title: no time given
`,
			want: `an event entry needs a "when"`,
			line: 4,
		},
		{
			name: "a field the section does not have",
			src: `ideas:
  - title: fine
  - title: also fine
    assignee: platform-team
`,
			want: `unknown field "assignee"`,
			line: 4,
		},
		{
			name: "an unknown section",
			src: `ideas:
  - title: fine
reminders:
  - title: not a section
`,
			want: `unknown batch section "reminders"`,
			line: 3,
		},
		{
			name: "a duplicate section silently dropping the first",
			src: `ideas:
  - title: first
tasks:
  - title: fine
ideas:
  - title: second
`,
			want: `duplicate section "ideas"`,
			line: 5,
		},
		{
			name: "a section that is not a list",
			src: `ideas: just one thought
`,
			want: `section "ideas" must be a list of entries`,
			line: 1,
		},
		{
			name: "an entry with no title",
			src: `ideas:
  - title: fine
  - body: a body and nothing to call it
`,
			want: `this entry needs a "title"`,
			line: 3,
		},
		{
			name: "a horizon that is not a duration",
			src: `tasks:
  - title: fine
  - title: also fine
    follow_up_after: soon
`,
			want: `follow_up_after: cannot read "soon" as a duration`,
			line: 4,
		},
		{
			name: "two entries claiming one explicit id",
			src: `ideas:
  - title: first
    id: same-id
  - title: second
    id: same-id
`,
			want: `id "same-id" is already used`,
			line: 5,
		},
		{
			name: "a status outside the closed vocabulary",
			src: `tasks:
  - title: fine
    status: maybe
`,
			want: "unknown status",
			line: 3,
		},
		{
			name: "a batch with no entries at all",
			src:  "ideas: []\n",
			want: "no entries in any of",
			line: 1,
		},
		{
			// yaml.v3 attributes an indentation error to the line it gave up on
			// rather than the line that was mistyped. Its own position is still
			// better than an invented one, and the message names the problem.
			name: "broken YAML",
			src: `ideas:
  - title: fine
   body: bad indent
`,
			want: "did not find expected '-' indicator",
			line: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := batchVault(t)
			now := batchNow(t, v)
			before := markdownFiles(t, v)

			batch, err := v.ParseBatch("meeting.yml", []byte(tc.src), now)
			if err == nil {
				t.Fatalf("a malformed batch was accepted, producing %d entries", len(batch.Entries))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain %q", err, tc.want)
			}
			// Every batch failure is positional, exactly as a malformed record
			// is, so the agent can point at the line to fix.
			var perr *frontmatter.Error
			if !errors.As(err, &perr) {
				t.Fatalf("error %v is not a path:line: reason error", err)
			}
			if perr.Path != "meeting.yml" {
				t.Errorf("the error names %q, want the batch file", perr.Path)
			}
			if perr.Line != tc.line {
				t.Errorf("the error is on line %d, want %d: %v", perr.Line, tc.line, err)
			}
			if after := markdownFiles(t, v); len(after) != len(before) {
				t.Errorf("a refused batch wrote files: before %v, after %v", before, after)
			}
		})
	}
}

// TestMalformedBatchWritesNothingEvenWithManyGoodEntriesFirst: the refusal is
// not "stop at the bad one", it is "write none of them".
func TestMalformedBatchWritesNothingEvenWithManyGoodEntriesFirst(t *testing.T) {
	v := batchVault(t)
	now := batchNow(t, v)
	src := `ideas:
  - title: idea one
  - title: idea two
  - title: idea three
tasks:
  - title: task one
  - title: task two
notes:
  - text: a note
events:
  - title: a good event
    when: 2026-09-09T14:00
  - title: the one bad entry
    when: 2026-09-09T14:00
    rrule: FREQ=NEVER
`
	if _, err := v.ParseBatch("meeting.yml", []byte(src), now); err == nil {
		t.Fatal("a batch with one bad entry was accepted")
	}
	if files := markdownFiles(t, v); len(files) != 0 {
		t.Errorf("seven good entries were written despite the eighth being bad: %v", files)
	}
	records, err := v.Walk()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("the vault holds %d records after a refused batch", len(records))
	}
}

// TestBatchNeverCollidesWithItselfOrTheVault: FreeID and FreePath only know
// what is on disk, and nothing in a batch is yet, so the batch carries its own
// running set of choices.
func TestBatchNeverCollidesWithItselfOrTheVault(t *testing.T) {
	v := batchVault(t)
	now := batchNow(t, v)

	first, err := v.ParseBatch("a.yml", []byte("ideas:\n  - title: the same thought\n"), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.WriteBatch(first); err != nil {
		t.Fatal(err)
	}

	second, err := v.ParseBatch("b.yml", []byte("ideas:\n  - title: the same thought\n  - title: the same thought\n"), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.WriteBatch(second); err != nil {
		t.Fatal(err)
	}
	records, err := v.Walk()
	if err != nil {
		t.Fatalf("colliding titles left the vault unparseable: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("three captures of one title produced %d records", len(records))
	}
	ids, paths := map[string]bool{}, map[string]bool{}
	for _, r := range records {
		if ids[r.ID] {
			t.Errorf("id %q was reused", r.ID)
		}
		if paths[r.Rel] {
			t.Errorf("path %q was reused", r.Rel)
		}
		ids[r.ID], paths[r.Rel] = true, true
	}
}

// TestBatchWriteFailurePartwayThroughNamesEveryFile is the honest half of the
// atomicity story. A filesystem offers no transaction across several files, so
// when the disk fails between two writes the tool reports exactly which landed
// and which did not, and deletes nothing.
func TestBatchWriteFailurePartwayThroughNamesEveryFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so a write cannot be forced to fail this way")
	}
	v := batchVault(t)
	now := batchNow(t, v)

	// Ideas are written before tasks, because entries are ordered by section.
	src := `ideas:
  - title: this one lands
tasks:
  - title: this one cannot be written
  - title: nor can this one
`
	batch, err := v.ParseBatch("meeting.yml", []byte(src), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Entries) != 3 {
		t.Fatalf("expected three entries, got %d", len(batch.Entries))
	}

	// Validation is complete, so the only failure left is the filesystem: take
	// the tasks directory away between validating and writing.
	tasksDir := filepath.Join(v.Root, TasksDir)
	if err := os.Chmod(tasksDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tasksDir, 0o755) })

	err = v.WriteBatch(batch)
	if err == nil {
		t.Fatal("a write into an unwritable directory reported success")
	}
	var wErr *BatchWriteError
	if !errors.As(err, &wErr) {
		t.Fatalf("error %v is not a BatchWriteError", err)
	}
	if len(wErr.Written) != 1 || !strings.HasPrefix(wErr.Written[0], IdeasDir) {
		t.Errorf("Written = %v, want exactly the one idea that landed", wErr.Written)
	}
	if len(wErr.Pending) != 2 {
		t.Errorf("Pending = %v, want the two tasks that did not", wErr.Pending)
	}
	// The message must name every file on both sides, so the user can see
	// the real state rather than infer it.
	msg := wErr.Error()
	for _, want := range append(append([]string{}, wErr.Written...), wErr.Pending...) {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not name %q:\n%s", want, msg)
		}
	}
	for _, want := range []string{"was not rolled back", "nothing was deleted"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not say %q:\n%s", want, msg)
		}
	}

	// Nothing was deleted, and the vault still parses: a partial batch is a
	// smaller vault, never a broken one.
	if err := os.Chmod(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	records, err := v.Walk()
	if err != nil {
		t.Fatalf("a partial batch left the vault unparseable: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("the vault holds %d records, want the one that was written", len(records))
	}
}
