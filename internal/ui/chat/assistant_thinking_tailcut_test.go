package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestThinkingCollapsed_LongThinkingTailSliced asserts the collapsed
// view of a very long thinking block renders only a bounded tail
// fragment: the no-count hint is shown, the last source paragraph is
// visible, and early paragraphs are elided at the SOURCE level (the
// full document never reaches glamour).
func TestThinkingCollapsed_LongThinkingTailSliced(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := thinkingMessageWithLines("sliced", 5000)
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	// Unique odd width: the glamour renderer cache is memoized per
	// width and is not safe for concurrent use by parallel tests.
	const width = 105
	_ = item.RawRender(width)
	plain := ansi.Strip(item.thinkingSec.out)

	require.Contains(t, plain, "earlier lines hidden",
		"a tail-sliced collapsed render must show the no-count truncation hint")
	require.Contains(t, plain, "ln5000",
		"collapsed render must include the LAST source paragraph")
	require.NotContains(t, plain, "ln1 ",
		"collapsed render of a long block must elide early source paragraphs")

	require.Empty(t, item.streamingThinking.stablePrefix,
		"the collapsed tail-slice path must not drive the streaming prefix cache")
}

// TestThinkingCollapsed_ShortThinkingKeepsCountHint asserts that a
// thinking block too short to tail-slice still renders in full and
// keeps the exact "(%d lines hidden)" hint.
func TestThinkingCollapsed_ShortThinkingKeepsCountHint(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	// 15 paragraphs: under the tail-slice minimum but over the
	// collapsed window cap after rendering.
	const paragraphs = 15
	require.Less(t, paragraphs, thinkingTailCutMinNonBlank,
		"test relies on the source being too short to tail-slice")
	msg := thinkingMessageWithLines("short-count", paragraphs)
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	const width = 107
	_ = item.RawRender(width)
	plain := ansi.Strip(item.thinkingSec.out)

	require.NotContains(t, plain, "earlier lines hidden",
		"a fully rendered collapsed block must show the counted hint, not the no-count one")
	require.Regexp(t, `\(\d+ lines hidden\)`, plain,
		"a fully rendered collapsed block must show the exact hidden-line count")
}

// TestThinkingCollapsed_NoSafeCutFallsBack asserts that a thinking
// block with no blank-line boundary at all (one giant paragraph)
// falls back to the full-render path and still shows the counted
// truncation hint.
func TestThinkingCollapsed_NoSafeCutFallsBack(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:   "no-cut",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{
				Thinking:  strings.Repeat("word ", 400),
				StartedAt: testStartedAt,
			},
		},
	}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	const width = 109
	_ = item.RawRender(width)
	plain := ansi.Strip(item.thinkingSec.out)

	require.NotContains(t, plain, "earlier lines hidden",
		"the fallback full render must show the counted hint, not the no-count one")
	require.Regexp(t, `\(\d+ lines hidden\)`, plain,
		"the fallback full render knows the total and must show the exact count")
}

// TestThinkingCollapsed_ListInPrefixStillSlices locks in the core
// regression fix: a list anywhere earlier in the thinking source must
// NOT prevent tail slicing. The streaming prefix cache's B1 hazard
// rule ("any list marker anywhere in the prefix rejects the
// boundary") froze the cached prefix and forced a growing
// full-document glamour render on every streaming flush; the
// collapsed tail cut deliberately does not inherit prefix hazards.
func TestThinkingCollapsed_ListInPrefixStillSlices(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString("Let me break this down:\n\n1. First step\n2. Second step\n3. Third step")
	for i := 1; i <= 40; i++ {
		b.WriteString("\n\npara")
		b.WriteString(itoa(i))
	}
	msg := &message.Message{
		ID:   "list-prefix",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: b.String(), StartedAt: testStartedAt},
		},
	}
	sty := styles.CharmtonePantera()
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	const width = 111
	_ = item.RawRender(width)
	plain := ansi.Strip(item.thinkingSec.out)

	require.Contains(t, plain, "earlier lines hidden",
		"a list early in the thinking must not force the full-render path")
	require.Contains(t, plain, "para40",
		"the tail fragment must include the latest source paragraph")
	require.NotContains(t, plain, "First step",
		"the tail fragment must elide the early list")
}

