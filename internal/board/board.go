// Package board builds the five-pane board and renders it two ways.
//
// There is exactly one assembly path, Build, and two renderers over its result.
// That is the whole design: an agent asked to "show the board" cannot re-author
// what a pane is, what order the panes come in, or what an empty week says,
// because none of those live in a prompt or in a per-run decision. The layout
// and every empty-state string, pane key and pane position live in
// templates/board.html; nothing else may add or reorder them.
package board

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Thanhbinh1905/secondbrain/internal/payload"
	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/render"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
	"github.com/Thanhbinh1905/secondbrain/templates"
)

// Schema is the versioned data contract. A payload that does not carry exactly
// this string is refused rather than rendered: a board built by a different
// version of this tool is a board whose panes may mean something else.
const Schema = "brain-board.v1"

// Slot is where a built page keeps its payload.
var Slot = payload.Slot{Marker: templates.DataSlot, ElementID: "board-data"}

// PaneSpec is one pane's fixed identity: its key, its heading, and what it says
// when it holds nothing.
//
// Every pane has its own empty-state string so an empty week renders as an
// empty week rather than as a missing pane. A pane is never dropped.
type PaneSpec struct {
	Key   string
	Title string
	Empty string
}

// Panes are read from the committed template, which is the single owner of
// pane identity, order, headings and empty-state strings.
var Panes = mustTemplatePanes()

func mustTemplatePanes() []PaneSpec {
	const open = `<script id="board-contract" type="application/json">`
	start := strings.Index(templates.Board, open)
	if start < 0 {
		panic("templates/board.html carries no board-contract block")
	}
	start += len(open)
	end := strings.Index(templates.Board[start:], "</script>")
	if end < 0 {
		panic("templates/board.html board-contract block is not closed")
	}
	var panes []PaneSpec
	if err := json.Unmarshal([]byte(templates.Board[start:start+end]), &panes); err != nil {
		panic(fmt.Sprintf("templates/board.html board-contract is invalid: %v", err))
	}
	if len(panes) == 0 {
		panic("templates/board.html board-contract declares no panes")
	}
	seen := make(map[string]bool, len(panes))
	for i, pane := range panes {
		if strings.TrimSpace(pane.Key) == "" || strings.TrimSpace(pane.Title) == "" || strings.TrimSpace(pane.Empty) == "" {
			panic(fmt.Sprintf("templates/board.html pane %d must have a key, a title and an empty state", i+1))
		}
		if seen[pane.Key] {
			panic(fmt.Sprintf("templates/board.html declares pane key %q more than once", pane.Key))
		}
		seen[pane.Key] = true
	}
	return panes
}

// RowKinds are the closed vocabulary a row's kind is drawn from.
var RowKinds = []string{"event", "agenda", "task", "idea"}

// Model is a whole board. Every field is always present, so a reader never has
// to tell an absent key from an empty one.
type Model struct {
	Schema    string `json:"schema"`
	Generated string `json:"generated"`
	Timezone  string `json:"timezone"`
	Panes     []Pane `json:"panes"`
}

// Pane is one pane of the board.
type Pane struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Empty string `json:"empty"`
	Rows  []Row  `json:"rows"`
}

// Row is one line of a pane. The shape is deliberately flat and identical in
// every pane: a renderer that has to branch on pane to read a row is a renderer
// that can disagree with the other one.
type Row struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Flag   string `json:"flag"`
	Note   string `json:"note"`
	Path   string `json:"path"`
}

