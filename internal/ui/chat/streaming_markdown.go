package chat

import (
	"strings"

	"charm.land/glamour/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
)

// streamingMarkdown caches a "stable prefix" glamour render so each
// streaming flush only re-renders the trailing portion of the
// document. F8 of docs/notes/2026-05-12-chat-rendering-perf.md.
//
// The boundary between "stable" and "trailing" is detected by
// [findSafeMarkdownBoundary]: a position immediately after a blank
// line at which we can prove no markdown construct is open
// (fenced code block, list, table, block quote, setext header).
//
// Two renders concatenated are NOT generally equal to a single
// render of the whole document — glamour's wrap state is reset
// between calls. The boundary check is therefore deliberately
// conservative; whenever it has the slightest doubt the call
// falls back to a full render and the cache is left untouched.
//
// Invariants:
//
//   - stablePrefix is always a literal byte prefix of the most
//     recently rendered content. If a new content does not have
//     stablePrefix as its prefix the cache is dropped.
//   - stablePrefixRender is the glamour render of stablePrefix
//     alone, with surrounding whitespace trimmed for clean
//     concatenation.
//   - width is the glamour wrap width that produced
//     stablePrefixRender. A width change drops the cache.
type streamingMarkdown struct {
	width              int
	stablePrefix       string
	stablePrefixRender string
}

// Reset drops every cached field. After Reset the next Render call
// is guaranteed to be a full render.
func (s *streamingMarkdown) Reset() {
	s.width = 0
	s.stablePrefix = ""
	s.stablePrefixRender = ""
}

// Render returns the glamour render of content at the given width,
// reusing the cached stable-prefix render when it is safe to do so.
// On any uncertainty the call falls back to a full render via
// renderer and leaves the cache untouched (or drops it).
//
// The returned string has its trailing newline trimmed to match
// the existing renderMarkdown contract on AssistantMessageItem.
//
// Concurrency: glamour's Render is stateful and not safe for
// concurrent invocation on a shared renderer. Crush's TUI is
// single-threaded so production never contends, but parallel
// callers (most notably the test suite) must serialize. We hold
// [common.LockMarkdownRenderer] for the entire prefix +
// trailing render sequence so other goroutines cannot interleave
// their own Render calls and corrupt goldmark's BlockStack.
func (s *streamingMarkdown) Render(content string, width int, renderer *glamour.TermRenderer) string {
	mu := common.LockMarkdownRenderer(renderer)
	mu.Lock()
	defer mu.Unlock()
	full := func() string {
		out, err := renderer.Render(content)
		if err != nil {
			return content
		}
		return strings.TrimSuffix(out, "\n")
	}

	// Width change OR content not a prefix-extension: drop cache,
	// full render, optionally try to seed a fresh boundary on this
	// call (step "f" in the design note).
	if width != s.width || !strings.HasPrefix(content, s.stablePrefix) {
		s.Reset()
		s.width = width
		out := full()
		s.tryAdvanceFromEmpty(content, width, renderer)
		return out
	}

	boundary := findSafeMarkdownBoundary(content)
	if boundary < 0 {
		// No safe boundary anywhere yet. Full render; do not
		// modify the cache (a future flush may find one).
		return full()
	}

	if boundary <= len(s.stablePrefix) {
		// Cached prefix already covers an at-least-as-late
		// boundary. Render the trailing partial fresh and glue.
		trail := content[len(s.stablePrefix):]
		return glueRenders(s.stablePrefixRender, s.renderTrailing(trail, renderer))
	}

	// boundary > len(stablePrefix): we have a NEW chunk of safe
	// content. Render the new chunk, append to stablePrefixRender,
	// promote the boundary, then render the remaining trail.
	newChunk := content[len(s.stablePrefix):boundary]
	newChunkRender := s.renderTrailing(newChunk, renderer)
	s.stablePrefixRender = glueRenders(s.stablePrefixRender, newChunkRender)
	s.stablePrefix = content[:boundary]

	trail := content[boundary:]
	if trail == "" {
		// boundary == len(content): no trailing content. Returning
		// the cached prefix render directly is correct.
		return s.stablePrefixRender
	}
	return glueRenders(s.stablePrefixRender, s.renderTrailing(trail, renderer))
}

