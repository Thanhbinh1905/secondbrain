package timeref

import (
	"math/rand"
	"testing"
	"time"
)

// propertyZones deliberately spans every shape of zone that has ever broken a
// calendar tool: no DST, whole-hour DST, half-hour DST, half-hour and
// three-quarter-hour base offsets, DST transitions at midnight, negative DST,
// and a zone that once skipped a whole calendar day.
var propertyZones = []string{
	"Asia/Bangkok",        // a fixed-offset zone: +07, no DST
	"UTC",                 // no offset at all
	"America/New_York",    // one-hour DST, 02:00 transitions
	"Europe/Dublin",       // negative DST in the tz database
	"Australia/Lord_Howe", // 30-minute DST shift
	"America/Havana",      // DST transition at midnight
	"Asia/Kolkata",        // +05:30, no DST
	"Asia/Kathmandu",      // +05:45, no DST
	"Pacific/Chatham",     // +12:45 / +13:45
	"Pacific/Apia",        // skipped 2011-12-30 entirely
	"America/St_Johns",    // -03:30 with DST
}

func zonesForTest(t *testing.T, weekStarts string) []Zone {
	t.Helper()
	out := make([]Zone, 0, len(propertyZones))
	for _, name := range propertyZones {
		z, err := LoadZone(name, weekStarts)
		if err != nil {
			t.Fatalf("LoadZone(%q): %v", name, err)
		}
		out = append(out, z)
	}
	return out
}

// randomInstants returns a reproducible spread of instants across two decades,
// densest around DST transition weeks where the bugs live.
func randomInstants(n int) []time.Time {
	r := rand.New(rand.NewSource(20260901))
	base := time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)
	span := int64(20 * 365 * 24 * time.Hour)
	out := make([]time.Time, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, base.Add(time.Duration(r.Int63n(span))))
	}
	// Every DST transition boundary in the covered years, to the minute.
	for year := 2015; year <= 2035; year++ {
		for _, md := range [][2]int{{3, 8}, {3, 29}, {4, 1}, {10, 4}, {10, 25}, {11, 1}, {12, 30}} {
			for _, hm := range [][2]int{{0, 0}, {0, 30}, {1, 0}, {1, 30}, {2, 0}, {2, 30}, {3, 0}, {23, 30}} {
				out = append(out, time.Date(year, time.Month(md[0]), md[1], hm[0], hm[1], 0, 0, time.UTC))
			}
		}
	}
	return out
}

// TestPropertyDayBoundaries: a day is the half-open interval
// [StartOfDay, EndOfDay), StartOfDay is idempotent, and no instant escapes it.
func TestPropertyDayBoundaries(t *testing.T) {
	instants := randomInstants(400)
	for _, z := range zonesForTest(t, "mon") {
		for _, at := range instants {
			s := z.StartOfDay(at)
			e := z.EndOfDay(at)
			if at.Before(s) || !at.Before(e) {
				t.Fatalf("%s: %s not inside [%s, %s)", z.Name(), Format(at.In(z.Loc)), Format(s), Format(e))
			}
			if !e.After(s) {
				t.Fatalf("%s: EndOfDay %s does not follow StartOfDay %s", z.Name(), Format(e), Format(s))
			}
			if again := z.StartOfDay(s); !again.Equal(s) {
				t.Fatalf("%s: StartOfDay is not idempotent: %s -> %s", z.Name(), Format(s), Format(again))
			}
			if got := z.StartOfDay(e); !got.Equal(e) {
				t.Fatalf("%s: EndOfDay %s is not the start of its own day (%s)", z.Name(), Format(e), Format(got))
			}
			// The interval is one calendar day, never two.
			if d := z.DaysBetween(s, e); d != 1 {
				t.Fatalf("%s: [%s, %s) spans %d days", z.Name(), Format(s), Format(e), d)
			}
			// Both ends fall on the calendar dates they claim.
			sd, ed := s.In(z.Loc), e.In(z.Loc)
			if sd.Day() == ed.Day() && sd.Month() == ed.Month() {
				t.Fatalf("%s: StartOfDay %s and EndOfDay %s are the same calendar day", z.Name(), Format(s), Format(e))
			}
		}
	}
}

