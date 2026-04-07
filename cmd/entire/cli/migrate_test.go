package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"
	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initMigrateTestRepo creates a repo with an initial commit.
func initMigrateTestRepo(t *testing.T) *git.Repository {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("init"), 0o644))
	_, err = wt.Add("README.md")
	require.NoError(t, err)
	_, err = wt.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	require.NoError(t, err)
	return repo
}

// writeV1Checkpoint writes a checkpoint to the v1 branch for testing.
func writeV1Checkpoint(t *testing.T, store *checkpoint.GitStore, cpID id.CheckpointID, sessionID string, transcript []byte, prompts []string) {
	t.Helper()
	err := store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    sessionID,
		Strategy:     "manual-commit",
		Transcript:   transcript,
		Prompts:      prompts,
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)
}

func newMigrateStores(repo *git.Repository) (*checkpoint.GitStore, *checkpoint.V2GitStore) {
	return checkpoint.NewGitStore(repo), checkpoint.NewV2GitStore(repo, migrateRemoteName)
}

func TestMigrateCheckpointsV2_Basic(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("a1b2c3d4e5f6")
	writeV1Checkpoint(t, v1Store, cpID, "session-001",
		[]byte("{\"type\":\"assistant\",\"message\":\"hello\"}\n"),
		[]string{"test prompt"},
	)

	var stdout bytes.Buffer

	result, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout)
	require.NoError(t, err)
	assert.Equal(t, 1, result.migrated)
	assert.Equal(t, 0, result.skipped)
	assert.Equal(t, 0, result.failed)

	// Verify checkpoint exists in v2
	summary, err := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, err)
	require.NotNil(t, summary, "checkpoint should exist in v2 after migration")
	assert.Equal(t, cpID, summary.CheckpointID)
}

func TestMigrateCheckpointsV2_Idempotent(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("c3d4e5f6a1b2")
	writeV1Checkpoint(t, v1Store, cpID, "session-idem",
		[]byte("{\"type\":\"assistant\",\"message\":\"idempotent test\"}\n"),
		[]string{"idem prompt"},
	)

	var stdout bytes.Buffer

	// First run: should migrate
	result1, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.migrated)
	assert.Equal(t, 0, result1.skipped)

	// Second run: should skip (no agent type means backfill also can't produce compact transcript)
	stdout.Reset()
	result2, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout)
	require.NoError(t, err)
	assert.Equal(t, 0, result2.migrated)
	assert.Equal(t, 1, result2.skipped)
}

func TestMigrateCheckpointsV2_MultiSession(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("d4e5f6a1b2c3")

	// Write first session
	writeV1Checkpoint(t, v1Store, cpID, "session-multi-1",
		[]byte("{\"type\":\"assistant\",\"message\":\"session 1\"}\n"),
		[]string{"prompt 1"},
	)

	// Write second session to same checkpoint
	writeV1Checkpoint(t, v1Store, cpID, "session-multi-2",
		[]byte("{\"type\":\"assistant\",\"message\":\"session 2\"}\n"),
		[]string{"prompt 2"},
	)

	var stdout bytes.Buffer

	result, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout)
	require.NoError(t, err)
	assert.Equal(t, 1, result.migrated)

	// Verify both sessions are in v2
	summary, readErr := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, readErr)
	require.NotNil(t, summary)
	assert.GreaterOrEqual(t, len(summary.Sessions), 2, "should have at least 2 sessions")
}

func TestMigrateCheckpointsV2_NoV1Branch(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)
	var stdout bytes.Buffer

	// No v1 data written — ListCommitted returns empty
	result, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout)
	require.NoError(t, err)
	assert.Equal(t, 0, result.migrated)
	assert.Contains(t, stdout.String(), "Nothing to migrate")
}

