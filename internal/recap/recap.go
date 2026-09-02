// Package recap answers what a period actually produced.
//
// Four rules shape everything here and each one is enforced rather than
// intended. It counts outcomes and never activity, so there is no metric for a
// commit or a line of anything. A value with no data source in the vault
// renders as unknown and never as zero, because a zero is a claim and unknown
// is the truth. A period with little in it renders exactly as neutrally as a
// full one: nothing in this package evaluates, and the words it prints carry no
// judgement. And the only thing a period is ever compared against is the same
// vault's own previous equivalent period; there is no benchmark and no target.
package recap

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/forge"
	"github.com/Thanhbinh1905/secondbrain/internal/payload"
	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/render"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
	"github.com/Thanhbinh1905/secondbrain/templates"
)

// Schema is the versioned data contract.
const Schema = "brain-recap.v1"

// Slot is where a built page keeps its payload.
var Slot = payload.Slot{Marker: templates.DataSlot, ElementID: "recap-data"}

// Kinds are the period shapes recap accepts as a positional argument.
var Kinds = []string{"week", "month", "quarter"}

// BlockSpec is one block's fixed identity.
type BlockSpec struct {
	Key   string
	Title string
	Empty string
}

// Blocks are the six blocks, always in this order.
var Blocks = []BlockSpec{
	{Key: "shipped", Title: "Shipped", Empty: "nothing is recorded as shipped in this period"},
	{Key: "commitments", Title: "Commitments", Empty: "no commitment was made in this period"},
	{Key: "pull_requests", Title: "Pull requests", Empty: "no pull request is recorded as merged in this period"},
	{Key: "meetings", Title: "Meetings", Empty: "no meeting is recorded in this period"},
	{Key: "delegated", Title: "Delegated", Empty: "nothing is recorded as delegated in this period"},
	{Key: "dormant", Title: "Dormant ideas", Empty: "no idea is past the dormancy window"},
}

// Period is one span of calendar dates and how it is named.
type Period struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	From  string `json:"from"`
	To    string `json:"to"`

	from timeref.Date
	to   timeref.Date
}

// Metric is one counted outcome.
//
// Value is a pointer and Known is explicit because the difference between "zero
// happened" and "the vault cannot answer this" is the difference between a
// report and a guess. A renderer that reads Value without reading Known would
// print a nought where the honest answer is "unknown".
type Metric struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value *int   `json:"value"`
	Known bool   `json:"known"`
	Note  string `json:"note"`
	// Delta is the change against the same vault's own previous equivalent
	// period, and nothing else. DeltaKnown is false when there is no
	// comparable number rather than when the change happens to be nought.
	Delta      *int `json:"delta"`
	DeltaKnown bool `json:"delta_known"`
}

// Row is one named outcome. Ideas that shipped are listed by name because a
// count of them is not something anybody can act on or remember.
type Row struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Note   string `json:"note"`
	Path   string `json:"path"`
}

// Block is one section of the recap.
type Block struct {
	Key     string   `json:"key"`
	Title   string   `json:"title"`
	Empty   string   `json:"empty"`
	Metrics []Metric `json:"metrics"`
	Rows    []Row    `json:"rows"`
}

// DriftRow is one record whose cached forge status disagrees with the forge,
// found only when --verify-forge asked.
type DriftRow struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Recorded string `json:"recorded"`
	Live     string `json:"live"`
	Error    string `json:"error"`
	Path     string `json:"path"`
}

// Model is a whole recap.
type Model struct {
	Schema    string `json:"schema"`
	Generated string `json:"generated"`
	Timezone  string `json:"timezone"`
	Period    Period `json:"period"`
	// Previous is the period this one is compared against: the same vault's own
	// equivalent span immediately before. It is in the payload so a reader can
	// see exactly what a delta is a delta against.
	Previous Period  `json:"previous"`
	Blocks   []Block `json:"blocks"`
	// Verified reports whether the forge was asked. Without it Drift is empty
	// because nothing was checked, which is not the same as nothing having
	// drifted.
	Verified bool       `json:"verified"`
	Drift    []DriftRow `json:"drift"`
}