// TestPropertyWeekBoundaries: the week starts on the configured day whatever
// the platform thinks, contains the instant, and is exactly seven days wide.
func TestPropertyWeekBoundaries(t *testing.T) {
	instants := randomInstants(300)
	for _, start := range []string{"mon", "sun", "sat", "wed"} {
		for _, z := range zonesForTest(t, start) {
			for _, at := range instants {
				s := z.StartOfWeek(at)
				e := z.EndOfWeek(at)
				if got := s.In(z.Loc).Weekday(); got != z.WeekStarts {
					t.Fatalf("%s/%s: week starts on %v, want %v (%s)", z.Name(), start, got, z.WeekStarts, Format(s))
				}
				if at.Before(s) || !at.Before(e) {
					t.Fatalf("%s/%s: %s not inside [%s, %s)", z.Name(), start, Format(at.In(z.Loc)), Format(s), Format(e))
				}
				if d := DateDiff(z.DateOf(s), z.DateOf(e)); d != 7 {
					t.Fatalf("%s/%s: week [%s, %s) spans %d calendar days", z.Name(), start, Format(s), Format(e), d)
				}
				if again := z.StartOfWeek(s); !again.Equal(s) {
					t.Fatalf("%s/%s: StartOfWeek is not idempotent: %s -> %s", z.Name(), start, Format(s), Format(again))
				}
				if got := z.StartOfWeek(e); !got.Equal(e) {
					t.Fatalf("%s/%s: EndOfWeek %s is not the start of its own week", z.Name(), start, Format(e))
				}
				// The week is seven calendar pages, each in its own slot 0..6.
				dates := z.WeekDates(at)
				if len(dates) != 7 {
					t.Fatalf("%s/%s: WeekDates returned %d dates", z.Name(), start, len(dates))
				}
				seen := map[int]bool{}
				for i, d := range dates {
					if slot := z.WeekdayOrder(d.Weekday()); slot != i {
						t.Fatalf("%s/%s: %s is day %d of the week but reports slot %d", z.Name(), start, d, i, slot)
					}
					seen[i] = true
				}
				if len(seen) != 7 {
					t.Fatalf("%s/%s: week starting %s covered %d slots", z.Name(), start, dates[0], len(seen))
				}
				if got := dates[0].String(); got != FormatDate(s.In(z.Loc)) {
					t.Fatalf("%s/%s: WeekDates starts %s but StartOfWeek is %s", z.Name(), start, got, Format(s))
				}
				if !z.StartOf(dates[6].AddDays(1)).Equal(e) {
					t.Fatalf("%s/%s: the day after %s is not EndOfWeek %s", z.Name(), start, dates[6], Format(e))
				}
			}
		}
	}
}

