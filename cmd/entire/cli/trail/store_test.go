package trail

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
)

// initTestRepo creates a test git repository with an initial commit.
func initTestRepo(t *testing.T) *git.Repository {
	t.Helper()

	dir := t.TempDir()

	ctx := context.Background()
	cmds := [][]string{
		{"git", "init", dir},
		{"git", "-C", dir, "config", "user.name", "Test"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
	}
	for _, args := range cmds {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	// Create a file and commit
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	commitCmds := [][]string{
		{"git", "-C", dir, "add", "."},
		{"git", "-C", dir, "commit", "-m", "Initial commit"},
	}
	for _, args := range commitCmds {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("failed to open repo: %v", err)
	}
	return repo
}

func TestStore_EnsureBranch(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewStore(repo)

	// First call should create the branch
	if err := store.EnsureBranch(); err != nil {
		t.Fatalf("EnsureBranch() error = %v", err)
	}

	// Second call should be idempotent
	if err := store.EnsureBranch(); err != nil {
		t.Fatalf("EnsureBranch() second call error = %v", err)
	}
}

func TestStore_WriteAndRead(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewStore(repo)

	trailID, err := GenerateID()
	if err != nil {
		t.Fatalf("GenerateID() error = %v", err)
	}

	now := time.Now().Truncate(time.Second)
	metadata := &Metadata{
		TrailID:   trailID,
		Branch:    "feature/test",
		Base:      "main",
		Title:     "Test trail",
		Body:      "A test trail",
		Status:    StatusDraft,
		Author:    "tester",
		Assignees: []string{},
		Labels:    []string{"test"},
		CreatedAt: now,
		UpdatedAt: now,
	}

	discussion := &Discussion{Comments: []Comment{}}

	if err := store.Write(metadata, discussion, nil); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// Read it back
	gotMeta, gotDisc, _, err := store.Read(trailID)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if gotMeta.TrailID != trailID {
		t.Errorf("Read() trail_id = %s, want %s", gotMeta.TrailID, trailID)
	}
	if gotMeta.Branch != "feature/test" {
		t.Errorf("Read() branch = %q, want %q", gotMeta.Branch, "feature/test")
	}
	if gotMeta.Title != "Test trail" {
		t.Errorf("Read() title = %q, want %q", gotMeta.Title, "Test trail")
	}
	if gotMeta.Status != StatusDraft {
		t.Errorf("Read() status = %q, want %q", gotMeta.Status, StatusDraft)
	}
	if len(gotMeta.Labels) != 1 || gotMeta.Labels[0] != "test" {
		t.Errorf("Read() labels = %v, want [test]", gotMeta.Labels)
	}
	if gotDisc == nil {
		t.Error("Read() discussion should not be nil")
	}
}

func TestStore_FindByBranch(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewStore(repo)

	now := time.Now()

	// Create two trails for different branches
	for _, branch := range []string{"feature/a", "feature/b"} {
		id, err := GenerateID()
		if err != nil {
			t.Fatalf("GenerateID() error = %v", err)
		}
		meta := &Metadata{
			TrailID:   id,
			Branch:    branch,
			Base:      "main",
			Title:     HumanizeBranchName(branch),
			Status:    StatusDraft,
			Author:    "test",
			Assignees: []string{},
			Labels:    []string{},
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := store.Write(meta, nil, nil); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}

	// Find by branch
	found, err := store.FindByBranch("feature/a")
	if err != nil {
		t.Fatalf("FindByBranch() error = %v", err)
	}
	if found == nil {
		t.Fatal("FindByBranch() returned nil, expected trail")
	}
	if found.Branch != "feature/a" {
		t.Errorf("FindByBranch() branch = %q, want %q", found.Branch, "feature/a")
	}

	// Not found
	notFound, err := store.FindByBranch("feature/c")
	if err != nil {
		t.Fatalf("FindByBranch() error = %v", err)
	}
	if notFound != nil {
		t.Error("FindByBranch() should return nil for non-existent branch")
	}
}

func TestStore_List(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewStore(repo)

	// List when no trails exist (branch doesn't exist yet)
	trails, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if trails != nil {
		t.Errorf("List() = %v, want nil for empty store", trails)
	}

	// Create a trail
	now := time.Now()
	id, err := GenerateID()
	if err != nil {
		t.Fatalf("GenerateID() error = %v", err)
	}
	meta := &Metadata{
		TrailID:   id,
		Branch:    "feature/test",
		Base:      "main",
		Title:     "Test",
		Status:    StatusDraft,
		Author:    "test",
		Assignees: []string{},
		Labels:    []string{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.Write(meta, nil, nil); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	trails, err = store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(trails) != 1 {
		t.Fatalf("List() returned %d trails, want 1", len(trails))
	}
	if trails[0].TrailID != id {
		t.Errorf("List()[0].TrailID = %s, want %s", trails[0].TrailID, id)
	}
}

func TestStore_Update(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewStore(repo)

	now := time.Now()
	id, err := GenerateID()
	if err != nil {
		t.Fatalf("GenerateID() error = %v", err)
	}
	meta := &Metadata{
		TrailID:   id,
		Branch:    "feature/test",
		Base:      "main",
		Title:     "Original",
		Status:    StatusDraft,
		Author:    "test",
		Assignees: []string{},
		Labels:    []string{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.Write(meta, nil, nil); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// Update
	if err := store.Update(id, func(m *Metadata) {
		m.Title = "Updated"
		m.Status = StatusInProgress
		m.Labels = []string{"urgent"}
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	// Verify
	updated, _, _, err := store.Read(id)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if updated.Title != "Updated" {
		t.Errorf("Read() title = %q, want %q", updated.Title, "Updated")
	}
	if updated.Status != StatusInProgress {
		t.Errorf("Read() status = %q, want %q", updated.Status, StatusInProgress)
	}
	if len(updated.Labels) != 1 || updated.Labels[0] != "urgent" {
		t.Errorf("Read() labels = %v, want [urgent]", updated.Labels)
	}
	if !updated.UpdatedAt.After(now) {
		t.Error("Read() updated_at should be after original")
	}
}

func TestStore_Delete(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewStore(repo)

	now := time.Now()
	id, err := GenerateID()
	if err != nil {
		t.Fatalf("GenerateID() error = %v", err)
	}
	meta := &Metadata{
		TrailID:   id,
		Branch:    "feature/test",
		Base:      "main",
		Title:     "To delete",
		Status:    StatusDraft,
		Author:    "test",
		Assignees: []string{},
		Labels:    []string{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.Write(meta, nil, nil); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// Delete
	if err := store.Delete(id); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Verify it's gone
	_, _, _, err = store.Read(id)
	if err == nil {
		t.Error("Read() should fail after delete")
	}
}

func TestStore_ReadNonExistent(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewStore(repo)

	if err := store.EnsureBranch(); err != nil {
		t.Fatalf("EnsureBranch() error = %v", err)
	}

	_, _, _, err := store.Read(ID("abcdef123456"))
	if err == nil {
		t.Error("Read() should fail for non-existent trail")
	}
}