// Build assembles the board. It is the only place a board's contents are
// decided, and both renderers take its result unchanged.
func Build(e *query.Engine) (Model, error) {
	z := e.Vault.Zone
	m := Model{
		Schema:    Schema,
		Generated: timeref.Format(e.Now),
		Timezone:  z.Name(),
		Panes:     make([]Pane, 0, len(Panes)),
	}

	today, err := e.Today()
	if err != nil {
		return Model{}, err
	}
	week, err := e.Week()
	if err != nil {
		return Model{}, err
	}
	agendas, err := e.PersonAgendas()
	if err != nil {
		return Model{}, err
	}
	tasks, err := e.Tasks(query.TaskFilter{OnlyOpen: true})
	if err != nil {
		return Model{}, err
	}
	ideas, err := e.Ideas(query.IdeaFilter{Status: "pending"})
	if err != nil {
		return Model{}, err
	}

	rows := map[string][]Row{
		"today":   occurrenceRows(e, today, agendas, false),
		"week":    occurrenceRows(e, week, agendas, true),
		"tasks":   taskRows(e, tasks, false),
		"ideas":   ideaRows(e, ideas),
		"waiting": taskRows(e, tasks, true),
	}
	for _, spec := range Panes {
		pane := Pane{Key: spec.Key, Title: spec.Title, Empty: spec.Empty, Rows: rows[spec.Key]}
		// The model invariant is established here, at the one place a board is
		// assembled, exactly as recap.Build does it for its blocks: every pane
		// carries a list. Each row builder above already returns one, and this
		// is what keeps that true of the next one somebody adds.
		if pane.Rows == nil {
			pane.Rows = []Row{}
		}
		m.Panes = append(m.Panes, pane)
	}
	return m, nil
}

// occurrenceRows renders an agenda, following each event with whatever is
// waiting to be raised with the people it is with.
//
// The agenda rows are the point of the people layer: the thing to raise with
// somebody is only useful in the thirty seconds before walking into the room
// with them, which is exactly where the event row already is.
func occurrenceRows(e *query.Engine, ag query.Agenda, agendas map[string][]query.AgendaItem, withDate bool) []Row {
	next, hasNext := ag.Next()
	// Never nil: a nil slice marshals to null, and the payload contract this
	// package validates against requires a list. An empty pane is an empty
	// list.
	out := make([]Row, 0, len(ag.Occurrences))
	// One agenda row per item per pane, however many meetings the person is in.
	shown := map[string]bool{}
	for _, occ := range ag.Occurrences {
		detail := occ.Start.Format("15:04")
		if !occ.End.Equal(occ.Start) {
			detail += "-" + occ.End.Format("15:04")
		}
		if withDate {
			detail = e.Vault.Zone.DateOf(occ.Start).String() + " " + detail
		}
		var flags []string
		if hasNext && occ.Record.ID == next.Record.ID && occ.Start.Equal(next.Start) {
			flags = append(flags, "next")
		}
		if occ.Recurring {
			flags = append(flags, "recurring")
		}
		if occ.Record.Status != "scheduled" {
			flags = append(flags, occ.Record.Status)
		}
		out = append(out, Row{
			ID: occ.Record.ID, Kind: "event", Title: occ.Record.Title, Detail: detail,
			Flag: strings.Join(flags, "+"), Note: strings.Join(occ.Record.With, " "), Path: occ.Record.Rel,
		})
		for _, who := range occ.Record.With {
			for _, item := range agendas[who] {
				key := who + "\x00" + item.Record.ID
				if shown[key] {
					continue
				}
				shown[key] = true
				out = append(out, Row{
					ID: item.Record.ID, Kind: "agenda", Title: item.Record.Title,
					Detail: strconv.Itoa(item.WaitingDays) + "d waiting",
					Note:   "raise with " + who, Path: item.Record.Rel,
				})
			}
		}
	}
	return out
}

// taskRows splits the open tasks in two: what is on the user's own desk, and
// what somebody else is holding. They are different questions and they get
// different panes.
func taskRows(e *query.Engine, rows []query.TaskRow, delegated bool) []Row {
	// Never nil, for the same reason as occurrenceRows: an empty waiting pane
	// is the ordinary case even on a busy vault.
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		if (row.Record.Assignee != "") != delegated {
			continue
		}
		detail := render.EmptyCell
		if row.Record.HasDue {
			detail = e.Vault.Zone.DateOf(row.Record.Due).String()
		}
		var flags []string
		if row.Overdue {
			flags = append(flags, fmt.Sprintf("overdue-%dd", -row.DueInDays))
		}
		if row.PastHorizon {
			flags = append(flags, fmt.Sprintf("unchecked-%dd", row.AgeDays))
		}
		note := row.Record.Status
		if row.Record.Assignee != "" {
			note = row.Record.Assignee + ", " + row.Record.Status
		}
		out = append(out, Row{
			ID: row.Record.ID, Kind: "task", Title: row.Record.Title, Detail: detail,
			Flag: strings.Join(flags, "+"), Note: note, Path: row.Record.Rel,
		})
	}
	return out
}

