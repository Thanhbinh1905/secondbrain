package vault

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/forge"
	"github.com/Thanhbinh1905/secondbrain/internal/frontmatter"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
)

// Kind is a record's type: key on the closed vocabulary, not on which
// directory the file happens to sit in, so a hand-moved file still reads right.
type Kind string

const (
	KindEvent  Kind = "event"
	KindIdea   Kind = "idea"
	KindTask   Kind = "task"
	KindNote   Kind = "note"
	KindPerson Kind = "person"
	KindDaily  Kind = "daily"
)

// Kinds are every valid type value.
var Kinds = []Kind{KindEvent, KindIdea, KindTask, KindNote, KindPerson, KindDaily}

// Status vocabularies are closed. An open vocabulary means a query can never
// make a confident statement about what is outstanding.
//
// A task is a commitment the user has to remember to check, which is why
// waiting is a first-class value rather than a flavour of open: something
// handed to somebody else is not the same as something still on their own desk.
var (
	EventStatuses = []string{"scheduled", "done", "cancelled"}
	IdeaStatuses  = []string{"pending", "building", "shipped", "dropped"}
	TaskStatuses  = []string{"open", "waiting", "done", "dropped"}
)

// TaskIsOpen reports whether a task status still needs following up. A done or
// dropped task has stopped decaying: nagging about something already finished
// is exactly the noise that makes a user stop reading what comes back.
func TaskIsOpen(status string) bool { return status == "open" || status == "waiting" }

// StatusesFor returns the closed status vocabulary for a kind, or nil when the
// kind carries no status.
func StatusesFor(k Kind) []string {
	switch k {
	case KindEvent:
		return EventStatuses
	case KindIdea:
		return IdeaStatuses
	case KindTask:
		return TaskStatuses
	default:
		return nil
	}
}

// DefaultDirFor is where add puts a new record of this kind.
func DefaultDirFor(k Kind) string {
	switch k {
	case KindEvent:
		return EventsDir
	case KindIdea:
		return IdeasDir
	case KindTask:
		return TasksDir
	case KindNote:
		return NotesDir
	case KindPerson:
		return PeopleDir
	case KindDaily:
		return DailyDir
	default:
		return ""
	}
}

// Record is one parsed vault file. Every field is validated on load, so a
// Record in hand is a record that made sense.
type Record struct {
	// Path is absolute; Rel is relative to the vault root and is what output
	// shows, because it is stable across machines.
	Path string
	Rel  string

	Kind    Kind
	ID      string
	Title   string
	Status  string
	Created timeref.Date

	// Touched is when an idea last moved. Absent on other kinds.
	Touched    timeref.Date
	HasTouched bool
	NudgeAfter timeref.Span
	HasNudge   bool

	// When is an event's start instant, always with an explicit offset.
	When    time.Time
	HasWhen bool
	// Duration is how long an event runs. Zero means a point in time.
	Duration timeref.Span
	With     []string

	// RRule is an RFC 5545 recurrence rule. Occurrences are expanded at query
	// time and never written to disk.
	RRule      string
	Exceptions []timeref.Date

	// Due is a task's deadline, always with an explicit offset. Assignee names
	// the people/ record a task was handed to, and FollowUpAfter is how long
	// it will be left before the user wants a reminder to check on it.
	Due           time.Time
	HasDue        bool
	Assignee      string
	FollowUpAfter timeref.Span
	HasFollowUp   bool

	// Forge is the cached status of a linked pull or merge request. It lives in
	// the record's own frontmatter rather than a side store: derived data in the
	// source of truth is acceptable exactly because it is visible and
	// hand-editable there.
	Forge    ForgeLink
	HasForge bool

	// FleetTasks are ids of work items owned by an external supervisor. They are
	// written by `link fleet` and read by nobody: brain-axi never reads a
	// supervisor's state, so this is a one-directional note to a human.
	FleetTasks []string

	// ShippedAt is when the work this record stands for landed, always with an
	// explicit offset, and ShippedPR is the change that landed it. They are
	// what makes "which ideas shipped last quarter" answerable from the files.
	ShippedAt  time.Time
	HasShipped bool
	ShippedPR  string

	Tags []string
	Body string
	// Links are the ids listed in the record's own `links:` frontmatter. They
	// are the linking layer: plain text a human can hand-edit and correct.
	Links []string
	// BodyLinks are the [[wiki-link]] targets found in the body, in order of
	// first appearance.
	BodyLinks []string

	// RaiseWith names the people this record is waiting to be raised with, and
	// Raised is the date it was. A record on somebody's agenda is one that
	// names them here, has not been raised, and has not closed.
	RaiseWith []string
	Raised    timeref.Date
	HasRaised bool

	doc *frontmatter.Doc
}

