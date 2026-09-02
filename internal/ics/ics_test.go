package ics

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestExportGolden(t *testing.T) {
	v, err := vault.OpenAt(filepath.Join("..", "vault", "testdata", "good"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := v.Walk()
	if err != nil {
		t.Fatal(err)
	}
	now, err := v.Zone.Normalise("2026-09-02T12:00")
	if err != nil {
		t.Fatal(err)
	}
	data, err := Export(v, records, now)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	path := filepath.Join("testdata", "brain.ics.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file (run go test ./internal/ics -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("export drifted\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestExportStructure(t *testing.T) {
	v, err := vault.OpenAt(filepath.Join("..", "vault", "testdata", "good"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := v.Walk()
	if err != nil {
		t.Fatal(err)
	}
	now, _ := v.Zone.Normalise("2026-09-02T12:00")
	data, err := Export(v, records, now)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)

	// RFC 5545 section 3.1: every line ends CRLF.
	for _, line := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		if strings.ContainsAny(line, "\n\r") {
			t.Errorf("line %q contains a bare newline", line)
		}
		if len(line) > 75 {
			t.Errorf("line is %d octets, over the 75-octet fold limit: %q", len(line), line)
		}
	}
	if !strings.HasPrefix(out, "BEGIN:VCALENDAR\r\n") || !strings.HasSuffix(out, "END:VCALENDAR\r\n") {
		t.Error("the stream is not a single VCALENDAR")
	}
	if got, want := strings.Count(out, "BEGIN:VEVENT"), 4; got != want {
		t.Errorf("%d VEVENTs, want %d (one per stored event, series unexpanded)", got, want)
	}
	if got := strings.Count(out, "END:VEVENT"); got != 4 {
		t.Errorf("%d END:VEVENT for 4 BEGIN:VEVENT", got)
	}
	// A series is one component carrying its rule and its exceptions, never
	// expanded into one component per occurrence.
	if !strings.Contains(out, "RRULE:FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR") {
		t.Errorf("the stored rrule is missing:\n%s", out)
	}
	if !strings.Contains(out, "EXDATE:20260903T020000Z") {
		t.Errorf("the exception date is missing:\n%s", out)
	}
	// Times are absolute UTC, so no VTIMEZONE has to be shipped or trusted.
	if !strings.Contains(out, "DTSTART:20260904T070000Z") {
		t.Errorf("the Platform team sync event's UTC start is missing:\n%s", out)
	}
	if !strings.Contains(out, "DTEND:20260904T080000Z") {
		t.Errorf("the Platform team sync event's UTC end is missing:\n%s", out)
	}
	if strings.Contains(out, "BEGIN:VTIMEZONE") {
		t.Error("a VTIMEZONE was emitted; every instant is already absolute")
	}
	// Only events are exported.
	for _, id := range []string{"customer-referral", "ci-capacity", "platform-team", "daily-2026-09-01"} {
		if strings.Contains(out, "UID:"+id+"@") {
			t.Errorf("%s is not an event but was exported", id)
		}
	}
	// Diacritics survive the export.
	if !strings.Contains(out, "SUMMARY:review the São Paulo referral pitch deck") {
		t.Errorf("a title lost its diacritics:\n%s", out)
	}
}

func TestEscape(t *testing.T) {
	cases := map[string]string{
		"plain":                      "plain",
		"a,b":                        `a\,b`,
		"a;b":                        `a\;b`,
		`a\b`:                        `a\\b`,
		"line\nbreak":                `line\nbreak`,
		"line\r\nbreak":              `line\nbreak`,
		"Zürich sync 東京":             "Zürich sync 東京",
		`all,of;them\and` + "\nmore": `all\,of\;them\\and\nmore`,
	}
	for in, want := range cases {
		if got := escape(in); got != want {
			t.Errorf("escape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFoldRespectsRuneBoundaries(t *testing.T) {
	long := "DESCRIPTION:" + strings.Repeat("Zürich rollout schedule 東京 ", 12)
	folded := fold(long)
	lines := strings.Split(folded, "\r\n")
	if len(lines) < 2 {
		t.Fatalf("a %d-octet line was not folded", len(long))
	}
	for i, l := range lines {
		if len(l) > 75 {
			t.Errorf("folded line %d is %d octets: %q", i, len(l), l)
		}
		if i > 0 && !strings.HasPrefix(l, " ") {
			t.Errorf("continuation line %d does not begin with a space: %q", i, l)
		}
	}
	// Unfolding per RFC 5545 must give back exactly the original.
	unfolded := strings.ReplaceAll(folded, "\r\n ", "")
	if unfolded != long {
		t.Errorf("unfolding did not round-trip:\n got %q\nwant %q", unfolded, long)
	}
	if strings.Contains(folded, "�") {
		t.Error("folding split a UTF-8 sequence")
	}
	if got := fold("SHORT:line"); got != "SHORT:line" {
		t.Errorf("a short line was folded: %q", got)
	}
}

// dstSeriesVault opens a fresh vault in the given zone with a weekly Sunday
// series carrying the given exception dates.
func dstSeriesVault(t *testing.T, zone, whenStamp string, exceptions []string) (*vault.Vault, *vault.Record) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "vault")
	cfg := vault.Config{Timezone: zone, WeekStarts: "sun", NudgeAfter: "14d"}
	if _, err := vault.Init(root, cfg, false); err != nil {
		t.Fatal(err)
	}
	v, err := vault.OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	when, err := v.Zone.Normalise(whenStamp)
	if err != nil {
		t.Fatalf("Normalise(%q): %v", whenStamp, err)
	}
	exc := make([]timeref.Date, 0, len(exceptions))
	for _, e := range exceptions {
		d, err := timeref.ParseDateOnly(e)
		if err != nil {
			t.Fatalf("ParseDateOnly(%q): %v", e, err)
		}
		exc = append(exc, d)
	}
	rel, doc, err := v.BuildEvent(vault.NewEvent{
		ID: "dst-series", Title: "dst-series", When: when,
		RRule: "FREQ=WEEKLY;BYDAY=SU", Exceptions: exc, Created: v.Zone.DateOf(when),
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
		if r.ID == "dst-series" {
			return v, r
		}
	}
	t.Fatal("record dst-series not found after save")
	return nil, nil
}

// TestExdatesResolveThroughTheVaultZone: a weekly Sunday series whose when:
// falls in EST, with an exception dated in EDT, must export an EXDATE
// carrying EDT's own -04:00 offset instant rather than reusing EST's -05:00,
// which would put it an hour off from the occurrence Expand computes for
// that date.
func TestExdatesResolveThroughTheVaultZone(t *testing.T) {
	v, r := dstSeriesVault(t, "America/New_York", "2026-01-11T09:00", []string{"2026-07-19"})
	now, err := v.Zone.Normalise("2026-01-01T00:00")
	if err != nil {
		t.Fatal(err)
	}
	data, err := Export(v, []*vault.Record{r}, now)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	// 2026-07-19 09:00 in America/New_York is EDT, -04:00, i.e. 13:00Z.
	if !strings.Contains(out, "EXDATE:20260719T130000Z") {
		t.Errorf("EXDATE does not carry the EDT instant:\n%s", out)
	}
	if strings.Contains(out, "EXDATE:20260719T140000Z") {
		t.Errorf("EXDATE carries the EST offset instead of EDT's:\n%s", out)
	}
}

// TestExdatesUnaffectedByDSTInTheVaultsDefaultZone is the control: a
// DST-free zone (Asia/Bangkok) exports the exception unchanged.
func TestExdatesUnaffectedByDSTInTheVaultsDefaultZone(t *testing.T) {
	v, r := dstSeriesVault(t, "Asia/Bangkok", "2026-01-11T09:00", []string{"2026-07-19"})
	now, err := v.Zone.Normalise("2026-01-01T00:00")
	if err != nil {
		t.Fatal(err)
	}
	data, err := Export(v, []*vault.Record{r}, now)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if !strings.Contains(out, "EXDATE:20260719T020000Z") {
		t.Errorf("EXDATE does not carry the Asia/Bangkok instant:\n%s", out)
	}
}

// TestExdatesRejectADSTGapDate: an exception dated on the spring-forward
// Sunday must fail loudly rather than export a silently shifted EXDATE.
func TestExdatesRejectADSTGapDate(t *testing.T) {
	v, r := dstSeriesVault(t, "America/New_York", "2026-03-01T02:30", []string{"2026-03-08"})
	now, err := v.Zone.Normalise("2026-01-01T00:00")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Export(v, []*vault.Record{r}, now)
	if err == nil {
		t.Fatal("a DST-gap exception date was accepted")
	}
	msg := err.Error()
	for _, want := range []string{"2026-03-08", "02:30:00", "America/New_York", "does not exist"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q:\n%s", want, msg)
		}
	}
}

// TestExdatesRejectAnAmbiguousDate: an exception dated on the fall-back
// Sunday must fail loudly, naming both candidate instants.
func TestExdatesRejectAnAmbiguousDate(t *testing.T) {
	v, r := dstSeriesVault(t, "America/New_York", "2026-10-25T01:30", []string{"2026-11-01"})
	now, err := v.Zone.Normalise("2026-01-01T00:00")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Export(v, []*vault.Record{r}, now)
	if err == nil {
		t.Fatal("an ambiguous exception date was accepted")
	}
	msg := err.Error()
	for _, want := range []string{
		"2026-11-01", "01:30:00", "America/New_York",
		"2026-11-01T01:30:00-04:00", "2026-11-01T01:30:00-05:00", "ambiguous",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q:\n%s", want, msg)
		}
	}
}

func TestICSStatusMapsClosedVocabulary(t *testing.T) {
	for _, s := range vault.EventStatuses {
		got := icsStatus(s)
		switch got {
		case "TENTATIVE", "CONFIRMED", "CANCELLED":
		default:
			t.Errorf("status %q mapped to %q, which is not an RFC 5545 value", s, got)
		}
	}
}
