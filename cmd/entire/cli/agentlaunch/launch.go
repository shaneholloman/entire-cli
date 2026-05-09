// Package agentlaunch is the shared "launch a normal coding agent session
// with a composed prompt" helper, used by `entire review --fix` and
// `entire investigate fix`. Both commands feed accepted findings back into
// a follow-up coding agent without spawning a review/investigate session
// themselves.
//
// The package is a leaf — review and investigate both depend on it, so it
// cannot import them back. The provenance env-var prefix lists below
// duplicate the names declared in review/env.go and investigate/env.go;
// when those files add a new ENTIRE_REVIEW_* or ENTIRE_INVESTIGATE_* var,
// the corresponding list here must be updated to match.
package agentlaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/entireio/cli/cmd/entire/cli/agent"
	agenttypes "github.com/entireio/cli/cmd/entire/cli/agent/types"
)

// reviewEnvPrefixes lists the ENTIRE_REVIEW_* env-var names that must be
// stripped before launching a fix agent. Mirror of the constants in
// cmd/entire/cli/review/env.go; kept local to avoid an import cycle
// (review depends on agentlaunch).
var reviewEnvPrefixes = []string{
	"ENTIRE_REVIEW_SESSION=",
	"ENTIRE_REVIEW_AGENT=",
	"ENTIRE_REVIEW_SKILLS=",
	"ENTIRE_REVIEW_PROMPT=",
	"ENTIRE_REVIEW_STARTING_SHA=",
}

// investigateEnvPrefixes lists the ENTIRE_INVESTIGATE_* env-var names that
// must be stripped before launching a fix agent. Mirror of the constants
// in cmd/entire/cli/investigate/env.go; kept local for the same reason as
// reviewEnvPrefixes.
var investigateEnvPrefixes = []string{
	"ENTIRE_INVESTIGATE_SESSION=",
	"ENTIRE_INVESTIGATE_AGENT=",
	"ENTIRE_INVESTIGATE_RUN_ID=",
	"ENTIRE_INVESTIGATE_ROUND=",
	"ENTIRE_INVESTIGATE_TURN=",
	"ENTIRE_INVESTIGATE_TOPIC=",
	"ENTIRE_INVESTIGATE_PROMPT=",
	"ENTIRE_INVESTIGATE_FINDINGS_DOC=",
	"ENTIRE_INVESTIGATE_TIMELINE_DOC=",
	"ENTIRE_INVESTIGATE_STARTING_SHA=",
}

// LaunchFixAgent starts a normal coding agent session with the given
// prompt. ENTIRE_REVIEW_* and ENTIRE_INVESTIGATE_* env entries are stripped
// from the child process so the fix session is not tagged as a review or
// investigate.
//
// agentName must be a launchable agent registry name. Returns nil on clean
// exit, or a wrapped error on cancellation / non-zero exit. Output / input
// are connected to the calling process's stdio so the user can interact
// with the fix session in their terminal.
func LaunchFixAgent(ctx context.Context, agentName string, prompt string) error {
	ag, err := agent.Get(agenttypes.AgentName(agentName))
	if err != nil {
		return fmt.Errorf("resolve fix agent %s: %w", agentName, err)
	}
	launcher, ok := agent.LauncherFor(ag.Name())
	if !ok {
		return fmt.Errorf("agent %s cannot be launched for fix sessions", agentName)
	}
	cmd, err := launcher.LaunchCmd(ctx, prompt)
	if err != nil {
		return fmt.Errorf("build fix command: %w", err)
	}
	cmd.Env = withoutReviewOrInvestigateEnv(cmd.Env)
	if len(cmd.Env) == 0 {
		cmd.Env = withoutReviewOrInvestigateEnv(os.Environ())
	}
	if err := cmd.Run(); err != nil {
		if errors.Is(err, context.Canceled) {
			return fmt.Errorf("fix agent cancelled: %w", err)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("fix agent exited with status %d: %w", exitErr.ExitCode(), err)
		}
		return fmt.Errorf("run fix agent: %w", err)
	}
	return nil
}

// withoutReviewOrInvestigateEnv returns a copy of base with all
// ENTIRE_REVIEW_* and ENTIRE_INVESTIGATE_* entries removed. The returned
// slice is fresh — base is never mutated.
func withoutReviewOrInvestigateEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		if isProvenanceEnvEntry(kv) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// isProvenanceEnvEntry reports whether kv is one of the ENTIRE_REVIEW_* or
// ENTIRE_INVESTIGATE_* contract entries.
func isProvenanceEnvEntry(kv string) bool {
	for _, p := range reviewEnvPrefixes {
		if strings.HasPrefix(kv, p) {
			return true
		}
	}
	for _, p := range investigateEnvPrefixes {
		if strings.HasPrefix(kv, p) {
			return true
		}
	}
	return false
}
