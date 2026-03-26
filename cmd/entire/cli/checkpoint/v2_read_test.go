package checkpoint

import (
	"context"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestV2ReadCommitted_ReturnsCheckpointSummary(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewV2GitStore(repo)
	cpID := id.MustCheckpointID("a1a2a3a4a5a6")
	ctx := context.Background()

	err := store.WriteCommitted(ctx, WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-1",
		Strategy:     "manual-commit",
		Transcript:   []byte(`{"test": true}`),
		Prompts:      []string{"hello"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	summary, err := store.ReadCommitted(ctx, cpID)
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.Equal(t, cpID, summary.CheckpointID)
	assert.Len(t, summary.Sessions, 1)
}

func TestV2ReadCommitted_ReturnsNilForMissing(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewV2GitStore(repo)
	cpID := id.MustCheckpointID("b1b2b3b4b5b6")
	ctx := context.Background()

	summary, err := store.ReadCommitted(ctx, cpID)
	assert.NoError(t, err)
	assert.Nil(t, summary)
}

func TestV2ReadSessionContent_ReturnsMetadataAndTranscript(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewV2GitStore(repo)
	cpID := id.MustCheckpointID("c1c2c3c4c5c6")
	ctx := context.Background()

	err := store.WriteCommitted(ctx, WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-1",
		Strategy:     "manual-commit",
		Transcript:   []byte(`{"message": "hello world"}`),
		Prompts:      []string{"test prompt"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	content, err := store.ReadSessionContent(ctx, cpID, 0)
	require.NoError(t, err)
	require.NotNil(t, content)
	assert.Equal(t, "session-1", content.Metadata.SessionID)
	assert.NotEmpty(t, content.Transcript)
	assert.Contains(t, content.Prompts, "test prompt")
}

func TestV2ReadSessionContent_TranscriptFromArchivedGeneration(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewV2GitStore(repo)
	store.maxCheckpointsPerGeneration = 1
	ctx := context.Background()

	cpID1 := id.MustCheckpointID("d1d2d3d4d5d6")
	err := store.WriteCommitted(ctx, WriteCommittedOptions{
		CheckpointID: cpID1,
		SessionID:    "session-1",
		Strategy:     "manual-commit",
		Transcript:   []byte(`{"first": true}`),
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	cpID2 := id.MustCheckpointID("e1e2e3e4e5e6")
	err = store.WriteCommitted(ctx, WriteCommittedOptions{
		CheckpointID: cpID2,
		SessionID:    "session-2",
		Strategy:     "manual-commit",
		Transcript:   []byte(`{"second": true}`),
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	content, err := store.ReadSessionContent(ctx, cpID1, 0)
	require.NoError(t, err)
	require.NotNil(t, content)
	assert.NotEmpty(t, content.Transcript, "transcript should be found in archived generation")
}

func TestV2ReadSessionContent_MissingTranscript_ReturnsEmptyTranscript(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewV2GitStore(repo)
	cpID := id.MustCheckpointID("f1f2f3f4f5f6")
	ctx := context.Background()

	err := store.WriteCommitted(ctx, WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-1",
		Strategy:     "manual-commit",
		Prompts:      []string{"prompt"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	content, err := store.ReadSessionContent(ctx, cpID, 0)
	require.NoError(t, err)
	require.NotNil(t, content)
	assert.Equal(t, "session-1", content.Metadata.SessionID)
	assert.Empty(t, content.Transcript, "transcript should be empty when not written")
}
