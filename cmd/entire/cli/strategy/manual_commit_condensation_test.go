package strategy

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/entireio/cli/cmd/entire/cli/agent"

	// Register agents so GetByAgentType works in tests.
	_ "github.com/entireio/cli/cmd/entire/cli/agent/claudecode"
	_ "github.com/entireio/cli/cmd/entire/cli/agent/cursor"
)

func TestCalculateTokenUsage_CursorReturnsNil(t *testing.T) {
	t.Parallel()

	// Cursor transcripts don't contain token usage data, so CalculateTokenUsage
	// should return nil (not an empty struct) to signal "no data available".
	transcript := []byte(`{"role":"user","message":{"content":[{"type":"text","text":"hello"}]}}`)

	ag, err := agent.GetByAgentType(agent.AgentTypeCursor)
	if err != nil {
		t.Fatalf("GetByAgentType(Cursor) error: %v", err)
	}
	result := agent.CalculateTokenUsage(context.Background(), ag, transcript, 0, "")
	if result != nil {
		t.Errorf("CalculateTokenUsage(Cursor) = %+v, want nil", result)
	}
}

func TestCalculateTokenUsage_EmptyData(t *testing.T) {
	t.Parallel()

	ag, err := agent.GetByAgentType(agent.AgentTypeClaudeCode)
	if err != nil {
		t.Fatalf("GetByAgentType(ClaudeCode) error: %v", err)
	}
	result := agent.CalculateTokenUsage(context.Background(), ag, nil, 0, "")
	if result == nil {
		t.Fatal("CalculateTokenUsage(empty) = nil, want non-nil empty struct")
	}
	if result.InputTokens != 0 || result.OutputTokens != 0 {
		t.Errorf("expected zero tokens for empty data, got %+v", result)
	}
}

func TestCalculateTokenUsage_ClaudeCodeBasic(t *testing.T) {
	t.Parallel()

	// Claude Code JSONL: "usage" with "id" lives inside the "message" JSON object
	lines := []string{
		`{"type":"human","uuid":"u1","message":{"content":"hello"}}`,
		`{"type":"assistant","uuid":"u2","message":{"id":"msg_001","usage":{"input_tokens":10,"output_tokens":5}}}`,
	}
	data := []byte(strings.Join(lines, "\n") + "\n")

	ag, err := agent.GetByAgentType(agent.AgentTypeClaudeCode)
	if err != nil {
		t.Fatalf("GetByAgentType(ClaudeCode) error: %v", err)
	}
	result := agent.CalculateTokenUsage(context.Background(), ag, data, 0, "")
	if result == nil {
		t.Fatal("CalculateTokenUsage(ClaudeCode) = nil, want non-nil")
	}
	if result.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", result.OutputTokens)
	}
	if result.APICallCount != 1 {
		t.Errorf("APICallCount = %d, want 1", result.APICallCount)
	}
}

func TestCalculateTokenUsage_ClaudeCodeWithOffset(t *testing.T) {
	t.Parallel()

	// 4-line transcript; start at offset 2 to only count the second pair
	lines := []string{
		`{"type":"human","uuid":"u1","message":{"content":"first"}}`,
		`{"type":"assistant","uuid":"u2","message":{"id":"msg_001","usage":{"input_tokens":10,"output_tokens":5}}}`,
		`{"type":"human","uuid":"u3","message":{"content":"second"}}`,
		`{"type":"assistant","uuid":"u4","message":{"id":"msg_002","usage":{"input_tokens":20,"output_tokens":15}}}`,
	}
	data := []byte(strings.Join(lines, "\n") + "\n")

	ag, err := agent.GetByAgentType(agent.AgentTypeClaudeCode)
	if err != nil {
		t.Fatalf("GetByAgentType(ClaudeCode) error: %v", err)
	}
	full := agent.CalculateTokenUsage(context.Background(), ag, data, 0, "")
	sliced := agent.CalculateTokenUsage(context.Background(), ag, data, 2, "")

	if full == nil || sliced == nil {
		t.Fatal("expected non-nil results")
	}
	if full.OutputTokens != 20 {
		t.Errorf("full OutputTokens = %d, want 20", full.OutputTokens)
	}
	if sliced.OutputTokens != 15 {
		t.Errorf("sliced OutputTokens = %d, want 15", sliced.OutputTokens)
	}
}

// cursorSampleTranscript is a subset of a real Cursor session transcript.
// Cursor uses "role" (not "type") and wraps user text in <user_query> tags.
var cursorSampleTranscript = strings.Join([]string{
	`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\ncreate a file with contents 'a' and commit, then create another file with contents 'b' and commit\n</user_query>"}]}}`,
	`{"role":"assistant","message":{"content":[{"type":"text","text":"Creating two files (contents 'a' and 'b') and committing each."}]}}`,
	`{"role":"assistant","message":{"content":[{"type":"text","text":"Both files are tracked and the working tree is clean."}]}}`,
	`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\ncreate a file with contents 'c' and commit\n</user_query>"}]}}`,
	`{"role":"assistant","message":{"content":[{"type":"text","text":"Created c.txt with contents c and committed it."}]}}`,
	`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\nadd a file called bingo and commit\n</user_query>"}]}}`,
	`{"role":"assistant","message":{"content":[{"type":"text","text":"Created bingo and committed it."}]}}`,
}, "\n") + "\n"

func TestCountTranscriptItems_Cursor(t *testing.T) {
	t.Parallel()

	count := countTranscriptItems(agent.AgentTypeCursor, cursorSampleTranscript)
	if count != 7 {
		t.Errorf("countTranscriptItems(Cursor) = %d, want 7", count)
	}
}

