package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDueSaysNothingWhenNothingIsDue: a command meant to be run on a short
// interval must be silent by default, or its output stops being read.
func TestDueSaysNothingWhenNothingIsDue(t *testing.T) {
	root := fixtureVault(t)
	// Long before anything in the fixture decayed past a window.
	got := invoke(t, root, "2026-08-02T03:00", false, "due")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	if got.Stdout != "" {
		t.Errorf("due printed something with nothing due:\n%q", got.Stdout)
	}
	if got.Stderr != "" {
		t.Errorf("due wrote to stderr with nothing due:\n%q", got.Stderr)
	}
	// --json still answers, because an agent needs to tell "none" apart from
	// "the command did not run".
	got = invoke(t, root, "2026-08-02T03:00", false, "due", "--json")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	var payload struct {
		Due []map[string]any `json:"due"`
	}
	if err := json.Unmarshal([]byte(got.Stdout), &payload); err != nil {
		t.Fatalf("due --json is not valid JSON: %v\n%s", err, got.Stdout)
	}
	if len(payload.Due) != 0 {
		t.Errorf("due --json reported %d items with nothing due", len(payload.Due))
	}
}

// TestDueNamesThePersonAndHowLong: the delegated category is the one worth
// building the command for, so its line has to be actionable on its own.
func TestDueNamesThePersonAndHowLong(t *testing.T) {
	root := fixtureVault(t)
	got := invoke(t, root, "2026-09-02T12:00", false, "due")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	for _, want := range []string{"delegated", "migrate-staging-db", "platform-team", "28d", "14d follow-up horizon"} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("due does not carry %q:\n%s", want, got.Stdout)
		}
	}
	got = invoke(t, root, "2026-09-02T12:00", false, "due", "--json")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	var payload struct {
		Due []struct {
			Category string `json:"category"`
			ID       string `json:"id"`
			Person   string `json:"person"`
			Days     int    `json:"days"`
			Reason   string `json:"reason"`
		} `json:"due"`
	}
	if err := json.Unmarshal([]byte(got.Stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Due) != 1 {
		t.Fatalf("due = %+v", payload.Due)
	}
	row := payload.Due[0]
	if row.Category != "delegated" || row.Person != "platform-team" || row.Days != 28 {
		t.Errorf("the delegated row is not actionable on its own: %+v", row)
	}
}

// TestDueWritesNothingThroughTheCLI: it is a read, however often it runs.
func TestDueWritesNothingThroughTheCLI(t *testing.T) {
	root := fixtureVault(t)
	before := recordSnapshot(t, root)
	if got := invoke(t, root, "2026-09-02T12:00", false, "due"); got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	if after := recordSnapshot(t, root); after != before {
		t.Error("due changed the vault")
	}
}

// TestRelatedAnswersBothDirections: what a meeting produced, and what a record
// points at, with the field that did the pointing.
func TestRelatedAnswersBothDirections(t *testing.T) {
	root := fixtureVault(t)
	got := invoke(t, root, "2026-09-02T12:00", false, "related", "platform-team-sync-20260904", "--json")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	var payload struct {
		PointsTo []struct {
			ID       string   `json:"id"`
			Via      []string `json:"via"`
			Resolved bool     `json:"resolved"`
		} `json:"points_to"`
		PointedToBy []struct {
			ID  string   `json:"id"`
			Via []string `json:"via"`
		} `json:"pointed_to_by"`
	}
	if err := json.Unmarshal([]byte(got.Stdout), &payload); err != nil {
		t.Fatal(err)
	}
	// The meeting is with a person and produced an idea that links back to it.
	if len(payload.PointsTo) != 1 || payload.PointsTo[0].ID != "platform-team" ||
		strings.Join(payload.PointsTo[0].Via, "+") != "with" {
		t.Errorf("points_to = %+v", payload.PointsTo)
	}
	if len(payload.PointedToBy) != 1 || payload.PointedToBy[0].ID != "calendar-export" ||
		strings.Join(payload.PointedToBy[0].Via, "+") != "links" {
		t.Errorf("pointed_to_by = %+v", payload.PointedToBy)
	}

	// Everything involving a person: their profile is pointed at by the event
	// they are in, the task they hold, and the note waiting to be raised.
	got = invoke(t, root, "2026-09-02T12:00", false, "related", "platform-team", "--json")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	if err := json.Unmarshal([]byte(got.Stdout), &payload); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, row := range payload.PointedToBy {
		seen[row.ID] = strings.Join(row.Via, "+")
	}
	for id, via := range map[string]string{
		"platform-team-sync-20260904": "with",
		"migrate-staging-db":          "body+assignee",
		"ci-capacity":                 "raise_with",
	} {
		if seen[id] != via {
			t.Errorf("%s points at platform-team via %q, want %q", id, seen[id], via)
		}
	}
}

