// Due answers one question - what needs attention right now - and answers
// nothing when nothing does.
//
// It is separate from Brief on purpose. A brief is a morning read: it names
// everything outstanding whether or not it changed. Due is meant to be run on a
// short interval by something that will stay quiet unless there is a reason not
// to be, so its three categories are all "this crossed a line", never "this
// exists".

package query

import (
	"fmt"
	"sort"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// DueCategory names one of the three things due reports.
type DueCategory string

const (
	// DueDelegated is a task handed to somebody that nobody has checked since
	// its follow-up horizon passed. It is the highest-value category: a
	// commitment somebody else is holding and nobody has asked about.
	DueDelegated DueCategory = "delegated"
	// DueEvent is an occurrence starting inside the configured window.
	DueEvent DueCategory = "event"
	// DueDormantIdea is an idea untouched beyond the configured dormancy
	// window.
	DueDormantIdea DueCategory = "dormant_idea"
)

// DueCategories are every category, in the order due reports them: the
// delegated commitments first, because they are the ones with somebody else's
// name on them.
var DueCategories = []DueCategory{DueDelegated, DueEvent, DueDormantIdea}

// DueFilter selects which categories to report. With nothing selected every
// category is reported, so the bare command is the whole question.
type DueFilter struct {
	Delegated bool
	Events    bool
	Ideas     bool
}

// selects reports whether this filter asks for a category.
func (f DueFilter) selects(c DueCategory) bool {
	if !f.Delegated && !f.Events && !f.Ideas {
		return true
	}
	switch c {
	case DueDelegated:
		return f.Delegated
	case DueEvent:
		return f.Events
	case DueDormantIdea:
		return f.Ideas
	}
	return false
}

// DueItem is one thing that crossed a line.
type DueItem struct {
	Category DueCategory
	Record   *vault.Record
	// Person is who a delegated item sits with. Empty for the other categories.
	Person string
	// Start is when a due event's occurrence begins, and MinutesUntil is how
	// long that is from now. Both are meaningless for the other categories.
	Start        time.Time
	MinutesUntil int
	// Days is how long a delegated item has gone unchecked, or a dormant idea
	// untouched.
	Days int
	// WindowDays is the configured window the item crossed, in days, and
	// WindowLabel is how that window is spelled in the config.
	WindowDays  int
	WindowLabel string
	// Reason is the one line a caller shows. It names the person for a
	// delegated item, because that is the fact that makes it actionable.
	Reason string
}

// Due lists everything that needs attention right now, most important first.
// An empty result is the ordinary case and is not an error.
func (e *Engine) Due(f DueFilter) ([]DueItem, error) {
	var out []DueItem
	if f.selects(DueDelegated) {
		items, err := e.dueDelegated()
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	if f.selects(DueEvent) {
		items, err := e.dueEvents()
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	if f.selects(DueDormantIdea) {
		items, err := e.dueDormantIdeas()
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

// dueDelegated is the category worth building the command for: something
// handed to a named person, past the horizon the user set, with no update
// since.
func (e *Engine) dueDelegated() ([]DueItem, error) {
	rows, err := e.Tasks(TaskFilter{OnlyOpen: true, PastFollowUp: true})
	if err != nil {
		return nil, err
	}
	var out []DueItem
	for _, row := range rows {
		if row.Record.Assignee == "" {
			continue
		}
		horizon := e.Vault.Horizon(row.Record)
		out = append(out, DueItem{
			Category: DueDelegated, Record: row.Record, Person: row.Record.Assignee,
			Days: row.AgeDays, WindowDays: row.HorizonDays, WindowLabel: horizon.String(),
			Reason: fmt.Sprintf("%s has had this for %dd with no update, past its %s follow-up horizon",
				row.Record.Assignee, row.AgeDays, horizon.String()),
		})
	}
	return out, nil
}

// dueEvents expands only the configured window rather than the whole day, so
// running this on a short interval costs a walk and almost no recurrence work.
func (e *Engine) dueEvents() ([]DueItem, error) {
	z := e.Vault.Zone
	deadline := z.Add(e.Now, e.Vault.DueWithin)
	records, err := e.Vault.Walk()
	if err != nil {
		return nil, err
	}
	var out []DueItem
	for _, r := range records {
		// A cancelled event is off the calendar and a completed one has already
		// happened; neither is something to walk into a room for.
		if r.Kind != vault.KindEvent || r.Status != "scheduled" {
			continue
		}
		occs, err := e.Vault.Expand(r, e.Now, deadline.Add(time.Nanosecond))
		if err != nil {
			return nil, err
		}
		for _, occ := range occs {
			if occ.Start.Before(e.Now) || occ.Start.After(deadline) {
				continue
			}
			minutes := int(occ.Start.Sub(e.Now).Round(time.Minute) / time.Minute)
			out = append(out, DueItem{
				Category: DueEvent, Record: r, Start: occ.Start, MinutesUntil: minutes,
				WindowLabel: e.Vault.DueWithin.String(),
				Reason:      fmt.Sprintf("starts in %dm, at %s", minutes, occ.Start.Format("15:04")),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		return out[i].Record.ID < out[j].Record.ID
	})
	return out, nil
}

func (e *Engine) dueDormantIdeas() ([]DueItem, error) {
	records, err := e.Vault.Walk()
	if err != nil {
		return nil, err
	}
	window := e.Vault.DormantAfter
	var out []DueItem
	for _, r := range records {
		if r.Kind != vault.KindIdea || vault.IsClosed(r.Kind, r.Status) {
			continue
		}
		if !e.Vault.Dormant(r, e.Now) {
			continue
		}
		age := e.Vault.AgeDays(r, e.Now)
		out = append(out, DueItem{
			Category: DueDormantIdea, Record: r, Days: age,
			WindowDays: window.ApproxDays(), WindowLabel: window.String(),
			Reason: fmt.Sprintf("untouched for %dd, past the %s dormancy window", age, window.String()),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Days != out[j].Days {
			return out[i].Days > out[j].Days
		}
		return out[i].Record.ID < out[j].Record.ID
	})
	return out, nil
}
