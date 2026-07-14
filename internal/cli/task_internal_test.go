package cli

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestSyncConflictAgentUsageOptionsUnsupportedHarnessUsesStableReason(t *testing.T) {
	tests := []struct {
		name    string
		harness string
		want    string
	}{
		{name: "blank", want: "unsupported_harness:unknown"},
		{name: "trimmed", harness: " local ", want: "unsupported_harness:local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := syncConflictAgentUsageOptions(
				agent.CommandSnapshot{Harness: tt.harness},
				t.TempDir(),
			)(taskstate.AgentExecution{}, nil)

			if options.UsageCapture.Status != taskstate.UsageCaptureUnknown {
				t.Fatalf("status = %q, want %q", options.UsageCapture.Status, taskstate.UsageCaptureUnknown)
			}
			if options.UsageCapture.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", options.UsageCapture.Reason, tt.want)
			}
		})
	}
}
