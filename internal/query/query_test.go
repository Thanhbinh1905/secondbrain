package query

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

func engine(t *testing.T, stamp string) *Engine {
	t.Helper()
	v, err := vault.OpenAt(filepath.Join("..", "vault", "testdata", "good"))
	if err != nil {
		t.Fatal(err)
	}
	now, err := v.Zone.Normalise(stamp)
	if err != nil {
		t.Fatal(err)
	}
	return New(v, now)
}

func ids(occs []vault.Occurrence) []string {
	out := make([]string, 0, len(occs))
	for _, o := range occs {
		out = append(out, o.Record.ID+"@"+o.Start.Format("2006-01-02T15:04"))
	}
	return out
}

// TestTodayIsChronologicalAndFlagsNext covers US-4 and FR-4.
func TestTodayIsChronologicalAndFlagsNext(t *testing.T) {
	// 2026-09-02 is a Wednesday: standup recurs, and the pitch deck review is
	// a one-off at 15:00.
	e := engine(t, "2026-09-02T12:00")
	a, err := e.Today()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(ids(a.Occurrences), " ")
	want := "standup@2026-09-02T09:00 referral-pitch-review@2026-09-02T15:00"
	if got != want {
		t.Errorf("today = %q, want %q", got, want)
	}
	next, ok := a.Next()
	if !ok {
		t.Fatal("no next event at 12:00 on a day with a 15:00 event")
	}
	if next.Record.ID != "referral-pitch-review" {
		t.Errorf("next = %s", next.Record.ID)
	}
	// After the last event, nothing is next.
	e = engine(t, "2026-09-02T23:00")
	a, err = e.Today()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Next(); ok {
		t.Error("an event is still next at 23:00")
	}
	// An in-progress event is the next one, not a past one.
	e = engine(t, "2026-09-02T15:30")
	a, _ = e.Today()
	if next, ok := a.Next(); !ok || next.Record.ID != "referral-pitch-review" {
		t.Errorf("an in-progress event is not flagged next: %v %v", next.Record, ok)
	}
	// An empty day returns no occurrences rather than an error.
	e = engine(t, "2026-09-06T10:00")
	a, err = e.Today()
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Occurrences) != 0 {
		t.Errorf("Sunday has %d occurrences: %v", len(a.Occurrences), ids(a.Occurrences))
	}
	if _, ok := a.Next(); ok {
		t.Error("an empty day reported a next event")
	}
}

// TestRecurrenceIsExpandedAtQueryTimeWithExceptions covers the recurrence
// contract: a series lives in one file, expands only for the window asked
// about, and its exceptions are honoured.
func TestRecurrenceIsExpandedAtQueryTimeWithExceptions(t *testing.T) {
	e := engine(t, "2026-09-02T08:00")
	from, _ := timeref.ParseDateOnly("2026-08-31")
	to, _ := timeref.ParseDateOnly("2026-09-06")
	a, err := e.Agenda(from, to)
	if err != nil {
		t.Fatal(err)
	}
	var standups []string
	for _, o := range a.Occurrences {
		if o.Record.ID == "standup" {
			standups = append(standups, o.Start.Format("2006-01-02"))
			if !o.Recurring {
				t.Error("an expanded occurrence is not marked recurring")
			}
		}
	}
	// Mon to Fri, minus the 2026-09-03 exception. The series starts 2026-09-02,
	// so 2026-08-31 and 2026-09-01 precede it.
	want := "2026-09-02 2026-09-04"
	if got := strings.Join(standups, " "); got != want {
		t.Errorf("standups = %q, want %q", got, want)
	}
	// Nothing was written to disk by expanding.
	root := filepath.Join("..", "vault", "testdata", "good", "events")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Errorf("events/ holds %d files after expansion; a series must stay one file", len(entries))
	}
}

