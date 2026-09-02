package vault

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Thanhbinh1905/secondbrain/internal/frontmatter"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
)

// A batch is one YAML document holding the sections a meeting note breaks down
// into. The agent reads the user's messy note and writes this file; the CLI
// only ever reads it, and it still resolves no relative date and calls no
// model.
//
// YAML rather than a bespoke Markdown dialect because the vault already speaks
// it: gopkg.in/yaml.v3 is a dependency, it reports a line number for every
// node, and internal/frontmatter already turns that into path:line: reason. A
// hand-written batch gets the same error the agent's would.
const (
	// SectionIdeas holds half-formed things, stored as ideas.
	SectionIdeas = "ideas"
	// SectionTasks holds what the user must do soon, stored as tasks.
	SectionTasks = "tasks"
	// SectionDelegated holds work handed to a named person that the user has
	// to remember to check. It is a task with an assignee, not a fifth record
	// kind: the thing being tracked is still the user's own commitment to follow up.
	SectionDelegated = "delegated"
	// SectionNotes holds plain notes, appended to the day's daily file.
	SectionNotes = "notes"
	// SectionEvents holds events and dated decisions, stored as events.
	SectionEvents = "events"
)

// BatchSections are every section a batch file may carry, in the order a
// report lists them.
var BatchSections = []string{SectionIdeas, SectionTasks, SectionDelegated, SectionNotes, SectionEvents}

// BatchEntry is one record a batch will create, resolved and validated but not
// yet written.
type BatchEntry struct {
	// Section is which part of the batch file this came from, so the echo-back
	// can be grouped the way it was dictated.
	Section string
	Kind    Kind
	ID      string
	Rel     string
	Title   string
	// Detail is the one extra fact worth echoing for this section: an event's
	// resolved instant, a task's due date and assignee, an idea's horizon.
	Detail string

	doc *frontmatter.Doc
}

// Batch is a fully validated batch: every entry resolved, every id and path
// free, nothing written yet.
type Batch struct {
	Entries []BatchEntry
}

// BatchWriteError reports an I/O failure partway through writing a batch.
//
// Batch ingest is the one operation in this tool that touches more than one
// file, and a filesystem offers no transaction across several. Its atomicity is
// therefore a validation gate: a malformed batch writes nothing at all. When
// the disk itself fails between two writes there is nothing to roll back to, so
// the tool reports exactly which files landed and which did not rather than
// deleting the user's data to keep a promise it cannot keep. vault/'s git
// history is the recovery path.
type BatchWriteError struct {
	// Root is the vault the partial batch landed in, so the recovery command
	// in the message is one the user can paste.
	Root    string
	Written []string
	Pending []string
	Err     error
}

func (e *BatchWriteError) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "the batch failed partway through and was not rolled back: %v", e.Err)
	sb.WriteString(fmt.Sprintf("\n  %d file(s) were written:", len(e.Written)))
	for _, w := range e.Written {
		sb.WriteString("\n    - " + w)
	}
	sb.WriteString(fmt.Sprintf("\n  %d file(s) were not:", len(e.Pending)))
	for _, p := range e.Pending {
		sb.WriteString("\n    - " + p)
	}
	fmt.Fprintf(&sb, "\nrun `git -C %s status` to see them; nothing was deleted", e.Root)
	return sb.String()
}

func (e *BatchWriteError) Unwrap() error { return e.Err }

// ReadBatch parses and fully validates a batch file without writing anything.
//
// Every failure carries path:line: reason against the batch file itself, and
// stops the whole batch: a note dictated in one breath is stored in
// one piece or not at all.
func (v *Vault) ReadBatch(path string, now time.Time) (*Batch, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the batch file: %w", err)
	}
	return v.ParseBatch(path, raw, now)
}

