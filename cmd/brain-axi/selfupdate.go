package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// modulePath is this tool's module path, which is also the repository the
// release assets are published from.
const modulePath = "github.com/Thanhbinh1905/secondbrain"

// EnvSource names the environment variable holding the tool's source checkout.
// install.sh writes it into the recorded install metadata; it can also be set
// by hand.
const EnvSource = "BRAIN_AXI_SOURCE"

// sourceFileName is the file install.sh drops beside the binary recording
// where the checkout it was built from lives.
const sourceFileName = ".brain-axi-source"

// methodFileName is the file install.sh drops beside the binary naming how it
// installed. The method is recorded rather than inferred, because guessing it
// means either replacing a binary the user did not install that way or telling
// them to run a command that cannot work.
const methodFileName = ".brain-axi-install"

// installMethod is how this binary got onto the machine, which is what decides
// what an upgrade even means.
type installMethod string

const (
	// methodCheckout was built from a git clone, by install.sh or in place.
	// An upgrade fast-forwards that clone and rebuilds from it.
	methodCheckout installMethod = "checkout"
	// methodRelease was downloaded from a published release by install.sh. An
	// upgrade downloads the newest one and verifies it the same way.
	methodRelease installMethod = "release"
	// methodGoInstall came from `go install`, which owns the binary and its
	// upgrade. There is nothing here to fast-forward and nothing to download.
	methodGoInstall installMethod = "go-install"
)

// goInstallCommand is what upgrades a `go install` installation. brain-axi
// prints it rather than running it: the Go toolchain placed that binary and
// knows where it goes, and a tool that shells out to replace itself behind the
// user's back is doing something the user can do plainly themselves.
const goInstallCommand = "go install " + modulePath + "/cmd/brain-axi@latest"

// executablePath is os.Executable, indirected so a test can point an upgrade at
// a throwaway file instead of at the test binary that is running it.
var executablePath = os.Executable

