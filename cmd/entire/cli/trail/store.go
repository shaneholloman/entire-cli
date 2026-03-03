package trail

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"
	"github.com/entireio/cli/cmd/entire/cli/paths"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	metadataFile    = "metadata.json"
	discussionFile  = "discussion.json"
	checkpointsFile = "checkpoints.json"
)

// ErrTrailNotFound is returned when a trail cannot be found.
var ErrTrailNotFound = errors.New("trail not found")

// Store provides CRUD operations for trail metadata on the entire/trails/v1 branch.
type Store struct {
	repo *git.Repository
}

// NewStore creates a new trail store backed by the given git repository.
func NewStore(repo *git.Repository) *Store {
	return &Store{repo: repo}
}

// EnsureBranch creates the entire/trails/v1 orphan branch if it doesn't exist.
func (s *Store) EnsureBranch() error {
	refName := plumbing.NewBranchReferenceName(paths.TrailsBranchName)
	_, err := s.repo.Reference(refName, true)
	if err == nil {
		return nil // Branch already exists
	}

	// Create orphan branch with empty tree
	emptyTreeHash, err := checkpoint.BuildTreeFromEntries(s.repo, make(map[string]object.TreeEntry))
	if err != nil {
		return fmt.Errorf("failed to build empty tree: %w", err)
	}

	authorName, authorEmail := checkpoint.GetGitAuthorFromRepo(s.repo)
	commitHash, err := s.createCommit(emptyTreeHash, plumbing.ZeroHash, "Initialize trails branch", authorName, authorEmail)
	if err != nil {
		return fmt.Errorf("failed to create initial commit: %w", err)
	}

	newRef := plumbing.NewHashReference(refName, commitHash)
	if err := s.repo.Storer.SetReference(newRef); err != nil {
		return fmt.Errorf("failed to set branch reference: %w", err)
	}
	return nil
}

// Write writes trail metadata, discussion, and checkpoints to the entire/trails/v1 branch.
// If checkpoints is nil, an empty checkpoints list is written.
func (s *Store) Write(metadata *Metadata, discussion *Discussion, checkpoints *Checkpoints) error {
	if metadata.TrailID.IsEmpty() {
		return errors.New("trail ID is required")
	}

	if err := s.EnsureBranch(); err != nil {
		return fmt.Errorf("failed to ensure trails branch: %w", err)
	}

	// Get current branch tree
	ref, entries, err := s.getBranchEntries()
	if err != nil {
		return fmt.Errorf("failed to get branch entries: %w", err)
	}

	// Build sharded path
	basePath := metadata.TrailID.Path() + "/"

	// Create metadata blob
	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	metadataBlob, err := checkpoint.CreateBlobFromContent(s.repo, metadataJSON)
	if err != nil {
		return fmt.Errorf("failed to create metadata blob: %w", err)
	}
	entries[basePath+metadataFile] = object.TreeEntry{
		Name: basePath + metadataFile,
		Mode: filemode.Regular,
		Hash: metadataBlob,
	}

	// Create discussion blob
	if discussion == nil {
		discussion = &Discussion{Comments: []Comment{}}
	}
	discussionJSON, err := json.MarshalIndent(discussion, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal discussion: %w", err)
	}
	discussionBlob, err := checkpoint.CreateBlobFromContent(s.repo, discussionJSON)
	if err != nil {
		return fmt.Errorf("failed to create discussion blob: %w", err)
	}
	entries[basePath+discussionFile] = object.TreeEntry{
		Name: basePath + discussionFile,
		Mode: filemode.Regular,
		Hash: discussionBlob,
	}

	// Create checkpoints blob
	if checkpoints == nil {
		checkpoints = &Checkpoints{Checkpoints: []CheckpointRef{}}
	}
	checkpointsJSON, err := json.MarshalIndent(checkpoints, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoints: %w", err)
	}
	checkpointsBlob, err := checkpoint.CreateBlobFromContent(s.repo, checkpointsJSON)
	if err != nil {
		return fmt.Errorf("failed to create checkpoints blob: %w", err)
	}
	entries[basePath+checkpointsFile] = object.TreeEntry{
		Name: basePath + checkpointsFile,
		Mode: filemode.Regular,
		Hash: checkpointsBlob,
	}

	// Build tree and commit
	newTreeHash, err := checkpoint.BuildTreeFromEntries(s.repo, entries)
	if err != nil {
		return fmt.Errorf("failed to build tree: %w", err)
	}

	authorName, authorEmail := checkpoint.GetGitAuthorFromRepo(s.repo)
	commitMsg := fmt.Sprintf("Trail: %s (%s)", metadata.Title, metadata.TrailID)
	commitHash, err := s.createCommit(newTreeHash, ref.Hash(), commitMsg, authorName, authorEmail)
	if err != nil {
		return fmt.Errorf("failed to create commit: %w", err)
	}

	// Update branch ref
	newRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(paths.TrailsBranchName), commitHash)
	if err := s.repo.Storer.SetReference(newRef); err != nil {
		return fmt.Errorf("failed to update branch reference: %w", err)
	}

	return nil
}