// TestPropertyAddDaysIsCalendarArithmetic: adding days moves the wall clock,
// which is what a calendar means, rather than adding multiples of 24 hours.
func TestPropertyAddDaysIsCalendarArithmetic(t *testing.T) {
	instants := randomInstants(200)
	for _, z := range zonesForTest(t, "mon") {
		for _, at := range instants {
			day := z.StartOfDay(at)
			for _, n := range []int{1, 7, 14, 28, 30, 31, 365, -1, -7, -30} {
				got := z.AddDays(day, n)
				if d := z.DaysBetween(day, got); d != n {
					t.Fatalf("%s: AddDays(%s, %d) landed %d days away at %s", z.Name(), Format(day), n, d, Format(got))
				}
				// Moving a day boundary is a calendar step, not a timestamp
				// step: on a date whose midnight the zone never visits, the
				// boundary is the first instant that exists, and carrying that
				// reading forward would drift off the next boundary.
				bound := z.StartOf(z.DateOf(day).AddDays(n))
				if !bound.Equal(z.StartOfDay(bound)) {
					t.Fatalf("%s: StartOf(date+%d) produced the non-boundary %s", z.Name(), n, Format(bound))
				}
				if d := z.DaysBetween(day, bound); d != n {
					t.Fatalf("%s: StartOf(%s + %d) landed %d days away at %s", z.Name(), z.DateOf(day), n, d, Format(bound))
				}
			}
			// Composition holds on the calendar. It cannot hold on the clock:
			// a zone that leaves midnight unvisited on a transition day moves
			// the reading, and the moved reading is what the next step carries.
			a, b := 13, 19
			l, r := z.DateOf(z.AddDays(z.AddDays(day, a), b)), z.DateOf(z.AddDays(day, a+b))
			if l != r {
				t.Fatalf("%s: AddDays composition broke: %s vs %s", z.Name(), l, r)
			}
			// Pure calendar arithmetic composes exactly.
			base := z.DateOf(day)
			if base.AddDays(a).AddDays(b) != base.AddDays(a+b) {
				t.Fatalf("%s: Date.AddDays composition broke at %s", z.Name(), base)
			}
		}
	}
}

// TestPropertyNormaliseRoundTrip: any instant this package produces survives
// being written to the vault and read back, to the second.
func TestPropertyNormaliseRoundTrip(t *testing.T) {
	instants := randomInstants(400)
	for _, z := range zonesForTest(t, "mon") {
		for _, at := range instants {
			stored := Format(at.In(z.Loc).Truncate(time.Second))
			back, err := ParseStored(stored)
			if err != nil {
				t.Fatalf("%s: ParseStored(%q): %v", z.Name(), stored, err)
			}
			if !back.Equal(at.Truncate(time.Second)) {
				t.Fatalf("%s: %q read back as %s", z.Name(), stored, Format(back))
			}
			// Feeding the stored form back through Normalise is a no-op.
			norm, err := z.Normalise(stored)
			if err != nil {
				t.Fatalf("%s: Normalise(%q): %v", z.Name(), stored, err)
			}
			if !norm.Equal(back) {
				t.Fatalf("%s: Normalise(%q) = %s, want %s", z.Name(), stored, Format(norm), Format(back))
			}
		}
	}
}

// TestPropertyNaiveNormalisationIsUniqueOrLoud: a naive reading either has
// exactly one meaning in the vault zone, or normalising it fails. It is never
// silently shifted (Go's own ParseInLocation shifts a gap reading) and never
// silently resolved to one of two ambiguous instants.
func TestPropertyNaiveNormalisationIsUniqueOrLoud(t *testing.T) {
	instants := randomInstants(400)
	for _, z := range zonesForTest(t, "mon") {
		for _, at := range instants {
			wall := at.In(z.Loc).Format("2006-01-02T15:04:05")
			got, err := z.Normalise(wall)
			cands := z.Resolve(at.In(z.Loc).Year(), at.In(z.Loc).Month(), at.In(z.Loc).Day(),
				at.In(z.Loc).Hour(), at.In(z.Loc).Minute(), at.In(z.Loc).Second())
			switch len(cands) {
			case 1:
				if err != nil {
					t.Fatalf("%s: Normalise(%q) failed on an unambiguous reading: %v", z.Name(), wall, err)
				}
				if got.Format("2006-01-02T15:04:05") != wall {
					t.Fatalf("%s: Normalise(%q) shifted the wall clock to %s", z.Name(), wall, Format(got))
				}
			default:
				if err == nil {
					t.Fatalf("%s: Normalise(%q) silently chose %s among %d readings", z.Name(), wall, Format(got), len(cands))
				}
			}
			// Whatever comes out is always stored with an offset.
			if err == nil {
				if _, e := ParseStored(Format(got)); e != nil {
					t.Fatalf("%s: Normalise produced an unstorable instant %q: %v", z.Name(), Format(got), e)
				}
			}
		}
	}
}