// ResolvePeriod turns a period name into its calendar span, in the vault's own
// timezone and against its own configured first day of the week.
func ResolvePeriod(z timeref.Zone, now time.Time, kind string) (Period, Period, error) {
	today := z.DateOf(now)
	switch kind {
	case "week":
		start := z.StartOfWeekDate(today)
		prev := start.AddDays(-7)
		return weekPeriod(start), weekPeriod(prev), nil
	case "month":
		start := today.StartOfMonth()
		prev := start.MonthStartAfter(-1)
		return monthPeriod(start), monthPeriod(prev), nil
	case "quarter":
		start := today.StartOfQuarter()
		prev := start.QuarterStartAfter(-1)
		return quarterPeriod(start), quarterPeriod(prev), nil
	default:
		return Period{}, Period{}, fmt.Errorf("unknown period %q: recap takes one of %s, or --from and --to", kind, strings.Join(Kinds, ", "))
	}
}

// RangePeriod is an explicit --from/--to span. Its previous equivalent is the
// same number of days immediately before it, which is the only comparison this
// package will ever make.
func RangePeriod(from, to timeref.Date) (Period, Period) {
	if to.Before(from) {
		from, to = to, from
	}
	days := timeref.DateDiff(from, to) + 1
	cur := Period{Kind: "range", Label: from.String() + ".." + to.String(), From: from.String(), To: to.String(), from: from, to: to}
	pFrom, pTo := from.AddDays(-days), from.AddDays(-1)
	prev := Period{Kind: "range", Label: pFrom.String() + ".." + pTo.String(), From: pFrom.String(), To: pTo.String(), from: pFrom, to: pTo}
	return cur, prev
}

func weekPeriod(start timeref.Date) Period {
	end := start.AddDays(6)
	return Period{Kind: "week", Label: "week of " + start.String(), From: start.String(), To: end.String(), from: start, to: end}
}

func monthPeriod(start timeref.Date) Period {
	end := start.MonthStartAfter(1).AddDays(-1)
	return Period{
		Kind: "month", Label: fmt.Sprintf("%04d-%02d", start.Year, int(start.Month)),
		From: start.String(), To: end.String(), from: start, to: end,
	}
}

func quarterPeriod(start timeref.Date) Period {
	end := start.QuarterStartAfter(1).AddDays(-1)
	return Period{
		Kind: "quarter", Label: fmt.Sprintf("%04d Q%d", start.Year, start.Quarter()),
		From: start.String(), To: end.String(), from: start, to: end,
	}
}

// Bounds exposes a period's calendar span.
func (p Period) Bounds() (timeref.Date, timeref.Date) { return p.from, p.to }

// Build assembles the recap. It is the only place a recap's contents are
// decided, and both renderers take its result unchanged.
func Build(e *query.Engine, current, previous Period) (Model, error) {
	m := Model{
		Schema: Schema, Generated: timeref.Format(e.Now), Timezone: e.Vault.Zone.Name(),
		Period: current, Previous: previous, Blocks: make([]Block, 0, len(Blocks)),
		Drift: []DriftRow{},
	}
	cur, err := gather(e, current)
	if err != nil {
		return Model{}, err
	}
	prev, err := gather(e, previous)
	if err != nil {
		return Model{}, err
	}
	byKey := map[string]Block{
		"shipped":       shippedBlock(e, cur, prev),
		"commitments":   commitmentsBlock(cur, prev),
		"pull_requests": pullRequestsBlock(e, cur),
		"meetings":      meetingsBlock(cur, prev),
		"delegated":     delegatedBlock(cur, prev),
		"dormant":       dormantBlock(cur),
	}
	for _, spec := range Blocks {
		b := byKey[spec.Key]
		b.Key, b.Title, b.Empty = spec.Key, spec.Title, spec.Empty
		if b.Metrics == nil {
			b.Metrics = []Metric{}
		}
		if b.Rows == nil {
			b.Rows = []Row{}
		}
		m.Blocks = append(m.Blocks, b)
	}
	return m, nil
}

