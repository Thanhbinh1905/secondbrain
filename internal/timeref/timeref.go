// Package timeref owns every timestamp decision in the vault: which zone a
// naive time belongs to, what "this week" means, and how a duration is spelled.
//
// Two rules are absolute. A timestamp is never stored without an explicit UTC
// offset (FR-3), and a naive timestamp read back out of the vault is a corrupt
// record rather than something to guess about (section 08 failure table).
//
// Relative-phrase resolution ("next Thursday at 2pm") is deliberately absent: it
// belongs to the agent, so this package has no relative-date bugs to have.
package timeref

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	// Embedded so a static binary resolves zone names without system tzdata.
	_ "time/tzdata"
)

// StoredLayout is the only form a timestamp is written in.
const StoredLayout = time.RFC3339

// DateLayout is the form a plain calendar date is written in.
const DateLayout = "2006-01-02"

const wallLayout = "2006-01-02T15:04:05"

// naiveLayouts are the timestamp spellings accepted on input without an
// offset. They are normalised through the vault zone before being stored.
var naiveLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// offsetLayouts are the timestamp spellings that already carry an offset.
var offsetLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04Z07:00",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04Z07:00",
}

// Zone is a vault's timezone together with its first day of the week. Every
// calendar question in the tool is asked of a Zone, never of the host.
type Zone struct {
	Loc        *time.Location
	WeekStarts time.Weekday
}

// LoadZone resolves an IANA zone name and a weekday name.
func LoadZone(name, weekStarts string) (Zone, error) {
	if strings.TrimSpace(name) == "" {
		return Zone{}, fmt.Errorf("timezone must be set (for example Asia/Bangkok)")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return Zone{}, fmt.Errorf("unknown timezone %q: %w", name, err)
	}
	wd, err := ParseWeekday(weekStarts)
	if err != nil {
		return Zone{}, err
	}
	return Zone{Loc: loc, WeekStarts: wd}, nil
}

var weekdayNames = map[string]time.Weekday{
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
	"sun": time.Sunday, "sunday": time.Sunday,
}

// ParseWeekday reads a weekday name such as "mon" or "sunday".
func ParseWeekday(s string) (time.Weekday, error) {
	wd, ok := weekdayNames[strings.ToLower(strings.TrimSpace(s))]
	if !ok {
		return 0, fmt.Errorf("unknown weekday %q: use one of mon tue wed thu fri sat sun", s)
	}
	return wd, nil
}

// Name returns the zone's IANA name.
func (z Zone) Name() string { return z.Loc.String() }

