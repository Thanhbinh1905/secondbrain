// Package render writes the tool's two kinds of output, and keeps them apart.
//
// Agent-facing output is compact axi-house-style text or --json. Human-facing
// output is the framed dashboard and the review screen. Box-drawing characters
// are pure token cost with no information, so they never reach an agent: when
// stdout is not a terminal the dashboard degrades to plain lines.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
)

// Out is a destination for output, and what it can carry.
type Out struct {
	W io.Writer
	// TTY reports whether W is a terminal. Frames are drawn only when it is.
	TTY bool
	// Width is the terminal width in cells, or zero when it is unknown.
	Width int
	// JSON switches every writer to machine-readable output.
	JSON bool
}

// Column is one column of an axi block.
type Column struct {
	Name string
	// Right right-aligns the column, which is what an age or a count wants.
	Right bool
	// Last marks the final column, which is never padded so no line carries
	// trailing spaces.
	Last bool
}

// Block is the axi house table: name[n]{cols}: then one indented row per item.
type Block struct {
	Name    string
	Columns []Column
	Rows    [][]string
	// Empty replaces the whole block when there are no rows, so an empty day
	// answers in words rather than printing an empty table.
	Empty string
}

// Cols builds a column list from names, right-aligning the named ones.
func Cols(names []string, right ...string) []Column {
	rightSet := map[string]bool{}
	for _, r := range right {
		rightSet[r] = true
	}
	out := make([]Column, len(names))
	for i, n := range names {
		out[i] = Column{Name: n, Right: rightSet[n], Last: i == len(names)-1}
	}
	return out
}

// EmptyCell stands in for a value a record does not have. It reads better than
// an empty pair of quotes in an aligned column, and it cannot be confused with
// a literal hyphen, which Quote quotes.
const EmptyCell = "-"

// needsQuote reports whether a cell must be quoted to survive being read back
// as one field of a comma-separated row.
func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	if strings.ContainsAny(s, ",\"\n\r") {
		return true
	}
	// A bare hyphen or a leading/trailing space would be read as something
	// other than the text it is.
	if s == "-" || s == "~" {
		return true
	}
	return strings.TrimSpace(s) != s
}

// Quote renders a cell, quoting only when the text would otherwise be
// misread. Plain sentences with spaces are left bare, as the other axi tools
// leave them.
func Quote(s string) string {
	if s == "" {
		return EmptyCell
	}
	if !needsQuote(s) {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(s) + `"`
}

// Scalar writes a `key: value` line.
func (o *Out) Scalar(key, value string) {
	fmt.Fprintf(o.W, "%s: %s\n", key, value)
}

// Field is one key/value line of an aligned scalar report.
type Field struct {
	Key   string
	Value string
}

// Fields writes key/value lines with the keys padded to a common width, which
// is what a report read down a column wants. Single scalars that
// are not part of a report use Scalar instead.
func (o *Out) Fields(fields []Field) {
	width := 0
	for _, f := range fields {
		if w := unitext.Width(f.Key); w > width {
			width = w
		}
	}
	for _, f := range fields {
		fmt.Fprintf(o.W, "%s %s\n", unitext.PadRight(f.Key+":", width+1), f.Value)
	}
}

// Block writes an axi block, aligning every column but the last to its widest
// cell. Alignment counts display cells, so an accented or wide-character row
// lines up with an ASCII one.
func (o *Out) Block(b Block) {
	if len(b.Rows) == 0 {
		if b.Empty != "" {
			fmt.Fprintf(o.W, "%s[0]: %s\n", b.Name, b.Empty)
		} else {
			fmt.Fprintf(o.W, "%s[0]:\n", b.Name)
		}
		return
	}
	names := make([]string, len(b.Columns))
	for i, c := range b.Columns {
		names[i] = c.Name
	}
	fmt.Fprintf(o.W, "%s[%d]{%s}:\n", b.Name, len(b.Rows), strings.Join(names, ","))

	quoted := make([][]string, len(b.Rows))
	widths := make([]int, len(b.Columns))
	for i, row := range b.Rows {
		quoted[i] = make([]string, len(b.Columns))
		for j := range b.Columns {
			cell := ""
			if j < len(row) {
				cell = Quote(row[j])
			}
			quoted[i][j] = cell
			if w := unitext.Width(cell); w > widths[j] {
				widths[j] = w
			}
		}
	}
	for _, row := range quoted {
		var sb strings.Builder
		sb.WriteString("  ")
		for j, c := range b.Columns {
			cell := row[j]
			if j > 0 {
				sb.WriteString(" ")
			}
			switch {
			case c.Last:
				sb.WriteString(cell)
			case c.Right:
				sb.WriteString(unitext.PadLeft(cell, widths[j]))
				sb.WriteString(",")
			default:
				sb.WriteString(cell)
				sb.WriteString(",")
				sb.WriteString(strings.Repeat(" ", max(0, widths[j]-unitext.Width(cell))))
			}
		}
		fmt.Fprintln(o.W, strings.TrimRight(sb.String(), " "))
	}
}

// List writes a counted list of plain messages: one inline when there is a
// single item, an indented list when there are more. Nothing is written when
// there are none, so a quiet run stays quiet.
func (o *Out) List(name string, items []string) {
	switch len(items) {
	case 0:
		return
	case 1:
		fmt.Fprintf(o.W, "%s[1]: %s\n", name, items[0])
	default:
		fmt.Fprintf(o.W, "%s[%d]:\n", name, len(items))
		for _, it := range items {
			fmt.Fprintf(o.W, "  - %s\n", it)
		}
	}
}

// Attention writes the attention block: what the user should look at.
func (o *Out) Attention(items []string) { o.List("attention", items) }

// Help writes the help footer: what to run next.
func (o *Out) Help(items []string) { o.List("help", items) }

// Emit writes a value as indented JSON.
func (o *Out) Emit(v any) error {
	enc := json.NewEncoder(o.W)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// FrameWidth is the display width the dashboard and review screens are drawn
// at, clamped to the terminal when it is narrower.
func (o *Out) FrameWidth(preferred int) int {
	if o.Width > 0 && o.Width < preferred {
		if o.Width < 40 {
			return 40
		}
		return o.Width
	}
	return preferred
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