// TestPropertyResolveMatchesGoParseWhereverGoIsRight anchors Resolve against
// the standard library on unambiguous readings, so the hand-rolled offset
// search cannot drift away from real zone data.
func TestPropertyResolveMatchesGoParseWhereverGoIsRight(t *testing.T) {
	instants := randomInstants(400)
	for _, z := range zonesForTest(t, "mon") {
		for _, at := range instants {
			in := at.In(z.Loc)
			wall := in.Format("2006-01-02T15:04:05")
			cands := z.Resolve(in.Year(), in.Month(), in.Day(), in.Hour(), in.Minute(), in.Second())
			if len(cands) != 1 {
				continue
			}
			ref, err := time.ParseInLocation("2006-01-02T15:04:05", wall, z.Loc)
			if err != nil {
				t.Fatalf("%s: ParseInLocation(%q): %v", z.Name(), wall, err)
			}
			if !cands[0].Equal(ref) {
				t.Fatalf("%s: Resolve(%q) = %s, stdlib says %s", z.Name(), wall, Format(cands[0]), Format(ref))
			}
		}
	}
}

// TestPropertyDaysBetweenIsAntisymmetric across DST-shortened and lengthened days.
func TestPropertyDaysBetweenIsAntisymmetric(t *testing.T) {
	instants := randomInstants(200)
	for _, z := range zonesForTest(t, "mon") {
		for i := 0; i+1 < len(instants); i += 2 {
			a, b := instants[i], instants[i+1]
			if fwd, back := z.DaysBetween(a, b), z.DaysBetween(b, a); fwd != -back {
				t.Fatalf("%s: DaysBetween(a,b)=%d but DaysBetween(b,a)=%d", z.Name(), fwd, back)
			}
			if same := z.DaysBetween(a, a); same != 0 {
				t.Fatalf("%s: DaysBetween(a,a)=%d", z.Name(), same)
			}
		}
	}
}

// The known-answer tests below pin the exact behaviours the audit measured.

func TestDSTGapIsRejectedNotShifted(t *testing.T) {
	z, err := LoadZone("America/New_York", "mon")
	if err != nil {
		t.Fatal(err)
	}
	// Go's own parser answers 01:30-05:00 for this reading, with no error.
	shifted, err := time.ParseInLocation("2006-01-02T15:04", "2026-03-08T02:30", z.Loc)
	if err != nil || shifted.Format("15:04") != "01:30" {
		t.Fatalf("stdlib behaviour changed: %v %v", shifted, err)
	}
	if _, err := z.Normalise("2026-03-08T02:30"); err == nil {
		t.Fatal("a local time that does not exist was accepted")
	} else if want := "local time 2026-03-08T02:30 does not exist in America/New_York: the clock jumps forward across it, so give an explicit offset or a different time"; err.Error() != want {
		t.Errorf("got %q\nwant %q", err, want)
	}
	// The same reading with an explicit offset is fine: the input said which
	// instant that was meant. It is displayed in the zone, so 02:30-05:00 shows as
	// 03:30-04:00, which is the same moment.
	if got, err := z.Normalise("2026-03-08T02:30:00-05:00"); err != nil {
		t.Errorf("explicit offset rejected: %v", err)
	} else if Format(got) != "2026-03-08T03:30:00-04:00" {
		t.Errorf("explicit offset landed at %s", Format(got))
	} else if got.Unix() != 1772955000 {
		t.Errorf("explicit offset is not the instant named: unix %d", got.Unix())
	}
}

