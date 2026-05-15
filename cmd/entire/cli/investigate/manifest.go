package investigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/session"
)

const manifestsSubdirName = "manifests"

// LocalManifest is the persisted record of one `entire investigate` run for
// local findings browsing. Written to <git-common-dir>/entire-investigations/
// manifests/<timestamp>-<run-id>.json after each run terminates.
//
// The schema is intentionally narrower than RunState: this file is what
// `entire investigate --findings` reads to render the picker, so it carries
// only what a human (or `entire status`) needs to identify a past run, not the
// state needed to resume one.
type LocalManifest struct {
	// RunID is the 12-hex-char investigation run identifier.
	RunID string `json:"run_id"`

	// Topic is the human-readable subject of the investigation.
	Topic string `json:"topic"`

	// Slug is the filesystem-safe form of Topic, derived via SlugifyTopic.
	Slug string `json:"slug"`

	// StartingSHA is the git commit SHA that was HEAD when the
	// investigation started.
	StartingSHA string `json:"starting_sha"`

	// WorktreePath is the absolute path to the worktree the run executed
	// in. Empty when the run was not associated with a specific
	// worktree.
	WorktreePath string `json:"worktree_path,omitempty"`

	// FindingsDoc is the absolute path to the findings document the run
	// produced. May also be a repo-relative path when the caller chose
	// to record it that way. The on-disk file is removed for terminal
	// outcomes (Quorum/Stalled) once FindingsContent has been captured —
	// the path remains here for resumable runs (Paused/Cancelled) where
	// the file still lives in the per-run directory.
	FindingsDoc string `json:"findings_doc,omitempty"`

	// FindingsContent embeds the final findings.md content as of run
	// end. Populated on terminal outcomes (Quorum/Stalled) so the
	// findings survive after the per-run directory is cleaned up. Empty
	// on Paused/Cancelled — those runs are resumable and the file lives
	// on disk in the per-run directory at FindingsDoc.
	FindingsContent string `json:"findings_content,omitempty"`

	// Agents is the ordered list of agent names that participated in
	// the run.
	Agents []string `json:"agents"`

	// Outcome is the terminal outcome of the run. One of: "quorum",
	// "stalled", "paused", "cancelled".
	Outcome string `json:"outcome"`

	// StancesByAgent records the LAST stance each agent expressed in
	// the run, keyed by agent name. Empty when the run terminated
	// without any stances being recorded.
	StancesByAgent map[string]string `json:"stances_by_agent,omitempty"`

	// StartedAt is when the run was initiated.
	StartedAt time.Time `json:"started_at"`

	// EndedAt is when the run terminated.
	EndedAt time.Time `json:"ended_at"`
}

// LocalManifestStore wraps the directory that holds persisted LocalManifest
// JSON files for one repository.
type LocalManifestStore struct {
	dir string
}

// NewLocalManifestStore creates a LocalManifestStore rooted at
// <git-common-dir>/entire-investigations/manifests. Resolves the common dir
// via session.GetGitCommonDir, so this requires a git repository context.
func NewLocalManifestStore(ctx context.Context) (*LocalManifestStore, error) {
	commonDir, err := session.GetGitCommonDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("get git common dir: %w", err)
	}
	return &LocalManifestStore{
		dir: filepath.Join(commonDir, InvestigationsDirName, manifestsSubdirName),
	}, nil
}

// NewLocalManifestStoreWithDir creates a LocalManifestStore rooted at dir.
// Useful for tests that do not want to depend on a real git repository.
func NewLocalManifestStoreWithDir(dir string) *LocalManifestStore {
	return &LocalManifestStore{dir: dir}
}

// Write persists m to the manifests directory using a deterministic filename
// derived from m.StartedAt and m.RunID. Existing files are overwritten — the
// timestamp+run-id combination is unique by construction (each run has a fresh
// run ID and a different start time).
func (s *LocalManifestStore) Write(ctx context.Context, m LocalManifest) error {
	_ = ctx // Reserved for future use.

	if err := validateRunID(m.RunID); err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	if m.StartedAt.IsZero() {
		return errors.New("manifest StartedAt is required")
	}

	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return fmt.Errorf("create investigations manifests dir: %w", err)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	finalPath := filepath.Join(s.dir, manifestFilename(m))
	tmpFile, err := os.CreateTemp(s.dir, filepath.Base(finalPath)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp manifest file: %w", err)
	}
	tmpName := tmpFile.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write manifest: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close manifest file: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		return fmt.Errorf("rename manifest file: %w", err)
	}
	removeTmp = false
	return nil
}

// List returns every manifest in the store sorted newest first by StartedAt.
// A missing directory is treated as an empty list (nil, nil) — useful for
// callers that want to render `--findings` even when no investigation has
// ever been run in this repo.
func (s *LocalManifestStore) List(ctx context.Context) ([]LocalManifest, error) {
	_ = ctx // Reserved for future use.

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read investigations manifests dir: %w", err)
	}

	manifests := make([]LocalManifest, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		b, readErr := os.ReadFile(filepath.Join(s.dir, name)) //nolint:gosec // names from os.ReadDir(s.dir)
		if readErr != nil {
			return nil, fmt.Errorf("read manifest %s: %w", name, readErr)
		}
		var m LocalManifest
		if err := json.Unmarshal(b, &m); err != nil {
			// Skip files we can't decode — they may be stale or
			// from a future schema. Listing must keep working.
			continue
		}
		manifests = append(manifests, m)
	}
	sort.SliceStable(manifests, func(i, j int) bool {
		return manifests[i].StartedAt.After(manifests[j].StartedAt)
	})
	return manifests, nil
}

// FindByRunID returns the manifest whose RunID equals runID. The bool
// reports whether a match was found; when false the returned manifest is
// the zero value. Returns an error only when the underlying directory read
// itself fails.
func (s *LocalManifestStore) FindByRunID(ctx context.Context, runID string) (LocalManifest, bool, error) {
	if err := validateRunID(runID); err != nil {
		return LocalManifest{}, false, fmt.Errorf("invalid run ID: %w", err)
	}
	manifests, err := s.List(ctx)
	if err != nil {
		return LocalManifest{}, false, err
	}
	for _, m := range manifests {
		if m.RunID == runID {
			return m, true, nil
		}
	}
	return LocalManifest{}, false, nil
}

// manifestFilename returns the on-disk filename for m. Format:
// <timestamp>-<run-id>.json, where timestamp is the UTC StartedAt formatted
// as 20060102T150405. The timestamp prefix sorts manifests
// chronologically by directory listing, making `ls` output match List's
// newest-first ordering by simple reverse.
func manifestFilename(m LocalManifest) string {
	stamp := m.StartedAt.UTC().Format("20060102T150405")
	return stamp + "-" + m.RunID + ".json"
}

// PathFor returns the on-disk path of the manifest file for m. The path
// is computed deterministically from m.StartedAt + m.RunID (the same
// inputs Write uses to choose its destination), so callers can use this
// to delete a manifest record without scanning the directory.
func (s *LocalManifestStore) PathFor(m LocalManifest) string {
	return filepath.Join(s.dir, manifestFilename(m))
}
