package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// homeAt points os.UserHomeDir at a temporary directory, so a test never reads
// or writes the developer's own agent directories.
func homeAt(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvPiAgentDir, "")
	return home
}

func rootOf(t *testing.T, targets []Target, agent string) string {
	t.Helper()
	for _, tg := range targets {
		if tg.Agent == agent {
			return tg.Root
		}
	}
	t.Fatalf("no target for %q in %+v", agent, targets)
	return ""
}

// TestPiIsAFirstClassAgent: installing the skill for pi used to mean finding
// pi's own skills path by hand and passing --dir, which `doctor` then could not
// see - so a correct installation was indistinguishable from a missing one.
func TestPiIsAFirstClassAgent(t *testing.T) {
	home := homeAt(t)
	targets, err := Targets(Choice{Pi: true})
	if err != nil {
		t.Fatalf("Targets(--pi): %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("--pi resolved %d targets: %+v", len(targets), targets)
	}
	want := filepath.Join(home, ".pi", "agent", "skills")
	if got := rootOf(t, targets, "pi"); got != want {
		t.Errorf("--pi resolved %s, want %s", got, want)
	}
	// Naming one agent must not drag the others in.
	for _, tg := range targets {
		if tg.Agent != "pi" {
			t.Errorf("--pi also installed for %s", tg.Agent)
		}
	}
}

// TestPiHonoursItsAgentDirOverride: pi resolves its skills directory under its
// agent directory, and $PI_CODING_AGENT_DIR moves that directory. Hardcoding
// ~/.pi/agent would write the skill somewhere the agent that asked for it does
// not read.
func TestPiHonoursItsAgentDirOverride(t *testing.T) {
	homeAt(t)
	elsewhere := t.TempDir()
	t.Setenv(EnvPiAgentDir, elsewhere)
	targets, err := Targets(Choice{Pi: true})
	if err != nil {
		t.Fatalf("Targets(--pi): %v", err)
	}
	want := filepath.Join(elsewhere, "skills")
	if got := rootOf(t, targets, "pi"); got != want {
		t.Errorf("$%s ignored: resolved %s, want %s", EnvPiAgentDir, got, want)
	}
}

func TestPiAgentDirOverridePreservesWhitespace(t *testing.T) {
	home := homeAt(t)
	dir := " " + filepath.Join(home, "pi-agent") + " "
	t.Setenv(EnvPiAgentDir, dir)
	targets, err := Targets(Choice{Pi: true})
	if err != nil {
		t.Fatalf("Targets(--pi): %v", err)
	}
	want := filepath.Join(dir, "skills")
	if got := rootOf(t, targets, "pi"); got != want {
		t.Errorf("$%s whitespace changed: resolved %q, want %q", EnvPiAgentDir, got, want)
	}
}

// TestDoctorSeesAnInstalledPiSkill: reporting is the half that makes the
// install worth having. An installed copy has to be visible, and a stale one
// has to be visible as stale.
func TestDoctorSeesAnInstalledPiSkill(t *testing.T) {
	homeAt(t)
	elsewhere := t.TempDir()
	t.Setenv(EnvPiAgentDir, elsewhere)
	targets, err := Targets(Choice{Pi: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(targets[0]); err != nil {
		t.Fatal(err)
	}
	found := Installed()
	if len(found) != 1 || found[0].Agent != "pi" {
		t.Fatalf("Installed() = %+v", found)
	}
	if found[0].Stale {
		t.Error("a copy just installed reports as stale")
	}
	if found[0].Path != filepath.Join(elsewhere, "skills", Name) {
		t.Errorf("reported %s", found[0].Path)
	}
	// An older copy is reported as older rather than as present.
	if err := os.WriteFile(filepath.Join(elsewhere, "skills", Name, "SKILL.md"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found = Installed(); len(found) != 1 || !found[0].Stale {
		t.Errorf("an older copy is not reported as stale: %+v", found)
	}
}

// TestAutoDetectionFindsPi: with no agent named, setup installs into every
// agent directory that already exists, and pi has to be one of them.
func TestAutoDetectionFindsPi(t *testing.T) {
	home := homeAt(t)
	if err := os.MkdirAll(filepath.Join(home, ".pi", "agent", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	targets, err := Targets(Choice{})
	if err != nil {
		t.Fatalf("Targets(): %v", err)
	}
	if len(targets) != 1 || targets[0].Agent != "pi" {
		t.Fatalf("auto-detection found %+v", targets)
	}
}

// TestNoAgentFoundNamesPi: the refusal is the only place a user learns which
// agents exist, so leaving pi out of it is how the gap stayed invisible.
func TestNoAgentFoundNamesPi(t *testing.T) {
	home := homeAt(t)
	_, err := Targets(Choice{})
	if err == nil {
		t.Fatal("Targets() found an agent where none exists")
	}
	msg := err.Error()
	for _, want := range []string{"--pi", filepath.Join(home, ".pi", "agent", "skills"), "--claude", "--codex"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not name %q:\n%s", want, msg)
		}
	}
}

// TestATildeInPiAgentDirIsExpanded: pi expands a leading ~ in that variable, so
// brain-axi has to as well. Writing to a directory literally named "~" is the
// worst outcome available here - the install reports success, doctor reports it
// as present, and pi never sees it.
func TestATildeInPiAgentDirIsExpanded(t *testing.T) {
	home := homeAt(t)
	t.Setenv(EnvPiAgentDir, filepath.Join("~", "custom"))
	targets, err := Targets(Choice{Pi: true})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "custom", "skills")
	if got := rootOf(t, targets, "pi"); got != want {
		t.Errorf("resolved %s, want %s", got, want)
	}
}

// TestEveryKnownAgentIsReachable: an agent in the table whose flag nothing maps
// to is worse than an absent one - `setup skill --that` would report success
// having installed nothing, which is exactly the invisibility this table exists
// to end. Choice.wants must answer for every entry.
func TestEveryKnownAgentIsReachable(t *testing.T) {
	home := homeAt(t)
	for _, a := range knownAgents {
		var c Choice
		switch a.agent {
		case "claude":
			c.Claude = true
		case "codex":
			c.Codex = true
		case "pi":
			c.Pi = true
		default:
			t.Errorf("%q is in knownAgents but this test does not know how to name it", a.agent)
			continue
		}
		if !c.wants(a.agent) {
			t.Errorf("Choice.wants(%q) is false when the flag is set", a.agent)
		}
		if !c.namedAny() {
			t.Errorf("Choice.namedAny() is false with %q named", a.agent)
		}
		targets, err := Targets(c)
		if err != nil {
			t.Errorf("Targets(--%s): %v", a.agent, err)
			continue
		}
		if len(targets) != 1 || targets[0].Agent != a.agent {
			t.Errorf("--%s resolved %+v", a.agent, targets)
		}
		if a.root(home) == "" {
			t.Errorf("%q resolves an empty root", a.agent)
		}
	}
}
