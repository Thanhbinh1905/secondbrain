package vault

import (
	"strings"
	"testing"

	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
)

// TestTaskIsAFirstClassKind: the fourth record kind has to be valid everywhere
// a kind is asked about, or a hand-written task file fails to parse.
func TestTaskIsAFirstClassKind(t *testing.T) {
	if DefaultDirFor(KindTask) != TasksDir {
		t.Errorf("a task has no directory, so ParseRecord would reject the type")
	}
	if !allowed(RecordDirs, TasksDir) {
		t.Errorf("tasks/ is not walked, so a task would be invisible to every query")
	}
	if got := StatusesFor(KindTask); strings.Join(got, ",") != "open,waiting,done,dropped" {
		t.Errorf("task statuses = %v", got)
	}
	found := false
	for _, k := range Kinds {
		if k == KindTask {
			found = true
		}
	}
	if !found {
		t.Error("KindTask is not in Kinds, so the unknown-type error would not list it")
	}
}

// TestTaskFollowUpHorizon is the whole point of the record kind: something
// handed over and not checked has to become visible on its own.
func TestTaskFollowUpHorizon(t *testing.T) {
	v := goodVault(t)
	records, err := v.Walk()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*Record{}
	for _, r := range records {
		byID[r.ID] = r
	}

	delegated := byID["migrate-staging-db"]
	if delegated == nil {
		t.Fatal("the delegated fixture is missing")
	}
	if delegated.Assignee != "platform-team" {
		t.Errorf("assignee = %q", delegated.Assignee)
	}
	if !delegated.HasFollowUp || delegated.FollowUpAfter.String() != "14d" {
		t.Errorf("follow_up_after = %v", delegated.FollowUpAfter)
	}
	// A task's horizon comes from follow_up_after, not from nudge_after, and
	// not from the vault default when it sets its own.
	if got := v.Horizon(delegated).String(); got != "14d" {
		t.Errorf("Horizon = %s, want the task's own 14d", got)
	}

	cases := []struct {
		now  string
		age  int
		past bool
	}{
		{"2026-08-05T12:00", 0, false},
		{"2026-08-19T12:00", 14, false}, // exactly at the horizon is not past it
		{"2026-08-20T12:00", 15, true},
		{"2026-09-02T12:00", 28, true}, // three weeks unchecked: impossible to miss
	}
	for _, tc := range cases {
		now := at(t, v, tc.now)
		if got := v.AgeDays(delegated, now); got != tc.age {
			t.Errorf("at %s age = %d, want %d", tc.now, got, tc.age)
		}
		if got := v.PastHorizon(delegated, now); got != tc.past {
			t.Errorf("at %s PastHorizon = %v, want %v", tc.now, got, tc.past)
		}
	}
}

// TestTaskWithoutItsOwnHorizonFallsBackToTheVaultDefault, exactly as an idea
// does. One mechanism, two spellings.
func TestTaskWithoutItsOwnHorizonFallsBackToTheVaultDefault(t *testing.T) {
	v := batchVault(t)
	created, err := timeref.ParseDateOnly("2026-09-01")
	if err != nil {
		t.Fatal(err)
	}
	rel, doc, err := v.BuildTask(NewTask{ID: "plain", Title: "a task with no horizon of its own", Created: created})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Save(rel, doc); err != nil {
		t.Fatal(err)
	}
	r, err := v.Find("plain")
	if err != nil {
		t.Fatal(err)
	}
	if r.HasFollowUp {
		t.Error("a task with no follow_up_after claims one")
	}
	if got := v.Horizon(r).String(); got != v.NudgeAfter.String() {
		t.Errorf("Horizon = %s, want the vault default %s", got, v.NudgeAfter)
	}
}

// TestBuildTaskDefaultsWaitingWhenDelegated: a task handed to somebody is not
// on the user's own desk, and calling it "open" would put it there.
func TestBuildTaskDefaultsWaitingWhenDelegated(t *testing.T) {
	v := batchVault(t)
	created, err := timeref.ParseDateOnly("2026-09-01")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, assignee, explicit, want string
	}{
		{"their own", "", "", "open"},
		{"delegated", "platform-team", "", "waiting"},
		{"delegated but explicitly open", "platform-team", "open", "open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, doc, err := v.BuildTask(NewTask{
				ID: "t-" + strings.ReplaceAll(tc.name, " ", "-"), Title: "x",
				Assignee: tc.assignee, Status: tc.explicit, Created: created,
			})
			if err != nil {
				t.Fatal(err)
			}
			got, ok, err := doc.String("status")
			if err != nil || !ok {
				t.Fatalf("status: %v %v", got, err)
			}
			if got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildTaskRefusesAStatusOutsideTheVocabulary keeps the vocabulary closed
// at the write path as well as the read path.
func TestBuildTaskRefusesAStatusOutsideTheVocabulary(t *testing.T) {
	v := batchVault(t)
	created, err := timeref.ParseDateOnly("2026-09-01")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = v.BuildTask(NewTask{ID: "x", Title: "x", Status: "maybe", Created: created})
	if err == nil {
		t.Fatal("an unknown task status was accepted")
	}
	for _, want := range []string{"maybe", "open, waiting, done, dropped"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not mention %q", err, want)
		}
	}
}

// TestForgeCacheRoundTripsThroughFrontmatter: the cache lives in the record's
// own file, is read back exactly as written, and always carries its timestamp.
func TestForgeCacheRoundTripsThroughFrontmatter(t *testing.T) {
	v := goodVault(t)
	r, err := v.Find("migrate-staging-db")
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasForge {
		t.Fatal("the fixture's forge link was not read")
	}
	if r.Forge.URL != "https://git.example.com/platform/service/-/merge_requests/42" {
		t.Errorf("url = %q", r.Forge.URL)
	}
	if !r.Forge.HasStatus {
		t.Fatal("the cached status was not read")
	}
	if r.Forge.State != "open" || r.Forge.Checks != "pending" {
		t.Errorf("state = %q, checks = %q", r.Forge.State, r.Forge.Checks)
	}
	if got := timeref.Format(r.Forge.CheckedAt); got != "2026-08-20T10:15:00+07:00" {
		t.Errorf("checked_at = %s, want the stored instant in the vault zone", got)
	}
	// The cache is ordinary frontmatter, so a hand edit removes it and costs
	// nothing but the next refresh.
	doc := r.Doc()
	for _, key := range []string{"forge_state", "forge_checks", "forge_checked_at"} {
		if !doc.Delete(key) {
			t.Errorf("%s was not present to delete", key)
		}
	}
	data, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	after, err := v.ParseRecord(r.Path, r.Rel, data)
	if err != nil {
		t.Fatalf("a record with its cache deleted by hand no longer parses: %v", err)
	}
	if !after.HasForge || after.Forge.HasStatus {
		t.Errorf("after deleting the cache: HasForge=%v HasStatus=%v, want the link kept and the status gone",
			after.HasForge, after.Forge.HasStatus)
	}
}
