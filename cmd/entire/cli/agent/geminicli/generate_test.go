package geminicli

import (
	"context"
	"os/exec"
	"slices"
	"testing"
)

// TestGenerateText_PromptViaStdin verifies that the prompt is passed to the
// Gemini CLI via stdin, and that the minimal `-p " "` placeholder is present
// to trigger headless mode (per gemini --help, -p is required for
// non-interactive mode and its value is appended to any stdin input).
func TestGenerateText_PromptViaStdin(t *testing.T) {
	// Not parallel: mutates package-level geminiCommandRunner.
	originalRunner := geminiCommandRunner
	t.Cleanup(func() {
		geminiCommandRunner = originalRunner
	})

	var capturedArgs []string
	geminiCommandRunner = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		capturedArgs = args
		return exec.CommandContext(ctx, "cat")
	}

	ag := &GeminiCLIAgent{}
	prompt := "this prompt must arrive via stdin, not argv"
	result, err := ag.GenerateText(context.Background(), prompt, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != prompt {
		t.Fatalf("stdin round-trip failed: result=%q, want=%q", result, prompt)
	}

	if slices.Contains(capturedArgs, prompt) {
		t.Fatalf("prompt leaked into argv: %v", capturedArgs)
	}
	pIdx := slices.Index(capturedArgs, "-p")
	if pIdx < 0 || pIdx+1 >= len(capturedArgs) {
		t.Fatalf("expected -p in args, got %v", capturedArgs)
	}
	// The placeholder is a single space; the real prompt rides on stdin.
	if capturedArgs[pIdx+1] != " " {
		t.Fatalf("expected -p followed by space placeholder, got -p %q", capturedArgs[pIdx+1])
	}
}

func TestGenerateText_ModelFlagPassedWhenSet(t *testing.T) {
	// Not parallel: mutates package-level geminiCommandRunner.
	originalRunner := geminiCommandRunner
	t.Cleanup(func() {
		geminiCommandRunner = originalRunner
	})

	var capturedArgs []string
	geminiCommandRunner = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		capturedArgs = args
		return exec.CommandContext(ctx, "cat")
	}

	ag := &GeminiCLIAgent{}
	if _, err := ag.GenerateText(context.Background(), "prompt", "gemini-2.5-flash"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	modelIdx := slices.Index(capturedArgs, "--model")
	if modelIdx < 0 || modelIdx+1 >= len(capturedArgs) {
		t.Fatalf("expected --model in args, got %v", capturedArgs)
	}
	if capturedArgs[modelIdx+1] != "gemini-2.5-flash" {
		t.Fatalf("expected --model gemini-2.5-flash, got %v", capturedArgs)
	}
}
