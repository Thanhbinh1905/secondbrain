package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/render"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// addKinds are the things add can capture.
var addKinds = []string{"event", "idea", "task", "note", "person"}

func (a *app) cmdAdd() error {
	if a.has("batch") {
		if len(a.args) > 0 {
			return usageError("add --batch takes no kind or text; the batch file carries both, got %s", strings.Join(a.args, " "))
		}
		return a.addBatch(a.flags["batch"])
	}
	if len(a.args) == 0 {
		return usageError("add needs a kind: %s, or --batch <file>", strings.Join(addKinds, ", "))
	}
	kind := a.args[0]
	rest := a.args[1:]
	if len(rest) == 0 {
		return usageError("add %s needs text, for example: brain-axi add %s \"...\"", kind, kind)
	}
	if len(rest) > 1 {
		return usageError("add %s takes one quoted text argument, got %d; quote the whole thing", kind, len(rest))
	}
	text := strings.TrimSpace(rest[0])
	if text == "" {
		return usageError("add %s: the text must not be empty", kind)
	}
	if err := a.openVault(); err != nil {
		return err
	}
	switch kind {
	case "event":
		return a.addEvent(text)
	case "idea":
		return a.addIdea(text)
	case "task":
		return a.addTask(text)
	case "note":
		return a.addNote(text)
	case "person":
		return a.addPerson(text)
	default:
		return usageError("unknown add kind %q: valid kinds are %s", kind, strings.Join(addKinds, ", "))
	}
}

// linkFlags reads --links and --raise-with. Both are format-validated here so
// a typo is refused before anything is written; whether the id names a record
// that exists is deliberately not checked, because a link to something not
// captured yet is useful and doctor already reports the dangling ones.
func (a *app) linkFlags() (links, raiseWith []string, err error) {
	links = a.listFlag("links")
	for _, id := range links {
		if err := vault.ValidateID(id); err != nil {
			return nil, nil, usageError("flag --links: %v", err)
		}
	}
	raiseWith = a.listFlag("raise-with")
	for _, id := range raiseWith {
		if err := vault.ValidateID(id); err != nil {
			return nil, nil, usageError("flag --raise-with: %v", err)
		}
	}
	return links, raiseWith, nil
}

// resolveID takes --id when given, otherwise slugs the title, and then finds
// the nearest free id. Ids are stable and never reused.
func (a *app) resolveID(want string) (string, error) {
	if explicit, ok := a.flags["id"]; ok {
		id := strings.TrimSpace(explicit)
		if err := vault.ValidateID(id); err != nil {
			return "", usageError("flag --id: %v", err)
		}
		if taken, ok, err := a.vault.IDTaken(id); err != nil {
			return "", err
		} else if ok {
			return "", usageError("id %q is already used by %s; ids are never reused", id, taken)
		}
		return id, nil
	}
	if want == "" {
		return "", usageError("cannot build an id from the given text; pass --id")
	}
	return a.vault.FreeID(want)
}

func (a *app) addEvent(title string) error {
	rawWhen, ok := a.flags["when"]
	if !ok {
		return usageError("add event needs --when as an absolute timestamp, for example --when 2026-09-04T14:00")
	}
	when, err := a.vault.Zone.Normalise(rawWhen)
	if err != nil {
		return usageError("--when: %v", err)
	}
	duration, _, err := a.spanFlag("duration")
	if err != nil {
		return err
	}
	if duration.Days < 0 || duration.Clock < 0 {
		return usageError("--duration must not be negative")
	}
	with := a.listFlag("with")
	for _, w := range with {
		if err := vault.ValidateID(w); err != nil {
			return usageError("flag --with: %v", err)
		}
	}
	var exceptions []timeref.Date
	for _, raw := range a.listFlag("exceptions") {
		d, err := timeref.ParseDateOnly(raw)
		if err != nil {
			return usageError("flag --exceptions: %v", err)
		}
		exceptions = append(exceptions, d)
	}
	rrule := strings.TrimSpace(a.flagOr("rrule", ""))
	if rrule != "" {
		if err := vault.ValidateRRule(rrule); err != nil {
			return usageError("flag --rrule: %v", err)
		}
	}
	if len(exceptions) > 0 && rrule == "" {
		return usageError("--exceptions needs --rrule: there is no series to except from")
	}

	base := unitext.SlugN(title, 40)
	if base != "" {
		base = fmt.Sprintf("%s-%s", base, when.Format("20060102"))
	}
	id, err := a.resolveID(base)
	if err != nil {
		return err
	}

	// Overlaps are reported and the event is still stored (FR-12): the user
	// double-booking is a fact about their day, not an error.
	overlaps, err := a.findOverlaps(when, duration, rrule)
	if err != nil {
		return err
	}

	links, raiseWith, err := a.linkFlags()
	if err != nil {
		return err
	}
	rel, doc, err := a.vault.BuildEvent(vault.NewEvent{
		ID: id, Title: title, When: when, Duration: duration, With: with,
		Status: a.flagOr("status", ""), RRule: rrule, Exceptions: exceptions,
		Body: a.flagOr("body", ""), Created: a.vault.Zone.DateOf(a.now),
		Links: links, RaiseWith: raiseWith,
	})
	if err != nil {
		return usageError("%v", err)
	}
	if err := a.vault.Save(rel, doc); err != nil {
		return err
	}
	return a.reportAdd(addResult{
		Kind: "event", ID: id, Path: rel, Title: title,
		When: timeref.Format(when), Duration: duration.String(),
		RRule: rrule, Overlaps: overlaps,
		Weekday: render.WeekdayLong(when.Weekday()),
	})
}

