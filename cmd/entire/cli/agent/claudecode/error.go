package claudecode

import "fmt"

// ClaudeErrorKind classifies a typed Claude CLI error so callers can
// produce actionable user-facing messages without parsing strings.
type ClaudeErrorKind string

const (
	// ClaudeErrorAuth indicates an authentication or authorization failure
	// (HTTP 401/403 in the CLI envelope, or recognized stderr substring).
	ClaudeErrorAuth ClaudeErrorKind = "auth"
	// ClaudeErrorRateLimit indicates the request was rejected for rate-limit
	// or quota reasons (HTTP 429).
	ClaudeErrorRateLimit ClaudeErrorKind = "rate_limit"
	// ClaudeErrorConfig indicates a client-side request error other than
	// auth or rate-limit (e.g., HTTP 4xx for invalid model or malformed args).
	ClaudeErrorConfig ClaudeErrorKind = "config"
	// ClaudeErrorCLIMissing indicates the claude binary was not found on PATH.
	ClaudeErrorCLIMissing ClaudeErrorKind = "cli_missing"
	// ClaudeErrorUnknown is the catch-all for failures we cannot classify.
	ClaudeErrorUnknown ClaudeErrorKind = "unknown"
)

// ClaudeError is a typed error returned by ClaudeCodeAgent's text generation
// methods. Callers can use errors.As to recover the kind and produce
// user-facing messages.
type ClaudeError struct {
	Kind      ClaudeErrorKind
	Message   string // user-safe text extracted from the CLI envelope or stderr
	APIStatus int    // HTTP status from the envelope; 0 if not applicable
	ExitCode  int    // subprocess exit code; 0 if not applicable
	Cause     error
}

func (e *ClaudeError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("claude CLI error (kind=%s)", e.Kind)
	}
	return fmt.Sprintf("claude CLI error (kind=%s): %s", e.Kind, e.Message)
}

func (e *ClaudeError) Unwrap() error { return e.Cause }
