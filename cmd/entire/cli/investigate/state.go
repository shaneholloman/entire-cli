package investigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/jsonutil"
	"github.com/entireio/cli/cmd/entire/cli/session"
)

// InvestigationsDirName is the directory name (under git common dir) where
// investigation runs persist their state.
const InvestigationsDirName = "entire-investigations"

// stateSubdirName is the subdirectory holding per-run state JSON files.
const stateSubdirName = "state"

// runIDPattern is the validation regex for investigation run IDs: exactly 12
// lowercase hex characters. Same shape as checkpoint IDs.
var runIDPattern = regexp.MustCompile(`^[a-f0-9]{12}$`)

// RunState is the persisted state of an investigation run, sufficient to
// resume after a crash, Ctrl+C, or `--continue`.
type RunState struct {
	RunID        string       `json:"run_id"`
	Topic        string       `json:"topic"`
	Agents       []string     `json:"agents"`
	MaxTurns     int          `json:"max_turns"`
	Quorum       int          `json:"quorum"`
	Round        int          `json:"round"`
	Turn         int          `json:"turn"`           // overall turn index across rounds
	NextAgentIdx int          `json:"next_agent_idx"` // index into Agents for the NEXT turn
	Stances      []TurnStance `json:"stances,omitempty"`
	FindingsDoc  string       `json:"findings_doc"` // absolute path
	TimelineDoc  string       `json:"timeline_doc"` // absolute path
	StartingSHA  string       `json:"starting_sha"`
	StartedAt    time.Time    `json:"started_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// TurnStance is one agent's recorded stance for a turn.
type TurnStance struct {
	Round           int    `json:"round"`
	Turn            int    `json:"turn"` // overall turn number
	Agent           string `json:"agent"`
	Stance          string `json:"stance"` // "approve" | "request-changes" | "abstain" | "unknown"
	PlanChanged     bool   `json:"plan_changed"`
	TimelineChanged bool   `json:"timeline_changed"`
	Note            string `json:"note,omitempty"`
}

// StateStore is the runs-state directory wrapper.
type StateStore struct {
	dir string
}

// NewStateStore creates a StateStore rooted at
// <git-common-dir>/entire-investigations/state. Resolves the common dir via
// session.GetGitCommonDir, so this requires a git repository context.
func NewStateStore(ctx context.Context) (*StateStore, error) {
	commonDir, err := session.GetGitCommonDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("get git common dir: %w", err)
	}
	return &StateStore{
		dir: filepath.Join(commonDir, InvestigationsDirName, stateSubdirName),
	}, nil
}

// NewStateStoreWithDir creates a StateStore rooted at dir. Useful for tests
// that don't want to depend on a real git repository.
func NewStateStoreWithDir(dir string) *StateStore {
	return &StateStore{dir: dir}
}

// Save writes the run state atomically (temp file + rename).
func (s *StateStore) Save(ctx context.Context, st *RunState) error {
	_ = ctx // Reserved for future use

	if err := validateRunID(st.RunID); err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}

	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return fmt.Errorf("create investigations state directory: %w", err)
	}

	data, err := jsonutil.MarshalIndentWithNewline(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run state: %w", err)
	}

	finalPath := s.runStatePath(st.RunID)
	fileName := st.RunID + ".json"

	// Use a unique temp file per save so concurrent writers can't corrupt
	// each other. Same pattern as session.StateStore.Save.
	tmpFile, err := os.CreateTemp(s.dir, fileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp run state file: %w", err)
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
		return fmt.Errorf("write run state: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close run state file: %w", err)
	}

	if err := os.Rename(tmpName, finalPath); err != nil {
		return fmt.Errorf("rename run state file: %w", err)
	}
	removeTmp = false
	return nil
}

// Load reads the run state for runID. Returns (nil, nil) when the file does
// not exist (treat as "no such run").
func (s *StateStore) Load(ctx context.Context, runID string) (*RunState, error) {
	_ = ctx // Reserved for future use

	if err := validateRunID(runID); err != nil {
		return nil, fmt.Errorf("invalid run ID: %w", err)
	}

	data, err := os.ReadFile(s.runStatePath(runID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil //nolint:nilnil // nil,nil indicates run not found
		}
		return nil, fmt.Errorf("read run state: %w", err)
	}

	var st RunState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("unmarshal run state: %w", err)
	}
	return &st, nil
}

// List returns all persisted run states. Returns nil (and no error) when the
// state directory does not exist.
func (s *StateStore) List(ctx context.Context) ([]*RunState, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read investigations state directory: %w", err)
	}

	var states []*RunState
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if strings.HasSuffix(name, ".tmp") {
			continue
		}
		runID := strings.TrimSuffix(name, ".json")
		if err := validateRunID(runID); err != nil {
			// Skip files that don't match the run-ID format — they are not
			// ours.
			continue
		}
		st, loadErr := s.Load(ctx, runID)
		if loadErr != nil {
			continue
		}
		if st == nil {
			continue
		}
		states = append(states, st)
	}
	return states, nil
}

// Clear removes the persisted state for runID. Missing files are treated as a
// successful clear (no-op).
func (s *StateStore) Clear(ctx context.Context, runID string) error {
	_ = ctx // Reserved for future use

	if err := validateRunID(runID); err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}

	if err := os.Remove(s.runStatePath(runID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove run state file: %w", err)
	}
	return nil
}

// runStatePath returns the on-disk path for runID's state file.
func (s *StateStore) runStatePath(runID string) string {
	return filepath.Join(s.dir, runID+".json")
}

// validateRunID enforces that runID is exactly 12 lowercase hex characters.
// Anything else is rejected to prevent path traversal and to keep the format
// stable for sharded directory layouts elsewhere in the codebase.
func validateRunID(runID string) error {
	if runID == "" {
		return errors.New("run ID cannot be empty")
	}
	if !runIDPattern.MatchString(runID) {
		return fmt.Errorf("invalid run ID %q: must be 12 lowercase hex characters", runID)
	}
	return nil
}
