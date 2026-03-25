package checkpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/entireio/cli/cmd/entire/cli/jsonutil"
	"github.com/entireio/cli/cmd/entire/cli/logging"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// DefaultMaxCheckpointsPerGeneration is the rotation threshold.
// When a generation reaches this many checkpoints, it is archived
// and a fresh /full/current is created.
const DefaultMaxCheckpointsPerGeneration = 100

// GenerationMetadata tracks the state of a /full/* generation.
// Stored at the tree root as generation.json and updated on every WriteCommitted.
// UpdateCommitted (stop-time finalization) does NOT update this file since it
// replaces an existing transcript rather than adding a new checkpoint.
//
// The generation's sequence number is derived from the ref name (e.g.,
// refs/entire/checkpoints/v2/full/0000000000001 → generation 1), not stored
// in this struct. The checkpoint count is len(Checkpoints).
type GenerationMetadata struct {
	// Checkpoints is the list of checkpoint IDs stored in this generation.
	// Used for finding which generation holds a specific checkpoint
	// without walking the tree. len(Checkpoints) gives the count.
	Checkpoints []string `json:"checkpoints"`

	// OldestCheckpointAt is the creation time of the earliest checkpoint.
	OldestCheckpointAt time.Time `json:"oldest_checkpoint_at"`

	// NewestCheckpointAt is the creation time of the most recent checkpoint.
	NewestCheckpointAt time.Time `json:"newest_checkpoint_at"`
}

// readGeneration reads generation.json from the given tree hash.
// Returns a zero-value GenerationMetadata if the file doesn't exist (new/empty generation).
func (s *V2GitStore) readGeneration(treeHash plumbing.Hash) (GenerationMetadata, error) {
	if treeHash == plumbing.ZeroHash {
		return GenerationMetadata{}, nil
	}

	tree, err := s.repo.TreeObject(treeHash)
	if err != nil {
		return GenerationMetadata{}, fmt.Errorf("failed to read tree: %w", err)
	}

	file, err := tree.File(paths.GenerationFileName)
	if err != nil {
		return GenerationMetadata{}, nil //nolint:nilerr // Missing file means empty/new generation
	}

	content, err := file.Contents()
	if err != nil {
		return GenerationMetadata{}, fmt.Errorf("failed to read %s: %w", paths.GenerationFileName, err)
	}

	var gen GenerationMetadata
	if err := json.Unmarshal([]byte(content), &gen); err != nil {
		return GenerationMetadata{}, fmt.Errorf("failed to parse %s: %w", paths.GenerationFileName, err)
	}

	return gen, nil
}

// readGenerationFromRef reads generation.json from the tree pointed to by the given ref.
func (s *V2GitStore) readGenerationFromRef(refName plumbing.ReferenceName) (GenerationMetadata, error) {
	_, treeHash, err := s.getRefState(refName)
	if err != nil {
		return GenerationMetadata{}, fmt.Errorf("failed to get ref state: %w", err)
	}
	return s.readGeneration(treeHash)
}

// marshalGenerationBlob marshals gen as generation.json and stores it as a git blob.
// Returns a TreeEntry ready to be placed in a tree.
func (s *V2GitStore) marshalGenerationBlob(gen GenerationMetadata) (object.TreeEntry, error) {
	data, err := jsonutil.MarshalIndentWithNewline(gen, "", "  ")
	if err != nil {
		return object.TreeEntry{}, fmt.Errorf("failed to marshal %s: %w", paths.GenerationFileName, err)
	}

	blobHash, err := CreateBlobFromContent(s.repo, data)
	if err != nil {
		return object.TreeEntry{}, fmt.Errorf("failed to create %s blob: %w", paths.GenerationFileName, err)
	}

	return object.TreeEntry{
		Name: paths.GenerationFileName,
		Mode: filemode.Regular,
		Hash: blobHash,
	}, nil
}

// writeGeneration marshals gen as generation.json and adds the blob entry to entries.
func (s *V2GitStore) writeGeneration(gen GenerationMetadata, entries map[string]object.TreeEntry) error {
	entry, err := s.marshalGenerationBlob(gen)
	if err != nil {
		return err
	}
	entries[paths.GenerationFileName] = entry
	return nil
}

// updateGenerationForWrite reads the current generation metadata, appends the
// checkpoint ID (if not already present), and updates timestamps.
// Returns the updated metadata for the caller to write into the tree.
func (s *V2GitStore) updateGenerationForWrite(rootTreeHash plumbing.Hash, checkpointID id.CheckpointID, now time.Time) (GenerationMetadata, error) {
	gen, err := s.readGeneration(rootTreeHash)
	if err != nil {
		return GenerationMetadata{}, err
	}

	cpStr := checkpointID.String()

	// Only append if checkpoint ID is not already present (multi-session writes
	// to the same checkpoint should not duplicate the ID).
	found := false
	for _, existing := range gen.Checkpoints {
		if existing == cpStr {
			found = true
			break
		}
	}
	if !found {
		gen.Checkpoints = append(gen.Checkpoints, cpStr)
	}

	gen.NewestCheckpointAt = now
	if gen.OldestCheckpointAt.IsZero() {
		gen.OldestCheckpointAt = now
	}

	return gen, nil
}

