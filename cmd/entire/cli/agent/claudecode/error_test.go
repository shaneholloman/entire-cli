package claudecode

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClaudeError_ErrorIncludesKindAndMessage(t *testing.T) {
	t.Parallel()
	e := &ClaudeError{Kind: ClaudeErrorAuth, Message: "Invalid API key"}
	s := e.Error()
	if !strings.Contains(s, "auth") {
		t.Errorf("Error() = %q; want to contain kind 'auth'", s)
	}
	if !strings.Contains(s, "Invalid API key") {
		t.Errorf("Error() = %q; want to contain message", s)
	}
}

func TestClaudeError_UnwrapReturnsCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("underlying")
	e := &ClaudeError{Kind: ClaudeErrorUnknown, Cause: cause}
	if got := errors.Unwrap(e); !errors.Is(got, cause) {
		t.Errorf("Unwrap() = %v; want %v", got, cause)
	}
}

func TestClaudeError_UnwrapNilCause(t *testing.T) {
	t.Parallel()
	e := &ClaudeError{Kind: ClaudeErrorAuth}
	if got := errors.Unwrap(e); got != nil {
		t.Errorf("Unwrap() = %v; want nil", got)
	}
}

func TestClaudeError_ErrorEmptyMessageFallback(t *testing.T) {
	t.Parallel()
	e := &ClaudeError{Kind: ClaudeErrorRateLimit}
	s := e.Error()
	want := "claude CLI error (kind=rate_limit)"
	if s != want {
		t.Errorf("Error() = %q; want %q", s, want)
	}
}

func TestClaudeError_ErrorsAsIntegration(t *testing.T) {
	t.Parallel()
	cause := errors.New("timeout")
	wrapped := fmt.Errorf("operation failed: %w", &ClaudeError{
		Kind:    ClaudeErrorCLIMissing,
		Message: "claude not found",
		Cause:   cause,
	})

	var ce *ClaudeError
	if !errors.As(wrapped, &ce) {
		t.Fatal("errors.As did not find *ClaudeError in wrapped chain")
	}
	if ce.Kind != ClaudeErrorCLIMissing {
		t.Errorf("Kind = %q; want %q", ce.Kind, ClaudeErrorCLIMissing)
	}
	if !errors.Is(ce, cause) {
		t.Error("errors.Is did not find cause through ClaudeError.Unwrap()")
	}
}
