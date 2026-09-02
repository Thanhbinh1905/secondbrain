// Package forge resolves a pull-request or merge-request URL and reports its
// status by running the operator's own already-authenticated forge CLI.
//
// brain-axi opens no socket and holds no token. NFR-2's amended wording is the
// contract this package implements: no network calls of its own, network reach
// delegated to explicitly invoked forge CLIs. `gh` speaks to GitHub, `glab`
// speaks to GitLab including self-hosted hosts, and each already knows its own
// hosts and credentials - so nothing here manages a host list, reads a token or
// writes one down.
//
// Nothing in this package runs unless the user asked for it. The offline
// commands never reach it; only an explicit PR command or an explicit
// --refresh does.
package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Kind is which forge a reference lives on, and therefore which CLI reaches it.
type Kind string

const (
	// GitHub is reached through `gh`.
	GitHub Kind = "github"
	// GitLab is reached through `glab`, self-hosted hosts included.
	GitLab Kind = "gitlab"
)

// CLI is the command a kind is read through.
func (k Kind) CLI() string {
	switch k {
	case GitHub:
		return "gh"
	case GitLab:
		return "glab"
	default:
		return ""
	}
}

// Noun is what the kind calls a change proposal, for error text the user
// reads.
func (k Kind) Noun() string {
	switch k {
	case GitHub:
		return "pull request"
	case GitLab:
		return "merge request"
	default:
		return "change"
	}
}

// Ref is one change proposal, resolved from its URL.
type Ref struct {
	// URL is the link exactly as it was given.
	URL string
	// Host is the forge host, which is what decides whether this machine can
	// reach it at all.
	Host string
	Kind Kind
	// Project is the path the forge identifies the repository by:
	// owner/repo on GitHub, group/subgroup/project on GitLab.
	Project string
	Number  int
}

// String names a ref the way a person would say it.
func (r Ref) String() string {
	return fmt.Sprintf("%s/%s!%d", r.Host, r.Project, r.Number)
}

// States are the closed vocabulary a cached forge state is stored in. Every
// forge's own spelling is mapped onto exactly one of these, so a query never
// has to know which forge a record points at.
var States = []string{"open", "draft", "merged", "closed"}

// CheckStates are the closed vocabulary a cached check rollup is stored in.
// "none" means the forge reported no checks, which is not the same as not
// having looked.
var CheckStates = []string{"passing", "failing", "pending", "none"}

// Status is what a forge said about a ref, and when it said it.
type Status struct {
	State  string `json:"state"`
	Checks string `json:"checks"`
	Title  string `json:"title"`
	// CheckedAt is when this answer was obtained. It is stored and displayed
	// everywhere the status is, because a status without its age can be
	// mistaken for a live one.
	CheckedAt time.Time `json:"checked_at"`
}

// Detect resolves a forge URL into a ref, or explains why it is not one.
//
// The path shape decides the forge, not the host alone: a host is enough to
// recognise github.com, but it can never tell a self-hosted GitLab apart from
// any other machine, and a self-hosted work forge is exactly that case.
// /pull/<n> is unmistakably GitHub and /-/merge_requests/<n> unmistakably
// GitLab, so both self-hosted and hosted URLs resolve by the same rule.
func Detect(raw string) (Ref, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Ref{}, fmt.Errorf("a forge URL must not be empty")
	}
	u, err := url.Parse(trimmed)
	// An SSH remote fails to parse rather than parsing with an odd scheme, so
	// both arrive at the same message: paste the browser link, not the remote.
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Ref{}, fmt.Errorf("%q must be an http or https URL, the kind the forge shows in a browser", trimmed)
	}
	if u.Host == "" {
		return Ref{}, fmt.Errorf("%q names no host", trimmed)
	}

	parts := splitPath(u.Path)
	kind, project, number, err := locate(parts)
	if err != nil {
		return Ref{}, fmt.Errorf("%q: %v", trimmed, err)
	}
	host := strings.ToLower(u.Host)
	// A host and a path shape that disagree is a typo, not something to guess
	// about: guessing here points a refresh at the wrong forge entirely.
	if host == "github.com" && kind != GitHub {
		return Ref{}, fmt.Errorf("%q is on github.com but is shaped like a GitLab merge request", trimmed)
	}
	if host == "gitlab.com" && kind != GitLab {
		return Ref{}, fmt.Errorf("%q is on gitlab.com but is shaped like a GitHub pull request", trimmed)
	}
	return Ref{URL: trimmed, Host: host, Kind: kind, Project: project, Number: number}, nil
}

