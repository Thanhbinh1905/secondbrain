package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Thanhbinh1905/secondbrain/internal/vault"
)

// TestTheFleetBridgeIsOneDirectionOnly: both commands are pure local writes.
// brain-axi holds no supervisor state and reaches nothing, so the tool is
// exactly as usable on a machine with no supervisor at all.
func TestTheFleetBridgeIsOneDirectionOnly(t *testing.T) {
	root := fixtureVault(t)
	f := useForge(t, &scriptedForge{fail: map[string]error{
		"gh":   errors.New("gh must not run here"),
		"glab": errors.New("glab must not run here"),
	}})
	for _, args := range [][]string{
		{"link", "fleet", "customer-referral", "--task", "PROJ-42"},
		{"ship", "customer-referral", "--pr", "https://github.com/owner/repo/pull/12",
			"--merged-at", "2026-09-02T11:30:00+07:00"},
	} {
		got := invoke(t, root, "2026-09-02T12:00", false, args...)
		if got.Code != exitOK {
			t.Fatalf("%v: exit %d: %s", args, got.Code, got.Stderr)
		}
		if len(f.calls) != 0 {
			t.Fatalf("%v reached a forge: %v", args, f.calls)
		}
	}
}

// TestLinkFleetRecordsTheReferenceOnTheRecord: it lands in the record's own
// frontmatter, visible and hand-editable, and nowhere else.
func TestLinkFleetRecordsTheReferenceOnTheRecord(t *testing.T) {
	root := fixtureVault(t)
	// The fixture's task already refers to PROJ-42, which is the shape a second
	// reference has to coexist with.
	got := invoke(t, root, "2026-09-02T12:00", false, "link", "fleet", "migrate-staging-db", "--task", "PROJ-99")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	raw, err := os.ReadFile(filepath.Join(root, "tasks", "migrate-staging-db.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "PROJ-99") {
		t.Errorf("the reference is not on the record:\n%s", raw)
	}
	// A record can refer to more than one work item, and the same one twice is
	// refused rather than silently ignored.
	if got := invoke(t, root, "2026-09-02T12:00", false,
		"link", "fleet", "migrate-staging-db", "--task", "team/repo#7"); got.Code != exitOK {
		t.Fatalf("a second reference was refused: exit %d: %s", got.Code, got.Stderr)
	}
	got = invoke(t, root, "2026-09-02T12:00", false, "link", "fleet", "migrate-staging-db", "--task", "PROJ-99")
	if got.Code == exitOK {
		t.Error("the same reference was recorded twice")
	}
	v, err := vault.OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := v.Find("migrate-staging-db")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.FleetTasks, " ") != "PROJ-42 PROJ-99 team/repo#7" {
		t.Errorf("fleet_tasks = %v", r.FleetTasks)
	}
	// The record still parses and its forge cache is untouched.
	if !r.HasForge || r.Forge.State != "open" {
		t.Errorf("the fleet reference disturbed the forge cache: %+v", r.Forge)
	}
}

// TestShipRecordsWhenTheWorkLanded: the merge time is the only date in the
// vault that says when something shipped, so it is required and must carry an
// explicit offset.
func TestShipRecordsWhenTheWorkLanded(t *testing.T) {
	root := fixtureVault(t)
	const url = "https://github.com/owner/repo/pull/12"
	got := invoke(t, root, "2026-09-02T12:00", false,
		"ship", "customer-referral", "--pr", url, "--merged-at", "2026-09-02T11:30:00+07:00")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	v, err := vault.OpenAt(root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := v.Find("customer-referral")
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasShipped || r.ShippedPR != url {
		t.Fatalf("the ship record is incomplete: %+v", r)
	}
	if r.Status != "shipped" {
		t.Errorf("status = %q, want shipped", r.Status)
	}
	// Shipping is the record moving, so its touched date moved with it.
	if r.Touched.String() != "2026-09-02" {
		t.Errorf("touched = %s", r.Touched)
	}
	// A second ship is refused rather than silently overwriting the first.
	got = invoke(t, root, "2026-09-02T12:00", false,
		"ship", "customer-referral", "--pr", url, "--merged-at", "2026-09-03T11:30:00+07:00")
	if got.Code == exitOK {
		t.Error("a second ship overwrote the first without --force")
	}
	if !strings.Contains(got.Stderr, "--force") {
		t.Errorf("the refusal does not name --force: %s", got.Stderr)
	}

	// A task ships as done, and a note has no status to move.
	for _, tc := range []struct{ id, want string }{
		{"review-backup-policy", "done"},
		{"ci-capacity", ""},
	} {
		got := invoke(t, root, "2026-09-02T12:00", false,
			"ship", tc.id, "--pr", url, "--merged-at", "2026-09-02T11:30:00+07:00")
		if got.Code != exitOK {
			t.Fatalf("%s: exit %d: %s", tc.id, got.Code, got.Stderr)
		}
		fresh, err := vault.OpenAt(root)
		if err != nil {
			t.Fatal(err)
		}
		r, err := fresh.Find(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != tc.want {
			t.Errorf("%s status = %q, want %q", tc.id, r.Status, tc.want)
		}
		if !r.HasShipped {
			t.Errorf("%s did not record when it shipped", tc.id)
		}
	}
}

// TestShipRefusesEveryMalformedInput: a malformed url, an id nothing claims,
// and a timestamp with no offset all fail loudly and change nothing.
func TestShipRefusesEveryMalformedInput(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no url", []string{"ship", "customer-referral", "--merged-at", "2026-09-02T11:30:00+07:00"}, "--pr"},
		{"not a forge url", []string{"ship", "customer-referral", "--pr", "https://example.com/thing",
			"--merged-at", "2026-09-02T11:30:00+07:00"}, "pull request or merge request"},
		{"no timestamp", []string{"ship", "customer-referral", "--pr", "https://github.com/owner/repo/pull/1"}, "--merged-at"},
		{"naive timestamp", []string{"ship", "customer-referral", "--pr", "https://github.com/owner/repo/pull/1",
			"--merged-at", "2026-09-02T11:30"}, "no UTC offset"},
		{"unreadable timestamp", []string{"ship", "customer-referral", "--pr", "https://github.com/owner/repo/pull/1",
			"--merged-at", "last Tuesday"}, "not a valid instant"},
		{"no such record", []string{"ship", "no-such-thing", "--pr", "https://github.com/owner/repo/pull/1",
			"--merged-at", "2026-09-02T11:30:00+07:00"}, "no record with id"},
		{"a kind that does not ship", []string{"ship", "platform-team-sync-20260904", "--pr", "https://github.com/owner/repo/pull/1",
			"--merged-at", "2026-09-02T11:30:00+07:00"}, "an event cannot ship"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureVault(t)
			before := recordSnapshot(t, root)
			got := invoke(t, root, "2026-09-02T12:00", false, tc.args...)
			if got.Code == exitOK {
				t.Fatalf("accepted a malformed ship:\n%s", got.Stdout)
			}
			if !strings.Contains(got.Stderr, tc.want) {
				t.Errorf("the refusal does not explain the problem: %s", got.Stderr)
			}
			if after := recordSnapshot(t, root); after != before {
				t.Error("a refused ship changed the vault")
			}
		})
	}
}

