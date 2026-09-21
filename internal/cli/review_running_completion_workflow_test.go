//go:build integration

package cli_test

import (
	"io"
	"strings"
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskDoneConfirmsRunningCompletionWithoutRewritingRunStatus(t *testing.T) {
	for _, test := range []struct {
		name, input string
		approve     bool
	}{{"approve", "yes\n", true}, {"decline", "no\n", false}} {
		t.Run(test.name, func(t *testing.T) {
			f := newFinalizationFixture(t, "op-main")
			f.completion("op-main", "main", taskWorkflowRepoRoot, "Implementation complete", "Ready for local review.", taskstate.RunStatusRunning)
			f.passedReview("op-main")
			f.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return true }

			stdout, stderr, err := f.runError(test.input, "task", "done", "op-main")

			assert.Equal(t, 1, strings.Count(stderr, "Finalize anyway? [y/N]:"))
			assert.Contains(t, stderr, "Implementation complete")
			current, item := f.loadFinalTask("op-main")
			require.Len(t, current.Runs, 1)
			assert.Equal(t, taskstate.RunStatusRunning, current.Runs[0].Status)
			if test.approve {
				require.NoError(t, err)
				assert.Contains(t, stdout, "Finalized op-main")
				assert.Equal(t, taskmodel.StatusClosed, item.Status)
				assert.NotEmpty(t, taskstate.FinalizationFacts(current).Commit)
			} else {
				require.ErrorContains(t, err, "explicit interactive confirmation is required")
				assert.Empty(t, stdout)
				f.assertUnpublished("op-main")
			}
		})
	}
}