// Read reads a trail by its ID from the entire/trails/v1 branch.
func (s *Store) Read(trailID ID) (*Metadata, *Discussion, *Checkpoints, error) {
	tree, err := s.getBranchTree()
	if err != nil {
		return nil, nil, nil, err
	}

	basePath := trailID.Path() + "/"

	// Read metadata
	metadataEntry, err := tree.FindEntry(basePath + metadataFile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("trail %s not found: %w", trailID, err)
	}
	metadataBlob, err := s.repo.BlobObject(metadataEntry.Hash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read metadata blob: %w", err)
	}
	metadataReader, err := metadataBlob.Reader()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open metadata reader: %w", err)
	}
	defer metadataReader.Close()

	var metadata Metadata
	if err := json.NewDecoder(metadataReader).Decode(&metadata); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to decode metadata: %w", err)
	}

	// Read discussion (optional, may not exist yet)
	var discussion Discussion
	discussionEntry, err := tree.FindEntry(basePath + discussionFile)
	if err == nil {
		discussionBlob, blobErr := s.repo.BlobObject(discussionEntry.Hash)
		if blobErr == nil {
			discussionReader, readerErr := discussionBlob.Reader()
			if readerErr == nil {
				//nolint:errcheck,gosec // best-effort decode of optional discussion
				json.NewDecoder(discussionReader).Decode(&discussion)
				_ = discussionReader.Close()
			}
		}
	}

	// Read checkpoints (optional, may not exist yet)
	var checkpoints Checkpoints
	checkpointsEntry, err := tree.FindEntry(basePath + checkpointsFile)
	if err == nil {
		checkpointsBlob, blobErr := s.repo.BlobObject(checkpointsEntry.Hash)
		if blobErr == nil {
			checkpointsReader, readerErr := checkpointsBlob.Reader()
			if readerErr == nil {
				//nolint:errcheck,gosec // best-effort decode of optional checkpoints
				json.NewDecoder(checkpointsReader).Decode(&checkpoints)
				_ = checkpointsReader.Close()
			}
		}
	}

	return &metadata, &discussion, &checkpoints, nil
}

// FindByBranch finds a trail for the given branch name.
// Returns (nil, nil) if no trail exists for the branch.
func (s *Store) FindByBranch(branchName string) (*Metadata, error) {
	trails, err := s.List()
	if err != nil {
		return nil, err
	}

	for _, t := range trails {
		if t.Branch == branchName {
			return t, nil
		}
	}
	return nil, nil //nolint:nilnil // nil, nil means "not found" — callers check both
}

// List returns all trail metadata from the entire/trails/v1 branch.
func (s *Store) List() ([]*Metadata, error) {
	tree, err := s.getBranchTree()
	if err != nil {
		// Branch doesn't exist yet — no trails
		return nil, nil //nolint:nilerr // Expected when no trails exist yet
	}

	var trails []*Metadata
	entries := make(map[string]object.TreeEntry)
	if err := checkpoint.FlattenTree(s.repo, tree, "", entries); err != nil {
		return nil, fmt.Errorf("failed to flatten tree: %w", err)
	}

	// Find all metadata.json files
	for path, entry := range entries {
		if !strings.HasSuffix(path, "/"+metadataFile) {
			continue
		}

		blob, err := s.repo.BlobObject(entry.Hash)
		if err != nil {
			continue
		}
		reader, err := blob.Reader()
		if err != nil {
			continue
		}

		var metadata Metadata
		decodeErr := json.NewDecoder(reader).Decode(&metadata)
		_ = reader.Close()
		if decodeErr != nil {
			continue
		}

		trails = append(trails, &metadata)
	}

	return trails, nil
}

// Update updates an existing trail's metadata. It reads the current metadata,
// applies the provided update function, and writes it back.
func (s *Store) Update(trailID ID, updateFn func(*Metadata)) error {
	metadata, discussion, checkpoints, err := s.Read(trailID)
	if err != nil {
		return fmt.Errorf("failed to read trail for update: %w", err)
	}

	updateFn(metadata)
	metadata.UpdatedAt = time.Now()

	return s.Write(metadata, discussion, checkpoints)
}

