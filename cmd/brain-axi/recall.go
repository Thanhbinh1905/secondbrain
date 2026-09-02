package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/render"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// occurrenceRow is one agenda row in JSON.
type occurrenceRow struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Start     string   `json:"start"`
	End       string   `json:"end"`
	Date      string   `json:"date"`
	Status    string   `json:"status"`
	With      []string `json:"with,omitempty"`
	Recurring bool     `json:"recurring"`
	Next      bool     `json:"next"`
	Path      string   `json:"path"`

	// start and end are the instants the strings above were rendered from.
	// Keeping them means no display path ever re-parses its own output.
	start time.Time
	end   time.Time
}

func (a *app) agendaRows(ag query.Agenda) []occurrenceRow {
	next, hasNext := ag.Next()
	out := make([]occurrenceRow, 0, len(ag.Occurrences))
	for _, o := range ag.Occurrences {
		end := ""
		if !o.End.Equal(o.Start) {
			end = timeref.Format(o.End)
		}
		out = append(out, occurrenceRow{
			start:     o.Start,
			end:       o.End,
			ID:        o.Record.ID,
			Title:     o.Record.Title,
			Start:     timeref.Format(o.Start),
			End:       end,
			Date:      a.vault.Zone.DateOf(o.Start).String(),
			Status:    o.Record.Status,
			With:      o.Record.With,
			Recurring: o.Recurring,
			Next:      hasNext && o.Record.ID == next.Record.ID && o.Start.Equal(next.Start),
			Path:      o.Record.Rel,
		})
	}
	return out
}

// tasksFor is the task half of a date-bounded answer: what is due inside the
// window, plus everything already overdue or past its follow-up horizon.
//
// Overdue and unchecked tasks are deliberately not clipped to the window. A
// deadline does not stop mattering because the week moved on, and a delegated
// thing nobody has checked in three weeks has to be impossible to miss rather
// than only visible on the day it was due.
func (a *app) tasksFor(ag query.Agenda) ([]taskRow, error) {
	rows, err := a.engine().Tasks(query.TaskFilter{OnlyOpen: true})
	if err != nil {
		return nil, err
	}
	var kept []query.TaskRow
	for _, r := range rows {
		inWindow := false
		if r.Record.HasDue {
			due := a.vault.Zone.DateOf(r.Record.Due)
			inWindow = !due.Before(ag.From) && !due.After(ag.To)
		}
		if inWindow || r.Overdue || r.PastHorizon {
			kept = append(kept, r)
		}
	}
	return a.taskRows(kept), nil
}

// writeAgenda prints an agenda in the axi house style, or as JSON.
func (a *app) writeAgenda(name string, ag query.Agenda, emptyMsg string) error {
	rows := a.agendaRows(ag)
	tasks, err := a.tasksFor(ag)
	if err != nil {
		return err
	}
	// What is waiting to be raised with the people in these events belongs
	// beside the events themselves: an agenda item is only useful in the
	// minutes before walking into the room with the person it is for.
	agendas, err := a.engine().AttendeeAgendas(ag)
	if err != nil {
		return err
	}
	raise := raiseRows(agendas)
	if a.out.JSON {
		return a.out.Emit(map[string]any{
			"from": ag.From.String(), "to": ag.To.String(),
			"now": timeref.Format(a.now), "timezone": a.vault.Zone.Name(),
			name: rows, "tasks": tasks, "raise_with": raise,
		})
	}
	block := render.Block{
		Name:    name,
		Columns: render.Cols([]string{"when", "id", "title", "with", "flag"}),
		Empty:   emptyMsg,
	}
	for _, r := range rows {
		flags := []string{}
		if r.Next {
			flags = append(flags, "next")
		}
		if r.Recurring {
			flags = append(flags, "recurring")
		}
		if r.Status != "scheduled" {
			flags = append(flags, r.Status)
		}
		block.Rows = append(block.Rows, []string{
			a.whenColumn(ag, r), r.ID, r.Title, strings.Join(r.With, " "), strings.Join(flags, "+"),
		})
	}
	a.out.Scalar("range", ag.From.String()+".."+ag.To.String())
	a.out.Block(block)
	// A vault with no task due and nothing overdue prints no task block at all,
	// the same way show omits an empty backlinks block. --json always carries
	// the key, so an agent never has to tell "none" apart from "not supported".
	if len(tasks) > 0 {
		a.out.Block(taskBlock("tasks", tasks, ""))
	}
	if len(raise) > 0 {
		a.out.Block(raiseBlock(raise))
	}
	a.out.Attention(append(a.agendaAttention(ag), taskAttention(tasks)...))
	a.out.Help(a.agendaHelp(name))
	return nil
}