// ParseBatch validates raw batch bytes against the vault, resolving every id
// and path. It is separated from ReadBatch so a test can drive it without a
// file on disk.
func (v *Vault) ParseBatch(path string, raw []byte, now time.Time) (*Batch, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, batchYAMLError(path, err)
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return nil, posErr(path, 1, "the batch file is empty; it needs at least one of %s", strings.Join(BatchSections, ", "))
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, posErr(path, doc.Line, "a batch must be a mapping of section to entries, found %s", nodeKind(doc.Kind))
	}

	// Duplicate keys are silently accepted by yaml.v3 and must not be here: a
	// second `tasks:` block would silently drop the first one's entries.
	seen := map[string]int{}
	for i := 0; i < len(doc.Content); i += 2 {
		k := doc.Content[i]
		if first, dup := seen[k.Value]; dup {
			return nil, posErr(path, k.Line, "duplicate section %q, first defined on line %d", k.Value, first)
		}
		seen[k.Value] = k.Line
	}

	b := &Batch{}
	// takenIDs and takenPaths start from the vault and then accumulate this
	// batch's own choices, so two entries in one batch can never collide with
	// each other any more than they can with an existing record.
	takenIDs, err := v.takenIDs()
	if err != nil {
		return nil, err
	}
	takenPaths := map[string]bool{}
	var dailyBullets []string
	dailyLine := 0

	for i := 0; i < len(doc.Content); i += 2 {
		key, value := doc.Content[i], doc.Content[i+1]
		if !allowed(BatchSections, key.Value) {
			return nil, posErr(path, key.Line, "unknown batch section %q: valid sections are %s", key.Value, strings.Join(BatchSections, ", "))
		}
		if value.Kind != yaml.SequenceNode {
			return nil, posErr(path, value.Line, "section %q must be a list of entries, found %s", key.Value, nodeKind(value.Kind))
		}
		for _, item := range value.Content {
			if item.Kind != yaml.MappingNode {
				return nil, posErr(path, item.Line, "each %s entry must be a mapping of field to value, found %s", key.Value, nodeKind(item.Kind))
			}
			e := entryNode{path: path, section: key.Value, node: item}
			if err := e.index(); err != nil {
				return nil, err
			}
			if key.Value == SectionNotes {
				bullet, err := v.batchNote(&e, now)
				if err != nil {
					return nil, err
				}
				dailyBullets = append(dailyBullets, bullet)
				if dailyLine == 0 {
					dailyLine = item.Line
				}
				continue
			}
			entry, err := v.batchRecord(&e, now, takenIDs, takenPaths)
			if err != nil {
				return nil, err
			}
			takenIDs[entry.ID] = true
			takenPaths[entry.Rel] = true
			b.Entries = append(b.Entries, entry)
		}
	}

	if len(dailyBullets) > 0 {
		entry, err := v.batchDaily(path, dailyLine, dailyBullets, now)
		if err != nil {
			return nil, err
		}
		b.Entries = append(b.Entries, entry)
	}
	if len(b.Entries) == 0 {
		return nil, posErr(path, 1, "the batch has no entries in any of %s", strings.Join(BatchSections, ", "))
	}
	// Written in section order so a report and the write order agree.
	order := map[string]int{}
	for i, s := range BatchSections {
		order[s] = i
	}
	sort.SliceStable(b.Entries, func(i, j int) bool {
		return order[b.Entries[i].Section] < order[b.Entries[j].Section]
	})
	return b, nil
}

// WriteBatch commits a validated batch. Every document was built and re-validated
// before this function was called, so the only failure left is the filesystem
// itself - which is reported file by file rather than swallowed or rolled back.
func (v *Vault) WriteBatch(b *Batch) error {
	var written []string
	for i, e := range b.Entries {
		if err := v.Save(e.Rel, e.doc); err != nil {
			pending := make([]string, 0, len(b.Entries)-i)
			for _, rest := range b.Entries[i:] {
				pending = append(pending, rest.Rel)
			}
			return &BatchWriteError{Root: v.Root, Written: written, Pending: pending, Err: err}
		}
		written = append(written, e.Rel)
	}
	return nil
}

// takenIDs is every id the vault already holds.
func (v *Vault) takenIDs() (map[string]bool, error) {
	records, err := v.Walk()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(records))
	for _, r := range records {
		out[r.ID] = true
	}
	return out, nil
}

// entryNode is one mapping in a batch section, indexed by field name so every
// error can name the line the field is actually on.
type entryNode struct {
	path    string
	section string
	node    *yaml.Node
	fields  map[string]*yaml.Node
	keys    map[string]*yaml.Node
}

