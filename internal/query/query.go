// Package query answers the questions a user actually asks: what is on
// today, what does the week look like, which ideas have gone stale, and where
// did I write about a thing.
//
// Every answer comes from a full walk of the Markdown. There is no index, so
// nothing here can disagree with the files. Every query calls the unscoped
// vault.Walk and filters in memory; scoping was deliberately removed and must
// not be reintroduced, because it is what keeps corruption anywhere visible
// from anywhere (see AGENTS.md). What keeps a five-thousand-file vault inside
// NFR-1's hundred-millisecond budget is vault.Walk's own parallel walk across
// every core plus its per-Vault memoisation of the result for the rest of the
// invocation (internal/vault's Walk/forgetWalk).
package query

import (
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// Engine answers queries against one vault at one moment. Holding "now" makes
// every answer reproducible, which is what the golden tests need.
type Engine struct {
	Vault *vault.Vault
	Now   time.Time
}

// New builds an engine.
func New(v *vault.Vault, now time.Time) *Engine {
	return &Engine{Vault: v, Now: now.In(v.Zone.Loc)}
}

// Agenda is a chronological list of occurrences over a date range.
type Agenda struct {
	From timeref.Date
	To   timeref.Date
	// Occurrences are in chronological order.
	Occurrences []vault.Occurrence
	// NextIndex is the position in Occurrences of the first occurrence that has
	// not finished yet, or -1 when every one of them is in the past.
	NextIndex int
}

// Next returns the occurrence the user is heading towards.
func (a Agenda) Next() (vault.Occurrence, bool) {
	if a.NextIndex < 0 || a.NextIndex >= len(a.Occurrences) {
		return vault.Occurrence{}, false
	}
	return a.Occurrences[a.NextIndex], true
}

// includeStatus decides whether an event's status keeps it on an agenda. A
// cancelled event is off the calendar; a completed one stays, because the
// user asking about today wants to see what they already did.
func includeStatus(status string) bool { return status != "cancelled" }

// Agenda expands every event that intersects [from, to] inclusive of both
// dates, in chronological order.
func (e *Engine) Agenda(from, to timeref.Date) (Agenda, error) {
	if to.Before(from) {
		from, to = to, from
	}
	z := e.Vault.Zone
	start := z.StartOf(from)
	end := z.StartOf(to.AddDays(1))

	records, err := e.Vault.Walk()
	if err != nil {
		return Agenda{}, err
	}

	out := Agenda{From: from, To: to, NextIndex: -1}
	for _, r := range records {
		if r.Kind != vault.KindEvent || !includeStatus(r.Status) {
			continue
		}
		occs, err := e.Vault.Expand(r, start, end)
		if err != nil {
			return Agenda{}, err
		}
		out.Occurrences = append(out.Occurrences, occs...)
	}
	sort.SliceStable(out.Occurrences, func(i, j int) bool {
		a, b := out.Occurrences[i], out.Occurrences[j]
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		return a.Record.ID < b.Record.ID
	})
	for i, o := range out.Occurrences {
		if o.End.After(e.Now) && o.Record.Status == "scheduled" {
			out.NextIndex = i
			break
		}
	}
	return out, nil
}

// Today is the agenda for the calendar day "now" falls on.
func (e *Engine) Today() (Agenda, error) {
	d := e.Vault.Zone.DateOf(e.Now)
	return e.Agenda(d, d)
}

// Week is the agenda for the week "now" falls in, against the configured first
// day of the week.
func (e *Engine) Week() (Agenda, error) {
	dates := e.Vault.Zone.WeekDates(e.Now)
	return e.Agenda(dates[0], dates[6])
}

// IdeaRow is one idea with the age that makes a list of notes a second brain.
type IdeaRow struct {
	Record *vault.Record
	// AgeDays counts calendar days since the idea was last touched.
	AgeDays int
	// HorizonDays is the nudge horizon that applies, its own or the vault's.
	HorizonDays int
	// PastHorizon reports whether it has been ignored longer than that.
	PastHorizon bool
}

// IdeaFilter narrows an idea listing.
type IdeaFilter struct {
	// Status, when set, keeps only ideas with that status.
	Status string
	// Stale, when set, keeps only ideas untouched for at least that long.
	Stale    timeref.Span
	HasStale bool
	// Kinds, when set, widens the listing beyond ideas. Empty means ideas only.
	Kinds []vault.Kind
}

// Ideas lists ideas newest-touched last, each row carrying its age (US-5).
func (e *Engine) Ideas(f IdeaFilter) ([]IdeaRow, error) {
	if f.Status != "" && !hasString(vault.IdeaStatuses, f.Status) {
		return nil, &UnknownStatusError{Status: f.Status, Valid: vault.IdeaStatuses}
	}
	kinds := f.Kinds
	if len(kinds) == 0 {
		kinds = []vault.Kind{vault.KindIdea}
	}
	records, err := e.Vault.Walk()
	if err != nil {
		return nil, err
	}
	var out []IdeaRow
	for _, r := range records {
		if !hasKind(kinds, r.Kind) {
			continue
		}
		if f.Status != "" && r.Status != f.Status {
			continue
		}
		age := e.Vault.AgeDays(r, e.Now)
		if f.HasStale && age < f.Stale.ApproxDays() {
			continue
		}
		out = append(out, IdeaRow{
			Record:      r,
			AgeDays:     age,
			HorizonDays: e.Vault.Horizon(r).ApproxDays(),
			PastHorizon: e.Vault.PastHorizon(r, e.Now),
		})
	}
	// Newest-touched last, so the thing most in need of attention reads first.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].AgeDays != out[j].AgeDays {
			return out[i].AgeDays > out[j].AgeDays
		}
		return out[i].Record.ID < out[j].Record.ID
	})
	return out, nil
}

