package render

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (run go test ./internal/render -update): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("%s drifted\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// TestBlockAlignsColumnsByDisplayCell holds the padding to display cells rather
// than bytes. The title column mixes three shapes in one column: ASCII, where a
// byte is a cell; combining marks, where a title is more bytes than cells; and
// East Asian wide runes, where it is more cells than runes. Byte-width padding
// misaligns the last two, and the golden file is where that shows.
func TestBlockAlignsColumnsByDisplayCell(t *testing.T) {
	decomposedTitle := "Cafe\u0301 planning"
	if decomposedTitle == "Café planning" || len([]rune(decomposedTitle)) <= unitext.Width(decomposedTitle) {
		t.Fatal("the decomposed title no longer exercises zero-width combining marks")
	}
	var buf bytes.Buffer
	o := &Out{W: &buf}
	o.Block(Block{
		Name:    "ideas",
		Columns: Cols([]string{"id", "title", "age", "touched"}, "age"),
		Rows: [][]string{
			{"customer-referral", "customer referral program", "23d", "2026-08-09"},
			{"shared-vault", "share a vault across a team", "8d", "2026-08-24"},
			{"calendar-export", "export the calendar as .ics", "2d", "2026-08-30"},
			{"planning", decomposedTitle, "2d", "2026-08-30"},
			{"ci-capacity", "ask the Zürich datacentre team", "1d", "2026-08-31"},
			{"tokyo-capacity", "東京リージョンの容量を確認", "1d", "2026-08-31"},
		},
	})
	got := buf.String()
	golden(t, "block-wide-and-combining.golden", got)

	// Every column must line up: a left-aligned column starts at the same
	// display cell on every row, a right-aligned one ends at the same cell.
	// Byte-width padding gets both wrong with combining marks or wide characters.
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")[1:]
	type span struct{ start, end int }
	spans := make([][]span, len(lines))
	for i, line := range lines {
		at := 0
		for _, field := range strings.Split(line, ",") {
			trimmed := strings.TrimLeft(field, " ")
			start := at + unitext.Width(field) - unitext.Width(trimmed)
			end := start + unitext.Width(strings.TrimRight(trimmed, " "))
			spans[i] = append(spans[i], span{start, end})
			at += unitext.Width(field) + 1 // the comma
		}
	}
	rightAligned := map[int]bool{2: true}
	for i := 1; i < len(spans); i++ {
		if len(spans[i]) != len(spans[0]) {
			t.Fatalf("row %d has %d fields, row 0 has %d:\n%s", i, len(spans[i]), len(spans[0]), got)
		}
		for j := range spans[i] {
			if rightAligned[j] {
				if spans[i][j].end != spans[0][j].end {
					t.Errorf("right-aligned column %d ends at cell %d on row %d and %d on row 0:\n%s",
						j, spans[i][j].end, i, spans[0][j].end, got)
				}
				continue
			}
			if spans[i][j].start != spans[0][j].start {
				t.Errorf("column %d starts at cell %d on row %d and %d on row 0:\n%s",
					j, spans[i][j].start, i, spans[0][j].start, got)
			}
		}
	}
}

func TestBlockEmptyAndQuoting(t *testing.T) {
	var buf bytes.Buffer
	o := &Out{W: &buf}
	o.Block(Block{Name: "events", Columns: Cols([]string{"a", "b"}), Empty: "nothing scheduled"})
	if got, want := buf.String(), "events[0]: nothing scheduled\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	buf.Reset()
	o.Block(Block{Name: "events", Columns: Cols([]string{"a", "b"})})
	if got, want := buf.String(), "events[0]:\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	cases := map[string]string{
		"plain":             "plain",
		"has spaces inside": "has spaces inside",
		"has,comma":         `"has,comma"`,
		`has"quote`:         `"has\"quote"`,
		"":                  EmptyCell,
		"-":                 `"-"`,
		" leading":          `" leading"`,
		"trailing ":         `"trailing "`,
		"line\nbreak":       `"line\nbreak"`,
		"Zürich sync 東京":    "Zürich sync 東京",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestListInlineAndIndented(t *testing.T) {
	var buf bytes.Buffer
	o := &Out{W: &buf}
	o.Attention(nil)
	if buf.Len() != 0 {
		t.Errorf("an empty attention list wrote %q", buf.String())
	}
	o.Attention([]string{"customer-referral past its 14d nudge horizon"})
	if got, want := buf.String(), "attention[1]: customer-referral past its 14d nudge horizon\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	buf.Reset()
	o.Help([]string{"run one", "run two"})
	if got, want := buf.String(), "help[2]:\n  - run one\n  - run two\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func dashboardFixture(t *testing.T, stamp string) Dashboard {
	t.Helper()
	v, err := vault.OpenAt(filepath.Join("..", "vault", "testdata", "good"))
	if err != nil {
		t.Fatal(err)
	}
	now, err := v.Zone.Normalise(stamp)
	if err != nil {
		t.Fatal(err)
	}
	e := query.New(v, now)
	today, err := e.Today()
	if err != nil {
		t.Fatal(err)
	}
	loads, week, err := e.WeekLoad()
	if err != nil {
		t.Fatal(err)
	}
	ideas, err := e.Ideas(query.IdeaFilter{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	return Dashboard{
		Now: now, Zone: v.Zone, Today: today, WeekLoad: loads, Week: week, Ideas: ideas,
		Backlog: 4, HasBacklog: true,
	}
}

// TestDashboardGoldenFrames locks the framed render, including a mixed-width
// alignment case: every drawn line must be exactly the frame's width in cells.
func TestDashboardGoldenFrames(t *testing.T) {
	cases := []struct {
		name  string
		stamp string
		width int
	}{
		{"dashboard-wednesday", "2026-09-02T12:00", DefaultFrameWidth},
		{"dashboard-before-first-event", "2026-09-02T07:00", DefaultFrameWidth},
		{"dashboard-empty-day", "2026-09-06T10:00", DefaultFrameWidth},
		{"dashboard-narrow", "2026-09-02T12:00", 48},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := dashboardFixture(t, tc.stamp)
			var buf bytes.Buffer
			o := &Out{W: &buf, TTY: true, Width: tc.width}
			o.Dashboard(d)
			got := buf.String()
			golden(t, tc.name+".golden", got)

			// Every line of a frame is exactly the frame width in cells. This
			// is what byte-width padding gets wrong on accented and wide text.
			for i, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
				if w := unitext.Width(line); w != tc.width {
					t.Errorf("line %d is %d cells wide, frame is %d:\n%q", i, w, tc.width, line)
				}
			}
			if !strings.Contains(got, "day, 2026-") {
				t.Errorf("no weekday and ISO date in the frame title:\n%s", got)
			}
		})
	}
}

// TestDashboardDegradesOffATTY: box-drawing characters are pure token cost, so
// they must never reach an agent's context.
func TestDashboardDegradesOffATTY(t *testing.T) {
	d := dashboardFixture(t, "2026-09-02T12:00")
	var buf bytes.Buffer
	o := &Out{W: &buf, TTY: false, Width: DefaultFrameWidth}
	o.Dashboard(d)
	got := buf.String()
	golden(t, "dashboard-plain.golden", got)
	for _, r := range "╭╮╰╯│─├┤" {
		if strings.ContainsRune(got, r) {
			t.Errorf("frame character %q reached a non-terminal writer:\n%s", r, got)
		}
	}
	// The information is still there.
	for _, want := range []string{"TODAY", "standup", "customer-referral", "24d", "09:00 - 09:30"} {
		if !strings.Contains(got, want) {
			t.Errorf("plain output lost %q:\n%s", want, got)
		}
	}
	// No line carries trailing whitespace.
	for i, line := range strings.Split(got, "\n") {
		if line != strings.TrimRight(line, " ") {
			t.Errorf("line %d has trailing spaces: %q", i, line)
		}
	}
}

func TestFrameRowPushesHalvesApart(t *testing.T) {
	f := NewFrame(30, "t", "")
	f.Row("Zürich 東京", "platform-team")
	out := f.String()
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := unitext.Width(line); w != 30 {
			t.Errorf("line %d is %d cells wide:\n%q", i, w, line)
		}
	}
	if !strings.Contains(out, "Zürich 東京") || !strings.Contains(out, "platform-team") {
		t.Errorf("Row lost content:\n%s", out)
	}
	// An over-wide left half is truncated rather than pushing the border out.
	f = NewFrame(24, "t", "")
	f.Row("a very long left hand side indeed", "right")
	for i, line := range strings.Split(strings.TrimRight(f.String(), "\n"), "\n") {
		if w := unitext.Width(line); w != 24 {
			t.Errorf("line %d is %d cells wide:\n%q", i, w, line)
		}
	}
}

