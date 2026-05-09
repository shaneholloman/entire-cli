package claudecode

import (
	"context"
	"os/exec"

	"github.com/entireio/cli/cmd/entire/cli/agent/spawn"
)

// claudeCodeSpawner produces argv: claude -p <prompt>; no stdin.
type claudeCodeSpawner struct{}

// NewSpawner returns a Spawner for claude-code's non-interactive review/investigate mode.
//

func NewSpawner() spawn.Spawner { return claudeCodeSpawner{} }

func (claudeCodeSpawner) Name() string { return "claude-code" }

func (claudeCodeSpawner) BuildCmd(ctx context.Context, env []string, prompt string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt)
	cmd.Env = env
	return cmd
}