// findOverlaps lists the events already occupying the new event's slot. A
// recurring new event is checked on its first occurrence only: reporting every
// future collision of an unbounded series would be noise, not information.
func (a *app) findOverlaps(when time.Time, duration timeref.Span, rrule string) ([]string, error) {
	end := when
	if !duration.IsZero() {
		end = a.vault.Zone.Add(when, duration)
	}
	if end.Equal(when) {
		// A point event overlaps anything running across it.
		end = when.Add(time.Nanosecond)
	}
	date := a.vault.Zone.DateOf(when)
	ag, err := a.engine().Agenda(date, a.vault.Zone.DateOf(end))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, o := range ag.Occurrences {
		if o.Start.Before(end) && when.Before(o.End) {
			out = append(out, fmt.Sprintf("%s at %s", o.Record.ID, o.Start.Format("15:04 02/01")))
			continue
		}
		// A zero-length existing event is a point in time.
		if o.End.Equal(o.Start) && !o.Start.Before(when) && o.Start.Before(end) {
			out = append(out, fmt.Sprintf("%s at %s", o.Record.ID, o.Start.Format("15:04 02/01")))
		}
	}
	return out, nil
}

func (a *app) addIdea(title string) error {
	nudge, hasNudge, err := a.spanFlag("nudge-after")
	if err != nil {
		return err
	}
	if hasNudge && nudge.ApproxDays() <= 0 {
		return usageError("--nudge-after must be at least one day")
	}
	id, err := a.resolveID(unitext.SlugN(title, 60))
	if err != nil {
		return err
	}
	links, raiseWith, err := a.linkFlags()
	if err != nil {
		return err
	}
	rel, doc, err := a.vault.BuildIdea(vault.NewIdea{
		ID: id, Title: title, Status: a.flagOr("status", ""),
		NudgeAfter: nudge, HasNudge: hasNudge,
		Body: a.flagOr("body", ""), Created: a.vault.Zone.DateOf(a.now),
		Links: links, RaiseWith: raiseWith,
	})
	if err != nil {
		return usageError("%v", err)
	}
	if err := a.vault.Save(rel, doc); err != nil {
		return err
	}
	return a.reportAdd(addResult{
		Kind: "idea", ID: id, Path: rel, Title: title,
		Status: a.flagOr("status", "pending"), NudgeAfter: a.vault.Horizon(&vault.Record{}).String(),
	})
}

