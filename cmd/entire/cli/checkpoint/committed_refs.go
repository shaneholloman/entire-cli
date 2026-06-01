package checkpoint

import (
	"context"

	"github.com/go-git/go-git/v6/plumbing"

	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/settings"
)

// CommittedRefs is the committed-metadata ref topology for the active
// checkpoints_version. It is resolved once from settings (ResolveCommittedRefs)
// and consulted by every read/write/mirror site instead of each one
// re-deriving the topology from settings.MirrorsToV1CustomRef plus the paths
// constants.
//
// Today Primary is always the v1 branch (the durable source of truth that is
// written and pushed). When checkpoints_version "1.1" is opted in, Read and
// Mirror are the local-only v1.1 custom ref; otherwise Read is the v1 branch
// and there is no mirror. A future rollout phase flips the topology (v1.1 as
// Primary) by changing ResolveCommittedRefs alone.
type CommittedRefs struct {
	// Primary is the source of truth: written first and pushed/fetched.
	Primary plumbing.ReferenceName
	// Read is the ref committed-checkpoint reads resolve against.
	Read plumbing.ReferenceName
	// Mirror is advanced to Primary's tip after each primary write and is kept
	// current before reads. Empty when there is no mirror. Local-only: never
	// pushed.
	Mirror plumbing.ReferenceName
}

// HasMirror reports whether a mirror ref is configured.
func (r CommittedRefs) HasMirror() bool { return r.Mirror != "" }

// ResolveCommittedRefs returns the committed-ref topology for the current
// settings loaded from disk. Falls back to the v1-branch-only topology when
// settings cannot be loaded (degrades safely to legacy v1).
//
// Invariant: Read == Mirror when a mirror exists, else Read == Primary.
func ResolveCommittedRefs(ctx context.Context) CommittedRefs {
	return committedRefsFor(settings.MirrorsToV1CustomRef(ctx))
}

// ResolveCommittedRefsFromSettings returns the committed-ref topology for an
// already-loaded settings object. Use when the mirror decision must honor an
// injected EntireSettings rather than disk (e.g. attach). A nil settings object
// resolves to the v1-branch-only topology.
func ResolveCommittedRefsFromSettings(s *settings.EntireSettings) CommittedRefs {
	return committedRefsFor(s != nil && s.MirrorsToV1CustomRef())
}

// committedRefsFor builds the topology given whether the mirror is enabled.
func committedRefsFor(mirrorEnabled bool) CommittedRefs {
	v1Branch := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	refs := CommittedRefs{Primary: v1Branch, Read: v1Branch}
	if mirrorEnabled {
		custom := plumbing.ReferenceName(paths.MetadataRefName)
		refs.Read = custom
		refs.Mirror = custom
	}
	return refs
}
