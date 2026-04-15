package cursor

import (
	"context"
	"os/exec"
	"slices"
	"testing"
)

// TestGenerateText_PromptViaStdin verifies that the prompt is passed to the
// Cursor CLI via stdin (not as a positional argument), and that expected flags
// are present. Uses `cat` as a fake runner so the stdin round-trip is
// end-to-end observable through the returned output.
func TestGenerateText_PromptViaStdin(t *testing.T) {
	// Not parallel: mutates package-level cursorCommandRunner.
	originalRunner := cursorCommandRunner
	t.Cleanup(func() {
		cursorCommandRunner = originalRunner
	})

	var capturedArgs []string
	cursorCommandRunner = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		capturedArgs = args
		return exec.CommandContext(ctx, "cat")
	}

	ag := &CursorAgent{}
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
	for _, expected := range []string{"--print", "--force", "--trust", "--workspace"} {
		if !slices.Contains(capturedArgs, expected) {
			t.Fatalf("expected %s in args, got %v", expected, capturedArgs)
		}
	}
}

func TestGenerateText_ModelFlagPassedWhenSet(t *testing.T) {
	// Not parallel: mutates package-level cursorCommandRunner.
	originalRunner := cursorCommandRunner
	t.Cleanup(func() {
		cursorCommandRunner = originalRunner
	})

	var capturedArgs []string
	cursorCommandRunner = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		capturedArgs = args
		return exec.CommandContext(ctx, "cat")
	}

	ag := &CursorAgent{}
	if _, err := ag.GenerateText(context.Background(), "prompt", "sonnet-4"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	modelIdx := slices.Index(capturedArgs, "--model")
	if modelIdx < 0 || modelIdx+1 >= len(capturedArgs) {
		t.Fatalf("expected --model in args, got %v", capturedArgs)
	}
	if capturedArgs[modelIdx+1] != "sonnet-4" {
		t.Fatalf("expected --model sonnet-4, got %v", capturedArgs)
	}
}
