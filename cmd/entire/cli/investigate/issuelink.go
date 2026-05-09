package investigate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ErrGhNotFound is returned when ResolveIssueLink cannot find the gh CLI on
// PATH. Callers (and tests) can match on this sentinel via errors.Is.
var ErrGhNotFound = errors.New("gh CLI not found on PATH")

// issueLinkPathRE matches GitHub paths of the shape
// /<owner>/<repo>/(issues|pull|pulls)/<number>. The prefix is anchored to the
// start of the path; trailing segments (e.g. /files, /commits) are tolerated
// only when matching against the trimmed path. Both `pull` and `pulls` are
// accepted because GitHub's redirector accepts both forms.
var issueLinkPathRE = regexp.MustCompile(`^/([^/]+)/([^/]+)/(issues|pull|pulls)/(\d+)$`)

// IssueLinkResult is the output of ResolveIssueLink.
type IssueLinkResult struct {
	// SeedDoc is the rendered markdown body — ready to write to a
	// findings doc via Bootstrap.IssueLinkSeed.
	SeedDoc []byte
	// Topic is the human-readable topic. Prefers the issue/PR title; if
	// the title is empty, falls back to the URL.
	Topic string
}

// ghUser is the JSON shape of a gh user object.
type ghUser struct {
	Login string `json:"login"`
}

// ghLabel is the JSON shape of a gh label object.
type ghLabel struct {
	Name string `json:"name"`
}

// ghComment is the JSON shape of a gh comment object.
type ghComment struct {
	Author    ghUser    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
	Body      string    `json:"body"`
}

// ghIssue is the JSON shape ResolveIssueLink unmarshals into. The same shape
// works for both issues and PRs because gh exposes the matching fields via
// `--json title,body,author,createdAt,labels,comments` for either resource.
type ghIssue struct {
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	Author    ghUser      `json:"author"`
	CreatedAt time.Time   `json:"createdAt"`
	Labels    []ghLabel   `json:"labels"`
	Comments  []ghComment `json:"comments"`
}

// runGhFn is the indirection the loop's gh-resolver calls. Production wires
// this to runGhExec; tests override it.
var runGhFn = runGhExec

// runGhExec is the production runGhFn implementation. Returns gh's stdout
// bytes, or an error wrapping any exec failure with stderr captured. Returns
// ErrGhNotFound when `gh` is missing from PATH.
func runGhExec(ctx context.Context, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, ErrGhNotFound
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, stderrStr)
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// ResolveIssueLink resolves a GitHub issue or PR URL via the gh CLI and
// returns a markdown seed-doc body suitable for passing to
// Bootstrap.IssueLinkSeed.
//
// Supported: GitHub issues and PRs only. Non-GitHub hosts (gitlab, bitbucket,
// self-hosted forges) and non-issue/PR GitHub paths return an actionable
// error pointing the user at --topic or [seed-doc] instead.
//
// The function intentionally does not follow nested issue/PR cross-references
// or fetch related sub-issues: keep the seed scope to one resource so agents
// have a clear starting point.
func ResolveIssueLink(ctx context.Context, rawURL string) (IssueLinkResult, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return IssueLinkResult{}, fmt.Errorf("parse --issue-link URL: %w", err)
	}
	host := strings.ToLower(u.Host)
	if host != "github.com" && host != "www.github.com" {
		return IssueLinkResult{}, errors.New("--issue-link only supports GitHub issues and PRs in this release; save the issue body to a file and pass it as a positional [seed-doc]")
	}

	matches := issueLinkPathRE.FindStringSubmatch(u.Path)
	if matches == nil {
		return IssueLinkResult{}, fmt.Errorf("--issue-link expects a GitHub issue or PR URL; got %s", u.Path)
	}
	resource := matches[3]

	var subcmd string
	switch resource {
	case "issues":
		subcmd = "issue"
	case "pull", "pulls":
		subcmd = "pr"
	default:
		// unreachable: regex restricts the alternatives.
		return IssueLinkResult{}, fmt.Errorf("--issue-link: unsupported resource %q", resource)
	}

	jsonOut, err := runGhFn(ctx, subcmd, "view", rawURL,
		"--json", "title,body,author,createdAt,labels,comments")
	if err != nil {
		if errors.Is(err, ErrGhNotFound) {
			return IssueLinkResult{}, errors.New("--issue-link requires the gh CLI; install it (https://cli.github.com) or pass [seed-doc]")
		}
		return IssueLinkResult{}, fmt.Errorf("gh %s view %s: %w", subcmd, rawURL, err)
	}

	var issue ghIssue
	if err := json.Unmarshal(jsonOut, &issue); err != nil {
		return IssueLinkResult{}, fmt.Errorf("decode gh %s view JSON: %w", subcmd, err)
	}

	body := renderIssueSeed(rawURL, issue)
	topic := issue.Title
	if strings.TrimSpace(topic) == "" {
		topic = rawURL
	}
	return IssueLinkResult{
		SeedDoc: body,
		Topic:   topic,
	}, nil
}

