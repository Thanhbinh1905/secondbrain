package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestProjectContracts keeps the launch surface honest. These are repository
// contracts rather than CLI behavior, so they fail before a release or CI
// change can silently make the documented path untrue.
func TestProjectContracts(t *testing.T) {
	root := filepath.Join("..", "..")

	mod := readProjectFile(t, root, "go.mod")
	if !regexp.MustCompile(`(?m)^go 1\.22$`).MatchString(mod) {
		t.Fatalf("go.mod must declare the supported minimum Go version as 1.22")
	}

	ci := readProjectFile(t, root, ".github", "workflows", "ci.yml")
	for _, want := range []string{
		"go test -race",
		"golangci-lint",
		"go test -coverprofile=coverage.out ./...",
		"go tool cover -func=coverage.out",
	} {
		if !strings.Contains(ci, want) {
			t.Errorf("CI is missing quality gate %q", want)
		}
	}

	readme := readProjectFile(t, root, "README.md")
	if lines := strings.Count(readme, "\n") + 1; lines > 260 {
		t.Errorf("README has %d lines; keep it a concise quickstart (260 lines maximum)", lines)
	}

	scope := readProjectFile(t, root, "docs", "release.md")
	scopeLower := strings.ToLower(scope)
	for _, want := range []string{
		"checksums are integrity checks, not signatures",
		"no cross-process file lock",
		"windows is not supported",
	} {
		if !strings.Contains(scopeLower, want) {
			t.Errorf("release scope documentation is missing %q", want)
		}
	}
}

func readProjectFile(t *testing.T, root string, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{root}, parts...)...)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