// TaskRow is one task with everything that decides how loudly it should be
// surfaced: when it is due, and how long it has gone unchecked.
type TaskRow struct {
	Record *vault.Record
	// AgeDays counts calendar days since the task was last touched, which is
	// the last time the user looked at it rather than the last time anyone
	// worked on it.
	AgeDays int
	// HorizonDays is the follow-up horizon that applies, its own or the vault's.
	HorizonDays int
	// PastHorizon reports that an open task has gone unchecked longer than
	// that. This is the whole point of the record kind: a thing handed to
	// somebody three weeks ago that nobody has asked about.
	PastHorizon bool
	// DueInDays is calendar days from today to the due date, negative when the
	// date has passed. Meaningless unless the record has a due date.
	DueInDays int
	// Overdue reports that an open task's due date has passed.
	Overdue bool
}

// TaskFilter narrows a task listing. An empty filter lists every task.
type TaskFilter struct {
	// Status, when set, keeps only tasks with that status.
	Status string
	// Assignee, when set, keeps only tasks handed to that person.
	Assignee string
	// OnlyOpen keeps only open and waiting tasks, which is what "outstanding"
	// means for this kind.
	OnlyOpen bool
	// PastFollowUp keeps only open tasks past their follow-up horizon.
	PastFollowUp bool
}