// tryAdvanceFromEmpty seeds the cache from a fresh state. We've
// already paid the cost of a full render of `content`; if there is
// a safe boundary inside it, render the prefix once more (cheap
// relative to the full render we just did) and cache it so the
// next flush can avoid the full work.
//
// This is the optional optimisation step "f" from the design
// note. We render the prefix separately rather than try to
// recover it from the full render output because two renders
// concatenated ≠ a single render of the whole, and we prefer the
// cached prefix render to be byte-for-byte what we'd produce on a
// future cached call.
func (s *streamingMarkdown) tryAdvanceFromEmpty(content string, width int, renderer *glamour.TermRenderer) {
	boundary := findSafeMarkdownBoundary(content)
	if boundary <= 0 {
		return
	}
	prefix := content[:boundary]
	out, err := renderer.Render(prefix)
	if err != nil {
		return
	}
	s.stablePrefix = prefix
	s.stablePrefixRender = trimGlamourMargins(out)
	s.width = width
}

// renderTrailing renders a trailing partial as a fresh glamour
// document and trims the surrounding whitespace so it can be
// concatenated to a cached prefix render without doubled blank
// lines.
func (s *streamingMarkdown) renderTrailing(text string, renderer *glamour.TermRenderer) string {
	if text == "" {
		return ""
	}
	out, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return trimGlamourMargins(out)
}

// glueRenders concatenates two glamour-rendered fragments with a
// single blank line separator. Glamour outputs typically carry
// their own surrounding margins; trimming on both sides and
// gluing with "\n\n" prevents the visible double-margin seam.
//
// Empty fragments are tolerated so the same helper works for the
// "boundary == len(content)" path where there is no trailing
// segment.
func glueRenders(prefix, trail string) string {
	prefix = trimGlamourMargins(prefix)
	trail = trimGlamourMargins(trail)
	switch {
	case prefix == "" && trail == "":
		return ""
	case prefix == "":
		return trail
	case trail == "":
		return prefix
	default:
		return prefix + "\n\n" + trail
	}
}

// trimGlamourMargins strips leading and trailing whitespace
// (including newlines) from a glamour-rendered fragment.
// Glamour adds a leading blank line for documents that open with
// a heading or paragraph, plus a trailing newline; both must be
// removed before concatenation.
func trimGlamourMargins(s string) string {
	return strings.Trim(s, " \t\n")
}

// findSafeMarkdownBoundary returns the byte offset of the END of
// the latest safe boundary in content, i.e. the offset such that
// content[:boundary] is a valid stable-prefix candidate. The
// returned offset always points immediately after a blank-line
// separator, so concatenating a fresh render of content[boundary:]
// to a cached render of content[:boundary] does not require glamour
// to share state across the cut.
//
// Returns -1 when no safe boundary exists. SAFETY FIRST: any time
// we have the slightest doubt we return -1 and let the caller fall
// back to a full render.
//
// Decision tree, in order of preference (latest boundary wins):
//
//  1. Walk backward through every "blank line" position p such that
//     content[:p] ends with "\n\n" (or "\n[ \t]*\n").
//  2. For each candidate, check that content[:p] has an even
//     number of triple-backtick fence lines (no open fenced
//     block). Any odd count means we'd be cutting inside a fence
//     and mis-syntax-highlighting the trailing partial.
//     2b. Reject if any line in content[:p] (outside fenced blocks)
//     is a list-marker line, an HTML-block opener, or a link
//     reference definition. See the B1/B2/B3 hazard rules on
//     [boundaryScan] for the reasoning behind these "anywhere in
//     prefix" rejects.
//  3. Reject if the last non-blank line of content[:p] is:
//     - a list item marker line ("^\s*([-*+]|\d+\.)\s")
//     - a table line (contains "|")
//     - a block quote ("^\s*>")
//     - a setext header underline ("^=+\s*$" or "^-+\s*$")
//     - an indented code line (4+ leading spaces or a tab)
//  4. Reject if the line immediately AFTER the boundary (skipping
//     leading blank lines) looks like a setext underline (a line
//     of '=' or '-' only). Rendering the prefix as a paragraph
//     would change once the underline arrived; that's exactly the
//     "splitting changes the prefix render" hazard §4.4 calls out.
//
// Returns the byte offset of the first character AFTER the blank
// line, i.e. the start of the trailing segment.
//
// The scan is a single O(n) pass ([scanBoundaries]) with an O(1)
// check per candidate; the naive formulation — re-scanning the
// prefix for fences and hazards at every blank-line position — is
// O(n²) on long documents and burned a noticeable share of every
// streaming flush on long reasoning traces.
func findSafeMarkdownBoundary(content string) int {
	if len(content) == 0 {
		return -1
	}
	s := scanBoundaries(content)
	for i := len(s.lines) - 1; i >= 1; i-- {
		if s.safePrefix(i) {
			return s.starts[i]
		}
	}
	return -1
}

