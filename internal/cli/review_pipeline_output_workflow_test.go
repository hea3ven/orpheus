//go:build integration

package cli_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowReviewPipelineAttachedReviewerPassesOutputThroughCLI(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-attached", "Attached review", "Stream attached reviewer output.")
	fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "impl", Reviewer: "reviewer"}, map[string]agent.Profile{
		"impl":     {Command: "impl"},
		"reviewer": {Command: "review-agent", Interactive: true},
	})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}}})
	fixture.agent.outcomes = []semanticAgentOutcome{{review: true, stdout: "attached stdout\n", stderr: "attached stderr\n"}}

	stdout, stderr := fixture.run("", "task", "run", "op-attached")

	is.Contains(stdout, "attached stdout")
	is.Contains(stdout, "Published op-attached")
	is.Contains(stderr, "attached stderr")
	is.NotContains(stderr, "\x1b[31mattached stderr")
	must.Len(fixture.agent.launches, 1)
	latest := loadLatestReview(t, fixture, "op-attached")
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
}

func TestIntegrationWorkflowReviewPipelineNoninteractiveReviewerFinalizesOutputThroughCLI(t *testing.T) {
	for _, tc := range []struct {
		name        string
		findingType taskstate.FindingType
		failure     bool
	}{
		{name: "clean pass"},
		{name: "advisory live tail", findingType: taskstate.FindingTypeAdvisory},
		{name: "blocker expanded tail", findingType: taskstate.FindingTypeBlocking},
		{name: "failure expanded tail", failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newReviewWorkflowFixture(t, "op-tail", "Review tail", "Finalize reviewer output.")
			fixture.forceInteractiveReviewOutput(80)
			var builder strings.Builder
			for i := 1; i <= 35; i++ {
				fmt.Fprintf(&builder, "agent stdout %02d\n", i)
			}
			outcome := semanticAgentOutcome{stdout: builder.String()}
			if tc.findingType != "" {
				outcome.findings = []taskstate.ReviewFinding{{Type: tc.findingType, Title: "Review finding", Description: "Keep this visible.", SuggestedAction: "Address the finding."}}
			}
			if tc.failure {
				outcome.err = errors.New("reviewer failed")
			}
			fixture.reviewers(outcome)
			fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}}})
			var stdout bytes.Buffer
			stderr := newCLIVisualTerminal(80)

			err := fixture.runWithWriters("p\n", &stdout, stderr, "task", "run", "op-tail")

			if tc.failure {
				require.ErrorIs(t, err, outcome.err)
			} else {
				require.NoError(t, err)
			}
			assert.NotContains(t, stdout.String(), "agent stdout")
			visible := stderr.Visible()
			latest := loadLatestReview(t, fixture, "op-tail")
			switch {
			case tc.failure:
				assert.Equal(t, taskstate.ReviewStatusFailed, latest.Status)
				assert.Contains(t, visible, "agent stdout 06")
				assert.Contains(t, visible, "agent stdout 35")
				assert.NotContains(t, visible, "agent stdout 05")
			case tc.findingType == taskstate.FindingTypeBlocking:
				assert.Equal(t, taskstate.ReviewStatusWaitingForAutomatedDecision, latest.Status)
				assert.Contains(t, visible, "agent stdout 07")
				assert.Contains(t, visible, "agent stdout 35")
				assert.NotContains(t, visible, "agent stdout 06")
				assert.Less(t, strings.Index(visible, "agent stdout 35"), strings.Index(visible, "Decision for finding 1"))
			case tc.findingType == taskstate.FindingTypeAdvisory:
				assert.Equal(t, taskstate.ReviewStatusPassed, latest.Status)
				assert.Contains(t, visible, "agent stdout 35")
				assert.Contains(t, visible, "Recorded advisory review finding 1 for op-tail.")
				assert.NotContains(t, visible, "agent stdout 27")
			default:
				assert.Equal(t, taskstate.ReviewStatusPassed, latest.Status)
				assert.NotContains(t, visible, "agent stdout")
			}
			if tc.failure || tc.findingType == taskstate.FindingTypeBlocking {
				assert.Empty(t, fixture.candidate.commits)
			} else {
				assert.Contains(t, stdout.String(), "Published op-tail")
			}
		})
	}
}