// TestLinkFleetRefusesEveryMalformedInput, and changes nothing when it does.
func TestLinkFleetRefusesEveryMalformedInput(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no task", []string{"link", "fleet", "customer-referral"}, "--task"},
		{"empty task", []string{"link", "fleet", "customer-referral", "--task", "  "}, "--task"},
		{"a shell fragment", []string{"link", "fleet", "customer-referral", "--task", "rm -rf /"}, "must be letters"},
		{"a sentence", []string{"link", "fleet", "customer-referral", "--task", "the thing we agreed"}, "must be letters"},
		{"no such record", []string{"link", "fleet", "no-such-thing", "--task", "PROJ-42"}, "no record with id"},
		{"no record named", []string{"link", "fleet", "--task", "PROJ-42"}, "needs a record id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureVault(t)
			before := recordSnapshot(t, root)
			got := invoke(t, root, "2026-09-02T12:00", false, tc.args...)
			if got.Code == exitOK {
				t.Fatalf("accepted a malformed reference:\n%s", got.Stdout)
			}
			if !strings.Contains(got.Stderr, tc.want) {
				t.Errorf("the refusal does not explain the problem: %s", got.Stderr)
			}
			if after := recordSnapshot(t, root); after != before {
				t.Error("a refused reference changed the vault")
			}
		})
	}
}

// TestWhatShippedIsQueryableInAPeriod: the point of recording a merge time is
// being able to ask what actually landed, by name, over a span.
func TestWhatShippedIsQueryableInAPeriod(t *testing.T) {
	root := fixtureVault(t)
	if got := invoke(t, root, "2026-09-02T12:00", false,
		"ship", "customer-referral", "--pr", "https://github.com/owner/repo/pull/12",
		"--merged-at", "2026-09-02T11:30:00+07:00"); got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	// The fixture already holds one that shipped in August.
	got := invoke(t, root, "2026-09-02T12:00", false, "recap", "--from", "2026-08-01", "--to", "2026-08-31", "--json")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	if names := shippedNames(t, got.Stdout); strings.Join(names, ",") != "shipped-thing" {
		t.Errorf("August shipped %v", names)
	}
	got = invoke(t, root, "2026-09-02T12:00", false, "recap", "month", "--json")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	if names := shippedNames(t, got.Stdout); strings.Join(names, ",") != "customer-referral" {
		t.Errorf("September shipped %v", names)
	}
	// And a single record's own ship record is on the idea listing, so an agent
	// can answer the same question without a second command.
	got = invoke(t, root, "2026-09-02T12:00", false, "ideas", "--status", "shipped", "--json")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	var listing struct {
		Ideas []struct {
			ID        string `json:"id"`
			ShippedAt string `json:"shipped_at"`
			ShippedPR string `json:"shipped_pr"`
		} `json:"ideas"`
	}
	if err := json.Unmarshal([]byte(got.Stdout), &listing); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, row := range listing.Ideas {
		if row.ShippedAt != "" {
			found++
		}
	}
	if found != 2 {
		t.Errorf("%d of %d shipped ideas carry a merge time", found, len(listing.Ideas))
	}
}

func shippedNames(t *testing.T, stdout string) []string {
	t.Helper()
	var payload struct {
		Recap struct {
			Blocks []struct {
				Key  string `json:"key"`
				Rows []struct {
					ID string `json:"id"`
				} `json:"rows"`
			} `json:"blocks"`
		} `json:"recap"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, b := range payload.Recap.Blocks {
		if b.Key != "shipped" {
			continue
		}
		for _, row := range b.Rows {
			out = append(out, row.ID)
		}
	}
	return out
}