// ForgeLink is a record's link to a pull or merge request, plus the last
// status that was read for it and when.
//
// CheckedAt is not optional. A cached state with no timestamp is
// indistinguishable from a live one, which is the single way this feature could
// mislead the user, so a link that has been checked must say when.
type ForgeLink struct {
	URL       string
	State     string
	Checks    string
	CheckedAt time.Time
	// HasStatus reports whether this link has ever been checked. A link with no
	// status is reported as never checked, never as fine.
	HasStatus bool
}

// Doc exposes the parsed frontmatter so a mutation can preserve every key the
// tool does not know about.
func (r *Record) Doc() *frontmatter.Doc { return r.doc }

// wikiLinkRe matches an Obsidian-style [[target]] or [[target|label]].
var wikiLinkRe = regexp.MustCompile(`\[\[([^\[\]|]+)(?:\|[^\[\]]*)?\]\]`)

// ParseLinks extracts wiki-link targets from body text, first appearance first.
func ParseLinks(body string) []string {
	matches := wikiLinkRe.FindAllStringSubmatch(body, -1)
	if matches == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		target := strings.TrimSpace(m[1])
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		out = append(out, target)
	}
	return out
}

// ListKeys are the frontmatter keys that hold a list of values. A mutation
// that writes one of them as a comma-joined scalar would store "a,b" as a
// single entry, so every write path consults this rather than guessing from
// the value it was handed.
var ListKeys = []string{"links", "raise_with", "with", "tags", "fleet_tasks", "exceptions"}

// IsListKey reports whether a frontmatter key holds a list.
func IsListKey(key string) bool {
	for _, k := range ListKeys {
		if k == key {
			return true
		}
	}
	return false
}

// LineOfBodyLink is the file line a [[wiki-link]] to id sits on, or the
// record's first line when the record was built rather than parsed.
func (r *Record) LineOfBodyLink(id string) int {
	base := r.doc.BodyLine()
	if base <= 0 {
		return 1
	}
	for i, line := range strings.Split(r.Body, "\n") {
		for _, match := range wikiLinkRe.FindAllStringSubmatch(line, -1) {
			if strings.TrimSpace(match[1]) == id {
				return base + i
			}
		}
	}
	return base
}

// LineOf is the file line a frontmatter key sits on.
func (r *Record) LineOf(key string) int { return r.doc.Line(key) }

// idRe constrains ids to what a filename, a URL and a wiki-link can all carry.
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidateID reports why an id is unusable, or nil.
func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("id must not be empty")
	}
	if !idRe.MatchString(id) {
		return fmt.Errorf("id %q must be lower-case letters, digits, dot, underscore or hyphen, starting with a letter or digit", id)
	}
	return nil
}

