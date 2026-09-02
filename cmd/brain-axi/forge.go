package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Thanhbinh1905/secondbrain/internal/forge"
	"github.com/Thanhbinh1905/secondbrain/internal/render"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// runner is the forge CLI runner this invocation uses. Production is the real
// one; a test replaces it so the suite never needs a network or a token.
var runner forge.Runner = forge.Exec

// cmdLink attaches a forge URL to a record.
//
// Attaching is offline: it validates the URL's shape and writes it, and reaches
// nothing. --refresh is what asks the forge, and it is opt-in here for the same
// reason it is opt-in everywhere else.
func (a *app) cmdLink() error {
	if len(a.args) == 0 {
		return usageError("link needs a record id and a forge URL, for example: brain-axi link my-task https://github.com/owner/repo/pull/12")
	}
	// "fleet" is a reserved first argument. A record whose id is literally
	// "fleet" cannot be given a forge URL through this command, which is a
	// price worth paying for a subcommand that cannot be confused with a URL.
	if a.args[0] == fleetWord {
		return a.cmdLinkFleet()
	}
	if err := a.requireArgs(2, "link"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	r, err := a.vault.Find(a.args[0])
	if err != nil {
		return err
	}
	ref, err := forge.Detect(a.args[1])
	if err != nil {
		return usageError("%v", err)
	}
	if r.HasForge && r.Forge.URL != ref.URL && !a.has("force") {
		return usageError("%s is already linked to %s; pass --force to replace it", r.ID, r.Forge.URL)
	}

	changes := [][2]string{{"forge_url", ref.URL}}
	// A new link starts with no cached status rather than inheriting the old
	// link's: a status that belonged to a different pull request is worse than
	// none at all.
	var cleared []string
	if r.HasForge && r.Forge.URL != ref.URL && r.Forge.HasStatus {
		cleared = []string{"forge_state", "forge_checks", "forge_checked_at"}
	}

	// A --refresh that cannot reach the forge must not cost the user the
	// link. Attaching is offline and the URL is valid; only the status read
	// failed, so the link is stored and the failure is reported with a non-zero
	// exit. A self-hosted forge may be unreachable from outside its network,
	// and refusing the link there would make the feature unusable exactly where
	// it is needed.
	var status *forge.Status
	var refreshErr error
	if a.has("refresh") {
		fetched, ferr := forge.Fetch(runner, ref, a.now)
		if ferr != nil {
			refreshErr = ferr
		} else {
			status = &fetched
			changes = append(changes,
				[2]string{"forge_state", fetched.State},
				[2]string{"forge_checks", fetched.Checks},
				[2]string{"forge_checked_at", timeref.Format(fetched.CheckedAt)},
			)
			cleared = nil
		}
	}

	doc := r.Doc()
	for _, c := range changes {
		doc.Set(c[0], c[1])
	}
	for _, key := range cleared {
		doc.Delete(key)
	}
	data, err := doc.Bytes()
	if err != nil {
		return err
	}
	if _, err := a.vault.ParseRecord(r.Path, r.Rel, data); err != nil {
		return dataError("the link would make %s invalid: %v", r.Rel, err)
	}
	if err := a.vault.WriteFile(r.Rel, data); err != nil {
		return err
	}

	if a.out.JSON {
		obj := map[string]any{
			"id": r.ID, "path": r.Rel, "forge": string(ref.Kind),
			"host": ref.Host, "project": ref.Project, "number": ref.Number, "url": ref.URL,
		}
		if status != nil {
			obj["state"] = status.State
			obj["checks"] = status.Checks
			obj["checked_at"] = timeref.Format(status.CheckedAt)
		}
		if refreshErr != nil {
			obj["error"] = forgeFailure(refreshErr)
		}
		if err := a.out.Emit(obj); err != nil {
			return err
		}
		if refreshErr != nil {
			return forgeError(ref, refreshErr)
		}
		return nil
	}
	a.out.Scalar("linked", r.ID)
	a.out.Scalar("path", r.Rel)
	a.out.Scalar("url", ref.URL)
	a.out.Scalar("forge", fmt.Sprintf("%s on %s", ref.Kind, ref.Host))
	if status != nil {
		a.out.Scalar("state", status.State)
		a.out.Scalar("checks", status.Checks)
		a.out.Scalar("checked_at", timeref.Format(status.CheckedAt))
	} else {
		a.out.Scalar("state", "never checked")
	}
	var attention []string
	if len(cleared) > 0 {
		attention = append(attention, "the previous link's cached status was dropped; it described a different change")
	}
	if refreshErr != nil {
		attention = append(attention, fmt.Sprintf("the link was stored, but its status could not be read: %s", forgeFailure(refreshErr)))
	}
	a.out.Attention(attention)
	a.out.Help([]string{
		fmt.Sprintf("Run `brain-axi pr %s --refresh` to read its status", r.ID),
		"Status is only ever read when you ask; today, week and the dashboard never reach a forge",
	})
	if refreshErr != nil {
		return forgeError(ref, refreshErr)
	}
	return nil
}

// fleetWord is link's reserved subcommand for the external-supervisor bridge.
const fleetWord = "fleet"

// cmdLinkFleet records a reference to an external supervisor's work item.
//
// This is one direction only, and deliberately so. brain-axi never reads a
// supervisor's state: it holds no endpoint, no token and no idea of what a
// work item is beyond an id it was handed, so the tool stays exactly as usable
// on a machine with no supervisor at all.
func (a *app) cmdLinkFleet() error {
	if len(a.args) != 2 {
		return usageError("link fleet needs a record id, for example: brain-axi link fleet my-task --task PROJ-42")
	}
	task := strings.TrimSpace(a.flagOr("task", ""))
	if task == "" {
		return usageError("link fleet needs --task <external-id>")
	}
	if err := vault.ValidateFleetTaskID(task); err != nil {
		return usageError("flag --task: %v", err)
	}
	if err := a.openVault(); err != nil {
		return err
	}
	r, err := a.vault.Find(a.args[1])
	if err != nil {
		return err
	}
	if contains(r.FleetTasks, task) {
		return usageError("%s already refers to fleet task %q; ids are recorded once", r.ID, task)
	}
	tasks := append(append([]string{}, r.FleetTasks...), task)
	doc := r.Doc()
	doc.SetStrings("fleet_tasks", tasks)
	data, err := doc.Bytes()
	if err != nil {
		return err
	}
	if _, err := a.vault.ParseRecord(r.Path, r.Rel, data); err != nil {
		return dataError("the fleet reference would make %s invalid: %v", r.Rel, err)
	}
	if err := a.vault.WriteFile(r.Rel, data); err != nil {
		return err
	}
	if a.out.JSON {
		return a.out.Emit(map[string]any{
			"id": r.ID, "path": r.Rel, "task": task, "fleet_tasks": tasks,
		})
	}
	a.out.Scalar("linked", r.ID)
	a.out.Scalar("path", r.Rel)
	a.out.Scalar("task", task)
	a.out.Scalar("fleet_tasks", strings.Join(tasks, " "))
	a.out.Help([]string{
		"This reference is written and never read back; brain-axi holds no external state",
		fmt.Sprintf("Run `brain-axi show %s` to see it on the record", r.ID),
	})
	return nil
}

// cmdShip records that a record's work landed.
//
// It is a pure local write. The merge time is supplied by the caller and must
// carry an explicit UTC offset, because it is the one date in the vault that
// says when something actually shipped, and every period report counts from it.
func (a *app) cmdShip() error {
	if err := a.requireArgs(1, "ship"); err != nil {
		return err
	}
	rawPR := strings.TrimSpace(a.flagOr("pr", ""))
	if rawPR == "" {
		return usageError("ship needs --pr <url>, the change that landed")
	}
	ref, err := forge.Detect(rawPR)
	if err != nil {
		return usageError("flag --pr: %v", err)
	}
	rawMerged := strings.TrimSpace(a.flagOr("merged-at", ""))
	if rawMerged == "" {
		return usageError("ship needs --merged-at <timestamp with an explicit UTC offset>")
	}
	// A merge time without an offset could silently place work in the wrong
	// period, so the input must carry one. The instant is then stored in the
	// vault zone like every other vault timestamp.
	mergedAt, err := timeref.ParseStored(rawMerged)
	if err != nil {
		return usageError("flag --merged-at: %v", err)
	}
	if err := a.openVault(); err != nil {
		return err
	}
	r, err := a.vault.Find(a.args[0])
	if err != nil {
		return err
	}
	if !vault.ShipsAsAKind(r.Kind) {
		return usageError("%s cannot ship; only %s can", article(string(r.Kind)), joinKindNames(vault.ShippableKinds))
	}
	if r.HasShipped && !a.has("force") {
		return usageError("%s already shipped at %s; pass --force to record a different merge",
			r.ID, timeref.Format(r.ShippedAt))
	}

	changes := [][2]string{
		{"shipped_at", timeref.Format(mergedAt.In(a.vault.Zone.Loc))},
		{"shipped_pr", ref.URL},
	}
	// Shipping is the record moving, so its status moves with it and its
	// touched date moves with the status, exactly as `done` does.
	if status := shippedStatus(r.Kind); status != "" && r.Status != status {
		changes = append(changes, [2]string{"status", status})
		if r.HasTouched {
			changes = append(changes, [2]string{"touched", a.vault.Zone.DateOf(a.now).String()})
		}
	}
	return a.applyChanges(r, changes)
}

// shippedStatus is what a kind's status becomes when its work lands. A note
// carries no status, so nothing moves.
func shippedStatus(k vault.Kind) string {
	switch k {
	case vault.KindIdea:
		return "shipped"
	case vault.KindTask:
		return "done"
	default:
		return ""
	}
}

// article prefixes a word with the indefinite article that reads right.
func article(word string) string {
	if word == "" {
		return word
	}
	switch word[0] {
	case 'a', 'e', 'i', 'o', 'u':
		return "an " + word
	default:
		return "a " + word
	}
}

func joinKindNames(ks []vault.Kind) string {
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}

// prRow is one linked record's status in JSON.
type prRow struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Forge     string `json:"forge"`
	Host      string `json:"host"`
	State     string `json:"state"`
	Checks    string `json:"checks"`
	CheckedAt string `json:"checked_at,omitempty"`
	AgeLabel  string `json:"checked_age,omitempty"`
	// Cached reports whether this row came from the record's frontmatter rather
	// than from the forge just now. It is never omitted: a cached answer that
	// does not say it is cached is the one way this feature can mislead.
	Cached bool   `json:"cached"`
	Error  string `json:"error,omitempty"`
	Path   string `json:"path"`
}

