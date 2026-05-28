//go:build unix

package strategy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/paths"

	"github.com/stretchr/testify/require"
)

// TestDoPushBranch_SharedBudget_BoundsTotalWallClock proves the whole checkpoint
// push shares one deadline: a per-attempt timeout regression would let the initial
// push, fetch+rebase, and retry each spend a full timeout (~2x the bound).
//
// A hanging GIT_SSH_COMMAND makes `git push` to an ssh:// URL block until the budget
// SIGKILLs the process group; the script sleeps past the asserted bound so a
// regression blows past it, and self-exits as a backstop.
//
// Not parallel: uses t.Setenv and overrides checkpointPushBudget.
func TestDoPushBranch_SharedBudget_BoundsTotalWallClock(t *testing.T) {
	// Shrink the production 60s budget so the test runs in seconds.
	const budget = 2 * time.Second
	restoreBudget := checkpointPushBudget
	checkpointPushBudget = budget
	t.Cleanup(func() { checkpointPushBudget = restoreBudget })

	// Hangs past the asserted bound; git runs it for the ssh:// URL via GIT_SSH_COMMAND.
	hangScript := filepath.Join(t.TempDir(), "hang.sh")
	require.NoError(t, os.WriteFile(hangScript, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755))
	t.Setenv("GIT_SSH_COMMAND", hangScript)
	// With a token set, newCommand rewrites ssh:// to https:// and the hang never runs.
	t.Setenv("ENTIRE_CHECKPOINT_TOKEN", "")

	tmpDir := setupRepoWithCheckpointBranch(t)
	t.Chdir(tmpDir)

	// Suppress the progress dots / warnings doPushBranch writes to os.Stderr.
	restoreStderr := captureStderr(t)
	defer restoreStderr()

	// ssh:// URL so git invokes GIT_SSH_COMMAND (our hang) for the transport.
	const target = "ssh://git@localhost/checkpoints.git"

	start := time.Now()
	err := doPushBranch(context.Background(), target, paths.MetadataBranchName)
	elapsed := time.Since(start)

	require.NoError(t, err, "doPushBranch degrades gracefully on a stuck transport")

	// Upper bound: one shared budget. A per-attempt regression would land at ~2x; the
	// hang script outlives this bound, so any failure to enforce the budget exceeds it.
	require.Less(t, elapsed, 5*time.Second,
		"doPushBranch should return at ~budget, not stack multiple full timeouts; took %s", elapsed)
	// Lower bound: confirm the push actually hung and was cut off by the budget, not
	// failing instantly (which would make the upper bound meaningless).
	require.GreaterOrEqual(t, elapsed, budget/2,
		"push should have run until the budget deadline; took %s", elapsed)
}