func splitPath(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// locate finds the marker segment that names the change proposal, and splits
// the project path off in front of it.
func locate(parts []string) (Kind, string, int, error) {
	for i, seg := range parts {
		var kind Kind
		switch seg {
		case "pull":
			kind = GitHub
		case "merge_requests":
			kind = GitLab
		default:
			continue
		}
		if i+1 >= len(parts) {
			return "", "", 0, fmt.Errorf("has no %s number after %q", kind.Noun(), seg)
		}
		number, err := strconv.Atoi(parts[i+1])
		if err != nil || number <= 0 {
			return "", "", 0, fmt.Errorf("has %q where a %s number should be", parts[i+1], kind.Noun())
		}
		// GitLab puts a literal "-" segment between the project path and the
		// resource; GitHub does not use one.
		project := parts[:i]
		if n := len(project); n > 0 && project[n-1] == "-" {
			project = project[:n-1]
		}
		if len(project) == 0 {
			return "", "", 0, fmt.Errorf("names no project before %q", seg)
		}
		return kind, strings.Join(project, "/"), number, nil
	}
	return "", "", 0, fmt.Errorf("is not a pull request or merge request URL; expected a path like /owner/repo/pull/12 or /group/project/-/merge_requests/12")
}

// Runner executes a forge CLI. Production uses execRunner; a test supplies its
// own so the suite never needs a network or a token.
type Runner interface {
	// Look reports whether a command exists on PATH, returning its resolved
	// path.
	Look(name string) (string, error)
	// Run executes a command and returns its stdout. The error carries the
	// command's own stderr, because that is where a forge CLI explains itself.
	Run(name string, args ...string) (stdout []byte, err error)
}

type execRunner struct{ timeout time.Duration }

func (execRunner) Look(name string) (string, error) { return exec.LookPath(name) }

func (r execRunner) Run(name string, args ...string) ([]byte, error) {
	// A forge that does not answer must not hang the command. An unreachable
	// self-hosted host is the ordinary case here - a self-hosted work forge is
	// only reachable from inside its network - so the timeout is part of
	// reporting "unreachable" promptly rather than a safety net.
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if ctx.Err() != nil {
			detail = fmt.Sprintf("no answer within %s (is the host reachable from this machine?)", r.timeout)
		}
		return out, &CLIError{CLI: name, Err: err, Stderr: detail}
	}
	return out, nil
}

// FetchTimeout is how long a status read waits for a forge.
const FetchTimeout = 30 * time.Second

// ProbeTimeout is how long a reachability check waits. It is shorter than
// FetchTimeout because doctor asks about every linked host in turn, and a
// diagnostic that takes a minute to say "unreachable" is one nobody runs.
const ProbeTimeout = 8 * time.Second

// Exec is the runner that actually reaches the network.
var Exec Runner = execRunner{timeout: FetchTimeout}

// Probe is the shorter-fused runner doctor uses for reachability.
var Probe Runner = execRunner{timeout: ProbeTimeout}

// MissingCLIError reports that the forge CLI this ref needs is not installed.
// It names the concrete missing requirement rather than reporting the status
// as unknown, because an unknown status renders as fine and is not.
type MissingCLIError struct {
	CLI  string
	Kind Kind
	Host string
}

func (e *MissingCLIError) Error() string {
	return fmt.Sprintf("%s is not on PATH, so %s on %s cannot be read; install %s and run `%s auth login --hostname %s`",
		e.CLI, e.Kind.Noun()+"s", e.Host, e.CLI, e.CLI, e.Host)
}

// CLIError carries a forge CLI's own failure, stderr included. An
// unauthenticated host and an unreachable one both arrive this way, and both
// are reported as exactly what they are.
type CLIError struct {
	CLI    string
	Err    error
	Stderr string
}

func (e *CLIError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s failed: %s", e.CLI, collapse(e.Stderr))
	}
	return fmt.Sprintf("%s failed: %v", e.CLI, e.Err)
}

func (e *CLIError) Unwrap() error { return e.Err }

// collapse folds a CLI's multi-line, box-drawn error into one line. Forge CLIs
// draw frames for a human terminal; this tool's error surface is one line.
func collapse(s string) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.Trim(line, "│┃|"))
		line = strings.TrimSpace(strings.Trim(line, "─━-"))
		if line == "" || line == "ERROR" || line == "X" {
			continue
		}
		kept = append(kept, strings.TrimPrefix(strings.TrimPrefix(line, "X "), "x "))
	}
	joined := strings.Join(kept, " ")
	return strings.Join(strings.Fields(joined), " ")
}

// Fetch reads one ref's current status through its forge CLI.
//
// This is the only function in brain-axi that causes a network request, and it
// runs only when the user explicitly asked for one.
func Fetch(r Runner, ref Ref, now time.Time) (Status, error) {
	cli := ref.Kind.CLI()
	if cli == "" {
		return Status{}, fmt.Errorf("no CLI knows how to read %s", ref.URL)
	}
	if _, err := r.Look(cli); err != nil {
		return Status{}, &MissingCLIError{CLI: cli, Kind: ref.Kind, Host: ref.Host}
	}
	switch ref.Kind {
	case GitHub:
		return fetchGitHub(r, ref, now)
	case GitLab:
		return fetchGitLab(r, ref, now)
	default:
		return Status{}, fmt.Errorf("unknown forge kind %q", ref.Kind)
	}
}

