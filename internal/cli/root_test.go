//go:build integration

//nolint:testpackage // Invocation-scoped fixture requires internal composition wiring.
package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIntegrationNewRootCommandHelp(t *testing.T) {
	t.Parallel()
	is := assert.New(t)

	output, _ := executeCommand(t, []string{"--help"})

	is.Contains(output, "Orpheus")
	is.Contains(output, "Usage:")
	is.Contains(output, "--verbose")
}

func TestIntegrationRootCommandDoesNotEmitDebugByDefault(t *testing.T) {
	t.Parallel()
	is := assert.New(t)

	stdout, stderr := executeCommand(t, []string{})

	is.NotContains(stdout, "level=DEBUG")
	is.NotContains(stderr, "level=DEBUG")
}

func TestIntegrationRootCommandVerboseEmitsDebugToStderr(t *testing.T) {
	t.Parallel()
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
			"selects the next safe transition",
			"Configured pipelines may include check, manual, and agent_review steps.",
			"Use `task show review` to inspect review findings",
			"PR synchronization remains `task sync`",
		},
	},
	{
		name: "task show review",
		args: []string{"task", "show", "review", "--help"},
		want: []string{
			"inspection surface for review state",
			"blocking/advisory/separate-task findings",
			"autonomous budget exhaustion",
			"interrupted automated blocker decisions",
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
			"Operators inspect findings with task show review",
		},
	},
}

func TestIntegrationReviewWorkflowCommandHelpExplainsResponsibilitiesAndNextCommands(t *testing.T) {
	t.Parallel()
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

func TestIntegrationTaskReviewCommandIsAbsent(t *testing.T) {
	t.Parallel()
	is := assert.New(t)

	stdout, stderr := executeCommand(t, []string{"task", "--help"})
	is.Empty(stderr)
	is.NotContains(stdout, "  review")
}