func TestDSTAmbiguityIsRejectedNotGuessed(t *testing.T) {
	z, err := LoadZone("America/New_York", "mon")
	if err != nil {
		t.Fatal(err)
	}
	// Go's own parser silently picks the daylight offset.
	guessed, _ := time.ParseInLocation("2006-01-02T15:04", "2026-11-01T01:30", z.Loc)
	if _, off := guessed.Zone(); off != -4*3600 {
		t.Fatalf("stdlib behaviour changed: offset %d", off)
	}
	_, err = z.Normalise("2026-11-01T01:30")
	if err == nil {
		t.Fatal("an ambiguous local time was accepted")
	}
	want := "local time 2026-11-01T01:30 is ambiguous in America/New_York (2026-11-01T01:30:00-04:00 or 2026-11-01T01:30:00-05:00): give an explicit offset"
	if err.Error() != want {
		t.Errorf("got %q\nwant %q", err, want)
	}
}

func TestHalfHourDSTAmbiguity(t *testing.T) {
	z, err := LoadZone("Australia/Lord_Howe", "mon")
	if err != nil {
		t.Fatal(err)
	}
	// Lord Howe shifts by thirty minutes, so only a half-hour-aware resolver
	// finds both readings of 01:45 on the April transition.
	cands := z.Resolve(2026, time.April, 5, 1, 45, 0)
	if len(cands) != 2 {
		t.Fatalf("expected two readings of 2026-04-05T01:45 in Lord Howe, got %d: %v", len(cands), cands)
	}
	if _, err := z.Normalise("2026-04-05T01:45"); err == nil {
		t.Error("ambiguous half-hour reading accepted")
	}
}

func TestMidnightDSTTransition(t *testing.T) {
	// Havana moves its clock at midnight, so 00:00 does not exist that day and
	// StartOfDay must be the first instant that does.
	z, err := LoadZone("America/Havana", "mon")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.March, 8, 12, 0, 0, 0, z.Loc)
	s := z.StartOfDay(at)
	if got := s.Format("2006-01-02T15:04:05-07:00"); got != "2026-03-08T01:00:00-04:00" {
		t.Errorf("StartOfDay = %s, want 2026-03-08T01:00:00-04:00", got)
	}
	if s.After(at) {
		t.Errorf("StartOfDay %s is after the instant it contains", Format(s))
	}
}

func TestNaiveStoredTimestampIsRejected(t *testing.T) {
	for _, raw := range []string{
		"2026-09-04T14:00:00",
		"2026-09-04T14:00",
		"2026-09-04 14:00:00",
		"2026-09-04",
	} {
		_, err := ParseStored(raw)
		if err == nil {
			t.Errorf("naive stored timestamp %q accepted", raw)
			continue
		}
		if want := "timestamp \"" + raw + "\" has no UTC offset: a stored timestamp must always carry one (for example " + raw + "+07:00)"; err.Error() != want {
			t.Errorf("got %q\nwant %q", err, want)
		}
	}
	if _, err := ParseStored("not a time"); err == nil {
		t.Error("garbage accepted as a timestamp")
	}
	for _, raw := range []string{"2026-09-04T14:00:00+07:00", "2026-09-04T07:00:00Z", "2026-09-04T14:00+07:00"} {
		if _, err := ParseStored(raw); err != nil {
			t.Errorf("ParseStored(%q): %v", raw, err)
		}
	}
}

func TestVaultZoneNormalisation(t *testing.T) {
	z, err := LoadZone("Asia/Bangkok", "mon")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"2026-09-04T14:00":          "2026-09-04T14:00:00+07:00",
		"2026-09-04T14:00:00":       "2026-09-04T14:00:00+07:00",
		"2026-09-04 14:00":          "2026-09-04T14:00:00+07:00",
		"2026-09-04":                "2026-09-04T00:00:00+07:00",
		"2026-09-04T07:00:00Z":      "2026-09-04T14:00:00+07:00",
		"2026-09-04T14:00:00+07:00": "2026-09-04T14:00:00+07:00",
		"2026-09-04T09:00:00+02:00": "2026-09-04T14:00:00+07:00",
	}
	for in, want := range cases {
		got, err := z.Normalise(in)
		if err != nil {
			t.Errorf("Normalise(%q): %v", in, err)
			continue
		}
		if Format(got) != want {
			t.Errorf("Normalise(%q) = %s, want %s", in, Format(got), want)
		}
	}
	if _, err := z.Normalise("thursday this week"); err == nil {
		t.Error("a relative phrase was accepted; date resolution belongs to the agent")
	}
	if _, err := z.Normalise(""); err == nil {
		t.Error("an empty timestamp was accepted")
	}
}