// Resolve returns every instant whose wall clock in this zone is the given
// calendar-and-clock reading, earliest first.
//
// The length of the result is the whole answer to the two questions Go's own
// parser answers silently and wrongly for our purposes: an empty result means
// the reading does not exist (a DST spring-forward gap), and a result longer
// than one means it is ambiguous (a fall-back repeat).
func (z Zone) Resolve(year int, month time.Month, day, hour, min, sec int) []time.Time {
	ref := time.Date(year, month, day, hour, min, sec, 0, time.UTC)
	want := ref.Format(wallLayout)

	// Fast path. No UTC offset exceeds fourteen hours, so every candidate
	// instant lies within fourteen hours of the reference. When the zone holds
	// one offset across a window wider than that, the reading has at most one
	// meaning and two probes settle it.
	_, before := ref.Add(-20 * time.Hour).In(z.Loc).Zone()
	_, after := ref.Add(20 * time.Hour).In(z.Loc).Zone()
	if before == after {
		cand := ref.Add(-time.Duration(before) * time.Second)
		if cand.In(z.Loc).Format(wallLayout) != want {
			return nil
		}
		return []time.Time{cand.In(z.Loc)}
	}

	var out []time.Time
	seen := map[int64]bool{}
	// Probing every half hour across two days either side observes every UTC
	// offset the zone has in effect anywhere near this reading, including the
	// 30- and 45-minute offsets some zones use.
	for step := -96; step <= 96; step++ {
		probe := ref.Add(time.Duration(step) * 30 * time.Minute).In(z.Loc)
		_, offset := probe.Zone()
		cand := ref.Add(-time.Duration(offset) * time.Second)
		if cand.In(z.Loc).Format(wallLayout) != want {
			continue
		}
		if !seen[cand.Unix()] {
			seen[cand.Unix()] = true
			out = append(out, cand.In(z.Loc))
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Before(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Normalise turns an input timestamp into an absolute instant.
//
// An input that already carries an offset is trusted as written. A naive input
// is interpreted in the vault zone, and is rejected rather than guessed at when
// that interpretation is not unique.
func (z Zone) Normalise(input string) (time.Time, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return time.Time{}, fmt.Errorf("timestamp must not be empty")
	}
	for _, layout := range offsetLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.In(z.Loc), nil
		}
	}
	for _, layout := range naiveLayouts {
		w, err := time.Parse(layout, s)
		if err != nil {
			continue
		}
		candidates := z.Resolve(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second())
		switch len(candidates) {
		case 1:
			return candidates[0], nil
		case 0:
			return time.Time{}, fmt.Errorf("local time %s does not exist in %s: the clock jumps forward across it, so give an explicit offset or a different time", s, z.Name())
		default:
			forms := make([]string, 0, len(candidates))
			for _, c := range candidates {
				forms = append(forms, c.Format(StoredLayout))
			}
			return time.Time{}, fmt.Errorf("local time %s is ambiguous in %s (%s): give an explicit offset", s, z.Name(), strings.Join(forms, " or "))
		}
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a timestamp: use 2006-01-02T15:04, 2006-01-02T15:04:05+07:00 or 2006-01-02", s)
}

// ParseStored reads a timestamp out of the vault. A timestamp without an
// explicit offset is a corrupt record: silently assuming the vault zone is how
// a meeting ends up an hour off.
func ParseStored(raw string) (time.Time, error) {
	s := strings.TrimSpace(raw)
	for _, layout := range offsetLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	for _, layout := range naiveLayouts {
		if _, err := time.Parse(layout, s); err == nil {
			return time.Time{}, fmt.Errorf("timestamp %q has no UTC offset: a stored timestamp must always carry one (for example %s+07:00)", s, s)
		}
	}
	return time.Time{}, fmt.Errorf("timestamp %q is not a valid instant: expected 2006-01-02T15:04:05+07:00", s)
}

// Format renders an instant the way the vault stores it.
func Format(t time.Time) string { return t.Format(StoredLayout) }

// FormatDate renders a calendar date the way the vault stores it.
func FormatDate(t time.Time) string { return t.Format(DateLayout) }

// ParseDate reads a plain calendar date as the first instant of that day in
// this zone. Used for created:, touched: and recurrence exceptions.
func (z Zone) ParseDate(raw string) (time.Time, error) {
	s := strings.TrimSpace(raw)
	w, err := time.Parse(DateLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("cannot read %q as a date: expected %s", s, DateLayout)
	}
	return z.StartOf(Date{Year: w.Year(), Month: w.Month(), Day: w.Day()}), nil
}

// Span is a duration in the spelling the vault uses: 60m, 90m, 2h30m, 14d, 1w.
//
// Calendar days are kept apart from clock time on purpose. Adding 14 days to a
// date must move the wall clock 14 days, which is not always 336 hours.
type Span struct {
	Days  int
	Clock time.Duration
	// text is the spelling the value was written with. Somebody typed 60m
	// and the specification's format says 60m, so a rewrite of the same file
	// must not quietly turn it into 1h.
	text string
}

// ParseSpan reads a duration written as a sequence of number-unit pairs.
// Units: w (weeks), d (days), h, m, s. Weeks and days are calendar units.
func ParseSpan(raw string) (Span, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Span{}, fmt.Errorf("duration must not be empty")
	}
	neg := false
	if rest, ok := strings.CutPrefix(s, "-"); ok {
		neg, s = true, rest
	}
	var out Span
	seenUnit := map[byte]bool{}
	i := 0
	for i < len(s) {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return Span{}, fmt.Errorf("cannot read %q as a duration: expected a number before %q", raw, s[i:])
		}
		n, err := strconv.Atoi(s[start:i])
		if err != nil {
			return Span{}, fmt.Errorf("cannot read %q as a duration: %v", raw, err)
		}
		if i >= len(s) {
			return Span{}, fmt.Errorf("cannot read %q as a duration: %q has no unit, use w d h m or s", raw, s[start:i])
		}
		unit := s[i]
		i++
		if seenUnit[unit] {
			return Span{}, fmt.Errorf("cannot read %q as a duration: unit %q appears twice", raw, string(unit))
		}
		seenUnit[unit] = true
		switch unit {
		case 'w':
			out.Days += n * 7
		case 'd':
			out.Days += n
		case 'h':
			out.Clock += time.Duration(n) * time.Hour
		case 'm':
			out.Clock += time.Duration(n) * time.Minute
		case 's':
			out.Clock += time.Duration(n) * time.Second
		default:
			return Span{}, fmt.Errorf("cannot read %q as a duration: unknown unit %q, use w d h m or s", raw, string(unit))
		}
	}
	if neg {
		out.Days, out.Clock = -out.Days, -out.Clock
	}
	out.text = strings.TrimSpace(raw)
	return out, nil
}

