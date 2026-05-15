package codex

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/entireio/cli/cmd/entire/cli/agent"
	"github.com/entireio/cli/cmd/entire/cli/agent/spawn"
)

// envFindingsDoc mirrors investigate.EnvFindingsDoc. Duplicated as a
// constant here so this package doesn't depend on the investigate
// package (which would create an import cycle: investigate already
// imports codex transitively via the bridge).
const envFindingsDoc = "ENTIRE_INVESTIGATE_FINDINGS_DOC"

// codexSpawner produces argv:
//
//	codex exec --skip-git-repo-check -s workspace-write [--add-dir <run-dir>] -
//
// Prompt is piped on stdin. --sandbox workspace-write is needed so the
// agent can edit files in the working tree (review reads only, so the
// flag is a no-op there — safe to apply universally). --add-dir is
// added when running under `entire investigate`, pointing at the
// per-run dir under <git-common-dir>/entire-investigations/<run-id>/
// so the agent can write findings.md + state.json even though .git/
// often falls outside the strict workspace boundary.
type codexSpawner struct{}

// NewSpawner returns a Spawner for codex's non-interactive review/investigate mode.
func NewSpawner() spawn.Spawner { return codexSpawner{} }

func (codexSpawner) Name() string { return string(agent.AgentNameCodex) }

func (codexSpawner) BuildCmd(ctx context.Context, env []string, prompt string) *exec.Cmd {
	args := []string{codexExecCommand, "--skip-git-repo-check", "-s", "workspace-write"}
	if runDir := investigateRunDirFromEnv(env); runDir != "" {
		args = append(args, "--add-dir", runDir)
	}
	args = append(args, "-")
	cmd := exec.CommandContext(ctx, string(agent.AgentNameCodex), args...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = env
	return cmd
}

// investigateRunDirFromEnv returns the per-run dir derived from the
// ENTIRE_INVESTIGATE_FINDINGS_DOC env var, or "" when the var isn't
// set (review mode, or any other caller that doesn't ship the var).
// The findings path is <run-dir>/findings.md; the run dir is its parent.
func investigateRunDirFromEnv(env []string) string {
	prefix := envFindingsDoc + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			path := strings.TrimPrefix(kv, prefix)
			if path == "" {
				return ""
			}
			return filepath.Dir(path)
		}
	}
	return ""
}