// AddCheckpoint prepends a checkpoint reference to a trail's checkpoints list (newest first).
func (s *Store) AddCheckpoint(trailID ID, ref CheckpointRef) error {
	metadata, discussion, checkpoints, err := s.Read(trailID)
	if err != nil {
		return fmt.Errorf("failed to read trail for checkpoint update: %w", err)
	}

	if checkpoints == nil {
		checkpoints = &Checkpoints{Checkpoints: []CheckpointRef{}}
	}

	// Prepend new ref (newest first) without always allocating a new slice.
	// Grow the slice by one, shift existing elements right, and insert at index 0.
	checkpoints.Checkpoints = append(checkpoints.Checkpoints, CheckpointRef{})
	copy(checkpoints.Checkpoints[1:], checkpoints.Checkpoints[:len(checkpoints.Checkpoints)-1])
	checkpoints.Checkpoints[0] = ref

	return s.Write(metadata, discussion, checkpoints)
}

// Delete removes a trail from the entire/trails/v1 branch.
func (s *Store) Delete(trailID ID) error {
	ref, entries, err := s.getBranchEntries()
	if err != nil {
		return fmt.Errorf("failed to get branch entries: %w", err)
	}

	basePath := trailID.Path() + "/"

	// Remove entries for this trail
	found := false
	for path := range entries {
		if strings.HasPrefix(path, basePath) {
			delete(entries, path)
			found = true
		}
	}
	if !found {
		return fmt.Errorf("trail %s not found", trailID)
	}

	// Build tree and commit
	newTreeHash, err := checkpoint.BuildTreeFromEntries(s.repo, entries)
	if err != nil {
		return fmt.Errorf("failed to build tree: %w", err)
	}

	authorName, authorEmail := checkpoint.GetGitAuthorFromRepo(s.repo)
	commitMsg := fmt.Sprintf("Delete trail: %s", trailID)
	commitHash, err := s.createCommit(newTreeHash, ref.Hash(), commitMsg, authorName, authorEmail)
	if err != nil {
		return fmt.Errorf("failed to create commit: %w", err)
	}

	newRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(paths.TrailsBranchName), commitHash)
	if err := s.repo.Storer.SetReference(newRef); err != nil {
		return fmt.Errorf("failed to update branch reference: %w", err)
	}

	return nil
}

// getBranchTree returns the tree for the entire/trails/v1 branch HEAD.
func (s *Store) getBranchTree() (*object.Tree, error) {
	refName := plumbing.NewBranchReferenceName(paths.TrailsBranchName)
	ref, err := s.repo.Reference(refName, true)
	if err != nil {
		// Try remote tracking branch
		remoteRefName := plumbing.NewRemoteReferenceName("origin", paths.TrailsBranchName)
		ref, err = s.repo.Reference(remoteRefName, true)
		if err != nil {
			return nil, fmt.Errorf("trails branch not found: %w", err)
		}
	}

	commit, err := s.repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	return tree, nil
}

// getBranchEntries returns the current branch reference and a flat map of all tree entries.
func (s *Store) getBranchEntries() (*plumbing.Reference, map[string]object.TreeEntry, error) {
	refName := plumbing.NewBranchReferenceName(paths.TrailsBranchName)
	ref, err := s.repo.Reference(refName, true)
	if err != nil {
		return nil, nil, fmt.Errorf("trails branch not found: %w", err)
	}

	commit, err := s.repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get tree: %w", err)
	}

	entries := make(map[string]object.TreeEntry)
	if err := checkpoint.FlattenTree(s.repo, tree, "", entries); err != nil {
		return nil, nil, fmt.Errorf("failed to flatten tree: %w", err)
	}

	return ref, entries, nil
}

// createCommit creates a commit on the trails branch.
func (s *Store) createCommit(treeHash, parentHash plumbing.Hash, message, authorName, authorEmail string) (plumbing.Hash, error) {
	now := time.Now()
	sig := object.Signature{
		Name:  authorName,
		Email: authorEmail,
		When:  now,
	}

	commit := &object.Commit{
		TreeHash:  treeHash,
		Author:    sig,
		Committer: sig,
		Message:   message,
	}

	if parentHash != plumbing.ZeroHash {
		commit.ParentHashes = []plumbing.Hash{parentHash}
	}

	obj := s.repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode commit: %w", err)
	}

	hash, err := s.repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store commit: %w", err)
	}

	return hash, nil
}
