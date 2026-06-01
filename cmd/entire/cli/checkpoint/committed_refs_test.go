package checkpoint

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/entireio/cli/cmd/entire/cli/settings"
)

// TestResolveCommittedRefs covers the committed-ref topology across
// checkpoints_version values. Mirrors settings.TestMirrorsToV1CustomRef: only
// the JSON string "1.1" opts into the v1.1 mirror; everything else is v1-only.
//
// Not parallel: uses t.Chdir() so settings.Load resolves the test repo.
func TestResolveCommittedRefs(t *testing.T) {
	tests := []struct {
		name       string
		version    string // value placed at strategy_options.checkpoints_version; "" omits it
		wantMirror bool
	}{
		{"unset is v1 only", "", false},
		{"string 1.1 opts into mirror", `"1.1"`, true},
		{"string 1 is v1 only", `"1"`, false},
		{"numeric 1 is v1 only", `1`, false},
		{"numeric 1.1 is v1 only (string only)", `1.1`, false},
		{"garbage is v1 only", `"abc"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			writeSettings(t, dir, tt.version)

			refs := ResolveCommittedRefs(context.Background())

			// Primary is always the v1 branch in the mirror-first phase.
			assert.Equal(t, v1BranchRef(), refs.Primary, "Primary")
			assert.Equal(t, tt.wantMirror, refs.HasMirror(), "HasMirror")

			if tt.wantMirror {
				assert.Equal(t, customRef(), refs.Read, "Read")
				assert.Equal(t, customRef(), refs.Mirror, "Mirror")
				// Invariant: with a mirror, reads resolve against it.
				assert.Equal(t, refs.Mirror, refs.Read, "Read==Mirror invariant")
			} else {
				assert.Equal(t, v1BranchRef(), refs.Read, "Read")
				assert.Empty(t, refs.Mirror, "Mirror")
				// Invariant: without a mirror, reads resolve against Primary.
				assert.Equal(t, refs.Primary, refs.Read, "Read==Primary invariant")
			}
		})
	}
}

// TestResolveCommittedRefsFromSettings covers resolution from an already-loaded
// settings object (the path attach uses for an injected EntireSettings),
// including the nil case.
func TestResolveCommittedRefsFromSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		settings   *settings.EntireSettings
		wantMirror bool
	}{
		{"nil settings is v1 only", nil, false},
		{"empty settings is v1 only", &settings.EntireSettings{}, false},
		{
			"checkpoints_version 1.1 opts into mirror",
			&settings.EntireSettings{StrategyOptions: map[string]any{"checkpoints_version": "1.1"}},
			true,
		},
		{
			"checkpoints_version 1 is v1 only",
			&settings.EntireSettings{StrategyOptions: map[string]any{"checkpoints_version": "1"}},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			refs := ResolveCommittedRefsFromSettings(tt.settings)
			assert.Equal(t, v1BranchRef(), refs.Primary, "Primary")
			assert.Equal(t, tt.wantMirror, refs.HasMirror(), "HasMirror")
			if tt.wantMirror {
				assert.Equal(t, customRef(), refs.Mirror, "Mirror")
				assert.Equal(t, customRef(), refs.Read, "Read")
			} else {
				assert.Empty(t, refs.Mirror, "Mirror")
				assert.Equal(t, v1BranchRef(), refs.Read, "Read")
			}
		})
	}
}
