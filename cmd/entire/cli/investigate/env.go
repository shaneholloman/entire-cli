// Package investigate contains the env-var contract between `entire
// investigate` (which spawns the agent process) and the lifecycle hook (which
// adopts the session), plus the persisted run state for resuming an
// investigation. These names are stable API; renaming any constant is a
// breaking change.
//
// Design rationale: each spawned agent inherits its own copy of the process
// environment, so multi-tenant correctness (multiple worktrees, multi-agent
// runs) holds by construction — one agent's env vars cannot bleed into
// another agent's session. The lifecycle UserPromptSubmit hook reads these
// env vars to tag the in-flight session as an investigate session (Kind =
// "agent_investigate") and records the run/round/turn coordinates.
package investigate

import (
	"strconv"
	"strings"
)

const (
	// EnvSession is the investigate-session indicator. `entire investigate`
	// sets this to "1" on the spawned agent process; the lifecycle hook
	// treats any other value (including unset) as a normal coding session.
	// Kept as a sentinel string rather than a bool so future versions can
	// carry additional metadata in the value without breaking the contract.
	EnvSession = "ENTIRE_INVESTIGATE_SESSION"

	// EnvAgent is the name of the agent spawned for the investigate turn
	// (e.g. "claude-code"). The lifecycle hook requires this to match the
	// hook's agent before tagging the session, preventing stale exported
	// investigate env from tagging sessions for a different agent.
	EnvAgent = "ENTIRE_INVESTIGATE_AGENT"

	// EnvRunID is the 12-hex-char identifier of the parent investigation
	// run. Multiple sessions across rounds share this ID so the loop driver
	// can correlate them.
	EnvRunID = "ENTIRE_INVESTIGATE_RUN_ID"

	// EnvRound is the round number (1-indexed) within the investigation
	// run, encoded as a base-10 string.
	EnvRound = "ENTIRE_INVESTIGATE_ROUND"

	// EnvTurn is the overall turn index (1-indexed across rounds), encoded
	// as a base-10 string.
	EnvTurn = "ENTIRE_INVESTIGATE_TURN"

	// EnvTopic is the human-readable topic the investigation was asked to
	// investigate. The lifecycle hook stores this so the checkpoint records
	// what was being investigated.
	EnvTopic = "ENTIRE_INVESTIGATE_TOPIC"

	// EnvPrompt is the full prompt text sent to the agent for this turn.
	// The lifecycle hook stores this so the checkpoint records what the
	// agent was asked to do.
	EnvPrompt = "ENTIRE_INVESTIGATE_PROMPT"

	// EnvFindingsDoc is the absolute path to the shared findings document
	// the agent should append to.
	EnvFindingsDoc = "ENTIRE_INVESTIGATE_FINDINGS_DOC"

	// EnvTimelineDoc is the absolute path to the shared timeline document
	// the agent should append to.
	EnvTimelineDoc = "ENTIRE_INVESTIGATE_TIMELINE_DOC"

	// EnvStartingSHA is the git commit SHA that was HEAD when `entire
	// investigate` was invoked. The lifecycle hook requires this to match
	// the session's initial base_commit before tagging the session, so
	// stale env from an old HEAD does not mark a later normal session as
	// an investigation.
	EnvStartingSHA = "ENTIRE_INVESTIGATE_STARTING_SHA"
)

// reviewEnvNames lists the ENTIRE_REVIEW_* env-var names that must be
// stripped before spawning an investigate session. We can't import the review
// package (would create a coupling that the design forbids), so we keep a
// local copy of the prefix list and accept the small duplication. If review
// adds a new env var, this list must be updated to match — see the env-var
// contract in cmd/entire/cli/review/env.go.
var reviewEnvNames = []string{
	"ENTIRE_REVIEW_SESSION",
	"ENTIRE_REVIEW_AGENT",
	"ENTIRE_REVIEW_SKILLS",
	"ENTIRE_REVIEW_PROMPT",
	"ENTIRE_REVIEW_STARTING_SHA",
}

// AppendOptions carries the data needed to populate the ENTIRE_INVESTIGATE_*
// env vars on a spawned agent process.
type AppendOptions struct {
	AgentName   string
	RunID       string
	Round       int
	Turn        int
	Topic       string
	Prompt      string
	FindingsDoc string
	TimelineDoc string
	StartingSHA string
}

// AppendInvestigateEnv adds the ENTIRE_INVESTIGATE_* env vars to base,
// returning the new slice. Used by the loop driver when spawning each per-turn
// agent process to propagate the investigate-session contract.
//
// Any pre-existing ENTIRE_INVESTIGATE_* AND ENTIRE_REVIEW_* entries in base
// are stripped before the new values are appended. Stripping investigate
// entries handles nested invocations and stale inheritance from a parent
// shell — duplicate keys would otherwise have implementation-defined
// precedence. Stripping review entries prevents an outer `entire review`
// session from mis-tagging a child investigate session if invoked nested.
func AppendInvestigateEnv(base []string, opts AppendOptions) []string {
	out := make([]string, 0, len(base)+9)
	for _, kv := range base {
		if IsInvestigateEnvEntry(kv) || isReviewEnvEntry(kv) {
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		EnvSession+"=1",
		EnvAgent+"="+opts.AgentName,
		EnvRunID+"="+opts.RunID,
		EnvRound+"="+strconv.Itoa(opts.Round),
		EnvTurn+"="+strconv.Itoa(opts.Turn),
		EnvTopic+"="+opts.Topic,
		EnvPrompt+"="+opts.Prompt,
		EnvFindingsDoc+"="+opts.FindingsDoc,
		EnvTimelineDoc+"="+opts.TimelineDoc,
		EnvStartingSHA+"="+opts.StartingSHA,
	)
}

// IsInvestigateEnvEntry reports whether kv is a "KEY=VALUE" entry whose key
// is one of the ENTIRE_INVESTIGATE_* contract variables.
func IsInvestigateEnvEntry(kv string) bool {
	for _, prefix := range []string{
		EnvSession + "=",
		EnvAgent + "=",
		EnvRunID + "=",
		EnvRound + "=",
		EnvTurn + "=",
		EnvTopic + "=",
		EnvPrompt + "=",
		EnvFindingsDoc + "=",
		EnvTimelineDoc + "=",
		EnvStartingSHA + "=",
	} {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

// isReviewEnvEntry reports whether kv is a "KEY=VALUE" entry whose key is one
// of the ENTIRE_REVIEW_* contract variables. Local copy of the review
// package's helper; see reviewEnvNames for the rationale.
func isReviewEnvEntry(kv string) bool {
	for _, name := range reviewEnvNames {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}
