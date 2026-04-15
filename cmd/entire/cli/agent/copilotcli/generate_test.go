package copilotcli

import (
	"context"
	"os/exec"
	"slices"
	"testing"
)

// TestGenerateText_PromptViaStdin verifies that the prompt is passed to the
// Copilot CLI via stdin (not as a CLI argument), and that expected flags are
// present. Uses `cat` as a fake runner so the stdin round-trip is end-to-end
// observable through the returned output.
func TestGenerateText_PromptViaStdin(t *testing.T) {
	// Not parallel: mutates package-level copilotCommandRunner.
	originalRunner := copilotCommandRunner
	t.Cleanup(func() {
		copilotCommandRunner = originalRunner
	})

	var capturedArgs []string
	copilotCommandRunner = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		capturedArgs = args
		return exec.CommandContext(ctx, "cat")
	}

	ag := &CopilotCLIAgent{}
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
	if !slices.Contains(capturedArgs, "--allow-all-tools") {
		t.Fatalf("expected --allow-all-tools in args, got %v", capturedArgs)
	}
	if !slices.Contains(capturedArgs, "--disable-builtin-mcps") {
		t.Fatalf("expected --disable-builtin-mcps in args, got %v", capturedArgs)
	}
}

func TestGenerateText_ModelFlagPassedWhenSet(t *testing.T) {
	// Not parallel: mutates package-level copilotCommandRunner.
	originalRunner := copilotCommandRunner
	t.Cleanup(func() {
		copilotCommandRunner = originalRunner
	})

	var capturedArgs []string
	copilotCommandRunner = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		capturedArgs = args
		return exec.CommandContext(ctx, "cat")
	}

	ag := &CopilotCLIAgent{}
	if _, err := ag.GenerateText(context.Background(), "prompt", "gpt-5"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	modelIdx := slices.Index(capturedArgs, "--model")
	if modelIdx < 0 || modelIdx+1 >= len(capturedArgs) {
		t.Fatalf("expected --model in args, got %v", capturedArgs)
	}
	if capturedArgs[modelIdx+1] != "gpt-5" {
		t.Fatalf("expected --model gpt-5, got %v", capturedArgs)
	}
}
