package summarize

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"
)

type blockingGenerator struct {
	wait <-chan struct{}
}

func (g *blockingGenerator) Generate(ctx context.Context, _ Input) (*checkpoint.Summary, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-g.wait:
		return nil, nil
	}
}

func TestGenerateFromTranscript_Timeout(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	transcript := []byte(`{"type":"user","message":{"content":"Hello"}}`)
	start := time.Now()
	_, err := GenerateFromTranscript(ctx, transcript, nil, "", &blockingGenerator{wait: make(chan struct{})})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}