// counted is everything one period's outcomes are counted from, gathered once
// so the current and previous periods are measured by identical code.
type counted struct {
	period       Period
	asOf         time.Time
	closed       bool
	shipped      []*vault.Record
	shippedIdeas []*vault.Record
	made         []*vault.Record
	kept         []*vault.Record
	meetings     []meeting
	ideasFrom    int
	delDone      []*vault.Record
	delOpen      []query.TaskRow
	dormant      []query.IdeaRow
	// linked is every record carrying a forge link, for the current-state
	// counts that have no period of their own.
	linked []*vault.Record
	merged []*vault.Record
	// checked reports whether any linked record has ever been checked. With
	// none, a state count is unknown rather than nought.
	checked bool
}

type meeting struct {
	Record *vault.Record
	Start  time.Time
	Ideas  []*vault.Record
}

func gather(e *query.Engine, p Period) (counted, error) {
	z := e.Vault.Zone
	c := counted{period: p}
	// Timestamp-backed outcomes remain reconstructable for closed periods.
	// Metrics derived from mutable status or touched dates are marked unknown
	// later, because the vault stores no lifecycle history from which to rebuild
	// their past value. Current periods are measured at now.
	c.asOf = z.StartOf(p.to.AddDays(1))
	c.closed = !c.asOf.After(e.Now)
	if c.asOf.After(e.Now) {
		c.asOf = e.Now
	}
	at := query.New(e.Vault, c.asOf)

	records, err := e.Vault.Walk()
	if err != nil {
		return counted{}, err
	}
	inPeriod := func(d timeref.Date) bool { return !d.Before(p.from) && !d.After(p.to) }

	c.shipped, err = e.Shipped(p.from, p.to)
	if err != nil {
		return counted{}, err
	}
	for _, r := range c.shipped {
		if r.Kind == vault.KindIdea {
			c.shippedIdeas = append(c.shippedIdeas, r)
		}
		if r.ShippedPR != "" {
			c.merged = append(c.merged, r)
		}
	}

	for _, r := range records {
		if r.HasForge {
			c.linked = append(c.linked, r)
			if r.Forge.HasStatus {
				c.checked = true
			}
		}
		if r.Kind != vault.KindTask {
			continue
		}
		if inPeriod(r.Created) {
			c.made = append(c.made, r)
			if r.Status == "done" {
				c.kept = append(c.kept, r)
			}
		}
		if r.Assignee == "" {
			continue
		}
		// A task's touched date moves when its status moves, so a delegated
		// task that reads done and was last touched inside the period is one
		// that closed inside the period. It is the only date the vault has for
		// that, and it is the one `done` writes.
		if r.Status == "done" && r.HasTouched && inPeriod(r.Touched) {
			c.delDone = append(c.delDone, r)
		}
	}

	openTasks, err := at.Tasks(query.TaskFilter{OnlyOpen: true, PastFollowUp: true})
	if err != nil {
		return counted{}, err
	}
	for _, row := range openTasks {
		if row.Record.Assignee != "" {
			c.delOpen = append(c.delOpen, row)
		}
	}

	ideas, err := at.Ideas(query.IdeaFilter{})
	if err != nil {
		return counted{}, err
	}
	for _, row := range ideas {
		if !vault.IsClosed(row.Record.Kind, row.Record.Status) && e.Vault.Dormant(row.Record, c.asOf) {
			c.dormant = append(c.dormant, row)
		}
	}

	ag, err := e.Agenda(p.from, p.to)
	if err != nil {
		return counted{}, err
	}
	byMeeting := map[string][]int{}
	for _, occ := range ag.Occurrences {
		// A meeting counts as held once it has started and was not cancelled.
		// A scheduled future slot is not an outcome.
		if occ.Record.Status == "cancelled" || occ.Start.After(c.asOf) {
			continue
		}
		byMeeting[occ.Record.ID] = append(byMeeting[occ.Record.ID], len(c.meetings))
		c.meetings = append(c.meetings, meeting{Record: occ.Record, Start: occ.Start})
	}
	// An idea generated by a meeting is an idea linked to it, in either
	// direction. That is what the linking layer is for, and it is the only
	// claim the files support.
	//
	// Both directions are resolved by walking each record's own link fields
	// once against the set of meetings, rather than by asking every idea about
	// every meeting: a quarter's recap can hold a few hundred meetings and a
	// few thousand ideas, and the product of the two is work nothing needs.
	ideasByID := map[string]*vault.Record{}
	for _, r := range records {
		if r.Kind == vault.KindIdea {
			ideasByID[r.ID] = r
		}
	}
	attach := func(meeting int, idea *vault.Record) {
		for _, already := range c.meetings[meeting].Ideas {
			if already.ID == idea.ID {
				return
			}
		}
		c.meetings[meeting].Ideas = append(c.meetings[meeting].Ideas, idea)
	}
	for _, r := range records {
		if r.Kind != vault.KindIdea {
			continue
		}
		for _, id := range append(append([]string{}, r.Links...), r.BodyLinks...) {
			if occurrences, ok := byMeeting[id]; ok && len(occurrences) > 0 {
				attach(occurrences[0], r)
			}
		}
	}
	for _, occurrences := range byMeeting {
		if len(occurrences) == 0 {
			continue
		}
		i := occurrences[0]
		m := c.meetings[i].Record
		for _, target := range append(append([]string{}, m.Links...), m.BodyLinks...) {
			if idea, ok := ideasByID[target]; ok {
				attach(i, idea)
			}
		}
	}
	seenIdea := map[string]bool{}
	for i := range c.meetings {
		ideas := c.meetings[i].Ideas
		sort.SliceStable(ideas, func(a, b int) bool { return ideas[a].ID < ideas[b].ID })
		for _, idea := range ideas {
			if !seenIdea[idea.ID] {
				seenIdea[idea.ID] = true
				c.ideasFrom++
			}
		}
	}
	sort.SliceStable(c.meetings, func(i, j int) bool {
		if !c.meetings[i].Start.Equal(c.meetings[j].Start) {
			return c.meetings[i].Start.Before(c.meetings[j].Start)
		}
		return c.meetings[i].Record.ID < c.meetings[j].Record.ID
	})
	return c, nil
}

