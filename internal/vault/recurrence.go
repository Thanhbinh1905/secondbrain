package vault

import (
	"fmt"
	"strings"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/teambition/rrule-go"
)

// Recurrence is stored as an RFC 5545 RRULE string plus a list of excepted
// dates, and is expanded only when a query asks. Nothing is ever written to
// disk per occurrence: a series stays one hand-editable file, which keeps the
// corruption surface the size of one file rather than the size of a calendar.
//
// The stored string is the same text an .ics file carries, so export needs no
// second serialiser.

// ValidateRRule reports why a recurrence rule is unusable, or nil. It rejects
// at parse time so a bad rule is a loud error on the file that holds it rather
// than an empty week later.
func ValidateRRule(spec string) error {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return fmt.Errorf("rrule must not be empty")
	}
	if trimmed != spec {
		return fmt.Errorf("rrule %q has surrounding whitespace", spec)
	}
	if strings.ContainsAny(spec, "\r\n") {
		return fmt.Errorf("rrule must be a single line")
	}
	// DTSTART belongs to when:, not to the rule; two sources of truth for the
	// series start is exactly the disagreement this format avoids.
	if strings.Contains(strings.ToUpper(spec), "DTSTART") {
		return fmt.Errorf("rrule must not carry DTSTART: the series starts at when:")
	}
	if _, err := rrule.StrToRRule(spec); err != nil {
		return fmt.Errorf("invalid rrule %q: %v; expected an RFC 5545 rule such as FREQ=WEEKLY;BYDAY=FR", spec, err)
	}
	return nil
}

// Occurrence is one instance of an event, real or expanded from a series.
type Occurrence struct {
	// Record is the stored file the occurrence came from. It is shared across
	// every occurrence of a series and is never mutated by expansion.
	Record *Record
	// Start and End are this occurrence's own instants.
	Start time.Time
	End   time.Time
	// Index counts occurrences of a series from zero. It is zero for a
	// non-recurring event.
	Index int
	// Recurring reports whether this came out of an rrule.
	Recurring bool
}

// IsExcepted reports whether a start instant falls on an excepted date.
// Exceptions are dates, not instants, because that is how a person thinks
// about a cancelled occurrence and how somebody would type one by hand.
func (v *Vault) IsExcepted(r *Record, start time.Time) bool {
	return isExceptedDate(r, v.Zone.DateOf(start))
}

func isExceptedDate(r *Record, on timeref.Date) bool {
	for _, e := range r.Exceptions {
		if e == on {
			return true
		}
	}
	return false
}

// wallAsUTC repaints an instant's wall-clock digits in this zone onto a UTC
// time.Time carrying the same year/month/day/hour/min/sec. UTC has no DST
// transitions, so date arithmetic done against the result cannot be corrupted
// by one the way rrule-go's raw time.Date calls would be against a real zone.
func wallAsUTC(t time.Time, loc *time.Location) time.Time {
	in := t.In(loc)
	return time.Date(in.Year(), in.Month(), in.Day(), in.Hour(), in.Minute(), in.Second(), in.Nanosecond(), time.UTC)
}

// ResolveOccurrence turns a wall-clock reading (only naive's year, month, day,
// hour, minute and second are read; its location is ignored) back into the
// real instant it names in the vault zone, or reports why it cannot: the
// reading falls in a DST gap, or is ambiguous across a fall-back repeat. Both
// are loud errors, never a guess, matching Zone.Normalise's treatment of the
// same hazard for hand-typed input. r is named only for the error message.
func (v *Vault) ResolveOccurrence(r *Record, naive time.Time) (time.Time, error) {
	cands := v.Zone.Resolve(naive.Year(), naive.Month(), naive.Day(), naive.Hour(), naive.Minute(), naive.Second())
	date := naive.Format(timeref.DateLayout)
	clock := naive.Format("15:04:05")
	switch len(cands) {
	case 1:
		return cands[0], nil
	case 0:
		return time.Time{}, fmt.Errorf("%s: recurring occurrence on %s at %s does not exist in %s: the clock jumps forward across it; change the series time, or add this date to the series' exceptions: list",
			r.Rel, date, clock, v.Zone.Name())
	default:
		forms := make([]string, 0, len(cands))
		for _, c := range cands {
			forms = append(forms, c.Format(timeref.StoredLayout))
		}
		return time.Time{}, fmt.Errorf("%s: recurring occurrence on %s at %s is ambiguous in %s (%s): change the series time, or add this date to the series' exceptions: list",
			r.Rel, date, clock, v.Zone.Name(), strings.Join(forms, " or "))
	}
}