func TestCountTranscriptItems_CursorEmpty(t *testing.T) {
	t.Parallel()

	count := countTranscriptItems(agent.AgentTypeCursor, "")
	if count != 0 {
		t.Errorf("countTranscriptItems(Cursor, empty) = %d, want 0", count)
	}
}

func TestExtractUserPrompts_Cursor(t *testing.T) {
	t.Parallel()

	// Cursor uses "role":"user" instead of "type":"human". extractUserPromptsFromLines
	// handles both via the "role" fallback.
	prompts := extractUserPrompts(agent.AgentTypeCursor, cursorSampleTranscript)
	if len(prompts) != 3 {
		t.Fatalf("extractUserPrompts(Cursor) returned %d prompts, want 3", len(prompts))
	}

	if !strings.Contains(prompts[0], "create a file with contents 'a'") {
		t.Errorf("prompt[0] = %q, expected to contain file creation request", prompts[0])
	}
	if !strings.Contains(prompts[2], "bingo") {
		t.Errorf("prompt[2] = %q, expected to contain 'bingo'", prompts[2])
	}

	// Verify <user_query> tags are stripped
	for i, p := range prompts {
		if strings.Contains(p, "<user_query>") || strings.Contains(p, "</user_query>") {
			t.Errorf("prompt[%d] still contains <user_query> tags: %q", i, p)
		}
	}
}

func TestExtractUserPrompts_CursorEmpty(t *testing.T) {
	t.Parallel()

	prompts := extractUserPrompts(agent.AgentTypeCursor, "")
	if len(prompts) != 0 {
		t.Errorf("extractUserPrompts(Cursor, empty) = %v, want empty", prompts)
	}
}

func TestCalculateTokenUsage_CursorRealTranscript(t *testing.T) {
	t.Parallel()

	// Even with a multi-line real transcript, Cursor should return nil
	ag, err := agent.GetByAgentType(agent.AgentTypeCursor)
	if err != nil {
		t.Fatalf("GetByAgentType(Cursor) error: %v", err)
	}
	result := agent.CalculateTokenUsage(context.Background(), ag, []byte(cursorSampleTranscript), 0, "")
	if result != nil {
		t.Errorf("CalculateTokenUsage(Cursor, real transcript) = %+v, want nil", result)
	}
}

func TestCalculateTokenUsage_CursorWithOffset(t *testing.T) {
	t.Parallel()

	// Offset should not matter — Cursor always returns nil
	ag, err := agent.GetByAgentType(agent.AgentTypeCursor)
	if err != nil {
		t.Fatalf("GetByAgentType(Cursor) error: %v", err)
	}
	result := agent.CalculateTokenUsage(context.Background(), ag, []byte(cursorSampleTranscript), 3, "")
	if result != nil {
		t.Errorf("CalculateTokenUsage(Cursor, offset=3) = %+v, want nil", result)
	}
}

func TestGenerateContextFromPrompts_CJKTruncation(t *testing.T) {
	t.Parallel()

	// 600 CJK characters exceeds the 500-rune truncation limit.
	prompt := strings.Repeat("あ", 600)

	result := generateContextFromPrompts([]string{prompt})

	if !utf8.Valid(result) {
		t.Error("generateContextFromPrompts produced invalid UTF-8 when truncating a CJK prompt")
	}

	resultStr := string(result)
	if !strings.Contains(resultStr, "...") {
		t.Error("expected truncated CJK prompt to contain '...' suffix")
	}
	// Should not contain more than 500 CJK characters
	if strings.Contains(resultStr, strings.Repeat("あ", 501)) {
		t.Error("CJK prompt was not truncated")
	}
}

func TestGenerateContextFromPrompts_EmojiTruncation(t *testing.T) {
	t.Parallel()

	// 600 emoji exceeds the 500-rune truncation limit.
	prompt := strings.Repeat("🎉", 600)

	result := generateContextFromPrompts([]string{prompt})

	if !utf8.Valid(result) {
		t.Error("generateContextFromPrompts produced invalid UTF-8 when truncating an emoji prompt")
	}

	resultStr := string(result)
	if !strings.Contains(resultStr, "...") {
		t.Error("expected truncated emoji prompt to contain '...' suffix")
	}
}

func TestGenerateContextFromPrompts_ASCIITruncation(t *testing.T) {
	t.Parallel()

	// Pure ASCII: should truncate at 500 runes with "..." suffix.
	prompt := strings.Repeat("a", 600)

	result := generateContextFromPrompts([]string{prompt})

	if !utf8.Valid(result) {
		t.Error("generateContextFromPrompts produced invalid UTF-8 when truncating an ASCII prompt")
	}

	resultStr := string(result)
	if !strings.Contains(resultStr, "...") {
		t.Error("expected truncated prompt to contain '...' suffix")
	}

	if strings.Contains(resultStr, strings.Repeat("a", 501)) {
		t.Error("prompt was not truncated")
	}
}

func TestGenerateContextFromPrompts_ShortCJKNotTruncated(t *testing.T) {
	t.Parallel()

	// 200 CJK characters is under the 500-rune limit, should not be truncated.
	prompt := strings.Repeat("あ", 200)

	result := generateContextFromPrompts([]string{prompt})

	if !utf8.Valid(result) {
		t.Error("generateContextFromPrompts produced invalid UTF-8")
	}

	resultStr := string(result)
	if strings.Contains(resultStr, "...") {
		t.Error("short CJK prompt should not be truncated")
	}
}
