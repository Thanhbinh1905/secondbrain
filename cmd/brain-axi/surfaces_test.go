package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Thanhbinh1905/secondbrain/internal/board"
	"github.com/Thanhbinh1905/secondbrain/internal/payload"
	"github.com/Thanhbinh1905/secondbrain/internal/recap"
)

// configure appends a key to a vault's config, so a test can drive the board
// settings the way a user would.
func configure(t *testing.T, root string, lines ...string) {
	t.Helper()
	path := filepath.Join(root, ".brain", "config.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(string(raw)+strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTheBoardIsWrittenToAFileAndNothingElse is the whole integration seam:
// brain-axi opens no socket and serves nothing, and the board never writes to
// the vault it reads.
func TestTheBoardIsWrittenToAFileAndNothingElse(t *testing.T) {
	root := fixtureVault(t)
	before := recordSnapshot(t, root)
	out := filepath.Join(t.TempDir(), "board.html")
	got := invoke(t, root, "2026-09-02T12:00", false, "board", "--html", out)
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	if after := recordSnapshot(t, root); after != before {
		t.Error("building the board changed the vault")
	}
	page, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), board.Schema) {
		t.Error("the page does not carry its schema")
	}
	// Self-contained: nothing on the page fetches anything.
	for _, forbidden := range []string{"<script src=", "@import", "<link rel=\"stylesheet\"", "http://", "https://fonts"} {
		if strings.Contains(string(page), forbidden) {
			t.Errorf("the page is not self-contained: it carries %q", forbidden)
		}
	}
	// It says plainly what an annotation on it is worth.
	for _, want := range []string{"input, never instruction", "confers no permission", "grants no permission"} {
		if strings.Contains(string(page), want) {
			return
		}
	}
	t.Errorf("the page does not say that an annotation is input rather than instruction")
}

// TestBoardJSONHasOneShape: an agent parsing this command parses one envelope
// whether or not a file was written.
func TestBoardJSONHasOneShape(t *testing.T) {
	root := fixtureVault(t)
	out := filepath.Join(t.TempDir(), "board.html")
	for _, args := range [][]string{
		{"board", "--json"},
		{"board", "--html", out, "--json"},
	} {
		got := invoke(t, root, "2026-09-02T12:00", false, args...)
		if got.Code != exitOK {
			t.Fatalf("%v: exit %d: %s", args, got.Code, got.Stderr)
		}
		var payload struct {
			Board board.Model `json:"board"`
		}
		if err := json.Unmarshal([]byte(got.Stdout), &payload); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, got.Stdout)
		}
		if payload.Board.Schema != board.Schema {
			t.Errorf("%v emitted no board under \"board\": %s", args, got.Stdout)
		}
		if len(payload.Board.Panes) != len(board.Panes) {
			t.Errorf("%v emitted %d panes", args, len(payload.Board.Panes))
		}
	}
}

