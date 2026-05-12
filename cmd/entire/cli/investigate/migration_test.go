package investigate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entireio/cli/cmd/entire/cli/testutil"
)

func TestMigration_NoOpWhenNoInvestigateField(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	testutil.InitRepo(t, tmp)
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, ".entire"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmp, ".entire/settings.json"),
		[]byte(`{"enabled": true}`), 0o644))

	var promptCount int
	promptYN := func(_ context.Context, _ string, def bool) (bool, error) {
		promptCount++
		return def, nil
	}

	err := maybePromptInvestigateSettingsMigration(
		context.Background(), os.Stdout, os.Stderr, true, promptYN)
	require.NoError(t, err)
	require.Equal(t, 0, promptCount, "must not prompt when no investigate field present")
}

func TestMigration_PromptAcceptedMovesField(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	testutil.InitRepo(t, tmp)
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, ".entire"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmp, ".entire/settings.json"),
		[]byte(`{"enabled":true,"investigate":{"agents":["claude-code"],"max_turns":3}}`),
		0o644))

	promptYN := func(_ context.Context, _ string, _ bool) (bool, error) { return true, nil }

	err := maybePromptInvestigateSettingsMigration(
		context.Background(), os.Stdout, os.Stderr, true, promptYN)
	require.NoError(t, err)

	base, err := os.ReadFile(filepath.Join(tmp, ".entire/settings.json"))
	require.NoError(t, err)
	require.NotContains(t, string(base), `"investigate"`)

	local, err := os.ReadFile(filepath.Join(tmp, ".entire/settings.local.json"))
	require.NoError(t, err)
	require.Contains(t, string(local), `"claude-code"`)
}

func TestMigration_PromptDeclinedLeavesField(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	testutil.InitRepo(t, tmp)
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, ".entire"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmp, ".entire/settings.json"),
		[]byte(`{"enabled":true,"investigate":{"agents":["codex"]}}`), 0o644))

	promptYN := func(_ context.Context, _ string, _ bool) (bool, error) { return false, nil }

	err := maybePromptInvestigateSettingsMigration(
		context.Background(), os.Stdout, os.Stderr, true, promptYN)
	require.NoError(t, err)

	base, err := os.ReadFile(filepath.Join(tmp, ".entire/settings.json"))
	require.NoError(t, err)
	require.Contains(t, string(base), `"investigate"`)

	_, err = os.Stat(filepath.Join(tmp, ".entire/settings.local.json"))
	require.True(t, os.IsNotExist(err), "local file must not be created on decline")
}

func TestMigration_NonInteractiveEmitsGuidance(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	testutil.InitRepo(t, tmp)
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, ".entire"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmp, ".entire/settings.json"),
		[]byte(`{"investigate":{"agents":["gemini-cli"]}}`), 0o644))

	var errBuf strings.Builder
	err := maybePromptInvestigateSettingsMigration(
		context.Background(), os.Stdout, &errBuf, false, nil)
	require.NoError(t, err)
	require.Contains(t, errBuf.String(), "entire investigate --edit")

	base, err := os.ReadFile(filepath.Join(tmp, ".entire/settings.json"))
	require.NoError(t, err)
	require.Contains(t, string(base), `"investigate"`)
}

func TestMigration_PreservesExistingLocalInvestigate(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	testutil.InitRepo(t, tmp)
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, ".entire"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmp, ".entire/settings.json"),
		[]byte(`{"investigate":{"agents":["claude-code"]}}`), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmp, ".entire/settings.local.json"),
		[]byte(`{"investigate":{"agents":["codex"]}}`), 0o644))

	promptYN := func(_ context.Context, _ string, _ bool) (bool, error) { return true, nil }
	err := maybePromptInvestigateSettingsMigration(
		context.Background(), os.Stdout, os.Stderr, true, promptYN)
	require.NoError(t, err)

	local, err := os.ReadFile(filepath.Join(tmp, ".entire/settings.local.json"))
	require.NoError(t, err)

	var localObj map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(local, &localObj))
	require.Contains(t, string(localObj["investigate"]), "codex",
		"existing local investigate must not be overwritten")
}
