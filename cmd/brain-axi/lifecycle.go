package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Thanhbinh1905/secondbrain/internal/board"
	"github.com/Thanhbinh1905/secondbrain/internal/forge"
	"github.com/Thanhbinh1905/secondbrain/internal/ics"
	"github.com/Thanhbinh1905/secondbrain/internal/payload"
	"github.com/Thanhbinh1905/secondbrain/internal/query"
	"github.com/Thanhbinh1905/secondbrain/internal/render"
	"github.com/Thanhbinh1905/secondbrain/internal/review"
	"github.com/Thanhbinh1905/secondbrain/internal/skill"
	"github.com/Thanhbinh1905/secondbrain/internal/timeref"
	"github.com/Thanhbinh1905/secondbrain/internal/vault"
	"golang.org/x/term"
)

func (a *app) cmdInit() error {
	if err := a.requireArgs(0, "init"); err != nil {
		return err
	}
	path := a.flagOr("path", a.flagOr("vault", filepath.Join(a.workdir, "vault")))
	cfg := vault.DefaultConfig()
	configPath := filepath.Join(path, vault.BrainDir, vault.ConfigName)
	configExists := false
	if _, err := os.Stat(configPath); err == nil {
		configExists = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking vault config: %w", err)
	}
	// The vault timezone is the one setting with no safe default: it stamps an
	// explicit UTC offset onto every stored record. Named on the command line
	// it is used as given; otherwise it comes from this machine, and a machine
	// that cannot say gets a refusal rather than a guessed offset.
	if a.has("timezone") {
		cfg.Timezone = a.flags["timezone"]
	} else if configExists {
		existing, err := vault.OpenAt(path)
		if err != nil {
			return err
		}
		cfg.Timezone = existing.Config.Timezone
	} else {
		zone, err := timeref.SystemZone()
		if err != nil {
			return err
		}
		cfg.Timezone = zone
	}
	cfg.WeekStarts = a.flagOr("week-starts", cfg.WeekStarts)
	cfg.NudgeAfter = a.flagOr("nudge-after", cfg.NudgeAfter)
	if a.has("backlog-cmd") {
		cfg.BacklogCmd = a.flags["backlog-cmd"]
	}

	res, err := vault.Init(path, cfg, !a.has("no-git"))
	if err != nil {
		return err
	}
	// Opening the vault we just wrote proves it is readable, rather than
	// reporting success on a directory nothing has ever parsed.
	v, err := vault.OpenAt(res.Root)
	if err != nil {
		return err
	}

	if a.out.JSON {
		return a.out.Emit(map[string]any{
			"vault": res.Root, "created": res.Created, "already_present": res.AlreadyHere,
			"git_initialised": res.GitInited, "git_skipped": res.GitSkipped,
			"timezone": v.Config.Timezone, "week_starts": v.Config.WeekStarts,
			"nudge_after": v.Config.NudgeAfter,
		})
	}
	a.out.Scalar("vault", res.Root)
	a.out.Scalar("timezone", v.Config.Timezone)
	a.out.Scalar("week_starts", v.Config.WeekStarts)
	a.out.Scalar("nudge_after", v.Config.NudgeAfter)
	switch {
	case res.GitInited:
		a.out.Scalar("git", "initialised")
	case res.GitSkipped != "":
		a.out.Scalar("git", "not initialised: "+res.GitSkipped)
	}
	block := render.Block{Name: "created", Columns: render.Cols([]string{"path"}), Empty: "nothing new; the vault was already here"}
	for _, c := range res.Created {
		block.Rows = append(block.Rows, []string{c})
	}
	a.out.Block(block)
	var attention []string
	if res.AlreadyHere {
		attention = append(attention, "a config was already here and was left exactly as it was")
	}
	switch remote, err := gitRemote(res.Root); {
	case err != nil && res.GitInited:
		attention = append(attention, fmt.Sprintf("cannot read the new repository's remotes: %v", err))
	case err == nil && remote == "":
		attention = append(attention, "vault has no git remote - a disk failure loses it")
	}
	a.out.Attention(attention)
	a.out.Help([]string{
		"Run `brain-axi setup skill --claude` to teach the agent about it",
		"Run `brain-axi add note \"...\"` to capture the first thing",
	})
	return nil
}