// Tasks lists tasks most-in-need-of-attention first: overdue before due,
// soonest due before latest, then longest unchecked.
func (e *Engine) Tasks(f TaskFilter) ([]TaskRow, error) {
	if f.Status != "" && !hasString(vault.TaskStatuses, f.Status) {
		return nil, &UnknownStatusError{Status: f.Status, Valid: vault.TaskStatuses}
	}
	records, err := e.Vault.Walk()
	if err != nil {
		return nil, err
	}
	today := e.Vault.Zone.DateOf(e.Now)
	var out []TaskRow
	for _, r := range records {
		if r.Kind != vault.KindTask {
			continue
		}
		if f.Status != "" && r.Status != f.Status {
			continue
		}
		if f.Assignee != "" && r.Assignee != f.Assignee {
			continue
		}
		open := vault.TaskIsOpen(r.Status)
		if f.OnlyOpen && !open {
			continue
		}
		row := TaskRow{
			Record:      r,
			AgeDays:     e.Vault.AgeDays(r, e.Now),
			HorizonDays: e.Vault.Horizon(r).ApproxDays(),
			// A closed task has stopped decaying: nagging about a task the
			// user already finished is exactly the noise that makes them stop
			// reading what comes back.
			PastHorizon: open && e.Vault.PastHorizon(r, e.Now),
		}
		if r.HasDue {
			row.DueInDays = timeref.DateDiff(today, e.Vault.Zone.DateOf(r.Due))
			row.Overdue = open && row.DueInDays < 0
		}
		if f.PastFollowUp && !row.PastHorizon {
			continue
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Record.HasDue != b.Record.HasDue {
			return a.Record.HasDue
		}
		if a.Record.HasDue && a.DueInDays != b.DueInDays {
			return a.DueInDays < b.DueInDays
		}
		if a.AgeDays != b.AgeDays {
			return a.AgeDays > b.AgeDays
		}
		return a.Record.ID < b.Record.ID
	})
	return out, nil
}

