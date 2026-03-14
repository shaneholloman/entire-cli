package strategy

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/testutil"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasUnpushedSessionsCommon(t *testing.T) {
	t.Parallel()

	branchName := "entire/checkpoints/v1"

	setupRepo := func(t *testing.T) (*git.Repository, plumbing.Hash) {
		t.Helper()
		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")

		repo, err := git.PlainOpen(tmpDir)
		require.NoError(t, err)

		head, err := repo.Head()
		require.NoError(t, err)

		localRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), head.Hash())
		require.NoError(t, repo.Storer.SetReference(localRef))

		return repo, head.Hash()
	}

	t.Run("no remote tracking ref exists", func(t *testing.T) {
		t.Parallel()
		repo, headHash := setupRepo(t)
		assert.True(t, hasUnpushedSessionsCommon(repo, "origin", headHash, branchName))
	})

	t.Run("local and remote same hash", func(t *testing.T) {
		t.Parallel()
		repo, headHash := setupRepo(t)

		remoteRef := plumbing.NewHashReference(
			plumbing.NewRemoteReferenceName("origin", branchName),
			headHash,
		)
		require.NoError(t, repo.Storer.SetReference(remoteRef))

		assert.False(t, hasUnpushedSessionsCommon(repo, "origin", headHash, branchName))
	})

	t.Run("local differs from remote", func(t *testing.T) {
		t.Parallel()
		repo, _ := setupRepo(t)

		differentHash := plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		assert.True(t, hasUnpushedSessionsCommon(repo, "origin", differentHash, branchName))
	})
}

// setupRepoWithCheckpointBranch creates a temp repo with one commit and a local
// entire/checkpoints/v1 branch pointing at HEAD. Returns the repo directory.
// Caller must call t.Chdir(tmpDir) if needed (not done here to keep the helper composable).
func setupRepoWithCheckpointBranch(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "f.txt", "init")
	testutil.GitAdd(t, tmpDir, "f.txt")
	testutil.GitCommit(t, tmpDir, "init")

	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	head, err := repo.Head()
	require.NoError(t, err)

	localRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), head.Hash())
	require.NoError(t, repo.Storer.SetReference(localRef))

	return tmpDir
}

// TestDoPushBranch_UnreachableTarget_ReturnsNil exercises the graceful degradation
// path in doPushBranch: when the push target is unreachable, the function logs a
// warning and returns nil (no error). This is the core behavior that ensures a
// failing checkpoint remote never blocks the user's main push.
//
// Not parallel: uses t.Chdir() (required for OpenRepository in fetchAndMergeSessionsCommon).
func TestDoPushBranch_UnreachableTarget_ReturnsNil(t *testing.T) {
	tmpDir := setupRepoWithCheckpointBranch(t)
	t.Chdir(tmpDir)

	ctx := context.Background()

	// Use a non-existent path as the push target. doPushBranch will:
	// 1. Try to push (fails — target doesn't exist)
	// 2. Try to fetch+merge (fails — can't fetch from non-existent path)
	// 3. Log warning and return nil (graceful degradation)
	nonExistentPath := filepath.Join(t.TempDir(), "does-not-exist")
	err := doPushBranch(ctx, nonExistentPath, paths.MetadataBranchName)
	assert.NoError(t, err, "doPushBranch should return nil when target is unreachable (graceful degradation)")
}

// TestPushBranchIfNeeded_UnreachableTarget_ReturnsNil exercises the full push path
// through pushBranchIfNeeded with an unreachable local path target. This verifies
// that the complete production code path (branch existence check -> push attempt ->
// graceful failure) works end-to-end.
//
// Not parallel: uses t.Chdir() (required for OpenRepository).
func TestPushBranchIfNeeded_UnreachableTarget_ReturnsNil(t *testing.T) {
	tmpDir := setupRepoWithCheckpointBranch(t)
	t.Chdir(tmpDir)

	ctx := context.Background()

	// Push to a non-existent path. pushBranchIfNeeded will:
	// 1. Open repository (CWD-based)
	// 2. Verify branch exists locally
	// 3. Since target is not a URL (no :// or @), check hasUnpushedSessionsCommon
	//    which finds no remote tracking ref -> returns true (has unpushed)
	// 4. Call doPushBranch which fails gracefully
	nonExistentPath := filepath.Join(t.TempDir(), "does-not-exist")
	err := pushBranchIfNeeded(ctx, nonExistentPath, paths.MetadataBranchName)
	assert.NoError(t, err, "pushBranchIfNeeded should return nil when target is unreachable")
}

// TestPushBranchIfNeeded_LocalBareRepo_PushesSuccessfully verifies that
// pushBranchIfNeeded works with a local bare repo path as the target.
// This exercises the same code path that PrePush uses when pushTarget()
// returns a URL, but with a local path. It validates the core routing
// behavior: a branch can be pushed to an arbitrary target path.
//
// Not parallel: uses t.Chdir() (required for OpenRepository).
func TestPushBranchIfNeeded_LocalBareRepo_PushesSuccessfully(t *testing.T) {
	ctx := context.Background()

	tmpDir := setupRepoWithCheckpointBranch(t)

	// Create a bare repo as the push target.
	bareDir := t.TempDir()
	initCmd := exec.CommandContext(ctx, "git", "init", "--bare")
	initCmd.Dir = bareDir
	initCmd.Env = testutil.GitIsolatedEnv()
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare failed: %v\n%s", err, output)
	}

	t.Chdir(tmpDir)

	// Push using pushBranchIfNeeded with the bare repo path as target.
	err := pushBranchIfNeeded(ctx, bareDir, paths.MetadataBranchName)
	require.NoError(t, err, "pushBranchIfNeeded should succeed with a local bare repo target")

	// Verify the branch arrived on the bare repo.
	verifyCmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+paths.MetadataBranchName)
	verifyCmd.Dir = bareDir
	verifyCmd.Env = testutil.GitIsolatedEnv()
	if output, err := verifyCmd.CombinedOutput(); err != nil {
		t.Errorf("branch should exist on bare remote after push: %v\n%s", err, output)
	}
}
