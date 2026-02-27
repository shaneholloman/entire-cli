---
name: agent-integration
description: >
  Run all three agent integration phases sequentially: research, write-tests,
  and implement. For individual phases, use /agent-integration:research,
  /agent-integration:write-tests, or /agent-integration:implement.
  Use when the user says "integrate agent", "add agent support", or wants
  to run the full agent integration pipeline end-to-end.
---

# Agent Integration — Full Pipeline

Run all three phases of agent integration in a single session. Parameters are collected once and reused across all phases.

## Parameters

Collect these before starting (ask the user if not provided):

| Parameter | Example | Description |
|-----------|---------|-------------|
| `AGENT_NAME` | "Windsurf" | Human-readable agent name |
| `AGENT_SLUG` | "windsurf" | Lowercase slug for file/directory paths |
| `AGENT_BIN` | "windsurf" | CLI binary name |
| `LIVE_COMMAND` | "windsurf --project ." | Full command to launch agent |
| `EVENTS_OR_UNKNOWN` | "unknown" | Known hook event names, or "unknown" |

## Architecture References

These documents define the agent integration contract:

- **Implementation guide**: `docs/architecture/agent-guide.md` — Step-by-step code templates, event mapping, testing patterns
- **Integration checklist**: `docs/architecture/agent-integration-checklist.md` — Design principles and validation criteria

## Pipeline

Run these three phases in order. Each phase builds on the previous phase's output.

### Phase 1: Research

Assess whether the agent's hook/lifecycle model is compatible with the Entire CLI.

Read and follow the research procedure from `.claude/skills/agent-integration/researcher.md`.

**Expected output:** Compatibility report with lifecycle event mapping, interface feasibility assessment, and a test script at `scripts/test-$AGENT_SLUG-agent-integration.sh`.

**Gate:** If the verdict is INCOMPATIBLE, stop and discuss with the user before proceeding.

### Phase 2: Write Tests

Generate the E2E test suite based on the research findings.

Read and follow the write-tests procedure from `.claude/skills/agent-integration/test-writer.md`.

**Expected output:** E2E agent runner at `e2e/agents/$AGENT_SLUG.go` and any agent-specific test scenarios.

### Phase 3: Implement

Build the Go agent package using test-driven development.

Read and follow the implement procedure from `.claude/skills/agent-integration/implementer.md`.

**Expected output:** Complete agent package at `cmd/entire/cli/agent/$AGENT_SLUG/` with all tests passing.

## Final Validation

After all three phases, run the complete validation:

```bash
mise run fmt      # Format
mise run lint     # Lint
mise run test:ci  # All tests (unit + integration)
```

Summarize:
- Compatibility verdict from Phase 1
- Files created in Phases 2 and 3
- Test coverage
- Any remaining TODOs or gaps