func (a *app) cmdReview() error {
	if err := a.requireArgs(0, "review"); err != nil {
		return err
	}
	if err := a.openVault(); err != nil {
		return err
	}
	stale, hasStale, err := a.spanFlag("stale")
	if err != nil {
		return err
	}
	ideas, err := a.engine().Ideas(query.IdeaFilter{Status: "pending", Stale: stale, HasStale: hasStale})
	if err != nil {
		return err
	}
	// Without an explicit --stale, review shows what has passed its own
	// horizon, which is the thing the horizon is for.
	if !hasStale {
		var pastHorizon []query.IdeaRow
		for _, r := range ideas {
			if r.PastHorizon {
				pastHorizon = append(pastHorizon, r)
			}
		}
		ideas = pastHorizon
	}
	// Unchecked tasks are triaged in the same pass. Splitting them into a
	// second command would mean remembering to run both, and the whole point of
	// a follow-up is not having to remember.
	taskFilter := query.TaskFilter{OnlyOpen: true, PastFollowUp: !hasStale}
	tasks, err := a.engine().Tasks(taskFilter)
	if err != nil {
		return err
	}
	if hasStale {
		var old []query.TaskRow
		for _, r := range tasks {
			if r.AgeDays >= stale.ApproxDays() {
				old = append(old, r)
			}
		}
		tasks = old
	}
	items := append(review.FromIdeas(ideas), review.FromTasks(tasks)...)
	if len(items) == 0 {
		if a.out.JSON {
			return a.out.Emit(map[string]any{"reviewed": 0, "decisions": []any{}, "note": "no idea or task is past its horizon"})
		}
		a.out.Scalar("review", "nothing to triage: no idea or task is past its horizon")
		a.out.Help([]string{"Run `brain-axi ideas` or `brain-axi tasks --all` to see them all anyway"})
		return nil
	}
	if a.out.JSON {
		// The triage screen is a human surface. Asked for machine-readable
		// output, report what would be triaged rather than pretending to run.
		out := make([]ideaRow, 0, len(ideas))
		for _, r := range ideas {
			out = append(out, ideaRow{
				ID: r.Record.ID, Title: r.Record.Title, Status: r.Record.Status,
				AgeDays: r.AgeDays, HorizonDays: r.HorizonDays, PastHorizon: r.PastHorizon,
				Created: r.Record.Created.String(), Touched: r.Record.Touched.String(), Path: r.Record.Rel,
			})
		}
		return a.out.Emit(map[string]any{
			"pending_review": out,
			"pending_tasks":  a.taskRows(tasks),
			"note":           "review is interactive; run it on a terminal, or use `update <id> --status ...` per record",
		})
	}
	if !a.out.TTY || !term.IsTerminal(int(a.env.Stdin.Fd())) {
		return usageError("review is an interactive screen and needs a terminal on both stdin and stdout; run `brain-axi ideas --stale 14d` or `brain-axi review --json` instead")
	}

	restore, err := term.MakeRaw(int(a.env.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("putting the terminal into raw mode: %w", err)
	}
	defer term.Restore(int(a.env.Stdin.Fd()), restore)

	summary, err := review.Run(a.out, a.vault, items, &fileKeys{f: a.env.Stdin}, a.now)
	term.Restore(int(a.env.Stdin.Fd()), restore)
	if err != nil {
		return err
	}
	block := render.Block{Name: "decisions", Columns: render.Cols([]string{"id", "action", "changed"}), Empty: "nothing changed"}
	for _, d := range summary.Decisions {
		var parts []string
		for _, c := range d.Changes {
			parts = append(parts, c.Key+"="+c.Value)
		}
		block.Rows = append(block.Rows, []string{d.ID, string(d.Action), strings.Join(parts, " ")})
	}
	a.out.Scalar("reviewed", fmt.Sprintf("%d of %d", summary.Reviewed, len(items)))
	a.out.Block(block)
	if summary.Quit {
		a.out.Attention([]string{fmt.Sprintf("stopped early; %d record(s) still waiting", len(items)-len(summary.Decisions))})
	}
	a.out.Help([]string{"Run `brain-axi ideas --status pending` and `brain-axi tasks` to see what is left"})
	return nil
}

// fileKeys reads single keystrokes from a terminal in raw mode.
type fileKeys struct{ f *os.File }

func (k *fileKeys) ReadKey() (byte, error) {
	var buf [1]byte
	n, err := k.f.Read(buf[:])
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, fmt.Errorf("no keystroke read")
	}
	return buf[0], nil
}