// ParseRecord validates one file's frontmatter and body into a Record.
// Every failure carries path:line: reason and stops the caller (NFR-4): a
// second brain that skips a malformed file is worse than one that refuses.
func (v *Vault) ParseRecord(path, rel string, raw []byte) (*Record, error) {
	doc, err := frontmatter.Parse(rel, raw)
	if err != nil {
		return nil, err
	}
	r := &Record{Path: path, Rel: rel, Body: doc.Body, doc: doc, BodyLinks: ParseLinks(doc.Body)}

	rawKind, err := doc.Require("type")
	if err != nil {
		return nil, err
	}
	kind := Kind(rawKind)
	if DefaultDirFor(kind) == "" {
		return nil, doc.Errorf("type", "unknown type %q: valid types are %s", rawKind, joinKinds(Kinds))
	}
	r.Kind = kind

	id, err := doc.Require("id")
	if err != nil {
		return nil, err
	}
	if err := ValidateID(id); err != nil {
		return nil, doc.Errorf("id", "%v", err)
	}
	r.ID = id

	if title, ok, err := doc.String("title"); err != nil {
		return nil, err
	} else if ok {
		r.Title = title
	}

	rawCreated, err := doc.Require("created")
	if err != nil {
		return nil, err
	}
	created, err := timeref.ParseDateOnly(rawCreated)
	if err != nil {
		return nil, doc.Errorf("created", "%v", err)
	}
	r.Created = created

	if err := v.parseStatus(doc, r); err != nil {
		return nil, err
	}
	if err := v.parseIdeaFields(doc, r); err != nil {
		return nil, err
	}
	if err := v.parseEventFields(doc, r); err != nil {
		return nil, err
	}
	if err := v.parseTaskFields(doc, r); err != nil {
		return nil, err
	}
	if err := v.parseForgeFields(doc, r); err != nil {
		return nil, err
	}
	if err := v.parseLinkFields(doc, r); err != nil {
		return nil, err
	}
	if err := v.parseFleetFields(doc, r); err != nil {
		return nil, err
	}
	if tags, ok, err := doc.Strings("tags"); err != nil {
		return nil, err
	} else if ok {
		r.Tags = tags
	}
	return r, nil
}

func (v *Vault) parseStatus(doc *frontmatter.Doc, r *Record) error {
	allowed := StatusesFor(r.Kind)
	raw, ok, err := doc.String("status")
	if err != nil {
		return err
	}
	if !ok {
		if allowed != nil {
			return doc.Errorf("type", "a %s must have a status; valid values are %s", r.Kind, strings.Join(allowed, ", "))
		}
		return nil
	}
	if allowed == nil {
		return doc.Errorf("status", "a %s must not have a status", r.Kind)
	}
	for _, s := range allowed {
		if raw == s {
			r.Status = raw
			return nil
		}
	}
	return doc.Errorf("status", "unknown status %q for a %s: valid values are %s", raw, r.Kind, strings.Join(allowed, ", "))
}

func (v *Vault) parseIdeaFields(doc *frontmatter.Doc, r *Record) error {
	rawTouched, hasTouched, err := doc.String("touched")
	if err != nil {
		return err
	}
	if hasTouched && r.Kind != KindIdea && r.Kind != KindTask && r.Kind != KindNote && r.Kind != KindDaily {
		return doc.Errorf("touched", "a %s must not have a touched date", r.Kind)
	}
	if hasTouched {
		touched, err := timeref.ParseDateOnly(rawTouched)
		if err != nil {
			return doc.Errorf("touched", "%v", err)
		}
		if touched.Before(r.Created) {
			return doc.Errorf("touched", "touched %s is before created %s", touched, r.Created)
		}
		r.Touched, r.HasTouched = touched, true
	} else if r.Kind == KindIdea {
		return doc.Errorf("type", "an idea must have a touched date; it is what its age is measured from")
	} else if r.Kind == KindTask {
		return doc.Errorf("type", "a task must have a touched date; it is what its follow-up horizon is measured from")
	}

	rawNudge, hasNudge, err := doc.String("nudge_after")
	if err != nil {
		return err
	}
	if hasNudge && r.Kind != KindIdea {
		return doc.Errorf("nudge_after", "a %s must not have a nudge_after date", r.Kind)
	}
	if hasNudge {
		span, err := timeref.ParseSpan(rawNudge)
		if err != nil {
			return doc.Errorf("nudge_after", "%v", err)
		}
		if span.ApproxDays() <= 0 {
			return doc.Errorf("nudge_after", "nudge_after %q must be at least one day", rawNudge)
		}
		r.NudgeAfter, r.HasNudge = span, true
	}
	return nil
}

