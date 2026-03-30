package strategy

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"
	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/testutil"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupRepoWithV2Ref creates a temp repo with one commit and a v2 /main ref.
// Returns the repo directory.
func setupRepoWithV2Ref(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "f.txt", "init")
	testutil.GitAdd(t, tmpDir, "f.txt")
	testutil.GitCommit(t, tmpDir, "init")

	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	// Create v2 /main ref with an empty tree
	emptyTree, err := checkpoint.BuildTreeFromEntries(repo, map[string]object.TreeEntry{})
	require.NoError(t, err)

	commitHash, err := checkpoint.CreateCommit(repo, emptyTree, plumbing.ZeroHash,
		"Init v2 main", "Test", "test@test.com")
	require.NoError(t, err)

	ref := plumbing.NewHashReference(plumbing.ReferenceName(paths.V2MainRefName), commitHash)
	require.NoError(t, repo.Storer.SetReference(ref))

	return tmpDir
}

// Not parallel: uses t.Chdir()
func TestPushRefIfNeeded_NoLocalRef_ReturnsNil(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "f.txt", "init")
	testutil.GitAdd(t, tmpDir, "f.txt")
	testutil.GitCommit(t, tmpDir, "init")
	t.Chdir(tmpDir)

	ctx := context.Background()
	err := pushRefIfNeeded(ctx, "origin", plumbing.ReferenceName(paths.V2MainRefName))
	assert.NoError(t, err)
}

// Not parallel: uses t.Chdir()
func TestPushRefIfNeeded_LocalBareRepo_PushesSuccessfully(t *testing.T) {
	ctx := context.Background()

	tmpDir := setupRepoWithV2Ref(t)
	t.Chdir(tmpDir)

	bareDir := t.TempDir()
	initCmd := exec.CommandContext(ctx, "git", "init", "--bare")
	initCmd.Dir = bareDir
	initCmd.Env = testutil.GitIsolatedEnv()
	require.NoError(t, initCmd.Run())

	err := pushRefIfNeeded(ctx, bareDir, plumbing.ReferenceName(paths.V2MainRefName))
	assert.NoError(t, err)

	// Verify ref exists in bare repo
	bareRepo, err := git.PlainOpen(bareDir)
	require.NoError(t, err)
	_, err = bareRepo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	assert.NoError(t, err, "v2 /main ref should exist in bare repo after push")
}

// Not parallel: uses t.Chdir()
func TestPushRefIfNeeded_UnreachableTarget_ReturnsNil(t *testing.T) {
	tmpDir := setupRepoWithV2Ref(t)
	t.Chdir(tmpDir)

	ctx := context.Background()
	nonExistentPath := filepath.Join(t.TempDir(), "does-not-exist")
	err := pushRefIfNeeded(ctx, nonExistentPath, plumbing.ReferenceName(paths.V2MainRefName))
	assert.NoError(t, err, "pushRefIfNeeded should return nil when target is unreachable")
}

func TestShortRefName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{"refs/entire/checkpoints/v2/main", "v2/main"},
		{"refs/entire/checkpoints/v2/full/current", "v2/full/current"},
		{"refs/entire/checkpoints/v2/full/0000000000001", "v2/full/0000000000001"},
		{"refs/heads/main", "refs/heads/main"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, shortRefName(plumbing.ReferenceName(tt.input)))
		})
	}
}

// writeV2Checkpoint writes a checkpoint to both /main and /full/current via V2GitStore.
func writeV2Checkpoint(t *testing.T, repo *git.Repository, cpID id.CheckpointID, sessionID string) {
	t.Helper()
	store := checkpoint.NewV2GitStore(repo)
	err := store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    sessionID,
		Strategy:     "manual-commit",
		Transcript:   []byte(`{"from":"` + sessionID + `"}`),
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)
}

// TestFetchAndMergeRef_MergesTrees verifies that fetchAndMergeRef correctly
// merges divergent trees from two repos sharing a common ref.
// Not parallel: uses t.Chdir()
func TestFetchAndMergeRef_MergesTrees(t *testing.T) {
	ctx := context.Background()
	refName := plumbing.ReferenceName(paths.V2MainRefName)

	// Create source repo with a v2 /main ref containing one checkpoint
	srcDir := setupRepoWithV2Ref(t)
	srcRepo, err := git.PlainOpen(srcDir)
	require.NoError(t, err)
	writeV2Checkpoint(t, srcRepo, id.MustCheckpointID("aabbccddeeff"), "session-src")

	// Create a bare "remote" and push src to it
	bareDir := t.TempDir()
	initCmd := exec.CommandContext(ctx, "git", "init", "--bare")
	initCmd.Dir = bareDir
	initCmd.Env = testutil.GitIsolatedEnv()
	require.NoError(t, initCmd.Run())

	pushCmd := exec.CommandContext(ctx, "git", "push", bareDir,
		string(refName)+":"+string(refName))
	pushCmd.Dir = srcDir
	require.NoError(t, pushCmd.Run())

	// Create a local repo that also has the ref but with a different checkpoint
	localDir := setupRepoWithV2Ref(t)
	localRepo, err := git.PlainOpen(localDir)
	require.NoError(t, err)
	writeV2Checkpoint(t, localRepo, id.MustCheckpointID("112233445566"), "session-local")

	t.Chdir(localDir)

	// Fetch and merge — should combine both checkpoints
	err = fetchAndMergeRef(ctx, bareDir, refName)
	require.NoError(t, err)

	// Verify merged tree contains both checkpoints on /main
	mergedRepo, err := git.PlainOpen(localDir)
	require.NoError(t, err)
	ref, err := mergedRepo.Reference(refName, true)
	require.NoError(t, err)
	commit, err := mergedRepo.CommitObject(ref.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)

	entries := make(map[string]object.TreeEntry)
	require.NoError(t, checkpoint.FlattenTree(mergedRepo, tree, "", entries))

	// Should have entries from both checkpoints (aa/ shard and 11/ shard)
	hasAA := false
	has11 := false
	for path := range entries {
		if strings.HasPrefix(path, "aa/") {
			hasAA = true
		}
		if strings.HasPrefix(path, "11/") {
			has11 = true
		}
	}
	assert.True(t, hasAA, "merged tree should contain checkpoint aabbccddeeff")
	assert.True(t, has11, "merged tree should contain checkpoint 112233445566")
}
