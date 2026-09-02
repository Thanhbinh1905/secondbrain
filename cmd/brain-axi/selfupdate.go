package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnvSource names the environment variable holding the tool's source checkout.
// install.sh writes it into the recorded install metadata; it can also be set
// by hand.
const EnvSource = "BRAIN_AXI_SOURCE"

// sourceFileName is the file install.sh drops beside the binary recording
// where the checkout it was built from lives.
const sourceFileName = ".brain-axi-source"

// cmdSelfUpdate rebuilds the binary from its own source checkout and replaces
// itself, verifying the replacement runs before it stands (FR-13).
//
// The upgrade fast-forwards the tracked half of the repository. `vault/` is
// gitignored there and is never touched by any of this.
//
// The binary itself links no network client: the fetch is delegated to `git`,
// and only when the user explicitly asks for an upgrade. The query and
// capture paths open no socket at all (NFR-2).
func (a *app) cmdSelfUpdate() error {
	if err := a.requireArgs(0, "update"); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding this binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	source, err := a.resolveSource(self)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return usageError("git is not on PATH, so the source checkout at %s cannot be updated", source)
	}
	if _, err := exec.LookPath("go"); err != nil {
		return usageError("go is not on PATH, so a new binary cannot be built from %s", source)
	}

	before, err := gitHead(source)
	if err != nil {
		return err
	}
	if dirty, err := gitDirty(source); err != nil {
		return err
	} else if dirty {
		return usageError("the source checkout at %s has uncommitted changes; commit or stash them before upgrading", source)
	}

	if a.has("check") {
		return a.reportUpdateCheck(source, self, before)
	}

	if out, err := exec.Command("git", "-C", source, "pull", "--ff-only", "--quiet").CombinedOutput(); err != nil {
		return fmt.Errorf("git pull --ff-only in %s failed: %v: %s", source, err, strings.TrimSpace(string(out)))
	}
	after, err := gitHead(source)
	if err != nil {
		return err
	}

	// Build beside the target so the replacing rename is atomic on one device.
	tmp, err := os.CreateTemp(filepath.Dir(self), ".brain-axi.new-*")
	if err != nil {
		return fmt.Errorf("creating a temporary file next to %s: %w", self, err)
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	build := exec.Command("go", "build",
		"-ldflags", "-X main.version="+after[:min(len(after), 12)],
		"-o", tmpName, "./cmd/brain-axi")
	build.Dir = source
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("building from %s failed: %v: %s", source, err, strings.TrimSpace(string(out)))
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("making the new binary executable: %w", err)
	}

	// Verify the replacement runs before it replaces anything.
	verify, err := exec.Command(tmpName, "--version").Output()
	if err != nil {
		return fmt.Errorf("the newly built binary does not run, so nothing was replaced: %w", err)
	}
	newVersion := strings.TrimSpace(string(verify))
	if !strings.HasPrefix(newVersion, "brain-axi ") {
		return fmt.Errorf("the newly built binary reported %q instead of a version, so nothing was replaced", newVersion)
	}
	if err := os.Rename(tmpName, self); err != nil {
		return fmt.Errorf("replacing %s: %w", self, err)
	}

	if a.out.JSON {
		return a.out.Emit(map[string]any{
			"binary": self, "source": source,
			"from_commit": before, "to_commit": after,
			"from_version": version, "to_version": strings.TrimPrefix(newVersion, "brain-axi "),
			"changed": before != after,
		})
	}
	a.out.Scalar("binary", self)
	a.out.Scalar("source", source)
	a.out.Scalar("version", version+" -> "+strings.TrimPrefix(newVersion, "brain-axi "))
	if before == after {
		a.out.Scalar("commit", before[:min(len(before), 12)]+" (already current; rebuilt anyway)")
	} else {
		a.out.Scalar("commit", before[:min(len(before), 12)]+" -> "+after[:min(len(after), 12)])
	}
	a.out.Attention([]string{"the vault was not touched: it is gitignored by the tool repository and lives in its own"})
	a.out.Help([]string{
		"Run `brain-axi doctor` to confirm the new binary sees the vault",
		"Run `brain-axi setup skill` to refresh the agent skill",
	})
	return nil
}

func (a *app) reportUpdateCheck(source, self, head string) error {
	if out, err := exec.Command("git", "-C", source, "fetch", "--quiet").CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch in %s failed: %v: %s", source, err, strings.TrimSpace(string(out)))
	}
	upstream, err := exec.Command("git", "-C", source, "rev-parse", "@{upstream}").Output()
	if err != nil {
		return usageError("the source checkout at %s has no upstream branch to compare against", source)
	}
	remote := strings.TrimSpace(string(upstream))
	behind := head != remote
	if a.out.JSON {
		return a.out.Emit(map[string]any{
			"binary": self, "source": source, "version": version,
			"local_commit": head, "upstream_commit": remote, "update_available": behind,
		})
	}
	a.out.Scalar("binary", self)
	a.out.Scalar("source", source)
	a.out.Scalar("version", version)
	a.out.Scalar("local", head[:min(len(head), 12)])
	a.out.Scalar("upstream", remote[:min(len(remote), 12)])
	if behind {
		a.out.Attention([]string{"an update is available; run `brain-axi update`"})
	} else {
		a.out.Scalar("state", "already current")
	}
	return nil
}

// resolveSource finds the checkout this binary was built from: the explicit
// flag, then the environment, then the file install.sh recorded, then the
// repository the binary is sitting inside.
func (a *app) resolveSource(self string) (string, error) {
	var tried []string
	candidates := []string{a.flagOr("source", ""), os.Getenv(EnvSource)}
	if recorded, err := os.ReadFile(filepath.Join(filepath.Dir(self), sourceFileName)); err == nil {
		candidates = append(candidates, strings.TrimSpace(string(recorded)))
	}
	// A binary run straight out of a checkout, such as a `go build` in place.
	candidates = append(candidates, filepath.Dir(self))
	for _, c := range candidates {
		if c == "" {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		tried = append(tried, abs)
		if isToolCheckout(abs) {
			return abs, nil
		}
		// Walk up: the binary may sit in a subdirectory of the checkout.
		dir := abs
		for {
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
			if isToolCheckout(dir) {
				return dir, nil
			}
		}
	}
	return "", usageError("cannot find the brain-axi source checkout; tried %s. Set $%s to the clone, or pass --source <path>",
		strings.Join(tried, ", "), EnvSource)
}

// isToolCheckout reports whether dir is a clone of this tool's repository.
func isToolCheckout(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	return strings.Contains(string(mod), "module github.com/Thanhbinh1905/secondbrain")
}

func gitHead(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("reading HEAD of %s: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitDirty(dir string) (bool, error) {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("reading the status of %s: %w", dir, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
