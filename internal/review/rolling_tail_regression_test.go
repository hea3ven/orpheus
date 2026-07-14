package review_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestRunPipelineInteractivePassingAgentReviewClearsTabbedAndWideRollingTail(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		notWanted string
	}{
		{
			name:      "tab expands past terminal width",
			line:      "\tabcdefghijk",
			notWanted: "abc",
		},
		{
			name:      "wide runes expand past terminal width",
			line:      "abcdefg界界",
			notWanted: "界",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runAgentReviewPipelineWithRollingOutput(t, 10, test.line)

			if result.err != nil {
				t.Fatalf("RunPipeline error = %v", result.err)
			}
			if result.outcome.Status != taskstate.ReviewStatusPassed {
				t.Fatalf("outcome = %q, want passed", result.outcome.Status)
			}
			if result.stdout != "" {
				t.Fatalf("stdout = %q, want rolling output captured away from stdout", result.stdout)
			}
			if visible := result.terminal.Visible(); strings.Contains(visible, test.notWanted) {
				t.Fatalf("visible terminal = %q, want passing rolling tail cleared", visible)
			}
		})
	}
}

func TestRunPipelineInteractiveAgentReviewSanitizesRollingTailOutput(t *testing.T) {
	result := runAgentReviewPipelineWithRollingOutput(t, 40, "safe\x1b[2J\ttext\x07")

	if result.err != nil {
		t.Fatalf("RunPipeline error = %v", result.err)
	}
	if result.outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome = %q, want passed", result.outcome.Status)
	}
	raw := result.terminal.raw.String()
	for _, notWanted := range []string{"\x1b[2J", "\t", "\a"} {
		if strings.Contains(raw, notWanted) {
			t.Fatalf("raw terminal output = %q, want without %q", raw, notWanted)
		}
	}
}

func TestRunPipelineInteractivePassingAgentReviewClearsRollingTailAfterResize(t *testing.T) {
	harness := newAgentReviewPipelineHarness(t)
	var stdout bytes.Buffer
	terminal := newCellVisualTerminal(21)

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context:           context.Background(),
		Store:             harness.store,
		RepoID:            "alpha",
		TaskID:            "op-1",
		Branch:            "main",
		Workdir:           harness.workdir,
		Attempt:           harness.attempt,
		Pipeline:          agentReviewPipeline(),
		Stdout:            &stdout,
		Stderr:            terminal,
		InteractiveOutput: true,
		OutputWidth:       21,
		OutputWidthFunc: func() (int, bool) {
			return terminal.width, true
		},
		AgentConfig: reviewAgentConfig(false),
		AgentLauncher: fakeReviewLauncherFunc(func(
			ctx context.Context,
			command agentexec.Command,
			opts agentexec.LaunchOptions,
		) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(opts.Stdout, "aaaaaaaaa界aaaaaaaaa"); err != nil {
				return err
			}
			terminal.Resize(10)
			return nil
		}),
	})

	if err != nil {
		t.Fatalf("RunPipeline error = %v", err)
	}
	if outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome = %q, want passed", outcome.Status)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want rolling output captured away from stdout", stdout.String())
	}
	if visible := terminal.Visible(); strings.Contains(visible, "aaaa") || strings.Contains(visible, "界") {
		t.Fatalf("visible terminal = %q, want resized rolling tail cleared", visible)
	}
}

type rollingOutputPipelineResult struct {
	outcome  review.PipelineOutcome
	stdout   string
	terminal *cellVisualTerminal
	err      error
}

func runAgentReviewPipelineWithRollingOutput(
	t *testing.T,
	width int,
	line string,
) rollingOutputPipelineResult {
	t.Helper()

	harness := newAgentReviewPipelineHarness(t)
	var stdout bytes.Buffer
	terminal := newCellVisualTerminal(width)

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context:           context.Background(),
		Store:             harness.store,
		RepoID:            "alpha",
		TaskID:            "op-1",
		Branch:            "main",
		Workdir:           harness.workdir,
		Attempt:           harness.attempt,
		Pipeline:          agentReviewPipeline(),
		Stdout:            &stdout,
		Stderr:            terminal,
		InteractiveOutput: true,
		OutputWidth:       width,
		OutputWidthFunc: func() (int, bool) {
			return terminal.width, true
		},
		AgentConfig: reviewAgentConfig(false),
		AgentLauncher: fakeReviewLauncherFunc(func(
			ctx context.Context,
			command agentexec.Command,
			opts agentexec.LaunchOptions,
		) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := fmt.Fprintln(opts.Stdout, line)
			return err
		}),
	})
	return rollingOutputPipelineResult{
		outcome:  outcome,
		stdout:   stdout.String(),
		terminal: terminal,
		err:      err,
	}
}

