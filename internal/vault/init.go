package vault

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/frontmatter"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/unitext"
)

// vaultGitignore keeps editor and OS noise out of the vault's own history
// without ever ignoring a record.
const vaultGitignore = `# The vault tracks Markdown and nothing else.
.DS_Store
.obsidian/workspace*
.obsidian/cache
*.tmp-*
`

const readmeBody = `# vault

Your second brain. Markdown is the only source of truth here: no database, no cache and
no index. Every file is readable and editable with a text editor or Obsidian, with or without
` + "`brain-axi`" + ` installed.

- ` + "`events/`" + ` dated commitments. ` + "`when:`" + ` always carries an explicit UTC offset.
- ` + "`ideas/`" + ` half-formed things, with a ` + "`touched:`" + ` date so their age is visible.
- ` + "`tasks/`" + ` commitments to follow up on, with an optional ` + "`assignee:`" + ` and ` + "`due:`" + `.
- ` + "`notes/`" + ` standalone notes.
- ` + "`people/`" + ` who the events are with.
- ` + "`daily/`" + ` one file per day; ` + "`brain-axi add note`" + ` appends here.
- ` + "`.brain/config.yml`" + ` timezone and first day of the week.

This directory is its own git repository, and is invisible to the tool's repository. An upgrade of
` + "`brain-axi`" + ` can never touch it.
`

// InitResult reports what Init actually did, so the CLI can tell the truth
// about a vault that was already partly there.
type InitResult struct {
	Root        string
	Created     []string
	GitInited   bool
	GitSkipped  string
	AlreadyHere bool
}

// Init creates the vault skeleton, its config, its .gitignore, and a git
// repository inside it (FR-1).
//
// It refuses to touch an existing config: re-running init must never rewrite
// the user's timezone.
func Init(root string, cfg Config, runGit bool) (*InitResult, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := timeref.LoadZone(cfg.Timezone, cfg.WeekStarts); err != nil {
		return nil, err
	}
	if _, err := timeref.ParseSpan(cfg.NudgeAfter); err != nil {
		return nil, fmt.Errorf("nudge_after: %w", err)
	}

	res := &InitResult{Root: abs}
	configPath := filepath.Join(abs, BrainDir, ConfigName)
	if _, err := os.Stat(configPath); err == nil {
		res.AlreadyHere = true
	}

	dirs := append([]string{BrainDir}, RecordDirs...)
	for _, d := range dirs {
		p := filepath.Join(abs, d)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", d, err)
		}
		res.Created = append(res.Created, d+"/")
	}

	files := []struct {
		rel  string
		data []byte
	}{
		{filepath.Join(BrainDir, ConfigName), cfg.Marshal()},
		{".gitignore", []byte(vaultGitignore)},
		{"README.md", []byte(readmeBody)},
	}
	for _, f := range files {
		p := filepath.Join(abs, f.rel)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.WriteFile(p, f.data, 0o644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", f.rel, err)
		}
		res.Created = append(res.Created, f.rel)
	}

	if runGit {
		switch {
		case isGitRepo(abs):
			res.GitSkipped = "already a git repository"
		default:
			if _, err := exec.LookPath("git"); err != nil {
				res.GitSkipped = "git is not on PATH"
			} else if out, err := exec.Command("git", "-C", abs, "init", "--quiet").CombinedOutput(); err != nil {
				return nil, fmt.Errorf("git init in %s failed: %v: %s", abs, err, strings.TrimSpace(string(out)))
			} else {
				res.GitInited = true
			}
		}
	} else {
		res.GitSkipped = "--no-git"
	}
	return res, nil
}

func isGitRepo(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (st.IsDir() || st.Mode().IsRegular())
}

// NewEvent builds an event record's document. The caller has already resolved
// when: to an absolute instant; this function never interprets a phrase.
type NewEvent struct {
	ID         string
	Title      string
	When       time.Time
	Duration   timeref.Span
	With       []string
	Status     string
	RRule      string
	Exceptions []timeref.Date
	Body       string
	Created    timeref.Date
	// Links are ids of other records this one points at, and RaiseWith names
	// the people it is waiting to be raised with.
	Links     []string
	RaiseWith []string
}

