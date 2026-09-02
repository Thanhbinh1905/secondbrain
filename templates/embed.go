// Package templates embeds the two HTML surfaces this tool writes.
//
// The embed lives here, beside the files, so the tracked HTML is the only copy
// (the same reason skills/embed.go sits beside the skill). A second copy under
// internal/ would be a thing that can silently disagree with the thing the
// contributor edits.
//
// The Markdown files in this directory are documentation rather than code:
// they show the shape of every record `brain-axi add` writes. The two HTML
// files are code, and are the only place board and recap markup exists.
package templates

import (
	_ "embed"
)

// Board is the committed board template. It owns layout, styling, pane order
// and every empty-state string; internal/board only ever substitutes its one
// data slot.
//
//go:embed board.html
var Board string

// Recap is the committed recap template, on the same contract as Board.
//
//go:embed recap.html
var Recap string

// DataSlot is the line each template carries exactly once, and the only thing
// a renderer replaces.
const DataSlot = "__BRAIN_AXI_DATA__"