func (v *Vault) parseEventFields(doc *frontmatter.Doc, r *Record) error {
	rawWhen, hasWhen, err := doc.String("when")
	if err != nil {
		return err
	}
	if hasWhen && r.Kind != KindEvent {
		return doc.Errorf("when", "a %s must not have a when: timestamp", r.Kind)
	}
	if hasWhen {
		when, err := timeref.ParseStored(rawWhen)
		if err != nil {
			return doc.Errorf("when", "%v", err)
		}
		r.When, r.HasWhen = when.In(v.Zone.Loc), true
	} else if r.Kind == KindEvent {
		return doc.Errorf("type", "an event must have a when: timestamp with an explicit UTC offset")
	}

	rawDuration, hasDuration, err := doc.String("duration")
	if err != nil {
		return err
	}
	if hasDuration && r.Kind != KindEvent {
		return doc.Errorf("duration", "a %s must not have a duration", r.Kind)
	}
	if hasDuration {
		span, err := timeref.ParseSpan(rawDuration)
		if err != nil {
			return doc.Errorf("duration", "%v", err)
		}
		if span.Days < 0 || span.Clock < 0 {
			return doc.Errorf("duration", "duration %q must not be negative", rawDuration)
		}
		r.Duration = span
	}

	rawWith, hasWith, err := doc.Strings("with")
	if err != nil {
		return err
	}
	if hasWith && r.Kind != KindEvent {
		return doc.Errorf("with", "a %s must not have a with: list", r.Kind)
	}
	if hasWith {
		for _, w := range rawWith {
			if err := ValidateID(w); err != nil {
				return doc.Errorf("with", "%v", err)
			}
		}
		r.With = rawWith
	}

	rawRRule, hasRRule, err := doc.String("rrule")
	if err != nil {
		return err
	}
	if hasRRule && r.Kind != KindEvent {
		return doc.Errorf("rrule", "a %s must not have an rrule", r.Kind)
	}
	if hasRRule {
		if !r.HasWhen {
			return doc.Errorf("rrule", "rrule needs a when: to recur from")
		}
		if err := ValidateRRule(rawRRule); err != nil {
			return doc.Errorf("rrule", "%v", err)
		}
		r.RRule = rawRRule
	}

	rawExceptions, hasExceptions, err := doc.Strings("exceptions")
	if err != nil {
		return err
	}
	if hasExceptions && r.Kind != KindEvent {
		return doc.Errorf("exceptions", "a %s must not have exceptions", r.Kind)
	}
	if hasExceptions {
		if !hasRRule {
			return doc.Errorf("exceptions", "exceptions without an rrule: there is no series to except from")
		}
		for _, e := range rawExceptions {
			d, err := timeref.ParseDateOnly(e)
			if err != nil {
				return doc.Errorf("exceptions", "%v", err)
			}
			r.Exceptions = append(r.Exceptions, d)
		}
	}
	return nil
}

// parseTaskFields validates the keys only a task may carry. A task is the
// user's own commitment to remember to check something; it is never a
// delivery work item, and brain-axi has no write path to any backlog.
func (v *Vault) parseTaskFields(doc *frontmatter.Doc, r *Record) error {
	rawDue, hasDue, err := doc.String("due")
	if err != nil {
		return err
	}
	if hasDue && r.Kind != KindTask {
		return doc.Errorf("due", "a %s must not have a due: timestamp", r.Kind)
	}
	if hasDue {
		due, err := timeref.ParseStored(rawDue)
		if err != nil {
			return doc.Errorf("due", "%v", err)
		}
		r.Due, r.HasDue = due.In(v.Zone.Loc), true
	}

	rawAssignee, hasAssignee, err := doc.String("assignee")
	if err != nil {
		return err
	}
	if hasAssignee && r.Kind != KindTask {
		return doc.Errorf("assignee", "a %s must not have an assignee", r.Kind)
	}
	if hasAssignee {
		// An assignee resolves to a people/ record through the same id rules
		// every other reference uses, so a delegated task is a first-class node
		// in the link graph rather than a free-text name.
		if err := ValidateID(rawAssignee); err != nil {
			return doc.Errorf("assignee", "%v", err)
		}
		r.Assignee = rawAssignee
	}

	rawFollow, hasFollow, err := doc.String("follow_up_after")
	if err != nil {
		return err
	}
	if hasFollow && r.Kind != KindTask {
		return doc.Errorf("follow_up_after", "a %s must not have a follow_up_after", r.Kind)
	}
	if hasFollow {
		span, err := timeref.ParseSpan(rawFollow)
		if err != nil {
			return doc.Errorf("follow_up_after", "%v", err)
		}
		if span.ApproxDays() <= 0 {
			return doc.Errorf("follow_up_after", "follow_up_after %q must be at least one day", rawFollow)
		}
		r.FollowUpAfter, r.HasFollowUp = span, true
	}
	return nil
}