// addTask captures a commitment the user has to remember to check.
//
// This is deliberately not a delivery work item: the work backlog owns those
// and brain-axi has no write path to any backlog. What lives here is the
// user's own memory of something they must follow up on, which is why
// --follow-up-after is the field that matters most.
func (a *app) addTask(title string) error {
	var due time.Time
	hasDue := false
	if raw, ok := a.flags["due"]; ok {
		parsed, err := a.vault.Zone.Normalise(raw)
		if err != nil {
			return usageError("--due: %v", err)
		}
		due, hasDue = parsed, true
	}
	assignee := strings.TrimSpace(a.flagOr("assignee", ""))
	if assignee != "" {
		if err := vault.ValidateID(assignee); err != nil {
			return usageError("flag --assignee: %v", err)
		}
	}
	follow, hasFollow, err := a.spanFlag("follow-up-after")
	if err != nil {
		return err
	}
	if hasFollow && follow.ApproxDays() <= 0 {
		return usageError("--follow-up-after must be at least one day")
	}
	id, err := a.resolveID(unitext.SlugN(title, 60))
	if err != nil {
		return err
	}
	links, raiseWith, err := a.linkFlags()
	if err != nil {
		return err
	}
	rel, doc, err := a.vault.BuildTask(vault.NewTask{
		ID: id, Title: title, Status: a.flagOr("status", ""), Assignee: assignee,
		Due: due, HasDue: hasDue, FollowUpAfter: follow, HasFollowUp: hasFollow,
		Body: a.flagOr("body", ""), Created: a.vault.Zone.DateOf(a.now),
		Links: links, RaiseWith: raiseWith,
	})
	if err != nil {
		return usageError("%v", err)
	}
	if err := a.vault.Save(rel, doc); err != nil {
		return err
	}
	r, err := a.vault.Find(id)
	if err != nil {
		return err
	}
	res := addResult{
		Kind: "task", ID: id, Path: rel, Title: title,
		Status: r.Status, Assignee: assignee,
		FollowUpAfter: a.vault.Horizon(r).String(),
	}
	if hasDue {
		res.Due = timeref.Format(due)
		res.Weekday = render.WeekdayLong(due.Weekday())
	}
	return a.reportAdd(res)
}

// addBatch ingests a whole batch file: every record is validated before any
// file is written, so a malformed entry anywhere stores nothing at all.
func (a *app) addBatch(path string) error {
	if strings.TrimSpace(path) == "" {
		return usageError("--batch needs a path to a batch file")
	}
	if err := a.openVault(); err != nil {
		return err
	}
	batch, err := a.vault.ReadBatch(path, a.now)
	if err != nil {
		return err
	}
	if err := a.vault.WriteBatch(batch); err != nil {
		return err
	}
	return a.reportBatch(path, batch)
}