// maxOccurrences bounds a single series' expansion inside one window. A rule
// with no UNTIL and no COUNT is infinite, so an unbounded window must not be
// able to spin: the query layer always passes a bounded window, and this is the
// backstop for a rule such as FREQ=SECONDLY.
const maxOccurrences = 10000

// Expand returns the occurrences of a record that intersect [from, to). A
// non-recurring event contributes at most one; a series contributes every
// occurrence the rule generates in the window, minus its exceptions.
//
// An occurrence intersects the window when it has not already finished by the
// time the window opens and it starts before the window closes, so a meeting
// running across midnight still shows up on both days.
func (v *Vault) Expand(r *Record, from, to time.Time) ([]Occurrence, error) {
	if !r.HasWhen {
		return nil, nil
	}
	if r.RRule == "" {
		occ := Occurrence{Record: r, Start: r.When, End: v.End(r)}
		if occ.End.Before(from) || !occ.Start.Before(to) {
			return nil, nil
		}
		return []Occurrence{occ}, nil
	}

	rule, err := rrule.StrToRRule(r.RRule)
	if err != nil {
		// Unreachable for a loaded record: ValidateRRule already ran.
		return nil, fmt.Errorf("%s: rrule %q: %v", r.Rel, r.RRule, err)
	}
	// rrule-go builds every occurrence with a raw time.Date(year, month, day,
	// hour, min, sec, 0, loc): given a real *time.Location it silently shifts a
	// wall clock that a DST gap skips and silently picks one of two candidates
	// for a fall-back repeat, both with a nil error (see
	// AGENTS.md "Sharp edges"). Running the rule against a UTC-painted DTStart
	// keeps rrule-go computing pure calendar arithmetic, so the year/month/day/
	// hour/min/sec it hands back are the wall-clock reading the series actually
	// intends, uncorrupted by any zone's DST rules. Every one of those readings
	// is then checked against the real vault zone through Zone.Resolve below.
	rule.DTStart(wallAsUTC(r.When, v.Zone.Loc))

	// A long event must still be found when the window opens mid-occurrence,
	// so the rule is asked from one duration before the window.
	searchFrom := from
	if !r.Duration.IsZero() {
		searchFrom = v.Zone.Add(from, timeref.Span{Days: -r.Duration.Days, Clock: -r.Duration.Clock})
	}
	starts := rule.Between(wallAsUTC(searchFrom, v.Zone.Loc), wallAsUTC(to, v.Zone.Loc), true)
	if len(starts) > maxOccurrences {
		return nil, fmt.Errorf("%s: rrule %q produces more than %d occurrences in the requested window; narrow the window or bound the rule with COUNT or UNTIL", r.Rel, r.RRule, maxOccurrences)
	}

	var out []Occurrence
	for i, naive := range starts {
		on := timeref.Date{Year: naive.Year(), Month: naive.Month(), Day: naive.Day()}
		if isExceptedDate(r, on) {
			continue
		}
		start, err := v.ResolveOccurrence(r, naive)
		if err != nil {
			return nil, err
		}
		end := start
		if !r.Duration.IsZero() {
			end = v.Zone.Add(start, r.Duration)
		}
		if end.Before(from) || !start.Before(to) {
			continue
		}
		out = append(out, Occurrence{Record: r, Start: start, End: end, Index: i, Recurring: true})
	}
	return out, nil
}

// NextOccurrence returns the first occurrence of a record at or after now, or
// false when the series has run out. It searches a year at a time so an
// exception-heavy series still resolves without an unbounded scan.
func (v *Vault) NextOccurrence(r *Record, now time.Time) (Occurrence, bool, error) {
	if !r.HasWhen {
		return Occurrence{}, false, nil
	}
	if r.RRule == "" {
		if r.When.Before(now) {
			return Occurrence{}, false, nil
		}
		return Occurrence{Record: r, Start: r.When, End: v.End(r)}, true, nil
	}
	from := now
	for year := 0; year < 10; year++ {
		to := v.Zone.AddDays(from, 366)
		occs, err := v.Expand(r, from, to)
		if err != nil {
			return Occurrence{}, false, err
		}
		for _, o := range occs {
			if !o.Start.Before(now) {
				return o, true, nil
			}
		}
		from = to
	}
	return Occurrence{}, false, nil
}