func TestIntegrationWorkflowReviewPipelinePassingCheckClearsRollingTailThroughCLI(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-check-pass", "Check pass", "Clear passing check output.")
	fixture.forceInteractiveReviewOutput(80)
	check := fixture.check("check", checkResult{stdout: "passing stdout\n", stderr: "passing stderr\n"})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "check", "name": "unit", "command": check}}})
	var stdout bytes.Buffer
	stderr := newCLIVisualTerminal(80)

	err := fixture.runWithWriters("", &stdout, stderr, "task", "run", "op-check-pass")

	must.NoError(err)
	is.NotContains(stdout.String(), "passing stdout")
	is.Contains(stdout.String(), "Published op-check-pass")
	visible := stderr.Visible()
	is.Contains(visible, "== Review step: unit (check) ==")
	is.NotContains(visible, "passing stdout")
	is.NotContains(visible, "passing stderr")
	is.Contains(stderr.Raw(), "\x1b[31mpassing stderr\x1b[0m")
}

func TestIntegrationWorkflowReviewPipelineBlockedCheckExpandsRollingTailThroughCLI(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-check-blocked", "Check blocked", "Expand blocked check output.")
	fixture.forceInteractiveReviewOutput(80)
	var builder strings.Builder
	for i := 1; i <= 35; i++ {
		fmt.Fprintf(&builder, "blocked stdout %02d\n", i)
	}
	check := fixture.check("check", checkResult{code: 7, stdout: builder.String()})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "check", "name": "unit", "command": check}}})
	var stdout bytes.Buffer
	stderr := newCLIVisualTerminal(80)

	err := fixture.runWithWriters("", &stdout, stderr, "task", "run", "op-check-blocked")

	must.NoError(err)
	is.NotContains(stdout.String(), "blocked stdout")
	visible := stderr.Visible()
	is.Contains(visible, "blocked stdout 06")
	is.Contains(visible, "blocked stdout 35")
	is.NotContains(visible, "blocked stdout 05")
	is.Contains(visible, "Review blocked for op-check-blocked by check \"unit\".")
	latest := loadLatestReview(t, fixture, "op-check-blocked")
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.Len(latest.Findings, 1)
}

type cliVisualTerminal struct {
	raw   bytes.Buffer
	lines []string
	cells []int
	row   int
	width int
}

func newCLIVisualTerminal(width int) *cliVisualTerminal {
	return &cliVisualTerminal{lines: []string{""}, cells: []int{0}, width: width}
}

func (t *cliVisualTerminal) Write(p []byte) (int, error) {
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
			index += cliTestEscapeSequenceLen(p[index:])
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
			spaces := 8 - t.cells[t.row]%8
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

func (t *cliVisualTerminal) Visible() string {
	last := len(t.lines)
	for last > 0 && t.lines[last-1] == "" {
		last--
	}
	return strings.Join(t.lines[:last], "\n")
}

func (t *cliVisualTerminal) Raw() string {
	return t.raw.String()
}

func (t *cliVisualTerminal) ensureRow() {
	for t.row >= len(t.lines) {
		t.lines = append(t.lines, "")
		t.cells = append(t.cells, 0)
	}
}

func (t *cliVisualTerminal) writeSpaces(count int) {
	for range count {
		t.writeRune(' ')
	}
}

func (t *cliVisualTerminal) writeRune(r rune) {
	width := cliTestRuneWidth(r)
	if width > 0 && t.width > 0 && t.cells[t.row]+width > t.width {
		t.row++
		t.ensureRow()
	}
	t.lines[t.row] += string(r)
	t.cells[t.row] += width
}

func cliTestEscapeSequenceLen(text []byte) int {
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

func cliTestRuneWidth(r rune) int {
	switch {
	case r < ' ' || r == '\x7f' || (r >= '\u0080' && r <= '\u009f'):
		return 0
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case cliTestWideRune(r):
		return 2
	default:
		return 1
	}
}

func cliTestWideRune(r rune) bool {
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