// cmdSelfUpdate upgrades this binary and verifies the replacement runs before
// it stands (FR-13). What "upgrade" means depends on how the binary was
// installed, so the recorded method is resolved first and never guessed.
//
// The binary itself links no network client: a checkout upgrade delegates its
// fetch to `git` and a release upgrade delegates its download to `curl` or
// `wget`, and only when the user explicitly asks for an upgrade. The query and
// capture paths open no socket at all (NFR-2).
func (a *app) cmdSelfUpdate() error {
	if err := a.requireArgs(0, "update"); err != nil {
		return err
	}
	self, err := executablePath()
	if err != nil {
		return fmt.Errorf("finding this binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	method, err := a.resolveMethod(self)
	if err != nil {
		return err
	}
	switch method {
	case methodGoInstall:
		return a.reportGoInstall(self)
	case methodRelease:
		return a.updateFromRelease(self)
	default:
		return a.updateFromCheckout(self)
	}
}

// resolveMethod reports how this binary was installed.
//
// An explicit --source or $BRAIN_AXI_SOURCE is the user naming a checkout, and
// outranks any record. Otherwise the record install.sh wrote decides, and a
// record that names something unknown or cannot be read is refused rather than
// guessed past. With no method record, a binary that install.sh recorded a
// checkout for, or that sits inside a clone of this repository, is a checkout
// install from before the method was recorded; anything else is a `go install`.
func (a *app) resolveMethod(self string) (installMethod, error) {
	if a.flagOr("source", "") != "" || os.Getenv(EnvSource) != "" {
		return methodCheckout, nil
	}
	dir := filepath.Dir(self)
	path := filepath.Join(dir, methodFileName)
	recorded, err := os.ReadFile(path)
	switch {
	case err == nil:
		switch method := installMethod(strings.TrimSpace(string(recorded))); method {
		case methodCheckout, methodRelease, methodGoInstall:
			return method, nil
		default:
			return "", usageError("%s records the install method as %q, which this binary does not know how to upgrade; reinstall to rewrite it, or delete it if this binary came from `go install`", path, method)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", usageError("reading the install method from %s: %v", path, err)
	}
	// install.sh recorded a checkout before it recorded a method, and a binary
	// built in place inside a clone has no record at all. Both are checkout
	// installs, and both keep upgrading the way they always have.
	if _, err := os.Stat(filepath.Join(dir, sourceFileName)); err == nil {
		return methodCheckout, nil
	}
	if _, err := a.resolveSource(self); err == nil {
		return methodCheckout, nil
	}
	return methodGoInstall, nil
}

// reportGoInstall says what upgrades this installation and exits successfully,
// because nothing is wrong: the user has a working binary and the one command
// that replaces it.
func (a *app) reportGoInstall(self string) error {
	if a.out.JSON {
		return a.out.Emit(map[string]any{
			"binary": self, "method": string(methodGoInstall),
			"version": buildVersion, "command": goInstallCommand,
			"changed": false,
		})
	}
	a.out.Scalar("binary", self)
	a.out.Scalar("method", string(methodGoInstall))
	a.out.Scalar("version", buildVersion)
	a.out.Scalar("upgrade", goInstallCommand)
	a.out.Help([]string{
		"Run `" + goInstallCommand + "` to upgrade this installation",
		"The Go toolchain installed this binary and is what replaces it",
	})
	return nil
}

// updateFromCheckout fast-forwards the recorded checkout, rebuilds, and
// replaces this binary with a build that runs.
//
// The upgrade fast-forwards the tracked half of the repository. `vault/` is
// gitignored there and is never touched by any of this.
func (a *app) updateFromCheckout(self string) error {
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
	tmpName, err := stagingFile(self)
	if err != nil {
		return err
	}
	defer os.Remove(tmpName)

	build := exec.Command("go", "build",
		"-ldflags", "-X main.version="+after[:min(len(after), shortRevision)],
		"-o", tmpName, "./cmd/brain-axi")
	build.Dir = source
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("building from %s failed: %v: %s", source, err, strings.TrimSpace(string(out)))
	}

	newVersion, err := installReplacement(self, tmpName, "newly built")
	if err != nil {
		return err
	}

	if a.out.JSON {
		return a.out.Emit(map[string]any{
			"binary": self, "method": string(methodCheckout), "source": source,
			"from_commit": before, "to_commit": after,
			"from_version": buildVersion, "to_version": newVersion,
			"changed": before != after,
		})
	}
	a.out.Scalar("binary", self)
	a.out.Scalar("method", string(methodCheckout))
	a.out.Scalar("source", source)
	a.out.Scalar("version", buildVersion+" -> "+newVersion)
	if before == after {
		a.out.Scalar("commit", before[:min(len(before), shortRevision)]+" (already current; rebuilt anyway)")
	} else {
		a.out.Scalar("commit", before[:min(len(before), shortRevision)]+" -> "+after[:min(len(after), shortRevision)])
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
			"binary": self, "method": string(methodCheckout), "source": source, "version": buildVersion,
			"local_commit": head, "upstream_commit": remote, "update_available": behind,
		})
	}
	a.out.Scalar("binary", self)
	a.out.Scalar("method", string(methodCheckout))
	a.out.Scalar("source", source)
	a.out.Scalar("version", buildVersion)
	a.out.Scalar("local", head[:min(len(head), shortRevision)])
	a.out.Scalar("upstream", remote[:min(len(remote), shortRevision)])
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
	return strings.Contains(string(mod), "module "+modulePath)
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

// releaseAssets maps a Go platform to the asset name a release publishes for
// it. The names are shaped exactly like `uname -s` and `uname -m` output, so
// install.sh computes one by string interpolation and never carries a platform
// table it has to keep in sync. This map and the release workflow's build step
// are the only two places that shape lives, and TestReleaseAssetsMatchWorkflow
// keeps them identical.
var releaseAssets = map[string]string{
	"linux/amd64":  "brain-axi_Linux_x86_64",
	"linux/arm64":  "brain-axi_Linux_aarch64",
	"darwin/amd64": "brain-axi_Darwin_x86_64",
	"darwin/arm64": "brain-axi_Darwin_arm64",
}

// releaseDownloadBase is where a release's assets live. GitHub redirects
// `releases/latest/download/<name>` to the newest published release, so the
// newest release is addressable without an API call, a token or a rate limit.
const releaseDownloadBase = "https://" + modulePath + "/releases/latest/download/"

// checksumsName is the manifest every release publishes: `sha256sum` format,
// one line per binary. It is the authoritative list of published platforms as
// well as the thing a download is verified against.
const checksumsName = "checksums.txt"

// versionName is the one-line asset holding a release's tag, so `update
// --check` can answer without downloading a whole binary.
const versionName = "version.txt"

// downloadTimeout bounds one download. A release binary is a few megabytes; a
// fetch still running after this is a stall, not a slow link.
const downloadTimeout = 5 * time.Minute

// fetcher runs a download CLI. brain-axi links no network client and holds no
// token (NFR-2): every byte that crosses the network is fetched by a program
// the user already has, the same way forge status is fetched by gh or glab and
// a checkout upgrade by git. Production uses execFetcher; a test supplies its
// own, so the suite never reaches a network.
type fetcher interface {
	// Look reports whether a command exists on PATH, returning its path.
	Look(name string) (string, error)
	// Run executes a command and returns its stdout. The error carries the
	// command's own stderr, because that is where a download tool explains
	// itself.
	Run(name string, args ...string) (stdout []byte, err error)
}

type execFetcher struct{ timeout time.Duration }

func (execFetcher) Look(name string) (string, error) { return exec.LookPath(name) }

func (f execFetcher) Run(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), f.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return out, fmt.Errorf("%s gave no answer within %s", name, f.timeout)
		}
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return out, fmt.Errorf("%s failed: %v: %s", name, err, detail)
		}
		return out, fmt.Errorf("%s failed: %w", name, err)
	}
	return out, nil
}