func TestFrameWidthClamping(t *testing.T) {
	o := &Out{Width: 0}
	if got := o.FrameWidth(64); got != 64 {
		t.Errorf("unknown terminal width gave %d", got)
	}
	o = &Out{Width: 120}
	if got := o.FrameWidth(64); got != 64 {
		t.Errorf("a wide terminal widened the frame to %d", got)
	}
	o = &Out{Width: 50}
	if got := o.FrameWidth(64); got != 50 {
		t.Errorf("a narrow terminal gave %d", got)
	}
	o = &Out{Width: 12}
	if got := o.FrameWidth(64); got != 40 {
		t.Errorf("a tiny terminal gave %d, want the 40-cell floor", got)
	}
}

func TestEmitJSON(t *testing.T) {
	var buf bytes.Buffer
	o := &Out{W: &buf, JSON: true}
	if err := o.Emit(map[string]any{"title": "Zürich sync 東京", "when": "2026-09-04T14:00:00+07:00"}); err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("emitted invalid JSON %q: %v", buf.String(), err)
	}
	if back["title"] != "Zürich sync 東京" {
		t.Errorf("non-ASCII text did not survive JSON: %v", back["title"])
	}
	if strings.Contains(buf.String(), `\u`) {
		t.Errorf("JSON escaped non-ASCII text: %s", buf.String())
	}
}

