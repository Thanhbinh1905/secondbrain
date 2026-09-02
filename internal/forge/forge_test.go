package forge

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeRunner stands in for gh and glab so this suite never needs a network, a
// token, or either binary installed.
type fakeRunner struct {
	// missing names commands this machine does not have.
	missing map[string]bool
	// out maps a command name to the stdout it answers with.
	out map[string]string
	// fail maps a command name to the CLI failure it answers with.
	fail map[string]*CLIError
	// calls records every invocation, so a test can assert that an offline
	// path really did not run anything.
	calls [][]string
}

func (f *fakeRunner) Look(name string) (string, error) {
	if f.missing[name] {
		return "", errors.New("executable file not found in $PATH")
	}
	return "/usr/bin/" + name, nil
}

func (f *fakeRunner) Run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if err, ok := f.fail[name]; ok {
		return nil, err
	}
	return []byte(f.out[name]), nil
}

// TestDetectAcrossEveryHostShape covers all three forge shapes: GitHub,
// GitLab.com, and a self-hosted GitLab on an arbitrary host. The path shape
// decides, because a host alone can never tell a self-hosted GitLab apart from
// any other machine.
func TestDetectAcrossEveryHostShape(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		kind    Kind
		host    string
		project string
		number  int
	}{
		{
			name: "github", url: "https://github.com/Thanhbinh1905/secondbrain/pull/1",
			kind: GitHub, host: "github.com", project: "Thanhbinh1905/secondbrain", number: 1,
		},
		{
			name: "gitlab.com", url: "https://gitlab.com/gitlab-org/gitlab/-/merge_requests/1",
			kind: GitLab, host: "gitlab.com", project: "gitlab-org/gitlab", number: 1,
		},
		{
			name: "self-hosted gitlab", url: "https://git.example.com/platform/service/-/merge_requests/42",
			kind: GitLab, host: "git.example.com", project: "platform/service", number: 42,
		},
		{
			name: "self-hosted gitlab with a subgroup", url: "https://git.example.com/acme/platform/service/-/merge_requests/12",
			kind: GitLab, host: "git.example.com", project: "acme/platform/service", number: 12,
		},
		{
			name: "gitlab without the dash segment", url: "https://git.example.com/platform/service/merge_requests/9",
			kind: GitLab, host: "git.example.com", project: "platform/service", number: 9,
		},
		{
			name: "a host is lower-cased so two spellings cache alike", url: "https://GitHub.com/owner/repo/pull/7",
			kind: GitHub, host: "github.com", project: "owner/repo", number: 7,
		},
		{
			name: "a trailing path is ignored", url: "https://github.com/owner/repo/pull/7/files",
			kind: GitHub, host: "github.com", project: "owner/repo", number: 7,
		},
		{
			name: "http is accepted for an internal host", url: "http://git.internal/team/svc/-/merge_requests/3",
			kind: GitLab, host: "git.internal", project: "team/svc", number: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := Detect(tc.url)
			if err != nil {
				t.Fatalf("Detect(%q): %v", tc.url, err)
			}
			if ref.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", ref.Kind, tc.kind)
			}
			if ref.Host != tc.host {
				t.Errorf("host = %q, want %q", ref.Host, tc.host)
			}
			if ref.Project != tc.project {
				t.Errorf("project = %q, want %q", ref.Project, tc.project)
			}
			if ref.Number != tc.number {
				t.Errorf("number = %d, want %d", ref.Number, tc.number)
			}
			if ref.URL != tc.url {
				t.Errorf("URL = %q, want the link exactly as given, %q", ref.URL, tc.url)
			}
		})
	}
}

