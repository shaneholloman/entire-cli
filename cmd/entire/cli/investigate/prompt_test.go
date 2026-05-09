package investigate

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "update golden files in testdata/")

// assertGoldenString writes/reads a golden file under testdata/. When
// -update is passed it overwrites the golden, otherwise it compares.
func assertGoldenString(t *testing.T, goldenPath, got string) {
	t.Helper()
	abs, err := filepath.Abs(goldenPath)
	if err != nil {
		t.Fatalf("abs golden path: %v", err)
	}
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	wantBytes, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read golden %s: %v (run go test ./... -update to create)", goldenPath, err)
	}
	if want := string(wantBytes); want != got {
		t.Errorf("prompt mismatch (golden=%s)\nWANT:\n%s\n\nGOT:\n%s", goldenPath, want, got)
	}
}

func TestComposeInvestigatePrompt_FirstRound(t *testing.T) {
	t.Parallel()

	got := ComposeInvestigatePrompt(ComposeInput{
		Topic:     "Why is checkout flaky?",
		AgentName: "claude-code",
		Round:     1,
		Turn:      1,
		Files: Files{
			Findings: "/abs/repo/.entire/investigations/why-is-checkout-flaky.md",
			Timeline: "/abs/repo/.entire/investigations/why-is-checkout-flaky-timeline.md",
		},
	})

	assertGoldenString(t, "testdata/prompt-first-round.txt", got)

	// Sanity checks the golden doesn't catch on its own.
	for _, want := range []string{
		"You are agent: claude-code",
		"Round: 1    (turn 1 overall in this session)",
		"Topic: Why is checkout flaky?",
		"Findings: /abs/repo/.entire/investigations/why-is-checkout-flaky.md",
		"## Turn 1 — claude-code",
		"approve | request-changes | abstain",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing substring %q", want)
		}
	}
}

func TestComposeInvestigatePrompt_MidLoop(t *testing.T) {
	t.Parallel()

	got := ComposeInvestigatePrompt(ComposeInput{
		Topic:     "Why is checkout flaky?",
		AgentName: "codex",
		Round:     2,
		Turn:      5,
		Files: Files{
			Findings: "/abs/repo/.entire/investigations/why-is-checkout-flaky.md",
			Timeline: "/abs/repo/.entire/investigations/why-is-checkout-flaky-timeline.md",
		},
	})

	assertGoldenString(t, "testdata/prompt-mid-loop.txt", got)

	if !strings.Contains(got, "Round: 2    (turn 5 overall in this session)") {
		t.Errorf("expected mid-loop round/turn coordinates")
	}
	if !strings.Contains(got, "## Turn 5 — codex") {
		t.Errorf("expected timeline heading for turn 5 as codex")
	}
}

func TestComposeInvestigatePrompt_WithAlwaysPrompt(t *testing.T) {
	t.Parallel()

	got := ComposeInvestigatePrompt(ComposeInput{
		Topic:        "Why is checkout flaky?",
		AgentName:    "claude-code",
		Round:        1,
		Turn:         1,
		AlwaysPrompt: "Project rule: cite test names in evidence.",
		Files: Files{
			Findings: "/abs/findings.md",
			Timeline: "/abs/timeline.md",
		},
	})

	assertGoldenString(t, "testdata/prompt-with-always.txt", got)

	if !strings.Contains(got, "Project rule: cite test names in evidence.") {
		t.Errorf("AlwaysPrompt was not appended verbatim")
	}
	// Should appear AFTER the main body — guard against accidental prepend.
	idxAlways := strings.Index(got, "Project rule: cite test names in evidence.")
	idxBody := strings.Index(got, "Exit when you've appended your turn entry.")
	if idxAlways < idxBody {
		t.Errorf("AlwaysPrompt rendered before body (idxAlways=%d idxBody=%d)", idxAlways, idxBody)
	}
}

func TestComposeInvestigatePrompt_WithPriorContext(t *testing.T) {
	t.Parallel()

	got := ComposeInvestigatePrompt(ComposeInput{
		Topic:     "Why is checkout flaky?",
		AgentName: "claude-code",
		Round:     1,
		Turn:      1,
		PriorContext: "Prior checkpoint a3b2c4d5e6f7 investigated a similar symptom.\n" +
			"Conclusion: timeout in /api/checkout was the cause.",
		Files: Files{
			Findings: "/abs/findings.md",
			Timeline: "/abs/timeline.md",
		},
	})

	assertGoldenString(t, "testdata/prompt-with-prior-context.txt", got)

	if !strings.Contains(got, "## Prior context") {
		t.Errorf("expected '## Prior context' block when PriorContext is set")
	}
	if !strings.Contains(got, "Prior checkpoint a3b2c4d5e6f7") {
		t.Errorf("PriorContext bytes not rendered")
	}
	// Prior context should come BEFORE the body, not be appended at the end.
	idxPrior := strings.Index(got, "## Prior context")
	idxBody := strings.Index(got, "You are participating in an autonomous")
	if idxPrior > idxBody {
		t.Errorf("PriorContext should render before body (idxPrior=%d idxBody=%d)", idxPrior, idxBody)
	}
}
