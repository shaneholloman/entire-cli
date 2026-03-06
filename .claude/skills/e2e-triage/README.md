# E2E Triage Skill

Triage E2E test failures by re-running tests, classifying failures as flaky vs real-bug, and taking appropriate action (fixes in local mode, PRs/issues in CI mode).

## Two Modes

| Mode | Trigger | Action on flaky | Action on real-bug |
|------|---------|----------------|--------------------|
| **Local** | User invokes `/e2e-triage` | Presents findings, applies fixes in working tree | Presents root cause analysis, applies fix if approved |
| **CI** | `WORKFLOW_RUN_ID` env var set (via `e2e-triage.yml`) | Creates batched PR | Creates or comments on GitHub issue |

## Local Usage

```
# Triage a specific test
/e2e-triage TestInteractiveMultiStep

# Triage a specific test for one agent
/e2e-triage TestInteractiveMultiStep --agent claude-code

# Triage multiple tests
/e2e-triage TestInteractiveMultiStep TestCheckpointRewind

# Analyze existing artifacts (skip re-running)
/e2e-triage /path/to/artifact/dir
```

The skill will:
1. Run the test(s) up to 3 times (first run, re-run on failure, tiebreaker if split)
2. Analyze artifacts (`console.log`, `entire.log`, `git-log.txt`, checkpoint metadata)
3. Classify each failure and present findings
4. Ask before applying any fixes

## CI Mode

Triggered automatically by the `e2e-triage.yml` workflow. Downloads artifacts, re-runs failures via `e2e-isolated.yml`, then:
- **Flaky fixes** — batched into a single PR (`fix/e2e-flaky-<id>`)
- **Real bugs** — filed as GitHub issues (with dedup check)
- **Summary** — writes `triage-summary.json` for Slack notifications

## Classification Logic

Re-run results are the primary signal:

| Pattern | Classification |
|---------|---------------|
| FAIL / PASS / PASS | Flaky |
| FAIL / PASS / FAIL | Flaky (non-deterministic) |
| FAIL / FAIL / PASS | Flaky (non-deterministic) |
| FAIL / FAIL / FAIL | Real-bug OR flaky (test-bug) — depends on root cause location |

**Key distinction for consistent failures:** if the root cause is in `cmd/entire/cli/` (product code), it's a **real-bug**. If it's in `e2e/` (test infra), it's **flaky (test-bug)**.

## Key Files

- `SKILL.md` — Full skill definition with all steps, classification rules, and action templates
- `../../.github/workflows/e2e-triage.yml` — CI workflow that triggers CI mode
- `../../scripts/download-e2e-artifacts.sh` — Downloads artifacts from CI runs
