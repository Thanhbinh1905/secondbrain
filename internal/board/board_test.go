package board

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Thanhbinh1905/secondbrain/internal/payload"
	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/render"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
	"github.com/Thanhbinh1905/secondbrain/templates"
)

func fixture(t *testing.T, stamp string) *query.Engine {
	t.Helper()
	v, err := vault.OpenAt(filepath.Join("..", "vault", "testdata", "good"))
	if err != nil {
		t.Fatal(err)
	}
	now, err := v.Zone.Normalise(stamp)
	if err != nil {
		t.Fatal(err)
	}
	return query.New(v, now)
}

func built(t *testing.T, stamp string) Model {
	t.Helper()
	m, err := Build(fixture(t, stamp))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestTheTemplateOwnsTheMarkup: the committed template carries exactly one data
// slot, and building a board substitutes that one line and nothing else. That
// is what stops an agent re-authoring the board's markup on every run.
func TestTheTemplateOwnsTheMarkup(t *testing.T) {
	if n := strings.Count(templates.Board, templates.DataSlot); n != 1 {
		t.Fatalf("the committed template carries %d data slots, want exactly 1", n)
	}
	m := built(t, "2026-09-02T12:00")
	page, err := RenderHTML(m)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := payload.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(templates.Board, templates.DataSlot, string(payload.Escape(raw)), 1)
	if string(page) != want {
		t.Error("the built page is not the committed template with only its data slot replaced")
	}
}

// TestTemplateDeclaresTheClosedPaneSet asserts the brain-board.v1 contract
// itself: the committed template must declare exactly these five pane keys,
// in exactly this order. A template edit that adds, drops, renames or
// reorders a pane changes this versioned contract and must fail the build,
// not render silently.
func TestTemplateDeclaresTheClosedPaneSet(t *testing.T) {
	want := []string{"today", "week", "tasks", "ideas", "waiting"}
	got := make([]string, len(Panes))
	for i, pane := range Panes {
		got[i] = pane.Key
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("templates/board.html declares pane keys %v, want %v", got, want)
	}
}

// TestBothRenderersReadTheSameModel is the point of having one assembly path:
// the framed board and the HTML page cannot disagree, because the HTML carries
// the model verbatim and the frame is laid out from that same value.
func TestBothRenderersReadTheSameModel(t *testing.T) {
	m := built(t, "2026-09-02T12:00")

	page, err := RenderHTML(m)
	if err != nil {
		t.Fatal(err)
	}
	back, line, err := payload.Extract(string(page), Slot)
	if err != nil {
		t.Fatal(err)
	}
	carried, err := Validate("page", back, line)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(carried, m) {
		t.Errorf("the page carries a different model:\n got %+v\nwant %+v", carried, m)
	}

	// The frame holds every pane in the contract's order, and as many rows in
	// each as the model does.
	var buf bytes.Buffer
	out := &render.Out{W: &buf}
	RenderASCII(out, m)
	got := counts(t, buf.String(), m)
	for i, pane := range m.Panes {
		if got[i] != len(pane.Rows) {
			t.Errorf("pane %q holds %d rows in the model and %d in the frame", pane.Key, len(pane.Rows), got[i])
		}
	}
}

// counts reads the row count of each pane out of a plain-rendered board, by
// counting the indented lines under each pane heading.
func counts(t *testing.T, text string, m Model) []int {
	t.Helper()
	out := make([]int, len(m.Panes))
	current := -1
	for _, line := range strings.Split(text, "\n") {
		heading := -1
		for i, pane := range m.Panes {
			if strings.TrimSpace(line) == strings.ToUpper(pane.Title) {
				heading = i
			}
		}
		if heading >= 0 {
			current = heading
			continue
		}
		if current < 0 || strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, " -") {
			if strings.TrimSpace(line) == m.Panes[current].Empty {
				continue
			}
			out[current]++
			continue
		}
		current = -1
	}
	return out
}