func known(key, label string, value int, note string) Metric {
	v := value
	return Metric{Key: key, Label: label, Value: &v, Known: true, Note: note}
}

// unknown is a metric the vault cannot answer. It is never rendered as nought,
// because a nought here would be a claim nothing supports.
func unknown(key, label, why string) Metric {
	return Metric{Key: key, Label: label, Value: nil, Known: false, Note: why}
}

// withDelta attaches the change against the same vault's own previous
// equivalent period. Nothing else is ever compared against.
func withDelta(m Metric, previous int) Metric {
	if !m.Known {
		return m
	}
	d := *m.Value - previous
	m.Delta, m.DeltaKnown = &d, true
	return m
}

func shippedBlock(e *query.Engine, cur, prev counted) Block {
	b := Block{Metrics: []Metric{
		withDelta(known("shipped", "ideas shipped", len(cur.shippedIdeas), ""), len(prev.shippedIdeas)),
	}}
	for _, r := range cur.shippedIdeas {
		note := "no pull request recorded"
		if r.ShippedPR != "" {
			note = r.ShippedPR
		}
		b.Rows = append(b.Rows, Row{
			ID: r.ID, Kind: string(r.Kind), Title: r.Title,
			Detail: e.Vault.Zone.DateOf(r.ShippedAt).String(), Note: note, Path: r.Rel,
		})
	}
	return b
}

