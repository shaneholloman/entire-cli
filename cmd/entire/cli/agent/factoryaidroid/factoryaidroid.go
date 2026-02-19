// Package factoryaidroid implements the Agent interface for Factory AI Droid.
package factoryaidroid

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/entireio/cli/cmd/entire/cli/agent"
	"github.com/entireio/cli/cmd/entire/cli/paths"
)

//nolint:gochecknoinits // Agent self-registration is the intended pattern
func init() {
	agent.Register(agent.AgentNameFactoryAIDroid, NewFactoryAIDroidAgent)
}

// FactoryAIDroidAgent implements the agent.Agent interface for Factory AI Droid.
//
//nolint:revive // FactoryAIDroidAgent is clearer than Agent in this context
type FactoryAIDroidAgent struct{}

// NewFactoryAIDroidAgent creates a new Factory AI Droid agent instance.
func NewFactoryAIDroidAgent() agent.Agent {
	return &FactoryAIDroidAgent{}
}

// Name returns the agent registry key.
func (f *FactoryAIDroidAgent) Name() agent.AgentName { return agent.AgentNameFactoryAIDroid }

// Type returns the agent type identifier.
func (f *FactoryAIDroidAgent) Type() agent.AgentType { return agent.AgentTypeFactoryAIDroid }

// Description returns a human-readable description.
func (f *FactoryAIDroidAgent) Description() string {
	return "Factory AI Droid - agent-native development platform"
}

// IsPreview returns true as Factory AI Droid integration is in preview.
func (f *FactoryAIDroidAgent) IsPreview() bool { return true }

// ProtectedDirs returns directories that Factory AI Droid uses for config/state.
func (f *FactoryAIDroidAgent) ProtectedDirs() []string { return []string{".factory"} }

// DetectPresence checks if Factory AI Droid is configured in the repository.
func (f *FactoryAIDroidAgent) DetectPresence() (bool, error) {
	repoRoot, err := paths.RepoRoot()
	if err != nil {
		repoRoot = "."
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".factory")); err == nil {
		return true, nil
	}
	return false, nil
}

// ReadTranscript reads the raw JSONL transcript bytes for a session.
func (f *FactoryAIDroidAgent) ReadTranscript(sessionRef string) ([]byte, error) {
	data, err := os.ReadFile(sessionRef) //nolint:gosec // Path comes from agent hook input
	if err != nil {
		return nil, fmt.Errorf("failed to read transcript: %w", err)
	}
	return data, nil
}

// ChunkTranscript splits a JSONL transcript at line boundaries.
func (f *FactoryAIDroidAgent) ChunkTranscript(content []byte, maxSize int) ([][]byte, error) {
	chunks, err := agent.ChunkJSONL(content, maxSize)
	if err != nil {
		return nil, fmt.Errorf("failed to chunk transcript: %w", err)
	}
	return chunks, nil
}

// ReassembleTranscript concatenates JSONL chunks with newlines.
func (f *FactoryAIDroidAgent) ReassembleTranscript(chunks [][]byte) ([]byte, error) {
	return agent.ReassembleJSONL(chunks), nil
}

// GetHookConfigPath returns the path to Factory AI Droid's hook config file.
func (f *FactoryAIDroidAgent) GetHookConfigPath() string { return ".factory/settings.json" }

// SupportsHooks returns true as Factory AI Droid supports lifecycle hooks.
func (f *FactoryAIDroidAgent) SupportsHooks() bool { return true }

// ParseHookInput parses Factory AI Droid hook input from stdin.
func (f *FactoryAIDroidAgent) ParseHookInput(_ agent.HookType, r io.Reader) (*agent.HookInput, error) {
	raw, err := agent.ReadAndParseHookInput[sessionInfoRaw](r)
	if err != nil {
		return nil, err
	}
	return &agent.HookInput{
		SessionID:  raw.SessionID,
		SessionRef: raw.TranscriptPath,
	}, nil
}

// GetSessionID extracts the session ID from hook input.
func (f *FactoryAIDroidAgent) GetSessionID(input *agent.HookInput) string { return input.SessionID }

// GetSessionDir is not implemented for Factory AI Droid.
func (f *FactoryAIDroidAgent) GetSessionDir(_ string) (string, error) {
	return "", errors.New("not implemented")
}

// ResolveSessionFile returns the path to a Factory AI Droid session file.
func (f *FactoryAIDroidAgent) ResolveSessionFile(sessionDir, agentSessionID string) string {
	return filepath.Join(sessionDir, agentSessionID+".jsonl")
}

// ReadSession is not implemented for Factory AI Droid.
func (f *FactoryAIDroidAgent) ReadSession(_ *agent.HookInput) (*agent.AgentSession, error) {
	return nil, errors.New("not implemented")
}

// WriteSession is not implemented for Factory AI Droid.
func (f *FactoryAIDroidAgent) WriteSession(_ *agent.AgentSession) error {
	return errors.New("not implemented")
}

// FormatResumeCommand returns the command to resume a Factory AI Droid session.
func (f *FactoryAIDroidAgent) FormatResumeCommand(sessionID string) string {
	return "droid --session-id " + sessionID
}
