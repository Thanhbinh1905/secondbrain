package vault

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
)

// dstVault opens a fresh vault configured for the given zone, ready for a
// weekly Sunday series to be added by the caller.
func dstVault(t *testing.T, zone string) *Vault {
	t.Helper()
	root := filepath.Join(t.TempDir(), "vault")
	cfg := Config{Timezone: zone, WeekStarts: "sun", NudgeAfter: "14d"}
	if _, err := Init(root, cfg, false); err != nil {
		t.Fatal(err)
	}
	v, err := OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// addWeeklySundayEvent builds and saves a weekly Sunday series starting at
// when, and returns the parsed record.
func addWeeklySundayEvent(t *testing.T, v *Vault, id, whenStamp string) *Record {
	t.Helper()
	when, err := v.Zone.Normalise(whenStamp)
	if err != nil {
		t.Fatalf("Normalise(%q): %v", whenStamp, err)
	}
	created := v.Zone.DateOf(when)
	rel, doc, err := v.BuildEvent(NewEvent{
		ID: id, Title: id, When: when, RRule: "FREQ=WEEKLY;BYDAY=SU", Created: created,
	})
	if err != nil {
		t.Fatalf("BuildEvent: %v", err)
	}
	if err := v.Save(rel, doc); err != nil {
		t.Fatal(err)
	}
	records, err := v.Walk()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("record %s not found after save", id)
	return nil
}

// TestExpandRejectsADSTGapOccurrence: a weekly Sunday 02:30 series recurring
// onto the US spring-forward Sunday (2026-03-08, the same date
// TestDSTGapIsRejectedNotShifted pins) must fail loudly rather than silently
// land on 01:30, the reading rrule-go's raw time.Date would produce.
func TestExpandRejectsADSTGapOccurrence(t *testing.T) {
	v := dstVault(t, "America/New_York")
	r := addWeeklySundayEvent(t, v, "gap-series", "2026-03-01T02:30")

	from, err := v.Zone.Normalise("2026-03-01T00:00")
	if err != nil {
		t.Fatal(err)
	}
	to, err := v.Zone.Normalise("2026-03-31T00:00")
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.Expand(r, from, to)
	if err == nil {
		t.Fatal("a DST-gap occurrence was accepted")
	}
	msg := err.Error()
	for _, want := range []string{
		"events/2026-03-01-gap-series.md", "2026-03-08", "02:30:00", "America/New_York",
		"exceptions:",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "does not exist") {
		t.Errorf("error does not name the reading as nonexistent:\n%s", msg)
	}
}

// TestExpandRejectsADSTAmbiguousOccurrence: a weekly Sunday 01:30 series
// recurring onto the US fall-back Sunday (2026-11-01, the same date
// TestDSTAmbiguityIsRejectedNotGuessed pins) must fail loudly, naming both
// candidate instants, rather than silently picking one of them.
func TestExpandRejectsADSTAmbiguousOccurrence(t *testing.T) {
	v := dstVault(t, "America/New_York")
	r := addWeeklySundayEvent(t, v, "ambiguous-series", "2026-10-25T01:30")

	from, err := v.Zone.Normalise("2026-10-25T00:00")
	if err != nil {
		t.Fatal(err)
	}
	to, err := v.Zone.Normalise("2026-11-08T00:00")
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.Expand(r, from, to)
	if err == nil {
		t.Fatal("an ambiguous DST occurrence was accepted")
	}
	msg := err.Error()
	for _, want := range []string{
		"events/2026-10-25-ambiguous-series.md", "2026-11-01", "01:30:00", "America/New_York",
		"2026-11-01T01:30:00-04:00", "2026-11-01T01:30:00-05:00",
		"exceptions:",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "ambiguous") {
		t.Errorf("error does not name the reading as ambiguous:\n%s", msg)
	}
}

// TestExpandUnaffectedByDSTInTheVaultsDefaultZone is the control: a normal
// weekly series in Asia/Bangkok, the fixture zone and a zone with no
// DST, expands every occurrence with no error, exactly as before.
func TestExpandUnaffectedByDSTInTheVaultsDefaultZone(t *testing.T) {
	v := dstVault(t, "Asia/Bangkok")
	r := addWeeklySundayEvent(t, v, "fixed-offset-series", "2026-03-01T14:00")

	from, err := v.Zone.Normalise("2026-03-01T00:00")
	if err != nil {
		t.Fatal(err)
	}
	to, err := v.Zone.Normalise("2026-03-31T00:00")
	if err != nil {
		t.Fatal(err)
	}
	occs, err := v.Expand(r, from, to)
	if err != nil {
		t.Fatalf("Expand in a DST-free zone failed: %v", err)
	}
	want := []string{
		"2026-03-01T14:00:00+07:00",
		"2026-03-08T14:00:00+07:00",
		"2026-03-15T14:00:00+07:00",
		"2026-03-22T14:00:00+07:00",
		"2026-03-29T14:00:00+07:00",
	}
	if len(occs) != len(want) {
		t.Fatalf("got %d occurrences, want %d", len(occs), len(want))
	}
	for i, o := range occs {
		if got := timeref.Format(o.Start); got != want[i] {
			t.Errorf("occurrence %d = %s, want %s", i, got, want[i])
		}
	}
}
