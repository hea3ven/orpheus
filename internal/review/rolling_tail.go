package review

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	liveTailLines    = 8
	blockedTailLines = 30
	tailTabWidth     = 8

	stderrColor = "\x1b[31m"
	resetColor  = "\x1b[0m"
)

type stepOutput struct {
	stdoutDest io.Writer
	stderrDest io.Writer
	tail       *rollingTail
	tailStdout *tailStreamWriter
	tailStderr *tailStreamWriter
}

func newStepOutput(opts PipelineRunOptions, rolling bool) stepOutput {
	if !rolling || !opts.InteractiveOutput {
		return stepOutput{stdoutDest: opts.Stdout, stderrDest: opts.Stderr}
	}

	tail := newRollingTail(opts.Stderr, opts.OutputWidth, opts.OutputWidthFunc)
	stdout := newTailStreamWriter(tail, tailStreamStdout)
	stderr := newTailStreamWriter(tail, tailStreamStderr)
	return stepOutput{
		stdoutDest: stdout,
		stderrDest: stderr,
		tail:       tail,
		tailStdout: stdout,
		tailStderr: stderr,
	}
}

func (o stepOutput) finishClear() {
	if o.tail == nil {
		return
	}
	o.flush()
	o.tail.finish(tailFinishClear)
}

func (o stepOutput) finishTail() {
	if o.tail == nil {
		return
	}
	o.flush()
	o.tail.finish(tailFinishLive)
}

func (o stepOutput) finishExpanded() {
	if o.tail == nil {
		return
	}
	o.flush()
	o.tail.finish(tailFinishExpanded)
}

func (o stepOutput) flush() {
	o.tailStdout.flush()
	o.tailStderr.flush()
}

func (o stepOutput) stdout() io.Writer {
	return o.stdoutDest
}

func (o stepOutput) stderr() io.Writer {
	return o.stderrDest
}

type tailStream int

const (
	tailStreamStdout tailStream = iota
	tailStreamStderr
)

type tailLine struct {
	stream tailStream
	text   string
}

type tailFinishMode int

const (
	tailFinishClear tailFinishMode = iota
	tailFinishLive
	tailFinishExpanded
)

type rollingTail struct {
	mu           sync.Mutex
	output       io.Writer
	width        int
	widthFunc    func() (int, bool)
	lines        []tailLine
	rendered     []string
	renderedRows int
}

func newRollingTail(output io.Writer, width int, widthFunc func() (int, bool)) *rollingTail {
	return &rollingTail{
		output:    output,
		width:     width,
		widthFunc: widthFunc,
		lines:     make([]tailLine, 0, blockedTailLines),
	}
}

func (t *rollingTail) append(stream tailStream, text string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.lines = append(t.lines, tailLine{stream: stream, text: sanitizeTailText(strings.TrimSuffix(text, "\r"))})
	if len(t.lines) > blockedTailLines {
		copy(t.lines, t.lines[len(t.lines)-blockedTailLines:])
		t.lines = t.lines[:blockedTailLines]
	}
	t.renderLocked(liveTailLines, false)
}

func (t *rollingTail) finish(mode tailFinishMode) {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch mode {
	case tailFinishClear:
		t.clearLocked()
	case tailFinishLive:
		t.renderLocked(liveTailLines, true)
	case tailFinishExpanded:
		t.renderLocked(blockedTailLines, true)
	}
}

func (t *rollingTail) renderLocked(limit int, trailingNewline bool) {
	t.clearLocked()
	visible := t.visibleLines(limit)
	width := t.currentWidth()
	rendered := make([]string, 0, len(visible))
	var renderedRows int
	for index, line := range visible {
		if index > 0 {
			_, _ = fmt.Fprint(t.output, "\n")
		}
		text, formatted, rows := formatTailLine(line, width)
		_, _ = fmt.Fprint(t.output, formatted)
		rendered = append(rendered, text)
		renderedRows += rows
	}
	if trailingNewline && len(visible) > 0 {
		_, _ = fmt.Fprint(t.output, "\n")
	}
	t.rendered = rendered
	t.renderedRows = renderedRows
	if trailingNewline {
		t.rendered = nil
		t.renderedRows = 0
	}
}

func (t *rollingTail) clearLocked() {
	rows := t.renderedRows
	if len(t.rendered) > 0 {
		rows = tailRenderedRows(t.rendered, t.currentWidth())
	}
	if rows == 0 {
		return
	}
	_, _ = fmt.Fprint(t.output, "\r\x1b[2K")
	for i := 1; i < rows; i++ {
		_, _ = fmt.Fprint(t.output, "\x1b[1A\r\x1b[2K")
	}
	t.rendered = nil
	t.renderedRows = 0
}

func (t *rollingTail) visibleLines(limit int) []tailLine {
	if len(t.lines) <= limit {
		return t.lines
	}
	return t.lines[len(t.lines)-limit:]
}