// parseForgeFields validates the cached pull-request status any record may
// carry. The cache is derived data living in the source of truth, which is only
// acceptable while it stays visible and hand-editable - so every key is parsed
// and validated exactly like a key the user typed, and a cache that does not
// make sense is a corrupt record rather than something to ignore.
func (v *Vault) parseForgeFields(doc *frontmatter.Doc, r *Record) error {
	rawURL, hasURL, err := doc.String("forge_url")
	if err != nil {
		return err
	}
	if hasURL {
		if _, err := forge.Detect(rawURL); err != nil {
			return doc.Errorf("forge_url", "%v", err)
		}
		r.Forge.URL, r.HasForge = rawURL, true
	}

	rawState, hasState, err := doc.String("forge_state")
	if err != nil {
		return err
	}
	rawChecks, hasChecks, err := doc.String("forge_checks")
	if err != nil {
		return err
	}
	rawCheckedAt, hasCheckedAt, err := doc.String("forge_checked_at")
	if err != nil {
		return err
	}
	for key, present := range map[string]bool{
		"forge_state": hasState, "forge_checks": hasChecks, "forge_checked_at": hasCheckedAt,
	} {
		if present && !hasURL {
			return doc.Errorf(key, "%s without a forge_url: there is nothing this status belongs to", key)
		}
	}
	if !hasState && !hasChecks && !hasCheckedAt {
		return nil
	}
	// All three or none. A state without its timestamp is exactly the shape
	// that lets a stale answer be read as a live one.
	if !hasState || !hasChecks || !hasCheckedAt {
		return doc.Errorf("forge_url", "a cached forge status needs forge_state, forge_checks and forge_checked_at together; a status without the time it was read cannot be told apart from a live one")
	}
	if !allowed(forge.States, rawState) {
		return doc.Errorf("forge_state", "unknown forge_state %q: valid values are %s", rawState, strings.Join(forge.States, ", "))
	}
	if !allowed(forge.CheckStates, rawChecks) {
		return doc.Errorf("forge_checks", "unknown forge_checks %q: valid values are %s", rawChecks, strings.Join(forge.CheckStates, ", "))
	}
	checkedAt, err := timeref.ParseStored(rawCheckedAt)
	if err != nil {
		return doc.Errorf("forge_checked_at", "%v", err)
	}
	r.Forge.State, r.Forge.Checks = rawState, rawChecks
	r.Forge.CheckedAt, r.Forge.HasStatus = checkedAt.In(v.Zone.Loc), true
	return nil
}

// parseLinkFields validates the linking layer and the person-agenda keys.
//
// A link naming an id no record claims is reported by doctor with path:line,
// never rejected here: that is the same precedent `with:` and `assignee` set,
// and it is what lets a link be written before its target is. A
// duplicate or self-referential entry is different in kind - it states nothing
// that could ever become true - so it is a corrupt record.
func (v *Vault) parseLinkFields(doc *frontmatter.Doc, r *Record) error {
	rawLinks, hasLinks, err := doc.Strings("links")
	if err != nil {
		return err
	}
	if hasLinks {
		seen := map[string]bool{}
		for _, id := range rawLinks {
			if err := ValidateID(id); err != nil {
				return doc.Errorf("links", "%v", err)
			}
			if id == r.ID {
				return doc.Errorf("links", "links names this record's own id %q; a record cannot link to itself", id)
			}
			if seen[id] {
				return doc.Errorf("links", "links names %q twice", id)
			}
			seen[id] = true
		}
		r.Links = rawLinks
	}

	rawRaise, hasRaise, err := doc.Strings("raise_with")
	if err != nil {
		return err
	}
	if hasRaise {
		switch r.Kind {
		case KindPerson:
			return doc.Errorf("raise_with", "a person must not have a raise_with: a profile is who you raise things with, not a thing to raise")
		case KindDaily:
			return doc.Errorf("raise_with", "a daily file must not have a raise_with: it is a whole day, not one item; capture the item as a task instead")
		}
		seen := map[string]bool{}
		for _, id := range rawRaise {
			if err := ValidateID(id); err != nil {
				return doc.Errorf("raise_with", "%v", err)
			}
			if seen[id] {
				return doc.Errorf("raise_with", "raise_with names %q twice", id)
			}
			seen[id] = true
		}
		r.RaiseWith = rawRaise
	}

	rawRaised, hasRaised, err := doc.String("raised")
	if err != nil {
		return err
	}
	if hasRaised {
		if !hasRaise {
			return doc.Errorf("raised", "raised without a raise_with: there is nobody this was raised with")
		}
		raised, err := timeref.ParseDateOnly(rawRaised)
		if err != nil {
			return doc.Errorf("raised", "%v", err)
		}
		if raised.Before(r.Created) {
			return doc.Errorf("raised", "raised %s is before created %s", raised, r.Created)
		}
		r.Raised, r.HasRaised = raised, true
	}
	return nil
}

