// Package review is the one workflow in this tool with genuine interactive
// state: deciding what to do with each stale idea and each unchecked task.
//
// Six ideas each needing a keep / start / drop / defer decision is twelve
// exchanges through chat and six keystrokes as one screen. That is the whole
// justification, and it is why nothing else here is interactive: there is no
// event loop, no alternate screen buffer, no redraw and no resize handling.
// Each card is printed, one byte is read, the decision is written, and the
// next card is printed.
//
// The decision logic is separated from the terminal so it can be tested
// without one.
package review

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/render"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// Action is what the user chose for one idea.
type Action string

const (
	// Keep marks the record reviewed, which resets its horizon.
	Keep Action = "keep"
	// Build moves an idea into building.
	Build Action = "build"
	// Done closes a task that actually happened.
	Done Action = "done"
	// Drop closes it.
	Drop Action = "drop"
	// Defer pushes its horizon out by DeferDays.
	Defer Action = "defer"
	// Quit ends the session without touching the current record.
	Quit Action = "quit"
)

// DeferDays is how far a defer pushes a horizon out.
const DeferDays = 30

// Binding is one key the review screen accepts.
type Binding struct {
	Key    byte
	Action Action
	// Label is the caption shown in the key bar.
	Label string
}

// ideaBindings are the review screen's keys for an idea.
var ideaBindings = []Binding{
	{'k', Keep, "keep"},
	{'b', Build, "build"},
	{'d', Drop, "drop"},
	{'s', Defer, fmt.Sprintf("defer %d days", DeferDays)},
	{'q', Quit, "quit"},
}

// taskBindings are the keys for a task. The verbs differ because the decisions
// differ: an idea can be started, a follow-up can only be closed, dropped or
// pushed out. Reusing one key for two meanings would make the same keystroke
// do different things on consecutive cards, which is how a fast triage session
// writes the wrong decision.
var taskBindings = []Binding{
	{'k', Keep, "keep"},
	{'x', Done, "done"},
	{'d', Drop, "drop"},
	{'s', Defer, fmt.Sprintf("defer %d days", DeferDays)},
	{'q', Quit, "quit"},
}

// BindingsFor is the key set for a record kind.
func BindingsFor(k vault.Kind) []Binding {
	if k == vault.KindTask {
		return taskBindings
	}
	return ideaBindings
}

// ActionForKey resolves a keystroke against a kind's own key set. Escape and
// Ctrl-C quit, because a terminal in raw mode must always have a way out.
func ActionForKey(k vault.Kind, b byte) (Action, bool) {
	for _, bind := range BindingsFor(k) {
		if b == bind.Key || b == bind.Key-32 { // accept the upper-case key too
			return bind.Action, true
		}
	}
	switch b {
	case 3, 4, 27: // Ctrl-C, Ctrl-D, Escape
		return Quit, true
	}
	return "", false
}

// KeyBar renders the key legend shown under a kind's card.
func KeyBar(k vault.Kind) string {
	bindings := BindingsFor(k)
	parts := make([]string, 0, len(bindings))
	for _, b := range bindings {
		parts = append(parts, fmt.Sprintf("%c %s", b.Key, b.Label))
	}
	return strings.Join(parts, "   ")
}

// Change is one frontmatter key an action rewrites.
type Change struct {
	Key   string
	Value string
}

// Plan returns the frontmatter changes an action makes to a record, without
// writing anything. Quit changes nothing.
//
// Every action that is not Quit sets touched:, because the user having
// looked at the record is itself the thing that resets its age.
func Plan(v *vault.Vault, r *vault.Record, a Action, now time.Time) ([]Change, error) {
	today := v.Zone.DateOf(now).String()
	// A task spells its horizon follow_up_after: and an idea nudge_after:, so
	// deferring writes whichever key this record's kind actually reads.
	horizonKey := "nudge_after"
	if r.Kind == vault.KindTask {
		horizonKey = "follow_up_after"
	}
	switch a {
	case Keep:
		return []Change{{"touched", today}}, nil
	case Build:
		if r.Kind == vault.KindTask {
			return nil, fmt.Errorf("a task cannot be moved to building; its statuses are %s", strings.Join(vault.TaskStatuses, ", "))
		}
		return []Change{{"status", "building"}, {"touched", today}}, nil
	case Done:
		if r.Kind != vault.KindTask {
			return nil, fmt.Errorf("%q closes a task; an %s is finished with `b`", Done, r.Kind)
		}
		return []Change{{"status", "done"}, {"touched", today}}, nil
	case Drop:
		return []Change{{"status", "dropped"}, {"touched", today}}, nil
	case Defer:
		return []Change{{"touched", today}, {horizonKey, timeref.Span{Days: DeferDays}.String()}}, nil
	case Quit:
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown review action %q", a)
	}
}

// Apply writes an action's changes to the record's file, preserving the body
// byte-for-byte and every frontmatter key the action does not name.
func Apply(v *vault.Vault, r *vault.Record, a Action, now time.Time) ([]Change, error) {
	changes, err := Plan(v, r, a, now)
	if err != nil || len(changes) == 0 {
		return nil, err
	}
	doc := r.Doc()
	for _, c := range changes {
		doc.Set(c.Key, c.Value)
	}
	if err := v.Save(r.Rel, doc); err != nil {
		return nil, err
	}
	return changes, nil
}

