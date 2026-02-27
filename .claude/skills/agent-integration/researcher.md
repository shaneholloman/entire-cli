# Research Command

Assess whether a target AI coding agent's hook/lifecycle model is compatible with the Entire CLI before writing any Go code.

## Procedure

### Phase 1: Architecture Inspection

Read these repo files to understand the Entire lifecycle model that the agent must integrate with:

**Required reading:**

1. `cmd/entire/cli/agent/agent.go` — Read to find the `Agent` interface and all optional capability interfaces
2. `cmd/entire/cli/agent/event.go` — Read to find all `EventType` constants (the normalized lifecycle events agents must map to)
3. `cmd/entire/cli/hook_registry.go` — How native hook names are registered and routed
4. `cmd/entire/cli/lifecycle.go` — `DispatchLifecycleEvent` handler
5. `docs/architecture/agent-guide.md` — Full implementation guide
6. `docs/architecture/agent-integration-checklist.md` — Validation criteria

**Reference implementations:** Run `Glob("cmd/entire/cli/agent/*/")` to discover all existing agent packages. Pick 1-2 as reference. In each, focus on `lifecycle.go` (ParseHookEvent), `hooks.go` (HookSupport), and `types.go` (hook input structs).

### Phase 2: Static Capability Checks

Non-destructive CLI probing. Record PASS/WARN/FAIL for each:

| Check | Command | PASS | FAIL |
|-------|---------|------|------|
| Binary present | `command -v $AGENT_BIN` | Found | Not found (blocker) |
| Help output | `$AGENT_BIN --help` or `$AGENT_BIN help` | Available | No help |
| Version info | `$AGENT_BIN --version` or `$AGENT_BIN version` | Available | N/A |
| Hook keywords | Scan help for: hook, lifecycle, callback, event, trigger, pre-, post-, plugin, extension | Found | None found |
| Session keywords | Scan help for: session, resume, continue, history, transcript, context | Found | None found |
| Config directory | Check `~/.$AGENT_SLUG/`, `~/.config/$AGENT_SLUG/`, `./$AGENT_SLUG/`, `./.${AGENT_SLUG}/` | Found | None found |
| Documentation | Web search for hook/plugin/extension docs | Found | None found |

### Phase 3: Test Script Creation

Based on Phase 2 findings, create an **agent-specific** test script:

```
scripts/test-$AGENT_SLUG-agent-integration.sh
```

The script is tailored to the specific agent's hook mechanism (not a generic template). Adapt the hook wiring section based on what Phase 2 discovered.

**Script structure:**

```bash
#!/usr/bin/env bash
set -euo pipefail

AGENT_NAME="..."
AGENT_SLUG="..."
AGENT_BIN="..."
PROBE_DIR=".entire/tmp/probe-${AGENT_SLUG}-$(date +%s)"
```

**Required sections:**

1. **Static checks** — Re-runnable binary/version/help checks
2. **Hook wiring** — Create workspace-local config that intercepts hooks and dumps stdin JSON to `$PROBE_DIR/captures/<event-name>-<timestamp>.json`
3. **Run modes:**
   - `--run-cmd '<cmd>'` — Automated: launch agent, wait, collect
   - `--manual-live` — Interactive: user runs agent manually, presses Enter
4. **Capture collection** — List and pretty-print all payload files
5. **Cleanup** — Restore original config (unless `--keep-config`)
6. **Verdict** — PASS/WARN/FAIL per lifecycle event + COMPATIBLE/PARTIAL/INCOMPATIBLE

### Phase 4: Execution & Analysis

Run the script and analyze:

1. **Execute**: `chmod +x scripts/test-$AGENT_SLUG-agent-integration.sh && scripts/test-$AGENT_SLUG-agent-integration.sh --manual-live`
2. **For each captured payload**: show command, artifact path, decoded JSON
3. **Lifecycle mapping**: native hook name → Entire EventType
4. **Field coverage**: which `Event` struct fields can be populated per event

### Phase 5: Compatibility Report

Generate structured markdown output directly to the user:

```markdown
# Agent Compatibility Report: $AGENT_NAME

**Date:** YYYY-MM-DD
**Agent:** $AGENT_NAME v$VERSION
**Binary:** $AGENT_BIN
**Verdict:** COMPATIBLE / PARTIAL / INCOMPATIBLE

## Static Capability Checks

| Check | Result | Notes |
|-------|--------|-------|
| Binary present | PASS/FAIL | path |
| Help available | PASS/FAIL | |
| Hook keywords found | PASS/WARN/FAIL | keywords found |
| Session concept | PASS/WARN/FAIL | |
| Config directory | PASS/WARN/FAIL | path |
| Documentation | PASS/WARN/FAIL | URLs |

## Lifecycle Event Mapping

For each EventType constant found in `cmd/entire/cli/agent/event.go`, create a row:

| Entire EventType | Native Hook | Status | Fields Available |
|-----------------|-------------|--------|-----------------|
| (one row per EventType from event.go) | ? | MAPPED/PARTIAL/MISSING | |

## Required Interface Feasibility

For each interface defined in `cmd/entire/cli/agent/agent.go`, assess feasibility:

| Interface | Feasible | Complexity | Notes |
|-----------|----------|------------|-------|
| Agent (core) | Yes/No/Partial | Low/Med/High | |
| (one row per optional interface from agent.go) | ... | ... | |

## Integration Gaps

1. **[HIGH/MED/LOW]** Description and impact
2. ...

## Recommended Adapter Approach

- Which interfaces to implement
- Complexity estimate (files, LOC)
- Similar implementation to use as template
- Key challenges

## Artifacts

- Test script: `scripts/test-$AGENT_SLUG-agent-integration.sh`
- Captured payloads: `.entire/tmp/probe-$AGENT_SLUG-*/captures/`
```

## Blocker Handling

If blocked at any point (auth, sandbox, binary not found):

1. State the exact blocker
2. Provide the exact command for the user to run manually
3. Explain what output to paste back
4. Continue with provided output

## Constraints

- **No Go code.** This command produces a feasibility report and test script only.
- **Non-destructive.** All artifacts go under `.entire/tmp/` (gitignored).
- **Agent-specific scripts.** Adapt based on Phase 2 findings, not a generic template.
- **Ask, don't assume.** If the hook mechanism is unclear, ask the user.
