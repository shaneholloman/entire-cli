# Implement Command

Build the agent Go package using test-driven development. Uses the research report findings and the E2E test suite as the spec.

## Prerequisites

- The research command's findings (hook events, transcript format, config mechanism)
- The E2E test runner already added (from `write-tests` command)
- If neither exists, read the agent's docs and ask the user about hook events, transcript format, and config

## Procedure

### Step 1: Read Implementation Guide

Read these files thoroughly before writing any code:

1. `docs/architecture/agent-guide.md` — Authoritative implementation guide with code templates. Read thoroughly.
2. `docs/architecture/agent-integration-checklist.md` — Validation criteria for completeness.
3. `cmd/entire/cli/agent/agent.go` — Read to find the exact `Agent` interface and all optional interfaces.
4. `cmd/entire/cli/agent/event.go` — Read to find `EventType` constants and shared parsing helpers.

### Step 2: Read Reference Implementation

Run `Glob("cmd/entire/cli/agent/*/")` to find all existing agent packages. Pick the closest match based on research findings — read a few agents' `hooks.go` files to find one with a similar hook mechanism to your target. Read all `*.go` files (skip `*_test.go` on first pass) in the chosen reference.

### Step 3: Create Package Structure

Create the agent package directory:

```
cmd/entire/cli/agent/$AGENT_SLUG/
```

### Step 4: TDD Cycle — Types

**Red**: Write `types_test.go` with tests for hook input struct parsing:

```go
//go:build !e2e

package $AGENT_SLUG

import (
    "encoding/json"
    "testing"
)

func TestHookInput_Parsing(t *testing.T) {
    t.Parallel()
    // Test that hook JSON payloads deserialize correctly
}
```

**Green**: Write `types.go` with hook input structs:

```go
package $AGENT_SLUG

// HookInput represents the JSON payload from the agent's hooks.
type HookInput struct {
    SessionID      string `json:"session_id"`
    TranscriptPath string `json:"transcript_path"`
    // ... fields from research report's captured payloads
}
```

**Refactor**: Ensure struct tags match the actual JSON field names from the research captures.

Run: `mise run test` to verify.

### Step 5: TDD Cycle — Core Agent

**Red**: Write `${AGENT_SLUG}_test.go` with tests for Identity methods (Name, Type, Description, IsPreview, DetectPresence, ProtectedDirs) and session management methods.

**Green**: Create `${AGENT_SLUG}.go`. Read the `Agent` interface in `cmd/entire/cli/agent/agent.go` for exact method signatures. Read `docs/architecture/agent-guide.md` Step 3 for the full code template. Use `agent.Register(agent.AgentName("$AGENT_SLUG"), New)` in `init()`.

Run: `mise run test`

### Step 6: TDD Cycle — Lifecycle (ParseHookEvent)

This is the **main contribution surface** — mapping native hooks to Entire events.

**Red**: Write `lifecycle_test.go` with tests for each hook name from the research report. Use actual JSON payloads from research captures. Test every EventType mapping, nil returns for pass-through hooks, empty input, and malformed JSON.

**Green**: Create `lifecycle.go`. Read the `HookSupport` interface in `cmd/entire/cli/agent/agent.go` for exact method signatures. Read `docs/architecture/agent-guide.md` Step 4 for the switch-case pattern. Read a reference agent's `lifecycle.go` (find via `Glob("cmd/entire/cli/agent/*/lifecycle.go")`) for the implementation pattern.

Run: `mise run test`

### Step 7: TDD Cycle — Hooks (HookSupport)

**Red**: Write `hooks_test.go` with tests for InstallHooks (creates config, idempotent), UninstallHooks (removes hooks), and AreHooksInstalled (detects presence).

**Green**: Create `hooks.go`. Read the `HookSupport` interface in `cmd/entire/cli/agent/agent.go` for exact signatures. Read `docs/architecture/agent-guide.md` Step 8 for the installation pattern. Read a reference agent's `hooks.go` (find via `Glob("cmd/entire/cli/agent/*/hooks.go")`) for the JSON config file pattern.

Use the research report to determine:
- Which config file to modify (e.g., `.agent/settings.json`)
- How hooks are registered (JSON objects, env vars, etc.)
- What command format to use (`entire hooks $AGENT_SLUG <verb>`)

Run: `mise run test`

### Step 8: TDD Cycle — Transcript

**Red**: Write `transcript_test.go` with tests for reading, chunking, and reassembling transcripts. Use sample data in the agent's native format.

**Green**: Create `transcript.go`. Read the `TranscriptAnalyzer` interface in `cmd/entire/cli/agent/agent.go` if implementing analysis. Read `docs/architecture/agent-guide.md` Transcript Format Guide for JSONL vs JSON patterns. Read a reference agent's `transcript.go` (find via `Glob("cmd/entire/cli/agent/*/transcript.go")`) for the implementation pattern.

Run: `mise run test`

### Step 9: Optional Interfaces

Read `cmd/entire/cli/agent/agent.go` for all optional interfaces. For each one the research report marked as feasible, follow the same TDD cycle: write tests, implement, refactor. Read the corresponding section in `docs/architecture/agent-guide.md` (Optional Interface Decision Tree) for guidance on when each is needed.

### Step 10: Register and Wire Up

1. **Register hook commands**: Search `cmd/entire/cli/` for where hook subcommands are registered and add the new agent
2. **Verify registration**: The `init()` function in `${AGENT_SLUG}.go` should call `agent.Register(agent.AgentName("$AGENT_SLUG"), New)`
3. **Run full test suite**: `mise run test:ci`

### Step 11: Final Validation

Run the complete validation:

```bash
mise run fmt      # Format
mise run lint     # Lint
mise run test:ci  # All tests (unit + integration)
```

Check against the integration checklist (`docs/architecture/agent-integration-checklist.md`):

- [ ] Full transcript stored at every checkpoint
- [ ] Native format preserved
- [ ] All mappable hook events implemented
- [ ] Session storage working
- [ ] Hook installation/uninstallation working
- [ ] Tests pass with `t.Parallel()`

## Key Patterns to Follow

- **Use `agent.ReadAndParseHookInput[T]`** for parsing hook stdin JSON
- **Use `paths.WorktreeRoot()`** not `os.Getwd()` for git-relative paths
- **Preserve unknown config keys** when modifying agent config files (don't clobber user settings)
- **Use `logging.Debug/Info/Warn/Error`** for internal logging, not `fmt.Print`
- **Keep interface implementations minimal** — only implement what's needed
- **Follow Go idioms** from `.golangci.yml` — check before writing code

## Output

Summarize what was implemented:
- Package directory and files created
- Interfaces implemented (core + optional)
- Hook names registered
- Test coverage (number of test functions, what they cover)
- Any gaps or TODOs remaining
- Commands to run full validation