// TestThinkingCollapsed_OpenFenceTail asserts that when the thinking
// ends inside an unclosed fenced code block, the tail cut lands
// BEFORE the fence opener (never inside the fence), so the collapsed
// render shows highlighted code rather than prose with a raw fence
// delimiter.
func TestThinkingCollapsed_OpenFenceTail(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	for i := 1; i <= 30; i++ {
		b.WriteString("para")
		b.WriteString(itoa(i))
		b.WriteString("\n\n")
	}
	b.WriteString("```go\n")
	for i := 1; i <= 20; i++ {
		b.WriteString("line")
		b.WriteString(itoa(i))
		b.WriteString("()\n")
	}
	msg := &message.Message{
		ID:   "fence-tail",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: b.String(), StartedAt: testStartedAt},
		},
	}
	sty := styles.CharmtonePantera()
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	const width = 113
	_ = item.RawRender(width)
	plain := ansi.Strip(item.thinkingSec.out)

	require.NotContains(t, plain, "```",
		"collapsed render must not leak a raw fence delimiter")
	require.Contains(t, plain, "line20()",
		"collapsed render must include the tail of the code block")
}

// TestThinkingCollapsed_SliceThenExpandRendersFull drives the state
// transition that matters for correctness: a collapsed render served
// from a tail fragment, followed by the user expanding the block.
// The expanded views must re-render the FULL source (the collapsed
// fragment render is not reusable) and must match a fresh item that
// was never tail-sliced.
func TestThinkingCollapsed_SliceThenExpandRendersFull(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	const total = 1500
	msg := thinkingMessageWithLines("slice-expand", total)
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	const width = 115

	// Collapsed: tail-sliced render.
	_ = item.RawRender(width)
	require.Contains(t, ansi.Strip(item.thinkingSec.out), "earlier lines hidden",
		"setup: collapsed render of a long block must be tail-sliced")

	// Tail-window: full source rendered, windowed to the cap.
	require.True(t, item.ToggleExpanded())
	require.Equal(t, thinkingTailWindow, item.thinkingViewMode)
	_ = item.RawRender(width)
	tailPlain := ansi.Strip(item.thinkingSec.out)
	require.Contains(t, tailPlain, "earlier lines hidden",
		"tail-window must show its affordance")
	require.Contains(t, tailPlain, "ln1500",
		"tail-window must include the last source paragraph")

	// Full expansion: everything present.
	require.True(t, item.ToggleExpanded())
	require.Equal(t, thinkingFullExpanded, item.thinkingViewMode)
	_ = item.RawRender(width)
	fullPlain := ansi.Strip(item.thinkingSec.out)
	require.Contains(t, fullPlain, "ln1 ",
		"full expansion after a tail-sliced collapsed render must include the first source paragraph")
	require.Contains(t, fullPlain, "ln1500 ",
		"full expansion after a tail-sliced collapsed render must include the last source paragraph")
}

// -----------------------------------------------------------------------
// findThinkingTailCut unit tests.
// -----------------------------------------------------------------------