func commitmentsBlock(cur, prev counted) Block {
	b := Block{Metrics: []Metric{
		mutablePeriodMetric("kept", "commitments kept", len(cur.kept), cur.closed, len(prev.kept), prev.closed),
		withDelta(known("made", "commitments made", len(cur.made), ""), len(prev.made)),
	}}
	for _, r := range cur.made {
		b.Rows = append(b.Rows, Row{
			ID: r.ID, Kind: string(r.Kind), Title: r.Title,
			Detail: r.Created.String(), Note: r.Status, Path: r.Rel,
		})
	}
	return b
}

// pullRequestsBlock counts what the records say. Only merged has a date in the
// vault, so only merged is a period count; the rest are the cached states as
// they stand, and opened has no data source at all.
func pullRequestsBlock(e *query.Engine, cur counted) Block {
	b := Block{}
	b.Metrics = append(b.Metrics, known("merged", "merged in this period", len(cur.merged),
		"counted from shipped_at and shipped_pr, which `brain-axi ship` writes"))
	b.Metrics = append(b.Metrics, unknown("opened", "opened in this period",
		"the vault records no date a pull request was opened, so this is unknown rather than nought"))
	states := map[string]int{}
	for _, r := range cur.linked {
		if r.Forge.HasStatus {
			states[r.Forge.State]++
		}
	}
	cachedNote := "the cached state of every linked record, as it stands now rather than over the period"
	if !cur.checked {
		for _, m := range []struct{ key, label string }{
			{"pending", "open or draft now"}, {"closed", "closed now"},
		} {
			b.Metrics = append(b.Metrics, unknown(m.key, m.label,
				"no linked record has ever been checked, so this is unknown rather than nought"))
		}
	} else {
		b.Metrics = append(b.Metrics,
			known("pending", "open or draft now", states["open"]+states["draft"], cachedNote),
			known("closed", "closed now", states["closed"], cachedNote))
	}
	for _, r := range cur.merged {
		b.Rows = append(b.Rows, Row{
			ID: r.ID, Kind: string(r.Kind), Title: r.Title,
			Detail: e.Vault.Zone.DateOf(r.ShippedAt).String(), Note: r.ShippedPR, Path: r.Rel,
		})
	}
	return b
}

func meetingsBlock(cur, prev counted) Block {
	b := Block{Metrics: []Metric{
		withDelta(known("held", "meetings held", len(cur.meetings), ""), len(prev.meetings)),
		withDelta(known("ideas_generated", "ideas linked to them", cur.ideasFrom,
			"an idea counts here when it links to the meeting; the linking layer is the only record of that"),
			prev.ideasFrom),
	}}
	for _, mt := range cur.meetings {
		note := "no idea links to it"
		if n := len(mt.Ideas); n > 0 {
			names := make([]string, 0, n)
			for _, idea := range mt.Ideas {
				names = append(names, idea.ID)
			}
			note = strconv.Itoa(n) + " idea(s): " + strings.Join(names, ", ")
		}
		b.Rows = append(b.Rows, Row{
			ID: mt.Record.ID, Kind: string(mt.Record.Kind), Title: mt.Record.Title,
			Detail: timeref.FormatDate(mt.Start), Note: note, Path: mt.Record.Rel,
		})
	}
	return b
}

