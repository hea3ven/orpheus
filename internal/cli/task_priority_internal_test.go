package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTaskPriorityAcceptsNumericAndPrefixedScale(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
	}{
		{"0", 0}, {"1", 1}, {"2", 2}, {"3", 3}, {"4", 4},
		{"P0", 0}, {"P1", 1}, {"P2", 2}, {"P3", 3}, {"P4", 4},
		{" p0 ", 0},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseTaskPriority(tc.input)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	for _, input := range []string{"", "P", "-1", "5", "P5", "P-1", "1.5", "00", "+1", "PP0", "high"} {
		t.Run("invalid/"+input, func(t *testing.T) {
			_, err := parseTaskPriority(input)
			require.ErrorContains(t, err, "invalid --priority")
			assert.Contains(t, err.Error(), "0–4 or P0–P4")
		})
	}
}