func (a *app) cmdExport() error {
	if len(a.args) == 0 {
		return usageError("export needs a format: ics")
	}
	if a.args[0] != "ics" {
		return usageError("unknown export format %q: the only format is ics", a.args[0])
	}
	if len(a.args) > 1 {
		return usageError("export ics takes no extra arguments, got %s", strings.Join(a.args[1:], " "))
	}
	if err := a.openVault(); err != nil {
		return err
	}
	records, err := a.vault.Walk()
	if err != nil {
		return err
	}
	from, hasFrom, err := a.dateFlag("from")
	if err != nil {
		return err
	}
	to, hasTo, err := a.dateFlag("to")
	if err != nil {
		return err
	}
	if hasFrom != hasTo {
		return usageError("export ics needs both --from and --to, or neither")
	}
	if hasFrom {
		var kept []*vault.Record
		for _, r := range records {
			// A series is kept whenever it can reach the window; its own
			// recurrence rule decides the occurrences, which is why nothing is
			// expanded here.
			if r.RRule != "" {
				if !a.vault.Zone.DateOf(r.When).After(to) {
					kept = append(kept, r)
				}
				continue
			}
			d := a.vault.Zone.DateOf(r.When)
			if !d.Before(from) && !d.After(to) {
				kept = append(kept, r)
			}
		}
		records = kept
	}
	data, err := ics.Export(a.vault, records, a.now)
	if err != nil {
		return err
	}

	out := a.flagOr("out", "")
	if out == "" || out == "-" {
		if a.out.JSON {
			return usageError("export ics writes an iCalendar stream, not JSON; use --out <path> or drop --json")
		}
		if _, err := a.stdout().Write(data); err != nil {
			return fmt.Errorf("writing the iCalendar stream: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}
	events := strings.Count(string(data), "BEGIN:VEVENT")
	if a.out.JSON {
		return a.out.Emit(map[string]any{"out": out, "events": events, "bytes": len(data)})
	}
	a.out.Scalar("exported", out)
	a.out.Scalar("events", strconv.Itoa(events))
	a.out.Scalar("bytes", strconv.Itoa(len(data)))
	a.out.Attention([]string{"one-way export: importing or syncing a calendar back into the vault is out of scope"})
	a.out.Help([]string{"Open the file in any calendar app; re-run this command after changes"})
	return nil
}

func (a *app) cmdSetup() error {
	if len(a.args) == 0 {
		return usageError("setup needs a target: skill")
	}
	if a.args[0] != "skill" {
		return usageError("unknown setup target %q: the only target is skill", a.args[0])
	}
	if len(a.args) > 1 {
		return usageError("setup skill takes no extra arguments, got %s", strings.Join(a.args[1:], " "))
	}
	targets, err := skill.Targets(skill.Choice{
		Claude: a.has("claude"), Codex: a.has("codex"), Dir: a.flagOr("dir", ""),
	})
	if err != nil {
		return usageError("%v", err)
	}
	var installed []skill.Result
	for _, t := range targets {
		res, err := skill.Install(t)
		if err != nil {
			return err
		}
		installed = append(installed, res)
	}
	if a.out.JSON {
		return a.out.Emit(map[string]any{"installed": installed})
	}
	block := render.Block{Name: "installed", Columns: render.Cols([]string{"agent", "path", "files", "state"})}
	for _, r := range installed {
		block.Rows = append(block.Rows, []string{r.Agent, r.Path, strconv.Itoa(r.Files), r.State})
	}
	a.out.Block(block)
	a.out.Help([]string{
		"The skill teaches the agent when to reach for the brain, how to resolve dates, and the echo-back rule",
		"Run `brain-axi doctor` to confirm it is found",
	})
	return nil
}

func (a *app) cmdDoctor() error {
	if err := a.requireArgs(0, "doctor"); err != nil {
		return err
	}
	rep := a.diagnose()
	if a.out.JSON {
		return a.out.Emit(rep)
	}
	fields := make([]render.Field, 0, len(rep.Rows))
	for _, row := range rep.Rows {
		fields = append(fields, render.Field{Key: row.Name, Value: row.Detail})
	}
	a.out.Fields(fields)
	a.out.Attention(rep.Attention)
	a.out.Help(rep.Help)
	if rep.Fatal != "" {
		return dataError("%s", rep.Fatal)
	}
	return nil
}

// doctorRow is one reported line.
type doctorRow struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	OK     bool   `json:"ok"`
}

// doctorReport is doctor's whole answer. It reports what is true, including
// what is missing: a missing vault remote is an attention item, not silence.
type doctorReport struct {
	Rows      []doctorRow `json:"checks"`
	Attention []string    `json:"attention"`
	Help      []string    `json:"help"`
	Fatal     string      `json:"fatal,omitempty"`
}

func (r *doctorReport) add(name, detail string, ok bool) {
	r.Rows = append(r.Rows, doctorRow{Name: name, Detail: detail, OK: ok})
}

func (a *app) diagnose() doctorReport {
	rep := doctorReport{}
	if err := a.openVault(); err != nil {
		rep.add("vault", "not found", false)
		rep.Attention = append(rep.Attention, strings.ReplaceAll(err.Error(), "\n", "; "))
		rep.Help = append(rep.Help, "Run `brain-axi init` to create a vault")
		rep.add("binary", version, true)
		rep.Fatal = "no vault to check"
		return rep
	}
	rep.add("vault", a.vault.Root+"  ok", true)
	rep.add("config", fmt.Sprintf("%s, week starts %s, nudge %s  ok",
		a.vault.Config.Timezone, a.vault.Config.WeekStarts, a.vault.Config.NudgeAfter), true)

	records, walkErr := a.vault.Walk()
	if walkErr != nil {
		rep.add("files", "parse failed", false)
		rep.Attention = append(rep.Attention, walkErr.Error())
		rep.Help = append(rep.Help, "Fix the file named above; the tool never skips a malformed record")
		rep.Fatal = "the vault does not parse"
	} else {
		counts := map[vault.Kind]int{}
		for _, r := range records {
			counts[r.Kind]++
		}
		var parts []string
		for _, k := range vault.Kinds {
			if counts[k] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
			}
		}
		detail := fmt.Sprintf("%d parsed, 0 malformed", len(records))
		if len(parts) > 0 {
			detail += " (" + strings.Join(parts, ", ") + ")"
		}
		rep.add("files", detail, true)

		if _, dangling := linksTo(records); len(dangling) > 0 {
			// Every unresolved reference is named with the file and the line it
			// is on, because "unresolved link" without one is a hunt through
			// the vault. An assignee nobody has a profile for arrives here too.
			assignees := 0
			for _, d := range dangling {
				if d.Field == "assignee" {
					assignees++
				}
				rep.Attention = append(rep.Attention, fmt.Sprintf("%s: %s: %s names %q, which no record claims",
					d.Where(), d.Field, d.Record.ID, d.ID))
			}
			detail := fmt.Sprintf("%d unresolved", len(dangling))
			if assignees > 0 {
				detail += fmt.Sprintf(" (%d of them an assignee with no people/ record)", assignees)
			}
			rep.add("links", detail, false)
		} else {
			rep.add("links", "all resolved  ok", true)
		}

		stale := 0
		for _, r := range records {
			if r.Kind == vault.KindIdea && r.Status == "pending" && a.vault.PastHorizon(r, a.now) {
				stale++
			}
		}
		if stale > 0 {
			rep.add("ideas", fmt.Sprintf("%d past their nudge horizon", stale), false)
			rep.Attention = append(rep.Attention, fmt.Sprintf("%d idea(s) past their nudge horizon - run `brain-axi review`", stale))
		} else {
			rep.add("ideas", "none past their nudge horizon  ok", true)
		}

		a.recurrenceDetail(&rep, records)
		a.taskDetail(&rep, records)
		a.boardDetail(&rep)
		a.forgeDetail(&rep, records)
	}

	rep.add("git", a.gitDetail(&rep), true)
	rep.add("skill", a.skillDetail(&rep), true)
	rep.add("backlog", a.backlogDetail(&rep), true)
	rep.add("binary", version, true)
	if len(rep.Help) == 0 {
		rep.Help = append(rep.Help, "Run `brain-axi` for the dashboard")
	}
	return rep
}

// recurrenceDetail checks every series against the next year (the same
// per-iteration window NextOccurrence searches) for an occurrence whose wall
// clock does not exist or is ambiguous in the vault zone (see
// internal/vault/recurrence.go and AGENTS.md "Sharp edges"). A bad series is
// an attention item, not a fatal error: doctor keeps reporting every other
// check regardless of what one broken rrule does.
func (a *app) recurrenceDetail(rep *doctorReport, records []*vault.Record) {
	from := a.now
	to := a.vault.Zone.AddDays(a.now, 366)
	bad := 0
	for _, r := range records {
		if r.Kind != vault.KindEvent || r.RRule == "" {
			continue
		}
		if _, err := a.vault.Expand(r, from, to); err != nil {
			bad++
			rep.Attention = append(rep.Attention, err.Error())
		}
	}
	if bad > 0 {
		rep.add("recurrence", fmt.Sprintf("%d series with an occurrence that cannot be resolved in the next year", bad), false)
	} else {
		rep.add("recurrence", "every series resolves in the next year  ok", true)
	}
}

// taskDetail reports outstanding commitments: what is overdue, and what has
// gone past its follow-up horizon without anyone checking it.
func (a *app) taskDetail(rep *doctorReport, records []*vault.Record) {
	overdue, unchecked := 0, 0
	for _, r := range records {
		if r.Kind != vault.KindTask || !vault.TaskIsOpen(r.Status) {
			continue
		}
		if r.HasDue && r.Due.Before(a.now) {
			overdue++
		}
		if a.vault.PastHorizon(r, a.now) {
			unchecked++
			who := "nobody has checked it"
			if r.Assignee != "" {
				who = "delegated to " + r.Assignee + ", not checked"
			}
			rep.Attention = append(rep.Attention, fmt.Sprintf("%s: %s for %dd, past its %dd follow-up horizon",
				r.ID, who, a.vault.AgeDays(r, a.now), a.vault.Horizon(r).ApproxDays()))
		}
	}
	switch {
	case overdue == 0 && unchecked == 0:
		rep.add("tasks", "none overdue, none past their follow-up horizon  ok", true)
	default:
		rep.add("tasks", fmt.Sprintf("%d overdue, %d past their follow-up horizon", overdue, unchecked), false)
	}
}

// boardDetail checks the board this vault points at, if it points at one.
//
// The board file is the one artefact this tool writes that something else
// reads, and it is the one a person or an older binary can have left off
// contract. Reading its payload back and validating it here is what turns
// "the board looks empty" into a line number.
func (a *app) boardDetail(rep *doctorReport) {
	path, err := vault.ExpandHome(strings.TrimSpace(a.vault.Config.BoardHTML))
	if err != nil {
		rep.add("board", "board_html cannot be resolved", false)
		rep.Attention = append(rep.Attention, err.Error())
		return
	}
	if path == "" {
		rep.add("board", "no board_html configured; `brain-axi board --html <path>` writes one anywhere", true)
		return
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			rep.add("board", path+" has not been built yet", true)
			rep.Help = append(rep.Help, "Run `brain-axi board` to build "+path)
			return
		}
		rep.add("board", "cannot be read", false)
		rep.Attention = append(rep.Attention, fmt.Sprintf("%s cannot be read: %v", path, readErr))
		return
	}
	payloadRaw, line, err := payload.Extract(string(raw), board.Slot)
	if err != nil {
		rep.add("board", "carries no readable payload", false)
		rep.Attention = append(rep.Attention, fmt.Sprintf("%s: %v; rebuild it with `brain-axi board`", path, err))
		return
	}
	model, err := board.Validate(path, payloadRaw, line)
	if err != nil {
		rep.add("board", "fails "+board.Schema, false)
		rep.Attention = append(rep.Attention, err.Error())
		return
	}
	rows := 0
	for _, pane := range model.Panes {
		rows += len(pane.Rows)
	}
	rep.add("board", fmt.Sprintf("%s  %s, %d panes, %d rows, generated %s  ok",
		path, model.Schema, len(model.Panes), rows, model.Generated), true)
}