func TestParseSpan(t *testing.T) {
	ok := map[string]Span{
		"60m":   {Clock: 60 * time.Minute},
		"90m":   {Clock: 90 * time.Minute},
		"2h":    {Clock: 2 * time.Hour},
		"2h30m": {Clock: 2*time.Hour + 30*time.Minute},
		"14d":   {Days: 14},
		"1w":    {Days: 7},
		"3w":    {Days: 21},
		"1d12h": {Days: 1, Clock: 12 * time.Hour},
		"30s":   {Clock: 30 * time.Second},
		"-7d":   {Days: -7},
		"0m":    {},
	}
	for in, want := range ok {
		got, err := ParseSpan(in)
		if err != nil {
			t.Errorf("ParseSpan(%q): %v", in, err)
			continue
		}
		if !got.SameLength(want) {
			t.Errorf("ParseSpan(%q) = %+v, want %+v", in, got, want)
		}
		if got.String() != in {
			t.Errorf("ParseSpan(%q).String() = %q: the spelling that was written must survive", in, got.String())
		}
	}
	for _, bad := range []string{"", "  ", "d", "14", "14x", "m14", "14dd", "1d1d", "1.5h", "14 d", "abc"} {
		if got, err := ParseSpan(bad); err == nil {
			t.Errorf("ParseSpan(%q) accepted, gave %+v", bad, got)
		}
	}
}

// TestSpanCanonicalStringForConstructedSpans covers the spans the tool builds
// itself, which have no written spelling to preserve.
func TestSpanCanonicalStringForConstructedSpans(t *testing.T) {
	cases := []struct {
		in   Span
		want string
	}{
		{Span{}, "0m"},
		{Span{Days: 30}, "30d"},
		{Span{Days: 14}, "14d"},
		{Span{Days: 7}, "7d"},
		{Span{Days: 21}, "21d"},
		{Span{Clock: 90 * time.Minute}, "1h30m"},
		{Span{Clock: 30 * time.Second}, "30s"},
		{Span{Days: 1, Clock: 12 * time.Hour}, "1d12h"},
		{Span{Days: -7}, "-7d"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("Span%+v.String() = %q, want %q", c.in, got, c.want)
		}
		back, err := ParseSpan(c.in.String())
		if err != nil {
			t.Errorf("ParseSpan(%q): %v", c.in.String(), err)
			continue
		}
		if !back.SameLength(c.in) {
			t.Errorf("%q reparsed as %+v, want %+v", c.in.String(), back, c.in)
		}
	}
}

func TestSpanStringRoundTrip(t *testing.T) {
	for _, in := range []string{"60m", "90m", "2h", "2h30m", "14d", "1w", "2w", "30s", "1d12h", "0m"} {
		s, err := ParseSpan(in)
		if err != nil {
			t.Fatalf("ParseSpan(%q): %v", in, err)
		}
		again, err := ParseSpan(s.String())
		if err != nil {
			t.Fatalf("ParseSpan(%q) from String(): %v", s.String(), err)
		}
		if !again.SameLength(s) {
			t.Errorf("%q -> %q -> %+v, want %+v", in, s.String(), again, s)
		}
	}
}