func (t *rollingTail) currentWidth() int {
	if t.widthFunc != nil {
		if width, ok := t.widthFunc(); ok {
			return width
		}
	}
	return t.width
}

func formatTailLine(line tailLine, width int) (string, string, int) {
	text := truncateTailLine(line.text, width)
	rows := tailTerminalRows(text, width)
	if line.stream != tailStreamStderr {
		return text, text, rows
	}
	return text, stderrColor + text + resetColor, rows
}

func truncateTailLine(text string, width int) string {
	text = expandTailTabs(sanitizeTailText(text))
	if width <= 0 {
		return text
	}
	if width > 1 {
		width--
	}
	if tailDisplayWidth(text) <= width {
		return text
	}
	if width <= 3 {
		return tailCellPrefix(text, width)
	}
	return tailCellPrefix(text, width-3) + "..."
}

func sanitizeTailText(text string) string {
	var builder strings.Builder
	for i := 0; i < len(text); {
		switch text[i] {
		case '\x1b':
			i += tailEscapeSequenceLen(text[i:])
			continue
		case '\t':
			builder.WriteByte('\t')
			i++
			continue
		}

		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			builder.WriteRune(r)
			i++
			continue
		}
		if tailControlRune(r) {
			i += size
			continue
		}
		builder.WriteRune(r)
		i += size
	}
	return builder.String()
}

func tailEscapeSequenceLen(text string) int {
	if len(text) <= 1 {
		return 1
	}
	switch text[1] {
	case '[':
		return tailCSISequenceLen(text)
	case ']':
		return tailOSCSequenceLen(text)
	default:
		if text[1] >= 0x40 && text[1] <= 0x5f {
			return 2
		}
		return 1
	}
}

func tailCSISequenceLen(text string) int {
	for i := 2; i < len(text); i++ {
		if text[i] >= 0x40 && text[i] <= 0x7e {
			return i + 1
		}
	}
	return len(text)
}

func tailOSCSequenceLen(text string) int {
	for i := 2; i < len(text); i++ {
		if text[i] == '\a' {
			return i + 1
		}
		if text[i] == '\x1b' && i+1 < len(text) && text[i+1] == '\\' {
			return i + 2
		}
	}
	return len(text)
}

func tailControlRune(r rune) bool {
	return r < ' ' || r == '\x7f' || (r >= '\u0080' && r <= '\u009f')
}

func expandTailTabs(text string) string {
	if !strings.ContainsRune(text, '\t') {
		return text
	}

	var builder strings.Builder
	var cells int
	for _, r := range text {
		if r != '\t' {
			builder.WriteRune(r)
			cells += tailRuneWidth(r)
			continue
		}
		spaces := tailTabWidth - cells%tailTabWidth
		builder.WriteString(strings.Repeat(" ", spaces))
		cells += spaces
	}
	return builder.String()
}

func tailTerminalRows(text string, width int) int {
	if width <= 0 {
		return 1
	}
	rows := 1
	var cells int
	for _, r := range text {
		runeWidth := tailRuneWidth(r)
		if runeWidth == 0 {
			continue
		}
		if cells+runeWidth > width {
			rows++
			cells = 0
		}
		cells += runeWidth
	}
	return rows
}

func tailRenderedRows(lines []string, width int) int {
	var rows int
	for _, line := range lines {
		rows += tailTerminalRows(line, width)
	}
	return rows
}

func tailDisplayWidth(text string) int {
	var cells int
	for _, r := range text {
		cells += tailRuneWidth(r)
	}
	return cells
}

func tailCellPrefix(text string, width int) string {
	if width <= 0 {
		return ""
	}
	var builder strings.Builder
	var cells int
	for _, r := range text {
		runeWidth := tailRuneWidth(r)
		if cells+runeWidth > width {
			break
		}
		builder.WriteRune(r)
		cells += runeWidth
	}
	return builder.String()
}

func tailRuneWidth(r rune) int {
	switch {
	case tailControlRune(r):
		return 0
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case tailWideRune(r):
		return 2
	default:
		return 1
	}
}

func tailWideRune(r rune) bool {
	return (r >= 0x1100 && r <= 0x115f) ||
		r == 0x2329 ||
		r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1faff) ||
		(r >= 0x20000 && r <= 0x3fffd)
}

type tailStreamWriter struct {
	mu     sync.Mutex
	tail   *rollingTail
	stream tailStream
	buffer strings.Builder
}

func newTailStreamWriter(tail *rollingTail, stream tailStream) *tailStreamWriter {
	return &tailStreamWriter{tail: tail, stream: stream}
}

func (w *tailStreamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, b := range p {
		if b == '\n' {
			w.tail.append(w.stream, w.buffer.String())
			w.buffer.Reset()
			continue
		}
		_ = w.buffer.WriteByte(b)
	}
	return len(p), nil
}

func (w *tailStreamWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.buffer.Len() == 0 {
		return
	}
	w.tail.append(w.stream, w.buffer.String())
	w.buffer.Reset()
}
