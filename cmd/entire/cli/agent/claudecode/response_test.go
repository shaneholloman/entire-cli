package claudecode

import (
	"strings"
	"testing"
)

func TestParseGenerateTextResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		stdout  string
		want    string
		wantErr string
	}{
		{
			name:   "legacy object result",
			stdout: `{"result":"hello"}`,
			want:   "hello",
		},
		{
			name:   "legacy object empty result",
			stdout: `{"result":""}`,
			want:   "",
		},
		{
			name:   "array result",
			stdout: `[{"type":"system"},{"type":"result","result":"hello"}]`,
			want:   "hello",
		},
		{
			name:   "array empty result",
			stdout: `[{"type":"system"},{"type":"result","result":""}]`,
			want:   "",
		},
		{
			name:    "missing result item",
			stdout:  `[{"type":"system"},{"type":"assistant","message":"working"}]`,
			wantErr: "missing result item",
		},
		{
			name:    "invalid json",
			stdout:  `not json`,
			wantErr: "unsupported Claude CLI JSON response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseGenerateTextResponse([]byte(tt.stdout))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseGenerateTextResponse() = %q, want %q", got, tt.want)
			}
		})
	}
}
