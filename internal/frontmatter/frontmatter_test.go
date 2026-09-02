package frontmatter

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// TestParseErrorPositions locks the exact path:line: reason string for every
// malformed shape. A wrong line number in an error message is a real defect,
// so the messages are golden files rather than assertions written by hand.
func TestParseErrorPositions(t *testing.T) {
	dir := filepath.Join("testdata", "errors")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			path := filepath.Join(dir, e.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := Parse(e.Name(), raw)
			if err == nil {
				t.Fatalf("expected a parse error, got a document with keys %v", doc.Keys())
			}
			var perr *Error
			if !asPositional(err, &perr) {
				t.Fatalf("error %T is not positional: %v", err, err)
			}
			if perr.Line < 1 {
				t.Fatalf("error line %d is not a real file line: %v", perr.Line, err)
			}
			golden := path + ".golden"
			got := err.Error() + "\n"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden file (run go test -run TestParseErrorPositions -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("error text drifted\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func asPositional(err error, target **Error) bool {
	if e, ok := err.(*Error); ok {
		*target = e
		return true
	}
	return false
}

// TestRoundTrip asserts that parsing and re-serialising a document leaves it
// byte-identical: unknown keys, key order, flow style and non-ASCII text all
// survive, which is what FR-7 and the format-churn response require.
func TestRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"event", "---\ntype: event\nid: platform-team-sync-20260904\ntitle: Platform team sync in Zürich\nwhen: 2026-09-04T14:00:00+07:00\nduration: 60m\nwith: [platform-team]\nstatus: scheduled\ncreated: 2026-09-01\n---\n\nDecide how expired schedules are handled.\n"},
		{"idea", "---\ntype: idea\nid: customer-referral\ntitle: customer referral program\nstatus: pending\ncreated: 2026-08-09\ntouched: 2026-08-09\nnudge_after: 14d\n---\n\nCustomer referral program.\n"},
		{"unknown-keys-preserved", "---\ntype: idea\nid: x\nobsidian_property: kept\nnested:\n  a: 1\n  b: 2\nstatus: pending\n---\n\nbody\n"},
		{"quoted-values", "---\ntype: note\nid: x\ntitle: \"a: colon in a title\"\ncreated: 2026-09-01\n---\n\nbody\n"},
		{"no-body", "---\ntype: note\nid: x\ncreated: 2026-09-01\n---\n"},
		{"body-with-hr", "---\ntype: note\nid: x\ncreated: 2026-09-01\n---\n\nfirst\n\n---\n\nsecond\n"},
		{"empty-list", "---\ntype: event\nid: x\nwith: []\n---\n\nbody\n"},
		{"crlf-body", "---\ntype: note\nid: x\n---\n\r\nbody line one\r\nbody line two\r\n"},
		{"special-scalars", "---\ntype: note\nid: x\ntitle: \"true\"\nempty: \"\"\ndash: \"- not a list\"\ncolon: \"a: b\"\nnum: \"007\"\n---\n\nbody\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Parse(tc.name+".md", []byte(tc.raw))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			out, err := doc.Bytes()
			if err != nil {
				t.Fatalf("serialise: %v", err)
			}
			if string(out) != tc.raw {
				t.Errorf("round trip changed the file\n got: %q\nwant: %q", out, tc.raw)
			}
			// Re-parsing the output must reach the same keys.
			again, err := Parse(tc.name+".md", out)
			if err != nil {
				t.Fatalf("re-parse: %v", err)
			}
			if strings.Join(again.Keys(), ",") != strings.Join(doc.Keys(), ",") {
				t.Errorf("keys drifted: %v vs %v", again.Keys(), doc.Keys())
			}
		})
	}
}