// fleetTaskRe constrains an external work item's id. It is deliberately wider
// than a vault id - a supervisor names its own work, and PROJ-42 and
// team/repo#12 are both ordinary - and still narrow enough that a shell
// fragment or a sentence is refused rather than written down.
var fleetTaskRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/#-]*$`)

// MaxFleetTaskID is the longest external work-item id this tool will store.
const MaxFleetTaskID = 128

// ValidateFleetTaskID reports why an external work item id is unusable, or nil.
func ValidateFleetTaskID(id string) error {
	if id == "" {
		return fmt.Errorf("a fleet task id must not be empty")
	}
	if len(id) > MaxFleetTaskID {
		return fmt.Errorf("fleet task id %q is %d bytes, over the %d-byte limit", id, len(id), MaxFleetTaskID)
	}
	if !fleetTaskRe.MatchString(id) {
		return fmt.Errorf("fleet task id %q must be letters, digits, dot, underscore, hyphen, slash or hash, starting with a letter or digit", id)
	}
	return nil
}

// parseFleetFields validates the fleet bridge's keys: the external work items a
// record refers to, and the change that shipped it.
//
// Nothing here is ever read back from an external supervisor. These keys are a
// note written outward, which is why brain-axi stays fully usable on a machine
// with no supervisor at all.
func (v *Vault) parseFleetFields(doc *frontmatter.Doc, r *Record) error {
	rawTasks, hasTasks, err := doc.Strings("fleet_tasks")
	if err != nil {
		return err
	}
	if hasTasks {
		seen := map[string]bool{}
		for _, id := range rawTasks {
			if err := ValidateFleetTaskID(id); err != nil {
				return doc.Errorf("fleet_tasks", "%v", err)
			}
			if seen[id] {
				return doc.Errorf("fleet_tasks", "fleet_tasks names %q twice", id)
			}
			seen[id] = true
		}
		r.FleetTasks = rawTasks
	}

	rawShippedAt, hasShippedAt, err := doc.String("shipped_at")
	if err != nil {
		return err
	}
	rawShippedPR, hasShippedPR, err := doc.String("shipped_pr")
	if err != nil {
		return err
	}
	if !hasShippedAt && !hasShippedPR {
		return nil
	}
	if !ShipsAsAKind(r.Kind) {
		key := "shipped_at"
		if !hasShippedAt {
			key = "shipped_pr"
		}
		return doc.Errorf(key, "a %s must not have a %s; only %s ship", r.Kind, key, joinKinds(ShippableKinds))
	}
	// A merge reference with no merge time is the same failure shape as a
	// cached forge state with no timestamp: it reads as a fact about now.
	if !hasShippedAt {
		return doc.Errorf("shipped_pr", "shipped_pr without a shipped_at: a merge reference with no merge time cannot be placed in any period")
	}
	shippedAt, err := timeref.ParseStored(rawShippedAt)
	if err != nil {
		return doc.Errorf("shipped_at", "%v", err)
	}
	r.ShippedAt, r.HasShipped = shippedAt.In(v.Zone.Loc), true
	if hasShippedPR {
		if _, err := forge.Detect(rawShippedPR); err != nil {
			return doc.Errorf("shipped_pr", "%v", err)
		}
		r.ShippedPR = rawShippedPR
	}
	return nil
}

