package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewRootCommandHelp(t *testing.T) {
	is := assert.New(t)

	output, _ := executeCommand(t, []string{"--help"})

	is.Contains(output, "Orpheus")
	is.Contains(output, "Usage:")
	is.Contains(output, "--verbose")
}

func TestRootCommandDoesNotEmitDebugByDefault(t *testing.T) {
	is := assert.New(t)

	stdout, stderr := executeCommand(t, []string{})

	is.NotContains(stdout, "level=DEBUG")
	is.NotContains(stderr, "level=DEBUG")
}

func TestRootCommandVerboseEmitsDebugToStderr(t *testing.T) {
	is := assert.New(t)

	stdout, stderr := executeCommand(t, []string{"--verbose"})

	is.NotContains(stdout, "level=DEBUG")
	is.Contains(stderr, "level=DEBUG")
	is.Contains(stderr, "msg=\"rendering root help\"")
}

type reviewWorkflowHelpCase struct {
	name string
	args []string
	want []string
}

var reviewWorkflowHelpCases = []reviewWorkflowHelpCase{
	{
		name: "task run",
		args: []string{"task", "run", "--help"},
		want: []string{
			"automatically starts a review follow-up run",
			"After the agent records completion with agent done, run task review",
		},
	},
	{
		name: "task review",
		args: []string{"task", "review", "--help"},
		want: []string{
			"Configured pipelines may include check, manual, and agent_review steps.",
			"Blocking findings leave the task ready for task run follow-up.",
			"Use task review show to inspect persisted findings",
		},
	},
	{
		name: "task review show",
		args: []string{"task", "review", "show", "--help"},
		want: []string{
			"inspection surface for review state",
			"blocking/advisory/separate-task findings",
			"created follow-up Beads",
		},
	},
	{
		name: "task done",
		args: []string{"task", "done", "--help"},
		want: []string{
			"refuses publication until the latest local review attempt has passed",
			"Use task done to retry publication or finalization",
		},
	},
	{
		name: "agent review add",
		args: []string{"agent", "review", "add", "--help"},
		want: []string{
			"Use this only from an attached agent_review pipeline step.",
			"Separate-task findings propose standalone follow-up work",
			"Operators inspect findings with task review show",
		},
	},
}

func TestReviewWorkflowCommandHelpExplainsResponsibilitiesAndNextCommands(t *testing.T) {
	for _, test := range reviewWorkflowHelpCases {
		t.Run(test.name, func(t *testing.T) {
			is := assert.New(t)

			stdout, stderr := executeCommand(t, test.args)

			is.Empty(stderr)
			for _, want := range test.want {
				is.Contains(stdout, want)
			}
		})
	}
}
