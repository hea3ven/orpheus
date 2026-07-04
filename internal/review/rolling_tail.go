package review

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

const (
	liveTailLines    = 8
	blockedTailLines = 30

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

	tail := newRollingTail(opts.Stderr, opts.OutputWidth)
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
	mu       sync.Mutex
	output   io.Writer
	width    int
	lines    []tailLine
	rendered int
}

func newRollingTail(output io.Writer, width int) *rollingTail {
	return &rollingTail{
		output: output,
		width:  width,
		lines:  make([]tailLine, 0, blockedTailLines),
	}
}

func (t *rollingTail) append(stream tailStream, text string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.lines = append(t.lines, tailLine{stream: stream, text: strings.TrimSuffix(text, "\r")})
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
	for index, line := range visible {
		if index > 0 {
			_, _ = fmt.Fprint(t.output, "\n")
		}
		_, _ = fmt.Fprint(t.output, formatTailLine(line, t.width))
	}
	if trailingNewline && len(visible) > 0 {
		_, _ = fmt.Fprint(t.output, "\n")
	}
	t.rendered = len(visible)
	if trailingNewline {
		t.rendered = 0
	}
}

func (t *rollingTail) clearLocked() {
	if t.rendered == 0 {
		return
	}
	_, _ = fmt.Fprint(t.output, "\r\x1b[2K")
	for i := 1; i < t.rendered; i++ {
		_, _ = fmt.Fprint(t.output, "\x1b[1A\r\x1b[2K")
	}
	t.rendered = 0
}

func (t *rollingTail) visibleLines(limit int) []tailLine {
	if len(t.lines) <= limit {
		return t.lines
	}
	return t.lines[len(t.lines)-limit:]
}

func formatTailLine(line tailLine, width int) string {
	text := truncateTailLine(line.text, width)
	if line.stream != tailStreamStderr {
		return text
	}
	return stderrColor + text + resetColor
}

func truncateTailLine(text string, width int) string {
	if width <= 0 {
		return text
	}
	if width > 1 {
		width--
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
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
