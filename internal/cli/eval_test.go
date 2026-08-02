//nolint:testpackage // Invocation-scoped fixture requires internal composition wiring.
package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvalReviewContextSkipsNormalInvocationState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	stdout, stderr, err := executeCommandWithError(t, []string{"eval", "review-context", "--repetitions", "0"})

	require.ErrorContains(t, err, "repetitions must be positive, got 0")
	require.NotContains(t, err.Error(), "XDG_CONFIG_HOME must be an absolute path")
	require.Empty(t, stdout)
	require.Empty(t, stderr)
}
