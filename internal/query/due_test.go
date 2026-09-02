package query

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// dueVault holds one record in each of due's three categories, plus records
// that must not be reported: a delegated task inside its horizon, an event
// outside the window, and an idea that is stale but not yet dormant.
func dueVault(t *testing.T) *vault.Vault {
	t.Helper()
	v := freshVault(t, t.TempDir())
	write(t, v, filepath.Join("people", "platform-team.md"), `---
type: person
id: platform-team
title: Platform team
created: 2026-01-01
---

body
`)
	write(t, v, filepath.Join("tasks", "late.md"), `---
type: task
id: late
title: migrate the staging database
status: waiting
assignee: platform-team
created: 2026-08-01
touched: 2026-08-01
follow_up_after: 7d
---

body
`)
	write(t, v, filepath.Join("tasks", "fresh.md"), `---
type: task
id: fresh
title: review CI capacity
status: waiting
assignee: platform-team
created: 2026-09-01
touched: 2026-09-01
follow_up_after: 30d
---

body
`)
	// An open task past its horizon with nobody holding it is not delegated,
	// so due leaves it to brief.
	write(t, v, filepath.Join("tasks", "mine.md"), `---
type: task
id: mine
title: my own late thing
status: open
created: 2026-08-01
touched: 2026-08-01
follow_up_after: 7d
---

body
`)
	write(t, v, filepath.Join("events", "soon.md"), `---
type: event
id: soon
title: Platform team sync
when: 2026-09-02T12:20:00+07:00
duration: 30m
status: scheduled
created: 2026-09-01
---

body
`)
	write(t, v, filepath.Join("events", "later.md"), `---
type: event
id: later
title: much later
when: 2026-09-02T16:00:00+07:00
duration: 30m
status: scheduled
created: 2026-09-01
---

body
`)
	write(t, v, filepath.Join("events", "cancelled.md"), `---
type: event
id: cancelled-soon
title: called off
when: 2026-09-02T12:15:00+07:00
status: cancelled
created: 2026-09-01
---

body
`)
	write(t, v, filepath.Join("ideas", "dormant.md"), `---
type: idea
id: dormant
title: an idea nobody has touched
status: pending
created: 2026-06-01
touched: 2026-06-01
---

body
`)
	write(t, v, filepath.Join("ideas", "stale-not-dormant.md"), `---
type: idea
id: stale-not-dormant
title: stale but not dormant
status: pending
created: 2026-08-10
touched: 2026-08-10
---

body
`)
	// A shipped idea has stopped decaying, however long ago it was touched.
	write(t, v, filepath.Join("ideas", "already-shipped.md"), `---
type: idea
id: already-shipped
title: done with
status: shipped
created: 2026-01-01
touched: 2026-01-02
---

body
`)
	return v
}

func dueEngine(t *testing.T) *Engine {
	t.Helper()
	v := dueVault(t)
	now, err := v.Zone.Normalise("2026-09-02T12:00")
	if err != nil {
		t.Fatal(err)
	}
	return New(v, now)
}

func dueIDs(items []DueItem) string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, string(item.Category)+":"+item.Record.ID)
	}
	return strings.Join(out, " ")
}

// TestDueReportsOnlyWhatCrossedALine: three categories, and nothing that is
// merely outstanding.
func TestDueReportsOnlyWhatCrossedALine(t *testing.T) {
	e := dueEngine(t)
	items, err := e.Due(DueFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// Delegated first, because it is the category with somebody else's name on
	// it; then the event about to start; then the dormant idea.
	if got := dueIDs(items); got != "delegated:late event:soon dormant_idea:dormant" {
		t.Errorf("due = %q", got)
	}
	// The delegated line names the person and how long it has been, because
	// that is what makes it actionable.
	if !strings.Contains(items[0].Reason, "platform-team") || !strings.Contains(items[0].Reason, "32d") {
		t.Errorf("the delegated reason does not name the person and the wait: %q", items[0].Reason)
	}
	if items[0].Person != "platform-team" {
		t.Errorf("person = %q", items[0].Person)
	}
	if items[1].MinutesUntil != 20 {
		t.Errorf("the event is %d minutes away, want 20", items[1].MinutesUntil)
	}
}

// TestEachDueCategoryIsIndependentlyTogglable: naming one reports only that
// one, and naming none reports all three.
func TestEachDueCategoryIsIndependentlyTogglable(t *testing.T) {
	e := dueEngine(t)
	cases := []struct {
		name   string
		filter DueFilter
		want   string
	}{
		{"delegated", DueFilter{Delegated: true}, "delegated:late"},
		{"events", DueFilter{Events: true}, "event:soon"},
		{"ideas", DueFilter{Ideas: true}, "dormant_idea:dormant"},
		{"two of them", DueFilter{Delegated: true, Ideas: true}, "delegated:late dormant_idea:dormant"},
		{"none means all", DueFilter{}, "delegated:late event:soon dormant_idea:dormant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, err := e.Due(tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if got := dueIDs(items); got != tc.want {
				t.Errorf("due = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNothingDueIsAnEmptyAnswerNotAnError: the ordinary case for a command run
// on a short interval is that there is nothing to say.
func TestNothingDueIsAnEmptyAnswerNotAnError(t *testing.T) {
	v := dueVault(t)
	// Before anything decayed past its window and long before the meeting.
	now, err := v.Zone.Normalise("2026-06-20T03:00")
	if err != nil {
		t.Fatal(err)
	}
	items, err := New(v, now).Due(DueFilter{})
	if err != nil {
		t.Fatalf("an empty answer must not be an error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("due = %v, want nothing", dueIDs(items))
	}
}

// TestDueWindowsComeFromTheConfig: each category's window is a vault setting,
// and moving it moves what is reported.
func TestDueWindowsComeFromTheConfig(t *testing.T) {
	cfg := testConfig()
	cfg.DueWithin = "10m"
	cfg.DormantAfter = "200d"
	cfg.FollowUpAfter = "365d"
	root := filepath.Join(t.TempDir(), "vault")
	if _, err := vault.Init(root, cfg, false); err != nil {
		t.Fatal(err)
	}
	src := dueVault(t)
	records, err := src.Walk()
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		raw, err := r.Doc().Bytes()
		if err != nil {
			t.Fatal(err)
		}
		write(t, v, r.Rel, string(raw))
	}
	now, err := v.Zone.Normalise("2026-09-02T12:00")
	if err != nil {
		t.Fatal(err)
	}
	items, err := New(v, now).Due(DueFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// The event is 20 minutes away and the window is 10; the idea is 93 days
	// old and dormancy is 200; the delegated task sets its own 7d horizon and
	// is still late, so only it survives.
	if got := dueIDs(items); got != "delegated:late" {
		t.Errorf("due = %q, want only the delegated task", got)
	}
}

// TestDueWritesNothing: a command run every few minutes must never change the
// vault it is watching.
func TestDueWritesNothing(t *testing.T) {
	v := dueVault(t)
	before := snapshot(t, v)
	now, err := v.Zone.Normalise("2026-09-02T12:00")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(v, now).Due(DueFilter{}); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, v); after != before {
		t.Error("due changed the vault")
	}
}

func snapshot(t *testing.T, v *vault.Vault) string {
	t.Helper()
	fresh, err := vault.OpenAt(v.Root)
	if err != nil {
		t.Fatal(err)
	}
	records, err := fresh.Walk()
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, r := range records {
		raw, err := r.Doc().Bytes()
		if err != nil {
			t.Fatal(err)
		}
		sb.WriteString(r.Rel)
		sb.Write(raw)
	}
	return sb.String()
}
