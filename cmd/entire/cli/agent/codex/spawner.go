package codex

import (
	"context"
	"os/exec"
	"strings"

	"github.com/entireio/cli/cmd/entire/cli/agent"
	"github.com/entireio/cli/cmd/entire/cli/agent/spawn"
)

// codexSpawner produces argv: codex exec --skip-git-repo-check -; prompt via stdin.
type codexSpawner struct{}

// NewSpawner returns a Spawner for codex's non-interactive review/investigate mode.
//

func NewSpawner() spawn.Spawner { return codexSpawner{} }

func (codexSpawner) Name() string { return string(agent.AgentNameCodex) }

func (codexSpawner) BuildCmd(ctx context.Context, env []string, prompt string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, string(agent.AgentNameCodex), codexExecCommand, "--skip-git-repo-check", "-")
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = env
	return cmd
}
