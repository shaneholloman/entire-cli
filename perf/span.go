package perf

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/logging"
)

// Span tracks timing for an operation and its substeps.
// A Span is not safe for concurrent use from multiple goroutines.
type Span struct {
	name     string
	start    time.Time
	parent   *Span
	children []*Span
	duration time.Duration
	attrs    []slog.Attr
	ctx      context.Context
	ended    bool
	err      error
}

// Start begins a new span. If ctx already has a span, the new one becomes a child.
// Returns the updated context and the span. Call span.End() when the operation completes.
func Start(ctx context.Context, name string, attrs ...slog.Attr) (context.Context, *Span) {
	parent := spanFromContext(ctx)
	s := &Span{
		name:   name,
		start:  time.Now(),
		parent: parent,
		attrs:  attrs,
		ctx:    ctx,
	}
	if parent != nil {
		parent.children = append(parent.children, s)
	}
	return contextWithSpan(ctx, s), s
}

// RecordError marks the span as errored. Only the first non-nil error is stored;
// subsequent calls are no-ops. Call this before End() on error paths.
func (s *Span) RecordError(err error) {
	if err == nil || s.err != nil {
		return
	}
	s.err = err
}

// End completes the span. For root spans, emits a single DEBUG log line
// with the full timing tree. For child spans, records the duration only.
// Safe to call multiple times -- subsequent calls are no-ops.
func (s *Span) End() {
	if s.ended {
		return
	}
	s.ended = true
	s.duration = time.Since(s.start)

	// Only root spans emit log output
	if s.parent != nil {
		return
	}

	// Build log attributes: op, duration_ms, error flag, then child step durations.
	// Component is set via context so it appears exactly once in the log line.
	logCtx := logging.WithComponent(s.ctx, "perf")
	grandchildren := 0
	for _, c := range s.children {
		grandchildren += len(c.children)
	}
	attrs := make([]any, 0, 3+2*len(s.children)+2*grandchildren+len(s.attrs))
	attrs = append(attrs, slog.String("op", s.name))
	attrs = append(attrs, slog.Int64("duration_ms", s.duration.Milliseconds()))
	if s.err != nil {
		attrs = append(attrs, slog.Bool("error", true))
	}

	// Add child step durations (and error flags) as flat keys.
	// Disambiguate duplicate child names (from loops) with ~1, ~2, etc. suffixes
	// to prevent later values from overwriting earlier ones in JSON output.
	//
	// Group spans (children that have their own children) also emit grandchildren
	// with 0-based indexing: steps.<name>.0_ms, steps.<name>.1_ms, etc. The ~N
	// suffix avoids collisions with this .0, .1, ... iteration indexing.
	seen := make(map[string]int, len(s.children))
	for _, child := range s.children {
		// Auto-end children that were not explicitly ended
		if !child.ended {
			child.End()
		}
		stepKey := childStepKey(child.name, seen)
		key := fmt.Sprintf("steps.%s_ms", stepKey)
		attrs = append(attrs, slog.Int64(key, child.duration.Milliseconds()))
		if child.err != nil {
			errKey := fmt.Sprintf("steps.%s_err", stepKey)
			attrs = append(attrs, slog.Bool(errKey, true))
		}

		// Emit grandchildren (group iterations) with 0-based indexing
		for i, gc := range child.children {
			if !gc.ended {
				gc.End()
			}
			gcKey := fmt.Sprintf("steps.%s.%d_ms", stepKey, i)
			attrs = append(attrs, slog.Int64(gcKey, gc.duration.Milliseconds()))
			if gc.err != nil {
				gcErrKey := fmt.Sprintf("steps.%s.%d_err", stepKey, i)
				attrs = append(attrs, slog.Bool(gcErrKey, true))
			}
		}
	}

	// Add any extra attributes from Start()
	for _, a := range s.attrs {
		attrs = append(attrs, a)
	}

	logging.Debug(logCtx, "perf", attrs...)
}

// childStepKey returns a unique key for a child span name.
// First occurrence keeps the original name; subsequent get ~1, ~2, etc.
// Uses "~" separator to avoid collision with grandchild "." indexing
// (e.g. steps.foo.0_ms for loop iterations).
// The seen map is updated in place.
func childStepKey(name string, seen map[string]int) string {
	n := seen[name]
	seen[name] = n + 1
	if n == 0 {
		return name
	}
	return fmt.Sprintf("%s~%d", name, n)
}

// LoopSpan wraps a Span that groups loop iterations. Each call to Iteration
// creates a child span representing one pass through the loop.
//
// Usage:
//
//	ctx, loop := perf.StartLoop(ctx, "process_sessions")
//	for _, item := range items {
//	    iterCtx, iterSpan := loop.Iteration(ctx)
//	    doWork(iterCtx, item)
//	    iterSpan.End()
//	}
//	loop.End()
type LoopSpan struct {
	span *Span
}

// StartLoop begins a new loop span. The returned context contains the loop span
// and should be passed to Iteration. Call loop.End() after the loop completes.
func StartLoop(ctx context.Context, name string, attrs ...slog.Attr) (context.Context, *LoopSpan) {
	ctx, s := Start(ctx, name, attrs...)
	return ctx, &LoopSpan{span: s}
}

// Iteration creates a child span for a single loop iteration. The caller must
// call End() on the returned span when the iteration completes.
func (l *LoopSpan) Iteration(ctx context.Context) (context.Context, *Span) {
	return Start(ctx, l.span.name)
}

// End completes the loop span, auto-ending any unended iteration children first
// so their durations are captured at loop-end time rather than deferring to the
// root span's End() (which may run much later).
func (l *LoopSpan) End() {
	for _, child := range l.span.children {
		if !child.ended {
			child.End()
		}
	}
	l.span.End()
}
