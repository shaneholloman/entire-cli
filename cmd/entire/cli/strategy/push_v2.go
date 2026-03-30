package strategy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// pushRefIfNeeded pushes a custom ref to the given target if it exists locally.
// Custom refs (under refs/entire/) don't have remote-tracking refs, so there's
// no "has unpushed" optimization — we always attempt the push and let git handle
// the no-op case.
func pushRefIfNeeded(ctx context.Context, target string, refName plumbing.ReferenceName) error {
	repo, err := OpenRepository(ctx)
	if err != nil {
		return nil //nolint:nilerr // Hook must be silent on failure
	}

	if _, err := repo.Reference(refName, true); err != nil {
		return nil //nolint:nilerr // Ref doesn't exist locally, nothing to push
	}

	return doPushRef(ctx, target, refName)
}

// tryPushRef attempts to push a custom ref using an explicit refspec.
func tryPushRef(ctx context.Context, target string, refName plumbing.ReferenceName) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// Use --no-verify to prevent recursive hook calls (this runs inside pre-push)
	refSpec := fmt.Sprintf("%s:%s", refName, refName)
	cmd := exec.CommandContext(ctx, "git", "push", "--no-verify", target, refSpec)
	cmd.Stdin = nil // Disconnect stdin to prevent hanging in hook context

	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "non-fast-forward") ||
			strings.Contains(string(output), "rejected") {
			return errors.New("non-fast-forward")
		}
		return fmt.Errorf("push failed: %s", output)
	}
	return nil
}

// doPushRef pushes a custom ref with fetch+merge recovery on conflict.
func doPushRef(ctx context.Context, target string, refName plumbing.ReferenceName) error {
	displayTarget := target
	if isURL(target) {
		displayTarget = "checkpoint remote"
	}

	shortRef := shortRefName(refName)
	fmt.Fprintf(os.Stderr, "[entire] Pushing %s to %s...", shortRef, displayTarget)
	stop := startProgressDots(os.Stderr)

	if err := tryPushRef(ctx, target, refName); err == nil {
		stop(" done")
		return nil
	}
	stop("")

	fmt.Fprintf(os.Stderr, "[entire] Syncing %s with remote...", shortRef)
	stop = startProgressDots(os.Stderr)

	if err := fetchAndMergeRef(ctx, target, refName); err != nil {
		stop("")
		fmt.Fprintf(os.Stderr, "[entire] Warning: couldn't sync %s: %v\n", shortRef, err)
		printCheckpointRemoteHint(target)
		return nil
	}
	stop(" done")

	fmt.Fprintf(os.Stderr, "[entire] Pushing %s to %s...", shortRef, displayTarget)
	stop = startProgressDots(os.Stderr)

	if err := tryPushRef(ctx, target, refName); err != nil {
		stop("")
		fmt.Fprintf(os.Stderr, "[entire] Warning: failed to push %s after sync: %v\n", shortRef, err)
		printCheckpointRemoteHint(target)
	} else {
		stop(" done")
	}

	return nil
}

// fetchAndMergeRef fetches a remote custom ref and merges it into the local ref.
// Uses the same tree-flattening merge as v1 (sharded paths are unique, so no conflicts).
func fetchAndMergeRef(ctx context.Context, target string, refName plumbing.ReferenceName) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// Fetch to a temp ref
	tmpRefSuffix := strings.ReplaceAll(string(refName), "/", "-")
	tmpRefName := plumbing.ReferenceName("refs/entire-fetch-tmp/" + tmpRefSuffix)
	refSpec := fmt.Sprintf("+%s:%s", refName, tmpRefName)

	fetchCmd := exec.CommandContext(ctx, "git", "fetch", target, refSpec)
	fetchCmd.Stdin = nil
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fetch failed: %s", output)
	}

	repo, err := OpenRepository(ctx)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Get local ref state
	localRef, err := repo.Reference(refName, true)
	if err != nil {
		return fmt.Errorf("failed to get local ref: %w", err)
	}
	localCommit, err := repo.CommitObject(localRef.Hash())
	if err != nil {
		return fmt.Errorf("failed to get local commit: %w", err)
	}
	localTree, err := localCommit.Tree()
	if err != nil {
		return fmt.Errorf("failed to get local tree: %w", err)
	}

	// Get fetched remote state
	remoteRef, err := repo.Reference(tmpRefName, true)
	if err != nil {
		return fmt.Errorf("failed to get remote ref: %w", err)
	}
	remoteCommit, err := repo.CommitObject(remoteRef.Hash())
	if err != nil {
		return fmt.Errorf("failed to get remote commit: %w", err)
	}
	remoteTree, err := remoteCommit.Tree()
	if err != nil {
		return fmt.Errorf("failed to get remote tree: %w", err)
	}

	// Flatten both trees and combine entries
	entries := make(map[string]object.TreeEntry)
	if err := checkpoint.FlattenTree(repo, localTree, "", entries); err != nil {
		return fmt.Errorf("failed to flatten local tree: %w", err)
	}
	if err := checkpoint.FlattenTree(repo, remoteTree, "", entries); err != nil {
		return fmt.Errorf("failed to flatten remote tree: %w", err)
	}

	// Build merged tree
	mergedTreeHash, err := checkpoint.BuildTreeFromEntries(repo, entries)
	if err != nil {
		return fmt.Errorf("failed to build merged tree: %w", err)
	}

	// Create merge commit
	mergeCommitHash, err := createMergeCommitCommon(repo, mergedTreeHash,
		[]plumbing.Hash{localRef.Hash(), remoteRef.Hash()},
		"Merge remote "+shortRefName(refName))
	if err != nil {
		return fmt.Errorf("failed to create merge commit: %w", err)
	}

	// Update local ref
	newRef := plumbing.NewHashReference(refName, mergeCommitHash)
	if err := repo.Storer.SetReference(newRef); err != nil {
		return fmt.Errorf("failed to update ref: %w", err)
	}

	// Clean up temp ref (best-effort)
	_ = repo.Storer.RemoveReference(tmpRefName) //nolint:errcheck // cleanup is best-effort

	return nil
}

// shortRefName returns a human-readable short form of a ref name for log output.
// e.g., "refs/entire/checkpoints/v2/main" -> "v2/main"
func shortRefName(refName plumbing.ReferenceName) string {
	const prefix = "refs/entire/checkpoints/"
	s := string(refName)
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return s
}