func (e *entryNode) index() error {
	e.fields = map[string]*yaml.Node{}
	e.keys = map[string]*yaml.Node{}
	for i := 0; i < len(e.node.Content); i += 2 {
		k, val := e.node.Content[i], e.node.Content[i+1]
		if _, dup := e.fields[k.Value]; dup {
			return posErr(e.path, k.Line, "duplicate field %q in this %s entry, first defined on line %d",
				k.Value, e.section, e.keys[k.Value].Line)
		}
		e.fields[k.Value] = val
		e.keys[k.Value] = k
	}
	return nil
}

// line is where an error about a field belongs, falling back to the entry.
func (e *entryNode) line(field string) int {
	if k, ok := e.keys[field]; ok {
		return k.Line
	}
	return e.node.Line
}

func (e *entryNode) errf(field, format string, args ...any) error {
	return posErr(e.path, e.line(field), "%s: %s", e.section, fmt.Sprintf(format, args...))
}

// str reads a scalar field.
func (e *entryNode) str(field string) (string, bool, error) {
	n, ok := e.fields[field]
	if !ok {
		return "", false, nil
	}
	if n.Kind != yaml.ScalarNode {
		return "", false, e.errf(field, "%q must be a single value, found %s", field, nodeKind(n.Kind))
	}
	return strings.TrimSpace(n.Value), true, nil
}

// require reads a scalar field that must be present and non-empty.
func (e *entryNode) require(field string) (string, error) {
	value, ok, err := e.str(field)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", e.errf(field, "this entry needs a %q", field)
	}
	if value == "" {
		return "", e.errf(field, "%q must not be empty", field)
	}
	return value, nil
}

// list reads a field that is either a YAML list or a comma-separated scalar,
// because a human writing this by hand will reach for either.
func (e *entryNode) list(field string) ([]string, error) {
	n, ok := e.fields[field]
	if !ok {
		return nil, nil
	}
	var out []string
	switch n.Kind {
	case yaml.ScalarNode:
		for _, part := range strings.Split(n.Value, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if item.Kind != yaml.ScalarNode {
				return nil, e.errf(field, "every %q entry must be a single value, found %s", field, nodeKind(item.Kind))
			}
			if p := strings.TrimSpace(item.Value); p != "" {
				out = append(out, p)
			}
		}
	default:
		return nil, e.errf(field, "%q must be a list or a comma-separated value, found %s", field, nodeKind(n.Kind))
	}
	return out, nil
}

// reject refuses a field that does not belong in this section, so a
// misremembered key is reported rather than silently ignored.
func (e *entryNode) reject(allowedFields ...string) error {
	for name := range e.fields {
		if !allowed(allowedFields, name) {
			return e.errf(name, "unknown field %q; a %s entry takes %s",
				name, e.section, strings.Join(allowedFields, ", "))
		}
	}
	return nil
}

// batchRecord validates one non-note entry into a ready-to-write document.
func (v *Vault) batchRecord(e *entryNode, now time.Time, takenIDs map[string]bool, takenPaths map[string]bool) (BatchEntry, error) {
	switch e.section {
	case SectionIdeas:
		return v.batchIdea(e, now, takenIDs, takenPaths)
	case SectionTasks, SectionDelegated:
		return v.batchTask(e, now, takenIDs, takenPaths)
	case SectionEvents:
		return v.batchEvent(e, now, takenIDs, takenPaths)
	default:
		return BatchEntry{}, e.errf("", "no record kind handles this section")
	}
}