// addGenerationToRootTree adds generation.json to an existing root tree, returning
// a new root tree hash. Preserves all existing entries (shard directories, etc.).
func (s *V2GitStore) addGenerationToRootTree(rootTreeHash plumbing.Hash, gen GenerationMetadata) (plumbing.Hash, error) {
	entry, err := s.marshalGenerationBlob(gen)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	return UpdateSubtree(s.repo, rootTreeHash, nil, []object.TreeEntry{entry},
		UpdateSubtreeOptions{MergeMode: MergeKeepExisting})
}

// generationRefWidth is the zero-padded width of archived generation ref names.
const generationRefWidth = 13

// listArchivedGenerations returns the names of all archived generation refs
// (everything under V2FullRefPrefix except "current"), sorted ascending.
func (s *V2GitStore) listArchivedGenerations() ([]string, error) {
	refs, err := s.repo.References()
	if err != nil {
		return nil, fmt.Errorf("failed to list references: %w", err)
	}

	var archived []string
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().String()
		if !strings.HasPrefix(name, paths.V2FullRefPrefix) {
			return nil
		}
		suffix := strings.TrimPrefix(name, paths.V2FullRefPrefix)
		if suffix == "current" {
			return nil
		}
		archived = append(archived, suffix)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to iterate references: %w", err)
	}

	sort.Strings(archived)
	return archived, nil
}

// nextGenerationNumber returns the next sequential generation number for archiving.
// Scans existing archived refs and returns max+1. Returns 1 if no archives exist.
func (s *V2GitStore) nextGenerationNumber() (int, error) {
	archived, err := s.listArchivedGenerations()
	if err != nil {
		return 0, err
	}
	if len(archived) == 0 {
		return 1, nil
	}

	highest := archived[len(archived)-1]
	n, err := strconv.ParseInt(highest, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse archived generation number %q: %w", highest, err)
	}
	return int(n) + 1, nil
}

// rotateGeneration archives the current /full/current generation and creates
// a fresh orphan. This is a 2-phase operation:
//
//  1. Archive: determine the next generation number, create a new ref pointing
//     to the current /full/current commit.
//  2. Reset: create a fresh orphan commit with an empty tree + seed generation.json,
//     point /full/current at it.
func (s *V2GitStore) rotateGeneration(ctx context.Context) error {
	refName := plumbing.ReferenceName(paths.V2FullCurrentRefName)

	currentRef, err := s.repo.Reference(refName, true)
	if err != nil {
		return fmt.Errorf("rotation: failed to read /full/current: %w", err)
	}

	archiveNumber, err := s.nextGenerationNumber()
	if err != nil {
		return fmt.Errorf("rotation: failed to determine next generation number: %w", err)
	}

	// Phase 1: Archive — create ref pointing to the current commit
	archiveRefName := plumbing.ReferenceName(fmt.Sprintf("%s%0*d", paths.V2FullRefPrefix, generationRefWidth, archiveNumber))
	archiveRef := plumbing.NewHashReference(archiveRefName, currentRef.Hash())
	if err := s.repo.Storer.SetReference(archiveRef); err != nil {
		return fmt.Errorf("rotation: failed to create archived ref %s: %w", archiveRefName, err)
	}

	// Phase 2: Create fresh orphan /full/current
	seedGen := GenerationMetadata{
		Checkpoints: []string{},
	}
	seedEntries := make(map[string]object.TreeEntry)
	if err := s.writeGeneration(seedGen, seedEntries); err != nil {
		return fmt.Errorf("rotation: failed to build seed generation: %w", err)
	}
	seedTreeHash, err := BuildTreeFromEntries(s.repo, seedEntries)
	if err != nil {
		return fmt.Errorf("rotation: failed to build seed tree: %w", err)
	}

	authorName, authorEmail := GetGitAuthorFromRepo(s.repo)
	orphanCommitHash, err := CreateCommit(s.repo, seedTreeHash, plumbing.ZeroHash, "Start generation", authorName, authorEmail)
	if err != nil {
		return fmt.Errorf("rotation: failed to create orphan commit: %w", err)
	}

	orphanRef := plumbing.NewHashReference(refName, orphanCommitHash)
	if err := s.repo.Storer.SetReference(orphanRef); err != nil {
		return fmt.Errorf("rotation: failed to reset /full/current: %w", err)
	}

	logging.Info(ctx, "generation rotation complete",
		slog.Int("archived_generation", archiveNumber),
		slog.String("archive_ref", string(archiveRefName)),
	)

	return nil
}