// BuildEvent renders a new event into a path and a document.
func (v *Vault) BuildEvent(e NewEvent) (string, *frontmatter.Doc, error) {
	if err := ValidateID(e.ID); err != nil {
		return "", nil, err
	}
	status := e.Status
	if status == "" {
		status = "scheduled"
	}
	if !allowed(EventStatuses, status) {
		return "", nil, fmt.Errorf("unknown status %q for an event: valid values are %s", status, strings.Join(EventStatuses, ", "))
	}
	pairs := [][2]string{
		{"type", string(KindEvent)},
		{"id", e.ID},
		{"title", e.Title},
		{"when", timeref.Format(e.When)},
	}
	if !e.Duration.IsZero() {
		pairs = append(pairs, [2]string{"duration", e.Duration.String()})
	}
	pairs = append(pairs, [2]string{"status", status}, [2]string{"created", e.Created.String()})
	if e.RRule != "" {
		if err := ValidateRRule(e.RRule); err != nil {
			return "", nil, err
		}
	}

	name := fmt.Sprintf("%s-%s.md", timeref.FormatDate(e.When), unitext.SlugN(e.Title, 60))
	rel, err := v.FreePath(filepath.Join(EventsDir, name))
	if err != nil {
		return "", nil, err
	}
	doc := frontmatter.New(rel, bodyOf(e.Body), pairs...)
	if len(e.With) > 0 {
		doc.SetStrings("with", e.With)
	}
	if e.RRule != "" {
		doc.Set("rrule", e.RRule)
	}
	if len(e.Exceptions) > 0 {
		doc.SetStrings("exceptions", datesToStrings(e.Exceptions))
	}
	if err := setLinkKeys(doc, KindEvent, e.ID, e.Links, e.RaiseWith); err != nil {
		return "", nil, err
	}
	return rel, doc, nil
}

// NewIdea is a new idea's fields.
type NewIdea struct {
	ID         string
	Title      string
	Status     string
	NudgeAfter timeref.Span
	HasNudge   bool
	Body       string
	Created    timeref.Date
	Links      []string
	RaiseWith  []string
}

// BuildIdea renders a new idea into a path and a document.
func (v *Vault) BuildIdea(i NewIdea) (string, *frontmatter.Doc, error) {
	if err := ValidateID(i.ID); err != nil {
		return "", nil, err
	}
	status := i.Status
	if status == "" {
		status = "pending"
	}
	if !allowed(IdeaStatuses, status) {
		return "", nil, fmt.Errorf("unknown status %q for an idea: valid values are %s", status, strings.Join(IdeaStatuses, ", "))
	}
	pairs := [][2]string{
		{"type", string(KindIdea)},
		{"id", i.ID},
		{"title", i.Title},
		{"status", status},
		{"created", i.Created.String()},
		{"touched", i.Created.String()},
	}
	rel, err := v.FreePath(filepath.Join(IdeasDir, unitext.SlugN(i.ID, 80)+".md"))
	if err != nil {
		return "", nil, err
	}
	doc := frontmatter.New(rel, bodyOf(i.Body), pairs...)
	if i.HasNudge {
		doc.Set("nudge_after", i.NudgeAfter.String())
	}
	if err := setLinkKeys(doc, KindIdea, i.ID, i.Links, i.RaiseWith); err != nil {
		return "", nil, err
	}
	return rel, doc, nil
}

// NewTask is a new task's fields. A task is something the user has to
// remember to check, which is why FollowUpAfter is the load-bearing one: a
// delegated thing nobody has looked at in three weeks has to become impossible
// to miss.
type NewTask struct {
	ID            string
	Title         string
	Status        string
	Assignee      string
	Due           time.Time
	HasDue        bool
	FollowUpAfter timeref.Span
	HasFollowUp   bool
	Body          string
	Created       timeref.Date
	Links         []string
	RaiseWith     []string
}