// TestDetectRefusesRatherThanGuesses: a URL that is not a change proposal must
// be refused, because guessing points a refresh at the wrong thing entirely.
func TestDetectRefusesRatherThanGuesses(t *testing.T) {
	cases := []struct {
		name, url, want string
	}{
		{"empty", "", "must not be empty"},
		{"no scheme", "github.com/owner/repo/pull/1", "must be an http or https URL"},
		{"an ssh remote instead of the browser link", "git@github.com:owner/repo.git", "must be an http or https URL"},
		{"no host", "https:///owner/repo/pull/1", "names no host"},
		{"a repository, not a change", "https://github.com/owner/repo", "is not a pull request or merge request URL"},
		{"an issue", "https://github.com/owner/repo/issues/4", "is not a pull request or merge request URL"},
		{"no number", "https://github.com/owner/repo/pull", "has no pull request number"},
		{"a branch where a number belongs", "https://github.com/owner/repo/pull/main", `has "main" where a pull request number should be`},
		{"a zero number", "https://github.com/owner/repo/pull/0", "where a pull request number should be"},
		{"no project", "https://github.com/pull/3", "names no project"},
		{"github host, gitlab shape", "https://github.com/owner/repo/-/merge_requests/3", "shaped like a GitLab merge request"},
		{"gitlab host, github shape", "https://gitlab.com/owner/repo/pull/3", "shaped like a GitHub pull request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := Detect(tc.url)
			if err == nil {
				t.Fatalf("Detect(%q) accepted it as %v", tc.url, ref)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

var fixedNow = time.Date(2026, 9, 2, 10, 15, 0, 0, time.UTC)

func TestFetchGitHub(t *testing.T) {
	cases := []struct {
		name       string
		payload    string
		wantState  string
		wantChecks string
	}{
		{
			name:      "merged with a passing rollup",
			payload:   `{"title":"feat: build brain-axi","state":"MERGED","isDraft":false,"statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]}`,
			wantState: "merged", wantChecks: "passing",
		},
		{
			name:      "a draft is its own state, not an open one",
			payload:   `{"title":"wip","state":"OPEN","isDraft":true,"statusCheckRollup":[]}`,
			wantState: "draft", wantChecks: "none",
		},
		{
			name:      "one failure fails the rollup",
			payload:   `{"title":"x","state":"OPEN","isDraft":false,"statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"CheckRun","status":"COMPLETED","conclusion":"FAILURE"}]}`,
			wantState: "open", wantChecks: "failing",
		},
		{
			name:      "a check still running is pending, never passing",
			payload:   `{"title":"x","state":"OPEN","isDraft":false,"statusCheckRollup":[{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""}]}`,
			wantState: "open", wantChecks: "pending",
		},
		{
			name:      "a skipped check does not fail the rollup",
			payload:   `{"title":"x","state":"CLOSED","isDraft":false,"statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SKIPPED"}]}`,
			wantState: "closed", wantChecks: "passing",
		},
		{
			name:      "a legacy status context is read too",
			payload:   `{"title":"x","state":"OPEN","isDraft":false,"statusCheckRollup":[{"__typename":"StatusContext","state":"FAILURE"}]}`,
			wantState: "open", wantChecks: "failing",
		},
		{
			name:      "no checks at all is none, which is not passing",
			payload:   `{"title":"x","state":"OPEN","isDraft":false,"statusCheckRollup":[]}`,
			wantState: "open", wantChecks: "none",
		},
	}
	ref, err := Detect("https://github.com/owner/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{out: map[string]string{"gh": tc.payload}}
			got, err := Fetch(r, ref, fixedNow)
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if got.State != tc.wantState {
				t.Errorf("state = %q, want %q", got.State, tc.wantState)
			}
			if got.Checks != tc.wantChecks {
				t.Errorf("checks = %q, want %q", got.Checks, tc.wantChecks)
			}
			if !got.CheckedAt.Equal(fixedNow) {
				t.Errorf("checked_at = %v, want the caller's clock %v", got.CheckedAt, fixedNow)
			}
			if len(r.calls) != 1 || r.calls[0][0] != "gh" {
				t.Errorf("expected exactly one gh call, got %v", r.calls)
			}
		})
	}
}

func TestFetchGitLabIncludingSelfHosted(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		payload    string
		wantState  string
		wantChecks string
		wantRepo   string
	}{
		{
			name:      "gitlab.com, merged with no pipeline",
			url:       "https://gitlab.com/group/project/-/merge_requests/4",
			payload:   `{"title":"t","state":"merged","draft":false}`,
			wantState: "merged", wantChecks: "none",
			wantRepo: "https://gitlab.com/group/project",
		},
		{
			name:      "self-hosted, open with a green pipeline",
			url:       "https://git.example.com/platform/service/-/merge_requests/42",
			payload:   `{"title":"t","state":"opened","draft":false,"head_pipeline":{"status":"success"}}`,
			wantState: "open", wantChecks: "passing",
			wantRepo: "https://git.example.com/platform/service",
		},
		{
			name:      "opened maps onto open, and a draft onto draft",
			url:       "https://git.example.com/platform/service/-/merge_requests/43",
			payload:   `{"title":"t","state":"opened","draft":true,"head_pipeline":{"status":"running"}}`,
			wantState: "draft", wantChecks: "pending",
			wantRepo: "https://git.example.com/platform/service",
		},
		{
			name:      "a failed pipeline is failing",
			url:       "https://gitlab.com/group/project/-/merge_requests/5",
			payload:   `{"title":"t","state":"opened","draft":false,"pipeline":{"status":"failed"}}`,
			wantState: "open", wantChecks: "failing",
			wantRepo: "https://gitlab.com/group/project",
		},
		{
			name:      "head_pipeline wins over the older pipeline field",
			url:       "https://gitlab.com/group/project/-/merge_requests/6",
			payload:   `{"title":"t","state":"opened","draft":false,"pipeline":{"status":"failed"},"head_pipeline":{"status":"success"}}`,
			wantState: "open", wantChecks: "passing",
			wantRepo: "https://gitlab.com/group/project",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := Detect(tc.url)
			if err != nil {
				t.Fatal(err)
			}
			r := &fakeRunner{out: map[string]string{"glab": tc.payload}}
			got, err := Fetch(r, ref, fixedNow)
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if got.State != tc.wantState {
				t.Errorf("state = %q, want %q", got.State, tc.wantState)
			}
			if got.Checks != tc.wantChecks {
				t.Errorf("checks = %q, want %q", got.Checks, tc.wantChecks)
			}
			// glab resolves a self-hosted host from its own configuration, so
			// brain-axi hands it the repository URL and manages no host list of
			// its own.
			call := strings.Join(r.calls[0], " ")
			if !strings.Contains(call, tc.wantRepo) {
				t.Errorf("glab was called as %q, which does not carry %q", call, tc.wantRepo)
			}
		})
	}
}

