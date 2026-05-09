package investigate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// withFakeGh swaps runGhFn for the duration of the test.
//
// runGhFn is a package-level variable, so tests that override it cannot run
// in parallel with each other — calling t.Parallel inside a test that uses
// withFakeGh would race with sibling tests' overrides. The CLAUDE.md project
// rule allows skipping t.Parallel when tests modify process-global state;
// runGhFn falls into that bucket.
func withFakeGh(t *testing.T, fake func(ctx context.Context, args ...string) ([]byte, error)) {
	t.Helper()
	prev := runGhFn
	runGhFn = fake
	t.Cleanup(func() { runGhFn = prev })
}

// fakeGhSuccess returns the given response unconditionally and asserts that
// the gh subcommand matches expectedSubcommand.
func fakeGhSuccess(t *testing.T, expectedSubcommand string, response any) func(ctx context.Context, args ...string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 0 || args[0] != expectedSubcommand {
			t.Errorf("expected subcommand %q, got args=%v", expectedSubcommand, args)
		}
		return json.Marshal(response)
	}
}

func TestResolveIssueLink_Issue(t *testing.T) {
	resp := ghIssue{
		Title:     "Checkout times out",
		Body:      "When I run `git checkout main`, it hangs.\n\nReproduce: ...",
		Author:    ghUser{Login: "alice"},
		CreatedAt: time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC),
		Labels:    []ghLabel{{Name: "bug"}, {Name: "p1"}},
		Comments: []ghComment{
			{
				Author:    ghUser{Login: "bob"},
				CreatedAt: time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC),
				Body:      "Same on macOS too.",
			},
		},
	}
	withFakeGh(t, fakeGhSuccess(t, "issue", resp))

	got, err := ResolveIssueLink(context.Background(), "https://github.com/owner/repo/issues/42")
	if err != nil {
		t.Fatalf("ResolveIssueLink: %v", err)
	}
	if got.Topic != "Checkout times out" {
		t.Errorf("Topic = %q, want %q", got.Topic, "Checkout times out")
	}

	body := string(got.SeedDoc)
	for _, want := range []string{
		"# Investigation: Checkout times out",
		"**Source:** https://github.com/owner/repo/issues/42",
		"**Author:** @alice",
		"**Created:** 2026-05-01T09:30:00Z",
		"**Labels:** bug, p1",
		"## Question",
		"When I run `git checkout main`",
		"## Comments",
		"- **@bob (2026-05-02T10:00:00Z):** Same on macOS too.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("seed doc missing %q\nGOT:\n%s", want, body)
		}
	}
}

func TestResolveIssueLink_PR(t *testing.T) {
	resp := ghIssue{
		Title:     "Fix flaky checkout",
		Body:      "This patch retries the network operation.",
		Author:    ghUser{Login: "alice"},
		CreatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
	}
	withFakeGh(t, fakeGhSuccess(t, "pr", resp))

	got, err := ResolveIssueLink(context.Background(), "https://github.com/owner/repo/pull/100")
	if err != nil {
		t.Fatalf("ResolveIssueLink: %v", err)
	}
	if got.Topic != "Fix flaky checkout" {
		t.Errorf("Topic = %q, want %q", got.Topic, "Fix flaky checkout")
	}
	body := string(got.SeedDoc)
	if !strings.Contains(body, "**Source:** https://github.com/owner/repo/pull/100") {
		t.Errorf("seed doc missing source URL\n%s", body)
	}
	if !strings.Contains(body, "This patch retries the network operation.") {
		t.Errorf("seed doc missing body\n%s", body)
	}
	// No comments was passed → the seed must NOT render an empty Comments
	// section.
	if strings.Contains(body, "## Comments") {
		t.Errorf("expected no Comments section when issue.Comments is empty\n%s", body)
	}
}

func TestResolveIssueLink_PR_PluralPathAccepted(t *testing.T) {
	resp := ghIssue{
		Title:     "Test",
		Body:      "body",
		Author:    ghUser{Login: "alice"},
		CreatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
	}
	withFakeGh(t, fakeGhSuccess(t, "pr", resp))

	if _, err := ResolveIssueLink(context.Background(), "https://github.com/owner/repo/pulls/100"); err != nil {
		t.Errorf("expected /pulls/ to be accepted, got: %v", err)
	}
}

func TestResolveIssueLink_RejectsNonGitHub(t *testing.T) {
	withFakeGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		t.Error("gh should not be called for non-GitHub URLs")
		return nil, nil
	})

	_, err := ResolveIssueLink(context.Background(), "https://gitlab.com/owner/repo/-/issues/42")
	if err == nil {
		t.Fatal("expected error for non-GitHub host")
	}
	want := "save the issue body to a file"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain actionable hint %q", err, want)
	}
}

func TestResolveIssueLink_RejectsMalformedPath(t *testing.T) {
	withFakeGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		t.Error("gh should not be called for malformed paths")
		return nil, nil
	})

	_, err := ResolveIssueLink(context.Background(), "https://github.com/owner/repo/tree/main")
	if err == nil {
		t.Fatal("expected error for malformed path")
	}
	if !strings.Contains(err.Error(), "GitHub issue or PR URL") {
		t.Errorf("error %q does not point at expected resource hint", err)
	}
}

func TestResolveIssueLink_GhMissing(t *testing.T) {
	withFakeGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, ErrGhNotFound
	})

	_, err := ResolveIssueLink(context.Background(), "https://github.com/owner/repo/issues/42")
	if err == nil {
		t.Fatal("expected error when gh is missing")
	}
	for _, want := range []string{
		"requires the gh CLI",
		"https://cli.github.com",
		"[seed-doc]",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain hint %q", err, want)
		}
	}
	// Sanity: the user-facing error text should be returned, not the raw
	// sentinel — but errors.Is on the sentinel must NOT be true since we
	// wrap in a plain errors.New.
	if errors.Is(err, ErrGhNotFound) {
		t.Errorf("expected user-facing error, not the raw ErrGhNotFound sentinel")
	}
}

func TestResolveIssueLink_TitleFallbackToURL(t *testing.T) {
	resp := ghIssue{
		Title:     "",
		Body:      "body",
		Author:    ghUser{Login: "alice"},
		CreatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	}
	withFakeGh(t, fakeGhSuccess(t, "issue", resp))

	rawURL := "https://github.com/owner/repo/issues/99"
	got, err := ResolveIssueLink(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("ResolveIssueLink: %v", err)
	}
	if got.Topic != rawURL {
		t.Errorf("Topic = %q, want fallback to URL %q", got.Topic, rawURL)
	}
}

func TestResolveIssueLink_GhExecError(t *testing.T) {
	withFakeGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, errors.New("HTTP 404: not found")
	})

	_, err := ResolveIssueLink(context.Background(), "https://github.com/owner/repo/issues/42")
	if err == nil {
		t.Fatal("expected error on gh failure")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("expected gh error to be wrapped, got %q", err.Error())
	}
}