func (v *Vault) batchIdea(e *entryNode, now time.Time, takenIDs, takenPaths map[string]bool) (BatchEntry, error) {
	if err := e.reject("title", "body", "id", "nudge_after", "status", "links", "raise_with"); err != nil {
		return BatchEntry{}, err
	}
	title, err := e.require("title")
	if err != nil {
		return BatchEntry{}, err
	}
	body, _, err := e.str("body")
	if err != nil {
		return BatchEntry{}, err
	}
	status, _, err := e.str("status")
	if err != nil {
		return BatchEntry{}, err
	}
	if status != "" && !allowed(IdeaStatuses, status) {
		return BatchEntry{}, e.errf("status", "unknown status %q for an idea: valid values are %s", status, strings.Join(IdeaStatuses, ", "))
	}
	nudge, hasNudge, err := e.span("nudge_after")
	if err != nil {
		return BatchEntry{}, err
	}
	id, err := v.batchID(e, title, takenIDs)
	if err != nil {
		return BatchEntry{}, err
	}
	links, raiseWith, err := e.linkFields()
	if err != nil {
		return BatchEntry{}, err
	}
	rel, doc, err := v.BuildIdea(NewIdea{
		ID: id, Title: title, Status: status,
		NudgeAfter: nudge, HasNudge: hasNudge,
		Body: body, Created: v.Zone.DateOf(now),
		Links: links, RaiseWith: raiseWith,
	})
	if err != nil {
		return BatchEntry{}, e.errf("title", "%v", err)
	}
	rel, err = v.batchPath(e, rel, takenPaths)
	if err != nil {
		return BatchEntry{}, err
	}
	detail := "horizon " + v.NudgeAfter.String()
	if hasNudge {
		detail = "horizon " + nudge.String()
	}
	return v.sealed(e, KindIdea, id, rel, title, detail, doc)
}

func (v *Vault) batchTask(e *entryNode, now time.Time, takenIDs, takenPaths map[string]bool) (BatchEntry, error) {
	if err := e.reject("title", "body", "id", "assignee", "due", "follow_up_after", "status", "links", "raise_with"); err != nil {
		return BatchEntry{}, err
	}
	title, err := e.require("title")
	if err != nil {
		return BatchEntry{}, err
	}
	body, _, err := e.str("body")
	if err != nil {
		return BatchEntry{}, err
	}
	status, _, err := e.str("status")
	if err != nil {
		return BatchEntry{}, err
	}
	if status != "" && !allowed(TaskStatuses, status) {
		return BatchEntry{}, e.errf("status", "unknown status %q for a task: valid values are %s", status, strings.Join(TaskStatuses, ", "))
	}
	assignee, hasAssignee, err := e.str("assignee")
	if err != nil {
		return BatchEntry{}, err
	}
	// The delegated section exists precisely to record who has it, so an entry
	// there without an assignee is a mistake worth naming rather than a task.
	if e.section == SectionDelegated && (!hasAssignee || assignee == "") {
		return BatchEntry{}, e.errf("assignee", "a delegated entry needs an %q; without one it is a plain task, so put it under %q instead", "assignee", SectionTasks)
	}
	if assignee != "" {
		if err := ValidateID(assignee); err != nil {
			return BatchEntry{}, e.errf("assignee", "%v", err)
		}
	}
	due, hasDue, err := e.instant(v, "due")
	if err != nil {
		return BatchEntry{}, err
	}
	follow, hasFollow, err := e.span("follow_up_after")
	if err != nil {
		return BatchEntry{}, err
	}
	id, err := v.batchID(e, title, takenIDs)
	if err != nil {
		return BatchEntry{}, err
	}
	links, raiseWith, err := e.linkFields()
	if err != nil {
		return BatchEntry{}, err
	}
	rel, doc, err := v.BuildTask(NewTask{
		ID: id, Title: title, Status: status, Assignee: assignee,
		Due: due, HasDue: hasDue, FollowUpAfter: follow, HasFollowUp: hasFollow,
		Body: body, Created: v.Zone.DateOf(now),
		Links: links, RaiseWith: raiseWith,
	})
	if err != nil {
		return BatchEntry{}, e.errf("title", "%v", err)
	}
	rel, err = v.batchPath(e, rel, takenPaths)
	if err != nil {
		return BatchEntry{}, err
	}
	var parts []string
	if assignee != "" {
		parts = append(parts, "assignee "+assignee)
	}
	if hasDue {
		parts = append(parts, "due "+timeref.Format(due))
	}
	horizon := v.FollowUpAfter
	if hasFollow {
		horizon = follow
	}
	parts = append(parts, "follow up after "+horizon.String())
	return v.sealed(e, KindTask, id, rel, title, strings.Join(parts, ", "), doc)
}