// forgeDetail reports which forge CLIs this machine has and which linked hosts
// it can actually reach.
//
// This is the one diagnostic that reaches the network, and it does so because
// the question it answers - can this machine read the linked forge -
// cannot be answered any other way. It asks each CLI about its own host with a
// short timeout, and reports a missing CLI, an unauthenticated host and an
// unreachable one as three different answers rather than one shrug.
func (a *app) forgeDetail(rep *doctorReport, records []*vault.Record) {
	var installed []string
	for _, kind := range []forge.Kind{forge.GitHub, forge.GitLab} {
		cli := kind.CLI()
		if _, err := runner.Look(cli); err != nil {
			installed = append(installed, cli+" missing")
			continue
		}
		installed = append(installed, cli+" present")
	}

	// One probe per distinct host, in a stable order, however many records
	// point at it.
	hosts := map[string]forge.Kind{}
	linked := 0
	for _, r := range records {
		if !r.HasForge {
			continue
		}
		linked++
		ref, err := forge.Detect(r.Forge.URL)
		if err != nil {
			rep.Attention = append(rep.Attention, fmt.Sprintf("%s has an unreadable forge_url: %v", r.ID, err))
			continue
		}
		hosts[ref.Host] = ref.Kind
	}
	detail := strings.Join(installed, ", ")
	if linked == 0 {
		rep.add("forge", detail+"; no record is linked to a pull request", true)
		return
	}
	names := make([]string, 0, len(hosts))
	for host := range hosts {
		names = append(names, host)
	}
	sort.Strings(names)
	ok := true
	for _, host := range names {
		state, reachable := forge.Reachable(forge.Probe, hosts[host], host)
		detail += fmt.Sprintf(", %s %s", host, state)
		if !reachable {
			ok = false
			rep.Attention = append(rep.Attention, fmt.Sprintf(
				"%s cannot be read from this machine: %s; linked records fall back to their cached status, which is only as fresh as its forge_checked_at",
				host, state))
		}
	}
	rep.add("forge", fmt.Sprintf("%s (%d linked record(s))", detail, linked), ok)
}