// fetch is the download delegation that actually reaches the network.
var fetch fetcher = execFetcher{timeout: downloadTimeout}

// downloadTo fetches url into path with whichever download CLI is installed.
// Both are asked to fail loudly on an HTTP error rather than writing the error
// page to disk, and both follow the redirect that `releases/latest` is.
func downloadTo(url, path string) error {
	if _, err := fetch.Look("curl"); err == nil {
		_, err := fetch.Run("curl", "--fail", "--silent", "--show-error", "--location", "--output", path, url)
		return err
	}
	if _, err := fetch.Look("wget"); err == nil {
		_, err := fetch.Run("wget", "--quiet", "--output-document", path, url)
		return err
	}
	return usageError("neither curl nor wget is on PATH, so a release cannot be downloaded; brain-axi links no network client of its own and delegates every download (NFR-2)")
}

// updateFromRelease downloads the newest published binary for this platform,
// verifies it against the published checksum manifest, verifies that it runs,
// and only then replaces this one.
func (a *app) updateFromRelease(self string) error {
	asset, ok := releaseAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return usageError("no brain-axi release is published for %s/%s; the published platforms are %s",
			runtime.GOOS, runtime.GOARCH, strings.Join(publishedPlatforms(), ", "))
	}

	work, err := os.MkdirTemp("", "brain-axi-release-")
	if err != nil {
		return fmt.Errorf("creating a temporary directory for the download: %w", err)
	}
	defer os.RemoveAll(work)

	if a.has("check") {
		return a.reportReleaseCheck(self, work)
	}

	manifestPath := filepath.Join(work, checksumsName)
	if err := downloadTo(releaseDownloadBase+checksumsName, manifestPath); err != nil {
		return releaseFetchError(checksumsName, err)
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("reading the downloaded checksum manifest: %w", err)
	}
	// The manifest is the release's own list of what it published, so an asset
	// it does not vouch for is refused before it is fetched rather than after.
	want, err := publishedSum(manifest, asset)
	if err != nil {
		return err
	}

	// Download beside the target so the replacing rename is atomic on one
	// device, and so a half-written download never lands on the real path.
	tmpName, err := stagingFile(self)
	if err != nil {
		return err
	}
	defer os.Remove(tmpName)

	if err := downloadTo(releaseDownloadBase+asset, tmpName); err != nil {
		return releaseFetchError(asset, err)
	}
	if err := verifyChecksum(want, asset, tmpName); err != nil {
		return err
	}

	newVersion, err := installReplacement(self, tmpName, "downloaded")
	if err != nil {
		return err
	}

	if a.out.JSON {
		return a.out.Emit(map[string]any{
			"binary": self, "method": string(methodRelease), "asset": asset,
			"from_version": buildVersion, "to_version": newVersion,
			"changed": buildVersion != newVersion,
		})
	}
	a.out.Scalar("binary", self)
	a.out.Scalar("method", string(methodRelease))
	a.out.Scalar("asset", asset)
	a.out.Scalar("version", buildVersion+" -> "+newVersion)
	a.out.Attention([]string{"the vault was not touched: it lives in its own git repository"})
	a.out.Help([]string{
		"Run `brain-axi doctor` to confirm the new binary sees the vault",
		"Run `brain-axi setup skill` to refresh the agent skill",
	})
	return nil
}