func delegatedBlock(cur, prev counted) Block {
	b := Block{Metrics: []Metric{
		mutablePeriodMetric("done", "delegated items done", len(cur.delDone), cur.closed, len(prev.delDone), prev.closed),
		mutablePeriodMetric("unchecked", "delegated items still unchecked", len(cur.delOpen), cur.closed, len(prev.delOpen), prev.closed),
	}}
	if cur.closed {
		return b
	}
	for _, row := range cur.delOpen {
		b.Rows = append(b.Rows, Row{
			ID: row.Record.ID, Kind: string(row.Record.Kind), Title: row.Record.Title,
			Detail: strconv.Itoa(row.AgeDays) + "d", Path: row.Record.Rel,
			Note: row.Record.Assignee + ", unchecked past its " + strconv.Itoa(row.HorizonDays) + "d horizon",
		})
	}
	for _, r := range cur.delDone {
		b.Rows = append(b.Rows, Row{
			ID: r.ID, Kind: string(r.Kind), Title: r.Title,
			Detail: r.Touched.String(), Note: r.Assignee + ", done", Path: r.Rel,
		})
	}
	return b
}

func dormantBlock(cur counted) Block {
	b := Block{Metrics: []Metric{
		mutablePeriodMetric("dormant", "ideas past the dormancy window", len(cur.dormant), cur.closed, 0, true),
	}}
	if cur.closed {
		return b
	}
	for _, row := range cur.dormant {
		b.Rows = append(b.Rows, Row{
			ID: row.Record.ID, Kind: string(row.Record.Kind), Title: row.Record.Title,
			Detail: strconv.Itoa(row.AgeDays) + "d", Note: row.Record.Status, Path: row.Record.Rel,
		})
	}
	return b
}

const mutableHistoryNote = "cannot be reconstructed for a closed period because it is derived from mutable status or touched dates"

func mutablePeriodMetric(key, label string, value int, closed bool, previous int, previousClosed bool) Metric {
	if closed {
		return unknown(key, label, mutableHistoryNote)
	}
	m := known(key, label, value, "measured from current vault state")
	if previousClosed {
		return m
	}
	return withDelta(m, previous)
}

// VerifyForge re-reads every linked record's status and records where the
// forge and the record disagree.
//
// This is the one part of recap that reaches anything, it runs only when the
// caller passed --verify-forge, and it goes through the same gh and glab
// delegation every other forge read in this tool goes through.
func VerifyForge(e *query.Engine, r forge.Runner) ([]DriftRow, error) {
	records, err := e.Vault.Walk()
	if err != nil {
		return nil, err
	}
	out := []DriftRow{}
	for _, rec := range records {
		if !rec.HasForge {
			continue
		}
		ref, err := forge.Detect(rec.Forge.URL)
		if err != nil {
			out = append(out, DriftRow{ID: rec.ID, URL: rec.Forge.URL, Path: rec.Rel, Error: err.Error()})
			continue
		}
		status, err := forge.Fetch(r, ref, e.Now)
		if err != nil {
			out = append(out, DriftRow{ID: rec.ID, URL: rec.Forge.URL, Path: rec.Rel, Error: err.Error()})
			continue
		}
		live := status.State + "/" + status.Checks
		recorded := "never checked"
		if rec.Forge.HasStatus {
			recorded = rec.Forge.State + "/" + rec.Forge.Checks
		}
		if recorded == live {
			continue
		}
		out = append(out, DriftRow{ID: rec.ID, URL: rec.Forge.URL, Path: rec.Rel, Recorded: recorded, Live: live})
	}
	return out, nil
}

