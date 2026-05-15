package investigate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrap_SeedDocPassthrough(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.md")
	seed := "# Investigation: Why does checkout retry forever?\n\n## Question\n\nDetails…\n"
	if err := os.WriteFile(seedPath, []byte(seed), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	findings := filepath.Join(dir, "out", "findings.md")

	res, err := Bootstrap(context.Background(), BootstrapInput{
		SeedDoc:     seedPath,
		FindingsDoc: findings,
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if res.Topic != "Why does checkout retry forever?" {
		t.Errorf("Topic = %q, want derived from '# Investigation:' heading", res.Topic)
	}

	gotFindings, err := os.ReadFile(findings)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	if string(gotFindings) != seed {
		t.Errorf("findings doc not verbatim copy of seed:\nGOT:\n%s\nWANT:\n%s", gotFindings, seed)
	}
}

func TestBootstrap_TopicScaffold(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	findings := filepath.Join(dir, "findings.md")

	res, err := Bootstrap(context.Background(), BootstrapInput{
		Topic:       "Why is checkout flaky?",
		FindingsDoc: findings,
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if res.Topic != "Why is checkout flaky?" {
		t.Errorf("Topic = %q, want %q", res.Topic, "Why is checkout flaky?")
	}

	body, err := os.ReadFile(findings)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"# Investigation: Why is checkout flaky?",
		"## TLDR",
		"## Question",
		"## Prior work",
		"## System under investigation",
		"## Approach",
		"## Findings",
		"## Unknowns / Assumptions",
		"## Conclusion",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("scaffold missing section %q", want)
		}
	}
	if strings.Contains(got, "## Prior Entire Context") {
		t.Errorf("scaffold unexpectedly contains 'Prior Entire Context' when none was passed")
	}
}

func TestBootstrap_TopicScaffoldWithPriorEntireContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	findings := filepath.Join(dir, "findings.md")

	priorBlock := "Prior session abc123 worked on the same area.\nConclusion: similar root cause."
	_, err := Bootstrap(context.Background(), BootstrapInput{
		Topic:              "Why is checkout flaky?",
		PriorEntireContext: priorBlock,
		FindingsDoc:        findings,
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	body, err := os.ReadFile(findings)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "## Prior Entire Context") {
		t.Errorf("scaffold missing 'Prior Entire Context' heading when prior block passed")
	}
	if !strings.Contains(got, "Prior session abc123 worked on the same area.") {
		t.Errorf("scaffold missing prior block content")
	}
	// Prior block should be inserted between Question and Prior work.
	idxQuestion := strings.Index(got, "## Question")
	idxPrior := strings.Index(got, "## Prior Entire Context")
	idxPriorWork := strings.Index(got, "## Prior work")
	if idxQuestion >= idxPrior || idxPrior >= idxPriorWork {
		t.Errorf("expected Question < PriorEntireContext < Prior work, got %d < %d < %d", idxQuestion, idxPrior, idxPriorWork)
	}
}

func TestBootstrap_IssueLinkSeed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	findings := filepath.Join(dir, "findings.md")

	seedBytes := []byte("# Investigation: gh#42 — checkout times out\n\n**Source:** https://github.com/o/r/issues/42\n\n## Question\n\nbody…\n")
	res, err := Bootstrap(context.Background(), BootstrapInput{
		IssueLinkSeed:  seedBytes,
		IssueLinkTopic: "checkout times out",
		FindingsDoc:    findings,
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if res.Topic != "checkout times out" {
		t.Errorf("Topic = %q, want from IssueLinkTopic", res.Topic)
	}

	body, err := os.ReadFile(findings)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	if string(body) != string(seedBytes) {
		t.Errorf("findings doc not verbatim copy of issue-link bytes")
	}
}

func TestBootstrap_RequiresOneInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := Bootstrap(context.Background(), BootstrapInput{
		FindingsDoc: filepath.Join(dir, "f.md"),
	})
	if err == nil {
		t.Fatalf("expected error when no input variant provided")
	}
}

func TestDeriveTopicFromSeed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		filename string
		want     string
	}{
		{
			name:     "investigation heading wins",
			body:     "# Investigation: Why slow?\n\n# Other heading\n",
			filename: "ignored.md",
			want:     "Why slow?",
		},
		{
			name:     "first H1 when no investigation heading",
			body:     "Some preface.\n\n# First Heading\n\n## Sub heading\n",
			filename: "ignored.md",
			want:     "First Heading",
		},
		{
			name:     "filename fallback when no headings",
			body:     "no headings here\nat all\n",
			filename: "/path/to/why-slow.md",
			want:     "why-slow",
		},
		{
			name:     "filename fallback with no extension",
			body:     "",
			filename: "/tmp/nofile",
			want:     "nofile",
		},
		{
			name:     "investigation heading trims spaces",
			body:     "#   Investigation:    spaced topic   \n",
			filename: "ignored.md",
			want:     "Investigation:    spaced topic",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DeriveTopicFromSeed([]byte(tc.body), tc.filename)
			if got != tc.want {
				t.Errorf("DeriveTopicFromSeed(%q, %q) = %q, want %q", tc.body, tc.filename, got, tc.want)
			}
		})
	}
}

func TestSlugifyTopic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "simple", input: "checkout flaky", want: "checkout-flaky"},
		{name: "punctuation", input: "Why is checkout flaky?!", want: "why-is-checkout-flaky"},
		{name: "leading and trailing dashes trimmed", input: "  ---hello world---  ", want: "hello-world"},
		{name: "non-ascii squashed", input: "café résumé", want: "caf-r-sum"},
		{name: "all punctuation falls back", input: "!!!", want: "investigation"},
		{name: "empty falls back", input: "", want: "investigation"},
		{name: "mixed case lowercased", input: "WhyIsThisHappening", want: "whyisthishappening"},
		{name: "long input truncated to 60", input: strings.Repeat("a", 100), want: strings.Repeat("a", 60)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SlugifyTopic(tc.input); got != tc.want {
				t.Errorf("SlugifyTopic(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