// raiseRow is one agenda item beside the meeting it is for.
type raiseRow struct {
	Person      string `json:"person"`
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	WaitingDays int    `json:"waiting_days"`
	Path        string `json:"path"`
}

// raiseRows flattens the per-person agendas into one stable list, ordered by
// person so a day with three meetings reads the same way every time.
func raiseRows(agendas map[string][]query.AgendaItem) []raiseRow {
	people := make([]string, 0, len(agendas))
	for who := range agendas {
		people = append(people, who)
	}
	sort.Strings(people)
	out := make([]raiseRow, 0, len(agendas))
	for _, who := range people {
		for _, item := range agendas[who] {
			out = append(out, raiseRow{
				Person: who, ID: item.Record.ID, Kind: string(item.Record.Kind),
				Title: item.Record.Title, WaitingDays: item.WaitingDays, Path: item.Record.Rel,
			})
		}
	}
	return out
}

func raiseBlock(rows []raiseRow) render.Block {
	block := render.Block{
		Name:    "raise_with",
		Columns: render.Cols([]string{"person", "id", "type", "title", "waiting"}, "waiting"),
	}
	for _, r := range rows {
		block.Rows = append(block.Rows, []string{
			r.Person, r.ID, r.Kind, r.Title, strconv.Itoa(r.WaitingDays) + "d",
		})
	}
	return block
}

// whenColumn is the time a row happened at. A single-day agenda drops the
// date, because every row shares it.
func (a *app) whenColumn(ag query.Agenda, r occurrenceRow) string {
	clock := r.start.In(a.vault.Zone.Loc).Format("15:04")
	if !r.end.Equal(r.start) {
		clock += "-" + r.end.In(a.vault.Zone.Loc).Format("15:04")
	}
	if ag.From == ag.To {
		return clock
	}
	return r.Date + " " + clock
}

func (a *app) agendaAttention(ag query.Agenda) []string {
	var out []string
	if next, ok := ag.Next(); ok {
		out = append(out, fmt.Sprintf("next: %s at %s", next.Record.ID, next.Start.Format("15:04 02/01")))
	}
	// Overlapping slots are worth naming: two things at once is a decision the
	// user has to make, and only they can.
	for i := 1; i < len(ag.Occurrences); i++ {
		prev, cur := ag.Occurrences[i-1], ag.Occurrences[i]
		if cur.Start.Before(prev.End) {
			out = append(out, fmt.Sprintf("overlap: %s and %s both run at %s",
				prev.Record.ID, cur.Record.ID, cur.Start.Format("15:04 02/01")))
		}
	}
	return out
}

func (a *app) agendaHelp(name string) []string {
	switch name {
	case "events":
		return []string{
			"Run `brain-axi week` for the rest of the week",
			"Run `brain-axi show <id>` for one event's body and links",
		}
	default:
		return []string{"Run `brain-axi show <id>` for one event's body and links"}
	}
}