// Validate checks a payload against brain-recap.v1.
func Validate(path string, raw []byte, lineOffset int) (Model, error) {
	if err := requireRecapFields(path, raw, lineOffset); err != nil {
		return Model{}, err
	}
	var m Model
	if err := payload.Decode(path, raw, lineOffset, &m); err != nil {
		return Model{}, err
	}
	if m.Schema != Schema {
		found := m.Schema
		if found == "" {
			found = "(none)"
		}
		return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "schema", lineOffset),
			"unsupported recap schema %s: this build writes and reads %s", found, Schema)
	}
	if _, err := timeref.ParseStored(m.Generated); err != nil {
		return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "generated", lineOffset), "generated: %v", err)
	}
	for _, p := range []struct {
		what string
		p    Period
	}{{"period", m.Period}, {"previous", m.Previous}} {
		if strings.TrimSpace(p.p.From) == "" || strings.TrimSpace(p.p.To) == "" || strings.TrimSpace(p.p.Label) == "" {
			return Model{}, payload.Errorf(path, payload.LineOfKey(raw, p.what, lineOffset),
				"%s needs a label, a from and a to", p.what)
		}
	}
	if len(m.Blocks) != len(Blocks) {
		return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "blocks", lineOffset),
			"a recap has exactly %d blocks, found %d: %s", len(Blocks), len(m.Blocks), strings.Join(blockKeys(), ", "))
	}
	for i, spec := range Blocks {
		b := m.Blocks[i]
		if b.Key != spec.Key {
			return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "key", lineOffset),
				"block %d is %q, and the block set is fixed in this order: %s", i+1, b.Key, strings.Join(blockKeys(), ", "))
		}
		if strings.TrimSpace(b.Empty) == "" {
			return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "empty", lineOffset),
				"block %q has no empty-state string", b.Key)
		}
		for _, metric := range b.Metrics {
			// A metric that claims to be known and carries no number would
			// render as a nought, which is the one shape this contract exists
			// to prevent.
			if metric.Known && metric.Value == nil {
				return Model{}, payload.Errorf(path, payload.LineOfKey(raw, metric.Key, lineOffset),
					"metric %q is marked known but carries no value", metric.Key)
			}
			if !metric.Known && metric.Value != nil {
				return Model{}, payload.Errorf(path, payload.LineOfKey(raw, metric.Key, lineOffset),
					"metric %q is marked unknown but carries the value %d", metric.Key, *metric.Value)
			}
			if metric.DeltaKnown && metric.Delta == nil {
				return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "delta", lineOffset),
					"metric %q is marked delta-known but carries no delta", metric.Key)
			}
			if !metric.DeltaKnown && metric.Delta != nil {
				return Model{}, payload.Errorf(path, payload.LineOfKey(raw, "delta", lineOffset),
					"metric %q is marked delta-unknown but carries the delta %d", metric.Key, *metric.Delta)
			}
		}
	}
	return m, nil
}