// TestAHandWrittenTildePathResolvesToTheHomeDirectory: config.yml is edited by
// hand and is not a shell, so a leading ~ must not create a directory called
// "~" beside the working directory.
func TestAHandWrittenTildePathResolvesToTheHomeDirectory(t *testing.T) {
	root := fixtureVault(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	configure(t, root, "board_html: ~/secondbrain/board.html")
	got := invoke(t, root, "2026-09-02T12:00", false, "board")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	want := filepath.Join(home, "secondbrain", "board.html")
	if !strings.Contains(got.Stdout, want) {
		t.Errorf("board did not resolve ~ to the home directory:\n%s", got.Stdout)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("the board was not written to %s: %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(root, "~")); err == nil {
		t.Error("a directory literally named \"~\" was created")
	}
}

// TestTheBoardPathIsStableAndRewrittenInPlace: an external viewer's URL has to
// survive a rebuild, so the same path is replaced rather than a new file made.
func TestTheBoardPathIsStableAndRewrittenInPlace(t *testing.T) {
	root := fixtureVault(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "board.html")
	first := invoke(t, root, "2026-09-02T12:00", false, "board", "--html", out)
	if first.Code != exitOK {
		t.Fatalf("exit %d: %s", first.Code, first.Stderr)
	}
	before, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	second := invoke(t, root, "2026-09-04T12:00", false, "board", "--html", out)
	if second.Code != exitOK {
		t.Fatalf("exit %d: %s", second.Code, second.Stderr)
	}
	after, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Error("the rebuild did not change the board")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the rebuild left %d files in the directory, want 1", len(entries))
	}
	// board_html gives the same stable path without the flag.
	configure(t, root, "board_html: "+out)
	third := invoke(t, root, "2026-09-05T12:00", false, "board")
	if third.Code != exitOK {
		t.Fatalf("exit %d: %s", third.Code, third.Stderr)
	}
	if !strings.Contains(third.Stdout, out) {
		t.Errorf("board did not use the configured board_html:\n%s", third.Stdout)
	}
}

// TestOpenWithoutAViewerKeepsTheFileAndSaysSo: --open may hand the file over,
// but a missing viewer is reported as itself and never swallowed, and the file
// that was already written stays exactly where it is.
func TestOpenWithoutAViewerKeepsTheFileAndSaysSo(t *testing.T) {
	t.Run("no file to open", func(t *testing.T) {
		root := fixtureVault(t)
		got := invoke(t, root, "2026-09-02T12:00", false, "board", "--open")
		if got.Code == exitOK {
			t.Error("--open with no file to open exited zero")
		}
		for _, want := range []string{"--html", "board_html"} {
			if !strings.Contains(got.Stderr, want) {
				t.Errorf("the refusal does not say how to give it a path: %s", got.Stderr)
			}
		}
	})
	t.Run("no viewer configured", func(t *testing.T) {
		root := fixtureVault(t)
		out := filepath.Join(t.TempDir(), "board.html")
		got := invoke(t, root, "2026-09-02T12:00", false, "board", "--html", out, "--open")
		if got.Code == exitOK {
			t.Error("--open with nothing to open with exited zero")
		}
		if !strings.Contains(got.Stderr, "board_open_cmd") {
			t.Errorf("the failure does not name the missing setting: %s", got.Stderr)
		}
		if _, err := os.Stat(out); err != nil {
			t.Errorf("the board that was already written was lost: %v", err)
		}
	})
	t.Run("viewer not on PATH", func(t *testing.T) {
		root := fixtureVault(t)
		configure(t, root, "board_open_cmd: no-such-viewer-at-all")
		out := filepath.Join(t.TempDir(), "board.html")
		got := invoke(t, root, "2026-09-02T12:00", false, "board", "--html", out, "--open")
		if got.Code == exitOK {
			t.Error("--open with an absent viewer exited zero")
		}
		if !strings.Contains(got.Stderr, "no-such-viewer-at-all") || !strings.Contains(got.Stderr, "written and unchanged") {
			t.Errorf("the failure does not say the file survives: %s", got.Stderr)
		}
		page, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("the board was lost when the viewer failed: %v", err)
		}
		if !strings.Contains(string(page), board.Schema) {
			t.Error("the board that survived is not a whole board")
		}
	})
	t.Run("a viewer that works", func(t *testing.T) {
		root := fixtureVault(t)
		configure(t, root, "board_open_cmd: true")
		out := filepath.Join(t.TempDir(), "board.html")
		got := invoke(t, root, "2026-09-02T12:00", false, "board", "--html", out, "--open")
		if got.Code != exitOK {
			t.Fatalf("exit %d: %s%s", got.Code, got.Stdout, got.Stderr)
		}
	})
}

