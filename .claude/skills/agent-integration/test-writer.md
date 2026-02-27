# Write-Tests Command

Generate the E2E test suite for a new agent integration. Uses the research report's findings and the existing E2E test infrastructure.

## Prerequisites

- The research command should have been run first (or equivalent knowledge of the agent's hook model)
- If no research report exists, ask the user about the agent's hook events, transcript format, and config mechanism

## Procedure

### Step 1: Read E2E Test Infrastructure

Read these files to understand the existing test patterns:

1. `e2e/tests/main_test.go` — `TestMain` builds the CLI binary (via `entire.BinPath()`), runs preflight checks for required binaries (git, tmux, agent CLIs), sets up artifact directories, and configures env
2. `e2e/testutil/repo.go` — `RepoState` struct (holds agent, dir, artifact dir, head/checkpoint refs), `SetupRepo` (creates temp git repo, runs `entire enable`, patches settings), `ForEachAgent` (runs a test per registered agent with repo setup, concurrency gating, and timeout scaling)
3. `e2e/agents/agent.go` — `Agent` interface (`Name`, `Binary`, `EntireAgent`, `PromptPattern`, `TimeoutMultiplier`, `RunPrompt`, `StartSession`, `Bootstrap`, `IsTransientError`), `Register()` for agent self-registration in `init()`, `RegisterGate()` for concurrency limits, `AcquireSlot`/`ReleaseSlot` for gating
4. `e2e/agents/tmux.go` — `TmuxSession` for interactive PTY-based tests: `NewTmuxSession`, `Send`, `SendKeys`, `WaitFor` (with settle-time logic), `Capture`, `Close`
5. `e2e/testutil/assertions.go` — Rich assertion helpers: `AssertFileExists`, `WaitForFileExists`, `AssertNewCommits`, `WaitForCheckpoint`, `AssertCheckpointAdvanced`, `AssertHasCheckpointTrailer`, `AssertCheckpointExists`, `AssertCommitLinkedToCheckpoint`, `AssertCheckpointMetadataComplete`, `ValidateCheckpointDeep`, and many more
6. `e2e/testutil/metadata.go` — `CheckpointMetadata`, `SessionMetadata`, `TokenUsage`, `Attribution`, `SessionRef` types; `CheckpointPath()` helper for sharded directory layout
7. `e2e/entire/entire.go` — CLI wrapper: `BinPath()` (builds from source or uses `E2E_ENTIRE_BIN`), `Enable`, `Disable`, `RewindList`, `Rewind`, `RewindLogsOnly`, `Explain`, `ExplainGenerate`, `ExplainCommit`, `Resume`
8. `e2e/testutil/artifacts.go` — Automatic artifact capture via `t.Cleanup`: `CaptureArtifacts` saves git-log, git-tree, checkpoint metadata, entire logs, and tmux pane content

### Step 2: Read Existing E2E Test Scenarios

Run `Glob("e2e/tests/*_test.go")` to find all existing test files. Read a few to understand the patterns:
- How tests use `testutil.ForEachAgent` with a timeout and callback `func(t, s, ctx)`
- How prompts are written inline (no separate prompt template file)
- How `s.RunPrompt`, `s.Git`, `s.StartSession`, `s.WaitFor`, `s.Send` are used
- How assertions validate checkpoints, rewind, metadata, etc.

### Step 3: Read Checkpoint Scenarios Doc

Read `docs/architecture/checkpoint-scenarios.md` for the state machine and scenarios the tests should cover.

### Step 4: Create Agent Implementation

Add a new `Agent` implementation in `e2e/agents/${agent_slug}.go`:

**Pattern to follow** (based on existing implementations like `claude.go`, `gemini.go`, `opencode.go`):

```go
package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func init() {
	if env := os.Getenv("E2E_AGENT"); env != "" && env != "${agent-slug}" {
		return
	}
	Register(&${AgentName}{})
	// Optional: limit concurrency for rate-limited agents
	// RegisterGate("${agent-slug}", 1)
}

type ${AgentName} struct{}

func (a *${AgentName}) Name() string               { return "${agent-slug}" }
func (a *${AgentName}) Binary() string             { return "${binary-name}" }
func (a *${AgentName}) EntireAgent() string        { return "${entire-agent-name}" }
func (a *${AgentName}) PromptPattern() string      { return `${prompt-regex}` }
func (a *${AgentName}) TimeoutMultiplier() float64 { return 1.0 }

func (a *${AgentName}) Bootstrap() error {
	// CI-specific setup: auth config, API key injection, warmup, etc.
	// Must be idempotent. Called once before any tests run.
	return nil
}

func (a *${AgentName}) IsTransientError(out Output, err error) bool {
	if err == nil {
		return false
	}
	combined := out.Stdout + out.Stderr
	for _, p := range []string{"overloaded", "rate limit", "503", "529"} {
		if strings.Contains(combined, p) {
			return true
		}
	}
	return false
}

func (a *${AgentName}) RunPrompt(ctx context.Context, dir string, prompt string, opts ...Option) (Output, error) {
	cfg := &runConfig{}
	for _, o := range opts {
		o(cfg)
	}

	args := []string{/* agent CLI flags for non-interactive prompt execution */}
	cmd := exec.CommandContext(ctx, a.Binary(), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ENTIRE_TEST_TTY=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return Output{
		Command:  a.Binary() + " " + strings.Join(args, " "),
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, err
}

func (a *${AgentName}) StartSession(ctx context.Context, dir string) (Session, error) {
	// Use NewTmuxSession for interactive PTY support.
	// Return nil if agent doesn't support interactive mode.
	name := fmt.Sprintf("${agent-slug}-test-%d", time.Now().UnixNano())
	s, err := NewTmuxSession(name, dir, nil, a.Binary(), /* interactive args */)
	if err != nil {
		return nil, err
	}
	return s, nil
}
```

Key implementation details:
- Self-register in `init()` with `Register()`, gated by `E2E_AGENT` env var
- Use `RegisterGate("name", N)` if the agent's API has strict rate limits (e.g., Gemini uses gate of 1)
- `Bootstrap()` handles CI-specific one-time setup (auth config, API key injection)
- `IsTransientError()` identifies retryable API failures — `RepoState.RunPrompt` retries once on transient errors
- `RunPrompt()` uses `exec.CommandContext` with `Setpgid: true` and process-group kill for clean cancellation
- `StartSession()` uses `NewTmuxSession` for interactive PTY tests; return `nil` if interactive mode isn't supported
- Use the research report to determine CLI flags, prompt passing mechanism, and env vars

### Step 5: Update SetupRepo (if needed)

Check if `testutil.SetupRepo` in `e2e/testutil/repo.go` needs agent-specific configuration. Look for the existing `if agent.Name() == "opencode"` block as an example:

- Agent-specific config files that must exist before `entire enable`
- Permission or auth config files
- Environment variables needed for hook testing

If no special setup is needed, skip this step.

### Step 6: Write E2E Test Scenarios

Existing tests are agent-agnostic (they use `ForEachAgent`), so they should already work with the new agent. **Only create new test files if the agent has unique behaviors** that existing scenarios don't cover.

Check if all existing scenarios work by reviewing:
- Does the agent support non-interactive prompt mode? (required for `RunPrompt`)
- Does the agent create files when prompted? (required for basic workflow)
- Does the agent support git operations? (required for commit scenarios)
- Does the agent support interactive mode? (required for interactive tests — can return nil from `StartSession`)

If the agent has unique behaviors, create new test files in `e2e/tests/`:

```go
//go:build e2e

package tests

import (
	"context"
	"testing"
	"time"

	"github.com/entireio/cli/e2e/testutil"
)

func TestAgentSpecificBehavior(t *testing.T) {
	testutil.ForEachAgent(t, 2*time.Minute, func(t *testing.T, s *testutil.RepoState, ctx context.Context) {
		// Skip for agents that don't apply
		if s.Agent.Name() != "${agent-slug}" {
			t.Skip("only applies to ${agent-slug}")
		}

		// Use s.RunPrompt for non-interactive, s.StartSession for interactive
		_, err := s.RunPrompt(t, ctx,
			"create a file at hello.txt with 'hello world'. Do not ask for confirmation.")
		if err != nil {
			t.Fatalf("agent failed: %v", err)
		}

		testutil.AssertFileExists(t, s.Dir, "hello.txt")
	})
}
```

See `e2e/README.md` for the canonical reference on structure, debugging, and CI workflows.

### Step 7: Verify

After writing the code:

1. **Lint check**: `mise run lint` — ensure no lint errors
2. **Compile check**: `go test -c -tags=e2e ./e2e/tests` — compile-only with the build tag to verify the code compiles
3. **List what to run**: Print the exact E2E commands but do NOT run them (they cost money):
   ```bash
   mise run test:e2e:${agent_slug} TestSingleSessionManualCommit
   ```
4. **Debug failures**: If tests fail, use `/debug-e2e {artifact-dir}` to diagnose — artifacts are auto-captured to `e2e/artifacts/{timestamp}/`
5. **Add mise task**: Remind the user to add a `test:e2e:${agent_slug}` task in `mise.toml` and update CI workflows

## Key Conventions

- **Build tag**: All E2E test files must have `//go:build e2e` as the first line
- **Package**: `package tests` for test files in `e2e/tests/`; `package agents` for agent implementations in `e2e/agents/`
- **Parallel**: `ForEachAgent` handles parallelism — do not call `t.Parallel()` inside the callback
- **Repo setup**: `ForEachAgent` calls `SetupRepo` automatically — do not call it manually
- **Prompts**: Write prompts inline in the test. Include "Do not ask for confirmation" to prevent agent stalling
- **Assertions**: Use helpers from `e2e/testutil/assertions.go` — see `AssertFileExists`, `WaitForCheckpoint`, `AssertCommitLinkedToCheckpoint`, `ValidateCheckpointDeep`, etc.
- **CLI operations**: Use the `e2e/entire` package (`entire.Enable`, `entire.RewindList`, `entire.Rewind`, etc.) — never call the binary via raw `exec.Command`
- **No hardcoded paths**: Use `s.Dir` for repo paths, `s.ArtifactDir` for artifacts
- **Console logging**: All operations through `s.RunPrompt`, `s.Git`, `s.Send`, `s.WaitFor` are automatically logged to `console.log`
- **Transient errors**: `s.RunPrompt` auto-retries once on transient API errors via `IsTransientError`
- **Interactive tests**: Use `s.StartSession`, `s.Send`, `s.WaitFor` — tmux pane is auto-captured in artifacts
- **Run commands**: `mise run test:e2e:${slug} TestName` — see `e2e/README.md` for all options
- **Do NOT run E2E tests**: They make real API calls. Only write the code and print commands.
- **Debugging failures**: If the user runs tests and they fail, use `/debug-e2e` with the artifact directory to diagnose CLI-level issues (hooks, checkpoints, session phases, attribution)

## Output

Summarize what was created/modified:
- Files added or modified
- New agent implementation details (how it invokes the agent, auth setup, concurrency gate)
- Any agent-specific test scenarios added
- Commands to run the tests (for user to execute manually)
- If tests fail, suggest using `/debug-e2e {artifact-dir}` for root cause analysis
- Reminder to update `mise.toml` and CI workflows
