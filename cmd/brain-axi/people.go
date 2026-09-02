package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/render"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// dueRow is one thing that needs attention right now, in JSON.
type dueRow struct {
	Category string `json:"category"`
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Reason   string `json:"reason"`
	// Person is who a delegated item sits with. It is the first thing a caller
	// should show, because it is what makes the item actionable.
	Person       string `json:"person,omitempty"`
	Start        string `json:"start,omitempty"`
	MinutesUntil int    `json:"minutes_until,omitempty"`
	Days         int    `json:"days,omitempty"`
	Window       string `json:"window,omitempty"`
	Path         string `json:"path"`
}

// cmdDue answers one question: what needs attention right now.
//
// Silence is the ordinary answer. A command meant to be run on a short interval
// that prints something every time is a command whose output stops being read,
// so with nothing due this writes nothing at all and exits zero.
func (a *app) cmdDue() error {
	if err := a.requireArgs(0, "due"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	filter := query.DueFilter{
		Delegated: a.has("delegated"), Events: a.has("events"), Ideas: a.has("ideas"),
	}
	items, err := a.engine().Due(filter)
	if err != nil {
		return err
	}
	rows := make([]dueRow, 0, len(items))
	for _, item := range items {
		row := dueRow{
			Category: string(item.Category), ID: item.Record.ID, Kind: string(item.Record.Kind),
			Title: item.Record.Title, Reason: item.Reason, Person: item.Person,
			Days: item.Days, Window: item.WindowLabel, Path: item.Record.Rel,
		}
		if item.Category == query.DueEvent {
			row.Start = timeref.Format(item.Start)
			row.MinutesUntil = item.MinutesUntil
		}
		rows = append(rows, row)
	}
	if a.out.JSON {
		return a.out.Emit(map[string]any{"now": timeref.Format(a.now), "due": rows})
	}
	// Not one byte when nothing is due, so a caller can treat any output as a
	// reason to interrupt.
	if len(rows) == 0 {
		return nil
	}
	block := render.Block{
		Name:    "due",
		Columns: render.Cols([]string{"category", "id", "who", "title", "why"}),
	}
	for _, r := range rows {
		who := r.Person
		if who == "" {
			who = render.EmptyCell
		}
		block.Rows = append(block.Rows, []string{r.Category, r.ID, who, r.Title, r.Reason})
	}
	a.out.Block(block)
	return nil
}

// relatedRow is one end of a link, in JSON.
type relatedRow struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Via names the fields that did the pointing: links, body, with, assignee
	// or raise_with. "This meeting produced that idea" and "that idea mentions
	// this meeting in passing" are different answers.
	Via      []string `json:"via"`
	Resolved bool     `json:"resolved"`
	Path     string   `json:"path"`
}

// cmdRelated is the linking layer's read side: everything this record points
// at, and everything that points at it.
func (a *app) cmdRelated() error {
	if err := a.requireArgs(1, "related"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	r, err := a.vault.Find(a.args[0])
	if err != nil {
		return err
	}
	links, err := a.engine().Links(r)
	if err != nil {
		return err
	}

	var out []relatedRow
	add := func(via string, targets []query.LinkTarget) {
		for _, t := range targets {
			row := relatedRow{ID: t.ID, Via: []string{via}, Resolved: t.Resolved()}
			if t.Resolved() {
				row.Kind, row.Title, row.Path = string(t.Record.Kind), t.Record.Title, t.Record.Rel
			}
			out = append(out, row)
		}
	}
	add("links", links.Outgoing)
	add("body", links.Body)
	add("with", links.With)
	add("raise_with", links.RaiseWith)
	if r.Assignee != "" {
		add("assignee", []query.LinkTarget{links.Assignee})
	}

	back := make([]relatedRow, 0, len(links.Backlinks))
	for _, b := range links.Backlinks {
		back = append(back, relatedRow{
			ID: b.Record.ID, Kind: string(b.Record.Kind), Title: b.Record.Title,
			Via: b.Via, Resolved: true, Path: b.Record.Rel,
		})
	}

	if a.out.JSON {
		return a.out.Emit(map[string]any{
			"id": r.ID, "type": string(r.Kind), "path": r.Rel, "title": r.Title,
			"points_to": relatedJSON(out), "pointed_to_by": relatedJSON(back),
		})
	}
	a.out.Scalar("id", r.ID)
	a.out.Scalar("type", string(r.Kind))
	a.out.Scalar("path", r.Rel)
	a.out.Block(relatedBlock("points_to", out, "this record points at nothing"))
	a.out.Block(relatedBlock("pointed_to_by", back, "nothing points at this record"))
	var attention []string
	for _, row := range out {
		if !row.Resolved {
			attention = append(attention, fmt.Sprintf("%s points at %q, which no record claims", r.ID, row.ID))
		}
	}
	a.out.Attention(attention)
	a.out.Help([]string{
		"Run `brain-axi show <id>` for either end of a link",
		fmt.Sprintf("Open %s to correct its links: field by hand", r.Rel),
	})
	return nil
}

func relatedBlock(name string, rows []relatedRow, empty string) render.Block {
	block := render.Block{
		Name:    name,
		Columns: render.Cols([]string{"id", "type", "via", "resolved", "title"}),
		Empty:   empty,
	}
	for _, r := range rows {
		kind := r.Kind
		if kind == "" {
			kind = render.EmptyCell
		}
		resolved := "no"
		if r.Resolved {
			resolved = "yes"
		}
		block.Rows = append(block.Rows, []string{r.ID, kind, strings.Join(r.Via, "+"), resolved, r.Title})
	}
	return block
}

func relatedJSON(rows []relatedRow) []relatedRow {
	if rows == nil {
		return []relatedRow{}
	}
	return rows
}

// agendaItemRow is one thing waiting to be raised, in JSON.
type agendaItemRow struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Status      string `json:"status,omitempty"`
	WaitingDays int    `json:"waiting_days"`
	Path        string `json:"path"`
}