// TestEveryPaneAlwaysRendersWithItsOwnEmptyState: an empty week must render as
// an empty week, never as a missing pane.
func TestEveryPaneAlwaysRendersWithItsOwnEmptyState(t *testing.T) {
	// A Sunday in a week the fixture has nothing in, so several panes are empty.
	m := built(t, "2026-10-11T12:00")
	if len(m.Panes) != len(Panes) {
		t.Fatalf("%d panes, want %d", len(m.Panes), len(Panes))
	}
	var buf bytes.Buffer
	RenderASCII(&render.Out{W: &buf}, m)
	text := buf.String()
	empties := map[string]bool{}
	for _, spec := range Panes {
		if empties[spec.Empty] {
			t.Errorf("two panes share the empty-state string %q", spec.Empty)
		}
		empties[spec.Empty] = true
		if !strings.Contains(text, strings.ToUpper(spec.Title)) {
			t.Errorf("pane %q is missing from the frame:\n%s", spec.Key, text)
		}
	}
	for _, pane := range m.Panes {
		if len(pane.Rows) == 0 && !strings.Contains(text, pane.Empty) {
			t.Errorf("empty pane %q rendered without its empty-state string:\n%s", pane.Key, text)
		}
	}
}

// TestAHostileNoteCannotTerminateTheDataBlock: a captured note containing a
// closing script tag must stay inside the payload. Every `<` is escaped, so
// the browser sees one data block whatever the user wrote.
func TestAHostileNoteCannotTerminateTheDataBlock(t *testing.T) {
	m := built(t, "2026-09-02T12:00")
	const hostile = `</script><script>alert("pwned")</script><!--`
	m.Panes[3].Rows = append(m.Panes[3].Rows, Row{
		ID: "hostile", Kind: "idea", Title: hostile, Detail: "0d", Note: hostile, Path: "ideas/hostile.md",
	})
	page, err := RenderHTML(m)
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := payload.Extract(string(page), Slot)
	if err != nil {
		t.Fatalf("the hostile note broke the data block open: %v", err)
	}
	if bytes.Contains(body, []byte("<")) {
		t.Errorf("the payload still carries a literal '<':\n%s", body)
	}
	// It is still the same text once parsed, so nothing was silently mangled.
	var carried Model
	if err := json.Unmarshal(body, &carried); err != nil {
		t.Fatalf("the escaped payload is not valid JSON: %v", err)
	}
	last := carried.Panes[3].Rows[len(carried.Panes[3].Rows)-1]
	if last.Title != hostile {
		t.Errorf("the note was altered: %q", last.Title)
	}
	// And the page still holds exactly one data block.
	if n := strings.Count(string(page), `<script id="board-data" type="application/json">`); n != 1 {
		t.Errorf("the page holds %d data blocks", n)
	}
}

// TestValidationIsFailClosed: every way a payload can be off contract is
// refused, with the line to fix it on.
func TestValidationIsFailClosed(t *testing.T) {
	m := built(t, "2026-09-02T12:00")
	good, err := payload.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"wrong schema", strings.Replace(string(good), `"brain-board.v1"`, `"brain-board.v2"`, 1), "unsupported board schema"},
		{"unknown pane", strings.Replace(string(good), `"key": "tasks"`, `"key": "invoices"`, 1), "the pane set is fixed in this order"},
		{"missing field", `{"schema":"brain-board.v1","timezone":"Asia/Bangkok","panes":[]}`, "generated"},
		{"wrong type", strings.Replace(string(good), `"panes": [`, `"panes": "five", "ignored": [`, 1), "panes"},
		{"unknown field", strings.Replace(string(good), `"schema"`, `"surprise": 1, "schema"`, 1), "unknown field"},
		{"not json", "{ this is not json", "not valid JSON"},
		{"no empty state", strings.Replace(string(good), `"empty": "nothing scheduled today"`, `"empty": ""`, 1), "empty-state string"},
		{"missing rows", strings.Replace(string(good), `      "rows": [`, `      "omitted_rows": [`, 1), "rows is required"},
		{"null rows", strings.Replace(string(good), `      "rows": [`, `      "rows": null, "discarded": [`, 1), "rows is required"},
		{"missing row field", strings.Replace(string(good), `          "title":`, `          "omitted_title":`, 1), "title is required"},
		{"bad row kind", strings.Replace(string(good), `"kind": "event"`, `"kind": "invoice"`, 1), "valid kinds are"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Validate("board.html", []byte(tc.raw), 40)
			if err == nil {
				t.Fatalf("accepted an off-contract payload")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not explain the problem: %v", err)
			}
			if !strings.HasPrefix(err.Error(), "board.html:") {
				t.Errorf("error does not name the file and the line: %v", err)
			}
		})
	}
}

// TestABadModelNeverReachesAFile: RenderHTML refuses before it produces
// anything, which is what lets the caller write the result over a board that
// was already good.
func TestABadModelNeverReachesAFile(t *testing.T) {
	m := built(t, "2026-09-02T12:00")
	m.Panes[2].Key = "invoices"
	if _, err := RenderHTML(m); err == nil {
		t.Fatal("an unknown pane was rendered instead of refused")
	}
}

