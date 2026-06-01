package cli

import (
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/testutil"
)

func setupCustomRefRepo(t *testing.T, version string) *git.Repository {
	t.Helper()
	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "f.txt", "init")
	testutil.GitAdd(t, tmpDir, "f.txt")
	testutil.GitCommit(t, tmpDir, "init")

	body := `{"enabled": true}`
	if version != "" {
		body = `{"enabled": true, "strategy_options": {"checkpoints_version": ` + version + `}}`
	}
	entireDir := filepath.Join(tmpDir, ".entire")
	require.NoError(t, os.MkdirAll(entireDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(entireDir, paths.SettingsFileName), []byte(body), 0o644))

	t.Chdir(tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)
	return repo
}

func pointV1MetadataBranchAtHead(t *testing.T, repo *git.Repository) plumbing.Hash {
	t.Helper()
	head, err := repo.Head()
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), head.Hash())))
	return head.Hash()
}

func readCustomRefHash(t *testing.T, repo *git.Repository) (plumbing.Hash, bool) {
	t.Helper()
	ref, err := repo.Reference(plumbing.ReferenceName(paths.MetadataRefName), true)
	if err != nil {
		return plumbing.ZeroHash, false
	}
	return ref.Hash(), true
}

// Not parallel: uses t.Chdir().
func TestMirrorToV1CustomRef_CreatesRefWhenEnabled(t *testing.T) {
	repo := setupCustomRefRepo(t, `"1.1"`)
	v1Hash := pointV1MetadataBranchAtHead(t, repo)

	require.NoError(t, mirrorToV1CustomRef(t.Context(), repo))

	got, ok := readCustomRefHash(t, repo)
	require.True(t, ok, "expected %s to exist", paths.MetadataRefName)
	assert.Equal(t, v1Hash, got)
}

// Not parallel: uses t.Chdir().
func TestMirrorToV1CustomRef_DisabledNoOp(t *testing.T) {
	repo := setupCustomRefRepo(t, "") // v1 only
	pointV1MetadataBranchAtHead(t, repo)

	require.NoError(t, mirrorToV1CustomRef(t.Context(), repo))

	_, ok := readCustomRefHash(t, repo)
	assert.False(t, ok, "v1 custom ref must not be created when not opted in")
}

// Not parallel: uses t.Chdir().
func TestMirrorToV1CustomRef_AdvancesExistingRef(t *testing.T) {
	repo := setupCustomRefRepo(t, `"1.1"`)
	oldHash := pointV1MetadataBranchAtHead(t, repo)
	require.NoError(t, repo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.ReferenceName(paths.MetadataRefName), oldHash)))

	cwd, err := os.Getwd()
	require.NoError(t, err)
	testutil.WriteFile(t, cwd, "f2.txt", "more")
	testutil.GitAdd(t, cwd, "f2.txt")
	testutil.GitCommit(t, cwd, "second")
	newHash := pointV1MetadataBranchAtHead(t, repo)
	require.NotEqual(t, oldHash, newHash)

	require.NoError(t, mirrorToV1CustomRef(t.Context(), repo))

	got, ok := readCustomRefHash(t, repo)
	require.True(t, ok)
	assert.Equal(t, newHash, got)
}

// Not parallel: uses t.Chdir().
func TestMirrorToV1CustomRef_V1MissingErrors(t *testing.T) {
	repo := setupCustomRefRepo(t, `"1.1"`) // no v1 metadata branch created

	err := mirrorToV1CustomRef(t.Context(), repo)
	require.Error(t, err)
	assert.Contains(t, err.Error(), paths.MetadataBranchName)

	_, ok := readCustomRefHash(t, repo)
	assert.False(t, ok, "v1 custom ref must not be created when v1 metadata branch is absent")
}

// Not parallel: uses t.Chdir().
func TestMirrorToV1CustomRef_DisabledTakesPrecedenceOverV1Missing(t *testing.T) {
	repo := setupCustomRefRepo(t, "")

	require.NoError(t, mirrorToV1CustomRef(t.Context(), repo))

	_, ok := readCustomRefHash(t, repo)
	assert.False(t, ok)
}