type cellVisualTerminal struct {
	raw   bytes.Buffer
	lines []string
	cells []int
	row   int
	width int
}

func newCellVisualTerminal(width int) *cellVisualTerminal {
	return &cellVisualTerminal{
		lines: []string{""},
		cells: []int{0},
		width: width,
	}
}

func (t *cellVisualTerminal) Write(p []byte) (int, error) {
	t.raw.Write(p)
	for index := 0; index < len(p); {
		if bytes.HasPrefix(p[index:], []byte("\x1b[1A")) {
			if t.row > 0 {
				t.row--
			}
			index += len("\x1b[1A")
			continue
		}
		if bytes.HasPrefix(p[index:], []byte("\x1b[2K")) {
			t.lines[t.row] = ""
			t.cells[t.row] = 0
			index += len("\x1b[2K")
			continue
		}
		if p[index] == '\x1b' {
			index += testEscapeSequenceLen(p[index:])
			continue
		}

		switch p[index] {
		case '\r':
			t.lines[t.row] = ""
			t.cells[t.row] = 0
			index++
		case '\n':
			t.row++
			t.ensureRow()
			index++
		case '\t':
			spaces := testTabWidth - t.cells[t.row]%testTabWidth
			t.writeSpaces(spaces)
			index++
		default:
			r, size := utf8.DecodeRune(p[index:])
			t.writeRune(r)
			index += size
		}
	}
	return len(p), nil
}

func (t *cellVisualTerminal) Visible() string {
	last := len(t.lines)
	for last > 0 && t.lines[last-1] == "" {
		last--
	}
	return strings.Join(t.lines[:last], "\n")
}

func (t *cellVisualTerminal) Resize(width int) {
	t.width = width
	oldLines := append([]string(nil), t.lines...)
	t.lines = []string{""}
	t.cells = []int{0}
	t.row = 0
	for index, line := range oldLines {
		if index > 0 {
			t.row++
			t.ensureRow()
		}
		for _, r := range line {
			t.writeRune(r)
		}
	}
}

func (t *cellVisualTerminal) ensureRow() {
	for t.row >= len(t.lines) {
		t.lines = append(t.lines, "")
		t.cells = append(t.cells, 0)
	}
}

func (t *cellVisualTerminal) writeSpaces(count int) {
	for range count {
		t.writeRune(' ')
	}
}

func (t *cellVisualTerminal) writeRune(r rune) {
	width := testRuneWidth(r)
	if width > 0 && t.width > 0 && t.cells[t.row]+width > t.width {
		t.row++
		t.ensureRow()
	}
	t.lines[t.row] += string(r)
	t.cells[t.row] += width
}

const testTabWidth = 8

func testEscapeSequenceLen(text []byte) int {
	if len(text) <= 1 {
		return 1
	}
	switch text[1] {
	case '[':
		for i := 2; i < len(text); i++ {
			if text[i] >= 0x40 && text[i] <= 0x7e {
				return i + 1
			}
		}
		return len(text)
	case ']':
		for i := 2; i < len(text); i++ {
			if text[i] == '\a' {
				return i + 1
			}
			if text[i] == '\x1b' && i+1 < len(text) && text[i+1] == '\\' {
				return i + 2
			}
		}
		return len(text)
	default:
		if text[1] >= 0x40 && text[1] <= 0x5f {
			return 2
		}
		return 1
	}
}

func testRuneWidth(r rune) int {
	switch {
	case r < ' ' || r == '\x7f' || (r >= '\u0080' && r <= '\u009f'):
		return 0
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case testWideRune(r):
		return 2
	default:
		return 1
	}
}

func testWideRune(r rune) bool {
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