// ghPR is the slice of `gh pr view --json` output this tool reads.
type ghPR struct {
	Title             string `json:"title"`
	State             string `json:"state"`
	IsDraft           bool   `json:"isDraft"`
	StatusCheckRollup []struct {
		TypeName   string `json:"__typename"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
	} `json:"statusCheckRollup"`
}

func fetchGitHub(r Runner, ref Ref, now time.Time) (Status, error) {
	out, err := r.Run("gh", "pr", "view", ref.URL,
		"--json", "title,state,isDraft,statusCheckRollup")
	if err != nil {
		return Status{}, err
	}
	var pr ghPR
	if err := json.Unmarshal(out, &pr); err != nil {
		return Status{}, fmt.Errorf("gh returned output this tool cannot read: %v", err)
	}
	state := strings.ToLower(pr.State)
	if state == "open" && pr.IsDraft {
		state = "draft"
	}
	if !known(States, state) {
		return Status{}, fmt.Errorf("gh reported state %q, which is not one of %s", pr.State, strings.Join(States, ", "))
	}
	checks := "none"
	if len(pr.StatusCheckRollup) > 0 {
		failing, pending := false, false
		for _, c := range pr.StatusCheckRollup {
			switch {
			case c.TypeName == "StatusContext":
				switch strings.ToUpper(c.State) {
				case "SUCCESS":
				case "PENDING", "EXPECTED", "":
					pending = true
				default:
					failing = true
				}
			case strings.ToUpper(c.Status) != "COMPLETED":
				pending = true
			default:
				switch strings.ToUpper(c.Conclusion) {
				case "SUCCESS", "NEUTRAL", "SKIPPED":
				default:
					failing = true
				}
			}
		}
		checks = rollup(failing, pending)
	}
	return Status{State: state, Checks: checks, Title: pr.Title, CheckedAt: now}, nil
}

// glabMR is the slice of `glab mr view -F json` output this tool reads.
type glabMR struct {
	Title    string `json:"title"`
	State    string `json:"state"`
	Draft    bool   `json:"draft"`
	Pipeline *struct {
		Status string `json:"status"`
	} `json:"pipeline"`
	HeadPipeline *struct {
		Status string `json:"status"`
	} `json:"head_pipeline"`
}

func fetchGitLab(r Runner, ref Ref, now time.Time) (Status, error) {
	// -R takes the full URL, and glab resolves the host through its own
	// configuration. That is deliberately not this tool's business: a
	// self-hosted host is configured once with `glab auth login` and brain-axi
	// never learns about it.
	out, err := r.Run("glab", "mr", "view", strconv.Itoa(ref.Number),
		"-R", "https://"+ref.Host+"/"+ref.Project, "-F", "json")
	if err != nil {
		return Status{}, err
	}
	var mr glabMR
	if err := json.Unmarshal(out, &mr); err != nil {
		return Status{}, fmt.Errorf("glab returned output this tool cannot read: %v", err)
	}
	state := strings.ToLower(mr.State)
	switch state {
	case "opened":
		state = "open"
	case "locked":
		state = "open"
	}
	if state == "open" && mr.Draft {
		state = "draft"
	}
	if !known(States, state) {
		return Status{}, fmt.Errorf("glab reported state %q, which is not one of %s", mr.State, strings.Join(States, ", "))
	}
	pipeline := mr.HeadPipeline
	if pipeline == nil {
		pipeline = mr.Pipeline
	}
	checks := "none"
	if pipeline != nil {
		switch strings.ToLower(pipeline.Status) {
		case "success", "manual":
			checks = "passing"
		case "failed", "canceled", "cancelled":
			checks = "failing"
		case "skipped":
			checks = "none"
		default:
			checks = "pending"
		}
	}
	return Status{State: state, Checks: checks, Title: mr.Title, CheckedAt: now}, nil
}

func rollup(failing, pending bool) string {
	switch {
	case failing:
		return "failing"
	case pending:
		return "pending"
	default:
		return "passing"
	}
}

func known(vocab []string, v string) bool {
	for _, s := range vocab {
		if s == v {
			return true
		}
	}
	return false
}

// Reachable reports whether this machine can actually read a host, by asking
// the host's own CLI. It is what doctor uses, and it never guesses: a missing
// CLI, an unauthenticated host and an unreachable one are three different
// answers.
func Reachable(r Runner, kind Kind, host string) (string, bool) {
	cli := kind.CLI()
	if _, err := r.Look(cli); err != nil {
		return cli + " is not installed", false
	}
	if _, err := r.Run(cli, "auth", "status", "--hostname", host); err != nil {
		var cliErr *CLIError
		detail := "not authenticated"
		if ok := asCLIError(err, &cliErr); ok && cliErr.Stderr != "" {
			detail = collapse(cliErr.Stderr)
		}
		return detail, false
	}
	return "authenticated", true
}

func asCLIError(err error, target **CLIError) bool {
	for err != nil {
		if e, ok := err.(*CLIError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
