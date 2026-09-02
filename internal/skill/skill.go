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
	// Dir installs into an explicit skills directory.
	Dir string
}

// Target is one place the skill will be written.
type Target struct {
	// Agent names the agent, for reporting.
	Agent string
	// Root is the skills directory; the skill goes in Root/secondbrain.
	Root string
}

// knownAgents are the agent skill directories this tool looks for, relative to
// the home directory.
var knownAgents = []struct {
	agent string
	rel   string
}{
	{"claude", filepath.Join(".claude", "skills")},
	{"codex", filepath.Join(".codex", "skills")},
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
		asked := (a.agent == "claude" && c.Claude) || (a.agent == "codex" && c.Codex)
		root := filepath.Join(home, a.rel)
		if asked {
			out = append(out, Target{Agent: a.agent, Root: root})
			continue
		}
		if c.Claude || c.Codex {
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
			dirs = append(dirs, filepath.Join(home, a.rel))
		}
		return nil, fmt.Errorf("no agent skills directory found (looked in %s); name one with %s or --dir <path>",
			strings.Join(dirs, " and "), strings.Join(names, " or "))
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
		path := filepath.Join(home, a.rel, Name)
		data, err := os.ReadFile(filepath.Join(path, "SKILL.md"))
		if err != nil {
			continue
		}
		out = append(out, Found{Agent: a.agent, Path: path, Stale: fingerprint(data) != Fingerprint()})
	}
	return out
}