func (v *Vault) batchEvent(e *entryNode, now time.Time, takenIDs, takenPaths map[string]bool) (BatchEntry, error) {
	if err := e.reject("title", "body", "id", "when", "duration", "with", "rrule", "exceptions", "status", "links", "raise_with"); err != nil {
		return BatchEntry{}, err
	}
	title, err := e.require("title")
	if err != nil {
		return BatchEntry{}, err
	}
	body, _, err := e.str("body")
	if err != nil {
		return BatchEntry{}, err
	}
	status, _, err := e.str("status")
	if err != nil {
		return BatchEntry{}, err
	}
	if status != "" && !allowed(EventStatuses, status) {
		return BatchEntry{}, e.errf("status", "unknown status %q for an event: valid values are %s", status, strings.Join(EventStatuses, ", "))
	}
	when, hasWhen, err := e.instant(v, "when")
	if err != nil {
		return BatchEntry{}, err
	}
	if !hasWhen {
		return BatchEntry{}, e.errf("when", "an event entry needs a %q with an absolute timestamp; the agent resolves the phrase, this tool never does", "when")
	}
	duration, _, err := e.span("duration")
	if err != nil {
		return BatchEntry{}, err
	}
	with, err := e.list("with")
	if err != nil {
		return BatchEntry{}, err
	}
	for _, w := range with {
		if err := ValidateID(w); err != nil {
			return BatchEntry{}, e.errf("with", "%v", err)
		}
	}
	rrule, _, err := e.str("rrule")
	if err != nil {
		return BatchEntry{}, err
	}
	if rrule != "" {
		if err := ValidateRRule(rrule); err != nil {
			return BatchEntry{}, e.errf("rrule", "%v", err)
		}
	}
	rawExceptions, err := e.list("exceptions")
	if err != nil {
		return BatchEntry{}, err
	}
	var exceptions []timeref.Date
	for _, raw := range rawExceptions {
		d, err := timeref.ParseDateOnly(raw)
		if err != nil {
			return BatchEntry{}, e.errf("exceptions", "%v", err)
		}
		exceptions = append(exceptions, d)
	}
	if len(exceptions) > 0 && rrule == "" {
		return BatchEntry{}, e.errf("exceptions", "exceptions without an rrule: there is no series to except from")
	}
	base := unitext.SlugN(title, 40)
	if base != "" {
		base = fmt.Sprintf("%s-%s", base, when.Format("20060102"))
	}
	id, err := v.batchIDFrom(e, base, takenIDs)
	if err != nil {
		return BatchEntry{}, err
	}
	links, raiseWith, err := e.linkFields()
	if err != nil {
		return BatchEntry{}, err
	}
	rel, doc, err := v.BuildEvent(NewEvent{
		ID: id, Title: title, When: when, Duration: duration, With: with,
		Status: status, RRule: rrule, Exceptions: exceptions,
		Body: body, Created: v.Zone.DateOf(now),
		Links: links, RaiseWith: raiseWith,
	})
	if err != nil {
		return BatchEntry{}, e.errf("title", "%v", err)
	}
	rel, err = v.batchPath(e, rel, takenPaths)
	if err != nil {
		return BatchEntry{}, err
	}
	return v.sealed(e, KindEvent, id, rel, title, timeref.Format(when), doc)
}

// linkFields reads an entry's links: and raise_with:.
//
// A batch is where the linking layer earns its keep: the agent gives the
// meeting an explicit id and every idea that came out of it links to that id,
// so "what did that meeting produce" is answerable afterwards without anyone
// having remembered to write it down twice.
func (e *entryNode) linkFields() (links, raiseWith []string, err error) {
	links, err = e.list("links")
	if err != nil {
		return nil, nil, err
	}
	for _, id := range links {
		if err := ValidateID(id); err != nil {
			return nil, nil, e.errf("links", "%v", err)
		}
	}
	raiseWith, err = e.list("raise_with")
	if err != nil {
		return nil, nil, err
	}
	for _, id := range raiseWith {
		if err := ValidateID(id); err != nil {
			return nil, nil, e.errf("raise_with", "%v", err)
		}
	}
	return links, raiseWith, nil
}