// batchRow is one stored record in the batch echo-back.
type batchRow struct {
	Section string `json:"section"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Detail  string `json:"detail,omitempty"`
	Path    string `json:"path"`
}

// reportBatch echoes back everything that was stored, grouped by section.
//
// The echo-back rule applies to a batch exactly as it applies to one capture,
// and it matters more here: a batch is a whole meeting resolved in one go, so a
// misread entry that is not shown now is one the user finds three weeks
// later.
func (a *app) reportBatch(path string, batch *vault.Batch) error {
	rows := make([]batchRow, 0, len(batch.Entries))
	for _, e := range batch.Entries {
		rows = append(rows, batchRow{
			Section: e.Section, Kind: string(e.Kind), ID: e.ID,
			Title: e.Title, Detail: e.Detail, Path: e.Rel,
		})
	}
	if a.out.JSON {
		return a.out.Emit(map[string]any{
			"batch": path, "stored": len(rows), "records": rows,
			"now": timeref.Format(a.now), "timezone": a.vault.Zone.Name(),
		})
	}
	a.out.Scalar("batch", path)
	a.out.Scalar("stored", strconv.Itoa(len(rows)))
	for _, section := range vault.BatchSections {
		block := render.Block{
			Name:    section,
			Columns: render.Cols([]string{"id", "title", "detail", "path"}),
		}
		for _, r := range rows {
			if r.Section == section {
				block.Rows = append(block.Rows, []string{r.ID, r.Title, r.Detail, r.Path})
			}
		}
		// A section the batch did not use is not reported at all, so the
		// echo-back is what was stored rather than a form with blanks.
		if len(block.Rows) > 0 {
			a.out.Block(block)
		}
	}
	a.out.Help([]string{
		"Read every stored row back to the user, grouped by section, so a misread is corrected now rather than in three weeks",
		"Run `brain-axi show <id>` for any row they question",
	})
	return nil
}

// addNote appends to today's daily file, so a thought captured without
// ceremony is never orphaned (US-3). The body is stored verbatim: the tool
// never summarises what the user said.
func (a *app) addNote(text string) error {
	if a.has("id") {
		return usageError("add note takes no --id: a note joins today's daily file, which already has one")
	}
	// A note joins the day's file, so a link or an agenda entry on it would be
	// a statement about the whole day rather than about the thing just said.
	for _, flag := range []string{"links", "raise-with"} {
		if a.has(flag) {
			return usageError("add note takes no --%s: a note joins today's daily file, which is a whole day rather than one item; capture it as a task or an idea instead", flag)
		}
	}
	rel, id, created, err := a.vault.AppendNote(a.now, text)
	if err != nil {
		return err
	}
	verb := "appended to"
	if created {
		verb = "created"
	}
	return a.reportAdd(addResult{
		Kind: "note", ID: id, Path: rel, Title: text,
		Note: verb + " today's daily file",
		When: timeref.Format(a.now),
	})
}

func (a *app) addPerson(name string) error {
	id, err := a.resolveID(unitext.SlugN(name, 60))
	if err != nil {
		return err
	}
	links, raiseWith, err := a.linkFlags()
	if err != nil {
		return err
	}
	if len(raiseWith) > 0 {
		return usageError("add person takes no --raise-with: a profile is who you raise things with, not a thing to raise")
	}
	rel, doc, err := a.vault.BuildPerson(vault.NewPerson{
		ID: id, Title: name, Body: a.flagOr("body", ""),
		Created: a.vault.Zone.DateOf(a.now), Links: links,
	})
	if err != nil {
		return usageError("%v", err)
	}
	if err := a.vault.Save(rel, doc); err != nil {
		return err
	}
	return a.reportAdd(addResult{Kind: "person", ID: id, Path: rel, Title: name})
}

// addResult is what a capture reports back. The id is the whole point: the
// agent and the user both refer to the record by it afterwards.
type addResult struct {
	Kind       string   `json:"kind"`
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	Title      string   `json:"title"`
	When       string   `json:"when,omitempty"`
	Weekday    string   `json:"weekday,omitempty"`
	Duration   string   `json:"duration,omitempty"`
	RRule      string   `json:"rrule,omitempty"`
	Status     string   `json:"status,omitempty"`
	NudgeAfter string   `json:"nudge_after,omitempty"`
	Note       string   `json:"note,omitempty"`
	Overlaps   []string `json:"overlaps,omitempty"`

	Due           string `json:"due,omitempty"`
	Assignee      string `json:"assignee,omitempty"`
	FollowUpAfter string `json:"follow_up_after,omitempty"`
}

func (a *app) reportAdd(r addResult) error {
	if a.out.JSON {
		return a.out.Emit(r)
	}
	a.out.Scalar("added", r.Kind)
	a.out.Scalar("id", r.ID)
	a.out.Scalar("path", r.Path)
	a.out.Scalar("title", render.Quote(r.Title))
	if r.When != "" {
		when := r.When
		if r.Weekday != "" {
			when += " (" + r.Weekday + ")"
		}
		a.out.Scalar("when", when)
	}
	if r.Duration != "" && r.Duration != "0m" {
		a.out.Scalar("duration", r.Duration)
	}
	if r.RRule != "" {
		a.out.Scalar("rrule", r.RRule)
	}
	if r.Status != "" {
		a.out.Scalar("status", r.Status)
	}
	if r.Assignee != "" {
		a.out.Scalar("assignee", r.Assignee)
	}
	if r.Due != "" {
		due := r.Due
		if r.Weekday != "" {
			due += " (" + r.Weekday + ")"
		}
		a.out.Scalar("due", due)
	}
	if r.FollowUpAfter != "" {
		a.out.Scalar("follow_up_after", r.FollowUpAfter)
	}
	if r.Note != "" {
		a.out.Scalar("note", r.Note)
	}
	var attention []string
	for _, o := range r.Overlaps {
		attention = append(attention, "overlaps "+o)
	}
	a.out.Attention(attention)
	help := []string{fmt.Sprintf("Run `brain-axi show %s` to check it", r.ID)}
	if r.Kind == "event" || (r.Kind == "task" && r.Due != "") {
		help = append(help, "Echo the resolved absolute date and weekday back to the user")
	}
	a.out.Help(help)
	return nil
}

func (a *app) cmdDone() error {
	if err := a.requireArgs(1, "done"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	r, err := a.vault.Find(a.args[0])
	if err != nil {
		return err
	}
	var target string
	switch r.Kind {
	case vault.KindEvent:
		target = "done"
	case vault.KindIdea:
		target = "shipped"
	case vault.KindTask:
		target = "done"
	default:
		return usageError("a %s has no status to complete; only an event, an idea or a task does", r.Kind)
	}
	if r.Status == target {
		return usageError("%s is already %s", r.ID, target)
	}
	changes := [][2]string{{"status", target}}
	if r.HasTouched {
		changes = append(changes, [2]string{"touched", a.vault.Zone.DateOf(a.now).String()})
	}
	return a.applyChanges(r, changes)
}

func (a *app) cmdUpdate() error {
	if err := a.requireArgs(1, "update"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	r, err := a.vault.Find(a.args[0])
	if err != nil {
		return err
	}
	var changes [][2]string
	// A status change is the record moving, which is what touched: records.
	// An arbitrary --set is not: silently resetting the decay clock because a
	// metadata key changed is how a second brain becomes a write-only archive.
	movesTheRecord := false
	var statusSet bool
	var statusValue string
	if status, ok := a.flags["status"]; ok {
		if err := validateStatus(r, status); err != nil {
			return err
		}
		changes = append(changes, [2]string{"status", status})
		movesTheRecord = true
		statusSet = true
		statusValue = status
	}
	for _, raw := range a.repeated("set") {
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			return usageError("--set needs key=value, got %q", raw)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return usageError("--set needs a key before the =, got %q", raw)
		}
		if key == "id" || key == "type" {
			return usageError("--set cannot change %q: it is what every reference resolves through", key)
		}
		if key == "status" {
			if err := validateStatus(r, value); err != nil {
				return err
			}
			if statusSet && value != statusValue {
				return usageError("--status %q conflicts with --set status=%q", statusValue, value)
			}
			if !statusSet {
				changes = append(changes, [2]string{"status", value})
				movesTheRecord = true
				statusSet = true
				statusValue = value
			}
			continue
		}
		changes = append(changes, [2]string{key, value})
	}
	if len(changes) == 0 {
		return usageError("update needs something to change: --status <status> or --set key=value")
	}
	if movesTheRecord && r.HasTouched && !named(changes, "touched") {
		changes = append(changes, [2]string{"touched", a.vault.Zone.DateOf(a.now).String()})
	}
	return a.applyChanges(r, changes)
}

// splitList reads a comma-separated --set value into trimmed, non-empty parts.
// An empty value clears the key, which is how a link list is removed.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func validateStatus(r *vault.Record, status string) error {
	allowed := vault.StatusesFor(r.Kind)
	if allowed == nil {
		return usageError("a %s has no status", r.Kind)
	}
	for _, s := range allowed {
		if s == status {
			return nil
		}
	}
	return usageError("unknown status %q for a %s: valid values are %s", status, r.Kind, strings.Join(allowed, ", "))
}

func named(changes [][2]string, key string) bool {
	for _, c := range changes {
		if c[0] == key {
			return true
		}
	}
	return false
}

// repeated collects a flag that may appear more than once. The parser keeps
// only the last value, so --set is re-read from the raw arguments.
func (a *app) repeated(name string) []string {
	var out []string
	for i := 0; i < len(a.rawArgs); i++ {
		arg := a.rawArgs[i]
		if arg == "--"+name {
			if i+1 < len(a.rawArgs) {
				out = append(out, a.rawArgs[i+1])
				i++
			}
			continue
		}
		if v, ok := strings.CutPrefix(arg, "--"+name+"="); ok {
			out = append(out, v)
		}
	}
	return out
}

// applyChanges rewrites only the named frontmatter keys, leaving the body
// byte-for-byte and every other key untouched (FR-7), then re-reads the file
// to prove the result is still a valid record.
func (a *app) applyChanges(r *vault.Record, changes [][2]string) error {
	doc := r.Doc()
	before := map[string]string{}
	for _, c := range changes {
		key, value := c[0], c[1]
		if vault.IsListKey(key) {
			// A key that holds a list is written as one. Joining the values
			// into a scalar would store "a,b" as a single id, which the next
			// read would reject as a malformed one.
			if old, ok, err := doc.Strings(key); err != nil {
				return err
			} else if ok {
				before[key] = strings.Join(old, ",")
			}
			parts := splitList(value)
			if len(parts) == 0 {
				doc.Delete(key)
				continue
			}
			doc.SetStrings(key, parts)
			continue
		}
		if old, ok, err := doc.String(key); err != nil {
			return err
		} else if ok {
			before[key] = old
		}
		doc.Set(key, value)
	}
	data, err := doc.Bytes()
	if err != nil {
		return err
	}
	// Validate before writing: a mutation must never be the thing that makes a
	// file unreadable.
	if _, err := a.vault.ParseRecord(r.Path, r.Rel, data); err != nil {
		return dataError("the change would make %s invalid: %v", r.Rel, err)
	}
	if err := a.vault.WriteFile(r.Rel, data); err != nil {
		return err
	}
	rows := make([][]string, 0, len(changes))
	for _, c := range changes {
		from := before[c[0]]
		if from == "" {
			from = "(unset)"
		}
		rows = append(rows, []string{c[0], from, c[1]})
	}
	if a.out.JSON {
		changed := map[string]any{}
		for _, c := range changes {
			changed[c[0]] = map[string]string{"from": before[c[0]], "to": c[1]}
		}
		return a.out.Emit(map[string]any{"id": r.ID, "path": r.Rel, "changed": changed})
	}
	a.out.Scalar("updated", r.ID)
	a.out.Scalar("path", r.Rel)
	a.out.Block(render.Block{Name: "changed", Columns: render.Cols([]string{"key", "from", "to"}), Rows: rows})
	a.out.Help([]string{fmt.Sprintf("Run `brain-axi show %s` to see the record", r.ID)})
	return nil
}

func (a *app) cmdRemove() error {
	if err := a.requireArgs(1, "rm"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	r, err := a.vault.Find(a.args[0])
	if err != nil {
		return err
	}
	if !a.has("yes") {
		return usageError("rm refuses without --yes; %s is %s (%s). Deleting it is not reversible by this tool, though vault/ git history is",
			r.ID, r.Rel, firstLine(r.Title))
	}
	if err := a.vault.Remove(r.Rel); err != nil {
		return err
	}
	if a.out.JSON {
		return a.out.Emit(map[string]any{"removed": r.ID, "path": r.Rel, "title": r.Title})
	}
	a.out.Scalar("removed", r.ID)
	a.out.Scalar("path", r.Rel)
	a.out.Help([]string{"Run `git checkout -- " + r.Rel + "` inside the vault to bring it back if it was committed"})
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// danglingLink is one reference to an id no record claims, and where to find
// it. It carries a line because "unresolved link" without one is a hunt.
type danglingLink struct {
	Record *vault.Record
	// ID is the target nothing claims, and Field is the key that names it:
	// links, body, with, assignee or raise_with.
	ID    string
	Field string
	Line  int
}

// Where renders the reference as path:line, the same shape every other failure
// in this tool carries.
func (d danglingLink) Where() string { return fmt.Sprintf("%s:%d", d.Record.Rel, d.Line) }

// linksTo is used by doctor to report every unresolved reference vault-wide.
//
// A dangling reference is reported, never rejected: writing a link before its
// target exists is ordinary, and this is the same precedent with: and assignee
// already set. What doctor owes the user is the line to fix it on.
func linksTo(records []*vault.Record) (map[string]bool, []danglingLink) {
	ids := map[string]bool{}
	people := map[string]bool{}
	for _, r := range records {
		ids[r.ID] = true
		if r.Kind == vault.KindPerson {
			people[r.ID] = true
		}
	}
	var dangling []danglingLink
	for _, r := range records {
		for _, group := range []struct {
			field      string
			ids        []string
			personOnly bool
		}{
			{"links", r.Links, false}, {"with", r.With, true}, {"raise_with", r.RaiseWith, true},
		} {
			for _, id := range group.ids {
				resolved := ids[id]
				if group.personOnly {
					resolved = people[id]
				}
				if !resolved {
					dangling = append(dangling, danglingLink{
						Record: r, ID: id, Field: group.field, Line: r.LineOf(group.field),
					})
				}
			}
		}
		for _, id := range r.BodyLinks {
			if !ids[id] {
				dangling = append(dangling, danglingLink{
					Record: r, ID: id, Field: "body", Line: r.LineOfBodyLink(id),
				})
			}
		}
		if r.Assignee != "" && !people[r.Assignee] {
			dangling = append(dangling, danglingLink{
				Record: r, ID: r.Assignee, Field: "assignee", Line: r.LineOf("assignee"),
			})
		}
	}
	return ids, dangling
}
