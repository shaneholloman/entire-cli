package factoryaidroid

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/agent"
)

func TestNewFactoryAIDroidAgent(t *testing.T) {
	t.Parallel()
	ag := NewFactoryAIDroidAgent()
	if ag == nil {
		t.Fatal("NewFactoryAIDroidAgent() returned nil")
	}
	if _, ok := ag.(*FactoryAIDroidAgent); !ok {
		t.Fatal("NewFactoryAIDroidAgent() didn't return *FactoryAIDroidAgent")
	}
}

func TestName(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	if name := ag.Name(); name != agent.AgentNameFactoryAIDroid {
		t.Errorf("Name() = %q, want %q", name, agent.AgentNameFactoryAIDroid)
	}
}

func TestType(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	if tp := ag.Type(); tp != agent.AgentTypeFactoryAIDroid {
		t.Errorf("Type() = %q, want %q", tp, agent.AgentTypeFactoryAIDroid)
	}
}

func TestDescription(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	desc := ag.Description()
	if desc == "" {
		t.Error("Description() returned empty string")
	}
}

func TestProtectedDirs(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	dirs := ag.ProtectedDirs()
	if len(dirs) != 1 || dirs[0] != ".factory" {
		t.Errorf("ProtectedDirs() = %v, want [.factory]", dirs)
	}
}

func TestGetHookConfigPath(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	path := ag.GetHookConfigPath()
	if path != ".factory/settings.json" {
		t.Errorf("GetHookConfigPath() = %q, want .factory/settings.json", path)
	}
}

func TestSupportsHooks(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	if !ag.SupportsHooks() {
		t.Error("SupportsHooks() = false, want true")
	}
}

// TestDetectPresence uses t.Chdir so it cannot be parallel.
func TestDetectPresence(t *testing.T) {
	t.Run("factory directory exists", func(t *testing.T) {
		tempDir := t.TempDir()
		t.Chdir(tempDir)

		if err := os.Mkdir(".factory", 0o755); err != nil {
			t.Fatalf("failed to create .factory: %v", err)
		}

		ag := &FactoryAIDroidAgent{}
		present, err := ag.DetectPresence()
		if err != nil {
			t.Fatalf("DetectPresence() error = %v", err)
		}
		if !present {
			t.Error("DetectPresence() = false, want true")
		}
	})

	t.Run("no factory directory", func(t *testing.T) {
		tempDir := t.TempDir()
		t.Chdir(tempDir)

		ag := &FactoryAIDroidAgent{}
		present, err := ag.DetectPresence()
		if err != nil {
			t.Fatalf("DetectPresence() error = %v", err)
		}
		if present {
			t.Error("DetectPresence() = true, want false")
		}
	})
}

// --- Transcript tests ---

func TestReadTranscript(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "transcript.jsonl")
	content := `{"role":"user","content":"hello"}
{"role":"assistant","content":"hi"}`
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	ag := &FactoryAIDroidAgent{}
	data, err := ag.ReadTranscript(file)
	if err != nil {
		t.Fatalf("ReadTranscript() error = %v", err)
	}
	if string(data) != content {
		t.Errorf("ReadTranscript() = %q, want %q", string(data), content)
	}
}

func TestReadTranscript_MissingFile(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	_, err := ag.ReadTranscript("/nonexistent/path/transcript.jsonl")
	if err == nil {
		t.Error("ReadTranscript() should error on missing file")
	}
}

func TestChunkTranscript_SmallContent(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	content := []byte(`{"role":"user","content":"hello"}`)

	chunks, err := ag.ChunkTranscript(content, agent.MaxChunkSize)
	if err != nil {
		t.Fatalf("ChunkTranscript() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Errorf("Expected 1 chunk, got %d", len(chunks))
	}
}

func TestChunkTranscript_LargeContent(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}

	// Build multi-line JSONL that exceeds a small maxSize
	var lines []string
	for i := range 50 {
		lines = append(lines, fmt.Sprintf(`{"role":"user","content":"message %d %s"}`, i, strings.Repeat("x", 200)))
	}
	content := []byte(strings.Join(lines, "\n"))

	maxSize := 2000
	chunks, err := ag.ChunkTranscript(content, maxSize)
	if err != nil {
		t.Fatalf("ChunkTranscript() error = %v", err)
	}
	if len(chunks) < 2 {
		t.Errorf("Expected at least 2 chunks for large content, got %d", len(chunks))
	}

	// Verify each chunk is valid JSONL (each line is valid JSON)
	for i, chunk := range chunks {
		chunkLines := strings.Split(string(chunk), "\n")
		for j, line := range chunkLines {
			if line == "" {
				continue
			}
			if line[0] != '{' {
				t.Errorf("Chunk %d, line %d doesn't look like JSON: %q", i, j, line[:min(len(line), 40)])
			}
		}
	}
}