// cmdPR reports the status of linked records, refreshing only when asked.
func (a *app) cmdPR() error {
	if len(a.args) > 1 {
		return usageError("pr takes at most one record id, got %s", strings.Join(a.args, " "))
	}
	if err := a.openVault(); err != nil {
		return err
	}
	records, err := a.vault.Walk()
	if err != nil {
		return err
	}
	var linked []*vault.Record
	if len(a.args) == 1 {
		r, err := a.vault.Find(a.args[0])
		if err != nil {
			return err
		}
		if !r.HasForge {
			return usageError("%s has no forge link; run `brain-axi link %s <url>` first", r.ID, r.ID)
		}
		linked = []*vault.Record{r}
	} else {
		for _, r := range records {
			if r.HasForge {
				linked = append(linked, r)
			}
		}
	}

	refresh := a.has("refresh")
	rows := make([]prRow, 0, len(linked))
	var attention []string
	var failures int
	for _, r := range linked {
		row := prRow{ID: r.ID, Kind: string(r.Kind), Title: r.Title, URL: r.Forge.URL, Path: r.Rel}
		ref, err := forge.Detect(r.Forge.URL)
		if err != nil {
			// ParseRecord already rejected an unresolvable URL, so reaching here
			// means the record changed under us. Report it rather than guess.
			row.Error = err.Error()
			row.State, row.Checks = "unreadable", "unreadable"
			rows = append(rows, row)
			attention = append(attention, fmt.Sprintf("%s: %v", r.ID, err))
			failures++
			continue
		}
		row.Forge, row.Host = string(ref.Kind), ref.Host

		if refresh {
			status, ferr := forge.Fetch(runner, ref, a.now)
			if ferr != nil {
				row.Error = forgeFailure(ferr)
				failures++
				// A refresh that failed falls back to the cache only out loud:
				// the row still says it is cached and how old it is.
				if r.Forge.HasStatus {
					fillCached(&row, r, a)
					attention = append(attention, fmt.Sprintf("%s: %s; showing the cached status from %s (%s)",
						r.ID, row.Error, timeref.Format(r.Forge.CheckedAt), checkedAge(a, r)))
				} else {
					row.State, row.Checks = "unknown", "unknown"
					attention = append(attention, fmt.Sprintf("%s: %s; this link has never been checked, so there is nothing cached to show", r.ID, row.Error))
				}
				rows = append(rows, row)
				continue
			}
			if err := a.storeStatus(r, status); err != nil {
				return err
			}
			row.State, row.Checks = status.State, status.Checks
			row.CheckedAt = timeref.Format(status.CheckedAt)
			row.AgeLabel = "just now"
			row.Cached = false
			if status.Title != "" {
				row.Title = status.Title
			}
			rows = append(rows, row)
			continue
		}

		if !r.Forge.HasStatus {
			row.State, row.Checks = "never checked", "never checked"
			row.Cached = true
			attention = append(attention, fmt.Sprintf("%s has never been checked; run `brain-axi pr %s --refresh`", r.ID, r.ID))
			rows = append(rows, row)
			continue
		}
		fillCached(&row, r, a)
		rows = append(rows, row)
	}

	if a.out.JSON {
		if err := a.out.Emit(map[string]any{
			"now": timeref.Format(a.now), "refreshed": refresh, "pull_requests": rows,
		}); err != nil {
			return err
		}
		return prFailure(failures, len(rows))
	}
	block := render.Block{
		Name:    "pull_requests",
		Columns: render.Cols([]string{"id", "state", "checks", "checked", "url"}),
		Empty:   "no record has a forge link; run `brain-axi link <id> <url>` to add one",
	}
	for _, row := range rows {
		checked := row.AgeLabel
		if row.CheckedAt != "" {
			checked = row.CheckedAt + " (" + row.AgeLabel + ")"
		}
		if checked == "" {
			checked = "never"
		}
		block.Rows = append(block.Rows, []string{row.ID, row.State, row.Checks, checked, row.URL})
	}
	a.out.Block(block)
	if !refresh && len(rows) > 0 {
		// Every row above came out of a file. Saying so once, every time, is
		// what keeps a cached answer from being read as a live one.
		attention = append(attention, "every status above is cached, as of the time in its checked column; pass --refresh to read the forges now")
	}
	a.out.Attention(attention)
	a.out.Help([]string{
		"Run `brain-axi pr --refresh` to read every linked forge now",
		"Run `brain-axi doctor` to check which forges this machine can reach",
	})
	return prFailure(failures, len(rows))
}

