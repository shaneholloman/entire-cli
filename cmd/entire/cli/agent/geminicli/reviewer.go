package geminicli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/entireio/cli/cmd/entire/cli/review"
	reviewtypes "github.com/entireio/cli/cmd/entire/cli/review/types"
)

// NewReviewer returns the AgentReviewer for gemini-cli.
//
// Argv shape: gemini -p " " (space placeholder to trigger headless mode).
// Prompt is piped via stdin; per gemini --help the -p flag appends to stdin
// content, so passing a single space lets stdin carry the actual prompt.
// Stdout in this mode is clean assistant output — no chrome filtering needed.
func NewReviewer() *reviewtypes.ReviewerTemplate {
	return &reviewtypes.ReviewerTemplate{
		AgentName: "gemini-cli",
		BuildCmd:  buildGeminiReviewCmd,
		Parser:    parseGeminiOutput,
	}
}

// buildGeminiReviewCmd builds the exec.Cmd for a gemini review run.
// Exposed at package level for test inspection of argv, stdin, and env.
//
// The argv shape and stdin wiring live in geminiSpawner.BuildCmd so they can
// be reused by `entire investigate`; this wrapper composes review-specific
// prompt + env and delegates the spawn.
func buildGeminiReviewCmd(ctx context.Context, cfg reviewtypes.RunConfig) *exec.Cmd {
	prompt := review.ComposeReviewPrompt(cfg)
	env := review.AppendReviewEnv(os.Environ(), "gemini-cli", cfg, prompt)
	return NewSpawner().BuildCmd(ctx, env, prompt)
}

// parseGeminiOutput converts gemini's -p mode stdout into a stream of Events.
// Gemini emits clean assistant output with no chrome — the parser emits Started
// once, then one AssistantText per non-empty line, then Finished{Success: true}
// on clean EOF or RunError + Finished{Success: false} on a torn stream.
//
// Exposed for golden-file contract testing.
func parseGeminiOutput(r io.Reader) <-chan reviewtypes.Event {
	out := make(chan reviewtypes.Event, 32)
	go func() {
		defer close(out)
		out <- reviewtypes.Started{}
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			out <- reviewtypes.AssistantText{Text: line}
		}
		if err := scanner.Err(); err != nil {
			out <- reviewtypes.RunError{Err: fmt.Errorf("read stdout: %w", err)}
			out <- reviewtypes.Finished{Success: false}
			return
		}
		out <- reviewtypes.Finished{Success: true}
	}()
	return out
}