// boundaryScan precomputes in a single forward pass everything the
// per-candidate boundary safety checks need, so both
// [findSafeMarkdownBoundary] and [findThinkingTailCut] can evaluate
// candidates in O(1) each.
//
// Line i is a boundary CANDIDATE — content[:starts[i]] is a valid
// place to split — when line i-1 is a blank-line separator that
// does not itself start at offset 0 (a leading blank line has no
// preceding newline, so nothing before it can be a prefix). The
// scan also covers the phantom line that follows a trailing
// newline, which is a legitimate end-of-document boundary.
//
// The anywhere-in-prefix hazard flag folds the three B-rules from
// the F8 round-2 review, each with its SIMPLEST viable
// conservative rule:
//
//	B1 (loose lists). A loose list has a blank line between an item
//	   and a continuation paragraph that begins with indentation
//	   but no list marker. If a candidate boundary lands on that
//	   blank line, the prefix's trailing non-blank line is the
//	   continuation paragraph, NOT a list marker, so the last-line
//	   check would accept it even though the list is still open.
//
//	   Rule chosen: any list-marker line ANYWHERE in the prefix
//	   forces -1. This is overly conservative — it forfeits
//	   boundary advancement past a closed list — but it eliminates
//	   the entire bug class with zero parsing of CommonMark's
//	   loose-list closure semantics. We retain the most useful
//	   boundary in practice: the one BEFORE the list opens (no
//	   marker has appeared in the prefix yet).
//
//	B2 (HTML blocks). CommonMark defines seven HTML-block opener
//	   patterns (script/pre/style/textarea, comments, processing
//	   instructions, CDATA, declarations, recognised tag names).
//	   If the prefix opens an HTML block that the suffix closes,
//	   splitting renders the prefix as raw HTML and the suffix as
//	   prose.
//
//	   Rule chosen: any HTML-block opener anywhere in the prefix
//	   forces -1. Same trade-off as B1 — the typical assistant
//	   output contains no raw HTML, so the perf cost is zero in
//	   the common case.
//
//	B3 (reference link definitions). A line of the form
//	   "[label]: <url>" defines a link reference that the suffix
//	   may later use as "[text][label]". Splitting the document
//	   loses the definition because each half is rendered as an
//	   independent glamour document.
//
//	   Rule chosen: any reference link definition line anywhere
//	   in the prefix forces -1. Suffix-side reference detection is
//	   fragile (three syntaxes: [text][label], [label][], [label]),
//	   so the prefix-side check is the simpler safe choice.
type boundaryScan struct {
	lines  []string // Every line of content, without terminators.
	starts []int    // Byte offset of each line's start.
	sep    []bool   // Line is blank per isBlankOrSpaces.
	fence  []bool   // Odd number of fence lines in lines[:i].
	hazard []bool   // Any B1/B2/B3 hazard in lines[:i].
	lastNB []int    // Last non-blank (TrimSpace) line in lines[:i], -1 if none.
	nextNB []int    // First non-blank (TrimSpace) line in lines[i:], -1 if none.
}

