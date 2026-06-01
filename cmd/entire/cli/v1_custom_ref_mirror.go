package cli

import (
	"context"
	"errors"
	"fmt"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"

	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/settings"
	"github.com/entireio/cli/cmd/entire/cli/strategy"
)

// mirrorToV1CustomRef advances refs/entire/checkpoints/v1.1 to the v1 metadata
// branch tip when opted in, returning errors so callers can surface them.
// The hook-side equivalent (strategy.mirrorMetadataToV1CustomRef) logs instead.
func mirrorToV1CustomRef(ctx context.Context, repo *git.Repository) error {
	if !settings.MirrorsToV1CustomRef(ctx) {
		return nil
	}
	v1Ref, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return fmt.Errorf("v1 metadata branch %s missing after v1 write", paths.MetadataBranchName)
		}
		return fmt.Errorf("read v1 metadata branch %s: %w", paths.MetadataBranchName, err)
	}
	if err := strategy.SafelyAdvanceLocalRef(ctx, repo, plumbing.ReferenceName(paths.MetadataRefName), v1Ref.Hash()); err != nil {
		return fmt.Errorf("advance v1 custom ref %s: %w", paths.MetadataRefName, err)
	}
	return nil
}
