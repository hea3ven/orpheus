package cli

import (
	"fmt"
	"strings"
)

func parseTaskPriority(value string) (int, error) {
	normalized := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(value)), "P")
	if len(normalized) == 1 && normalized[0] >= '0' && normalized[0] <= '4' {
		return int(normalized[0] - '0'), nil
	}
	return 0, fmt.Errorf("invalid --priority %q; expected 0–4 or P0–P4 (critical, high, medium, low, backlog)", value)
}