// batchNote turns one notes entry into a daily-file bullet.
func (v *Vault) batchNote(e *entryNode, now time.Time) (string, error) {
	if err := e.reject("text", "title"); err != nil {
		return "", err
	}
	// `text` is the documented spelling; `title` is accepted because a human
	// writing this by hand will reach for the same key the other sections use.
	field := "text"
	if _, ok := e.fields["text"]; !ok {
		if _, ok := e.fields["title"]; ok {
			field = "title"
		}
	}
	text, err := e.require(field)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("- %s %s\n", now.In(v.Zone.Loc).Format("15:04 -07:00"), text), nil
}

// batchDaily folds every note in the batch into one write of the day's file.
//
// AppendNote does a read-modify-write per note, which would make a batch of
// five notes five writes of the same file and five chances to fail halfway. One
// accumulated document keeps the batch's write phase to one write per output
// file.
func (v *Vault) batchDaily(path string, line int, bullets []string, now time.Time) (BatchEntry, error) {
	date := v.Zone.DateOf(now)
	rel := DailyRel(date)
	id := DailyID(date)
	abs := v.Root + string(os.PathSeparator) + rel

	raw, readErr := os.ReadFile(abs)
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return BatchEntry{}, fmt.Errorf("reading %s: %w", rel, readErr)
		}
		doc := frontmatter.New(rel, "\n"+strings.Join(bullets, ""), [][2]string{
			{"type", string(KindDaily)},
			{"id", id},
			{"title", date.String()},
			{"created", date.String()},
			{"touched", date.String()},
		}...)
		return v.sealedDaily(path, line, id, rel, date.String(), len(bullets), doc)
	}
	doc, err := frontmatter.Parse(rel, raw)
	if err != nil {
		return BatchEntry{}, err
	}
	// Validate the existing file before adding to it: appending to a corrupt
	// daily file would bury the corruption under new content.
	if _, err := v.ParseRecord(abs, rel, raw); err != nil {
		return BatchEntry{}, err
	}
	if !strings.HasSuffix(doc.Body, "\n") && doc.Body != "" {
		doc.Body += "\n"
	}
	doc.Body += strings.Join(bullets, "")
	doc.Set("touched", date.String())
	return v.sealedDaily(path, line, id, rel, date.String(), len(bullets), doc)
}

// sealed re-parses a built document before the batch is allowed to hold it, so
// a batch that reaches the write phase cannot produce an unreadable file.
func (v *Vault) sealed(e *entryNode, kind Kind, id, rel, title, detail string, doc *frontmatter.Doc) (BatchEntry, error) {
	if err := v.proves(rel, doc); err != nil {
		return BatchEntry{}, e.errf("title", "%v", err)
	}
	return BatchEntry{Section: e.section, Kind: kind, ID: id, Rel: rel, Title: title, Detail: detail, doc: doc}, nil
}

func (v *Vault) sealedDaily(path string, line int, id, rel, title string, bullets int, doc *frontmatter.Doc) (BatchEntry, error) {
	if err := v.proves(rel, doc); err != nil {
		return BatchEntry{}, posErr(path, line, "notes: %v", err)
	}
	detail := fmt.Sprintf("%d bullet(s) appended to the daily file", bullets)
	return BatchEntry{
		Section: SectionNotes, Kind: KindDaily, ID: id, Rel: rel,
		Title: title, Detail: detail, doc: doc,
	}, nil
}

// proves renders a document and reads it back as a record. A batch entry that
// cannot survive this never reaches the write phase.
func (v *Vault) proves(rel string, doc *frontmatter.Doc) error {
	data, err := doc.Bytes()
	if err != nil {
		return err
	}
	if _, err := v.ParseRecord(v.Root+string(os.PathSeparator)+rel, rel, data); err != nil {
		return err
	}
	return nil
}

// batchID resolves an entry's id from an explicit id: or from its title.
func (v *Vault) batchID(e *entryNode, title string, taken map[string]bool) (string, error) {
	return v.batchIDFrom(e, unitext.SlugN(title, 60), taken)
}