// prFailure turns unreadable links into a non-zero exit. A forge this machine
// cannot reach is a real failure, and a command that exits zero on it teaches
// the user to trust an answer that was never obtained.
func prFailure(failures, total int) error {
	if failures == 0 {
		return nil
	}
	return usageError("%d of %d linked record(s) could not be read from their forge; the reason for each is in the attention block above",
		failures, total)
}

// fillCached puts a record's stored status onto a row, always labelled as
// cached and always carrying the time it was read.
func fillCached(row *prRow, r *vault.Record, a *app) {
	row.State, row.Checks = r.Forge.State, r.Forge.Checks
	row.CheckedAt = timeref.Format(r.Forge.CheckedAt)
	row.AgeLabel = checkedAge(a, r)
	row.Cached = true
}

// checkedAge says how long ago a cached status was read, in the coarsest unit
// that is still honest.
func checkedAge(a *app, r *vault.Record) string {
	d := a.now.Sub(r.Forge.CheckedAt)
	switch {
	case d < 0:
		return "checked in the future; the clock or the record is wrong"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return strconv.Itoa(timeref.DateDiff(a.vault.Zone.DateOf(r.Forge.CheckedAt), a.vault.Zone.DateOf(a.now))) + "d ago"
	}
}

// storeStatus writes a fresh status back into the record's own frontmatter.
func (a *app) storeStatus(r *vault.Record, status forge.Status) error {
	doc := r.Doc()
	doc.Set("forge_state", status.State)
	doc.Set("forge_checks", status.Checks)
	doc.Set("forge_checked_at", timeref.Format(status.CheckedAt))
	data, err := doc.Bytes()
	if err != nil {
		return err
	}
	if _, err := a.vault.ParseRecord(r.Path, r.Rel, data); err != nil {
		return dataError("the refreshed status would make %s invalid: %v", r.Rel, err)
	}
	return a.vault.WriteFile(r.Rel, data)
}

// forgeError turns a forge failure into a usage error naming the concrete
// missing requirement. An unknown status is never rendered as fine.
func forgeError(ref forge.Ref, err error) error {
	return usageError("%s could not be read: %s", ref.URL, forgeFailure(err))
}

func forgeFailure(err error) string {
	return err.Error()
}