// TestALinkToNothingIsReportedNotRejected: writing a link before its target
// exists is ordinary, and the same precedent with: and assignee already set.
func TestALinkToNothingIsReportedNotRejected(t *testing.T) {
	root := fixtureVault(t)
	got := invoke(t, root, "2026-09-02T12:00", false,
		"add", "idea", "an idea about something uncaptured", "--id", "dangler", "--links", "never-captured")
	if got.Code != exitOK {
		t.Fatalf("a link to a record that does not exist was rejected: exit %d: %s", got.Code, got.Stderr)
	}
	// Every read command still works, because a dangling link is not corruption.
	if got := invoke(t, root, "2026-09-02T12:00", false, "today"); got.Code != exitOK {
		t.Fatalf("a dangling link broke today: exit %d: %s", got.Code, got.Stderr)
	}
	got = invoke(t, root, "2026-09-02T12:00", false, "related", "dangler")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "never-captured") || !strings.Contains(got.Stdout, "no") {
		t.Errorf("related does not show the link as unresolved:\n%s", got.Stdout)
	}
}

// TestDoctorNamesEveryUnresolvedLinkWithItsLine is the definition of done for
// the linking layer's diagnostic half: doctor reports the unresolved link, the
// unknown pane, and the assignee nobody has a profile for, each with the file
// and line to fix it on.
func TestDoctorNamesEveryUnresolvedLinkWithItsLine(t *testing.T) {
	root := fixtureVault(t)
	for _, args := range [][]string{
		{"add", "idea", "links nowhere", "--id", "dangler", "--links", "never-captured"},
		{"add", "task", "chase a ghost", "--id", "ghost-chase", "--assignee", "nobody-exists"},
		{"add", "task", "raise with a ghost", "--id", "ghost-raise", "--raise-with", "also-nobody"},
	} {
		if got := invoke(t, root, "2026-09-02T12:00", false, args...); got.Code != exitOK {
			t.Fatalf("%v: exit %d: %s", args, got.Code, got.Stderr)
		}
	}
	got := invoke(t, root, "2026-09-02T12:00", false, "doctor", "--json")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	var rep doctorReport
	if err := json.Unmarshal([]byte(got.Stdout), &rep); err != nil {
		t.Fatal(err)
	}
	rows := map[string]doctorRow{}
	for _, row := range rep.Rows {
		rows[row.Name] = row
	}
	if links, ok := rows["links"]; !ok || links.OK {
		t.Fatalf("doctor's links check did not flag anything: %+v", rows["links"])
	}
	if !strings.Contains(rows["links"].Detail, "assignee with no people/ record") {
		t.Errorf("doctor does not single out the dangling assignee: %q", rows["links"].Detail)
	}
	joined := strings.Join(rep.Attention, "\n")
	for _, want := range []string{
		"ideas/dangler.md:", "links: dangler names \"never-captured\"",
		"tasks/ghost-chase.md:", "assignee: ghost-chase names \"nobody-exists\"",
		"tasks/ghost-raise.md:", "raise_with: ghost-raise names \"also-nobody\"",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("doctor's attention does not carry %q:\n%s", want, joined)
		}
	}
	// Every line named is a real line in the file it names.
	for _, line := range rep.Attention {
		if !strings.Contains(line, ".md:") {
			continue
		}
		if strings.Contains(line, ".md:0:") || strings.Contains(line, ".md:1:") {
			t.Errorf("an unresolved link was reported without a usable line: %s", line)
		}
	}
}

// TestAnAgendaSurfacesBesideTheMeetingItIsFor: what to raise with somebody is
// only useful in the minutes before walking into a room with them.
func TestAnAgendaSurfacesBesideTheMeetingItIsFor(t *testing.T) {
	root := fixtureVault(t)
	// today and week are agent surfaces and carry the item's id; the framed
	// board is the human one and carries its title and who to raise it with.
	for _, args := range [][]string{{"today"}, {"week"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got := invoke(t, root, "2026-09-02T12:00", false, args...)
			if got.Code != exitOK {
				t.Fatalf("exit %d: %s", got.Code, got.Stderr)
			}
			for _, want := range []string{"raise_with[", "platform-team", "ci-capacity"} {
				if !strings.Contains(got.Stdout, want) {
					t.Errorf("%v does not surface the agenda item %q:\n%s", args, want, got.Stdout)
				}
			}
		})
	}
	t.Run("board", func(t *testing.T) {
		got := invoke(t, root, "2026-09-02T12:00", false, "board")
		if got.Code != exitOK {
			t.Fatalf("exit %d: %s", got.Code, got.Stderr)
		}
		if !strings.Contains(got.Stdout, "raise with platform-team") {
			t.Errorf("the board does not surface the agenda beside the meeting:\n%s", got.Stdout)
		}
		got = invoke(t, root, "2026-09-02T12:00", false, "board", "--json")
		if got.Code != exitOK {
			t.Fatalf("exit %d: %s", got.Code, got.Stderr)
		}
		var payload struct {
			Board struct {
				Panes []struct {
					Key  string `json:"key"`
					Rows []struct {
						ID   string `json:"id"`
						Kind string `json:"kind"`
						Note string `json:"note"`
					} `json:"rows"`
				} `json:"panes"`
			} `json:"board"`
		}
		if err := json.Unmarshal([]byte(got.Stdout), &payload); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, pane := range payload.Board.Panes {
			for _, row := range pane.Rows {
				if row.Kind == "agenda" && row.ID == "ci-capacity" && row.Note == "raise with platform-team" {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("the board payload carries no agenda row for the meeting: %s", got.Stdout)
		}
	})
	// A day with no meeting with that person carries no agenda for them.
	got := invoke(t, root, "2026-09-05T08:00", false, "today")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	if strings.Contains(got.Stdout, "ci-capacity") {
		t.Errorf("an agenda surfaced on a day with no meeting for it:\n%s", got.Stdout)
	}
}

// TestAgendaTakesAPersonOrARange: the two forms cannot be confused, because a
// range takes no positional argument and a person takes exactly one.
func TestAgendaTakesAPersonOrARange(t *testing.T) {
	root := fixtureVault(t)
	got := invoke(t, root, "2026-09-02T12:00", false, "agenda", "--from", "2026-09-01", "--to", "2026-09-07")
	if got.Code != exitOK || !strings.Contains(got.Stdout, "range:") {
		t.Errorf("the range form broke: exit %d\n%s%s", got.Code, got.Stdout, got.Stderr)
	}
	got = invoke(t, root, "2026-09-02T12:00", false, "agenda", "platform-team")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	for _, want := range []string{"person: platform-team", "ci-capacity"} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("the person form does not carry %q:\n%s", want, got.Stdout)
		}
	}
	// An id nobody claims is a typo, and saying so is more use than silence.
	got = invoke(t, root, "2026-09-02T12:00", false, "agenda", "nobody-at-all")
	if got.Code == exitOK {
		t.Error("an unknown person exited zero")
	}
}