// BuildTask renders a new task into a path and a document.
func (v *Vault) BuildTask(t NewTask) (string, *frontmatter.Doc, error) {
	if err := ValidateID(t.ID); err != nil {
		return "", nil, err
	}
	status := t.Status
	if status == "" {
		// A task handed to somebody else starts as waiting rather than open:
		// the user is not the one who has to do it, they are the one who has to
		// check it.
		status = "open"
		if t.Assignee != "" {
			status = "waiting"
		}
	}
	if !allowed(TaskStatuses, status) {
		return "", nil, fmt.Errorf("unknown status %q for a task: valid values are %s", status, strings.Join(TaskStatuses, ", "))
	}
	if t.Assignee != "" {
		if err := ValidateID(t.Assignee); err != nil {
			return "", nil, fmt.Errorf("assignee: %w", err)
		}
	}
	pairs := [][2]string{
		{"type", string(KindTask)},
		{"id", t.ID},
		{"title", t.Title},
		{"status", status},
	}
	if t.Assignee != "" {
		pairs = append(pairs, [2]string{"assignee", t.Assignee})
	}
	if t.HasDue {
		pairs = append(pairs, [2]string{"due", timeref.Format(t.Due)})
	}
	pairs = append(pairs,
		[2]string{"created", t.Created.String()},
		[2]string{"touched", t.Created.String()},
	)
	rel, err := v.FreePath(filepath.Join(TasksDir, unitext.SlugN(t.ID, 80)+".md"))
	if err != nil {
		return "", nil, err
	}
	doc := frontmatter.New(rel, bodyOf(t.Body), pairs...)
	if t.HasFollowUp {
		doc.Set("follow_up_after", t.FollowUpAfter.String())
	}
	if err := setLinkKeys(doc, KindTask, t.ID, t.Links, t.RaiseWith); err != nil {
		return "", nil, err
	}
	return rel, doc, nil
}

// NewNote is a new standalone note's fields.
type NewNote struct {
	ID        string
	Title     string
	Body      string
	Created   timeref.Date
	Tags      []string
	Links     []string
	RaiseWith []string
}

// BuildNote renders a standalone note into a path and a document.
func (v *Vault) BuildNote(n NewNote) (string, *frontmatter.Doc, error) {
	if err := ValidateID(n.ID); err != nil {
		return "", nil, err
	}
	pairs := [][2]string{
		{"type", string(KindNote)},
		{"id", n.ID},
		{"title", n.Title},
		{"created", n.Created.String()},
		{"touched", n.Created.String()},
	}
	rel, err := v.FreePath(filepath.Join(NotesDir, unitext.SlugN(n.ID, 80)+".md"))
	if err != nil {
		return "", nil, err
	}
	doc := frontmatter.New(rel, bodyOf(n.Body), pairs...)
	if len(n.Tags) > 0 {
		doc.SetStrings("tags", n.Tags)
	}
	if err := setLinkKeys(doc, KindNote, n.ID, n.Links, n.RaiseWith); err != nil {
		return "", nil, err
	}
	return rel, doc, nil
}

// NewPerson is a new people-profile's fields.
type NewPerson struct {
	ID      string
	Title   string
	Body    string
	Created timeref.Date
	Links   []string
}

// BuildPerson renders a people profile into a path and a document.
func (v *Vault) BuildPerson(p NewPerson) (string, *frontmatter.Doc, error) {
	if err := ValidateID(p.ID); err != nil {
		return "", nil, err
	}
	rel, err := v.FreePath(filepath.Join(PeopleDir, unitext.SlugN(p.ID, 80)+".md"))
	if err != nil {
		return "", nil, err
	}
	doc := frontmatter.New(rel, bodyOf(p.Body), [][2]string{
		{"type", string(KindPerson)},
		{"id", p.ID},
		{"title", p.Title},
		{"created", p.Created.String()},
	}...)
	if err := setLinkKeys(doc, KindPerson, p.ID, p.Links, nil); err != nil {
		return "", nil, err
	}
	return rel, doc, nil
}