// scanBoundaries builds the [boundaryScan] for content in one
// forward pass plus one backward pass for nextNB.
func scanBoundaries(content string) *boundaryScan {
	n := strings.Count(content, "\n") + 1
	s := &boundaryScan{
		lines:  make([]string, 0, n),
		starts: make([]int, 0, n),
		sep:    make([]bool, 0, n),
		fence:  make([]bool, 0, n),
		hazard: make([]bool, 0, n),
		lastNB: make([]int, 0, n),
		nextNB: make([]int, 0, n),
	}
	inFence := false
	hazardSeen := false
	lastNonBlank := -1
	for start := 0; start <= len(content); {
		idx := len(s.lines)
		var line string
		next := len(content) + 1
		if nl := strings.IndexByte(content[start:], '\n'); nl >= 0 {
			line = content[start : start+nl]
			next = start + nl + 1
		} else {
			line = content[start:]
		}
		s.lines = append(s.lines, line)
		s.starts = append(s.starts, start)
		s.sep = append(s.sep, isBlankOrSpaces(line))
		s.fence = append(s.fence, inFence)
		s.hazard = append(s.hazard, hazardSeen)
		s.lastNB = append(s.lastNB, lastNonBlank)

		// Fence lines toggle state and are never hazards; lines
		// inside a fence are skipped so list/html/ref patterns in
		// code do not falsely trigger the hazards.
		if isFenceLine(line) {
			inFence = !inFence
		} else if !inFence {
			if trimmed := strings.TrimLeft(line, " \t"); trimmed != "" &&
				(isListItemMarker(trimmed) || isHTMLBlockOpener(line) || isLinkRefDefinition(line)) {
				hazardSeen = true
			}
		}
		if strings.TrimSpace(line) != "" {
			lastNonBlank = idx
		}
		if next > len(content) {
			break
		}
		start = next
	}

	nextNonBlank := -1
	s.nextNB = make([]int, len(s.lines))
	for i := len(s.lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(s.lines[i]) != "" {
			nextNonBlank = i
		}
		s.nextNB[i] = nextNonBlank
	}
	return s
}

// candidate reports whether line i begins at a blank-line boundary.
func (s *boundaryScan) candidate(i int) bool {
	return i > 0 && s.sep[i-1] && s.starts[i-1] > 0
}

// safePrefix reports whether cutting at the start of line i yields
// a safe stable prefix for the incremental streaming cache: a
// blank-line boundary with even fence parity, no anywhere-in-prefix
// hazard, and no construct torn across the cut. This is the
// single-pass equivalent of the old per-candidate isSafeBoundaryAt.
func (s *boundaryScan) safePrefix(i int) bool {
	if !s.candidate(i) || s.fence[i] || s.hazard[i] {
		return false
	}
	return s.cutLocal(i)
}

// safeTail reports whether rendering content starting at line i
// alone produces a correct-looking tail of the full render. The
// anywhere-in-prefix hazards (B1/B2/B3) deliberately do NOT apply:
// they protect the cached prefix render's glue equivalence, while
// here the skipped prefix is never rendered at all. The fence
// parity and cut-local checks DO apply — cutting into an open
// fence or a construct continuation would visibly mis-render the
// tail.
func (s *boundaryScan) safeTail(i int) bool {
	if !s.candidate(i) || s.fence[i] {
		return false
	}
	if nb := s.nextNB[i]; nb < 0 || s.lines[nb][0] == ' ' || s.lines[nb][0] == '\t' {
		// Either nothing to render, or the fragment would open on
		// an indented continuation (e.g. a loose-list paragraph
		// torn from its item, or an indented code block missing
		// its lead-in).
		return false
	}
	return s.cutLocal(i)
}

// cutLocal runs the checks that only depend on the lines adjacent
// to the cut: the last non-blank line before it must not keep a
// markdown construct open, and the first non-blank line after it
// must not be a setext underline that would retroactively promote
// the prefix's final paragraph to a header.
func (s *boundaryScan) cutLocal(i int) bool {
	if nb := s.lastNB[i]; nb >= 0 && lineOpensConstruct(s.lines[nb]) {
		return false
	}
	if nb := s.nextNB[i]; nb >= 0 && isSetextUnderlineCandidate(s.lines[nb]) {
		return false
	}
	return true
}