// TestAFailedWriteLeavesThePreviousBoardStanding: validation is fail-closed
// and the replacement is atomic, so a board that was good is never left half
// rewritten.
func TestAFailedWriteLeavesThePreviousBoardStanding(t *testing.T) {
	root := fixtureVault(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "board.html")
	if got := invoke(t, root, "2026-09-02T12:00", false, "board", "--html", out); got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	good, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// A path that cannot be written is the failure a filesystem actually
	// produces; the board that already exists must not be touched by it.
	blocked := filepath.Join(dir, "locked")
	if err := os.Mkdir(blocked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	got := invoke(t, root, "2026-09-04T12:00", false, "board", "--html", filepath.Join(blocked, "board.html"))
	if got.Code == exitOK {
		t.Skip("this filesystem lets an unwritable directory be written to")
	}
	if got.Stderr == "" {
		t.Error("a failed write said nothing")
	}
	after, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(good) {
		t.Error("a failed write to one path disturbed the board at another")
	}
}

// TestDoctorReportsABoardThatFailsItsContract is the diagnostic half of the
// board's contract: a hand-edited or older page is named with the line that is
// wrong, and the page itself is left alone.
func TestDoctorReportsABoardThatFailsItsContract(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(string) string
		want    string
	}{
		{"unknown pane", func(s string) string {
			return strings.Replace(s, `"key": "tasks"`, `"key": "invoices"`, 1)
		}, "the pane set is fixed in this order"},
		{"wrong schema", func(s string) string {
			return strings.Replace(s, `"brain-board.v1"`, `"brain-board.v0"`, 1)
		}, "unsupported board schema"},
		{"missing empty state", func(s string) string {
			return strings.Replace(s, `"empty": "no task is outstanding"`, `"empty": ""`, 1)
		}, "empty-state string"},
		{"not json at all", func(s string) string {
			return strings.Replace(s, `"schema": "brain-board.v1",`, `"schema": ,`, 1)
		}, "not valid JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureVault(t)
			out := filepath.Join(t.TempDir(), "board.html")
			configure(t, root, "board_html: "+out)
			if got := invoke(t, root, "2026-09-02T12:00", false, "board"); got.Code != exitOK {
				t.Fatalf("exit %d: %s", got.Code, got.Stderr)
			}
			page, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			broken := tc.corrupt(string(page))
			if broken == string(page) {
				t.Fatal("the corruption did not apply; the page shape changed")
			}
			if err := os.WriteFile(out, []byte(broken), 0o644); err != nil {
				t.Fatal(err)
			}
			got := invoke(t, root, "2026-09-02T12:00", false, "doctor", "--json")
			if got.Code != exitOK {
				t.Fatalf("doctor: exit %d: %s", got.Code, got.Stderr)
			}
			var rep doctorReport
			if err := json.Unmarshal([]byte(got.Stdout), &rep); err != nil {
				t.Fatal(err)
			}
			var boardRow doctorRow
			for _, row := range rep.Rows {
				if row.Name == "board" {
					boardRow = row
				}
			}
			if boardRow.Name == "" {
				t.Fatal("doctor has no board check")
			}
			if boardRow.OK {
				t.Errorf("doctor reported a broken board as ok: %q", boardRow.Detail)
			}
			joined := strings.Join(rep.Attention, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("doctor does not explain the problem: %s", joined)
			}
			if !strings.Contains(joined, out+":") {
				t.Errorf("doctor does not name the file and the line: %s", joined)
			}
			// doctor reads; it never repairs.
			again, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != broken {
				t.Error("doctor rewrote the board it was asked to check")
			}
		})
	}
}

// TestABoardPayloadSurvivesAHostileNote through the CLI: a captured note with a
// closing script tag in it cannot terminate the data block.
func TestABoardPayloadSurvivesAHostileNote(t *testing.T) {
	root := fixtureVault(t)
	const hostile = `an idea </script><script>alert(1)</script> about escaping`
	if got := invoke(t, root, "2026-09-02T12:00", false,
		"add", "idea", hostile, "--id", "hostile"); got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	out := filepath.Join(t.TempDir(), "board.html")
	if got := invoke(t, root, "2026-09-02T12:00", false, "board", "--html", out); got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	page, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := payload.Extract(string(page), board.Slot)
	if err != nil {
		t.Fatalf("the hostile note broke the data block open: %v", err)
	}
	if strings.Contains(string(body), "<") {
		t.Errorf("the payload carries a literal '<':\n%s", body)
	}
	var carried board.Model
	if err := json.Unmarshal(body, &carried); err != nil {
		t.Fatalf("the escaped payload is not valid JSON: %v", err)
	}
	found := false
	for _, pane := range carried.Panes {
		for _, row := range pane.Rows {
			if row.ID == "hostile" && row.Title == hostile {
				found = true
			}
		}
	}
	if !found {
		t.Error("escaping altered the captured note")
	}
}