// placeholderUnknown is the rendered value used when an author or timestamp
// field is missing. Kept as a constant so the seed-doc structure stays stable
// across renderings.
const placeholderUnknown = "(unknown)"

// renderIssueSeed renders an issue/PR fetched from gh into a markdown
// seed-doc body. Format:
//
//	# Investigation: <title>
//
//	**Source:** <url>
//	**Author:** @<login>
//	**Created:** <iso-date>
//	**Labels:** <comma-joined>
//
//	## Question
//
//	<body>
//
//	## Comments
//
//	- **@<login> (<iso-date>):** <comment-body>
//	...
//
// Empty fields are rendered with `(none)` placeholders so the structure is
// stable for the agents that read it.
func renderIssueSeed(rawURL string, issue ghIssue) []byte {
	var b strings.Builder

	title := strings.TrimSpace(issue.Title)
	if title == "" {
		title = rawURL
	}
	fmt.Fprintf(&b, "# Investigation: %s\n\n", title)

	author := strings.TrimSpace(issue.Author.Login)
	if author == "" {
		author = placeholderUnknown
	}
	created := placeholderUnknown
	if !issue.CreatedAt.IsZero() {
		created = issue.CreatedAt.UTC().Format(time.RFC3339)
	}

	labels := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		if name := strings.TrimSpace(l.Name); name != "" {
			labels = append(labels, name)
		}
	}
	labelLine := "(none)"
	if len(labels) > 0 {
		labelLine = strings.Join(labels, ", ")
	}

	fmt.Fprintf(&b, "**Source:** %s\n", rawURL)
	fmt.Fprintf(&b, "**Author:** @%s\n", author)
	fmt.Fprintf(&b, "**Created:** %s\n", created)
	fmt.Fprintf(&b, "**Labels:** %s\n\n", labelLine)

	body := strings.TrimSpace(issue.Body)
	if body == "" {
		body = "(no body)"
	}
	fmt.Fprintf(&b, "## Question\n\n%s\n\n", body)

	if len(issue.Comments) > 0 {
		b.WriteString("## Comments\n\n")
		for _, c := range issue.Comments {
			cAuthor := strings.TrimSpace(c.Author.Login)
			if cAuthor == "" {
				cAuthor = placeholderUnknown
			}
			cCreated := placeholderUnknown
			if !c.CreatedAt.IsZero() {
				cCreated = c.CreatedAt.UTC().Format(time.RFC3339)
			}
			cBody := strings.TrimSpace(c.Body)
			if cBody == "" {
				cBody = "(empty)"
			}
			fmt.Fprintf(&b, "- **@%s (%s):** %s\n", cAuthor, cCreated, cBody)
		}
	}

	return []byte(b.String())
}