func TestMigrateCmd_InvalidFlag(t *testing.T) {
	t.Parallel()
	cmd := newMigrateCmd()
	cmd.SetArgs([]string{"--checkpoints", "v3"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported checkpoints version")
}

func TestMigrateCheckpointsV2_CompactionSkipped(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("e5f6a1b2c3d4")
	// Write checkpoint with no agent type — compaction will be skipped
	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-noagent",
		Strategy:     "manual-commit",
		Transcript:   []byte("{\"type\":\"assistant\",\"message\":\"no agent\"}\n"),
		Prompts:      []string{"compact fail prompt"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	var stdout bytes.Buffer

	result, migrateErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout)
	require.NoError(t, migrateErr)
	assert.Equal(t, 1, result.migrated)
	assert.Contains(t, stdout.String(), "compact transcript not generated")
}

func TestMigrateCheckpointsV2_TaskCheckpoint(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("b2c3d4e5f6a1")
	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-task-001",
		Strategy:     "manual-commit",
		Transcript:   []byte("{\"type\":\"assistant\",\"message\":\"task work\"}\n"),
		Prompts:      []string{"task prompt"},
		IsTask:       true,
		ToolUseID:    "toolu_01ABC",
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	var stdout bytes.Buffer

	result, migrateErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout)
	require.NoError(t, migrateErr)
	assert.Equal(t, 1, result.migrated)

	// Verify task checkpoint exists in v2
	summary, readErr := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, readErr)
	require.NotNil(t, summary)

	// Verify task metadata tree was copied into v2 /full/current.
	_, rootTreeHash, refErr := v2Store.GetRefState(plumbing.ReferenceName(paths.V2FullCurrentRefName))
	require.NoError(t, refErr)
	rootTree, treeErr := repo.TreeObject(rootTreeHash)
	require.NoError(t, treeErr)
	_, taskFileErr := rootTree.File(cpID.Path() + "/0/tasks/toolu_01ABC/checkpoint.json")
	require.NoError(t, taskFileErr, "expected migrated task checkpoint metadata in /full/current")
}

func TestMigrateCheckpointsV2_AllSkippedOnRerun(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID1 := id.MustCheckpointID("f6a1b2c3d4e5")
	cpID2 := id.MustCheckpointID("a1b2c3d4e5f7")

	writeV1Checkpoint(t, v1Store, cpID1, "session-p1",
		[]byte("{\"type\":\"assistant\",\"message\":\"first\"}\n"),
		[]string{"prompt 1"},
	)
	writeV1Checkpoint(t, v1Store, cpID2, "session-p2",
		[]byte("{\"type\":\"assistant\",\"message\":\"second\"}\n"),
		[]string{"prompt 2"},
	)

	// First run: migrates both
	var discard bytes.Buffer
	result1, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &discard)
	require.NoError(t, err)
	assert.Equal(t, 2, result1.migrated)

	// Second run: skips both
	var stdout bytes.Buffer
	result2, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout)
	require.NoError(t, err)
	assert.Equal(t, 0, result2.migrated)
	assert.Equal(t, 2, result2.skipped)
}

func TestMigrateCheckpointsV2_BackfillCompactTranscript(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("aabb11223344")

	// Write v1 checkpoint with agent type (so compaction can succeed)
	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-backfill",
		Strategy:     "manual-commit",
		Transcript:   []byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}\n"),
		Prompts:      []string{"hello"},
		Agent:        "Claude Code",
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	// Write to v2 WITHOUT compact transcript (simulating earlier migration)
	err = v2Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-backfill",
		Strategy:     "manual-commit",
		Transcript:   []byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n"),
		Prompts:      []string{"hello"},
		Agent:        "Claude Code",
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
		// CompactTranscript intentionally nil
	})
	require.NoError(t, err)

	// Verify no transcript.jsonl on /main yet
	summary, err := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.Empty(t, summary.Sessions[0].Transcript, "should have no compact transcript before backfill")

	// Run migration — should backfill the compact transcript
	var stdout bytes.Buffer
	result, migrateErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout)
	require.NoError(t, migrateErr)
	assert.Equal(t, 1, result.migrated, "backfill should count as migrated")
	assert.Equal(t, 0, result.skipped)
	assert.Contains(t, stdout.String(), "added transcript.jsonl")

	// Verify transcript.jsonl now exists
	summary2, err := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, err)
	require.NotNil(t, summary2)
	assert.NotEmpty(t, summary2.Sessions[0].Transcript, "should have compact transcript after backfill")
}

func TestBuildMigrateWriteOpts_PromptSeparatorRoundTrip(t *testing.T) {
	t.Parallel()

	cpID := id.MustCheckpointID("123456abcdef")
	rawPrompts := strings.Join([]string{
		"first line\nwith newline",
		"second prompt",
	}, checkpoint.PromptSeparator)

	opts := buildMigrateWriteOpts(&checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			SessionID: "session-prompts-001",
			Strategy:  "manual-commit",
		},
		Prompts: rawPrompts,
	}, checkpoint.CommittedInfo{
		CheckpointID: cpID,
	})

	require.Len(t, opts.Prompts, 2)
	assert.Equal(t, "first line\nwith newline", opts.Prompts[0])
	assert.Equal(t, "second prompt", opts.Prompts[1])
}