// findThinkingTailCut returns a byte offset into content such that
// rendering only content[cut:] yields a correct-looking tail of the
// full render. The collapsed thinking view only ever displays the
// last maxCollapsedThinkingHeight rendered lines, so when the
// source is long enough it renders just a bounded tail fragment
// instead of the whole document — streaming a long reasoning trace
// otherwise pays a full-document glamour render on every debounced
// flush.
//
// The walk goes backward from the end and takes the LATEST safe
// blank-line boundary whose fragment holds at least minNonBlank
// non-blank lines, giving up once maxScan lines or maxBytes bytes
// were scanned. ok=false sends the caller down the full-render
// path.
func findThinkingTailCut(content string, minNonBlank, maxScan, maxBytes int) (cut int, ok bool) {
	s := scanBoundaries(content)
	nonBlank := 0
	for i := len(s.lines) - 1; i >= 1; i-- {
		if strings.TrimSpace(s.lines[i]) != "" {
			nonBlank++
		}
		if scanned := len(s.lines) - i; scanned > maxScan || len(content)-s.starts[i] > maxBytes {
			return 0, false
		}
		if nonBlank < minNonBlank || !s.safeTail(i) {
			continue
		}
		return s.starts[i], true
	}
	return 0, false
}

// isBlankOrSpaces reports whether s consists entirely of spaces
// and tabs (or is empty).
func isBlankOrSpaces(s string) bool {
	for i := range len(s) {
		if s[i] != ' ' && s[i] != '\t' {
			return false
		}
	}
	return true
}

// isFenceLine reports whether line opens or closes a fenced code
// block.
func isFenceLine(line string) bool {
	// Strip up to 3 spaces of indentation.
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) {
		return false
	}
	c := line[i]
	if c != '`' && c != '~' {
		return false
	}
	run := 0
	for i < len(line) && line[i] == c {
		i++
		run++
	}
	return run >= 3
}

// lineOpensConstruct reports whether line keeps a markdown
// construct open across the boundary. We err conservatively —
// any case that smells like list/table/quote/setext/indented-code
// returns true.
func lineOpensConstruct(line string) bool {
	// Indented code: a tab, or 4+ leading spaces.
	if len(line) > 0 && line[0] == '\t' {
		return true
	}
	if strings.HasPrefix(line, "    ") {
		return true
	}

	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return false
	}

	// Block quote.
	if trimmed[0] == '>' {
		return true
	}

	// List item: "- " "* " "+ " or "<digits>. " or "<digits>) ".
	if isListItemMarker(trimmed) {
		return true
	}

	// Table: any pipe character anywhere in the line. Conservative:
	// pipe-in-prose is rare and the cost of bailing is one slow
	// frame.
	if strings.ContainsRune(line, '|') {
		return true
	}

	// Setext underline candidate as the LAST line of the prefix:
	// this would be a setext header for an even-earlier paragraph.
	// Refuse to split at all in this case — the boundary is right
	// in the middle of a header.
	if isSetextUnderlineCandidate(trimmed) {
		return true
	}

	return false
}

// isListItemMarker reports whether line (already left-trimmed)
// starts with a CommonMark list-item marker followed by a space
// or tab.
func isListItemMarker(line string) bool {
	if line == "" {
		return false
	}
	c := line[0]
	if c == '-' || c == '*' || c == '+' {
		if len(line) >= 2 && (line[1] == ' ' || line[1] == '\t') {
			return true
		}
		return false
	}
	// Ordered list: digits followed by '.' or ')' and a space.
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i > 9 {
		return false
	}
	if i >= len(line) {
		return false
	}
	if line[i] != '.' && line[i] != ')' {
		return false
	}
	if i+1 >= len(line) {
		return false
	}
	return line[i+1] == ' ' || line[i+1] == '\t'
}

