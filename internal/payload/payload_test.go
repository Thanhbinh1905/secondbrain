package payload

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var slot = Slot{Marker: "__DATA__", ElementID: "data"}

const template = `<!doctype html>
<body>
<script id="data" type="application/json">
__DATA__
</script>
</body>
`

// TestEscapeMakesAPayloadInertInsideAScriptBlock: `<` never appears in JSON
// syntax outside a string, so escaping every one of them leaves the document
// identical once parsed and makes a closing script tag impossible to mistake
// for the end of the data block.
func TestEscapeMakesAPayloadInertInsideAScriptBlock(t *testing.T) {
	raw, err := Marshal(map[string]string{"note": `</script><script>alert(1)</script><!--`})
	if err != nil {
		t.Fatal(err)
	}
	escaped := Escape(raw)
	if strings.Contains(string(escaped), "<") {
		t.Fatalf("a literal '<' survived escaping: %s", escaped)
	}
	if !strings.Contains(string(escaped), `\u003c`) {
		t.Fatalf("nothing was escaped: %s", escaped)
	}
	var back map[string]string
	if err := json.Unmarshal(escaped, &back); err != nil {
		t.Fatalf("the escaped payload is not valid JSON: %v", err)
	}
	if back["note"] != `</script><script>alert(1)</script><!--` {
		t.Errorf("escaping altered the text: %q", back["note"])
	}
}

// TestMarshalLeavesNonASCIIAlone: vault content may contain Unicode, and a page
// that spells it back in escapes is unreadable in a diff.
func TestMarshalLeavesNonASCIIAlone(t *testing.T) {
	raw, err := Marshal(map[string]string{"title": "Zürich datacentre 東京"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Zürich datacentre 東京") {
		t.Errorf("non-ASCII text was escaped: %s", raw)
	}
}

// TestInjectReplacesOnlyTheSlot: the template is the only source of markup, so
// a built page is the committed file with exactly one line changed.
func TestInjectReplacesOnlyTheSlot(t *testing.T) {
	page, err := Inject(template, slot, []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if page != strings.Replace(template, "__DATA__", `{"a":1}`, 1) {
		t.Errorf("more than the slot changed:\n%s", page)
	}
	if _, err := Inject("<body></body>", slot, []byte("{}")); err == nil {
		t.Error("a template with no slot was accepted")
	}
	if _, err := Inject(template+template, slot, []byte("{}")); err == nil {
		t.Error("a template with two slots was accepted")
	}
}

// TestExtractReadsThePayloadBackWithItsLine: a page whose payload cannot be
// found again is a page that would fail in a browser, and the line it starts
// on is what makes a contract failure point at something.
func TestExtractReadsThePayloadBackWithItsLine(t *testing.T) {
	page, err := Inject(template, slot, []byte("{\n  \"a\": 1\n}"))
	if err != nil {
		t.Fatal(err)
	}
	body, line, err := Extract(page, slot)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "{\n  \"a\": 1\n}" {
		t.Errorf("payload = %q", body)
	}
	// The template's data block opens on file line 3, so the payload starts on 4.
	if line != 4 {
		t.Errorf("payload starts on line %d, want 4", line)
	}
	if _, _, err := Extract("<body></body>", slot); err == nil {
		t.Error("a page with no data block was accepted")
	}
	if _, _, err := Extract("<script id=\"data\" type=\"application/json\">\n{}\n", slot); err == nil {
		t.Error("an unterminated data block was accepted")
	}
}

// TestErrorsCarryTheLineInTheFileTheReaderIsLookingAt, not a line in a
// fragment nobody can see.
func TestErrorsCarryTheLineInTheFileTheReaderIsLookingAt(t *testing.T) {
	raw := []byte("{\n  \"schema\": \"x\",\n  \"count\": \"not a number\"\n}")
	var into struct {
		Schema string `json:"schema"`
		Count  int    `json:"count"`
	}
	err := Decode("board.html", raw, 4, &into)
	if err == nil {
		t.Fatal("a wrong type was accepted")
	}
	// "count" is on the payload's line 3, and the payload starts on file line 4.
	if !strings.HasPrefix(err.Error(), "board.html:6:") {
		t.Errorf("error does not name the file line: %v", err)
	}
	if line := LineOfKey(raw, "count", 4); line != 6 {
		t.Errorf("LineOfKey = %d, want 6", line)
	}
	// A key that is not there anchors at the payload's own first line rather
	// than inventing a position.
	if line := LineOfKey(raw, "absent", 4); line != 4 {
		t.Errorf("LineOfKey for an absent key = %d, want 4", line)
	}
	if err := Decode("board.html", []byte("{ nope"), 1, &into); err == nil ||
		!strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("a syntax error was not reported as one: %v", err)
	}
	if err := Decode("board.html", []byte(`{"surprise": 1}`), 1, &into); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Errorf("an unknown field was accepted: %v", err)
	}
}

// TestWriteFileReplacesInPlaceAndLeavesNothingBehind: an interrupted write must
// leave the previous page standing, which is what an atomic rename buys.
func TestWriteFileReplacesInPlaceAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "page.html")
	if err := WriteFile(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Errorf("file = %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("%d files left in the directory, want 1", len(entries))
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("private")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("replacement mode = %o, want 600", got)
	}
	newPath := filepath.Join(dir, "new.html")
	if err := WriteFile(newPath, []byte("private")); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got&0o077 != 0 {
		t.Errorf("new file mode = %o, want no group or world access", got)
	}
	// A directory that cannot be written is reported, never swallowed.
	blocked := filepath.Join(dir, "locked")
	if err := os.Mkdir(blocked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	if err := WriteFile(filepath.Join(blocked, "page.html"), []byte("x")); err == nil {
		t.Skip("this filesystem lets an unwritable directory be written to")
	}
}
