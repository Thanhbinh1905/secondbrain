package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// DefaultFrameWidth is the dashboard's drawn width.
const DefaultFrameWidth = 64

// Weekday names. The short forms are two cells wide because the week strip
// packs seven of them plus their load marks onto one line of the frame; a
// three-letter abbreviation pushes that line past the frame's inner width.
var (
	weekdayLong = map[time.Weekday]string{
		time.Monday:    "Monday",
		time.Tuesday:   "Tuesday",
		time.Wednesday: "Wednesday",
		time.Thursday:  "Thursday",
		time.Friday:    "Friday",
		time.Saturday:  "Saturday",
		time.Sunday:    "Sunday",
	}
	weekdayShort = map[time.Weekday]string{
		time.Monday:    "Mo",
		time.Tuesday:   "Tu",
		time.Wednesday: "We",
		time.Thursday:  "Th",
		time.Friday:    "Fr",
		time.Saturday:  "Sa",
		time.Sunday:    "Su",
	}
)

// WeekdayLong is a weekday's name.
func WeekdayLong(w time.Weekday) string { return weekdayLong[w] }

// WeekdayShort is a weekday's two-cell abbreviation.
func WeekdayShort(w time.Weekday) string { return weekdayShort[w] }

// Dashboard is everything the bare invocation shows.
type Dashboard struct {
	Now      time.Time
	Zone     timeref.Zone
	Today    query.Agenda
	WeekLoad []query.DayLoad
	Week     query.Agenda
	Ideas    []query.IdeaRow
	// Tasks are the open commitments worth a line on the frame: due soon,
	// overdue, or past their follow-up horizon.
	Tasks []query.TaskRow
	// Backlog is the read-only count from the configured backlog command.
	// BacklogNote is the doctor-facing reason there is no count; the dashboard
	// footer only checks it for presence and renders its own short label.
	Backlog     int
	HasBacklog  bool
	BacklogNote string
}

// dayLoadMarks renders a day's load as dots: one filled dot per event up to
// three, then a plus. A day with nothing shows a single faint dot.
func dayLoadMarks(count int) string {
	switch {
	case count <= 0:
		return "·"
	case count > 3:
		return "●●●+"
	default:
		return strings.Repeat("●", count)
	}
}

// Dashboard renders the framed dashboard, or plain lines when stdout is not a
// terminal. It never returns box-drawing characters to a pipe.
func (o *Out) Dashboard(d Dashboard) {
	f := o.buildDashboard(d)
	if o.TTY {
		fmt.Fprint(o.W, f.String())
		return
	}
	fmt.Fprint(o.W, f.Plain())
}

func (o *Out) buildDashboard(d Dashboard) *Frame {
	width := o.FrameWidth(DefaultFrameWidth)
	local := d.Now.In(d.Zone.Loc)
	title := fmt.Sprintf("%s, %s", WeekdayLong(local.Weekday()), local.Format(timeref.DateLayout))
	f := NewFrame(width, "brain", title)
	f.Blank()

	f.Line(" TODAY")
	if len(d.Today.Occurrences) == 0 {
		f.Line("   nothing scheduled today")
	} else {
		next, hasNext := d.Today.Next()
		for _, occ := range d.Today.Occurrences {
			marker := "   "
			switch {
			case hasNext && occ.Start.Equal(next.Start) && occ.Record.ID == next.Record.ID:
				marker = " ▸ "
			case occ.Record.Status == "done":
				// A completed event stays on the day, marked, because what was
				// already done is part of what the day was.
				marker = " ✓ "
			}
			f.Row(marker+timeRange(occ, o.TTY)+"   "+occ.Record.Title, strings.Join(occ.Record.With, " ")+" ")
		}
	}

	f.Section(" THIS WEEK")
	f.Line("   " + o.weekStrip(d))
	for _, line := range o.weekHighlights(d) {
		f.Line("   " + line)
	}

	if len(d.Tasks) > 0 {
		f.Section(" TASKS")
		for _, row := range d.Tasks {
			mark := "○"
			if row.PastHorizon || row.Overdue {
				mark = "●"
			}
			left := fmt.Sprintf("   %s %s", mark, row.Record.ID)
			if row.Record.Assignee != "" {
				left += " → " + row.Record.Assignee
			}
			f.Row(left, taskNote(row)+" ")
		}
	}

	f.Section(" IDEAS PENDING")
	if len(d.Ideas) == 0 {
		f.Line("   no idea is pending")
	} else {
		for _, row := range d.Ideas {
			mark := "○"
			if row.PastHorizon {
				mark = "●"
			}
			f.Row(fmt.Sprintf("   %s %s", mark, row.Record.ID), fmt.Sprintf("%dd ", row.AgeDays))
		}
	}
	f.Blank()

	f.Caption = o.caption(d)
	return f
}