func (a *app) gitDetail(rep *doctorReport) string {
	if _, err := os.Stat(filepath.Join(a.vault.Root, ".git")); err != nil {
		rep.Attention = append(rep.Attention, "vault is not a git repository - a bad edit is unrecoverable; run `git -C "+a.vault.Root+" init`")
		return "not a repository"
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "git is not on PATH, cannot report"
	}
	var parts []string
	if out, err := exec.Command("git", "-C", a.vault.Root, "status", "--porcelain").Output(); err == nil {
		if n := len(strings.Fields(strings.TrimSpace(string(out)))); n == 0 {
			parts = append(parts, "clean")
		} else {
			parts = append(parts, "uncommitted changes")
		}
	}
	if out, err := exec.Command("git", "-C", a.vault.Root, "rev-list", "--count", "HEAD").Output(); err == nil {
		parts = append(parts, strings.TrimSpace(string(out))+" commits")
	} else {
		parts = append(parts, "no commits yet")
	}
	remote, err := gitRemote(a.vault.Root)
	switch {
	case err != nil:
		parts = append(parts, "cannot read remotes")
	case remote == "":
		// The one durability gap the specification leaves open: local history
		// protects against a bad edit, not against a dead disk.
		parts = append(parts, "no remote configured")
		rep.Attention = append(rep.Attention, "vault has no remote - a disk failure loses it; `git -C "+a.vault.Root+" remote add origin <private repo>` closes the gap")
	default:
		parts = append(parts, "remote "+remote)
	}
	return strings.Join(parts, ", ")
}