func requireRecapFields(path string, raw []byte, lineOffset int) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	if err := requireFields(path, raw, lineOffset, root, "schema", "generated", "timezone", "period", "previous", "blocks", "verified", "drift"); err != nil {
		return err
	}
	for _, key := range []string{"period", "previous"} {
		var period map[string]json.RawMessage
		if json.Unmarshal(root[key], &period) == nil {
			if err := requireFields(path, raw, lineOffset, period, "kind", "label", "from", "to"); err != nil {
				return err
			}
		}
	}
	var blocks []json.RawMessage
	if json.Unmarshal(root["blocks"], &blocks) == nil {
		for _, blockRaw := range blocks {
			var block map[string]json.RawMessage
			if json.Unmarshal(blockRaw, &block) != nil {
				continue
			}
			if err := requireFields(path, raw, lineOffset, block, "key", "title", "empty", "metrics", "rows"); err != nil {
				return err
			}
			var metrics []json.RawMessage
			if json.Unmarshal(block["metrics"], &metrics) == nil {
				for _, metricRaw := range metrics {
					var metric map[string]json.RawMessage
					if json.Unmarshal(metricRaw, &metric) == nil {
						if err := requireFields(path, raw, lineOffset, metric, "key", "label", "value", "known", "note", "delta", "delta_known"); err != nil {
							return err
						}
					}
				}
			}
			var rows []json.RawMessage
			if json.Unmarshal(block["rows"], &rows) == nil {
				for _, rowRaw := range rows {
					var row map[string]json.RawMessage
					if json.Unmarshal(rowRaw, &row) == nil {
						if err := requireFields(path, raw, lineOffset, row, "id", "kind", "title", "detail", "note", "path"); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	var drift []json.RawMessage
	if json.Unmarshal(root["drift"], &drift) == nil {
		for _, driftRaw := range drift {
			var row map[string]json.RawMessage
			if json.Unmarshal(driftRaw, &row) == nil {
				if err := requireFields(path, raw, lineOffset, row, "id", "url", "recorded", "live", "error", "path"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func requireFields(path string, raw []byte, lineOffset int, object map[string]json.RawMessage, fields ...string) error {
	for _, field := range fields {
		value, ok := object[field]
		if !ok {
			return payload.Errorf(path, payload.LineOfKey(raw, field, lineOffset), "%s is required", field)
		}
		if string(value) == "null" && field != "value" && field != "delta" {
			return payload.Errorf(path, payload.LineOfKey(raw, field, lineOffset), "%s must not be null", field)
		}
	}
	return nil
}

func blockKeys() []string {
	out := make([]string, 0, len(Blocks))
	for _, b := range Blocks {
		out = append(out, b.Key)
	}
	return out
}

// RenderHTML builds the self-contained page, on the same contract as the board.
func RenderHTML(m Model) ([]byte, error) {
	raw, err := payload.Marshal(m)
	if err != nil {
		return nil, err
	}
	if _, err := Validate("<recap payload>", raw, 0); err != nil {
		return nil, err
	}
	page, err := payload.Inject(templates.Recap, Slot, payload.Escape(raw))
	if err != nil {
		return nil, err
	}
	back, line, err := payload.Extract(page, Slot)
	if err != nil {
		return nil, fmt.Errorf("the built recap cannot be read back: %w", err)
	}
	if _, err := Validate("<built recap>", back, line); err != nil {
		return nil, fmt.Errorf("the built recap does not carry a readable payload: %w", err)
	}
	return []byte(page), nil
}

// RenderASCII writes the framed recap, or plain lines when stdout is a pipe,
// from the same Model the HTML renderer reads.
func RenderASCII(o *render.Out, m Model) {
	f := Frame(o, m)
	if o.TTY {
		fmt.Fprint(o.W, f.String())
		return
	}
	fmt.Fprint(o.W, f.Plain())
}

// Frame lays the model out. It is exported so a test can assert both renderers
// are fed the same Model value.
func Frame(o *render.Out, m Model) *render.Frame {
	f := render.NewFrame(o.FrameWidth(render.DefaultFrameWidth), "brain-axi recap", m.Period.Label)
	f.Line(" " + m.Period.From + " to " + m.Period.To + ", " + m.Timezone)
	if m.Verified {
		f.Line(" forge verified: " + strconv.Itoa(len(m.Drift)) + " record(s) differ from their cache")
	}
	for _, b := range m.Blocks {
		f.Rule()
		f.Line(" " + strings.ToUpper(b.Title))
		for _, metric := range b.Metrics {
			f.Row("   "+metric.Label, MetricText(metric, m.Previous.Label)+" ")
		}
		if len(b.Rows) == 0 {
			f.Line("   " + b.Empty)
			continue
		}
		width := 0
		for _, row := range b.Rows {
			if w := unitext.Width(row.Detail); w > width {
				width = w
			}
		}
		for _, row := range b.Rows {
			f.Row("   "+unitext.PadLeft(row.Detail, width)+"  "+row.Title, row.ID+" ")
		}
	}
	f.Caption = "every number counted from this vault"
	return f
}

// MetricText renders one metric, including the case that matters most: a value
// the vault cannot supply reads "unknown" and never "0".
func MetricText(m Metric, previousLabel string) string {
	if !m.Known {
		return "unknown"
	}
	out := strconv.Itoa(*m.Value)
	if m.DeltaKnown {
		sign := ""
		if *m.Delta > 0 {
			sign = "+"
		}
		out += fmt.Sprintf(" (%s%d vs %s)", sign, *m.Delta, previousLabel)
	}
	return out
}
