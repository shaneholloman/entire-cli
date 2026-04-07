package checkpoint

import (
	"context"
	"errors"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
)

// CommittedReader provides read access to committed checkpoint data.
// Both GitStore (v1) and V2GitStore (v2) implement this interface.
type CommittedReader interface {
	ReadCommitted(ctx context.Context, checkpointID id.CheckpointID) (*CheckpointSummary, error)
	ReadSessionContent(ctx context.Context, checkpointID id.CheckpointID, sessionIndex int) (*SessionContent, error)
}

// ResolveCommittedReaderForCheckpoint resolves which committed checkpoint reader
// should be used for a specific checkpoint ID.
//
// Fallback behavior mirrors resume/rewind patterns:
//   - Try v2 first when preferV2 is true
//   - Fall back to v1 when checkpoint is not found in v2
//   - Fall back to v1 when v2 returns ErrCheckpointNotFound/ErrNoTranscript
func ResolveCommittedReaderForCheckpoint(
	ctx context.Context,
	checkpointID id.CheckpointID,
	v1Store *GitStore,
	v2Store *V2GitStore,
	preferV2 bool,
) (CommittedReader, *CheckpointSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err //nolint:wrapcheck // Propagating context cancellation
	}

	if preferV2 && v2Store != nil {
		summary, err := v2Store.ReadCommitted(ctx, checkpointID)
		if err == nil && summary != nil {
			return v2Store, summary, nil
		}
		if err != nil && !errors.Is(err, ErrCheckpointNotFound) && !errors.Is(err, ErrNoTranscript) {
			return nil, nil, err
		}
	}

	if v1Store == nil {
		return nil, nil, ErrCheckpointNotFound
	}

	summary, err := v1Store.ReadCommitted(ctx, checkpointID)
	if err != nil {
		return nil, nil, err
	}
	if summary == nil {
		return nil, nil, ErrCheckpointNotFound
	}

	return v1Store, summary, nil
}

// ResolveRawSessionLogForCheckpoint resolves the raw transcript log bytes for a
// checkpoint with v2-first, v1-fallback behavior.
//
// Fallback behavior mirrors resume/rewind patterns:
//   - Try v2 first when preferV2 is true
//   - Fall back to v1 when checkpoint/transcript is missing in v2
func ResolveRawSessionLogForCheckpoint(
	ctx context.Context,
	checkpointID id.CheckpointID,
	v1Store *GitStore,
	v2Store *V2GitStore,
	preferV2 bool,
) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err //nolint:wrapcheck // Propagating context cancellation
	}

	if preferV2 && v2Store != nil {
		content, sessionID, err := v2Store.GetSessionLog(ctx, checkpointID)
		if err == nil && len(content) > 0 {
			return content, sessionID, nil
		}
		if err != nil && !errors.Is(err, ErrCheckpointNotFound) && !errors.Is(err, ErrNoTranscript) {
			return nil, "", err
		}
	}

	if v1Store == nil {
		return nil, "", ErrCheckpointNotFound
	}

	return v1Store.GetSessionLog(ctx, checkpointID)
}