// IsZero reports whether the span moves nothing.
func (s Span) IsZero() bool { return s.Days == 0 && s.Clock == 0 }

// SameLength reports whether two spans move a clock by the same amount,
// ignoring how each was spelled.
func (s Span) SameLength(other Span) bool {
	return s.Days == other.Days && s.Clock == other.Clock
}

// String renders the span in the vault's spelling, preserving the spelling it
// was parsed from.
func (s Span) String() string {
	if s.text != "" {
		return s.text
	}
	if s.IsZero() {
		return "0m"
	}
	var sb strings.Builder
	days, clock := s.Days, s.Clock
	if days < 0 || clock < 0 {
		sb.WriteByte('-')
		days, clock = -days, -clock
	}
	// Days, never weeks: the vault's own horizons are written 14d and 30d, and
	// "2w" reads worse than "14d" in an age column.
	if days != 0 {
		fmt.Fprintf(&sb, "%dd", days)
	}
	for _, u := range []struct {
		size time.Duration
		name string
	}{{time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}} {
		if n := clock / u.size; n > 0 {
			fmt.Fprintf(&sb, "%d%s", n, u.name)
			clock -= n * u.size
		}
	}
	return sb.String()
}

// ApproxDays expresses the span in whole days, for comparing an age against a
// nudge horizon. Clock time shorter than a day rounds down.
func (s Span) ApproxDays() int {
	return s.Days + int(s.Clock/(24*time.Hour))
}

// Add applies a span to an instant in this zone: calendar days move the wall
// clock, clock time moves the absolute instant.
func (z Zone) Add(t time.Time, s Span) time.Time {
	out := t.In(z.Loc)
	if s.Days != 0 {
		out = z.addDays(out, s.Days)
	}
	if s.Clock != 0 {
		out = out.Add(s.Clock)
	}
	return out
}

// Date is a calendar date in the vault zone: what a calendar page shows,
// independent of whether the zone had any instants on it. Samoa skipped
// 2011-12-30 entirely, and Havana's clock leaves midnight unvisited on a
// transition day, so counting days on instants is not the same thing as
// counting days on a calendar. Anything measured in days is measured here.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// anchor is a location-free reference instant for the date. UTC has no
// transitions, so date arithmetic through it is exact.
func (d Date) anchor() time.Time {
	return time.Date(d.Year, d.Month, d.Day, 12, 0, 0, 0, time.UTC)
}

