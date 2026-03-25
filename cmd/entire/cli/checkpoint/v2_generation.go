package checkpoint

import "time"

// DefaultMaxCheckpointsPerGeneration is the rotation threshold.
// When a generation reaches this many checkpoints, it is archived
// and a fresh /full/current is created.
const DefaultMaxCheckpointsPerGeneration = 100

// GenerationMetadata tracks the state of a /full/* generation.
// Stored at the tree root as generation.json and updated on every WriteCommitted.
// UpdateCommitted (stop-time finalization) does NOT update this file since it
// replaces an existing transcript rather than adding a new checkpoint.
type GenerationMetadata struct {
	// Generation is the sequence number (0 for /full/current, 1+ for archived).
	Generation int `json:"generation"`

	// CheckpointCount is the number of checkpoints in this generation.
	// Matches len(Checkpoints). Present per spec for quick reads by the
	// cleanup tool without parsing the full Checkpoints array.
	CheckpointCount int `json:"checkpoint_count"`

	// Checkpoints is the list of checkpoint IDs stored in this generation.
	// Used for finding which generation holds a specific checkpoint
	// without walking the tree.
	Checkpoints []string `json:"checkpoints"`

	// OldestCheckpointAt is the creation time of the earliest checkpoint.
	OldestCheckpointAt time.Time `json:"oldest_checkpoint_at"`

	// NewestCheckpointAt is the creation time of the most recent checkpoint.
	NewestCheckpointAt time.Time `json:"newest_checkpoint_at"`
}