func ideaRows(e *query.Engine, rows []query.IdeaRow) []Row {
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		flag := ""
		switch {
		case e.Vault.Dormant(row.Record, e.Now):
			flag = fmt.Sprintf("dormant-%dd", row.AgeDays)
		case row.PastHorizon:
			flag = fmt.Sprintf("stale-%dd", row.AgeDays)
		}
		out = append(out, Row{
			ID: row.Record.ID, Kind: "idea", Title: row.Record.Title,
			Detail: strconv.Itoa(row.AgeDays) + "d", Flag: flag,
			Note: "horizon " + strconv.Itoa(row.HorizonDays) + "d", Path: row.Record.Rel,
		})
	}
	return out
}

// Validate checks a payload against brain-board.v1 and refuses anything that
// does not satisfy it, naming the line.
//
// It is deliberately usable on a payload this build did not write: doctor reads
// the payload back out of the board file, which a person may have edited and an
// older binary may have written.
func Validate(path string, raw []byte, lineOffset int) (Model, error) {
	if err := requireBoardFields(path, raw, lineOffset); err != nil {
		return Model{}, err
	}
	var m Model
	if err := payload.Decode(path, raw, lineOffset, &m); err != nil {
		return Model{}, err
	}
	if m.Schema != Schema {
		found := m.Schema
		if found == "" {
			found = "(none)"
		}
		return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "schema", lineOffset),
			"unsupported board schema %s: this build writes and reads %s", found, Schema)
	}
	for _, field := range []struct{ key, value string }{
		{"generated", m.Generated}, {"timezone", m.Timezone},
	} {
		if strings.TrimSpace(field.value) == "" {
			return Model{}, payload.Errorf(path, payload.LineOfKey(raw, field.key, lineOffset),
				"%s must not be empty", field.key)
		}
	}
	if _, err := timeref.ParseStored(m.Generated); err != nil {
		return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "generated", lineOffset), "generated: %v", err)
	}
	if len(m.Panes) != len(Panes) {
		return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "panes", lineOffset),
			"a board has exactly %d panes, found %d: %s", len(Panes), len(m.Panes), strings.Join(paneKeys(), ", "))
	}
	for i, spec := range Panes {
		pane := m.Panes[i]
		if pane.Key != spec.Key {
			return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "key", lineOffset),
				"pane %d is %q, and the pane set is fixed in this order: %s", i+1, pane.Key, strings.Join(paneKeys(), ", "))
		}
		if strings.TrimSpace(pane.Title) == "" {
			return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "title", lineOffset),
				"pane %q has no title", pane.Key)
		}
		// An empty pane with no empty-state string renders as a missing pane,
		// which is the one way this surface can lie about the vault.
		if strings.TrimSpace(pane.Empty) == "" {
			return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "empty", lineOffset),
				"pane %q has no empty-state string; an empty pane must say so in words", pane.Key)
		}
		if pane.Title != spec.Title || pane.Empty != spec.Empty {
			return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "title", lineOffset),
				"pane %q identity differs from the committed template", pane.Key)
		}
		for j, row := range pane.Rows {
			if strings.TrimSpace(row.ID) == "" {
				return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "rows", lineOffset),
					"row %d of pane %q has no id", j+1, pane.Key)
			}
			if !known(RowKinds, row.Kind) {
				return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "kind", lineOffset),
					"row %d of pane %q has kind %q: valid kinds are %s", j+1, pane.Key, row.Kind, strings.Join(RowKinds, ", "))
			}
		}
	}
	return m, nil
}

func requireBoardFields(path string, raw []byte, lineOffset int) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	if err := requireFields(path, raw, lineOffset, root, "schema", "generated", "timezone", "panes"); err != nil {
		return err
	}
	var panes []json.RawMessage
	if err := json.Unmarshal(root["panes"], &panes); err != nil {
		return nil
	}
	for _, paneRaw := range panes {
		var pane map[string]json.RawMessage
		if json.Unmarshal(paneRaw, &pane) != nil {
			continue
		}
		if err := requireFields(path, raw, lineOffset, pane, "key", "title", "empty", "rows"); err != nil {
			return err
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(pane["rows"], &rows); err != nil {
			continue
		}
		for _, rowRaw := range rows {
			var row map[string]json.RawMessage
			if json.Unmarshal(rowRaw, &row) != nil {
				continue
			}
			if err := requireFields(path, raw, lineOffset, row, "id", "kind", "title", "detail", "flag", "note", "path"); err != nil {
				return err
			}
		}
	}
	return nil
}