func TestChunkTranscript_RoundTrip(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}

	original := `{"role":"user","content":"hello"}
{"role":"assistant","content":"hi there"}
{"role":"user","content":"thanks"}`

	chunks, err := ag.ChunkTranscript([]byte(original), 60)
	if err != nil {
		t.Fatalf("ChunkTranscript() error = %v", err)
	}

	reassembled, err := ag.ReassembleTranscript(chunks)
	if err != nil {
		t.Fatalf("ReassembleTranscript() error = %v", err)
	}

	if string(reassembled) != original {
		t.Errorf("Round-trip mismatch:\n got: %q\nwant: %q", string(reassembled), original)
	}
}

func TestReassembleTranscript_SingleChunk(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}

	chunk := []byte(`{"role":"user","content":"hello"}`)
	result, err := ag.ReassembleTranscript([][]byte{chunk})
	if err != nil {
		t.Fatalf("ReassembleTranscript() error = %v", err)
	}
	if string(result) != string(chunk) {
		t.Errorf("ReassembleTranscript() = %q, want %q", string(result), string(chunk))
	}
}

func TestReassembleTranscript_MultipleChunks(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}

	chunk1 := []byte(`{"role":"user","content":"hello"}`)
	chunk2 := []byte(`{"role":"assistant","content":"hi"}`)

	result, err := ag.ReassembleTranscript([][]byte{chunk1, chunk2})
	if err != nil {
		t.Fatalf("ReassembleTranscript() error = %v", err)
	}

	expected := `{"role":"user","content":"hello"}
{"role":"assistant","content":"hi"}`
	if string(result) != expected {
		t.Errorf("ReassembleTranscript() = %q, want %q", string(result), expected)
	}
}

// --- ParseHookInput tests ---

func TestParseHookInput_Valid(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	input := `{"session_id":"sess-abc","transcript_path":"/tmp/transcript.jsonl"}`

	result, err := ag.ParseHookInput(agent.HookSessionStart, strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseHookInput() error = %v", err)
	}
	if result.SessionID != "sess-abc" {
		t.Errorf("SessionID = %q, want %q", result.SessionID, "sess-abc")
	}
	if result.SessionRef != "/tmp/transcript.jsonl" {
		t.Errorf("SessionRef = %q, want %q", result.SessionRef, "/tmp/transcript.jsonl")
	}
}

func TestParseHookInput_Empty(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	_, err := ag.ParseHookInput(agent.HookSessionStart, strings.NewReader(""))
	if err == nil {
		t.Error("ParseHookInput() should error on empty input")
	}
}

func TestParseHookInput_InvalidJSON(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	_, err := ag.ParseHookInput(agent.HookSessionStart, strings.NewReader("not json"))
	if err == nil {
		t.Error("ParseHookInput() should error on invalid JSON")
	}
}

// --- Session stub tests ---

func TestGetSessionID(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	input := &agent.HookInput{SessionID: "test-session-123"}

	id := ag.GetSessionID(input)
	if id != "test-session-123" {
		t.Errorf("GetSessionID() = %q, want %q", id, "test-session-123")
	}
}

func TestGetSessionDir(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	_, err := ag.GetSessionDir("/some/repo")
	if err == nil {
		t.Error("GetSessionDir() should return error (not implemented)")
	}
}

func TestReadSession(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	_, err := ag.ReadSession(&agent.HookInput{SessionID: "test"})
	if err == nil {
		t.Error("ReadSession() should return error (not implemented)")
	}
}

func TestWriteSession(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	err := ag.WriteSession(&agent.AgentSession{})
	if err == nil {
		t.Error("WriteSession() should return error (not implemented)")
	}
}

// --- Other methods ---

func TestResolveSessionFile(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	result := ag.ResolveSessionFile("/sessions", "abc-123")
	expected := filepath.Join("/sessions", "abc-123.jsonl")
	if result != expected {
		t.Errorf("ResolveSessionFile() = %q, want %q", result, expected)
	}
}

func TestFormatResumeCommand(t *testing.T) {
	t.Parallel()
	ag := &FactoryAIDroidAgent{}
	cmd := ag.FormatResumeCommand("sess-456")
	expected := "droid --session-id sess-456"
	if cmd != expected {
		t.Errorf("FormatResumeCommand() = %q, want %q", cmd, expected)
	}
}