// TestSpanAddIsDSTSafe: adding fourteen days across a DST boundary keeps the
// wall clock, which "untouched for 14 days" has to mean.
func TestSpanAddIsDSTSafe(t *testing.T) {
	z, err := LoadZone("America/New_York", "mon")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, time.March, 1, 9, 0, 0, 0, z.Loc)
	span, _ := ParseSpan("14d")
	got := z.Add(start, span)
	if want := "2026-03-15T09:00:00-04:00"; Format(got) != want {
		t.Errorf("14d after %s = %s, want %s", Format(start), Format(got), want)
	}
	// Clock time is absolute, so an event that starts before the transition
	// and lasts an hour still ends an hour later on the clock face plus DST.
	hour, _ := ParseSpan("60m")
	ev := time.Date(2026, time.March, 8, 1, 30, 0, 0, z.Loc)
	if want := "2026-03-08T03:30:00-04:00"; Format(z.Add(ev, hour)) != want {
		t.Errorf("60m after %s = %s, want %s", Format(ev), Format(z.Add(ev, hour)), want)
	}
}

// TestMonthEndDoesNotOverflow: Go's AddDate turns 2026-01-31 plus a month into
// 2026-03-03. Day arithmetic must never inherit that.
func TestMonthEndDoesNotOverflow(t *testing.T) {
	z, err := LoadZone("Asia/Bangkok", "mon")
	if err != nil {
		t.Fatal(err)
	}
	jan31, err := z.ParseDate("2026-01-31")
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatDate(z.AddDays(jan31, 1)); got != "2026-02-01" {
		t.Errorf("2026-01-31 + 1d = %s", got)
	}
	if got := FormatDate(z.AddDays(jan31, 28)); got != "2026-02-28" {
		t.Errorf("2026-01-31 + 28d = %s", got)
	}
	// 2028 is a leap year.
	feb, _ := z.ParseDate("2028-02-28")
	if got := FormatDate(z.AddDays(feb, 1)); got != "2028-02-29" {
		t.Errorf("2028-02-28 + 1d = %s", got)
	}
	dec31, _ := z.ParseDate("2026-12-31")
	if got := FormatDate(z.AddDays(dec31, 1)); got != "2027-01-01" {
		t.Errorf("2026-12-31 + 1d = %s", got)
	}
}

func TestWeekStartsIsConfigured(t *testing.T) {
	// 2026-09-01 is a Tuesday.
	for _, tc := range []struct {
		start string
		want  string
	}{
		{"mon", "2026-08-31"},
		{"sun", "2026-08-30"},
		{"tue", "2026-09-01"},
		{"wed", "2026-08-26"},
	} {
		z, err := LoadZone("Asia/Bangkok", tc.start)
		if err != nil {
			t.Fatal(err)
		}
		at, _ := z.ParseDate("2026-09-01")
		if got := FormatDate(z.StartOfWeek(at)); got != tc.want {
			t.Errorf("week starting %s: StartOfWeek(2026-09-01) = %s, want %s", tc.start, got, tc.want)
		}
	}
	if _, err := ParseWeekday("funday"); err == nil {
		t.Error("an unknown weekday was accepted")
	}
	if _, err := LoadZone("Mars/Olympus", "mon"); err == nil {
		t.Error("an unknown timezone was accepted")
	}
	if _, err := LoadZone("", "mon"); err == nil {
		t.Error("an empty timezone was accepted")
	}
}

func TestPacificApiaSkippedDay(t *testing.T) {
	// Samoa jumped from 2011-12-29 to 2011-12-31; 2011-12-30 never happened.
	z, err := LoadZone("Pacific/Apia", "mon")
	if err != nil {
		t.Fatal(err)
	}
	if got := z.Resolve(2011, time.December, 30, 12, 0, 0); len(got) != 0 {
		t.Errorf("2011-12-30T12:00 resolved to %v in a zone that skipped that day", got)
	}
	if _, err := z.Normalise("2011-12-30T12:00"); err == nil {
		t.Error("a date that never happened was accepted")
	}
	// The day either side is ordinary, and stepping across the gap still counts
	// 2011-12-30 as a calendar day because the calendar page exists.
	before, err := z.ParseDate("2011-12-29")
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatDate(z.AddDays(before, 1)); got != "2011-12-31" {
		t.Errorf("the day after 2011-12-29 in Apia is %s", got)
	}
}
