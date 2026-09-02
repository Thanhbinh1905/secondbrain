package review

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/render"
	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

var update = flag.Bool("update", false, "rewrite golden files")

// keys is a Reader over a fixed byte sequence.
type keys struct {
	seq []byte
	at  int
}

func (k *keys) ReadKey() (byte, error) {
	if k.at >= len(k.seq) {
		return 0, io.EOF
	}
	b := k.seq[k.at]
	k.at++
	return b, nil
}

// testConfig is vault.DefaultConfig with a timezone named, because
// DefaultConfig deliberately carries none: the tests' stored timestamps are
// written at +07:00, and Asia/Bangkok is that offset with no DST to shift a
// boundary.
func testConfig() vault.Config {
	cfg := vault.DefaultConfig()
	cfg.Timezone = "Asia/Bangkok"
	return cfg
}

func fixture(t *testing.T, stamp string) (*vault.Vault, []query.IdeaRow, *query.Engine) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if _, err := vault.Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	v, err := vault.OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"ideas/customer-referral.md": `---
type: idea
id: customer-referral
title: customer referral program
status: pending
created: 2026-08-09
touched: 2026-08-09
nudge_after: 14d
obsidian_prop: keep me
---

Every referrer earns a share of the reward. Unclear whether it counts the first order or the whole customer lifetime.
`,
		"ideas/shared-vault.md": `---
type: idea
id: shared-vault
title: share a vault across a team
status: pending
created: 2026-07-01
touched: 2026-07-01
---

Share a vault with a whole team instead of one person at a time.
`,
		"ideas/calendar-export.md": `---
type: idea
id: calendar-export
title: export the calendar as .ics
status: pending
created: 2026-08-30
touched: 2026-08-30
---

Export the calendar as .ics so another app can open it.
`,
		"tasks/migrate-staging-db.md": `---
type: task
id: migrate-staging-db
title: migrate the staging database
status: waiting
assignee: platform-team
created: 2026-08-01
touched: 2026-08-05
follow_up_after: 14d
---

Assigned to platform-team. Not checked in on yet.
`,
	}
	for rel, body := range files {
		if err := v.WriteFile(rel, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	now, err := v.Zone.Normalise(stamp)
	if err != nil {
		t.Fatal(err)
	}
	e := query.New(v, now)
	rows, err := e.Ideas(query.IdeaFilter{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	var stale []query.IdeaRow
	for _, r := range rows {
		if r.PastHorizon {
			stale = append(stale, r)
		}
	}
	return v, stale, e
}

func TestCardGolden(t *testing.T) {
	v, rows, _ := fixture(t, "2026-09-01T12:00")
	if len(rows) != 2 {
		t.Fatalf("expected two stale ideas, got %d", len(rows))
	}
	// The most neglected idea is reviewed first.
	if rows[0].Record.ID != "shared-vault" || rows[1].Record.ID != "customer-referral" {
		t.Fatalf("stale ideas are not most-neglected-first: %s then %s", rows[0].Record.ID, rows[1].Record.ID)
	}
	var buf bytes.Buffer
	o := &render.Out{W: &buf, TTY: true, Width: render.DefaultFrameWidth}
	fmt.Fprint(&buf, Card(o, FromIdeas(rows)[0], 0, len(rows), v.Zone).String())
	got := buf.String()
	path := filepath.Join("testdata", "card.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing golden file (run go test ./internal/review -update): %v", err)
		}
		if got != string(want) {
			t.Errorf("card drifted\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	}
	for i, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if w := unitext.Width(line); w != render.DefaultFrameWidth {
			t.Errorf("line %d is %d cells wide, frame is %d:\n%q", i, w, render.DefaultFrameWidth, line)
		}
	}
}

func TestActionForKey(t *testing.T) {
	cases := []struct {
		kind    vault.Kind
		want    map[byte]Action
		unbound []byte
	}{
		{
			kind: vault.KindIdea,
			want: map[byte]Action{
				'k': Keep, 'K': Keep,
				'b': Build, 'B': Build,
				'd': Drop, 'D': Drop,
				's': Defer, 'S': Defer,
				'q': Quit, 'Q': Quit,
				3: Quit, 4: Quit, 27: Quit,
			},
			// An idea has nothing "done" could mean, so the key stays unbound
			// rather than doing something else on the next card.
			unbound: []byte{'x', 'z', '1', ' ', '\n'},
		},
		{
			kind: vault.KindTask,
			want: map[byte]Action{
				'k': Keep, 'K': Keep,
				'x': Done, 'X': Done,
				'd': Drop, 'D': Drop,
				's': Defer, 'S': Defer,
				'q': Quit, 'Q': Quit,
				3: Quit, 4: Quit, 27: Quit,
			},
			// A task cannot be "started", so b is unbound here even though it
			// is the second key on an idea's card.
			unbound: []byte{'b', 'z', '1', ' ', '\n'},
		},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			for key, action := range tc.want {
				got, ok := ActionForKey(tc.kind, key)
				if !ok || got != action {
					t.Errorf("ActionForKey(%s, %d) = %q %v, want %q", tc.kind, key, got, ok, action)
				}
			}
			for _, key := range tc.unbound {
				if a, ok := ActionForKey(tc.kind, key); ok {
					t.Errorf("ActionForKey(%s, %q) bound to %q", tc.kind, string(rune(key)), a)
				}
			}
			// Every binding is reachable and the key bar names all of them.
			bar := KeyBar(tc.kind)
			for _, b := range BindingsFor(tc.kind) {
				if !strings.Contains(bar, string(rune(b.Key))+" "+b.Label) {
					t.Errorf("the key bar does not name %c %s: %q", b.Key, b.Label, bar)
				}
			}
		})
	}
}

func TestPlanForEveryAction(t *testing.T) {
	v, rows, _ := fixture(t, "2026-09-01T12:00")
	now, _ := v.Zone.Normalise("2026-09-01T12:00")
	r := rows[0].Record
	cases := map[Action]string{
		Keep:  "touched=2026-09-01",
		Build: "status=building,touched=2026-09-01",
		Drop:  "status=dropped,touched=2026-09-01",
		Defer: "touched=2026-09-01,nudge_after=30d",
		Quit:  "",
	}
	for action, want := range cases {
		changes, err := Plan(v, r, action, now)
		if err != nil {
			t.Fatalf("Plan(%q): %v", action, err)
		}
		var parts []string
		for _, c := range changes {
			parts = append(parts, c.Key+"="+c.Value)
		}
		if got := strings.Join(parts, ","); got != want {
			t.Errorf("Plan(%q) = %q, want %q", action, got, want)
		}
	}
	if _, err := Plan(v, r, Action("bogus"), now); err == nil {
		t.Error("an unknown action was planned")
	}
}

// TestRunAppliesEachDecisionImmediately: an interrupted session keeps the
// decisions already made, which is why the write is per keystroke.
func TestRunAppliesEachDecisionImmediately(t *testing.T) {
	v, rows, _ := fixture(t, "2026-09-01T12:00")
	now, _ := v.Zone.Normalise("2026-09-01T12:00")
	var buf bytes.Buffer
	o := &render.Out{W: &buf, TTY: false, Width: render.DefaultFrameWidth}
	// Reject an unbound key, then build the first card, then quit on the second.
	first, second := rows[0].Record.ID, rows[1].Record.ID
	s, err := Run(o, v, FromIdeas(rows), &keys{seq: []byte{'x', 'b', 'q'}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Quit {
		t.Error("quitting was not reported")
	}
	if s.Reviewed != 2 {
		t.Errorf("reviewed %d cards, want 2", s.Reviewed)
	}
	if len(s.Decisions) != 1 || s.Decisions[0].Action != Build {
		t.Fatalf("decisions = %+v", s.Decisions)
	}
	if !strings.Contains(buf.String(), "is not available") {
		t.Errorf("an unbound key was accepted silently:\n%s", buf.String())
	}
	if s.Decisions[0].ID != first {
		t.Errorf("the decision landed on %s, not the first card %s", s.Decisions[0].ID, first)
	}
	// The first card's idea was written, the second's was not.
	acted, err := v.Find(first)
	if err != nil {
		t.Fatal(err)
	}
	if acted.Status != "building" || acted.Touched.String() != "2026-09-01" {
		t.Errorf("%s = status %q touched %s", first, acted.Status, acted.Touched)
	}
	untouched, err := v.Find(second)
	if err != nil {
		t.Fatal(err)
	}
	if untouched.Status != "pending" || untouched.Touched.String() != "2026-08-09" {
		t.Errorf("the idea quit on was modified: status %q touched %s", untouched.Status, untouched.Touched)
	}
	// The unknown key and the body of the reviewed file both survived.
	raw, err := os.ReadFile(untouched.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "obsidian_prop: keep me") {
		t.Errorf("an unknown key was dropped:\n%s", raw)
	}
	if !strings.Contains(string(raw), "Every referrer earns a share of the reward.") {
		t.Errorf("the body did not survive:\n%s", raw)
	}
}

func TestRunThroughEveryCard(t *testing.T) {
	v, rows, _ := fixture(t, "2026-09-01T12:00")
	now, _ := v.Zone.Normalise("2026-09-01T12:00")
	var buf bytes.Buffer
	o := &render.Out{W: &buf, TTY: false, Width: render.DefaultFrameWidth}
	s, err := Run(o, v, FromIdeas(rows), &keys{seq: []byte{'k', 's'}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Quit {
		t.Error("a completed session reported quitting")
	}
	if len(s.Decisions) != 2 {
		t.Fatalf("decisions = %+v", s.Decisions)
	}
	kept, err := v.Find(rows[0].Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Status != "pending" || kept.Touched.String() != "2026-09-01" {
		t.Errorf("keep changed more than touched: status %q touched %s", kept.Status, kept.Touched)
	}
	// Keeping resets the horizon, so it is no longer stale.
	e := query.New(v, now)
	if v.PastHorizon(kept, now) {
		t.Error("a kept idea is still past its horizon")
	}
	deferred, err := v.Find(rows[1].Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !deferred.HasNudge || deferred.NudgeAfter.ApproxDays() != DeferDays {
		t.Errorf("defer did not set a %dd horizon: %+v", DeferDays, deferred.NudgeAfter)
	}
	rowsAfter, err := e.Ideas(query.IdeaFilter{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rowsAfter {
		if r.PastHorizon {
			t.Errorf("%s is still stale after review", r.Record.ID)
		}
	}
}

func TestRunEndsOnExhaustedInput(t *testing.T) {
	v, rows, _ := fixture(t, "2026-09-01T12:00")
	now, _ := v.Zone.Normalise("2026-09-01T12:00")
	var buf bytes.Buffer
	o := &render.Out{W: &buf}
	_, err := Run(o, v, FromIdeas(rows), &keys{}, now)
	if !errors.Is(err, io.EOF) {
		t.Errorf("running out of input gave %v, want io.EOF", err)
	}
}

func TestWrapBody(t *testing.T) {
	got := wrapBody("Every referrer earns a share of the reward. Unclear whether it counts the first order or the whole customer lifetime.", 40)
	for _, line := range got {
		if unitext.Width(line) > 40 {
			t.Errorf("line %q is %d cells wide", line, unitext.Width(line))
		}
	}
	if len(got) < 2 {
		t.Errorf("a long body was not wrapped: %v", got)
	}
	if strings.Join(got, " ") != "Every referrer earns a share of the reward. Unclear whether it counts the first order or the whole customer lifetime." {
		t.Errorf("wrapping lost or reordered text: %v", got)
	}
	if got := wrapBody("   \n\n  ", 40); len(got) != 1 || got[0] != "(no body)" {
		t.Errorf("an empty body gave %v", got)
	}
}

// TestTaskTriageWritesTaskKeys: a task and an idea share one screen but not one
// vocabulary. Deferring a task must write follow_up_after:, not nudge_after:,
// or the horizon it writes is one the record's own kind never reads.
func TestTaskTriageWritesTaskKeys(t *testing.T) {
	v, _, e := fixture(t, "2026-09-02T12:00")
	now := e.Now
	task, err := v.Find("migrate-staging-db")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		action Action
		want   map[string]string
	}{
		{Keep, map[string]string{"touched": "2026-09-02"}},
		{Done, map[string]string{"status": "done", "touched": "2026-09-02"}},
		{Drop, map[string]string{"status": "dropped", "touched": "2026-09-02"}},
		{Defer, map[string]string{"touched": "2026-09-02", "follow_up_after": "30d"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.action), func(t *testing.T) {
			changes, err := Plan(v, task, tc.action, now)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, c := range changes {
				got[c.Key] = c.Value
			}
			if len(got) != len(tc.want) {
				t.Fatalf("changes = %v, want %v", got, tc.want)
			}
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("%s = %q, want %q", k, got[k], want)
				}
			}
			if _, wrong := got["nudge_after"]; wrong {
				t.Error("a task was given nudge_after:, which a task never reads")
			}
		})
	}

	// The two kinds' exclusive actions are refused rather than reinterpreted.
	if _, err := Plan(v, task, Build, now); err == nil {
		t.Error("a task was moved to building, which is not one of its statuses")
	}
	idea, err := v.Find("customer-referral")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Plan(v, idea, Done, now); err == nil {
		t.Error("an idea accepted the task-only done action")
	}
	// Deferring an idea still writes the idea's own key.
	changes, err := Plan(v, idea, Defer, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range changes {
		if c.Key == "nudge_after" {
			found = true
		}
		if c.Key == "follow_up_after" {
			t.Error("an idea was given follow_up_after:, which an idea never reads")
		}
	}
	if !found {
		t.Errorf("deferring an idea did not write nudge_after: %v", changes)
	}
}

// TestTaskCardShowsWhoHasItAndWhenItWasDue: the two facts that decide a
// follow-up lead the card rather than being buried in the body.
func TestTaskCardShowsWhoHasItAndWhenItWasDue(t *testing.T) {
	v, _, e := fixture(t, "2026-09-02T12:00")
	rows, err := e.Tasks(query.TaskFilter{OnlyOpen: true, PastFollowUp: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Record.ID != "migrate-staging-db" {
		t.Fatalf("expected the one unchecked task, got %d rows", len(rows))
	}
	var buf bytes.Buffer
	o := &render.Out{W: &buf, TTY: true, Width: render.DefaultFrameWidth}
	card := Card(o, FromTasks(rows)[0], 0, 1, v.Zone)
	got := card.String()
	for _, want := range []string{"migrate-staging-db", "assigned to platform-team", "waiting", "follow up 14d", "x done"} {
		if !strings.Contains(got, want) {
			t.Errorf("the task card does not show %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "b build") {
		t.Errorf("the task card offers an idea-only action:\n%s", got)
	}
	// The frame stays exactly as wide with an assignee line on it.
	for i, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if w := unitext.Width(line); w != render.DefaultFrameWidth {
			t.Errorf("line %d is %d cells wide, frame is %d:\n%q", i, w, render.DefaultFrameWidth, line)
		}
	}
}
