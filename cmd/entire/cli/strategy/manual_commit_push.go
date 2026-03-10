package strategy

import (
	"context"

	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/perf"
)

// PrePush is called by the git pre-push hook before pushing to a remote.
// It pushes the entire/checkpoints/v1 branch alongside the user's push.
//
// If a checkpoint_remote URL is configured in settings, checkpoint and trails
// branches are pushed to that remote instead of the default push remote.
//
// Configuration options (stored in .entire/settings.json under strategy_options):
//   - push_sessions: false to disable automatic pushing
//   - checkpoint_remote: URL to push checkpoint branches to a separate remote
func (s *ManualCommitStrategy) PrePush(ctx context.Context, remote string) error {
	// Load settings once for both remote resolution and push_sessions check
	ps := resolvePushSettings(ctx, remote)

	if ps.pushDisabled {
		return nil
	}

	_, pushCheckpointsSpan := perf.Start(ctx, "push_checkpoints_branch")
	if err := pushBranchIfNeeded(ctx, ps.remote, paths.MetadataBranchName); err != nil {
		pushCheckpointsSpan.RecordError(err)
		pushCheckpointsSpan.End()
		return err
	}
	pushCheckpointsSpan.End()

	_, pushTrailsSpan := perf.Start(ctx, "push_trails_branch")
	err := pushBranchIfNeeded(ctx, ps.remote, paths.TrailsBranchName)
	pushTrailsSpan.RecordError(err)
	pushTrailsSpan.End()
	return err
}