// DailyRel is the vault-relative path of a date's daily file.
func DailyRel(d timeref.Date) string {
	return filepath.Join(DailyDir, d.String()+".md")
}

// DailyID is the stable id of a date's daily file.
func DailyID(d timeref.Date) string { return "daily-" + d.String() }

// AppendNote adds a timestamped bullet to a date's daily file, creating the
// file when it is the first note of the day.
//
// A quick note lands here rather than in its own file so it is never orphaned
// (US-3), and the whole capture is still one file write: no operation in this
// tool touches two files.
func (v *Vault) AppendNote(at time.Time, text string) (rel string, id string, created bool, err error) {
	date := v.Zone.DateOf(at)
	rel = DailyRel(date)
	id = DailyID(date)
	bullet := fmt.Sprintf("- %s %s\n", at.In(v.Zone.Loc).Format("15:04 -07:00"), text)

	abs := filepath.Join(v.Root, rel)
	raw, readErr := os.ReadFile(abs)
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return "", "", false, fmt.Errorf("reading %s: %w", rel, readErr)
		}
		doc := frontmatter.New(rel, "\n"+bullet, [][2]string{
			{"type", string(KindDaily)},
			{"id", id},
			{"title", date.String()},
			{"created", date.String()},
			{"touched", date.String()},
		}...)
		return rel, id, true, v.Save(rel, doc)
	}

	doc, err := frontmatter.Parse(rel, raw)
	if err != nil {
		return "", "", false, err
	}
	// Validate before mutating: appending to a corrupt daily file would bury
	// the corruption under new content.
	if _, err := v.ParseRecord(abs, rel, raw); err != nil {
		return "", "", false, err
	}
	if !strings.HasSuffix(doc.Body, "\n") && doc.Body != "" {
		doc.Body += "\n"
	}
	doc.Body += bullet
	doc.Set("touched", v.Zone.DateOf(at).String())
	return rel, id, false, v.Save(rel, doc)
}

// setLinkKeys writes the linking-layer keys onto a new document, refusing the
// two shapes that can never be true rather than storing them and letting the
// next read fail.
func setLinkKeys(doc *frontmatter.Doc, kind Kind, id string, links, raiseWith []string) error {
	if len(links) > 0 {
		seen := map[string]bool{}
		for _, l := range links {
			if err := ValidateID(l); err != nil {
				return fmt.Errorf("links: %w", err)
			}
			if l == id {
				return fmt.Errorf("links: a record cannot link to itself (%q)", l)
			}
			if seen[l] {
				return fmt.Errorf("links: %q is named twice", l)
			}
			seen[l] = true
		}
		doc.SetStrings("links", links)
	}
	if len(raiseWith) == 0 {
		return nil
	}
	switch kind {
	case KindPerson:
		return fmt.Errorf("raise_with: a person profile is who you raise things with, not a thing to raise")
	case KindDaily:
		return fmt.Errorf("raise_with: a daily file is a whole day, not one item; capture the item as a task instead")
	}
	seen := map[string]bool{}
	for _, w := range raiseWith {
		if err := ValidateID(w); err != nil {
			return fmt.Errorf("raise_with: %w", err)
		}
		if seen[w] {
			return fmt.Errorf("raise_with: %q is named twice", w)
		}
		seen[w] = true
	}
	doc.SetStrings("raise_with", raiseWith)
	return nil
}

func allowed(vocab []string, v string) bool {
	for _, s := range vocab {
		if s == v {
			return true
		}
	}
	return false
}

func bodyOf(body string) string {
	trimmed := strings.TrimRight(body, "\n")
	if trimmed == "" {
		return "\n"
	}
	return "\n" + trimmed + "\n"
}

func datesToStrings(ds []timeref.Date) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.String())
	}
	return out
}