func TestDayLoadMarks(t *testing.T) {
	cases := map[int]string{0: "·", 1: "●", 2: "●●", 3: "●●●", 4: "●●●+", 9: "●●●+"}
	for count, want := range cases {
		if got := dayLoadMarks(count); got != want {
			t.Errorf("dayLoadMarks(%d) = %q, want %q", count, got, want)
		}
	}
}

func TestWeekdayNamesFitTheWeekStrip(t *testing.T) {
	for _, wd := range []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday} {
		if WeekdayLong(wd) == "" || WeekdayShort(wd) == "" {
			t.Errorf("%v has no name", wd)
		}
		if got := unitext.Width(WeekdayShort(wd)); got != 2 {
			t.Errorf("WeekdayShort(%v) = %q is %d cells, want 2", wd, WeekdayShort(wd), got)
		}
	}
	if got := WeekdayLong(time.Tuesday); got != "Tuesday" {
		t.Errorf("Tuesday = %q", got)
	}
}

func TestTimeRangeAlignsWithAndWithoutDuration(t *testing.T) {
	z, err := timeref.LoadZone("Asia/Bangkok", "mon")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 2, 9, 0, 0, 0, z.Loc)
	for _, tty := range []bool{true, false} {
		withDur := timeRange(vault.Occurrence{Start: start, End: start.Add(30 * time.Minute)}, tty)
		point := timeRange(vault.Occurrence{Start: start, End: start}, tty)
		if unitext.Width(withDur) != unitext.Width(point) {
			t.Errorf("tty=%v: %q (%d cells) and %q (%d cells) do not align",
				tty, withDur, unitext.Width(withDur), point, unitext.Width(point))
		}
		if !tty && strings.Contains(withDur, "─") {
			t.Errorf("a box-drawing separator reached non-terminal output: %q", withDur)
		}
	}
}

func TestFieldsAlignKeys(t *testing.T) {
	var buf bytes.Buffer
	o := &Out{W: &buf}
	o.Fields([]Field{
		{"vault", "/home/you/secondbrain/vault  ok"},
		{"config", "Asia/Bangkok, week starts mon  ok"},
		{"files", "1284 parsed, 0 malformed"},
		{"backlog", "4 open, read-only  ok"},
	})
	want := "vault:   /home/you/secondbrain/vault  ok\n" +
		"config:  Asia/Bangkok, week starts mon  ok\n" +
		"files:   1284 parsed, 0 malformed\n" +
		"backlog: 4 open, read-only  ok\n"
	if got := buf.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// Every value starts at the same display column.
	var starts []int
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		key, value, _ := strings.Cut(line, ":")
		starts = append(starts, unitext.Width(key)+1+unitext.Width(value)-unitext.Width(strings.TrimLeft(value, " ")))
	}
	for i := 1; i < len(starts); i++ {
		if starts[i] != starts[0] {
			t.Errorf("value %d starts at cell %d, value 0 at %d", i, starts[i], starts[0])
		}
	}
}
