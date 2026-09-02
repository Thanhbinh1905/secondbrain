package recap

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/render"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

func fixture(t *testing.T, stamp string) *query.Engine {
	t.Helper()
	v, err := vault.OpenAt(filepath.Join("..", "vault", "testdata", "good"))
	if err != nil {
		t.Fatal(err)
	}
	now, err := v.Zone.Normalise(stamp)
	if err != nil {
		t.Fatal(err)
	}
	return query.New(v, now)
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

func freshVault(t *testing.T, cfg vault.Config) *vault.Vault {
	t.Helper()
	root := filepath.Join(t.TempDir(), "vault")
	if _, err := vault.Init(root, cfg, false); err != nil {
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

func engineAt(t *testing.T, v *vault.Vault, stamp string) *query.Engine {
	t.Helper()
	now, err := v.Zone.Normalise(stamp)
	if err != nil {
		t.Fatal(err)
	}
	return query.New(v, now)
}

func build(t *testing.T, e *query.Engine, kind string) Model {
	t.Helper()
	cur, prev, err := ResolvePeriod(e.Vault.Zone, e.Now, kind)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Build(e, cur, prev)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func plain(t *testing.T, m Model) string {
	t.Helper()
	var buf bytes.Buffer
	RenderASCII(&render.Out{W: &buf}, m)
	return buf.String()
}

func metric(t *testing.T, m Model, block, key string) Metric {
	t.Helper()
	for _, b := range m.Blocks {
		if b.Key != block {
			continue
		}
		for _, mt := range b.Metrics {
			if mt.Key == key {
				return mt
			}
		}
	}
	t.Fatalf("no metric %q in block %q", key, block)
	return Metric{}
}

// TestEveryMetricCountsAnOutcome is rule one: a recap counts what a period
// produced, never how busy it looked. There is no metric for a commit, a line
// or an hour, and the whole metric set is fixed here so one cannot be added
// without this test saying so.
func TestEveryMetricCountsAnOutcome(t *testing.T) {
	m := build(t, fixture(t, "2026-09-02T12:00"), "month")
	want := []string{
		"shipped/shipped",
		"commitments/kept", "commitments/made",
		"pull_requests/merged", "pull_requests/opened", "pull_requests/pending", "pull_requests/closed",
		"meetings/held", "meetings/ideas_generated",
		"delegated/done", "delegated/unchecked",
		"dormant/dormant",
	}
	var got []string
	for _, b := range m.Blocks {
		for _, mt := range b.Metrics {
			got = append(got, b.Key+"/"+mt.Key)
		}
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("metric set drifted\n got %v\nwant %v", got, want)
	}
	// Activity is not an outcome, and no wording here may suggest it is. The
	// terms are matched on word boundaries so "commitment" is not mistaken for
	// "commits": a commitment kept is an outcome, a commit is not.
	text := strings.ToLower(plain(t, m))
	for _, banned := range []string{
		`\bcommits\b`, `\blines? of code\b`, `\blines changed\b`, `\bfiles changed\b`,
		`\bkeystrokes?\b`, `\bhours worked\b`, `\bvelocity\b`, `\bthroughput\b`,
	} {
		if regexp.MustCompile(banned).MatchString(text) {
			t.Errorf("the recap counts activity: it matches %s\n%s", banned, text)
		}
	}
}

// TestUnknownIsNeverRenderedAsZero is rule two. The vault records no date a
// pull request was opened, so that number is unknown; a period with no merges
// is a genuine nought. The two must be told apart at a glance.
func TestUnknownIsNeverRenderedAsZero(t *testing.T) {
	m := build(t, fixture(t, "2026-09-02T12:00"), "month")

	opened := metric(t, m, "pull_requests", "opened")
	if opened.Known || opened.Value != nil {
		t.Errorf("a metric with no data source claims to be known: %+v", opened)
	}
	if opened.Note == "" {
		t.Error("an unknown metric does not say why it is unknown")
	}
	merged := metric(t, m, "pull_requests", "merged")
	if !merged.Known || merged.Value == nil || *merged.Value != 0 {
		t.Fatalf("expected a genuine zero for merged, got %+v", merged)
	}

	if got := MetricText(opened, m.Previous.Label); got != "unknown" {
		t.Errorf("an unknown metric renders as %q", got)
	}
	if got := MetricText(merged, m.Previous.Label); got != "0" {
		t.Errorf("a genuine zero renders as %q", got)
	}
	text := plain(t, m)
	if !strings.Contains(text, "opened in this period") || !strings.Contains(text, "unknown") {
		t.Errorf("the frame does not distinguish unknown from zero:\n%s", text)
	}

	// The contract refuses the two shapes that would collapse the distinction.
	bad := `{"schema":"brain-recap.v1","generated":"2026-09-02T12:00:00+07:00","timezone":"Asia/Bangkok",` +
		`"period":{"kind":"month","label":"2026-09","from":"2026-09-01","to":"2026-09-30"},` +
		`"previous":{"kind":"month","label":"2026-08","from":"2026-08-01","to":"2026-08-31"},` +
		`"verified":false,"drift":[],"blocks":[%s]}`
	blocks := make([]string, 0, len(Blocks))
	for i, spec := range Blocks {
		metrics := "[]"
		if i == 0 {
			metrics = `[{"key":"shipped","label":"shipped","value":null,"known":true,"note":"","delta":null,"delta_known":false}]`
		}
		blocks = append(blocks, fmt.Sprintf(`{"key":%q,"title":%q,"empty":%q,"metrics":%s,"rows":[]}`,
			spec.Key, spec.Title, spec.Empty, metrics))
	}
	_, err := Validate("recap.html", []byte(fmt.Sprintf(bad, strings.Join(blocks, ","))), 1)
	if err == nil || !strings.Contains(err.Error(), "marked known but carries no value") {
		t.Errorf("a known metric with no value was accepted: %v", err)
	}
	badDelta := strings.Replace(fmt.Sprintf(bad, strings.Join(blocks, ",")),
		`"value":null,"known":true`, `"value":0,"known":true`, 1)
	badDelta = strings.Replace(badDelta, `"delta":null,"delta_known":false`, `"delta":null,"delta_known":true`, 1)
	if _, err := Validate("recap.html", []byte(badDelta), 1); err == nil || !strings.Contains(err.Error(), "delta-known") {
		t.Errorf("a known delta with no value was accepted: %v", err)
	}
	badDeltaKnown := strings.Replace(fmt.Sprintf(bad, strings.Join(blocks, ",")),
		`"value":null,"known":true,"note":"","delta":null,"delta_known":false`,
		`"value":0,"known":true,"note":"","delta":3,"delta_known":false`, 1)
	if _, err := Validate("recap.html", []byte(badDeltaKnown), 1); err == nil || !strings.Contains(err.Error(), "delta-unknown") {
		t.Errorf("a delta-unknown metric carrying a delta was accepted: %v", err)
	}
	missingMetrics := strings.Replace(fmt.Sprintf(bad, strings.Join(blocks, ",")), `"metrics":[]`, `"omitted_metrics":[]`, 1)
	if _, err := Validate("recap.html", []byte(missingMetrics), 1); err == nil || !strings.Contains(err.Error(), "metrics is required") {
		t.Errorf("a block with no metrics field was accepted: %v", err)
	}
}

func TestShippedOutcomeCountsIdeasOnly(t *testing.T) {
	v := freshVault(t, testConfig())
	for _, tc := range []struct{ kind, id, status string }{{"idea", "idea-one", "shipped"}, {"task", "task-one", "done"}, {"note", "note-one", ""}} {
		status := ""
		if tc.status != "" {
			status = "status: " + tc.status + "\n"
		}
		touched := ""
		if tc.kind != "note" {
			touched = "touched: 2026-09-02\n"
		}
		write(t, v, tc.kind+"s/"+tc.id+".md", fmt.Sprintf("---\ntype: %s\nid: %s\ntitle: %s\n%screated: 2026-09-01\n%sshipped_at: 2026-09-02T10:00:00+07:00\nshipped_pr: https://github.com/owner/repo/pull/1\n---\n\nbody\n", tc.kind, tc.id, tc.id, status, touched))
	}
	m := build(t, engineAt(t, v, "2026-09-15T12:00"), "month")
	shipped := metric(t, m, "shipped", "shipped")
	if !shipped.Known || shipped.Value == nil || *shipped.Value != 1 {
		t.Fatalf("ideas shipped = %+v, want 1", shipped)
	}
	for _, block := range m.Blocks {
		if block.Key == "shipped" && (len(block.Rows) != 1 || block.Rows[0].ID != "idea-one") {
			t.Errorf("shipped rows = %+v, want only idea-one", block.Rows)
		}
	}
	merged := metric(t, m, "pull_requests", "merged")
	if merged.Value == nil || *merged.Value != 3 {
		t.Errorf("merged PR count = %+v, want all three shipped kinds", merged)
	}
}

func TestClosedPeriodsDoNotRewriteMutableHistory(t *testing.T) {
	v := freshVault(t, testConfig())
	write(t, v, "ideas/shipped.md", "---\ntype: idea\nid: shipped\ntitle: shipped\nstatus: shipped\ncreated: 2026-08-01\ntouched: 2026-09-01\nshipped_at: 2026-08-05T10:00:00+07:00\nshipped_pr: https://github.com/owner/repo/pull/1\n---\n\nbody\n")
	write(t, v, "tasks/delegated.md", "---\ntype: task\nid: delegated\ntitle: delegated\nstatus: done\nassignee: platform-team\ncreated: 2026-08-02\ntouched: 2026-09-01\n---\n\nbody\n")
	write(t, v, "people/platform-team.md", "---\ntype: person\nid: platform-team\ntitle: Platform team\ncreated: 2026-08-01\n---\n\nbody\n")
	past := build(t, engineAt(t, v, "2026-09-15T12:00"), "month").Previous
	prev, _ := timeref.ParseDateOnly("2026-07-01")
	pastModel, err := Build(engineAt(t, v, "2026-09-15T12:00"), past, monthPeriod(prev))
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"commitments", "kept"}, {"delegated", "done"}, {"delegated", "unchecked"}, {"dormant", "dormant"}} {
		m := metric(t, pastModel, pair[0], pair[1])
		if m.Known || m.Value != nil || !strings.Contains(m.Note, "cannot be reconstructed") {
			t.Errorf("closed-period %s/%s = %+v, want explained unknown", pair[0], pair[1], m)
		}
	}
	for _, pair := range [][2]string{{"shipped", "shipped"}, {"pull_requests", "merged"}, {"commitments", "made"}} {
		if m := metric(t, pastModel, pair[0], pair[1]); !m.Known || m.Value == nil {
			t.Errorf("timestamp-backed %s/%s became unknown: %+v", pair[0], pair[1], m)
		}
	}
	current := build(t, engineAt(t, v, "2026-09-15T12:00"), "month")
	for _, pair := range [][2]string{{"commitments", "kept"}, {"delegated", "done"}, {"delegated", "unchecked"}, {"dormant", "dormant"}} {
		if m := metric(t, current, pair[0], pair[1]); !m.Known || m.Value == nil {
			t.Errorf("current-period %s/%s is not real: %+v", pair[0], pair[1], m)
		}
	}
}

func TestRecurringMeetingsCountEveryOccurrence(t *testing.T) {
	v := freshVault(t, testConfig())
	write(t, v, "events/daily-sync.md", "---\ntype: event\nid: daily-sync\ntitle: daily sync\nwhen: 2026-09-01T09:00:00+07:00\nduration: 30m\nrrule: FREQ=DAILY;COUNT=3\nstatus: scheduled\ncreated: 2026-09-01\n---\n\nbody\n")
	m := build(t, engineAt(t, v, "2026-09-15T12:00"), "month")
	held := metric(t, m, "meetings", "held")
	if held.Value == nil || *held.Value != 3 {
		t.Errorf("recurring meetings held = %+v, want 3", held)
	}
}

// TestASlowPeriodRendersNeutrally is rule three. A period with nothing in it is
// reported as a period with nothing in it: no judgement, no encouragement, and
// nothing that reads as a target missed.
func TestASlowPeriodRendersNeutrally(t *testing.T) {
	// A month the fixture vault has no records in at all.
	m := build(t, fixture(t, "2020-04-15T12:00"), "month")
	text := plain(t, m)
	for _, b := range m.Blocks {
		if len(b.Rows) != 0 {
			t.Fatalf("block %q is not empty in an empty period: %+v", b.Key, b.Rows)
		}
	}
	loaded := []string{
		"only", "just ", "unfortunately", "sadly", "regret", "behind", "poor", "weak",
		"disappointing", "slow", "sluggish", "underperform", "worse", "better", "improve",
		"should have", "failed", "failure", "no progress", "quiet month", "target", "goal",
		"quota", "expected", "well done", "great", "good job", "nothing to show", "bad",
	}
	lower := strings.ToLower(text)
	for _, word := range loaded {
		if strings.Contains(lower, word) {
			t.Errorf("an empty period was commented on: it contains %q\n%s", word, text)
		}
	}
	// It still says what it counted rather than going silent, and a nought is
	// still shown as a nought.
	for _, want := range []string{"SHIPPED", "COMMITMENTS", "DORMANT IDEAS", "nothing is recorded as shipped in this period"} {
		if !strings.Contains(text, want) {
			t.Errorf("an empty period dropped %q:\n%s", want, text)
		}
	}
}

// TestComparisonIsOnlyAgainstThisVaultsOwnPreviousPeriod is rule four. The
// delta is computed from the same vault's own equivalent earlier span and from
// nothing else, and the payload names that span so a reader can check it.
func TestComparisonIsOnlyAgainstThisVaultsOwnPreviousPeriod(t *testing.T) {
	v := freshVault(t, testConfig())
	// Two shipped last month, one this month.
	for i, stamp := range []string{"2026-08-04T10:00:00+07:00", "2026-08-19T10:00:00+07:00", "2026-09-01T10:00:00+07:00"} {
		write(t, v, fmt.Sprintf("ideas/shipped-%d.md", i), fmt.Sprintf(`---
type: idea
id: shipped-%d
title: idea %d
status: shipped
created: 2026-07-01
touched: 2026-08-01
shipped_at: %s
---

body
`, i, i, stamp))
	}
	m := build(t, engineAt(t, v, "2026-09-15T12:00"), "month")
	if m.Period.Label != "2026-09" || m.Previous.Label != "2026-08" {
		t.Fatalf("period %q compared against %q", m.Period.Label, m.Previous.Label)
	}
	if m.Previous.From != "2026-08-01" || m.Previous.To != "2026-08-31" {
		t.Errorf("the previous period is %s..%s", m.Previous.From, m.Previous.To)
	}
	shipped := metric(t, m, "shipped", "shipped")
	if !shipped.Known || *shipped.Value != 1 {
		t.Fatalf("shipped = %+v", shipped)
	}
	if !shipped.DeltaKnown || *shipped.Delta != -1 {
		t.Errorf("delta = %+v, want -1 against the vault's own previous month", shipped.Delta)
	}
	// Shipped ideas are listed by name, not just counted.
	names := []string{}
	for _, b := range m.Blocks {
		if b.Key == "shipped" {
			for _, row := range b.Rows {
				names = append(names, row.ID)
			}
		}
	}
	if strings.Join(names, ",") != "shipped-2" {
		t.Errorf("shipped ideas are not listed by name: %v", names)
	}
}

// TestPeriodBoundariesCrossMonthAndQuarterEdges: month and quarter arithmetic
// runs on the calendar rather than on instants, so the 31st of a month and the
// turn of a year both resolve to the span a calendar shows.
func TestPeriodBoundariesCrossMonthAndQuarterEdges(t *testing.T) {
	v := freshVault(t, testConfig())
	cases := []struct {
		now                      string
		kind                     string
		from, to, prevFrom, prev string
	}{
		{"2026-01-15T12:00", "month", "2026-01-01", "2026-01-31", "2025-12-01", "2025-12-31"},
		// The 31st must not roll a month forward the way AddDate does.
		{"2026-03-31T12:00", "month", "2026-03-01", "2026-03-31", "2026-02-01", "2026-02-28"},
		{"2026-12-31T23:00", "month", "2026-12-01", "2026-12-31", "2026-11-01", "2026-11-30"},
		{"2026-01-15T12:00", "quarter", "2026-01-01", "2026-03-31", "2025-10-01", "2025-12-31"},
		{"2026-09-30T12:00", "quarter", "2026-07-01", "2026-09-30", "2026-04-01", "2026-06-30"},
		{"2026-10-01T00:30", "quarter", "2026-10-01", "2026-12-31", "2026-07-01", "2026-09-30"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+" at "+tc.now, func(t *testing.T) {
			now, err := v.Zone.Normalise(tc.now)
			if err != nil {
				t.Fatal(err)
			}
			cur, prev, err := ResolvePeriod(v.Zone, now, tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if cur.From != tc.from || cur.To != tc.to {
				t.Errorf("period = %s..%s, want %s..%s", cur.From, cur.To, tc.from, tc.to)
			}
			if prev.From != tc.prevFrom || prev.To != tc.prev {
				t.Errorf("previous = %s..%s, want %s..%s", prev.From, prev.To, tc.prevFrom, tc.prev)
			}
		})
	}
}

// TestTheWeekStartIsConfigured: a week is whatever the vault says a week is,
// and the previous week is the seven days before it.
func TestTheWeekStartIsConfigured(t *testing.T) {
	for _, tc := range []struct{ starts, from, to, prevFrom string }{
		{"mon", "2026-08-31", "2026-09-06", "2026-08-24"},
		{"sun", "2026-08-30", "2026-09-05", "2026-08-23"},
	} {
		t.Run(tc.starts, func(t *testing.T) {
			cfg := testConfig()
			cfg.WeekStarts = tc.starts
			v := freshVault(t, cfg)
			now, err := v.Zone.Normalise("2026-09-02T12:00")
			if err != nil {
				t.Fatal(err)
			}
			cur, prev, err := ResolvePeriod(v.Zone, now, "week")
			if err != nil {
				t.Fatal(err)
			}
			if cur.From != tc.from || cur.To != tc.to {
				t.Errorf("week = %s..%s, want %s..%s", cur.From, cur.To, tc.from, tc.to)
			}
			if prev.From != tc.prevFrom {
				t.Errorf("previous week starts %s, want %s", prev.From, tc.prevFrom)
			}
		})
	}
}

// TestARangeIsComparedAgainstTheSpanBeforeIt: an explicit --from/--to period
// gets the same treatment, against the same number of days immediately before.
func TestARangeIsComparedAgainstTheSpanBeforeIt(t *testing.T) {
	from, _ := timeref.ParseDateOnly("2026-09-01")
	to, _ := timeref.ParseDateOnly("2026-09-10")
	cur, prev := RangePeriod(from, to)
	if cur.From != "2026-09-01" || cur.To != "2026-09-10" {
		t.Errorf("range = %s..%s", cur.From, cur.To)
	}
	if prev.From != "2026-08-22" || prev.To != "2026-08-31" {
		t.Errorf("previous = %s..%s, want 2026-08-22..2026-08-31", prev.From, prev.To)
	}
}

// TestAMeetingCountsTheIdeasLinkedToIt: "what did that meeting produce" is
// answered by the linking layer in either direction, and an idea linked to two
// meetings is still one idea.
func TestAMeetingCountsTheIdeasLinkedToIt(t *testing.T) {
	v := freshVault(t, testConfig())
	for _, id := range []string{"sync-a", "sync-b"} {
		write(t, v, "events/"+id+".md", `---
type: event
id: `+id+`
title: `+id+`
when: 2026-09-02T14:00:00+07:00
duration: 60m
status: scheduled
created: 2026-09-01
---

body
`)
	}
	// One idea points at both meetings; one meeting points at an idea instead.
	write(t, v, "ideas/from-both.md", `---
type: idea
id: from-both
title: an idea both meetings produced
status: pending
created: 2026-09-02
touched: 2026-09-02
links: [sync-a, sync-b]
---

body
`)
	write(t, v, "ideas/pointed-at.md", `---
type: idea
id: pointed-at
title: an idea a meeting points at
status: pending
created: 2026-09-02
touched: 2026-09-02
---

body
`)
	write(t, v, "events/sync-c.md", `---
type: event
id: sync-c
title: sync-c
when: 2026-09-03T14:00:00+07:00
duration: 60m
status: scheduled
created: 2026-09-01
links: [pointed-at]
---

body
`)
	m := build(t, engineAt(t, v, "2026-09-15T12:00"), "month")
	held := metric(t, m, "meetings", "held")
	if !held.Known || *held.Value != 3 {
		t.Errorf("meetings held = %+v, want 3", held)
	}
	generated := metric(t, m, "meetings", "ideas_generated")
	if !generated.Known || *generated.Value != 2 {
		t.Errorf("ideas linked to meetings = %+v, want 2 distinct", generated)
	}
	notes := map[string]string{}
	for _, b := range m.Blocks {
		if b.Key != "meetings" {
			continue
		}
		for _, row := range b.Rows {
			notes[row.ID] = row.Note
		}
	}
	for id, want := range map[string]string{
		"sync-a": "1 idea(s): from-both",
		"sync-b": "1 idea(s): from-both",
		"sync-c": "1 idea(s): pointed-at",
	} {
		if notes[id] != want {
			t.Errorf("%s note = %q, want %q", id, notes[id], want)
		}
	}
}

// scriptedForge answers as gh and glab would, without either installed.
type scriptedForge struct {
	out   map[string]string
	fail  map[string]error
	calls [][]string
}

func (f *scriptedForge) Look(name string) (string, error) { return "/usr/bin/" + name, nil }

func (f *scriptedForge) Run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if err, ok := f.fail[name]; ok {
		return nil, err
	}
	return []byte(f.out[name]), nil
}

// TestVerifyForgeIsOptInAndReportsDrift: building a recap reaches nothing, and
// only an explicit check compares the record against the forge.
func TestVerifyForgeIsOptInAndReportsDrift(t *testing.T) {
	e := fixture(t, "2026-09-02T12:00")
	f := &scriptedForge{}
	// Building the whole recap must not touch a forge.
	m := build(t, e, "month")
	if len(f.calls) != 0 {
		t.Fatalf("building a recap reached a forge: %v", f.calls)
	}
	if m.Verified || len(m.Drift) != 0 {
		t.Errorf("an unverified recap claims to have checked: %+v", m.Drift)
	}

	// The fixture's linked merge request records open/pending; the forge says
	// merged/passing, which is drift.
	f.out = map[string]string{"glab": `{"title":"migrate","state":"merged","draft":false,"head_pipeline":{"status":"success"}}`}
	drift, err := VerifyForge(e, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) == 0 {
		t.Fatal("--verify-forge did not reach the forge")
	}
	if len(drift) != 1 {
		t.Fatalf("drift = %+v", drift)
	}
	if drift[0].ID != "migrate-staging-db" || drift[0].Recorded != "open/pending" || drift[0].Live != "merged/passing" {
		t.Errorf("drift does not name both sides: %+v", drift[0])
	}

	// A forge that cannot be read is reported as itself, never as agreement.
	f2 := &scriptedForge{fail: map[string]error{"glab": errors.New("i/o timeout")}}
	drift, err = VerifyForge(e, f2)
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) != 1 || !strings.Contains(drift[0].Error, "i/o timeout") {
		t.Errorf("an unreadable forge was not reported: %+v", drift)
	}
}

// TestTheRecapPageCarriesTheModelVerbatim: the framed recap and the HTML page
// are built from one value, exactly as the board's two renderers are.
func TestTheRecapPageCarriesTheModelVerbatim(t *testing.T) {
	m := build(t, fixture(t, "2026-09-02T12:00"), "quarter")
	page, err := RenderHTML(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), Schema) {
		t.Error("the page does not carry its schema")
	}
	for _, spec := range Blocks {
		if !strings.Contains(string(page), spec.Empty) {
			t.Errorf("the committed template lost block %q's empty-state string", spec.Key)
		}
	}
	text := plain(t, m)
	for _, spec := range Blocks {
		if !strings.Contains(text, strings.ToUpper(spec.Title)) {
			t.Errorf("the frame dropped block %q", spec.Key)
		}
	}
}
