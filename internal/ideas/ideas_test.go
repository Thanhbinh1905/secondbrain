package ideas

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Thanhbinh1905/secondbrain/internal/payload"
	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
	"github.com/Thanhbinh1905/secondbrain/templates"
)

func fixture(t *testing.T) *query.Engine {
	t.Helper()
	v, err := vault.OpenAt(filepath.Join("..", "vault", "testdata", "good"))
	if err != nil {
		t.Fatal(err)
	}
	now, err := v.Zone.Normalise("2026-09-02T12:00")
	if err != nil {
		t.Fatal(err)
	}
	return query.New(v, now)
}

func built(t *testing.T) Model {
	t.Helper()
	stale, err := timeref.ParseSpan("14d")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Build(fixture(t), query.IdeaFilter{Status: "pending", Stale: stale, HasStale: true})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestTheCommittedTemplateIsTheOnlyMarkupOwner protects the visual contract:
// rendering may replace the one payload line and nothing else.
func TestTheCommittedTemplateIsTheOnlyMarkupOwner(t *testing.T) {
	if n := strings.Count(templates.Ideas, templates.DataSlot); n != 1 {
		t.Fatalf("the committed template carries %d data slots, want exactly 1", n)
	}
	m := built(t)
	page, err := RenderHTML(m)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := payload.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(templates.Ideas, templates.DataSlot, string(payload.Escape(raw)), 1)
	if string(page) != want {
		t.Error("the built page is not the committed template with only its data slot replaced")
	}
	carriedRaw, line, err := payload.Extract(string(page), Slot)
	if err != nil {
		t.Fatal(err)
	}
	carried, err := Validate("ideas.html", carriedRaw, line)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(carried, m) {
		t.Errorf("the page carries a different model:\n got %+v\nwant %+v", carried, m)
	}
}

func TestEmptyIdeasAreAnEmptyList(t *testing.T) {
	m, err := Build(fixture(t), query.IdeaFilter{Status: "dropped"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Ideas == nil {
		t.Fatal("an empty ideas result is nil, which marshals as null")
	}
	page, err := RenderHTML(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(page, []byte("No ideas match these filters.")) {
		t.Error("the committed template has no empty-state text")
	}
}

func TestAHostileTitleCannotTerminateTheDataBlock(t *testing.T) {
	m := built(t)
	const hostile = `</script><script>alert("pwned")</script>`
	m.Ideas[0].Title = hostile
	page, err := RenderHTML(m)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := payload.Extract(string(page), Slot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("<")) {
		t.Errorf("the payload carries a literal '<':\n%s", raw)
	}
	var carried Model
	if err := json.Unmarshal(raw, &carried); err != nil {
		t.Fatal(err)
	}
	if carried.Ideas[0].Title != hostile {
		t.Errorf("the title was altered: %q", carried.Ideas[0].Title)
	}
}

func TestValidationIsFailClosed(t *testing.T) {
	good, err := payload.Marshal(built(t))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"wrong schema", strings.Replace(string(good), Schema, "brain-ideas.v2", 1), "unsupported ideas schema"},
		{"missing ideas", strings.Replace(string(good), `  "ideas": [`, `  "omitted_ideas": [`, 1), "ideas is required"},
		{"null ideas", strings.Replace(string(good), `  "ideas": [`, `  "ideas": null, "discarded": [`, 1), "ideas is required"},
		{"bad filter status", strings.Replace(string(good), `"status": "pending"`, `"status": "maybe"`, 1), "unknown idea status"},
		{"bad stale span", strings.Replace(string(good), `"stale": "14d"`, `"stale": "later"`, 1), "stale:"},
		{"bad row status", strings.Replace(string(good), `      "status": "pending"`, `      "status": "maybe"`, 1), "valid statuses"},
		{"missing row field", strings.Replace(string(good), `      "touched":`, `      "omitted_touched":`, 1), "touched is required"},
		{"unknown field", strings.Replace(string(good), `  "schema":`, `  "surprise": true,
  "schema":`, 1), "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Validate("ideas.html", []byte(tc.raw), 40)
			if err == nil {
				t.Fatal("accepted an off-contract payload")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not explain the problem: %v", err)
			}
			if !strings.HasPrefix(err.Error(), "ideas.html:") {
				t.Errorf("error does not name the file and line: %v", err)
			}
		})
	}
}