// timeRange renders an occurrence's clock span, or just its start when it has
// no duration. The separator is a box-drawing rule on a terminal and a plain
// hyphen off one, because no box-drawing character may reach an agent.
func timeRange(occ vault.Occurrence, tty bool) string {
	sep := " - "
	if tty {
		sep = " ─ "
	}
	start := occ.Start.Format("15:04")
	if occ.End.Equal(occ.Start) {
		return start + strings.Repeat(" ", unitext.Width(sep)+5)
	}
	return start + sep + occ.End.Format("15:04")
}

// weekStrip is the load-per-day row: Mo ·  Tu ●·  We ●● ...
func (o *Out) weekStrip(d Dashboard) string {
	parts := make([]string, 0, len(d.WeekLoad))
	todayDate := d.Zone.DateOf(d.Now)
	for _, load := range d.WeekLoad {
		label := WeekdayShort(load.Date.Weekday())
		if load.Date == todayDate {
			label = "[" + label + "]"
		}
		parts = append(parts, label+" "+dayLoadMarks(load.Count))
	}
	return strings.Join(parts, "  ")
}

// weekHighlights names the loaded days of the rest of the week, wrapped to the
// frame width.
func (o *Out) weekHighlights(d Dashboard) []string {
	todayDate := d.Zone.DateOf(d.Now)
	var items []string
	for _, load := range d.WeekLoad {
		if load.Count == 0 || load.Date.Before(todayDate) {
			continue
		}
		items = append(items, fmt.Sprintf("%s  %s", WeekdayShort(load.Date.Weekday()), load.Highlight()))
	}
	if len(items) == 0 {
		return []string{"nothing left this week"}
	}
	inner := o.FrameWidth(DefaultFrameWidth) - 7
	var lines []string
	cur := ""
	for _, it := range items {
		candidate := it
		if cur != "" {
			candidate = cur + "   " + it
		}
		if unitext.Width(candidate) > inner && cur != "" {
			lines = append(lines, cur)
			cur = it
			continue
		}
		cur = candidate
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// taskNote is the right-hand column of a task line: how late it is, or how
// long it has gone unchecked. Overdue wins, because a missed deadline is the
// more concrete fact.
func taskNote(row query.TaskRow) string {
	switch {
	case row.Overdue:
		return fmt.Sprintf("%dd overdue", -row.DueInDays)
	case row.PastHorizon:
		return fmt.Sprintf("%dd unchecked", row.AgeDays)
	case row.Record.HasDue && row.DueInDays == 0:
		return "due today"
	case row.Record.HasDue:
		return fmt.Sprintf("due in %dd", row.DueInDays)
	default:
		return fmt.Sprintf("%dd", row.AgeDays)
	}
}

// caption is the bottom rule: the read-only backlog count and the number of
// ideas past their nudge horizon.
func (o *Out) caption(d Dashboard) string {
	var parts []string
	switch {
	case d.HasBacklog:
		parts = append(parts, fmt.Sprintf("%d open in backlog", d.Backlog))
	case d.BacklogNote != "":
		parts = append(parts, "backlog_cmd failed")
	}
	overdue := 0
	for _, row := range d.Ideas {
		if row.PastHorizon {
			overdue++
		}
	}
	if overdue > 0 {
		parts = append(parts, fmt.Sprintf("%d idea stale", overdue))
	} else {
		parts = append(parts, "no idea is stale")
	}
	unchecked := 0
	for _, row := range d.Tasks {
		if row.PastHorizon || row.Overdue {
			unchecked++
		}
	}
	if unchecked > 0 {
		parts = append(parts, fmt.Sprintf("%d task needs attention", unchecked))
	}
	return strings.Join(parts, " · ")
}
