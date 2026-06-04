package checkpoint

import (
	"context"
	"slices"

	"github.com/go-git/go-git/v6/plumbing"

	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/settings"
)

// CommittedRefs is the committed-metadata ref topology for the active
// checkpoints_version. Resolve it once and consult it instead of re-deriving
// the topology at each read/write/mirror/push/fetch site.
type CommittedRefs struct {
	Primary plumbing.ReferenceName   // source of truth: written and pushed
	Read    plumbing.ReferenceName   // committed reads resolve against this
	Mirror  plumbing.ReferenceName   // advanced after Primary writes/fetches; "" disables
	Push    []plumbing.ReferenceName // refs PrePush advances on the user's remote
}

// HasMirror reports whether a mirror ref is configured.
func (r CommittedRefs) HasMirror() bool { return r.Mirror != "" }

// PrimaryFetchableFromOrigin reports whether Primary has an origin-tracking
// shadow — i.e. whether bootstrap-from-origin paths can fetch it. True when
// Primary appears in Push: we push it, so origin tracks it.
func (r CommittedRefs) PrimaryFetchableFromOrigin() bool {
	return slices.Contains(r.Push, r.Primary)
}

// ResolveCommittedRefs returns the topology for the settings on disk, falling
// back to v1-branch-only when settings cannot be loaded.
func ResolveCommittedRefs(ctx context.Context) CommittedRefs {
	return committedRefsFor(settings.MirrorsToV1CustomRef(ctx))
}

// ResolveCommittedRefsFromSettings returns the topology for an already-loaded
// settings object, honoring an injected EntireSettings rather than disk (e.g.
// attach). Nil resolves to v1-branch-only.
func ResolveCommittedRefsFromSettings(s *settings.EntireSettings) CommittedRefs {
	return committedRefsFor(s != nil && s.MirrorsToV1CustomRef())
}

func committedRefsFor(mirrorEnabled bool) CommittedRefs {
	v1Branch := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	refs := CommittedRefs{
		Primary: v1Branch,
		Read:    v1Branch,
		Push:    []plumbing.ReferenceName{v1Branch},
	}
	if mirrorEnabled {
		custom := plumbing.ReferenceName(paths.MetadataRefName)
		refs.Read = custom
		refs.Mirror = custom
	}
	return refs
}