// TestWriteFileReplacesAtomically: a rebuild leaves the same path holding a
// whole file, never a truncated one.
func TestWriteFileReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "board.html")
	first, err := RenderHTML(built(t, "2026-09-02T12:00"))
	if err != nil {
		t.Fatal(err)
	}
	if err := payload.WriteFile(path, first); err != nil {
		t.Fatal(err)
	}
	second, err := RenderHTML(built(t, "2026-09-04T12:00"))
	if err != nil {
		t.Fatal(err)
	}
	if err := payload.WriteFile(path, second); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, second) {
		t.Error("a rebuild did not replace the file in place")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the rebuild left %d files behind, want 1", len(entries))
	}
}

// emptyVault is a freshly initialised vault: exactly what a new user has, and
// the shape `board --html` was broken on.
func emptyVault(t *testing.T, stamp string) *query.Engine {
	t.Helper()
	root := filepath.Join(t.TempDir(), "vault")
	cfg := vault.DefaultConfig()
	cfg.Timezone = "Asia/Bangkok"
	if _, err := vault.Init(root, cfg, false); err != nil {
		t.Fatal(err)
	}
	v, err := vault.OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	now, err := v.Zone.Normalise(stamp)
	if err != nil {
		t.Fatal(err)
	}
	return query.New(v, now)
}

// TestHTMLBoardBuildsWithEveryPaneEmpty: an empty pane must reach the page as
// an empty list, not as null. Every row builder used to declare `var out []Row`
// and return it, so an empty result marshalled as `null` and the board's own
// validator - correctly - refused the payload it had just produced. Any one of
// today, week, tasks or waiting being empty killed the whole page, and an empty
// waiting pane is normal even for a busy vault.
//
// The validator is not the thing to relax here: `rows: null` must stay refused,
// because a viewer cannot render it. The producer is what was wrong.
func TestHTMLBoardBuildsWithEveryPaneEmpty(t *testing.T) {
	m, err := Build(emptyVault(t, "2026-09-02T12:00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Panes) != len(Panes) {
		t.Fatalf("%d panes, want %d", len(m.Panes), len(Panes))
	}
	for _, pane := range m.Panes {
		if pane.Rows == nil {
			t.Errorf("pane %q has nil rows, which marshals to null", pane.Key)
		}
		if len(pane.Rows) != 0 {
			t.Errorf("pane %q has %d rows on an empty vault", pane.Key, len(pane.Rows))
		}
	}
	// The marshalled payload is where the bug actually bit.
	raw, err := payload.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"rows":null`)) {
		t.Errorf("the payload carries a null rows list:\n%s", raw)
	}
	if _, err := Validate("<board payload>", raw, 0); err != nil {
		t.Fatalf("the board's own validator refused the board's own payload: %v", err)
	}
	// And the whole page builds, which is what the user actually ran.
	page, err := RenderHTML(m)
	if err != nil {
		t.Fatalf("RenderHTML on an empty vault: %v", err)
	}
	for _, spec := range Panes {
		if !bytes.Contains(page, []byte(spec.Empty)) {
			t.Errorf("the page is missing pane %q's empty state %q", spec.Key, spec.Empty)
		}
	}
}

// TestHTMLBoardBuildsWithSomePanesEmpty: the mixed case, which is the ordinary
// one. A vault with events and an idea but nothing delegated has rows in some
// panes and none in others, and every pane must still carry a list.
func TestHTMLBoardBuildsWithSomePanesEmpty(t *testing.T) {
	// A Sunday: nothing is scheduled today, while the week, the tasks and the
	// ideas all still have rows.
	m := built(t, "2026-10-11T12:00")
	var filled, empty []string
	for _, pane := range m.Panes {
		if pane.Rows == nil {
			t.Errorf("pane %q has nil rows, which marshals to null", pane.Key)
		}
		if len(pane.Rows) == 0 {
			empty = append(empty, pane.Key)
		} else {
			filled = append(filled, pane.Key)
		}
	}
	if len(filled) == 0 || len(empty) == 0 {
		t.Fatalf("this fixture does not exercise the mixed case: filled=%v empty=%v", filled, empty)
	}
	raw, err := payload.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"rows":null`)) {
		t.Errorf("the payload carries a null rows list:\n%s", raw)
	}
	if _, err := RenderHTML(m); err != nil {
		t.Fatalf("RenderHTML with %v empty: %v", empty, err)
	}
}