func (v *Vault) batchIDFrom(e *entryNode, want string, taken map[string]bool) (string, error) {
	if explicit, ok, err := e.str("id"); err != nil {
		return "", err
	} else if ok {
		if err := ValidateID(explicit); err != nil {
			return "", e.errf("id", "%v", err)
		}
		if taken[explicit] {
			return "", e.errf("id", "id %q is already used; ids are never reused", explicit)
		}
		return explicit, nil
	}
	if want == "" {
		return "", e.errf("title", "cannot build an id from this title; give the entry an explicit %q", "id")
	}
	if !taken[want] {
		return want, nil
	}
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d", want, n)
		if !taken[candidate] {
			return candidate, nil
		}
	}
	return "", e.errf("title", "cannot find a free id near %q after 999 attempts", want)
}

// batchPath keeps two entries in one batch from choosing the same filename.
// FreePath only knows what is on disk, and nothing in this batch is yet.
func (v *Vault) batchPath(e *entryNode, rel string, taken map[string]bool) (string, error) {
	if !taken[rel] {
		return rel, nil
	}
	ext := ".md"
	stem := strings.TrimSuffix(rel, ext)
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, n, ext)
		if !taken[candidate] && !v.Exists(candidate) {
			return candidate, nil
		}
	}
	return "", e.errf("title", "cannot find a free filename near %q after 999 attempts", rel)
}

// instant reads an absolute timestamp. A naive value is normalised through the
// vault zone exactly as it is on the command line, and a relative phrase is
// rejected: resolving one is the agent's job.
func (e *entryNode) instant(v *Vault, field string) (time.Time, bool, error) {
	raw, ok, err := e.str(field)
	if err != nil {
		return time.Time{}, false, err
	}
	if !ok || raw == "" {
		return time.Time{}, false, nil
	}
	t, err := v.Zone.Normalise(raw)
	if err != nil {
		return time.Time{}, false, e.errf(field, "%s: %v", field, err)
	}
	return t, true, nil
}

func (e *entryNode) span(field string) (timeref.Span, bool, error) {
	raw, ok, err := e.str(field)
	if err != nil {
		return timeref.Span{}, false, err
	}
	if !ok || raw == "" {
		return timeref.Span{}, false, nil
	}
	span, err := timeref.ParseSpan(raw)
	if err != nil {
		return timeref.Span{}, false, e.errf(field, "%s: %v", field, err)
	}
	if span.ApproxDays() <= 0 && span.Clock <= 0 {
		return timeref.Span{}, false, e.errf(field, "%s %q must be positive", field, raw)
	}
	return span, true, nil
}

// posErr builds a path:line: reason error against the batch file, reusing the
// frontmatter package's error type so the CLI maps it to exit code 2 exactly as
// it maps a malformed record.
func posErr(path string, line int, format string, args ...any) *frontmatter.Error {
	if line <= 0 {
		line = 1
	}
	return &frontmatter.Error{Path: path, Line: line, Msg: fmt.Sprintf(format, args...)}
}

// batchYAMLError rewrites yaml.v3's own message into path:line: reason,
// mirroring what internal/frontmatter does for a record.
//
// The line is yaml.v3's, and for an indentation error it is the line the parser
// gave up on rather than the line that was mistyped. Reporting the parser's
// own position is still better than inventing one, and the message names the
// problem either way.
func batchYAMLError(path string, err error) error {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	msg = strings.TrimPrefix(msg, "unmarshal errors:\n  ")
	line := 1
	if m := yamlLineRe.FindStringSubmatchIndex(msg); m != nil {
		if n, convErr := strconv.Atoi(msg[m[2]:m[3]]); convErr == nil {
			line = n
			msg = msg[:m[0]] + msg[m[1]:]
		}
	}
	return posErr(path, line, "%s", strings.TrimSpace(msg))
}

// yamlLineRe matches the "line N:" position yaml.v3 puts in its errors.
var yamlLineRe = regexp.MustCompile(`line (\d+): `)

func nodeKind(k yaml.Kind) string {
	switch k {
	case yaml.SequenceNode:
		return "a list"
	case yaml.MappingNode:
		return "a mapping"
	case yaml.ScalarNode:
		return "a single value"
	case yaml.AliasNode:
		return "an alias"
	default:
		return "nothing"
	}
}
