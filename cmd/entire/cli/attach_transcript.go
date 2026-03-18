package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/agent/geminicli"
	"github.com/entireio/cli/cmd/entire/cli/transcript"
)

// transcriptMetadata holds metadata extracted from a single transcript parse pass.
type transcriptMetadata struct {
	FirstPrompt string
	TurnCount   int
	Model       string
}

// extractTranscriptMetadata parses transcript bytes once and extracts the first user prompt,
// user turn count, and model name. Supports both JSONL (Claude Code, Cursor, OpenCode) and
// Gemini JSON format.
func extractTranscriptMetadata(data []byte) transcriptMetadata {
	var meta transcriptMetadata

	// Try JSONL format first (Claude Code, Cursor, OpenCode, etc.)
	lines, err := transcript.ParseFromBytes(data)
	if err == nil {
		for _, line := range lines {
			if line.Type == transcript.TypeUser {
				if prompt := transcript.ExtractUserContent(line.Message); prompt != "" {
					meta.TurnCount++
					if meta.FirstPrompt == "" {
						meta.FirstPrompt = prompt
					}
				}
			}
			if line.Type == transcript.TypeAssistant && meta.Model == "" {
				var msg struct {
					Model string `json:"model"`
				}
				if json.Unmarshal(line.Message, &msg) == nil && msg.Model != "" {
					meta.Model = msg.Model
				}
			}
		}
		if meta.TurnCount > 0 || meta.Model != "" {
			return meta
		}
	}

	// Fallback: try Gemini JSON format {"messages": [...]}
	if prompts, gemErr := geminicli.ExtractAllUserPrompts(data); gemErr == nil && len(prompts) > 0 {
		meta.FirstPrompt = prompts[0]
		meta.TurnCount = len(prompts)
	}

	return meta
}

// estimateSessionDuration estimates session duration in milliseconds from JSONL transcript timestamps.
// The "timestamp" field is a top-level field in JSONL lines (alongside "type", "uuid", "message"),
// NOT inside the "message" object. We parse raw lines since transcript.Line doesn't capture it.
// Uses bufio.Scanner for memory efficiency with large transcripts.
// Returns 0 if timestamps are not available (e.g., Gemini transcripts).
func estimateSessionDuration(data []byte) int64 {
	type timestamped struct {
		Timestamp string `json:"timestamp"`
	}

	var first, last time.Time
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		rawLine := scanner.Bytes()
		if len(rawLine) == 0 {
			continue
		}
		var ts timestamped
		if err := json.Unmarshal(rawLine, &ts); err != nil || ts.Timestamp == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, ts.Timestamp)
		if err != nil {
			parsed, err = time.Parse(time.RFC3339, ts.Timestamp)
			if err != nil {
				continue
			}
		}
		if first.IsZero() {
			first = parsed
		}
		last = parsed
	}

	if first.IsZero() || last.IsZero() || !last.After(first) {
		return 0
	}
	return last.Sub(first).Milliseconds()
}