// reportReleaseCheck says whether the newest published release differs from
// this binary, without downloading it. The comparison is against the tag the
// release published, so a binary stamped from a checkout rather than a tag
// always reads as different - which is honest: it is not that release.
func (a *app) reportReleaseCheck(self, work string) error {
	path := filepath.Join(work, versionName)
	if err := downloadTo(releaseDownloadBase+versionName, path); err != nil {
		return releaseFetchError(versionName, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading the downloaded release version: %w", err)
	}
	latest := strings.TrimSpace(string(raw))
	if latest == "" {
		return usageError("the published %s is empty, so there is no version to compare against", versionName)
	}
	behind := latest != buildVersion
	if a.out.JSON {
		return a.out.Emit(map[string]any{
			"binary": self, "method": string(methodRelease), "version": buildVersion,
			"latest_release": latest, "update_available": behind,
		})
	}
	a.out.Scalar("binary", self)
	a.out.Scalar("method", string(methodRelease))
	a.out.Scalar("version", buildVersion)
	a.out.Scalar("latest", latest)
	if behind {
		a.out.Attention([]string{"an update is available; run `brain-axi update`"})
	} else {
		a.out.Scalar("state", "already current")
	}
	return nil
}

// releaseFetchError names the failure a user is most likely to hit first: a
// repository whose releases page is empty, where every asset URL is a 404.
func releaseFetchError(what string, err error) error {
	return usageError("downloading %s from %s failed: %v. If %s has no published release yet there is nothing to upgrade to; nothing was replaced",
		what, releaseDownloadBase, err, modulePath)
}

// publishedSum reads the checksum a release published for one asset. An asset
// the manifest does not list is refused: there would be nothing to verify a
// download of it against, and an unverified binary is never installed.
func publishedSum(manifest []byte, asset string) (string, error) {
	var want string
	for _, line := range strings.Split(string(manifest), "\n") {
		// `sha256sum` writes "<hex>  <name>", and "<hex> *<name>" in binary
		// mode. Both name the same file.
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			want = fields[0]
		}
	}
	if want == "" {
		return "", usageError("the published %s does not list %s, so there is nothing to verify a download against; nothing was replaced", checksumsName, asset)
	}
	return want, nil
}

// verifyChecksum refuses anything that does not hash to what the release
// published. A mismatch is never repaired or retried: it means the download or
// the release is wrong, and both are things a person has to look at.
func verifyChecksum(want, asset, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening the downloaded binary: %w", err)
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return fmt.Errorf("reading the downloaded binary: %w", err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want {
		return usageError("the downloaded %s hashes to %s but the published %s says %s, so nothing was replaced",
			asset, got, checksumsName, want)
	}
	return nil
}

// publishedPlatforms lists the platforms a release publishes for, in a stable
// order, for the message a user on an unsupported one reads.
func publishedPlatforms() []string {
	out := make([]string, 0, len(releaseAssets))
	for platform := range releaseAssets {
		out = append(out, platform)
	}
	sort.Strings(out)
	return out
}

// stagingFile reserves a name beside self for the replacement to be assembled
// in, so the rename that installs it stays on one device and stays atomic.
func stagingFile(self string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(self), ".brain-axi.new-*")
	if err != nil {
		return "", fmt.Errorf("creating a temporary file next to %s: %w", self, err)
	}
	name := f.Name()
	f.Close()
	return name, nil
}

// installReplacement makes the staged binary executable, refuses it unless it
// runs and reports a version, and only then renames it over self. It is the
// last guard on every upgrade path: a binary that cannot answer --version never
// replaces a working one.
func installReplacement(self, staged, what string) (string, error) {
	if err := os.Chmod(staged, 0o755); err != nil {
		return "", fmt.Errorf("making the new binary executable: %w", err)
	}
	verify, err := exec.Command(staged, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("the %s binary does not run, so nothing was replaced: %w", what, err)
	}
	reported := strings.TrimSpace(string(verify))
	if !strings.HasPrefix(reported, "brain-axi ") {
		return "", fmt.Errorf("the %s binary reported %q instead of a version, so nothing was replaced", what, reported)
	}
	if err := os.Rename(staged, self); err != nil {
		return "", fmt.Errorf("replacing %s: %w", self, err)
	}
	return strings.TrimPrefix(reported, "brain-axi "), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
