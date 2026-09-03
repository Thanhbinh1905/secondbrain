// Package skill installs the agent-facing skill into a detected agent's skill
// directory.
//
// The skill is embedded in the binary so a single static file is the whole
// installation, and so `setup skill` cannot install a version that disagrees
// with the tool it came with.
package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Thanhbinh1905/secondbrain/skills"
)

// Name is the skill's directory name in an agent's skills tree.
const Name = "secondbrain"

// Choice is what the user asked setup to install.
type Choice struct {
	Claude bool
	Codex  bool
	Pi     bool
	// Dir installs into an explicit skills directory.
	Dir string
}

// wants reports whether the choice named this agent.
func (c Choice) wants(agent string) bool {
	switch agent {
	case "claude":
		return c.Claude
	case "codex":
		return c.Codex
	case "pi":
		return c.Pi
	}
	return false
}

// namedAny reports whether the choice named any agent at all. Naming none is
// what turns Targets into auto-detection.
func (c Choice) namedAny() bool { return c.Claude || c.Codex || c.Pi }

// Target is one place the skill will be written.
type Target struct {
	// Agent names the agent, for reporting.
	Agent string
	// Root is the skills directory; the skill goes in Root/secondbrain.
	Root string
}

// EnvPiAgentDir moves pi's agent directory, and with it the skills directory
// underneath it. pi reads it on every run, so a skill written to the default
// path while it is set lands somewhere pi never looks.
const EnvPiAgentDir = "PI_CODING_AGENT_DIR"

// knownAgents are the agent skill directories this tool looks for.
//
// Each agent resolves its own user-scope skills directory from the home
// directory, rather than declaring a path relative to it, because pi's is not
// expressible that way: it lives under an agent directory that an environment
// variable can move wholesale.
var knownAgents = []struct {
	agent string
	root  func(home string) string
}{
	{"claude", func(home string) string { return filepath.Join(home, ".claude", "skills") }},
	{"codex", func(home string) string { return filepath.Join(home, ".codex", "skills") }},
	{"pi", func(home string) string { return filepath.Join(piAgentDir(home), "skills") }},
}

// joinWords lists items as prose: "a and b", "a, b and c". A conjunction
// between every pair reads as a list of two however long it gets.
func joinWords(items []string, conj string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	last := len(items) - 1
	return strings.Join(items[:last], ", ") + " " + conj + " " + items[last]
}

// piAgentDir is pi's agent directory: $PI_CODING_AGENT_DIR when set, and
// ~/.pi/agent otherwise.
//
// A leading ~ is expanded because pi expands it too, and resolving this path
// differently from the agent that reads it is the whole defect: the skill
// would land in a directory literally named "~", pi would never see it, and
// doctor would report it as installed. This is vault.ExpandHome's rule, kept
// here rather than reached for so that installing a skill does not depend on
// the vault package.
func piAgentDir(home string) string {
	dir := strings.TrimSpace(os.Getenv(EnvPiAgentDir))
	switch {
	case dir == "":
		return filepath.Join(home, ".pi", "agent")
	case dir == "~":
		return home
	case strings.HasPrefix(dir, "~"+string(os.PathSeparator)):
		return filepath.Join(home, dir[2:])
	}
	return dir
}

// Targets resolves a choice into destinations. With nothing named, it installs
// into every agent whose directory already exists, and errors when none does
// rather than guessing where the user wants it.
func Targets(c Choice) ([]Target, error) {
	if c.Dir != "" {
		abs, err := filepath.Abs(c.Dir)
		if err != nil {
			return nil, err
		}
		return []Target{{Agent: "dir", Root: abs}}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving the home directory: %w", err)
	}
	var out []Target
	for _, a := range knownAgents {
		root := a.root(home)
		if c.wants(a.agent) {
			out = append(out, Target{Agent: a.agent, Root: root})
			continue
		}
		if c.namedAny() {
			continue
		}
		if st, err := os.Stat(root); err == nil && st.IsDir() {
			out = append(out, Target{Agent: a.agent, Root: root})
		}
	}
	if len(out) == 0 {
		var names, dirs []string
		for _, a := range knownAgents {
			names = append(names, "--"+a.agent)
			dirs = append(dirs, a.root(home))
		}
		names = append(names, "--dir <path>")
		return nil, fmt.Errorf("no agent skills directory found (looked in %s); name one with %s",
			joinWords(dirs, "and"), joinWords(names, "or"))
	}
	return out, nil
}

// Result is what one installation did.
type Result struct {
	Agent string `json:"agent"`
	Path  string `json:"path"`
	Files int    `json:"files"`
	State string `json:"state"`
}

// Install writes the skill into a target, replacing an older copy.
func Install(t Target) (Result, error) {
	dest := filepath.Join(t.Root, Name)
	state := "installed"
	if existing, err := os.ReadFile(filepath.Join(dest, "SKILL.md")); err == nil {
		if fingerprint(existing) == Fingerprint() {
			state = "already current"
		} else {
			state = "updated"
		}
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return Result{}, fmt.Errorf("creating %s: %w", dest, err)
	}
	count := 0
	err := fs.WalkDir(skills.SecondBrain, skills.SecondBrainRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(skills.SecondBrainRoot, p)
		if err != nil {
			return err
		}
		data, err := skills.SecondBrain.ReadFile(p)
		if err != nil {
			return err
		}
		out := filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", out, err)
		}
		count++
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Agent: t.Agent, Path: dest, Files: count, State: state}, nil
}

// Content is the embedded SKILL.md, which doctor and the tests read.
func Content() []byte {
	data, err := skills.SecondBrain.ReadFile(skills.SecondBrainRoot + "/SKILL.md")
	if err != nil {
		// Unreachable: the file is embedded at build time.
		panic("skill: embedded SKILL.md is missing: " + err.Error())
	}
	return data
}

// Fingerprint identifies the embedded skill, so doctor can tell an installed
// copy from an older one.
func Fingerprint() string { return fingerprint(Content()) }

func fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// Found is an installed copy of the skill.
type Found struct {
	Agent string `json:"agent"`
	Path  string `json:"path"`
	// Stale reports that the installed copy differs from this binary's.
	Stale bool `json:"stale"`
}

// Installed lists the copies of the skill already on this machine.
func Installed() []Found {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []Found
	for _, a := range knownAgents {
		path := filepath.Join(a.root(home), Name)
		data, err := os.ReadFile(filepath.Join(path, "SKILL.md"))
		if err != nil {
			continue
		}
		out = append(out, Found{Agent: a.agent, Path: path, Stale: fingerprint(data) != Fingerprint()})
	}
	return out
}