// TestAnItemLeavesTheAgendaWhenItIsRaisedOrCloses: the two ways off a person's
// list, both of them ordinary edits.
func TestAnItemLeavesTheAgendaWhenItIsRaisedOrCloses(t *testing.T) {
	t.Run("raised", func(t *testing.T) {
		root := fixtureVault(t)
		got := invoke(t, root, "2026-09-02T12:00", false, "update", "ci-capacity", "--set", "raised=2026-09-02")
		if got.Code != exitOK {
			t.Fatalf("exit %d: %s", got.Code, got.Stderr)
		}
		got = invoke(t, root, "2026-09-02T12:00", false, "agenda", "platform-team")
		if got.Code != exitOK {
			t.Fatalf("exit %d: %s", got.Code, got.Stderr)
		}
		if strings.Contains(got.Stdout, "ci-capacity") {
			t.Errorf("a raised item stayed on the agenda:\n%s", got.Stdout)
		}
	})
	t.Run("closed", func(t *testing.T) {
		root := fixtureVault(t)
		// A task on somebody's agenda leaves it when the task closes.
		if got := invoke(t, root, "2026-09-02T12:00", false,
			"add", "task", "review CI capacity", "--id", "review-ci-capacity", "--raise-with", "platform-team"); got.Code != exitOK {
			t.Fatalf("exit %d: %s", got.Code, got.Stderr)
		}
		got := invoke(t, root, "2026-09-02T12:00", false, "agenda", "platform-team")
		if !strings.Contains(got.Stdout, "review-ci-capacity") {
			t.Fatalf("the new item is not on the agenda:\n%s", got.Stdout)
		}
		if got := invoke(t, root, "2026-09-02T12:00", false, "done", "review-ci-capacity"); got.Code != exitOK {
			t.Fatalf("exit %d: %s", got.Code, got.Stderr)
		}
		got = invoke(t, root, "2026-09-02T12:00", false, "agenda", "platform-team")
		if strings.Contains(got.Stdout, "review-ci-capacity") {
			t.Errorf("a closed item stayed on the agenda:\n%s", got.Stdout)
		}
	})
}

// TestAPersonRecordCarriesWhatTheyAreHolding: people/ is a record kind worth
// opening, not just a name a link resolves to.
func TestAPersonRecordCarriesWhatTheyAreHolding(t *testing.T) {
	root := fixtureVault(t)
	got := invoke(t, root, "2026-09-02T12:00", false, "show", "platform-team", "--json")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	var obj struct {
		Open   []map[string]any `json:"open_items"`
		Closed []map[string]any `json:"closed_items"`
		Agenda []struct {
			ID string `json:"id"`
		} `json:"agenda"`
	}
	if err := json.Unmarshal([]byte(got.Stdout), &obj); err != nil {
		t.Fatal(err)
	}
	if len(obj.Open) != 1 || obj.Open[0]["id"] != "migrate-staging-db" {
		t.Errorf("open_items = %+v", obj.Open)
	}
	if obj.Closed == nil {
		t.Error("closed_items is absent; an agent cannot tell none from unsupported")
	}
	if len(obj.Agenda) != 1 || obj.Agenda[0].ID != "ci-capacity" {
		t.Errorf("agenda = %+v", obj.Agenda)
	}
	// The three blocks are on the text surface too.
	got = invoke(t, root, "2026-09-02T12:00", false, "show", "platform-team")
	for _, want := range []string{"open_items[", "closed_items[", "agenda["} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("show does not carry %q:\n%s", want, got.Stdout)
		}
	}
}