func requireFields(path string, raw []byte, lineOffset int, object map[string]json.RawMessage, fields ...string) error {
	for _, field := range fields {
		value, ok := object[field]
		if !ok || string(value) == "null" {
			return payload.Errorf(path, payload.LineOfKey(raw, field, lineOffset), "%s is required and must not be null", field)
		}
	}
	return nil
}

func paneKeys() []string {
	out := make([]string, 0, len(Panes))
	for _, p := range Panes {
		out = append(out, p.Key)
	}
	return out
}

func known(vocab []string, v string) bool {
	for _, s := range vocab {
		if s == v {
			return true
		}
	}
	return false
}

// RenderHTML builds the self-contained page.
//
// It validates the model before touching anything, injects the escaped payload
// into the committed template, and then reads the payload back out of the built
// page and validates it again. A page that would fail to load in a browser
// fails here instead, before it can replace one that loaded.
func RenderHTML(m Model) ([]byte, error) {
	raw, err := payload.Marshal(m)
	if err != nil {
		return nil, err
	}
	if _, err := Validate("<board payload>", raw, 0); err != nil {
		return nil, err
	}
	page, err := payload.Inject(templates.Board, Slot, payload.Escape(raw))
	if err != nil {
		return nil, err
	}
	back, line, err := payload.Extract(page, Slot)
	if err != nil {
		return nil, fmt.Errorf("the built board cannot be read back: %w", err)
	}
	if _, err := Validate("<built board>", back, line); err != nil {
		return nil, fmt.Errorf("the built board does not carry a readable payload: %w", err)
	}
	return []byte(page), nil
}

// RenderASCII writes the framed board, or plain lines when stdout is a pipe.
// It reads the same Model the HTML renderer does, so the two cannot disagree
// about what is on the board.
func RenderASCII(o *render.Out, m Model) {
	f := Frame(o, m)
	if o.TTY {
		fmt.Fprint(o.W, f.String())
		return
	}
	fmt.Fprint(o.W, f.Plain())
}

// Frame lays the model out. It is exported so a test can assert that the two
// renderers are fed the same Model value.
func Frame(o *render.Out, m Model) *render.Frame {
	f := render.NewFrame(o.FrameWidth(render.DefaultFrameWidth), "brain-axi board", m.Generated)
	for i, pane := range m.Panes {
		if i > 0 {
			f.Rule()
		}
		f.Line(" " + strings.ToUpper(pane.Title))
		if len(pane.Rows) == 0 {
			f.Line("   " + pane.Empty)
			continue
		}
		// The detail column is padded to its widest cell within the pane, so a
		// column of times and a column of ages both read down rather than
		// ragged. Padding counts display cells, not bytes, so an accented or
		// wide-character row lines up with an ASCII one.
		width := 0
		for _, row := range pane.Rows {
			// An agenda row hangs under the event it belongs to and is already
			// indented past it, so it is left out of the column width rather
			// than stretching every other row to match it.
			if row.Kind == "agenda" {
				continue
			}
			if w := unitext.Width(row.Detail); w > width {
				width = w
			}
		}
		for _, row := range pane.Rows {
			indent, detail := "   ", unitext.PadLeft(row.Detail, width)
			if row.Kind == "agenda" {
				indent, detail = "     - ", row.Detail
			}
			left := indent + detail + "  " + row.Title
			right := row.Flag
			if right == "" {
				right = row.Note
			}
			if right != "" {
				right += " "
			}
			f.Row(left, right)
		}
	}
	f.Caption = caption(m)
	return f
}

// caption is the bottom rule: how many rows each pane holds, so the shape of
// the board is legible before any of it is read.
func caption(m Model) string {
	parts := make([]string, 0, len(m.Panes))
	for _, pane := range m.Panes {
		parts = append(parts, fmt.Sprintf("%s %d", pane.Key, len(pane.Rows)))
	}
	return strings.Join(parts, " · ")
}
