package render

import (
	"fmt"
	"strings"

	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
)

// Frame draws a rounded box with a titled top rule and a captioned bottom rule.
// It is a static render: no event loop, no alternate screen buffer, no redraw
// and no resize handling. That is the whole point - all of the appeal of a
// composed terminal surface, none of the machinery.
type Frame struct {
	// Width is the total display width including both borders.
	Width int
	// TitleLeft and TitleRight sit in the top rule.
	TitleLeft  string
	TitleRight string
	// Caption sits in the bottom rule.
	Caption string
	lines   []string
}

// NewFrame starts a frame.
func NewFrame(width int, titleLeft, titleRight string) *Frame {
	return &Frame{Width: width, TitleLeft: titleLeft, TitleRight: titleRight}
}

// Inner is the display width available between the borders and their padding.
func (f *Frame) Inner() int { return f.Width - 4 }

// Line adds one content line, truncated to the frame's inner width.
func (f *Frame) Line(s string) {
	f.lines = append(f.lines, unitext.Truncate(s, f.Inner()))
}

// Blank adds an empty content line.
func (f *Frame) Blank() { f.lines = append(f.lines, "") }

// Section adds a blank line and then a heading, unless the frame is empty.
func (f *Frame) Section(heading string) {
	if len(f.lines) > 0 {
		f.Blank()
	}
	f.Line(heading)
}

// Rule adds a full-width horizontal divider.
func (f *Frame) Rule() { f.lines = append(f.lines, ruleMarker) }

const ruleMarker = "\x00rule"

// Row lays out a left part and a right part on one line, pushed apart to the
// frame's inner width. The gap is computed in display cells, so an accented or
// wide-character left part still leaves the right part flush.
func (f *Frame) Row(left, right string) {
	inner := f.Inner()
	if right == "" {
		f.Line(left)
		return
	}
	rw := unitext.Width(right)
	// Keep at least one space between the two halves.
	left = unitext.Truncate(left, max(0, inner-rw-1))
	gap := inner - unitext.Width(left) - rw
	if gap < 1 {
		gap = 1
	}
	f.lines = append(f.lines, left+strings.Repeat(" ", gap)+right)
}

// String renders the frame.
func (f *Frame) String() string {
	var sb strings.Builder
	sb.WriteString(f.topRule())
	sb.WriteByte('\n')
	for _, l := range f.lines {
		if l == ruleMarker {
			sb.WriteString("├" + strings.Repeat("─", f.Width-2) + "┤\n")
			continue
		}
		sb.WriteString("│ " + unitext.PadRight(l, f.Inner()) + " │\n")
	}
	sb.WriteString(f.bottomRule())
	sb.WriteByte('\n')
	return sb.String()
}

func (f *Frame) topRule() string {
	left := "╭─"
	if f.TitleLeft != "" {
		left += " " + f.TitleLeft + " "
	}
	right := ""
	if f.TitleRight != "" {
		right = " " + f.TitleRight + " ─"
	} else {
		right = "─"
	}
	fill := f.Width - unitext.Width(left) - unitext.Width(right) - 1
	if fill < 0 {
		fill = 0
	}
	return left + strings.Repeat("─", fill) + right + "╮"
}

func (f *Frame) bottomRule() string {
	left := "╰─"
	if f.Caption != "" {
		left += " " + f.Caption + " "
	}
	fill := f.Width - unitext.Width(left) - 1
	if fill < 0 {
		fill = 0
	}
	return left + strings.Repeat("─", fill) + "╯"
}

// Plain renders the frame's content without any box drawing, for the moment
// stdout is a pipe rather than a terminal.
func (f *Frame) Plain() string {
	var sb strings.Builder
	if f.TitleLeft != "" || f.TitleRight != "" {
		fmt.Fprintf(&sb, "%s%s\n", f.TitleLeft, prefixIf(" - ", f.TitleRight))
	}
	for _, l := range f.lines {
		if l == ruleMarker {
			continue
		}
		// One level of the frame's own indentation comes off, so a plain
		// render reads as text rather than as a box with the box removed.
		sb.WriteString(strings.TrimRight(strings.TrimPrefix(l, " "), " "))
		sb.WriteByte('\n')
	}
	if f.Caption != "" {
		fmt.Fprintf(&sb, "%s\n", f.Caption)
	}
	return sb.String()
}

func prefixIf(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}