// Item is one record awaiting a decision. Ideas and tasks both decay and both
// get triaged the same way, so the screen takes the shape they share rather
// than one type per kind.
type Item struct {
	Record      *vault.Record
	AgeDays     int
	HorizonDays int
}

// FromIdeas adapts an idea listing into review items.
func FromIdeas(rows []query.IdeaRow) []Item {
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		out = append(out, Item{Record: r.Record, AgeDays: r.AgeDays, HorizonDays: r.HorizonDays})
	}
	return out
}

// FromTasks adapts a task listing into review items.
func FromTasks(rows []query.TaskRow) []Item {
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		out = append(out, Item{Record: r.Record, AgeDays: r.AgeDays, HorizonDays: r.HorizonDays})
	}
	return out
}

// Card renders one record's review screen.
func Card(o *render.Out, item Item, index, total int, zone timeref.Zone) *render.Frame {
	width := o.FrameWidth(render.DefaultFrameWidth)
	f := render.NewFrame(width, "weekly review", fmt.Sprintf("%d / %d", index+1, total))
	f.Blank()
	f.Row(" "+item.Record.ID, fmt.Sprintf("%dd ", item.AgeDays))
	if item.Record.Title != "" {
		f.Line(" " + item.Record.Title)
	}
	if item.Record.Kind == vault.KindTask {
		// Who has it and when it was due are the two facts that decide a
		// follow-up, so the card leads with them rather than burying them.
		var parts []string
		if item.Record.Assignee != "" {
			parts = append(parts, "assigned to "+item.Record.Assignee)
		}
		if item.Record.HasDue {
			parts = append(parts, "due "+item.Record.Due.In(zone.Loc).Format("2006-01-02 15:04"))
		}
		parts = append(parts, item.Record.Status)
		f.Line(" " + strings.Join(parts, " · "))
	}
	f.Blank()
	for _, line := range wrapBody(item.Record.Body, f.Inner()-1) {
		f.Line(" " + line)
	}
	f.Blank()
	meta := fmt.Sprintf(" created %s", item.Record.Created)
	if item.Record.HasTouched {
		meta += fmt.Sprintf(" · touched %s", item.Record.Touched)
	}
	horizonWord := "nudge"
	if item.Record.Kind == vault.KindTask {
		horizonWord = "follow up"
	}
	meta += fmt.Sprintf(" · %s %dd", horizonWord, item.HorizonDays)
	f.Line(meta)
	f.Blank()
	f.Rule()
	f.Line(" " + KeyBar(item.Record.Kind))
	return f
}

// wrapBody folds a body to the card's width, dropping blank leading lines.
func wrapBody(body string, width int) []string {
	var out []string
	for _, para := range strings.Split(strings.TrimSpace(body), "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			if len(out) > 0 {
				out = append(out, "")
			}
			continue
		}
		cur := ""
		for _, word := range strings.Fields(para) {
			candidate := word
			if cur != "" {
				candidate = cur + " " + word
			}
			if unitext.Width(candidate) > width && cur != "" {
				out = append(out, cur)
				cur = word
				continue
			}
			cur = candidate
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	if len(out) == 0 {
		return []string{"(no body)"}
	}
	return out
}

// Reader supplies one keystroke at a time. A terminal in raw mode satisfies
// it, and so does a fixed byte sequence in a test.
type Reader interface {
	// ReadKey returns the next keystroke, or an error at end of input.
	ReadKey() (byte, error)
}

// Summary is what a review session did.
type Summary struct {
	// Decisions are the records acted on, in the order they were reviewed.
	Decisions []Decision
	// Reviewed is how many cards were shown.
	Reviewed int
	// Quit reports whether the user ended the session early.
	Quit bool
}

// Decision is one record and what happened to it.
type Decision struct {
	ID      string
	Action  Action
	Changes []Change
}

// Run shows each stale idea in turn and applies the user's decision.
//
// A write happens immediately after each keystroke rather than at the end, so
// an interrupted session keeps every decision already made.
func Run(o *render.Out, v *vault.Vault, items []Item, in Reader, now time.Time) (Summary, error) {
	var s Summary
	for i, item := range items {
		card := Card(o, item, i, len(items), v.Zone)
		if o.TTY {
			fmt.Fprint(o.W, card.String())
		} else {
			fmt.Fprint(o.W, card.Plain())
		}
		action, err := readAction(o.W, in, item.Record.Kind)
		if err != nil {
			return s, err
		}
		s.Reviewed++
		if action == Quit {
			s.Quit = true
			return s, nil
		}
		changes, err := Apply(v, item.Record, action, now)
		if err != nil {
			return s, err
		}
		s.Decisions = append(s.Decisions, Decision{ID: item.Record.ID, Action: action, Changes: changes})
	}
	return s, nil
}

// readAction reads keystrokes until one of them binds to an action for this
// kind. A key that means something for an idea but not for a task is refused
// rather than reinterpreted.
func readAction(w io.Writer, in Reader, kind vault.Kind) (Action, error) {
	for {
		b, err := in.ReadKey()
		if err != nil {
			return "", err
		}
		if a, ok := ActionForKey(kind, b); ok {
			return a, nil
		}
		fmt.Fprintf(w, "key %q is not available; use: %s\r\n", printable(b), KeyBar(kind))
	}
}

func printable(b byte) string {
	if b >= 32 && b < 127 {
		return string(rune(b))
	}
	return fmt.Sprintf("\\x%02x", b)
}
