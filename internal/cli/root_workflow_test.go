//go:build integration

package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type reviewWorkflowHelpCase struct {
	name string
	args []string
	want []string
}

var reviewWorkflowHelpCases = []reviewWorkflowHelpCase{
	{name: "root", args: []string{"--help"}, want: []string{"Orpheus", "Usage:", "--verbose"}},
	{name: "task list", args: []string{"task", "list", "--help"}, want: []string{"--repo", "repository"}},
	{name: "task block", args: []string{"task", "block", "--help"}, want: []string{"block <id>", "safely inactive", "review/publication boundaries", "without stopping existing executions", "not a persistent manual hold"}},
	{name: "task unblock", args: []string{"task", "unblock", "--help"}, want: []string{"unblock <id>", "recorded pre-block lifecycle", "automatic reopening takes precedence", "existing workflow gates still apply"}},
	{name: "task sync", args: []string{"task", "sync", "--help"}, want: []string{"integration branch", "conflict-free branches unchanged", "configured conflict resolver"}},
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

func TestIntegrationWorkflowReviewWorkflowCommandHelpExplainsResponsibilitiesAndNextCommands(t *testing.T) {
	t.Parallel()
	for _, test := range reviewWorkflowHelpCases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCommandWorkflow(t)
			is := assert.New(t)

			stdout, stderr := fixture.mustExecute(test.args...)

			is.Empty(stderr)
			for _, want := range test.want {
				is.Contains(stdout, want)
			}
		})
	}
}
