package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestResolveVersion covers the three ways a binary can know what it is, and
// the one case where it genuinely does not. A binary that reports "dev" while
// its own build information names the commit is lying to the user, which is why
// this is a table rather than a smoke test.
func TestResolveVersion(t *testing.T) {
	info := func(mainVersion string, settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{
				Main:     debug.Module{Path: modulePath, Version: mainVersion},
				Settings: settings,
			}, true
		}
	}
	revision := debug.BuildSetting{Key: "vcs.revision", Value: "231a6c8d35dcb0f3a1e2c4d5e6f70819"}
	clean := debug.BuildSetting{Key: "vcs.modified", Value: "false"}
	dirty := debug.BuildSetting{Key: "vcs.modified", Value: "true"}

	cases := []struct {
		name    string
		stamped string
		info    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{
			name:    "ldflags win",
			stamped: "v1.2.3",
			info:    info("v0.9.0", revision, clean),
			want:    "v1.2.3",
		},
		{
			// The release workflow stamps the tag; nothing else is consulted.
			name:    "a stamped commit is still a stamp",
			stamped: "231a6c8d35dc-dirty",
			info:    info("(devel)"),
			want:    "231a6c8d35dc-dirty",
		},
		{
			// `go install ...@v1.4.0` off the module proxy.
			name:    "a released module version",
			stamped: "dev",
			info:    info("v1.4.0"),
			want:    "v1.4.0",
		},
		{
			// `go install ...@latest` against an untagged repository: the
			// pseudo-version names the commit, which is a real answer.
			name:    "a pseudo-version",
			stamped: "dev",
			info:    info("v0.0.0-20260902162935-231a6c8d35dc"),
			want:    "v0.0.0-20260902162935-231a6c8d35dc",
		},
		{
			// A plain `go build` inside a clone with a .git directory.
			name:    "vcs revision when the module version says nothing",
			stamped: "dev",
			info:    info("(devel)", revision, clean),
			want:    "231a6c8d35dc",
		},
		{
			name:    "a dirty tree is marked as one",
			stamped: "dev",
			info:    info("(devel)", revision, dirty),
			want:    "231a6c8d35dc-dirty",
		},
		{
			name:    "an empty module version is not a version",
			stamped: "dev",
			info:    info("", revision, clean),
			want:    "231a6c8d35dc",
		},
		{
			// A build in a git worktree, where the toolchain stamps no vcs
			// information at all.
			name:    "nothing recorded at all",
			stamped: "dev",
			info:    info("(devel)"),
			want:    "dev",
		},
		{
			name:    "no build information at all",
			stamped: "dev",
			info:    func() (*debug.BuildInfo, bool) { return nil, false },
			want:    "dev",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.stamped, tc.info); got != tc.want {
				t.Errorf("resolveVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveMethod is the mapping an upgrade turns on. Getting it wrong means
// either replacing a binary the user did not install that way, or telling them
// to run a command that cannot work, so every branch is pinned here.
func TestResolveMethod(t *testing.T) {
	// binaryIn returns the path of a stand-in binary inside a fresh install
	// directory holding exactly the records named.
	binaryIn := func(t *testing.T, records map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		self := filepath.Join(dir, "brain-axi")
		if err := os.WriteFile(self, []byte("not really a binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range records {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return self
	}

	t.Run("a recorded method decides", func(t *testing.T) {
		for _, want := range []installMethod{methodCheckout, methodRelease, methodGoInstall} {
			self := binaryIn(t, map[string]string{methodFileName: string(want) + "\n"})
			got, err := methodOf(t, self)
			if err != nil {
				t.Fatalf("%s: %v", want, err)
			}
			if got != want {
				t.Errorf("recorded %q, resolved %q", want, got)
			}
		}
	})

	t.Run("an unknown method is refused, never guessed", func(t *testing.T) {
		self := binaryIn(t, map[string]string{methodFileName: "homebrew\n"})
		_, err := methodOf(t, self)
		if err == nil {
			t.Fatal("an unknown install method was accepted")
		}
		for _, want := range []string{methodFileName, `"homebrew"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not name %s: %v", want, err)
			}
		}
	})

	t.Run("an unreadable record is refused, never guessed", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode 0 file, so there is nothing to refuse")
		}
		self := binaryIn(t, map[string]string{methodFileName: string(methodRelease)})
		if err := os.Chmod(filepath.Join(filepath.Dir(self), methodFileName), 0o000); err != nil {
			t.Fatal(err)
		}
		_, err := methodOf(t, self)
		if err == nil {
			t.Fatal("an unreadable install method was accepted")
		}
		if !strings.Contains(err.Error(), methodFileName) {
			t.Errorf("the refusal does not name the file: %v", err)
		}
	})

	t.Run("no record at all is a go install", func(t *testing.T) {
		self := binaryIn(t, nil)
		got, err := methodOf(t, self)
		if err != nil {
			t.Fatal(err)
		}
		if got != methodGoInstall {
			t.Errorf("resolved %q, want %q", got, methodGoInstall)
		}
	})

	t.Run("a checkout recorded before methods were is still a checkout", func(t *testing.T) {
		// install.sh wrote .brain-axi-source long before it wrote a method,
		// and those installations must keep upgrading the way they always did.
		self := binaryIn(t, map[string]string{sourceFileName: "/nowhere/secondbrain\n"})
		got, err := methodOf(t, self)
		if err != nil {
			t.Fatal(err)
		}
		if got != methodCheckout {
			t.Errorf("resolved %q, want %q", got, methodCheckout)
		}
	})

	t.Run("a binary built in place inside a clone is a checkout", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+modulePath+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		self := filepath.Join(dir, "brain-axi")
		if err := os.WriteFile(self, []byte("not really a binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := methodOf(t, self)
		if err != nil {
			t.Fatal(err)
		}
		if got != methodCheckout {
			t.Errorf("resolved %q, want %q", got, methodCheckout)
		}
	})

	t.Run("an explicit source outranks the record", func(t *testing.T) {
		self := binaryIn(t, map[string]string{methodFileName: string(methodRelease)})
		app := appFor(t, "update", "--source", "/somewhere/secondbrain")
		got, err := app.resolveMethod(self)
		if err != nil {
			t.Fatal(err)
		}
		if got != methodCheckout {
			t.Errorf("--source resolved %q, want %q", got, methodCheckout)
		}
	})

	t.Run("an explicit environment source outranks the record", func(t *testing.T) {
		self := binaryIn(t, map[string]string{methodFileName: string(methodRelease)})
		t.Setenv(EnvSource, "/somewhere/secondbrain")
		got, err := methodOf(t, self)
		if err != nil {
			t.Fatal(err)
		}
		if got != methodCheckout {
			t.Errorf("$%s resolved %q, want %q", EnvSource, got, methodCheckout)
		}
	})
}

// TestUpdateOnAGoInstallPrintsTheCommand: nothing is wrong with such a binary,
// so the command exits zero and hands over the one line that upgrades it.
func TestUpdateOnAGoInstallPrintsTheCommand(t *testing.T) {
	root := fixtureVault(t)
	self := filepath.Join(t.TempDir(), "brain-axi")
	if err := os.WriteFile(self, []byte("not really a binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	useExecutable(t, self)

	for _, args := range [][]string{{"update"}, {"update", "--check"}, {"update", "--json"}} {
		got := invoke(t, root, "2026-09-02T12:00", false, args...)
		if got.Code != exitOK {
			t.Fatalf("%v: exit %d: %s", args, got.Code, got.Stderr)
		}
		if !strings.Contains(got.Stdout, goInstallCommand) {
			t.Errorf("%v did not print the upgrade command:\n%s", args, got.Stdout)
		}
		if strings.Contains(got.Stdout, EnvSource) {
			t.Errorf("%v advised setting $%s, which cannot help here:\n%s", args, EnvSource, got.Stdout)
		}
	}
}

// TestUpdateRefusesAnUnknownInstallMethod: a record naming something this
// binary cannot upgrade is a refusal, never a fallback to another path.
func TestUpdateRefusesAnUnknownInstallMethod(t *testing.T) {
	root := fixtureVault(t)
	dir := t.TempDir()
	self := filepath.Join(dir, "brain-axi")
	if err := os.WriteFile(self, []byte("not really a binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, methodFileName), []byte("apt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	useExecutable(t, self)
	f := useFetch(t, &scriptedFetch{})

	got := invoke(t, root, "2026-09-02T12:00", false, "update")
	if got.Code != exitUsage {
		t.Fatalf("exit %d, want %d: %s", got.Code, exitUsage, got.Stderr)
	}
	if !strings.Contains(got.Stderr, `"apt"`) {
		t.Errorf("the refusal does not name what it read: %s", got.Stderr)
	}
	if len(f.calls) != 0 {
		t.Errorf("an unknown method still reached the network: %v", f.calls)
	}
}

// TestUpdateFromRelease downloads, verifies and replaces without a network: the
// download is delegated to a CLI, and the test supplies its own.
func TestUpdateFromRelease(t *testing.T) {
	asset, ok := releaseAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		t.Skipf("no release asset is published for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	replacement := "#!/bin/sh\necho 'brain-axi v9.9.9'\n"

	t.Run("a verified download replaces the binary", func(t *testing.T) {
		root := fixtureVault(t)
		self := installedBinary(t, methodRelease)
		f := useFetch(t, &scriptedFetch{files: map[string]string{
			checksumsName: manifestFor(map[string]string{asset: replacement}),
			asset:         replacement,
			versionName:   "v9.9.9\n",
		}})

		got := invoke(t, root, "2026-09-02T12:00", false, "update")
		if got.Code != exitOK {
			t.Fatalf("exit %d: %s", got.Code, got.Stderr)
		}
		if !strings.Contains(got.Stdout, "v9.9.9") {
			t.Errorf("the new version was not reported:\n%s", got.Stdout)
		}
		if content, err := os.ReadFile(self); err != nil || string(content) != replacement {
			t.Errorf("the binary was not replaced: %q, %v", content, err)
		}
		// The manifest is fetched before the asset, so a mismatch is caught
		// before the download rather than after it.
		if len(f.calls) < 2 {
			t.Fatalf("expected a manifest and an asset download, got %v", f.calls)
		}
		if !strings.HasSuffix(f.calls[0].url, "/"+checksumsName) {
			t.Errorf("the first download was %s, not the checksum manifest", f.calls[0].url)
		}
		if !strings.HasPrefix(f.calls[0].url, releaseDownloadBase) {
			t.Errorf("downloaded from %s, not the release URL", f.calls[0].url)
		}
	})

	t.Run("a checksum that does not match replaces nothing", func(t *testing.T) {
		root := fixtureVault(t)
		self := installedBinary(t, methodRelease)
		before, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		useFetch(t, &scriptedFetch{files: map[string]string{
			checksumsName: manifestFor(map[string]string{asset: "a different binary entirely"}),
			asset:         replacement,
		}})

		got := invoke(t, root, "2026-09-02T12:00", false, "update")
		if got.Code == exitOK {
			t.Fatalf("an unverified binary was installed: %s", got.Stdout)
		}
		if !strings.Contains(got.Stderr, "nothing was replaced") {
			t.Errorf("the refusal does not say nothing was replaced: %s", got.Stderr)
		}
		if after, err := os.ReadFile(self); err != nil || string(after) != string(before) {
			t.Errorf("the binary changed despite a checksum mismatch: %q, %v", after, err)
		}
		if leftovers := stagingLeftovers(t, filepath.Dir(self)); len(leftovers) != 0 {
			t.Errorf("a refused download was left behind: %v", leftovers)
		}
	})

	t.Run("an asset the manifest does not list replaces nothing", func(t *testing.T) {
		root := fixtureVault(t)
		installedBinary(t, methodRelease)
		useFetch(t, &scriptedFetch{files: map[string]string{
			checksumsName: manifestFor(map[string]string{"brain-axi_Plan9_vax": replacement}),
			asset:         replacement,
		}})

		got := invoke(t, root, "2026-09-02T12:00", false, "update")
		if got.Code == exitOK {
			t.Fatalf("an unlisted asset was installed: %s", got.Stdout)
		}
		if !strings.Contains(got.Stderr, checksumsName) {
			t.Errorf("the refusal does not name the manifest: %s", got.Stderr)
		}
	})

	t.Run("a repository with no published release says so", func(t *testing.T) {
		root := fixtureVault(t)
		installedBinary(t, methodRelease)
		useFetch(t, &scriptedFetch{fail: errors.New("The requested URL returned error: 404")})

		got := invoke(t, root, "2026-09-02T12:00", false, "update")
		if got.Code == exitOK {
			t.Fatal("a missing release was treated as a success")
		}
		if !strings.Contains(got.Stderr, "no published release") {
			t.Errorf("the refusal does not name the likely cause: %s", got.Stderr)
		}
	})

	t.Run("no download tool at all is a refusal, not a silent skip", func(t *testing.T) {
		root := fixtureVault(t)
		installedBinary(t, methodRelease)
		useFetch(t, &scriptedFetch{missing: true})

		got := invoke(t, root, "2026-09-02T12:00", false, "update")
		if got.Code == exitOK {
			t.Fatal("an upgrade with no download tool reported success")
		}
		for _, want := range []string{"curl", "wget"} {
			if !strings.Contains(got.Stderr, want) {
				t.Errorf("the refusal does not name %s: %s", want, got.Stderr)
			}
		}
	})

	t.Run("--check reports without replacing", func(t *testing.T) {
		root := fixtureVault(t)
		self := installedBinary(t, methodRelease)
		before, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		f := useFetch(t, &scriptedFetch{files: map[string]string{versionName: "v9.9.9\n"}})

		got := invoke(t, root, "2026-09-02T12:00", false, "update", "--check")
		if got.Code != exitOK {
			t.Fatalf("exit %d: %s", got.Code, got.Stderr)
		}
		if !strings.Contains(got.Stdout, "v9.9.9") {
			t.Errorf("--check did not report the newest release:\n%s", got.Stdout)
		}
		if after, err := os.ReadFile(self); err != nil || string(after) != string(before) {
			t.Errorf("--check replaced the binary: %q, %v", after, err)
		}
		for _, call := range f.calls {
			if strings.HasSuffix(call.url, "/"+asset) {
				t.Errorf("--check downloaded a whole binary: %s", call.url)
			}
		}
	})
}

// TestReleaseAssetsMatchWorkflow normalises the release trigger and the file
// arguments declared on the top-level GitHub release command.
func TestReleaseAssetsMatchWorkflow(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	model, err := normaliseReleaseWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(model.TagPatterns) != 1 || model.TagPatterns[0] != "v*" {
		t.Errorf("release trigger tags = %v, want [v*]", model.TagPatterns)
	}
	wantPublished := map[string]bool{checksumsName: true, versionName: true}
	for _, asset := range releaseAssets {
		wantPublished[asset] = true
	}
	if !setsEqual(model.PublishedFiles, wantPublished) {
		t.Errorf("release publishes = %v, want %v", model.PublishedFiles, wantPublished)
	}
}

func TestInstallerFailureBeforeMetadataPublicationPreservesExistingMethod(t *testing.T) {
	asset, ok := releaseAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		t.Skip("the release installer does not publish this test platform")
	}

	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	oldBinary := []byte("existing release binary")
	method := []byte(string(methodRelease) + "\n")
	if err := os.WriteFile(filepath.Join(installDir, "brain-axi"), oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, methodFileName), method, 0o644); err != nil {
		t.Fatal(err)
	}

	replacement := "#!/bin/sh\nprintf 'brain-axi v9.9.9\\n'\n"
	assetPath := filepath.Join(root, asset)
	manifestPath := filepath.Join(root, checksumsName)
	if err := os.WriteFile(assetPath, []byte(replacement), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(manifestFor(map[string]string{asset: replacement})), 0o644); err != nil {
		t.Fatal(err)
	}

	curl := `#!/bin/sh
output=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output="$2"; shift 2 ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  */checksums.txt) cp "$TEST_MANIFEST" "$output" ;;
  *) cp "$TEST_ASSET" "$output" ;;
esac
`
	failingMove := `#!/bin/sh
case "$1" in
  */.brain-axi-install) exit 73 ;;
esac
exec /usr/bin/mv "$@"
`
	if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "mv"), []byte(failingMove), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", filepath.Join("..", "..", "install.sh"), "--release")
	cmd.Env = []string{
		"HOME=" + root,
		"PATH=" + binDir + ":/usr/local/go/bin:/usr/bin:/bin",
		"BRAIN_AXI_INSTALL_DIR=" + installDir,
		"BRAIN_AXI_LINK_DIR=" + installDir,
		"TEST_ASSET=" + assetPath,
		"TEST_MANIFEST=" + manifestPath,
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("installer unexpectedly succeeded:\n%s", out)
	}

	gotMethod, readErr := os.ReadFile(filepath.Join(installDir, methodFileName))
	if readErr != nil {
		t.Fatalf("existing method record was removed: %v\n%s", readErr, out)
	}
	if string(gotMethod) != string(method) {
		t.Errorf("method record = %q, want %q", gotMethod, method)
	}
	gotBinary, readErr := os.ReadFile(filepath.Join(installDir, "brain-axi"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(gotBinary) != string(oldBinary) {
		t.Errorf("existing binary changed to %q", gotBinary)
	}
}

// TestTheBinaryLinksNoNetworkClient is NFR-2 as a build failure rather than a
// promise. The release path added a download, and the way it must stay added is
// by delegating to a CLI: the moment anything here imports an HTTP or socket
// package, the binary starts opening its own connections and holding its own
// timeouts, and the amendment in docs/requirements.md stops being true.
func TestTheBinaryLinksNoNetworkClient(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	// net/url is parsing, not reach: it opens nothing. Everything else under
	// net, and TLS, is a client.
	banned := map[string]bool{"net": true, "net/http": true, "crypto/tls": true}
	for _, pkg := range strings.Fields(string(out)) {
		if banned[pkg] || strings.HasPrefix(pkg, "net/http/") {
			t.Errorf("brain-axi links %s; NFR-2 delegates every network call to an invoked CLI", pkg)
		}
	}
}

// --- helpers ----------------------------------------------------------------

type releaseWorkflow struct {
	On struct {
		Push struct {
			Tags []string `yaml:"tags"`
		} `yaml:"push"`
	} `yaml:"on"`
	Jobs map[string]struct {
		Steps []struct {
			Name string `yaml:"name"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

type releaseWorkflowModel struct {
	TagPatterns    []string
	PublishedFiles map[string]bool
}

func normaliseReleaseWorkflow(raw []byte) (releaseWorkflowModel, error) {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		return releaseWorkflowModel{}, fmt.Errorf("parse release workflow: %w", err)
	}
	job, ok := workflow.Jobs["release"]
	if !ok {
		return releaseWorkflowModel{}, errors.New("release workflow has no release job")
	}
	model := releaseWorkflowModel{
		TagPatterns:    workflow.On.Push.Tags,
		PublishedFiles: map[string]bool{},
	}
	for _, step := range job.Steps {
		if step.Name == "Publish the release" {
			published, err := releaseCreateFiles(step.Run)
			if err != nil {
				return releaseWorkflowModel{}, err
			}
			model.PublishedFiles = published
		}
	}
	if len(model.PublishedFiles) == 0 {
		return releaseWorkflowModel{}, errors.New("release workflow has no published files")
	}
	return model, nil
}

func releaseCreateFiles(script string) (map[string]bool, error) {
	lines := strings.Split(script, "\n")
	for i, raw := range lines {
		if raw != strings.TrimSpace(raw) || !strings.HasPrefix(raw, "gh release create ") {
			continue
		}
		command := strings.TrimSuffix(raw, "\\")
		for strings.HasSuffix(raw, "\\") && i+1 < len(lines) {
			i++
			raw = strings.TrimSpace(lines[i])
			command += " " + strings.TrimSuffix(raw, "\\")
		}
		files := map[string]bool{}
		for _, field := range strings.Fields(command) {
			field = strings.Trim(field, "\"")
			if strings.HasPrefix(field, "dist/") {
				files[strings.TrimPrefix(field, "dist/")] = true
			}
		}
		return files, nil
	}
	return nil, errors.New("release workflow has no top-level gh release create command")
}

func setsEqual(got, want map[string]bool) bool {
	if len(got) != len(want) {
		return false
	}
	for key := range want {
		if !got[key] {
			return false
		}
	}
	return true
}

// methodOf resolves the install method with no flags set.
func methodOf(t *testing.T, self string) (installMethod, error) {
	t.Helper()
	return appFor(t, "update").resolveMethod(self)
}

// appFor parses a command line without opening a vault, for the parts of an
// upgrade that never touch one.
func appFor(t *testing.T, args ...string) *app {
	t.Helper()
	a, err := newApp(args, Env{Stdout: io.Discard, Stderr: io.Discard, Workdir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// installedBinary writes a stand-in binary with a recorded install method and
// points the upgrade at it, so no test can replace the test binary itself.
func installedBinary(t *testing.T, method installMethod) string {
	t.Helper()
	dir := t.TempDir()
	self := filepath.Join(dir, "brain-axi")
	if err := os.WriteFile(self, []byte("the binary that is being replaced"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, methodFileName), []byte(string(method)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	useExecutable(t, self)
	return self
}

func useExecutable(t *testing.T, path string) {
	t.Helper()
	previous := executablePath
	executablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { executablePath = previous })
}

func useFetch(t *testing.T, f *scriptedFetch) *scriptedFetch {
	t.Helper()
	previous := fetch
	fetch = f
	t.Cleanup(func() { fetch = previous })
	return f
}

// stagingLeftovers reports the temporary files an abandoned upgrade left in the
// install directory. A refused upgrade must clean up after itself.
func stagingLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".brain-axi.new-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// manifestFor writes the sha256sum-format manifest a release publishes.
func manifestFor(assets map[string]string) string {
	names := make([]string, 0, len(assets))
	for name := range assets {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		sum := sha256.Sum256([]byte(assets[name]))
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	return b.String()
}

// scriptedFetch answers as curl would, without a network. It writes the file
// the real tool would have written, so the checksum and the run check downstream
// are exercised for real.
type scriptedFetch struct {
	// missing pretends this machine has no download tool at all.
	missing bool
	// files maps an asset name to the bytes a download of it produces.
	files map[string]string
	// fail is the error every download answers with.
	fail error
	// calls records every download, so a test can prove what was fetched and
	// in what order.
	calls []fetchCall
}

type fetchCall struct {
	name string
	url  string
	path string
}

func (f *scriptedFetch) Look(name string) (string, error) {
	if f.missing {
		return "", errors.New("executable file not found in $PATH")
	}
	if name != "curl" {
		return "", errors.New("executable file not found in $PATH")
	}
	return "/usr/bin/" + name, nil
}

func (f *scriptedFetch) Run(name string, args ...string) ([]byte, error) {
	url, path := args[len(args)-1], args[len(args)-2]
	f.calls = append(f.calls, fetchCall{name: name, url: url, path: path})
	if f.fail != nil {
		return nil, f.fail
	}
	content, ok := f.files[strings.TrimPrefix(url, releaseDownloadBase)]
	if !ok {
		return nil, fmt.Errorf("The requested URL returned error: 404")
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return nil, nil
}
