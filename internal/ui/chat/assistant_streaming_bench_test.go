package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// streamingThinkingSource builds a realistic long reasoning trace:
// short paragraphs separated by blank lines, with a numbered list
// early on (anywhere-in-prefix hazard for the streaming cache) and
// occasional fenced code blocks.
func streamingThinkingSource(paragraphs int) string {
	var b strings.Builder
	for i := 1; i <= paragraphs; i++ {
		switch {
		case i == 5:
			b.WriteString("Let me break this down:\n\n1. First, check the config file\n2. Then, verify the database connection\n3. Finally, run the migrations")
		case i%50 == 0:
			b.WriteString("```go\nfunc example() {\n\tfmt.Println(\"hi\")\n}\n```")
		default:
			b.WriteString(fmt.Sprintf("Paragraph %d of the reasoning trace, considering some aspect of the problem at hand.", i))
		}
		if i < paragraphs {
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

// BenchmarkStreamingThinking simulates the debounced streaming loop
// for a long reasoning trace in the default collapsed view: the
// thinking text grows one paragraph per flush and each flush
// triggers a SetMessage + RawRender, exactly as the UI does on each
// pubsub update.
func BenchmarkStreamingThinking(b *testing.B) {
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:   "bench",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{StartedAt: testStartedAt},
		},
	}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	const width = 90

	b.ResetTimer()
	for chunk := 10; chunk <= 2000; chunk += 10 {
		item.SetMessage(&message.Message{
			ID:   "bench",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: streamingThinkingSource(chunk), StartedAt: testStartedAt},
			},
		})
		_ = item.RawRender(width)
	}
}

// BenchmarkStreamingThinkingSteadyState measures the cost of ONE
// flush at a large, fixed thinking size — the per-frame cost the UI
// pays on every debounced update while the reasoning trace is long.
// Before the collapsed tail-slice fix this grew linearly with the
// document (~49ms per flush at this size); afterwards it is bounded
// by the tail fragment (~0.3ms).
func BenchmarkStreamingThinkingSteadyState(b *testing.B) {
	sty := styles.CharmtonePantera()
	doc := streamingThinkingSource(2000) // ~150KB reasoning trace
	msg := &message.Message{
		ID:   "bench-ss",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: doc, StartedAt: testStartedAt},
		},
	}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	const width = 90
	_ = item.RawRender(width) // warm caches

	b.ResetTimer()
	for b.Loop() {
		// Append one more paragraph, as a streaming flush would.
		item.SetMessage(&message.Message{
			ID:   "bench-ss",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: doc + "\n\nextra", StartedAt: testStartedAt},
			},
		})
		_ = item.RawRender(width)
	}
}

// BenchmarkFindSafeMarkdownBoundary measures the O(n) single-pass
// boundary scan on a long document.
func BenchmarkFindSafeMarkdownBoundary(b *testing.B) {
	doc := streamingThinkingSource(2000)
	b.ResetTimer()
	for b.Loop() {
		_ = findSafeMarkdownBoundary(doc)
	}
}