// Weekday reports the day of the week the calendar page carries.
func (d Date) Weekday() time.Weekday { return d.anchor().Weekday() }

// AddDays moves the calendar page by n days.
func (d Date) AddDays(n int) Date {
	a := d.anchor().AddDate(0, 0, n)
	return Date{Year: a.Year(), Month: a.Month(), Day: a.Day()}
}

// String renders the date as the vault writes it.
func (d Date) String() string { return d.anchor().Format(DateLayout) }

// Before reports whether d precedes other.
func (d Date) Before(other Date) bool { return d.anchor().Before(other.anchor()) }

// After reports whether d follows other.
func (d Date) After(other Date) bool { return d.anchor().After(other.anchor()) }

// StartOfMonth is the first calendar date of the month d falls in.
func (d Date) StartOfMonth() Date { return Date{Year: d.Year, Month: d.Month, Day: 1} }

// MonthStartAfter is the first calendar date of the month n months after d's.
//
// The arithmetic runs on the (year, month) pair and always lands on day 1, so
// it can never do what AddDate(0, 1, 0) does to the 31st of a month - which is
// land on the 3rd of the month after next. n may be negative.
func (d Date) MonthStartAfter(n int) Date {
	total := d.Year*12 + int(d.Month) - 1 + n
	year, month := total/12, total%12
	if month < 0 {
		month += 12
		year--
	}
	return Date{Year: year, Month: time.Month(month + 1), Day: 1}
}

// StartOfQuarter is the first calendar date of the quarter d falls in.
func (d Date) StartOfQuarter() Date {
	return Date{Year: d.Year, Month: time.Month(((int(d.Month)-1)/3)*3 + 1), Day: 1}
}

// QuarterStartAfter is the first calendar date of the quarter n quarters after
// d's.
func (d Date) QuarterStartAfter(n int) Date { return d.StartOfQuarter().MonthStartAfter(3 * n) }

// Quarter is which quarter of its year the date falls in, 1 to 4.
func (d Date) Quarter() int { return (int(d.Month)-1)/3 + 1 }

// DateDiff counts calendar days from a to b, negative when b precedes a.
func DateDiff(a, b Date) int {
	return int(b.anchor().Sub(a.anchor()) / (24 * time.Hour))
}

// ParseDateOnly reads a plain calendar date without needing a zone.
func ParseDateOnly(raw string) (Date, error) {
	w, err := time.Parse(DateLayout, strings.TrimSpace(raw))
	if err != nil {
		return Date{}, fmt.Errorf("cannot read %q as a date: expected %s", strings.TrimSpace(raw), DateLayout)
	}
	return Date{Year: w.Year(), Month: w.Month(), Day: w.Day()}, nil
}

// DateOf is the calendar date an instant falls on in this zone.
func (z Zone) DateOf(t time.Time) Date {
	in := t.In(z.Loc)
	return Date{Year: in.Year(), Month: in.Month(), Day: in.Day()}
}

// StartOf is the first instant on a calendar date in this zone. On a date the
// zone skipped, it is the first instant that follows.
//
// This is an internal window-boundary computation, not a user-facing reading:
// some instant must be chosen for "start of day", so an ambiguous midnight
// intentionally takes the earliest candidate rather than rejecting, unlike
// Normalise and vault.Expand's occurrence resolution, which reject ambiguity
// instead of guessing.
func (z Zone) StartOf(d Date) time.Time {
	if c := z.Resolve(d.Year, d.Month, d.Day, 0, 0, 0); len(c) > 0 {
		return c[0]
	}
	return z.firstInstantAtOrAfter(d.Year, d.Month, d.Day, 0, 0, 0)
}

