package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/entireio/cli/cmd/entire/cli/paths"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// ReadCommitted reads the checkpoint summary from the v2 /main ref.
// Returns nil, nil if the checkpoint doesn't exist (same contract as GitStore.ReadCommitted).
func (s *V2GitStore) ReadCommitted(ctx context.Context, checkpointID id.CheckpointID) (*CheckpointSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // Propagating context cancellation
	}

	refName := plumbing.ReferenceName(paths.V2MainRefName)
	_, rootTreeHash, err := s.getRefState(refName)
	if err != nil {
		return nil, nil //nolint:nilnil,nilerr // Ref doesn't exist means no checkpoint
	}

	rootTree, err := s.repo.TreeObject(rootTreeHash)
	if err != nil {
		return nil, nil //nolint:nilnil,nilerr // Tree not readable
	}

	cpTree, err := rootTree.Tree(checkpointID.Path())
	if err != nil {
		return nil, nil //nolint:nilnil,nilerr // Checkpoint subtree not found
	}

	metadataFile, err := cpTree.File(paths.MetadataFileName)
	if err != nil {
		return nil, nil //nolint:nilnil,nilerr // metadata.json not found
	}

	content, err := metadataFile.Contents()
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata.json: %w", err)
	}

	var summary CheckpointSummary
	if err := json.Unmarshal([]byte(content), &summary); err != nil {
		return nil, fmt.Errorf("failed to parse metadata.json: %w", err)
	}

	return &summary, nil
}

// ReadSessionContent reads a session's metadata and prompts from the v2 /main ref,
// and the raw transcript from /full/* refs (current + archived generations).
// If the transcript is not found in any /full/* ref, the returned SessionContent
// has an empty Transcript field — metadata and prompts are still populated.
func (s *V2GitStore) ReadSessionContent(ctx context.Context, checkpointID id.CheckpointID, sessionIndex int) (*SessionContent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // Propagating context cancellation
	}

	refName := plumbing.ReferenceName(paths.V2MainRefName)
	_, rootTreeHash, err := s.getRefState(refName)
	if err != nil {
		return nil, ErrCheckpointNotFound
	}

	rootTree, err := s.repo.TreeObject(rootTreeHash)
	if err != nil {
		return nil, ErrCheckpointNotFound
	}

	cpTree, err := rootTree.Tree(checkpointID.Path())
	if err != nil {
		return nil, ErrCheckpointNotFound
	}

	sessionDir := strconv.Itoa(sessionIndex)
	sessionTree, err := cpTree.Tree(sessionDir)
	if err != nil {
		return nil, fmt.Errorf("session %d not found: %w", sessionIndex, err)
	}

	result := &SessionContent{}

	if metadataFile, fileErr := sessionTree.File(paths.MetadataFileName); fileErr == nil {
		if content, contentErr := metadataFile.Contents(); contentErr == nil {
			if jsonErr := json.Unmarshal([]byte(content), &result.Metadata); jsonErr != nil {
				return nil, fmt.Errorf("failed to parse session metadata: %w", jsonErr)
			}
		}
	}

	if file, fileErr := sessionTree.File(paths.PromptFileName); fileErr == nil {
		if content, contentErr := file.Contents(); contentErr == nil {
			result.Prompts = content
		}
	}

	transcript, _ := s.resolveTranscriptFromFull(ctx, checkpointID, sessionIndex) //nolint:errcheck // Missing transcript is not an error — caller handles empty transcript
	result.Transcript = transcript

	return result, nil
}

// resolveTranscriptFromFull searches /full/current then archived generations
// for the raw transcript of a specific checkpoint session.
func (s *V2GitStore) resolveTranscriptFromFull(ctx context.Context, checkpointID id.CheckpointID, sessionIndex int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // Propagating context cancellation
	}

	sessionPath := fmt.Sprintf("%s/%d", checkpointID.Path(), sessionIndex)

	transcript, err := s.readTranscriptFromRef(plumbing.ReferenceName(paths.V2FullCurrentRefName), sessionPath)
	if err == nil && len(transcript) > 0 {
		return transcript, nil
	}

	archived, err := s.listArchivedGenerations()
	if err != nil {
		return nil, err
	}
	for i := len(archived) - 1; i >= 0; i-- {
		refName := plumbing.ReferenceName(paths.V2FullRefPrefix + archived[i])
		transcript, err := s.readTranscriptFromRef(refName, sessionPath)
		if err == nil && len(transcript) > 0 {
			return transcript, nil
		}
	}

	return nil, nil
}

// readTranscriptFromRef reads the raw transcript from a specific /full/* ref.
// Handles both chunked and non-chunked transcripts.
func (s *V2GitStore) readTranscriptFromRef(refName plumbing.ReferenceName, sessionPath string) ([]byte, error) {
	_, rootTreeHash, err := s.getRefState(refName)
	if err != nil {
		return nil, err
	}

	rootTree, err := s.repo.TreeObject(rootTreeHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read tree: %w", err)
	}

	sessionTree, err := rootTree.Tree(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("session path %s not found: %w", sessionPath, err)
	}

	file, err := sessionTree.File(paths.TranscriptFileName)
	if err == nil {
		content, contentErr := file.Contents()
		if contentErr == nil {
			return []byte(content), nil
		}
	}

	return readChunkedTranscriptFromTree(sessionTree)
}

// readChunkedTranscriptFromTree reads and reassembles chunked transcript files from a tree.
func readChunkedTranscriptFromTree(tree *object.Tree) ([]byte, error) {
	var chunks []struct {
		index int
		name  string
	}

	for _, entry := range tree.Entries {
		if strings.HasPrefix(entry.Name, paths.TranscriptFileName+".") {
			suffix := strings.TrimPrefix(entry.Name, paths.TranscriptFileName+".")
			idx, err := strconv.Atoi(suffix)
			if err == nil && idx > 0 {
				chunks = append(chunks, struct {
					index int
					name  string
				}{index: idx, name: entry.Name})
			}
		}
	}

	if len(chunks) == 0 {
		return nil, errors.New("no transcript files found")
	}

	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].index < chunks[j].index
	})

	var result []byte
	for _, chunk := range chunks {
		file, err := tree.File(chunk.name)
		if err != nil {
			return nil, fmt.Errorf("failed to read chunk %s: %w", chunk.name, err)
		}
		content, err := file.Contents()
		if err != nil {
			return nil, fmt.Errorf("failed to read chunk content %s: %w", chunk.name, err)
		}
		result = append(result, []byte(content)...)
	}

	return result, nil
}

// GetSessionLog reads the latest session's raw transcript and session ID from v2 refs.
// Convenience wrapper matching the GitStore.GetSessionLog signature.
func (s *V2GitStore) GetSessionLog(ctx context.Context, cpID id.CheckpointID) ([]byte, string, error) {
	summary, err := s.ReadCommitted(ctx, cpID)
	if err != nil {
		return nil, "", err
	}
	if summary == nil {
		return nil, "", ErrCheckpointNotFound
	}
	if len(summary.Sessions) == 0 {
		return nil, "", ErrCheckpointNotFound
	}

	latestIndex := len(summary.Sessions) - 1
	content, err := s.ReadSessionContent(ctx, cpID, latestIndex)
	if err != nil {
		return nil, "", err
	}
	if len(content.Transcript) == 0 {
		return nil, "", ErrNoTranscript
	}
	return content.Transcript, content.Metadata.SessionID, nil
}