func TestAgendaSkipsCancelledKeepsDone(t *testing.T) {
	dir := t.TempDir()
	v := freshVault(t, dir)
	write(t, v, "events/2026-09-02-cancelled.md", `---
type: event
id: cancelled-one
title: cancelled
when: 2026-09-02T10:00:00+07:00
status: cancelled
created: 2026-09-01
---

body
`)
	write(t, v, "events/2026-09-02-done.md", `---
type: event
id: done-one
title: done
when: 2026-09-02T11:00:00+07:00
status: done
created: 2026-09-01
---

body
`)
	now, _ := v.Zone.Normalise("2026-09-02T09:00")
	a, err := New(v, now).Today()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids(a.Occurrences), " "); got != "done-one@2026-09-02T11:00" {
		t.Errorf("today = %q; a cancelled event must be off the calendar and a done one on it", got)
	}
	if _, ok := a.Next(); ok {
		t.Error("a completed event was flagged as next")
	}
}

// TestIdeasCarryAgeAndStaleness covers US-5 and FR-5.
func TestIdeasCarryAgeAndStaleness(t *testing.T) {
	e := engine(t, "2026-09-01T12:00")
	rows, err := e.Ideas(IdeaFilter{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%s:%dd:%v", r.Record.ID, r.AgeDays, r.PastHorizon))
	}
	want := "customer-referral:23d:true shared-vault:8d:false calendar-export:2d:false"
	if strings.Join(got, " ") != want {
		t.Errorf("ideas = %q, want %q", strings.Join(got, " "), want)
	}
	// The shipped idea is excluded by the status filter, included without one.
	rows, err = e.Ideas(IdeaFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Errorf("unfiltered ideas = %d, want 4", len(rows))
	}
	// --stale 14d keeps only what has been ignored at least that long.
	stale, _ := timeref.ParseSpan("14d")
	rows, err = e.Ideas(IdeaFilter{Status: "pending", Stale: stale, HasStale: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Record.ID != "customer-referral" {
		t.Errorf("--stale 14d gave %d rows: %v", len(rows), rows)
	}
	// An unknown status is rejected against the closed vocabulary.
	if _, err := e.Ideas(IdeaFilter{Status: "maybe"}); err == nil {
		t.Error("an unknown status filter was accepted")
	} else if !strings.Contains(err.Error(), "pending, building, shipped, dropped") {
		t.Errorf("the error does not list the vocabulary: %v", err)
	}
}

func TestIdeaHorizonUsesOwnValueThenVaultDefault(t *testing.T) {
	dir := t.TempDir()
	v := freshVault(t, dir)
	write(t, v, "ideas/own.md", `---
type: idea
id: own-horizon
title: has its own horizon
status: pending
created: 2026-08-25
touched: 2026-08-25
nudge_after: 3d
---

body
`)
	write(t, v, "ideas/default.md", `---
type: idea
id: default-horizon
title: uses the vault default
status: pending
created: 2026-08-25
touched: 2026-08-25
---

body
`)
	now, _ := v.Zone.Normalise("2026-09-01T12:00")
	rows, err := New(v, now).Ideas(IdeaFilter{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]IdeaRow{}
	for _, r := range rows {
		byID[r.Record.ID] = r
	}
	if got := byID["own-horizon"]; got.HorizonDays != 3 || !got.PastHorizon {
		t.Errorf("own horizon: %+v", got)
	}
	if got := byID["default-horizon"]; got.HorizonDays != 14 || got.PastHorizon {
		t.Errorf("default horizon: %+v", got)
	}
}

// TestSearchMatchesDiacriticsBothWays covers US-7 and FR-6.
func TestSearchMatchesDiacriticsBothWays(t *testing.T) {
	e := engine(t, "2026-09-02T12:00")
	// The ranking rules are: a diacritic-exact match before a folded one, then
	// id before title before tags before body, then most recently touched.
	cases := map[string]string{
		"program":        "customer-referral", // only its title carries it
		"krakow rollout": "daily-2026-09-01",  // the only text holding the phrase
		"Kraków rollout": "daily-2026-09-01",  // the same record, with diacritics
		"capacity":       "ci-capacity",
		"zurich":         "ci-capacity",                 // folded title match
		"Zürich":         "ci-capacity",                 // exact title match, and it outranks the daily's body
		"sync":           "platform-team-sync-20260904", // an id match beats standup's body link
		"pitch deck":     "referral-pitch-review",
		"Málaga":         "", // nothing in the vault mentions it
	}
	for q, wantFirst := range cases {
		hits, err := e.Search(q, 10)
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		if wantFirst == "" {
			if len(hits) != 0 {
				t.Errorf("Search(%q) found %d hits, want none", q, len(hits))
			}
			continue
		}
		if len(hits) == 0 {
			t.Errorf("Search(%q) found nothing", q)
			continue
		}
		if hits[0].Record.ID != wantFirst {
			t.Errorf("Search(%q) ranked %s first, want %s", q, hits[0].Record.ID, wantFirst)
		}
		if hits[0].Line == "" {
			t.Errorf("Search(%q) returned no context line", q)
		}
	}
	// The same record is reached from both spellings, and only the spelling
	// that kept its diacritics is reported as an exact match.
	withMarks, err := e.Search("Kraków rollout", 10)
	if err != nil {
		t.Fatal(err)
	}
	without, err := e.Search("krakow rollout", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(withMarks) == 0 || len(without) == 0 {
		t.Fatal("one of the two spellings found nothing")
	}
	if withMarks[0].Record.ID != without[0].Record.ID {
		t.Errorf("the two spellings found different records: %s vs %s", withMarks[0].Record.ID, without[0].Record.ID)
	}
	if !withMarks[0].Exact {
		t.Error("a diacritic-exact match was not ranked as exact")
	}
	if without[0].Exact {
		t.Error("a diacritic-free query was reported as an exact match")
	}
	// An id match outranks a body match for the same query.
	ranked, err := e.Search("referral", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range ranked {
		if h.Field == "body" {
			break
		}
		if h.Field != "id" && h.Field != "title" {
			t.Errorf("%s matched on %q before any body match", h.Record.ID, h.Field)
		}
	}
	// The limit is honoured.
	if hits, _ := e.Search("a", 2); len(hits) > 2 {
		t.Errorf("limit ignored: %d hits", len(hits))
	}
	if hits, _ := e.Search("   ", 10); len(hits) != 0 {
		t.Error("a blank query returned hits")
	}
}

func TestSearchLineNumbersPointIntoTheFile(t *testing.T) {
	e := engine(t, "2026-09-02T12:00")
	hits, err := e.Search("expired schedules", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hit for a body phrase")
	}
	h := hits[0]
	raw, err := os.ReadFile(h.Record.Path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	if h.LineNo < 1 || h.LineNo > len(lines) {
		t.Fatalf("line %d is outside a %d-line file", h.LineNo, len(lines))
	}
	if got := strings.TrimSpace(lines[h.LineNo-1]); got != h.Line {
		t.Errorf("line %d of %s is %q, hit reported %q", h.LineNo, h.Record.Rel, got, h.Line)
	}
}

func TestLinksAndBacklinks(t *testing.T) {
	e := engine(t, "2026-09-02T12:00")
	idea, err := e.Vault.Find("customer-referral")
	if err != nil {
		t.Fatal(err)
	}
	g, err := e.Links(idea)
	if err != nil {
		t.Fatal(err)
	}
	var back []string
	for _, b := range g.Backlinks {
		// Each backlink names the field that did the pointing, so "this meeting
		// produced that idea" is distinguishable from a passing mention.
		back = append(back, b.Record.ID+"("+strings.Join(b.Via, "+")+")")
	}
	if got := strings.Join(back, ","); got != "platform-team(body),referral-pitch-review(body)" {
		t.Errorf("backlinks = %q", got)
	}
	// An event's with: list resolves to people profiles.
	ev, err := e.Vault.Find("platform-team-sync-20260904")
	if err != nil {
		t.Fatal(err)
	}
	g, err = e.Links(ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.With) != 1 || g.With[0].ID != "platform-team" || !g.With[0].Resolved() {
		t.Errorf("with = %+v", g.With)
	}
	// A dangling link is reported, not dropped.
	dir := t.TempDir()
	v := freshVault(t, dir)
	write(t, v, "notes/dangling.md", `---
type: note
id: dangling
title: dangling link
created: 2026-09-01
---

Refers to [[nobody-home]].
`)
	rec, err := v.Find("dangling")
	if err != nil {
		t.Fatal(err)
	}
	now, _ := v.Zone.Normalise("2026-09-01T12:00")
	g, err = New(v, now).Links(rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Body) != 1 || g.Body[0].ID != "nobody-home" || g.Body[0].Resolved() {
		t.Errorf("a dangling body link was not reported unresolved: %+v", g.Body)
	}
	if len(g.Outgoing) != 0 {
		t.Errorf("a body wiki-link leaked into the links: field: %+v", g.Outgoing)
	}
}

// TestFrontmatterLinksResolveAndDangle: the linking layer is a frontmatter
// field a human maintains, and a link to something not captured yet is
// reported unresolved rather than rejected - the precedent with: and assignee
// already set.
func TestFrontmatterLinksResolveAndDangle(t *testing.T) {
	dir := t.TempDir()
	v := freshVault(t, dir)
	write(t, v, "events/meeting.md", `---
type: event
id: meeting
title: Platform team sync
when: 2026-09-04T14:00:00+07:00
status: scheduled
created: 2026-09-01
---

body
`)
	write(t, v, "ideas/produced.md", `---
type: idea
id: produced
title: cache the expiry lookup
status: pending
created: 2026-09-01
touched: 2026-09-01
links: [meeting, never-captured]
---

body
`)
	now, _ := v.Zone.Normalise("2026-09-01T12:00")
	e := New(v, now)
	idea, err := v.Find("produced")
	if err != nil {
		t.Fatal(err)
	}
	g, err := e.Links(idea)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Outgoing) != 2 {
		t.Fatalf("links: = %+v", g.Outgoing)
	}
	if g.Outgoing[0].ID != "meeting" || !g.Outgoing[0].Resolved() {
		t.Errorf("a link to a record that exists was not resolved: %+v", g.Outgoing[0])
	}
	if g.Outgoing[1].ID != "never-captured" || g.Outgoing[1].Resolved() {
		t.Errorf("a link to nothing was not reported unresolved: %+v", g.Outgoing[1])
	}
	// The meeting can answer what it produced, which is the source query the
	// linking layer exists for.
	meeting, err := v.Find("meeting")
	if err != nil {
		t.Fatal(err)
	}
	mg, err := e.Links(meeting)
	if err != nil {
		t.Fatal(err)
	}
	if len(mg.Backlinks) != 1 || mg.Backlinks[0].Record.ID != "produced" {
		t.Fatalf("the meeting has no backlink from what it produced: %+v", mg.Backlinks)
	}
	if strings.Join(mg.Backlinks[0].Via, "+") != "links" {
		t.Errorf("the backlink does not name the field that points: %v", mg.Backlinks[0].Via)
	}
}

func TestPersonReferencesResolveOnlyToPersonRecords(t *testing.T) {
	v := freshVault(t, t.TempDir())
	write(t, v, "ideas/alice.md", "---\ntype: idea\nid: alice\ntitle: not a person\nstatus: pending\ncreated: 2026-09-01\ntouched: 2026-09-01\n---\n\nbody\n")
	write(t, v, "tasks/assigned.md", "---\ntype: task\nid: assigned\ntitle: assigned\nstatus: open\nassignee: alice\ncreated: 2026-09-01\ntouched: 2026-09-01\n---\n\nbody\n")
	now, _ := v.Zone.Normalise("2026-09-02T12:00")
	task, err := v.Find("assigned")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := New(v, now).Links(task)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Assignee.Resolved() {
		t.Errorf("assignee resolved to a non-person record: %+v", graph.Assignee.Record)
	}
	idea, err := v.Find("alice")
	if err != nil {
		t.Fatal(err)
	}
	graph, err = New(v, now).Links(idea)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Backlinks) != 0 {
		t.Errorf("a non-person record received person-field backlinks: %+v", graph.Backlinks)
	}
}

func TestWeekUsesConfiguredFirstDay(t *testing.T) {
	e := engine(t, "2026-09-02T12:00")
	a, err := e.Week()
	if err != nil {
		t.Fatal(err)
	}
	// The vault starts weeks on Monday, so the week of Wednesday 2026-09-02 is
	// 2026-08-31 to 2026-09-06.
	if a.From.String() != "2026-08-31" || a.To.String() != "2026-09-06" {
		t.Errorf("week = %s..%s", a.From, a.To)
	}
	loads, _, err := e.WeekLoad()
	if err != nil {
		t.Fatal(err)
	}
	if len(loads) != 7 {
		t.Fatalf("WeekLoad returned %d days", len(loads))
	}
	var counts []string
	for _, l := range loads {
		counts = append(counts, fmt.Sprintf("%s=%d", l.Date, l.Count))
	}
	// 2026-09-02 is a Wednesday. Standup recurs Mon-Fri, so it lands on
	// Wednesday and Friday but not the excepted Thursday or the weekend.
	want := "2026-08-31=0 2026-09-01=0 2026-09-02=2 2026-09-03=0 2026-09-04=2 2026-09-05=1 2026-09-06=0"
	if got := strings.Join(counts, " "); got != want {
		t.Errorf("week load = %q\n              want %q", got, want)
	}
}

func TestBriefSurfacesStaleAndUpcoming(t *testing.T) {
	e := engine(t, "2026-09-02T08:00")
	b, err := e.Brief(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Today.Occurrences) != 2 {
		t.Errorf("brief today = %v", ids(b.Today.Occurrences))
	}
	if len(b.Stale) != 1 || b.Stale[0].Record.ID != "customer-referral" {
		t.Errorf("brief stale = %+v", b.Stale)
	}
	if len(b.Upcoming) == 0 {
		t.Error("brief has no upcoming events")
	}
	for _, o := range b.Upcoming {
		if e.Vault.Zone.DateOf(o.Start) == e.Vault.Zone.DateOf(e.Now) {
			t.Errorf("upcoming includes today: %s", o.Record.ID)
		}
	}
}

// TestQueryLatencyOnFiveThousandFiles is NFR-1: any query under 100 ms on a
// 5,000-file vault, with no index to help.
// EnvLatencyBudget overrides the NFR-1 per-query budget the latency tests
// assert against.
//
// NFR-1's hundred milliseconds is a statement about a developer's own host: a
// real machine running an interactive CLI. Asserting that same absolute wall
// clock on arbitrary shared CI hardware measures the runner, not the code - a
// shared two-core runner has been observed taking up to ~470 ms for `search`
// and ~300-330 ms for the other queries, and that range moves with runner
// load, not with the code. The budget is therefore an explicit input with the
// strict value as its default, and the measurement is always logged either
// way, so a genuine order-of-magnitude regression still fails everywhere.
const EnvLatencyBudget = "BRAIN_AXI_LATENCY_BUDGET"

// latencyBudget is the per-query budget these tests hold the code to.
func latencyBudget(t *testing.T) time.Duration {
	t.Helper()
	raw, ok := os.LookupEnv(EnvLatencyBudget)
	if !ok {
		return 100 * time.Millisecond
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("$%s=%q is not a duration: %v", EnvLatencyBudget, raw, err)
	}
	if d <= 0 {
		t.Fatalf("$%s=%q must be positive", EnvLatencyBudget, raw)
	}
	return d
}

func TestQueryLatencyOnFiveThousandFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 5,000-file vault")
	}
	dir := t.TempDir()
	v := freshVault(t, dir)
	base, err := timeref.ParseDateOnly("2020-01-01")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2500; i++ {
		d := base.AddDays(i % 2000)
		write(t, v, fmt.Sprintf("events/%s-event-%04d.md", d, i), fmt.Sprintf(`---
type: event
id: event-%04d
title: meeting %d about the schedule
when: %sT09:00:00+07:00
duration: 30m
status: scheduled
created: 2020-01-01
---

Notes from meeting %d, on the rollout schedule and capacity.
`, i, i, d, i))
	}
	for i := 0; i < 1500; i++ {
		write(t, v, fmt.Sprintf("ideas/idea-%04d.md", i), fmt.Sprintf(`---
type: idea
id: idea-%04d
title: idea %d
status: pending
created: 2025-01-01
touched: 2025-06-%02d
---

Idea %d about the referral programme.
`, i, i, (i%28)+1, i))
	}
	for i := 0; i < 500; i++ {
		d := base.AddDays(i % 2000)
		write(t, v, fmt.Sprintf("tasks/task-%04d.md", i), fmt.Sprintf(`---
type: task
id: task-%04d
title: task %d to follow up
status: open
due: %sT17:00:00+07:00
created: 2025-01-01
touched: 2025-06-%02d
follow_up_after: 7d
---

Task %d, handed to the datacentre team.
`, i, i, d, (i%28)+1, i))
	}
	for i := 0; i < 1000; i++ {
		d := base.AddDays(i)
		write(t, v, fmt.Sprintf("daily/%s.md", d), fmt.Sprintf(`---
type: daily
id: daily-%s
title: %s
created: %s
touched: %s
---

- 09:00 quick note %d
`, d, d, d, d, i))
	}
	count := 0
	if err := filepath.WalkDir(v.Root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
			count++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count < 5000 {
		t.Fatalf("the fixture vault has only %d files", count)
	}

	// Every measurement reopens the vault, because that is what a real
	// invocation is: a fresh process that has parsed nothing yet. Measuring a
	// second query against the same Vault value would measure the
	// per-invocation memo, which no CLI run ever benefits from.
	warm, err := vault.OpenAt(v.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := warm.Walk(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		run  func(*Engine) error
	}{
		{"today", func(e *Engine) error { _, err := e.Today(); return err }},
		{"week", func(e *Engine) error { _, err := e.Week(); return err }},
		{"ideas", func(e *Engine) error { _, err := e.Ideas(IdeaFilter{Status: "pending"}); return err }},
		{"search", func(e *Engine) error { _, err := e.Search("krakow rollout", 20); return err }},
		{"brief", func(e *Engine) error { _, err := e.Brief(7); return err }},
		{"tasks", func(e *Engine) error { _, err := e.Tasks(TaskFilter{OnlyOpen: true}); return err }},
		// What `brain-axi today` actually does: the agenda plus the task block
		// beside it. Measuring only the agenda would miss half the command.
		{"today+tasks", func(e *Engine) error {
			if _, err := e.Today(); err != nil {
				return err
			}
			_, err := e.Tasks(TaskFilter{OnlyOpen: true})
			return err
		}},
		// `due` is meant to be run on a short interval, so it is held to the
		// same budget as the queries a human waits on.
		{"due", func(e *Engine) error { _, err := e.Due(DueFilter{}); return err }},
		{"board", func(e *Engine) error {
			if _, err := e.Today(); err != nil {
				return err
			}
			if _, err := e.Week(); err != nil {
				return err
			}
			if _, err := e.PersonAgendas(); err != nil {
				return err
			}
			if _, err := e.Tasks(TaskFilter{OnlyOpen: true}); err != nil {
				return err
			}
			_, err := e.Ideas(IdeaFilter{Status: "pending"})
			return err
		}},
	} {
		fresh, err := vault.OpenAt(v.Root)
		if err != nil {
			t.Fatal(err)
		}
		now, err := fresh.Zone.Normalise("2024-06-12T10:00")
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := tc.run(New(fresh, now)); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		elapsed := time.Since(start)
		t.Logf("%s on %d files, from a cold vault: %v", tc.name, count, elapsed)
		if budget := latencyBudget(t); elapsed > budget {
			t.Errorf("%s took %v on %d files, over the %v budget (NFR-1)", tc.name, elapsed, count, budget)
		}
	}
}

// TestCaptureLatencyOnFiveThousandFiles: a capture asks the vault three
// questions - a free id, a free path and an overlap check - and must still fit
// the budget. Without the per-invocation memo this took 103 ms.
func TestCaptureLatencyOnFiveThousandFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 5,000-file vault")
	}
	dir := t.TempDir()
	v := freshVault(t, dir)
	base, err := timeref.ParseDateOnly("2020-01-01")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2500; i++ {
		d := base.AddDays(i % 2000)
		write(t, v, fmt.Sprintf("events/%s-e-%04d.md", d, i), fmt.Sprintf(`---
type: event
id: e-%04d
title: meeting %d
when: %sT09:00:00+07:00
duration: 30m
status: scheduled
created: 2020-01-01
---

Body.
`, i, i, d))
	}
	for i := 0; i < 2500; i++ {
		write(t, v, fmt.Sprintf("ideas/i-%04d.md", i), fmt.Sprintf(`---
type: idea
id: i-%04d
title: idea %d
status: pending
created: 2025-01-01
touched: 2025-06-01
---

Body.
`, i, i))
	}
	if _, err := v.Walk(); err != nil {
		t.Fatal(err)
	}

	fresh, err := vault.OpenAt(v.Root)
	if err != nil {
		t.Fatal(err)
	}
	now, err := fresh.Zone.Normalise("2024-06-12T10:00")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := fresh.FreeID("new-thing"); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.FreePath("events/2024-06-12-new-thing.md"); err != nil {
		t.Fatal(err)
	}
	d := fresh.Zone.DateOf(now)
	if _, err := New(fresh, now).Agenda(d, d); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("the three questions a capture asks, on 5000 files, from a cold vault: %v", elapsed)
	if budget := latencyBudget(t); elapsed > budget {
		t.Errorf("a capture took %v on 5000 files, over the %v budget (NFR-1)", elapsed, budget)
	}
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

func freshVault(t *testing.T, dir string) *vault.Vault {
	t.Helper()
	root := filepath.Join(dir, "vault")
	if _, err := vault.Init(root, testConfig(), false); err != nil {
		t.Fatal(err)
	}
	v, err := vault.OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func write(t *testing.T, v *vault.Vault, rel, body string) {
	t.Helper()
	if err := v.WriteFile(rel, []byte(body)); err != nil {
		t.Fatal(err)
	}
}

// TestTasksAreOrderedByWhatNeedsAttentionFirst: overdue before due, soonest
// due before latest, then longest unchecked. A list in file order is not an
// answer to "what am I waiting on".
func TestTasksAreOrderedByWhatNeedsAttentionFirst(t *testing.T) {
	e := engine(t, "2026-09-02T12:00")
	rows, err := e.Tasks(TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("the fixture holds %d tasks, want 2", len(rows))
	}
	// The one with a due date comes first; the undated one is ordered by age.
	if rows[0].Record.ID != "review-backup-policy" || rows[1].Record.ID != "migrate-staging-db" {
		t.Errorf("order = %s then %s", rows[0].Record.ID, rows[1].Record.ID)
	}
	if rows[0].DueInDays != 3 {
		t.Errorf("due_in_days = %d, want 3", rows[0].DueInDays)
	}
	if rows[0].Overdue {
		t.Error("a task due in three days is reported as overdue")
	}
	if rows[0].PastHorizon {
		t.Error("a task touched yesterday with a 3d horizon is past it")
	}
	// The delegated one is 28 days unchecked against its own 14d horizon.
	if rows[1].AgeDays != 28 || rows[1].HorizonDays != 14 || !rows[1].PastHorizon {
		t.Errorf("delegated row = %+v, want 28d against a 14d horizon and past it", rows[1])
	}
}

func TestTaskFilters(t *testing.T) {
	e := engine(t, "2026-09-24T12:00")
	cases := []struct {
		name   string
		filter TaskFilter
		want   []string
	}{
		{"everything", TaskFilter{}, []string{"review-backup-policy", "migrate-staging-db"}},
		{"only open keeps open and waiting", TaskFilter{OnlyOpen: true}, []string{"review-backup-policy", "migrate-staging-db"}},
		{"by status", TaskFilter{Status: "waiting"}, []string{"migrate-staging-db"}},
		{"by assignee", TaskFilter{Assignee: "platform-team"}, []string{"migrate-staging-db"}},
		{"past their follow-up horizon", TaskFilter{PastFollowUp: true}, []string{"review-backup-policy", "migrate-staging-db"}},
		{"a status nothing has", TaskFilter{Status: "dropped"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := e.Tasks(tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range rows {
				got = append(got, r.Record.ID)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}

	// A status outside the closed vocabulary is refused, listing the valid ones.
	if _, err := e.Tasks(TaskFilter{Status: "maybe"}); err == nil {
		t.Error("an unknown task status was accepted")
	} else if !strings.Contains(err.Error(), "open, waiting, done, dropped") {
		t.Errorf("%q does not list the valid statuses", err)
	}
}

// TestClosedTasksStopDecaying: nagging about a task the user already
// finished is exactly the noise that makes a user stop reading what comes back.
func TestClosedTasksStopDecaying(t *testing.T) {
	dir := t.TempDir()
	v := freshVault(t, dir)
	for _, status := range []string{"open", "waiting", "done", "dropped"} {
		write(t, v, "tasks/"+status+".md", `---
type: task
id: `+status+`
title: a task
status: `+status+`
due: 2026-01-01T09:00:00+07:00
created: 2026-01-01
touched: 2026-01-01
follow_up_after: 3d
---

body
`)
	}
	now, err := v.Zone.Normalise("2026-09-02T12:00")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := New(v, now).Tasks(TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		open := r.Record.Status == "open" || r.Record.Status == "waiting"
		if r.PastHorizon != open {
			t.Errorf("%s: PastHorizon = %v, want %v", r.Record.Status, r.PastHorizon, open)
		}
		if r.Overdue != open {
			t.Errorf("%s: Overdue = %v, want %v", r.Record.Status, r.Overdue, open)
		}
	}
	if rows, err := New(v, now).Tasks(TaskFilter{OnlyOpen: true}); err != nil {
		t.Fatal(err)
	} else if len(rows) != 2 {
		t.Errorf("OnlyOpen kept %d of four statuses, want 2", len(rows))
	}
}

// TestBriefCarriesDueAndUncheckedTasks is the push half for the task kind: a
// delegated commitment nobody has checked arrives without being asked for.
func TestBriefCarriesDueAndUncheckedTasks(t *testing.T) {
	e := engine(t, "2026-09-02T08:00")
	b, err := e.Brief(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.DueTasks) != 1 || b.DueTasks[0].Record.ID != "review-backup-policy" {
		t.Errorf("due tasks = %v, want the one due inside the window", taskIDs(b.DueTasks))
	}
	if len(b.UncheckedTasks) != 1 || b.UncheckedTasks[0].Record.ID != "migrate-staging-db" {
		t.Errorf("unchecked tasks = %v, want the delegated one", taskIDs(b.UncheckedTasks))
	}
	// A task due beyond the window is not on the brief yet.
	short, err := e.Brief(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(short.DueTasks) != 0 {
		t.Errorf("a one-day brief carries %v", taskIDs(short.DueTasks))
	}
	// An overdue task stays on the brief however far past it is: a deadline
	// does not stop mattering because the window moved on.
	late, err := engine(t, "2026-10-30T08:00").Brief(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(late.DueTasks) != 1 {
		t.Errorf("a long-overdue task fell off the brief: %v", taskIDs(late.DueTasks))
	}
}

func taskIDs(rows []TaskRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Record.ID)
	}
	return out
}