// Shipped lists the records that landed inside a date range, oldest first.
//
// It reads shipped_at:, which `brain-axi ship` writes with the merge time the
// caller supplied. That is the only date in the vault that says when work
// actually landed, which is why it is the one a period report can count.
func (e *Engine) Shipped(from, to timeref.Date) ([]*vault.Record, error) {
	if to.Before(from) {
		from, to = to, from
	}
	records, err := e.Vault.Walk()
	if err != nil {
		return nil, err
	}
	var out []*vault.Record
	for _, r := range records {
		if !r.HasShipped {
			continue
		}
		d := e.Vault.Zone.DateOf(r.ShippedAt)
		if d.Before(from) || d.After(to) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].ShippedAt.Equal(out[j].ShippedAt) {
			return out[i].ShippedAt.Before(out[j].ShippedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// UnknownStatusError rejects a status against the closed vocabulary.
type UnknownStatusError struct {
	Status string
	Valid  []string
}

func (e *UnknownStatusError) Error() string {
	return "unknown status " + `"` + e.Status + `"` + ": valid values are " + strings.Join(e.Valid, ", ")
}

func hasString(vocab []string, v string) bool {
	for _, s := range vocab {
		if s == v {
			return true
		}
	}
	return false
}

func hasKind(ks []vault.Kind, k vault.Kind) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

// Hit is one search result: where it was found and the line that matched.
type Hit struct {
	Record *vault.Record
	// Line is the matched line, trimmed. LineNo is its 1-based position in the
	// file, so it can be opened straight to the right place.
	Line   string
	LineNo int
	// Field names what matched: "title", "id", "body" or "tags".
	Field string
	// Exact reports whether the match kept its diacritics.
	Exact bool
}

// Search matches full text across every vault file, diacritic-insensitively in
// both directions (FR-6). "zurich" finds "Zürich", and so does "Zürich".
func (e *Engine) Search(queryText string, limit int) ([]Hit, error) {
	needle := strings.TrimSpace(queryText)
	if needle == "" {
		return nil, nil
	}
	records, err := e.Vault.Walk()
	if err != nil {
		return nil, err
	}
	folded := unitext.Fold(needle)
	lowered := strings.ToLower(needle)

	// Folding is Unicode normalisation and is the expensive part of a search,
	// so it runs across all cores. The results are collected by index and
	// sorted afterwards, so the ranking is identical to a serial pass.
	found := make([]Hit, len(records))
	hit := make([]bool, len(records))
	errs := make([]error, len(records))
	workers := runtime.GOMAXPROCS(0)
	if workers > len(records) {
		workers = len(records)
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	idx := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				h, ok, err := bestHit(records[i], needle, lowered, folded)
				switch {
				case err != nil:
					errs[i] = err
				case ok:
					found[i], hit[i] = h, true
				}
			}
		}()
	}
	for i := range records {
		idx <- i
	}
	close(idx)
	wg.Wait()

	// The first failure in path order is the one reported, so the same vault
	// always fails the same way.
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	var hits []Hit
	for i := range records {
		if hit[i] {
			hits = append(hits, found[i])
		}
	}
	// Rank: a diacritic-exact match first, then a title or id match over a body
	// match, then most-recently-touched, then id so the order never wobbles.
	fieldRank := map[string]int{"id": 0, "title": 1, "tags": 2, "body": 3}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.Exact != b.Exact {
			return a.Exact
		}
		if fieldRank[a.Field] != fieldRank[b.Field] {
			return fieldRank[a.Field] < fieldRank[b.Field]
		}
		ad, bd := a.Record.Created, b.Record.Created
		if a.Record.HasTouched {
			ad = a.Record.Touched
		}
		if b.Record.HasTouched {
			bd = b.Record.Touched
		}
		if ad != bd {
			return bd.Before(ad)
		}
		return a.Record.ID < b.Record.ID
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// bestHit finds the single most relevant line in one record.
func bestHit(r *vault.Record, needle, lowered, folded string) (Hit, bool, error) {
	// An id or title match is about the record itself, so it outranks any line.
	if strings.Contains(strings.ToLower(r.ID), lowered) {
		return Hit{Record: r, Line: r.Title, LineNo: 0, Field: "id", Exact: true}, true, nil
	}
	if r.Title != "" {
		if strings.Contains(strings.ToLower(r.Title), lowered) {
			return Hit{Record: r, Line: r.Title, LineNo: 0, Field: "title", Exact: true}, true, nil
		}
		if strings.Contains(unitext.Fold(r.Title), folded) {
			return Hit{Record: r, Line: r.Title, LineNo: 0, Field: "title", Exact: false}, true, nil
		}
	}
	for _, tag := range r.Tags {
		if unitext.Contains(tag, needle) {
			return Hit{Record: r, Line: "tags: " + strings.Join(r.Tags, ", "), LineNo: 0, Field: "tags",
				Exact: strings.Contains(strings.ToLower(tag), lowered)}, true, nil
		}
	}
	// One folded pass over the whole body decides whether any line can match.
	// Folding is Unicode normalisation, and doing it once per file rather than
	// once per line is most of this function's cost.
	loweredBody := strings.ToLower(r.Body)
	exactPossible := strings.Contains(loweredBody, lowered)
	foldedBody := ""
	foldedPossible := false
	if !exactPossible {
		foldedBody = unitext.Fold(r.Body)
		foldedPossible = strings.Contains(foldedBody, folded)
	}
	if !exactPossible && !foldedPossible {
		return Hit{}, false, nil
	}

	// The body's line numbers must be the numbers in the file, so counting
	// starts after the frontmatter block rather than at the body's own line 1.
	offset, err := frontmatterLines(r)
	if err != nil {
		return Hit{}, false, err
	}
	lines := strings.Split(r.Body, "\n")
	loweredLines := strings.Split(loweredBody, "\n")
	var foldedLines []string
	if foldedPossible {
		foldedLines = strings.Split(foldedBody, "\n")
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if exactPossible && i < len(loweredLines) && strings.Contains(loweredLines[i], lowered) {
			return Hit{Record: r, Line: trimmed, LineNo: offset + i + 1, Field: "body", Exact: true}, true, nil
		}
		if foldedPossible && i < len(foldedLines) && strings.Contains(foldedLines[i], folded) {
			return Hit{Record: r, Line: trimmed, LineNo: offset + i + 1, Field: "body", Exact: false}, true, nil
		}
	}
	return Hit{}, false, nil
}

// frontmatterLines counts the file lines the frontmatter block occupies,
// delimiters included, so a body line number matches the file.
func frontmatterLines(r *vault.Record) (int, error) {
	raw, err := r.Doc().Bytes()
	if err != nil {
		return 0, err
	}
	head := strings.TrimSuffix(string(raw), r.Body)
	return strings.Count(head, "\n"), nil
}

// LinkGraph is the link neighbourhood of one record: everything it points at,
// and everything that points at it.
type LinkGraph struct {
	// Outgoing are the ids in the record's own links: frontmatter, resolved
	// where possible. This is the linking layer proper.
	Outgoing []LinkTarget
	// Body are the [[wiki-link]] targets in the record's body. They are kept
	// apart from Outgoing because one is a field the user maintains and the
	// other is prose they happened to write.
	Body []LinkTarget
	// Backlinks are the records that point at this one, each carrying which
	// field did the pointing.
	Backlinks []Backlink
	// With are the people profiles named in an event's with: list.
	With []LinkTarget
	// RaiseWith are the people this record is waiting to be raised with.
	RaiseWith []LinkTarget
	// Assignee is a task's assignee, resolved where possible. Zero value
	// (Resolved() false with an empty ID) means the record has none.
	Assignee LinkTarget
}

// LinkTarget is a link and the record it resolves to, if any.
type LinkTarget struct {
	ID     string
	Record *vault.Record
}

// Resolved reports whether the target names a record that exists.
func (t LinkTarget) Resolved() bool { return t.Record != nil }

// Backlink is one record pointing at another, and the fields it points with.
type Backlink struct {
	Record *vault.Record
	// Via names every field of Record that names the target: any of "links",
	// "body", "with", "assignee" or "raise_with". Knowing which one matters:
	// "this meeting produced that idea" and "that idea mentions this meeting
	// in passing" are different answers.
	Via []string
}

// LinkFields are every frontmatter field that can name another record, in the
// order output lists them.
var LinkFields = []string{"links", "body", "with", "assignee", "raise_with"}

// Links builds the link neighbourhood of a record. A link to an id with no
// file is reported unresolved rather than dropped: a dangling link is
// information, and Obsidian treats it the same way.
func (e *Engine) Links(r *vault.Record) (LinkGraph, error) {
	records, err := e.Vault.Walk()
	if err != nil {
		return LinkGraph{}, err
	}
	byID := map[string]*vault.Record{}
	people := map[string]*vault.Record{}
	for _, rec := range records {
		byID[rec.ID] = rec
		if rec.Kind == vault.KindPerson {
			people[rec.ID] = rec
		}
	}
	g := LinkGraph{}
	for _, id := range r.Links {
		g.Outgoing = append(g.Outgoing, LinkTarget{ID: id, Record: byID[id]})
	}
	for _, id := range r.BodyLinks {
		g.Body = append(g.Body, LinkTarget{ID: id, Record: byID[id]})
	}
	for _, id := range r.With {
		g.With = append(g.With, LinkTarget{ID: id, Record: people[id]})
	}
	for _, id := range r.RaiseWith {
		g.RaiseWith = append(g.RaiseWith, LinkTarget{ID: id, Record: people[id]})
	}
	if r.Assignee != "" {
		g.Assignee = LinkTarget{ID: r.Assignee, Record: people[r.Assignee]}
	}
	for _, rec := range records {
		if rec.ID == r.ID {
			continue
		}
		if via := PointsAt(rec, r); len(via) > 0 {
			g.Backlinks = append(g.Backlinks, Backlink{Record: rec, Via: via})
		}
	}
	sort.SliceStable(g.Backlinks, func(i, j int) bool { return g.Backlinks[i].Record.ID < g.Backlinks[j].Record.ID })
	return g, nil
}

// PointsAt reports every field of rec that names id, in LinkFields order.
func PointsAt(rec, target *vault.Record) []string {
	var via []string
	id := target.ID
	if hasString(rec.Links, id) {
		via = append(via, "links")
	}
	if hasString(rec.BodyLinks, id) {
		via = append(via, "body")
	}
	if target.Kind == vault.KindPerson && hasString(rec.With, id) {
		via = append(via, "with")
	}
	if target.Kind == vault.KindPerson && rec.Assignee == id {
		via = append(via, "assignee")
	}
	if target.Kind == vault.KindPerson && hasString(rec.RaiseWith, id) {
		via = append(via, "raise_with")
	}
	return via
}

// DayLoad is how many occurrences fall on one calendar date, for the
// dashboard's week strip.
type DayLoad struct {
	Date  timeref.Date
	Count int
	// Titles are the occurrence titles on that date, chronologically, and
	// Recurring marks which of them came out of a series.
	Titles    []string
	Recurring []bool
}

// Highlight is the one title worth naming for this day. A one-off is more
// informative than a standup that happens every morning, so it wins.
func (d DayLoad) Highlight() string {
	if len(d.Titles) == 0 {
		return ""
	}
	for i, recurring := range d.Recurring {
		if !recurring {
			return d.Titles[i]
		}
	}
	return d.Titles[0]
}

// WeekLoad counts the occurrences on each of the seven dates of the current
// week, so the dashboard can show how loaded each remaining day is.
func (e *Engine) WeekLoad() ([]DayLoad, Agenda, error) {
	dates := e.Vault.Zone.WeekDates(e.Now)
	agenda, err := e.Agenda(dates[0], dates[6])
	if err != nil {
		return nil, Agenda{}, err
	}
	loads := make([]DayLoad, len(dates))
	for i, d := range dates {
		loads[i] = DayLoad{Date: d}
	}
	for _, o := range agenda.Occurrences {
		on := e.Vault.Zone.DateOf(o.Start)
		for i := range loads {
			if loads[i].Date == on {
				loads[i].Count++
				loads[i].Titles = append(loads[i].Titles, o.Record.Title)
				loads[i].Recurring = append(loads[i].Recurring, o.Recurring)
				break
			}
		}
	}
	return loads, agenda, nil
}

// Brief is what the morning session brief needs: today, what is coming due,
// and what has been ignored too long (US-9).
type Brief struct {
	Today Agenda
	// Stale are ideas past their nudge horizon, most neglected first.
	Stale []IdeaRow
	// Upcoming are the occurrences in the next Horizon days beyond today.
	Upcoming []vault.Occurrence
	// UpcomingDays is how far ahead Upcoming looks.
	UpcomingDays int
	// DueTasks are open tasks due between today and the end of the upcoming
	// window, plus everything already overdue.
	DueTasks []TaskRow
	// UncheckedTasks are open tasks past their follow-up horizon. They are the
	// push half of the task record: a delegated thing nobody has checked in
	// three weeks has to arrive unasked.
	UncheckedTasks []TaskRow
}

// Brief assembles the push half of the product: what the user was about to
// forget, without having to ask.
func (e *Engine) Brief(upcomingDays int) (Brief, error) {
	today, err := e.Today()
	if err != nil {
		return Brief{}, err
	}
	z := e.Vault.Zone
	from := z.DateOf(e.Now).AddDays(1)
	to := z.DateOf(e.Now).AddDays(upcomingDays)
	ahead, err := e.Agenda(from, to)
	if err != nil {
		return Brief{}, err
	}
	ideas, err := e.Ideas(IdeaFilter{Status: "pending"})
	if err != nil {
		return Brief{}, err
	}
	var stale []IdeaRow
	for _, row := range ideas {
		if row.PastHorizon {
			stale = append(stale, row)
		}
	}
	tasks, err := e.Tasks(TaskFilter{OnlyOpen: true})
	if err != nil {
		return Brief{}, err
	}
	var due, unchecked []TaskRow
	for _, row := range tasks {
		// Everything overdue stays on the brief however far past it is: a
		// deadline that has gone by does not stop mattering because the window
		// moved on.
		if row.Record.HasDue && row.DueInDays <= upcomingDays {
			due = append(due, row)
		}
		if row.PastHorizon {
			unchecked = append(unchecked, row)
		}
	}
	return Brief{
		Today: today, Stale: stale, Upcoming: ahead.Occurrences, UpcomingDays: upcomingDays,
		DueTasks: due, UncheckedTasks: unchecked,
	}, nil
}