// isSetextUnderlineCandidate reports whether line (with optional
// leading whitespace) consists entirely of '=' or entirely of '-'
// characters with optional trailing whitespace. CommonMark
// requires no leading whitespace on the underline; we accept up
// to three spaces for safety so an indented underline still
// blocks a split.
func isSetextUnderlineCandidate(line string) bool {
	// Strip leading whitespace.
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i == len(line) {
		return false
	}
	c := line[i]
	if c != '=' && c != '-' {
		return false
	}
	j := i
	for j < len(line) && line[j] == c {
		j++
	}
	// Allow trailing whitespace.
	for j < len(line) {
		if line[j] != ' ' && line[j] != '\t' {
			return false
		}
		j++
	}
	// Need at least one underline character. "-" alone is also a
	// list marker without a trailing space; the listItem check
	// covers the marker case before we get here.
	return j-i >= 1
}

// isHTMLBlockOpener reports whether line begins one of the seven
// CommonMark HTML block patterns. We accept up to three spaces of
// leading indentation (CommonMark rule). Matching is intentionally
// loose — we only need to know the line "looks like an HTML
// block start", not parse the contained markup.
func isHTMLBlockOpener(line string) bool {
	// Strip up to 3 spaces of indentation.
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	rest := line[i:]
	if len(rest) < 2 || rest[0] != '<' {
		return false
	}

	// Type 2: HTML comment "<!--".
	if strings.HasPrefix(rest, "<!--") {
		return true
	}
	// Type 3: processing instruction "<?".
	if strings.HasPrefix(rest, "<?") {
		return true
	}
	// Type 5: CDATA "<![CDATA[".
	if strings.HasPrefix(rest, "<![CDATA[") {
		return true
	}
	// Type 4: declaration "<!" followed by an ASCII letter.
	if len(rest) >= 3 && rest[1] == '!' && isASCIILetter(rest[2]) {
		return true
	}

	// Type 1: <script | <pre | <style | <textarea (case-insensitive)
	// followed by whitespace, '>', end-of-line, or other non-name
	// terminators. Use a permissive HasPrefix check on lowercase.
	low := strings.ToLower(rest)
	for _, t := range []string{"<script", "<pre", "<style", "<textarea"} {
		if strings.HasPrefix(low, t) {
			next := byte(0)
			if len(low) > len(t) {
				next = low[len(t)]
			}
			if next == 0 || next == ' ' || next == '\t' || next == '>' {
				return true
			}
		}
	}

	// Types 6 & 7: open or close of a block-level tag.
	//
	// Type 6 matches a fixed CommonMark tag set; type 7 matches any
	// otherwise-valid open/close tag whose name is not in the
	// script/pre/style/textarea family. We collapse both into a
	// single check: the line must start with '<' or '</' followed
	// by an ASCII letter. This deliberately mirrors the other
	// hazards — when in doubt, forfeit the boundary. Lines like
	// "<3", "<-", "<<", or mid-line "<foo>" do NOT trigger because
	// we require the line to *start* (after up to 3 spaces) with
	// '<letter' or '</letter'.
	j := 1 // past '<'
	if j < len(rest) && rest[j] == '/' {
		j++
	}
	if j >= len(rest) || !isASCIILetter(rest[j]) {
		return false
	}
	return true
}

// isASCIILetter reports whether b is an ASCII letter.
func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isLinkRefDefinition reports whether line matches a CommonMark
// link reference definition opener. The conservative pattern:
//
//	^[ ]{0,3}\[[^\]]+\]:\s*\S+
//
// i.e. up to 3 spaces, then a bracketed label (no nested ']'),
// then a colon, then whitespace, then at least one non-whitespace
// character of destination. We do not validate the destination —
// presence of a ref-def opener anywhere in the prefix is enough
// to forfeit the boundary.
func isLinkRefDefinition(line string) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || line[i] != '[' {
		return false
	}
	i++
	labelStart := i
	for i < len(line) && line[i] != ']' {
		i++
	}
	if i >= len(line) || i == labelStart {
		// No closing bracket, or empty label.
		return false
	}
	// i points at ']'.
	i++
	if i >= len(line) || line[i] != ':' {
		return false
	}
	i++
	// Skip required whitespace.
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	// At least one non-whitespace character of destination.
	return i < len(line)
}