// addDays moves the wall clock by whole calendar days. The date is shifted on
// the calendar and only then resolved in the zone: shifting through AddDate in
// a location silently lands back on the original day when the target reading
// does not exist.
//
// Like StartOf, this is an internal boundary computation: stepping a day must
// land somewhere, so an ambiguous target reading intentionally takes the
// earliest candidate rather than rejecting.
func (z Zone) addDays(t time.Time, n int) time.Time {
	in := t.In(z.Loc)
	target := z.DateOf(in).AddDays(n)
	cands := z.Resolve(target.Year, target.Month, target.Day, in.Hour(), in.Minute(), in.Second())
	if len(cands) > 0 {
		return cands[0]
	}
	// The reading does not exist on the target date. Take the first instant
	// after the gap, which is where a calendar puts it.
	return z.firstInstantAtOrAfter(target.Year, target.Month, target.Day, in.Hour(), in.Minute(), in.Second())
}

// AddDays moves an instant by whole calendar days in this zone.
func (z Zone) AddDays(t time.Time, n int) time.Time { return z.addDays(t, n) }

// firstInstantAtOrAfter walks forward a minute at a time from a reading that
// does not exist until one does. It also carries across a skipped calendar
// date, which is why the search runs two days rather than a few hours.
func (z Zone) firstInstantAtOrAfter(year int, month time.Month, day, hour, min, sec int) time.Time {
	base := time.Date(year, month, day, hour, min, sec, 0, time.UTC)
	for step := 0; step <= 2*24*60; step++ {
		w := base.Add(time.Duration(step) * time.Minute)
		if c := z.Resolve(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second()); len(c) > 0 {
			return c[0]
		}
	}
	// Unreachable for any real zone: no gap approaches two days.
	panic(fmt.Sprintf("timeref: no instant found within two days of %04d-%02d-%02dT%02d:%02d:%02d in %s",
		year, month, day, hour, min, sec, z.Name()))
}

// StartOfDay is the first instant of t's calendar day in this zone. Zones that
// shift their clocks at midnight have days whose first instant is not 00:00.
func (z Zone) StartOfDay(t time.Time) time.Time { return z.StartOf(z.DateOf(t)) }

// EndOfDay is the first instant of the following calendar date, so a day is the
// half-open interval [StartOfDay, EndOfDay).
func (z Zone) EndOfDay(t time.Time) time.Time { return z.StartOf(z.DateOf(t).AddDays(1)) }

// StartOfWeekDate is the first calendar date of the week containing d, against
// the configured first day rather than whatever the platform thinks a week is.
func (z Zone) StartOfWeekDate(d Date) Date {
	back := (int(d.Weekday()) - int(z.WeekStarts) + 7) % 7
	return d.AddDays(-back)
}

// WeekDates returns the seven calendar dates of the week containing t.
func (z Zone) WeekDates(t time.Time) []Date {
	first := z.StartOfWeekDate(z.DateOf(t))
	out := make([]Date, 7)
	for i := range out {
		out[i] = first.AddDays(i)
	}
	return out
}

// StartOfWeek is the first instant of the week containing t.
func (z Zone) StartOfWeek(t time.Time) time.Time {
	return z.StartOf(z.StartOfWeekDate(z.DateOf(t)))
}

// EndOfWeek is the first instant of the following week.
func (z Zone) EndOfWeek(t time.Time) time.Time {
	return z.StartOf(z.StartOfWeekDate(z.DateOf(t)).AddDays(7))
}

// DaysBetween counts calendar days from a to b in this zone. It is negative
// when b precedes a and zero when both fall on the same day.
func (z Zone) DaysBetween(a, b time.Time) int {
	return DateDiff(z.DateOf(a), z.DateOf(b))
}

// WeekdayOrder returns the offset of wd within a week starting on this zone's
// configured first day: 0 for the first day, 6 for the last.
func (z Zone) WeekdayOrder(wd time.Weekday) int {
	return (int(wd) - int(z.WeekStarts) + 7) % 7
}