func (a *app) cmdToday() error {
	if err := a.requireArgs(0, "today"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	ag, err := a.engine().Today()
	if err != nil {
		return err
	}
	return a.writeAgenda("events", ag, "nothing scheduled today")
}

func (a *app) cmdWeek() error {
	if err := a.requireArgs(0, "week"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	ag, err := a.engine().Week()
	if err != nil {
		return err
	}
	return a.writeAgenda("events", ag, "nothing scheduled this week")
}

func (a *app) cmdAgenda() error {
	if err := a.requireArgs(0, "agenda"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	from, hasFrom, err := a.dateFlag("from")
	if err != nil {
		return err
	}
	to, hasTo, err := a.dateFlag("to")
	if err != nil {
		return err
	}
	if !hasFrom || !hasTo {
		return usageError("agenda needs both --from and --to as YYYY-MM-DD dates")
	}
	ag, err := a.engine().Agenda(from, to)
	if err != nil {
		return err
	}
	return a.writeAgenda("events", ag, "nothing scheduled in that range")
}

// ideaRow is one idea listing row in JSON.
type ideaRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	AgeDays     int    `json:"age_days"`
	HorizonDays int    `json:"horizon_days"`
	PastHorizon bool   `json:"past_horizon"`
	Created     string `json:"created"`
	Touched     string `json:"touched"`
	// ShippedAt is present only on a record `brain-axi ship` has marked. It is
	// what makes "which ideas actually shipped, and when" answerable without a
	// second command.
	ShippedAt string `json:"shipped_at,omitempty"`
	ShippedPR string `json:"shipped_pr,omitempty"`
	Path      string `json:"path"`
}

func (a *app) cmdIdeas() error {
	if err := a.requireArgs(0, "ideas"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	stale, hasStale, err := a.spanFlag("stale")
	if err != nil {
		return err
	}
	rows, err := a.engine().Ideas(query.IdeaFilter{
		Status: a.flagOr("status", ""), Stale: stale, HasStale: hasStale,
	})
	if err != nil {
		return err
	}
	jsonRows := make([]ideaRow, 0, len(rows))
	for _, r := range rows {
		touched := r.Record.Created.String()
		if r.Record.HasTouched {
			touched = r.Record.Touched.String()
		}
		row := ideaRow{
			ID: r.Record.ID, Title: r.Record.Title, Status: r.Record.Status,
			AgeDays: r.AgeDays, HorizonDays: r.HorizonDays, PastHorizon: r.PastHorizon,
			Created: r.Record.Created.String(), Touched: touched, Path: r.Record.Rel,
		}
		if r.Record.HasShipped {
			row.ShippedAt = timeref.Format(r.Record.ShippedAt)
			row.ShippedPR = r.Record.ShippedPR
		}
		jsonRows = append(jsonRows, row)
	}
	if a.out.JSON {
		return a.out.Emit(map[string]any{"now": timeref.Format(a.now), "ideas": jsonRows})
	}
	block := render.Block{
		Name:    "ideas",
		Columns: render.Cols([]string{"id", "title", "status", "age", "touched"}, "age"),
		Empty:   "no ideas match",
	}
	var attention []string
	for _, r := range jsonRows {
		block.Rows = append(block.Rows, []string{
			r.ID, r.Title, r.Status, strconv.Itoa(r.AgeDays) + "d", r.Touched,
		})
		if r.PastHorizon {
			attention = append(attention, fmt.Sprintf("%s past its %dd nudge horizon", r.ID, r.HorizonDays))
		}
	}
	a.out.Block(block)
	a.out.Attention(attention)
	a.out.Help([]string{
		"Run `brain-axi review` to triage the stale ones",
		"Run `brain-axi update <id> --status building` to start one",
	})
	return nil
}

// taskRow is one task listing row in JSON.
type taskRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Assignee    string `json:"assignee,omitempty"`
	Due         string `json:"due,omitempty"`
	DueInDays   int    `json:"due_in_days,omitempty"`
	Overdue     bool   `json:"overdue"`
	AgeDays     int    `json:"age_days"`
	HorizonDays int    `json:"horizon_days"`
	PastHorizon bool   `json:"past_follow_up"`
	Touched     string `json:"touched"`
	Path        string `json:"path"`
}

func (a *app) taskRows(rows []query.TaskRow) []taskRow {
	out := make([]taskRow, 0, len(rows))
	for _, r := range rows {
		row := taskRow{
			ID: r.Record.ID, Title: r.Record.Title, Status: r.Record.Status,
			Assignee: r.Record.Assignee, Overdue: r.Overdue,
			AgeDays: r.AgeDays, HorizonDays: r.HorizonDays, PastHorizon: r.PastHorizon,
			Touched: r.Record.Touched.String(), Path: r.Record.Rel,
		}
		if r.Record.HasDue {
			row.Due = timeref.Format(r.Record.Due)
			row.DueInDays = r.DueInDays
		}
		out = append(out, row)
	}
	return out
}

// taskBlock is the agent-facing table of tasks. It is its own block rather than
// extra rows in events[]: an event is an occurrence with a start and an end and
// a whole recurrence model behind it, and folding a due date into that column
// would make the events contract mean two different things.
func taskBlock(name string, rows []taskRow, empty string) render.Block {
	block := render.Block{
		Name:    name,
		Columns: render.Cols([]string{"due", "id", "title", "assignee", "status", "flag"}),
		Empty:   empty,
	}
	for _, r := range rows {
		due := render.EmptyCell
		if r.Due != "" {
			due = r.Due
		}
		var flags []string
		if r.Overdue {
			flags = append(flags, fmt.Sprintf("overdue-%dd", -r.DueInDays))
		}
		if r.PastHorizon {
			flags = append(flags, fmt.Sprintf("unchecked-%dd", r.AgeDays))
		}
		block.Rows = append(block.Rows, []string{
			due, r.ID, r.Title, r.Assignee, r.Status, strings.Join(flags, "+"),
		})
	}
	return block
}

// taskAttention names what must not be missed: an overdue task, and a task
// nobody has checked since its follow-up horizon passed. The second one is the
// whole reason the record kind exists.
func taskAttention(rows []taskRow) []string {
	var out []string
	for _, r := range rows {
		if r.Overdue {
			out = append(out, fmt.Sprintf("%s was due %s, %dd ago", r.ID, r.Due, -r.DueInDays))
		}
		if r.PastHorizon {
			who := "nobody has checked it"
			if r.Assignee != "" {
				who = fmt.Sprintf("delegated to %s and not checked", r.Assignee)
			}
			out = append(out, fmt.Sprintf("%s: %s for %dd, past its %dd follow-up horizon", r.ID, who, r.AgeDays, r.HorizonDays))
		}
	}
	return out
}

func (a *app) cmdTasks() error {
	if err := a.requireArgs(0, "tasks"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	filter := query.TaskFilter{
		Status:   a.flagOr("status", ""),
		Assignee: a.flagOr("assignee", ""),
		// Without a status filter the listing is what is outstanding, because
		// a list that buries four open tasks under forty finished ones is not
		// an answer to "what am I waiting on".
		OnlyOpen:     a.flagOr("status", "") == "" && !a.has("all"),
		PastFollowUp: a.has("overdue"),
	}
	rows, err := a.engine().Tasks(filter)
	if err != nil {
		return err
	}
	jsonRows := a.taskRows(rows)
	if a.out.JSON {
		return a.out.Emit(map[string]any{"now": timeref.Format(a.now), "tasks": jsonRows})
	}
	a.out.Block(taskBlock("tasks", jsonRows, "no task matches"))
	a.out.Attention(taskAttention(jsonRows))
	a.out.Help([]string{
		"Run `brain-axi done <id>` when one is finished",
		"Run `brain-axi update <id> --status waiting` when it is with somebody else",
	})
	return nil
}

// hitRow is one search result in JSON.
type hitRow struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Field string `json:"field"`
	Line  string `json:"line"`
	LineN int    `json:"line_number"`
	Exact bool   `json:"exact"`
	Path  string `json:"path"`
}

func (a *app) cmdSearch() error {
	if err := a.requireArgs(1, "search"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	limit, err := a.intFlag("limit", 20)
	if err != nil {
		return err
	}
	hits, err := a.engine().Search(a.args[0], limit)
	if err != nil {
		return err
	}
	rows := make([]hitRow, 0, len(hits))
	for _, h := range hits {
		rows = append(rows, hitRow{
			ID: h.Record.ID, Kind: string(h.Record.Kind), Field: h.Field,
			Line: h.Line, LineN: h.LineNo, Exact: h.Exact, Path: h.Record.Rel,
		})
	}
	if a.out.JSON {
		return a.out.Emit(map[string]any{"query": a.args[0], "hits": rows})
	}
	block := render.Block{
		Name:    "hits",
		Columns: render.Cols([]string{"id", "kind", "where", "line"}),
		Empty:   fmt.Sprintf("nothing matches %q", a.args[0]),
	}
	for _, r := range rows {
		where := r.Field
		if r.LineN > 0 {
			where = fmt.Sprintf("%s:%d", r.Path, r.LineN)
		}
		block.Rows = append(block.Rows, []string{r.ID, r.Kind, where, r.Line})
	}
	a.out.Block(block)
	if len(rows) == limit {
		a.out.Attention([]string{fmt.Sprintf("stopped at the --limit of %d; there may be more", limit)})
	}
	a.out.Help([]string{"Run `brain-axi show <id>` for the whole record"})
	return nil
}

func (a *app) cmdShow() error {
	if err := a.requireArgs(1, "show"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	r, err := a.vault.Find(a.args[0])
	if err != nil {
		return err
	}
	e := a.engine()
	links, err := e.Links(r)
	if err != nil {
		return err
	}

	if a.out.JSON {
		obj, err := a.showJSON(r, links)
		if err != nil {
			return err
		}
		return a.out.Emit(obj)
	}
	a.out.Scalar("id", r.ID)
	a.out.Scalar("type", string(r.Kind))
	a.out.Scalar("path", r.Rel)
	if r.Title != "" {
		a.out.Scalar("title", r.Title)
	}
	if r.Status != "" {
		a.out.Scalar("status", r.Status)
	}
	if r.HasWhen {
		a.out.Scalar("when", timeref.Format(r.When))
		if !r.Duration.IsZero() {
			a.out.Scalar("duration", r.Duration.String())
			a.out.Scalar("ends", timeref.Format(a.vault.End(r)))
		}
	}
	if r.RRule != "" {
		a.out.Scalar("rrule", r.RRule)
		if next, ok, err := a.vault.NextOccurrence(r, a.now); err != nil {
			return err
		} else if ok {
			a.out.Scalar("next_occurrence", timeref.Format(next.Start))
		} else {
			a.out.Scalar("next_occurrence", "none: the series has run out")
		}
	}
	if len(r.Exceptions) > 0 {
		a.out.Scalar("exceptions", strings.Join(dateStrings(r.Exceptions), " "))
	}
	if r.Assignee != "" {
		a.out.Scalar("assignee", r.Assignee)
	}
	if r.HasDue {
		a.out.Scalar("due", fmt.Sprintf("%s (%s)", timeref.Format(r.Due), render.WeekdayLong(r.Due.Weekday())))
	}
	if r.Kind == vault.KindTask {
		a.out.Scalar("follow_up_after", a.vault.Horizon(r).String())
	}
	if r.HasForge {
		a.out.Scalar("forge_url", r.Forge.URL)
		// The timestamp travels with the status everywhere it is shown. A
		// cached state that does not say when it was read is the one way this
		// can be mistaken for a live one.
		if r.Forge.HasStatus {
			a.out.Scalar("forge_state", fmt.Sprintf("%s, checks %s (cached, checked %s, %s)",
				r.Forge.State, r.Forge.Checks, timeref.Format(r.Forge.CheckedAt), checkedAge(a, r)))
		} else {
			a.out.Scalar("forge_state", "never checked; run `brain-axi pr "+r.ID+" --refresh`")
		}
	}
	a.out.Scalar("created", r.Created.String())
	if r.HasTouched {
		a.out.Scalar("touched", fmt.Sprintf("%s (%dd ago)", r.Touched, a.vault.AgeDays(r, a.now)))
	}
	if r.HasNudge || r.Kind == vault.KindIdea {
		a.out.Scalar("nudge_after", a.vault.Horizon(r).String())
	}
	if len(r.Tags) > 0 {
		a.out.Scalar("tags", strings.Join(r.Tags, " "))
	}
	if len(r.FleetTasks) > 0 {
		// Written by `link fleet` and never read back from anywhere: this is a
		// note pointing outward, not a view of a supervisor's state.
		a.out.Scalar("fleet_tasks", strings.Join(r.FleetTasks, " "))
	}
	if r.HasShipped {
		shipped := timeref.Format(r.ShippedAt)
		if r.ShippedPR != "" {
			shipped += " (" + r.ShippedPR + ")"
		}
		a.out.Scalar("shipped_at", shipped)
	}
	if r.HasRaised {
		a.out.Scalar("raised", r.Raised.String())
	}
	a.writeLinkBlock("links", links.Outgoing)
	a.writeLinkBlock("body_links", links.Body)
	a.writeLinkBlock("with", links.With)
	a.writeLinkBlock("raise_with", links.RaiseWith)
	if r.Assignee != "" {
		a.writeLinkBlock("assignee", []query.LinkTarget{links.Assignee})
	}
	backBlock := render.Block{Name: "backlinks", Columns: render.Cols([]string{"id", "type", "via", "title"})}
	for _, b := range links.Backlinks {
		backBlock.Rows = append(backBlock.Rows, []string{b.Record.ID, string(b.Record.Kind), strings.Join(b.Via, "+"), b.Record.Title})
	}
	if len(backBlock.Rows) > 0 {
		a.out.Block(backBlock)
	}
	if r.Kind == vault.KindPerson {
		a.writePersonBlocks(r)
	}
	if body := strings.TrimRight(r.Body, "\n"); strings.TrimSpace(body) != "" {
		fmt.Fprintln(a.stdout(), "body:")
		for _, line := range strings.Split(strings.TrimLeft(body, "\n"), "\n") {
			fmt.Fprintf(a.stdout(), "  %s\n", line)
		}
	}
	a.out.Attention(a.showAttention(r, links))
	a.out.Help([]string{
		fmt.Sprintf("Run `brain-axi update %s --status <status>` to change it", r.ID),
		fmt.Sprintf("Open %s to edit it by hand", r.Rel),
	})
	return nil
}

func (a *app) writeLinkBlock(name string, targets []query.LinkTarget) {
	if len(targets) == 0 {
		return
	}
	block := render.Block{Name: name, Columns: render.Cols([]string{"id", "resolved", "title"})}
	for _, t := range targets {
		title, resolved := "", "no"
		if t.Resolved() {
			title, resolved = t.Record.Title, "yes"
		}
		block.Rows = append(block.Rows, []string{t.ID, resolved, title})
	}
	a.out.Block(block)
}

func (a *app) showAttention(r *vault.Record, links query.LinkGraph) []string {
	var out []string
	if a.vault.PastHorizon(r, a.now) && r.Kind == vault.KindIdea && r.Status == "pending" {
		out = append(out, fmt.Sprintf("past its %s nudge horizon by %dd",
			a.vault.Horizon(r).String(), a.vault.AgeDays(r, a.now)-a.vault.Horizon(r).ApproxDays()))
	}
	if r.Kind == vault.KindTask && vault.TaskIsOpen(r.Status) {
		if r.HasDue && r.Due.Before(a.now) {
			out = append(out, fmt.Sprintf("was due %s, %dd ago", timeref.Format(r.Due),
				timeref.DateDiff(a.vault.Zone.DateOf(r.Due), a.vault.Zone.DateOf(a.now))))
		}
		if a.vault.PastHorizon(r, a.now) {
			who := "not checked"
			if r.Assignee != "" {
				who = "delegated to " + r.Assignee + " and not checked"
			}
			out = append(out, fmt.Sprintf("%s for %dd, past its %s follow-up horizon",
				who, a.vault.AgeDays(r, a.now), a.vault.Horizon(r).String()))
		}
	}
	for _, group := range [][]query.LinkTarget{links.Outgoing, links.Body, links.With, links.RaiseWith} {
		for _, t := range group {
			if !t.Resolved() {
				out = append(out, fmt.Sprintf("%s links to %q, which no record claims", r.ID, t.ID))
			}
		}
	}
	if r.Assignee != "" && !links.Assignee.Resolved() {
		out = append(out, fmt.Sprintf("%s is assigned to %q, which no record claims", r.ID, r.Assignee))
	}
	return out
}

func (a *app) showJSON(r *vault.Record, links query.LinkGraph) (map[string]any, error) {
	obj := map[string]any{
		"id": r.ID, "type": string(r.Kind), "path": r.Rel, "title": r.Title,
		"created": r.Created.String(), "body": r.Body,
	}
	if r.Status != "" {
		obj["status"] = r.Status
	}
	if r.HasWhen {
		obj["when"] = timeref.Format(r.When)
		if !r.Duration.IsZero() {
			obj["duration"] = r.Duration.String()
			obj["ends"] = timeref.Format(a.vault.End(r))
		}
	}
	if r.RRule != "" {
		obj["rrule"] = r.RRule
		next, ok, err := a.vault.NextOccurrence(r, a.now)
		if err != nil {
			return nil, err
		}
		if ok {
			obj["next_occurrence"] = timeref.Format(next.Start)
		}
	}
	if len(r.Exceptions) > 0 {
		obj["exceptions"] = dateStrings(r.Exceptions)
	}
	if r.Assignee != "" {
		obj["assignee"] = r.Assignee
		obj["assignee_profile"] = linkJSON([]query.LinkTarget{links.Assignee})[0]
	}
	if r.HasDue {
		obj["due"] = timeref.Format(r.Due)
		obj["due_in_days"] = timeref.DateDiff(a.vault.Zone.DateOf(a.now), a.vault.Zone.DateOf(r.Due))
	}
	if r.Kind == vault.KindTask {
		obj["follow_up_after"] = a.vault.Horizon(r).String()
		obj["past_follow_up"] = vault.TaskIsOpen(r.Status) && a.vault.PastHorizon(r, a.now)
	}
	if r.HasForge {
		link := map[string]any{"url": r.Forge.URL, "checked": r.Forge.HasStatus}
		if r.Forge.HasStatus {
			link["state"] = r.Forge.State
			link["checks"] = r.Forge.Checks
			link["checked_at"] = timeref.Format(r.Forge.CheckedAt)
			link["checked_age"] = checkedAge(a, r)
			// Always true here: show never reaches a forge, so anything it
			// reports came out of the file.
			link["cached"] = true
		}
		obj["forge"] = link
	}
	if r.HasTouched {
		obj["touched"] = r.Touched.String()
		obj["age_days"] = a.vault.AgeDays(r, a.now)
	}
	if r.Kind == vault.KindIdea || r.HasNudge {
		obj["nudge_after"] = a.vault.Horizon(r).String()
		obj["past_horizon"] = a.vault.PastHorizon(r, a.now)
	}
	if len(r.With) > 0 {
		obj["with"] = r.With
	}
	if len(r.Tags) > 0 {
		obj["tags"] = r.Tags
	}
	if len(r.FleetTasks) > 0 {
		obj["fleet_tasks"] = r.FleetTasks
	}
	if r.HasShipped {
		shipped := map[string]any{"at": timeref.Format(r.ShippedAt)}
		if r.ShippedPR != "" {
			shipped["pr"] = r.ShippedPR
		}
		obj["shipped"] = shipped
	}
	if r.HasRaised {
		obj["raised"] = r.Raised.String()
	}
	obj["links"] = linkJSON(links.Outgoing)
	obj["body_links"] = linkJSON(links.Body)
	obj["with_profiles"] = linkJSON(links.With)
	obj["raise_with"] = linkJSON(links.RaiseWith)
	back := make([]map[string]any, 0, len(links.Backlinks))
	for _, b := range links.Backlinks {
		back = append(back, map[string]any{
			"id": b.Record.ID, "type": string(b.Record.Kind), "title": b.Record.Title,
			"via": b.Via, "path": b.Record.Rel,
		})
	}
	obj["backlinks"] = back
	if r.Kind == vault.KindPerson {
		profile, err := a.engine().Person(r.ID)
		if err != nil {
			return nil, err
		}
		obj["open_items"] = a.taskRows(profile.Open)
		obj["closed_items"] = a.taskRows(profile.Closed)
		obj["agenda"] = agendaItemRows(profile.Agenda)
	}
	return obj, nil
}

// writePersonBlocks is what promotes people/ from a name a link resolves to
// into a record kind worth opening: what they are holding, what has closed, and
// what is waiting to be raised with them. All three are derived from the other
// records, so none of them can disagree with the files they came from.
func (a *app) writePersonBlocks(r *vault.Record) {
	profile, err := a.engine().Person(r.ID)
	if err != nil {
		// Every caller of show has already walked the vault successfully, so
		// this cannot fail here; reporting it rather than dropping it keeps
		// that assumption honest if it ever stops being true.
		a.out.Attention([]string{fmt.Sprintf("cannot assemble %s's profile: %v", r.ID, err)})
		return
	}
	a.out.Block(taskBlock("open_items", a.taskRows(profile.Open), "nothing is assigned to them"))
	a.out.Block(taskBlock("closed_items", a.taskRows(profile.Closed), "nothing assigned to them has closed"))
	a.out.Block(agendaBlock(agendaItemRows(profile.Agenda), "nothing is waiting to be raised with them"))
}

func linkJSON(ts []query.LinkTarget) []map[string]any {
	out := make([]map[string]any, 0, len(ts))
	for _, t := range ts {
		out = append(out, map[string]any{"id": t.ID, "resolved": t.Resolved()})
	}
	return out
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func dateStrings(ds []timeref.Date) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.String())
	}
	return out
}

func (a *app) cmdBrief() error {
	if err := a.requireArgs(0, "brief"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	days, err := a.intFlag("days", 7)
	if err != nil {
		return err
	}
	if days < 1 {
		return usageError("--days must be at least 1")
	}
	b, err := a.engine().Brief(days)
	if err != nil {
		return err
	}
	if a.out.JSON {
		stale := make([]ideaRow, 0, len(b.Stale))
		for _, r := range b.Stale {
			stale = append(stale, ideaRow{
				ID: r.Record.ID, Title: r.Record.Title, Status: r.Record.Status,
				AgeDays: r.AgeDays, HorizonDays: r.HorizonDays, PastHorizon: true,
				Created: r.Record.Created.String(), Touched: r.Record.Touched.String(), Path: r.Record.Rel,
			})
		}
		return a.out.Emit(map[string]any{
			"now": timeref.Format(a.now), "timezone": a.vault.Zone.Name(),
			"today": a.agendaRows(b.Today), "stale_ideas": stale,
			"upcoming": a.agendaRows(query.Agenda{
				From:        a.vault.Zone.DateOf(a.now).AddDays(1),
				To:          a.vault.Zone.DateOf(a.now).AddDays(days),
				Occurrences: b.Upcoming, NextIndex: -1,
			}),
			"upcoming_days":   days,
			"due_tasks":       a.taskRows(b.DueTasks),
			"unchecked_tasks": a.taskRows(b.UncheckedTasks),
		})
	}
	a.out.Scalar("today", a.vault.Zone.DateOf(a.now).String()+" "+render.WeekdayLong(a.now.Weekday()))
	todayBlock := render.Block{
		Name:    "today",
		Columns: render.Cols([]string{"when", "id", "title"}),
		Empty:   "nothing scheduled today",
	}
	for _, r := range a.agendaRows(b.Today) {
		todayBlock.Rows = append(todayBlock.Rows, []string{a.whenColumn(b.Today, r), r.ID, r.Title})
	}
	a.out.Block(todayBlock)

	upcomingBlock := render.Block{
		Name:    "upcoming",
		Columns: render.Cols([]string{"date", "when", "id", "title"}),
		Empty:   fmt.Sprintf("nothing in the next %dd", days),
	}
	for _, o := range b.Upcoming {
		clock := o.Start.Format("15:04")
		if !o.End.Equal(o.Start) {
			clock += "-" + o.End.Format("15:04")
		}
		upcomingBlock.Rows = append(upcomingBlock.Rows, []string{
			a.vault.Zone.DateOf(o.Start).String(), clock, o.Record.ID, o.Record.Title,
		})
	}
	a.out.Block(upcomingBlock)

	dueTasks := a.taskRows(b.DueTasks)
	a.out.Block(taskBlock("due_tasks", dueTasks, fmt.Sprintf("no task due in the next %dd", days)))

	unchecked := a.taskRows(b.UncheckedTasks)
	a.out.Block(taskBlock("unchecked_tasks", unchecked, "no task is past its follow-up horizon"))

	staleBlock := render.Block{
		Name:    "stale_ideas",
		Columns: render.Cols([]string{"id", "title", "age", "horizon"}, "age"),
		Empty:   "no idea is past its nudge horizon",
	}
	var attention []string
	for _, r := range b.Stale {
		staleBlock.Rows = append(staleBlock.Rows, []string{
			r.Record.ID, r.Record.Title, strconv.Itoa(r.AgeDays) + "d", strconv.Itoa(r.HorizonDays) + "d",
		})
		attention = append(attention, fmt.Sprintf("%s untouched for %dd, past its %dd horizon", r.Record.ID, r.AgeDays, r.HorizonDays))
	}
	a.out.Block(staleBlock)
	// Unchecked tasks are named after the stale ideas because they are the
	// louder half: an idea nobody looked at is a missed opportunity, a
	// delegated commitment nobody checked is a broken promise.
	attention = append(attention, taskAttention(dueTasks)...)
	for _, item := range taskAttention(unchecked) {
		if !contains(attention, item) {
			attention = append(attention, item)
		}
	}
	a.out.Attention(attention)
	a.out.Help([]string{
		"Run `brain-axi review` to triage the stale ideas and unchecked tasks",
		"Run `brain-axi week` for the whole week",
	})
	return nil
}

func (a *app) cmdDashboard() error {
	if err := a.requireArgs(0, "dashboard"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	e := a.engine()
	today, err := e.Today()
	if err != nil {
		return err
	}
	loads, week, err := e.WeekLoad()
	if err != nil {
		return err
	}
	ideas, err := e.Ideas(query.IdeaFilter{Status: "pending"})
	if err != nil {
		return err
	}
	// The dashboard reads files and nothing else. It never reaches a forge,
	// which is why it still answers on a plane.
	tasks, err := e.Tasks(query.TaskFilter{OnlyOpen: true})
	if err != nil {
		return err
	}
	var shown []query.TaskRow
	for _, r := range tasks {
		// A task earns a line when it is late, unchecked, or due inside the
		// week the frame already shows. Everything else would be a list, and a
		// dashboard that lists everything shows nothing.
		if r.Overdue || r.PastHorizon || (r.Record.HasDue && r.DueInDays >= 0 && r.DueInDays <= 7) {
			shown = append(shown, r)
		}
	}
	if a.out.JSON {
		// A dashboard asked for as JSON is the brief: the same information
		// without the frame, because frames carry no information.
		return a.cmdBrief()
	}
	d := render.Dashboard{
		Now: a.now, Zone: a.vault.Zone, Today: today, WeekLoad: loads, Week: week,
		Ideas: ideas, Tasks: shown,
	}
	d.Backlog, d.HasBacklog, d.BacklogNote = a.backlogCount()
	a.out.Dashboard(d)
	return nil
}

// backlogCount runs the configured backlog command and reads one integer from
// its output. A failure is reported in the footer rather than swallowed, and
// no configured command means no footer segment at all.
func (a *app) backlogCount() (int, bool, string) {
	cmd := strings.TrimSpace(a.vault.Config.BacklogCmd)
	if cmd == "" {
		return 0, false, ""
	}
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil {
		return 0, false, "backlog_cmd failed"
	}
	text := strings.TrimSpace(string(out))
	n, convErr := strconv.Atoi(text)
	if convErr != nil {
		return 0, false, "backlog_cmd did not return a number"
	}
	return n, true, ""
}