func TestBodyPreservedByteForByteAcrossMutation(t *testing.T) {
	raw := "---\ntype: idea\nid: customer-referral\nstatus: pending\ntouched: 2026-08-09\nobsidian_prop: keep me\n---\n\nEvery referrer earns a share: Zürich and 東京 included.\n\n  indented   spacing   kept\ntrailing spaces here   \n"
	doc, err := Parse("idea.md", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	wantBody := doc.Body
	doc.Set("status", "building")
	doc.Set("touched", "2026-09-01")
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.HasSuffix(got, wantBody) {
		t.Fatalf("body not preserved byte-for-byte\n got tail: %q\nwant tail: %q", got, wantBody)
	}
	if !strings.Contains(got, "status: building") || !strings.Contains(got, "touched: 2026-09-01") {
		t.Errorf("mutation did not land: %s", got)
	}
	if !strings.Contains(got, "obsidian_prop: keep me") {
		t.Errorf("unknown key dropped: %s", got)
	}
	// Order must be unchanged: status stays where it was.
	reparsed, err := Parse("idea.md", out)
	if err != nil {
		t.Fatal(err)
	}
	if want := "type,id,status,touched,obsidian_prop"; strings.Join(reparsed.Keys(), ",") != want {
		t.Errorf("key order changed: %v, want %s", reparsed.Keys(), want)
	}
}

func TestSetAppendsNewKeyAtEnd(t *testing.T) {
	doc, err := Parse("x.md", []byte("---\ntype: event\nid: x\n---\n\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	doc.Set("rrule", "FREQ=WEEKLY;BYDAY=FR")
	doc.SetStrings("exceptions", []string{"2026-09-11", "2026-09-18"})
	out, _ := doc.Bytes()
	want := "---\ntype: event\nid: x\nrrule: FREQ=WEEKLY;BYDAY=FR\nexceptions: [2026-09-11, 2026-09-18]\n---\n\nbody\n"
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestDeleteAndScalarListReads(t *testing.T) {
	doc, err := Parse("x.md", []byte("---\ntype: event\nid: x\nwith: solo-person\ntags: [a, b]\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	// A scalar in a list position reads as a one-element list.
	got, ok, err := doc.Strings("with")
	if err != nil || !ok || len(got) != 1 || got[0] != "solo-person" {
		t.Fatalf("Strings(with) = %v %v %v", got, ok, err)
	}
	got, ok, err = doc.Strings("tags")
	if err != nil || !ok || strings.Join(got, ",") != "a,b" {
		t.Fatalf("Strings(tags) = %v %v %v", got, ok, err)
	}
	if _, ok, _ := doc.Strings("absent"); ok {
		t.Error("absent key reported present")
	}
	if !doc.Delete("with") {
		t.Error("Delete reported the key absent")
	}
	if doc.Has("with") {
		t.Error("key still present after Delete")
	}
	if doc.Delete("with") {
		t.Error("second Delete reported the key present")
	}
}

func TestRequireAndErrorAnchors(t *testing.T) {
	doc, err := Parse("x.md", []byte("---\ntype: event\nid: \"\"\nstatus: scheduled\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Require("id"); err == nil {
		t.Error("empty required key accepted")
	} else if want := "x.md:3: key \"id\" must not be empty"; err.Error() != want {
		t.Errorf("got %q want %q", err, want)
	}
	if _, err := doc.Require("when"); err == nil {
		t.Error("missing required key accepted")
	} else if want := "x.md:1: missing required key \"when\""; err.Error() != want {
		t.Errorf("got %q want %q", err, want)
	}
	if got, want := doc.Errorf("status", "unknown status %q", "bogus").Error(), "x.md:4: unknown status \"bogus\""; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestNewProducesParseableDocument(t *testing.T) {
	doc := New("events/x.md", "\nDecide how expired schedules are handled.\n",
		[2]string{"type", "event"},
		[2]string{"id", "sync-20260904"},
		[2]string{"title", "Platform team sync in Zürich"},
		[2]string{"when", "2026-09-04T14:00:00+07:00"},
	)
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := "---\ntype: event\nid: sync-20260904\ntitle: Platform team sync in Zürich\nwhen: 2026-09-04T14:00:00+07:00\n---\n\nDecide how expired schedules are handled.\n"
	if string(out) != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
	if _, err := Parse("events/x.md", out); err != nil {
		t.Errorf("New produced an unparseable document: %v", err)
	}
}

// TestTypedReadErrorPositions covers documents that parse cleanly but whose
// values have the wrong shape for the key. These fail on read, not on parse.
func TestTypedReadErrorPositions(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "readerrors", "list-where-scalar.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse("list-where-scalar.md", raw)
	if err != nil {
		t.Fatalf("expected the document to parse: %v", err)
	}
	_, ok, err := doc.String("id")
	if !ok {
		t.Fatal("id reported absent")
	}
	want := "list-where-scalar.md:4: key \"id\" must be a single value, found a list"
	if err == nil || err.Error() != want {
		t.Errorf("got %v want %q", err, want)
	}
	if _, err := doc.Require("id"); err == nil || err.Error() != want {
		t.Errorf("Require: got %v want %q", err, want)
	}
}

// TestSetValuesSurviveReparse is the correctness half of leaving the YAML tag
// empty in scalar(): the encoder may pick a plain or a quoted form, but the raw
// text read back must always equal what was written.
func TestSetValuesSurviveReparse(t *testing.T) {
	values := []string{
		"2026-09-04T14:00:00+07:00", "2026-09-01", "60m", "14d", "pending",
		"true", "false", "null", "~", "007", "0x10", "1e3", "",
		"- not a list", "a: b", "Zürich sync 東京", "#hash", "@at", "*star",
		"[bracket", "{brace", "yes", "no", "on", "off", "  padded  ",
		"multi\nline", "quote\"inside", "'single'", "%percent", "&amp",
	}
	for _, v := range values {
		doc := New("x.md", "\nbody\n", [2]string{"type", "note"}, [2]string{"id", "x"})
		doc.Set("probe", v)
		doc.SetStrings("probes", []string{v, "sentinel"})
		out, err := doc.Bytes()
		if err != nil {
			t.Fatalf("%q: serialise: %v", v, err)
		}
		back, err := Parse("x.md", out)
		if err != nil {
			t.Fatalf("%q: re-parse of %q: %v", v, out, err)
		}
		got, ok, err := back.String("probe")
		if err != nil || !ok {
			t.Fatalf("%q: read back: %v %v", v, ok, err)
		}
		if got != v {
			t.Errorf("scalar %q round-tripped as %q via %q", v, got, out)
		}
		list, ok, err := back.Strings("probes")
		if err != nil || !ok || len(list) != 2 || list[0] != v || list[1] != "sentinel" {
			t.Errorf("list %q round-tripped as %v (%v, %v) via %q", v, list, ok, err, out)
		}
	}
}

// TestFrontmatterCRLFIsNormalised documents that CRLF line endings inside the
// frontmatter block become LF on rewrite, while the body is untouched. It is
// recorded as a test so the behaviour cannot change silently.
func TestFrontmatterCRLFIsNormalised(t *testing.T) {
	raw := "---\r\ntype: note\r\nid: x\r\n---\r\nbody\r\nkept\r\n"
	doc, err := Parse("x.md", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Body != "body\r\nkept\r\n" {
		t.Errorf("body changed: %q", doc.Body)
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if want := "---\ntype: note\nid: x\n---\nbody\r\nkept\r\n"; string(out) != want {
		t.Errorf("got %q want %q", out, want)
	}
}