func TestFindThinkingTailCut_TableDriven(t *testing.T) {
	t.Parallel()

	paragraphs := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			b.WriteString("para")
			b.WriteString(itoa(i))
			if i < n {
				b.WriteString("\n\n")
			}
		}
		return b.String()
	}

	t.Run("basic cut lands on a blank-line boundary", func(t *testing.T) {
		t.Parallel()
		doc := paragraphs(50)
		cut, ok := findThinkingTailCut(doc, 20, 400, 1<<20)
		require.True(t, ok)
		require.Positive(t, cut)
		require.Equal(t, byte('\n'), doc[cut-1], "cut must sit right after a newline")
		fragment := doc[cut:]
		nonBlank := 0
		for line := range strings.Lines(fragment) {
			if strings.TrimSpace(line) != "" {
				nonBlank++
			}
		}
		require.GreaterOrEqual(t, nonBlank, 20,
			"fragment must cover at least minNonBlank non-blank lines")
		require.Contains(t, fragment, "para50", "fragment must reach the end of the source")
	})

	t.Run("cut is as small as possible", func(t *testing.T) {
		t.Parallel()
		doc := paragraphs(50)
		cut, ok := findThinkingTailCut(doc, 20, 400, 1<<20)
		require.True(t, ok)
		// The fragment should hold 20 or 21 non-blank lines (the
		// walk stops at the first boundary past the minimum), not
		// the whole document.
		nonBlank := 0
		for line := range strings.Lines(doc[cut:]) {
			if strings.TrimSpace(line) != "" {
				nonBlank++
			}
		}
		require.LessOrEqual(t, nonBlank, 21,
			"fragment should be the smallest one satisfying minNonBlank; got %d", nonBlank)
	})

	t.Run("no blank lines: not ok", func(t *testing.T) {
		t.Parallel()
		doc := strings.Repeat("word ", 400)
		_, ok := findThinkingTailCut(doc, 20, 400, 1<<20)
		require.False(t, ok)
	})

	t.Run("document shorter than minNonBlank: not ok", func(t *testing.T) {
		t.Parallel()
		_, ok := findThinkingTailCut(paragraphs(10), 20, 400, 1<<20)
		require.False(t, ok)
	})

	t.Run("scan budget exceeded: not ok", func(t *testing.T) {
		t.Parallel()
		// 10 scanned lines can never hold the required 20 non-blank
		// lines, so the scan must give up.
		_, ok := findThinkingTailCut(paragraphs(2000), 20, 10, 1<<20)
		require.False(t, ok,
			"a scan capped at 10 lines must give up when minNonBlank is 20")
	})

	t.Run("byte budget exceeded: not ok", func(t *testing.T) {
		t.Parallel()
		_, ok := findThinkingTailCut(paragraphs(2000), 20, 400, 100)
		require.False(t, ok,
			"a byte budget of 100 must give up before reaching minNonBlank")
	})

	t.Run("unclosed fence at end: cut lands before the fence", func(t *testing.T) {
		t.Parallel()
		doc := paragraphs(30) + "\n\n```go\nfoo()\nbar()\n"
		cut, ok := findThinkingTailCut(doc, 5, 400, 1<<20)
		require.True(t, ok)
		fragment := doc[cut:]
		require.Contains(t, fragment, "```go",
			"the fragment must include the whole open fence, never start inside it")
		require.True(t, strings.HasPrefix(fragment, "para") || strings.HasPrefix(fragment, "```"),
			"the cut must land at a construct boundary, got prefix %.20q", fragment)
	})

	t.Run("list markers in prefix do not block the cut", func(t *testing.T) {
		t.Parallel()
		doc := "1. one\n2. two\n\n" + paragraphs(30)
		_, ok := findThinkingTailCut(doc, 20, 400, 1<<20)
		require.True(t, ok,
			"B1-style anywhere-in-prefix hazards must not block a tail cut")
	})

	t.Run("loose-list continuation is not torn", func(t *testing.T) {
		t.Parallel()
		// A candidate boundary between the item and its indented
		// continuation must be rejected; the cut lands earlier.
		doc := paragraphs(30) + "\n\n- item one\n\n  continuation of item one\n\n" + paragraphs(10)
		cut, ok := findThinkingTailCut(doc, 5, 400, 1<<20)
		require.True(t, ok)
		fragment := doc[cut:]
		firstNB := ""
		for line := range strings.Lines(fragment) {
			if strings.TrimSpace(line) != "" {
				firstNB = line
				break
			}
		}
		require.False(t, strings.HasPrefix(firstNB, " ") || strings.HasPrefix(firstNB, "\t"),
			"fragment must not open on an indented continuation, got %q", firstNB)
	})
}
