package investigate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entireio/cli/cmd/entire/cli/settings"
	"github.com/entireio/cli/cmd/entire/cli/testutil"
)

// TestSaveInvestigateConfig_WritesLocalFile verifies that
// saveInvestigateConfig persists into .entire/settings.local.json (not the
// committed .entire/settings.json). Mirrors the review-side behaviour so
// agent picker output stays out of project settings.
//
// NOTE: This test uses t.Chdir, which Go forbids combining with
// t.Parallel(). Do not add t.Parallel() here.
func TestSaveInvestigateConfig_WritesLocalFile(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	testutil.InitRepo(t, tmp)

	cfg := &settings.InvestigateConfig{
		Agents:   []string{"claude-code", "codex"},
		MaxTurns: 4,
		Quorum:   2,
	}
	require.NoError(t, saveInvestigateConfig(context.Background(), cfg))

	// settings.json should NOT contain investigate.
	base, err := os.ReadFile(filepath.Join(tmp, ".entire/settings.json"))
	if err == nil {
		require.NotContains(t, string(base), `"investigate"`,
			"investigate must not be written to project settings")
	}

	// settings.local.json should contain investigate.
	local, err := os.ReadFile(filepath.Join(tmp, ".entire/settings.local.json"))
	require.NoError(t, err)
	require.Contains(t, string(local), `"agents"`)
	require.Contains(t, string(local), `"claude-code"`)
}

// TestResolveDocPaths_PerRunIsolation verifies that two runs land in
// distinct per-run directories under the git common dir, so they don't
// stomp each other's findings/state files.
func TestResolveDocPaths_PerRunIsolation(t *testing.T) {
	t.Parallel()

	const commonDir = "/repo/.git"

	findings1 := resolveDocPaths(commonDir, "aaaaaaaaaaaa")
	findings2 := resolveDocPaths(commonDir, "bbbbbbbbbbbb")

	require.Equal(t,
		filepath.Join(commonDir, "entire-investigations", "aaaaaaaaaaaa", "findings.md"),
		findings1,
	)
	require.Equal(t,
		filepath.Join(commonDir, "entire-investigations", "bbbbbbbbbbbb", "findings.md"),
		findings2,
	)
	require.NotEqual(t, findings1, findings2,
		"two runs must not share findings doc paths")
}