func gitRemote(dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return "", err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", err
	}
	out, err := exec.Command("git", "-C", dir, "remote").Output()
	if err != nil {
		return "", err
	}
	remotes := strings.Fields(strings.TrimSpace(string(out)))
	if len(remotes) == 0 {
		return "", nil
	}
	return remotes[0], nil
}

func (a *app) skillDetail(rep *doctorReport) string {
	found := skill.Installed()
	if len(found) == 0 {
		rep.Attention = append(rep.Attention, "the agent skill is not installed - run `brain-axi setup skill --claude`")
		return "not installed"
	}
	var parts []string
	for _, f := range found {
		state := "installed"
		if f.Stale {
			state = "out of date"
			rep.Attention = append(rep.Attention, f.Path+" is an older copy of the skill - re-run `brain-axi setup skill`")
		}
		parts = append(parts, f.Path+"  "+state)
	}
	return strings.Join(parts, ", ")
}

func (a *app) backlogDetail(rep *doctorReport) string {
	cmd := strings.TrimSpace(a.vault.Config.BacklogCmd)
	if cmd == "" {
		return "no backlog_cmd configured; the dashboard footer omits it"
	}
	n, ok, note := a.backlogCount()
	if !ok {
		rep.Attention = append(rep.Attention, fmt.Sprintf("backlog_cmd %q did not return a count (%s); fix or remove it in %s", cmd, note, a.vault.ConfigPath()))
		return "configured, not working"
	}
	return fmt.Sprintf("%d open, read-only  ok", n)
}