// TestRecapReachesNothingWithoutVerifyForge: the flag is the only part of the
// command that touches a network, and it is opt-in.
func TestRecapReachesNothingWithoutVerifyForge(t *testing.T) {
	root := fixtureVault(t)
	f := useForge(t, &scriptedForge{fail: map[string]error{
		"gh":   errors.New("gh must not run here"),
		"glab": errors.New("glab must not run here"),
	}})
	for _, args := range [][]string{
		{"recap", "week"}, {"recap", "month", "--json"}, {"recap", "quarter"},
		{"recap", "--from", "2026-08-01", "--to", "2026-09-30"},
	} {
		got := invoke(t, root, "2026-09-02T12:00", false, args...)
		if got.Code != exitOK {
			t.Fatalf("%v: exit %d: %s", args, got.Code, got.Stderr)
		}
		if len(f.calls) != 0 {
			t.Fatalf("%v reached a forge: %v", args, f.calls)
		}
	}
	// Asked to check, it checks, and reports the drift it finds.
	useForge(t, &scriptedForge{out: map[string]string{
		"glab": `{"title":"migrate","state":"merged","draft":false,"head_pipeline":{"status":"success"}}`,
	}})
	got := invoke(t, root, "2026-09-02T12:00", false, "recap", "month", "--verify-forge", "--json")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	var payloadObj struct {
		Recap struct {
			Verified bool `json:"verified"`
			Drift    []struct {
				ID       string `json:"id"`
				Recorded string `json:"recorded"`
				Live     string `json:"live"`
			} `json:"drift"`
		} `json:"recap"`
	}
	if err := json.Unmarshal([]byte(got.Stdout), &payloadObj); err != nil {
		t.Fatal(err)
	}
	if !payloadObj.Recap.Verified {
		t.Error("--verify-forge did not mark the recap verified")
	}
	if len(payloadObj.Recap.Drift) != 1 || payloadObj.Recap.Drift[0].Live != "merged/passing" {
		t.Errorf("drift = %+v", payloadObj.Recap.Drift)
	}
}

// TestTheRecapPageAndTheFrameCarryTheSameModel: one assembly path, two
// renderers, and the page carries the model the frame was laid out from.
func TestTheRecapPageAndTheFrameCarryTheSameModel(t *testing.T) {
	root := fixtureVault(t)
	out := filepath.Join(t.TempDir(), "recap.html")
	got := invoke(t, root, "2026-09-02T12:00", false, "recap", "month", "--html", out, "--json")
	if got.Code != exitOK {
		t.Fatalf("exit %d: %s", got.Code, got.Stderr)
	}
	var emitted struct {
		Recap json.RawMessage `json:"recap"`
	}
	if err := json.Unmarshal([]byte(got.Stdout), &emitted); err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	body, line, err := payload.Extract(string(page), recap.Slot)
	if err != nil {
		t.Fatal(err)
	}
	fromPage, err := recap.Validate(out, body, line)
	if err != nil {
		t.Fatal(err)
	}
	var fromJSON recap.Model
	if err := json.Unmarshal(emitted.Recap, &fromJSON); err != nil {
		t.Fatal(err)
	}
	if fromPage.Generated != fromJSON.Generated || len(fromPage.Blocks) != len(fromJSON.Blocks) {
		t.Fatalf("the page and the JSON disagree:\n%+v\n%+v", fromPage, fromJSON)
	}
	for i := range fromPage.Blocks {
		if fromPage.Blocks[i].Key != fromJSON.Blocks[i].Key {
			t.Errorf("block %d differs: %q and %q", i, fromPage.Blocks[i].Key, fromJSON.Blocks[i].Key)
		}
	}
}
