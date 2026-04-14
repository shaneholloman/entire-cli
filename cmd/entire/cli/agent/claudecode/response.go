package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
)

type responseEnvelope struct {
	Type   string  `json:"type"`
	Result *string `json:"result"`
}

// parseGenerateTextResponse extracts the raw text payload from Claude CLI JSON output.
// Claude may return either a legacy single object or a newer array of events.
func parseGenerateTextResponse(stdout []byte) (string, error) {
	var response responseEnvelope
	if err := json.Unmarshal(stdout, &response); err == nil && response.Result != nil {
		return *response.Result, nil
	}

	var responses []responseEnvelope
	if err := json.Unmarshal(stdout, &responses); err != nil {
		return "", fmt.Errorf("unsupported Claude CLI JSON response: %w", err)
	}

	for i := len(responses) - 1; i >= 0; i-- {
		if responses[i].Type == "result" && responses[i].Result != nil {
			return *responses[i].Result, nil
		}
	}

	return "", errors.New("unsupported Claude CLI JSON response: missing result item")
}