func agendaItemRows(items []query.AgendaItem) []agendaItemRow {
	out := make([]agendaItemRow, 0, len(items))
	for _, item := range items {
		out = append(out, agendaItemRow{
			ID: item.Record.ID, Kind: string(item.Record.Kind), Title: item.Record.Title,
			Status: item.Record.Status, WaitingDays: item.WaitingDays, Path: item.Record.Rel,
		})
	}
	return out
}

func agendaBlock(items []agendaItemRow, empty string) render.Block {
	block := render.Block{
		Name:    "agenda",
		Columns: render.Cols([]string{"id", "type", "title", "waiting"}, "waiting"),
		Empty:   empty,
	}
	for _, item := range items {
		block.Rows = append(block.Rows, []string{
			item.ID, item.Kind, item.Title, strconv.Itoa(item.WaitingDays) + "d",
		})
	}
	return block
}

// cmdPersonAgenda is what to raise with somebody, which is only useful in the
// thirty seconds before walking into a room with them. It is deliberately one
// short answer rather than their whole profile.
func (a *app) cmdPersonAgenda() error {
	if err := a.requireArgs(1, "agenda"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	// The two forms of this command must not be silently mixed: a range given
	// beside a person id is a misread, not a narrowing.
	for _, flag := range []string{"from", "to"} {
		if a.has(flag) {
			return usageError("agenda takes a person id or a --from/--to range, not both; drop --%s or drop the id", flag)
		}
	}
	id := a.args[0]
	if err := vault.ValidateID(id); err != nil {
		return usageError("agenda takes a person id or a --from/--to range: %v", err)
	}
	profile, err := a.engine().Person(id)
	if err != nil {
		return err
	}
	// A person nobody has a profile for and nothing points at is a typo, not an
	// empty agenda, and saying so is more use than printing nothing.
	if profile.Record == nil && len(profile.Agenda) == 0 && len(profile.Open) == 0 && len(profile.Closed) == 0 {
		return usageError("no person %q, and no record names them; run `brain-axi add person \"...\"` or check the id", id)
	}
	items := agendaItemRows(profile.Agenda)
	if a.out.JSON {
		obj := map[string]any{
			"person": id, "now": timeref.Format(a.now), "agenda": items,
			"has_profile": profile.Record != nil,
		}
		if profile.Record != nil {
			obj["title"] = profile.Record.Title
			obj["path"] = profile.Record.Rel
		}
		return a.out.Emit(obj)
	}
	a.out.Scalar("person", id)
	if profile.Record != nil {
		a.out.Scalar("title", profile.Record.Title)
		a.out.Scalar("path", profile.Record.Rel)
	}
	a.out.Block(agendaBlock(items, "nothing is waiting to be raised with them"))
	var attention []string
	if profile.Record == nil {
		attention = append(attention, fmt.Sprintf("no people/ record claims %q, though other records name them", id))
	}
	a.out.Attention(attention)
	a.out.Help([]string{
		fmt.Sprintf("Run `brain-axi show %s` for what they are holding as well", id),
		"Run `brain-axi update <id> --set raised=<date>` once an item has been raised",
	})
	return nil
}