// ShippableKinds are the kinds that can carry a ship record. An identity and a
// day-file do not ship, and an event happens rather than lands.
var ShippableKinds = []Kind{KindIdea, KindTask, KindNote}

// ShipsAsAKind reports whether this kind can carry shipped_at and shipped_pr.
func ShipsAsAKind(k Kind) bool {
	for _, s := range ShippableKinds {
		if s == k {
			return true
		}
	}
	return false
}

// IsClosed reports whether a record's status means it has stopped needing
// attention. A kind with no status is never closed: there is nothing to close.
func IsClosed(k Kind, status string) bool {
	switch k {
	case KindEvent:
		return status == "done" || status == "cancelled"
	case KindIdea:
		return status == "shipped" || status == "dropped"
	case KindTask:
		return !TaskIsOpen(status)
	default:
		return false
	}
}

func joinKinds(ks []Kind) string {
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}

// End is when an event finishes. For a point event it equals When.
func (v *Vault) End(r *Record) time.Time {
	if !r.HasWhen {
		return time.Time{}
	}
	if r.Duration.IsZero() {
		return r.When
	}
	return v.Zone.Add(r.When, r.Duration)
}

// Horizon is the decay horizon that applies to a record: its own if it sets
// one, otherwise the vault default.
//
// An idea spells its horizon nudge_after: and a task spells it
// follow_up_after:, because they mean different things to the user - one is
// "poke me about this thought", the other is "remember to check whether this
// actually happened". They are the same mechanism, so they resolve through the
// same function rather than through two that can drift.
func (v *Vault) Horizon(r *Record) timeref.Span {
	if r.Kind == KindTask {
		if r.HasFollowUp {
			return r.FollowUpAfter
		}
		return v.FollowUpAfter
	}
	if r.HasNudge {
		return r.NudgeAfter
	}
	return v.NudgeAfter
}

// Dormant reports whether a record has gone untouched past the vault's
// dormancy window. It is a longer, blunter question than PastHorizon: the
// horizon asks "is it time to poke me about this", dormancy asks "has this
// stopped".
func (v *Vault) Dormant(r *Record, now time.Time) bool {
	return v.AgeDays(r, now) > v.DormantAfter.ApproxDays()
}

// WaitingToBeRaised reports whether this record is still on somebody's agenda:
// it names people to raise it with, nobody has recorded raising it, and it has
// not closed.
func (r *Record) WaitingToBeRaised() bool {
	return len(r.RaiseWith) > 0 && !r.HasRaised && !IsClosed(r.Kind, r.Status)
}

// AgeDays is how many calendar days ago a record was last touched, falling back
// to its creation date. Age is the signal a user cannot produce from
// memory, so it is computed for every kind.
func (v *Vault) AgeDays(r *Record, now time.Time) int {
	from := r.Created
	if r.HasTouched {
		from = r.Touched
	}
	return timeref.DateDiff(from, v.Zone.DateOf(now))
}

// PastHorizon reports whether a record has sat untouched beyond its nudge
// horizon.
func (v *Vault) PastHorizon(r *Record, now time.Time) bool {
	return v.AgeDays(r, now) > v.Horizon(r).ApproxDays()
}

// SortRecords orders records the way output shows them: events by start
// instant, everything else oldest-touched first, ties broken by id so output
// never depends on filesystem order.
func (v *Vault) SortRecords(rs []*Record) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.HasWhen && b.HasWhen && !a.When.Equal(b.When) {
			return a.When.Before(b.When)
		}
		if a.HasWhen != b.HasWhen {
			return a.HasWhen
		}
		ad, bd := a.Created, b.Created
		if a.HasTouched {
			ad = a.Touched
		}
		if b.HasTouched {
			bd = b.Touched
		}
		if ad != bd {
			return ad.Before(bd)
		}
		return a.ID < b.ID
	})
}
