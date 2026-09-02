// Package payload is the machinery both HTML surfaces share: injecting one
// validated JSON payload into a committed template, reading it back out, and
// reporting a contract failure as path:line: reason.
//
// Nothing here knows what a board or a recap contains. It owns exactly the
// three properties that must hold for both: the template is the only source of
// markup, the payload cannot terminate its own script block, and a page that
// does not satisfy its contract is never written over a page that did.
package payload

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Thanhbinh1905/secondbrain/internal/frontmatter"
)

// Errorf builds a path:line: reason error. It reuses the frontmatter error type
// so a bad payload maps to the same exit code a malformed record does: both are
// data that does not make sense.
func Errorf(path string, line int, format string, args ...any) error {
	if line <= 0 {
		line = 1
	}
	return &frontmatter.Error{Path: path, Line: line, Msg: fmt.Sprintf(format, args...)}
}

// Marshal renders a payload the way it is injected: indented, so a line number
// in an error points at a line a human can find, and with HTML escaping off so
// non-ASCII text survives as itself. Escape is what makes the result safe to
// put inside a script block.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("rendering the payload: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Escape makes a JSON payload inert inside a <script> block.
//
// `<` never appears in JSON syntax outside a string, so replacing every one of
// them with its six-character \u003c string escape leaves the document
// identical once parsed and makes a captured note containing a closing script
// tag impossible to mistake for the end of the data block.
func Escape(raw []byte) []byte {
	return bytes.ReplaceAll(raw, []byte("<"), []byte(`\u003c`))
}

// Slot is the placeholder line a template carries exactly once.
type Slot struct {
	// Marker is the placeholder text, on a line of its own.
	Marker string
	// ElementID is the id of the script element holding the payload, used to
	// read the payload back out of a built page.
	ElementID string
}

// LineOf reports the 1-based line the marker sits on, or 0 when it is absent.
func LineOf(text, marker string) int {
	for i, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == marker {
			return i + 1
		}
	}
	return 0
}

// count reports how many lines are exactly the marker.
func count(text, marker string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == marker {
			n++
		}
	}
	return n
}

// Inject substitutes an already-escaped payload for the template's one data
// slot. It is the whole of what a renderer does to the template: no markup is
// built here, so the page is byte-for-byte the committed file apart from the
// payload line.
func Inject(template string, slot Slot, escaped []byte) (string, error) {
	switch n := count(template, slot.Marker); n {
	case 1:
	case 0:
		return "", fmt.Errorf("the committed template carries no %s data slot", slot.Marker)
	default:
		return "", fmt.Errorf("the committed template carries %d %s data slots, and exactly one is required", n, slot.Marker)
	}
	var out []string
	for _, line := range strings.Split(template, "\n") {
		if strings.TrimSpace(line) == slot.Marker {
			out = append(out, string(escaped))
			continue
		}
		out = append(out, line)
	}
	page := strings.Join(out, "\n")
	if count(page, slot.Marker) != 0 {
		return "", fmt.Errorf("the %s data slot survived injection", slot.Marker)
	}
	return page, nil
}

// PayloadStart is the 1-based line of a built page's first payload line, so an
// error about the payload can name the line in the file the reader is looking
// at rather than a line in a fragment nobody can see.
func PayloadStart(page string, slot Slot) int {
	open := `<script id="` + slot.ElementID + `" type="application/json">`
	for i, line := range strings.Split(page, "\n") {
		if strings.TrimSpace(line) == open {
			return i + 2
		}
	}
	return 0
}

// Extract reads the payload back out of a built page. A page whose payload
// cannot be found again is a page that would render as a failure in a browser,
// which is worth discovering here instead.
func Extract(page string, slot Slot) ([]byte, int, error) {
	open := `<script id="` + slot.ElementID + `" type="application/json">`
	lines := strings.Split(page, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == open {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, 0, fmt.Errorf("this page has no %q data block", slot.ElementID)
	}
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "</script>" {
			return []byte(strings.Join(lines[start:i], "\n")), start + 1, nil
		}
	}
	return nil, 0, fmt.Errorf("this page's %q data block is never closed", slot.ElementID)
}

// LineAt turns a byte offset inside a payload into a 1-based line number,
// shifted by the payload's own first line in the file it came from.
func LineAt(raw []byte, offset int64, lineOffset int) int {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(raw)) {
		offset = int64(len(raw))
	}
	line := bytes.Count(raw[:offset], []byte("\n")) + 1
	if lineOffset > 0 {
		return line + lineOffset - 1
	}
	return line
}

// LineOfKey finds the line a JSON key sits on, so an error about a missing or
// wrong value points at the value rather than at the top of the file.
func LineOfKey(raw []byte, key string, lineOffset int) int {
	idx := bytes.Index(raw, []byte(`"`+key+`"`))
	if idx < 0 {
		if lineOffset > 0 {
			return lineOffset
		}
		return 1
	}
	return LineAt(raw, int64(idx), lineOffset)
}

// Decode unmarshals a payload strictly, turning every failure into
// path:line: reason. An unknown field is refused: the schema is versioned, so
// a key this build does not know about means the payload was written by
// something else.
func Decode(path string, raw []byte, lineOffset int, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		switch e := err.(type) {
		case *json.SyntaxError:
			return Errorf(path, LineAt(raw, e.Offset, lineOffset), "the payload is not valid JSON: %v", e)
		case *json.UnmarshalTypeError:
			field := e.Field
			if field == "" {
				field = "the payload"
			}
			return Errorf(path, LineAt(raw, e.Offset, lineOffset),
				"%s must be %s, found %s", field, e.Type, e.Value)
		default:
			return Errorf(path, lineOffset, "the payload does not match the contract: %v", err)
		}
	}
	if dec.More() {
		return Errorf(path, lineOffset, "the payload carries more than one JSON document")
	}
	return nil
}

// WriteFile writes a built page atomically: a temporary file beside the
// target, fsynced, then renamed over it. An interrupted write leaves the
// previous page standing, which is what "the previous board file survives
// unmodified" has to mean once validation has already passed.
func WriteFile(path string, data []byte) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", path, err)
	}
	dir := filepath.Dir(abs)
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(abs); statErr == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("reading permissions on %s: %w", path, statErr)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(abs)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating a temporary file next to %s: %w", path, err)
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("flushing %s to disk: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing %s: %w", path, err)
	}
	if err := os.Chmod(name, mode); err != nil {
		cleanup()
		return fmt.Errorf("setting permissions on %s: %w", path, err)
	}
	if err := os.Rename(name, abs); err != nil {
		cleanup()
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}
