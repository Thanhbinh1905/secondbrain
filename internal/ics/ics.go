// Package ics exports the vault's events as an RFC 5545 iCalendar stream.
//
// The export is one-way and stateless: no import, no sync, no provider, no
// reconciliation. It exists so a vault can be opened in any calendar
// app, and it is regenerated from the Markdown every time.
//
// A recurring series is exported as one VEVENT carrying its RRULE and its
// EXDATEs, exactly as it is stored. Occurrences are never expanded here, for
// the same reason they are never expanded to disk: the series stays one thing.
package ics

import (
	"strings"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// ProdID identifies the writer, as RFC 5545 section 3.7.3 requires.
const ProdID = "-//brain-axi//secondbrain//EN"

// utcLayout is the UTC form RFC 5545 calls a "form #2" date-time.
const utcLayout = "20060102T150405Z"

// Export renders the events as an iCalendar stream. now is stamped as DTSTAMP
// on every component, so a caller controlling the clock gets a reproducible
// file. An exception date whose wall-clock reading does not exist or is
// ambiguous in the vault zone fails loudly rather than exporting a wrong
// EXDATE instant, matching NFR-4.
func Export(v *vault.Vault, records []*vault.Record, now time.Time) ([]byte, error) {
	var lines []string
	lines = append(lines,
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:"+ProdID,
		"CALSCALE:GREGORIAN",
		"METHOD:PUBLISH",
		"X-WR-CALNAME:brain",
		"X-WR-TIMEZONE:"+v.Zone.Name(),
	)
	stamp := now.UTC().Format(utcLayout)
	for _, r := range records {
		if r.Kind != vault.KindEvent || !r.HasWhen {
			continue
		}
		ev, err := event(v, r, stamp)
		if err != nil {
			return nil, err
		}
		lines = append(lines, ev...)
	}
	lines = append(lines, "END:VCALENDAR")

	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(fold(l))
		// RFC 5545 section 3.1 requires CRLF line breaks.
		sb.WriteString("\r\n")
	}
	return []byte(sb.String()), nil
}

func event(v *vault.Vault, r *vault.Record, stamp string) ([]string, error) {
	out := []string{
		"BEGIN:VEVENT",
		"UID:" + r.ID + "@brain-axi",
		"DTSTAMP:" + stamp,
		"DTSTART:" + r.When.UTC().Format(utcLayout),
	}
	end := v.End(r)
	if !end.Equal(r.When) {
		out = append(out, "DTEND:"+end.UTC().Format(utcLayout))
	}
	summary := r.Title
	if summary == "" {
		summary = r.ID
	}
	out = append(out, "SUMMARY:"+escape(summary))

	if body := strings.TrimSpace(r.Body); body != "" {
		out = append(out, "DESCRIPTION:"+escape(body))
	}
	if len(r.With) > 0 {
		// with: names vault people profiles, not mail addresses, so it is
		// carried as a category rather than invented into an ATTENDEE.
		out = append(out, "CATEGORIES:"+escape(strings.Join(r.With, ",")))
	}
	out = append(out, "STATUS:"+icsStatus(r.Status))
	if r.RRule != "" {
		out = append(out, "RRULE:"+r.RRule)
		if len(r.Exceptions) > 0 {
			ex, err := exdates(v, r)
			if err != nil {
				return nil, err
			}
			out = append(out, "EXDATE:"+strings.Join(ex, ","))
		}
	}
	out = append(out, "END:VEVENT")
	return out, nil
}

func exdates(v *vault.Vault, r *vault.Record) ([]string, error) {
	out := make([]string, 0, len(r.Exceptions))
	for _, d := range r.Exceptions {
		// The excepted date's wall-clock reading, at the series' clock time, must
		// be resolved through the vault's real zone rather than built with a raw
		// time.Date: that would repeat the DST lie AGENTS.md's sharp-edges section
		// bans for recurrence expansion, just relocated to the export path.
		naive := time.Date(d.Year, d.Month, d.Day, r.When.Hour(), r.When.Minute(), r.When.Second(), 0, time.UTC)
		at, err := v.ResolveOccurrence(r, naive)
		if err != nil {
			return nil, err
		}
		out = append(out, at.UTC().Format(utcLayout))
	}
	return out, nil
}

// icsStatus maps the vault's closed event vocabulary onto RFC 5545's.
func icsStatus(status string) string {
	switch status {
	case "done":
		return "CONFIRMED"
	case "cancelled":
		return "CANCELLED"
	default:
		return "TENTATIVE"
	}
}

// escape applies RFC 5545 section 3.3.11 text escaping.
func escape(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		";", `\;`,
		",", `\,`,
		"\r\n", `\n`,
		"\n", `\n`,
		"\r", `\n`,
	).Replace(s)
}

// fold wraps a content line to 75 octets, continuing with a leading space, as
// RFC 5545 section 3.1 requires. The cut never lands inside a UTF-8 sequence.
func fold(line string) string {
	const limit = 75
	if len(line) <= limit {
		return line
	}
	var sb strings.Builder
	rest := line
	budget := limit
	for len(rest) > budget {
		cut := budget
		// Back off to a rune boundary.
		for cut > 0 && !utf8Start(rest[cut]) {
			cut--
		}
		if cut == 0 {
			break
		}
		sb.WriteString(rest[:cut])
		sb.WriteString("\r\n ")
		rest = rest[cut:]
		// A continuation line's leading space counts against the octet limit.
		budget = limit - 1
	}
	sb.WriteString(rest)
	return sb.String()
}

// utf8Start reports whether b begins a UTF-8 sequence.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