// TestMissingCLINamesTheConcreteRequirement: never render unknown as fine.
func TestMissingCLINamesTheConcreteRequirement(t *testing.T) {
	for _, tc := range []struct {
		url, cli string
	}{
		{"https://github.com/owner/repo/pull/1", "gh"},
		{"https://git.example.com/platform/service/-/merge_requests/42", "glab"},
	} {
		ref, err := Detect(tc.url)
		if err != nil {
			t.Fatal(err)
		}
		r := &fakeRunner{missing: map[string]bool{tc.cli: true}}
		_, err = Fetch(r, ref, fixedNow)
		if err == nil {
			t.Fatalf("a missing %s was not reported", tc.cli)
		}
		var missing *MissingCLIError
		if !errors.As(err, &missing) {
			t.Fatalf("error %v is not a MissingCLIError", err)
		}
		for _, want := range []string{tc.cli + " is not on PATH", ref.Host, "auth login"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q does not mention %q", err, want)
			}
		}
		if len(r.calls) != 0 {
			t.Errorf("a missing CLI was still executed: %v", r.calls)
		}
	}
}

// TestUnreachableHostIsReportedAsItself: an unauthenticated or unreachable host
// carries the CLI's own explanation, collapsed onto one line.
func TestUnreachableHostIsReportedAsItself(t *testing.T) {
	ref, err := Detect("https://git.example.com/platform/service/-/merge_requests/42")
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{fail: map[string]*CLIError{"glab": {
		CLI: "glab", Err: errors.New("exit status 1"),
		Stderr: "   \n   ERROR  \n          \n  X dial tcp 203.0.113.10:443: i/o timeout   \n",
	}}}
	_, err = Fetch(r, ref, fixedNow)
	if err == nil {
		t.Fatal("an unreachable host was not reported")
	}
	got := err.Error()
	if !strings.Contains(got, "i/o timeout") {
		t.Errorf("%q does not carry the forge CLI's own reason", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("the error is not one line: %q", got)
	}
	if strings.ContainsAny(got, "│┃") {
		t.Errorf("a forge CLI's frame characters reached the error: %q", got)
	}
}

// TestGlabJSONErrorOnStdoutIsNotMistakenForSuccess: glab can exit non-zero
// with an error JSON object on stdout and nothing on stderr. The exit code,
// not stdout's content, must drive the error branch (AGENTS.md "Sharp
// edges"): the returned error must be the exit-status fallback, and the JSON
// object that arrived on stdout must never leak into it.
func TestGlabJSONErrorOnStdoutIsNotMistakenForSuccess(t *testing.T) {
	ref, err := Detect("https://git.example.com/platform/service/-/merge_requests/42")
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{
		out:  map[string]string{"glab": `{"message":"404 Not Found"}`},
		fail: map[string]*CLIError{"glab": {CLI: "glab", Err: errors.New("exit status 1")}},
	}
	_, err = Fetch(r, ref, fixedNow)
	if err == nil {
		t.Fatal("a non-zero exit with JSON on stdout was accepted as success")
	}
	got := err.Error()
	if want := "glab failed: exit status 1"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
	if strings.Contains(got, "404 Not Found") {
		t.Errorf("stdout's JSON error object leaked into the error: %q", got)
	}
}

// TestUnknownForgeStateIsRefused: a state this tool cannot map is a loud
// failure, never a value silently written into the vault's closed vocabulary.
func TestUnknownForgeStateIsRefused(t *testing.T) {
	ref, err := Detect("https://github.com/owner/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{out: map[string]string{"gh": `{"title":"x","state":"QUANTUM","isDraft":false}`}}
	if _, err := Fetch(r, ref, fixedNow); err == nil {
		t.Fatal("an unmappable state was accepted")
	} else if !strings.Contains(err.Error(), "QUANTUM") {
		t.Errorf("%q does not name the state it could not map", err)
	}
}

func TestReachableDistinguishesItsThreeAnswers(t *testing.T) {
	t.Run("missing CLI", func(t *testing.T) {
		r := &fakeRunner{missing: map[string]bool{"glab": true}}
		detail, ok := Reachable(r, GitLab, "git.example.com")
		if ok {
			t.Error("a missing CLI was reported as reachable")
		}
		if !strings.Contains(detail, "not installed") {
			t.Errorf("detail = %q", detail)
		}
	})
	t.Run("unauthenticated", func(t *testing.T) {
		r := &fakeRunner{fail: map[string]*CLIError{"glab": {
			CLI: "glab", Err: errors.New("exit status 1"),
			Stderr: "X git.example.com has not been authenticated with glab",
		}}}
		detail, ok := Reachable(r, GitLab, "git.example.com")
		if ok {
			t.Error("an unauthenticated host was reported as reachable")
		}
		if !strings.Contains(detail, "has not been authenticated") {
			t.Errorf("detail = %q", detail)
		}
	})
	t.Run("authenticated", func(t *testing.T) {
		r := &fakeRunner{out: map[string]string{"gh": ""}}
		detail, ok := Reachable(r, GitHub, "github.com")
		if !ok {
			t.Errorf("an authenticated host was reported as unreachable: %s", detail)
		}
	})
}

// TestClosedVocabulariesStayClosed pins the two vocabularies the vault stores,
// because widening one silently is how a query stops being able to make a
// confident statement.
func TestClosedVocabulariesStayClosed(t *testing.T) {
	if want := []string{"open", "draft", "merged", "closed"}; !equal(States, want) {
		t.Errorf("States = %v, want %v", States, want)
	}
	if want := []string{"passing", "failing", "pending", "none"}; !equal(CheckStates, want) {
		t.Errorf("CheckStates = %v, want %v", CheckStates, want)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
