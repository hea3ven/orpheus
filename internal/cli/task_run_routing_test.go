//nolint:testpackage // Exercises unexported state-specific flag validation.
package cli

import (
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/workflow"
)

func TestValidateTaskRunRouteFlags(t *testing.T) {
	tests := []struct {
		name   string
		action workflow.TaskRunAction
		agent  string
		pipe   string
		main   bool
		root   bool
		want   string
	}{
		{name: "dispatch accepts dispatch controls", action: workflow.TaskRunActionStartImplementation, agent: "implementer", pipe: "quality", main: true},
		{name: "fresh review accepts review controls", action: workflow.TaskRunActionStartReview, agent: "implementer", pipe: "quality"},
		{name: "manual resume rejects pipeline", action: workflow.TaskRunActionResumeReview, pipe: "quality", want: "--pipeline cannot affect"},
		{name: "manual resume rejects agent", action: workflow.TaskRunActionResumeReview, agent: "implementer", want: "--agent cannot affect"},
		{name: "review rejects target mode", action: workflow.TaskRunActionStartReview, main: true, want: "--main and --repo-root only apply"},
		{name: "finalization rejects review controls", action: workflow.TaskRunActionRetryFinalization, pipe: "quality", want: "--pipeline cannot affect"},
		{name: "open pr rejects implementation controls", action: workflow.TaskRunActionOpenPR, root: true, want: "--main and --repo-root only apply"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTaskRunRouteFlags("op-1", tt.action, tt.agent, tt.pipe, tt.main, tt.root)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("validateTaskRunRouteFlags() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateTaskRunRouteFlags() error = %v, want %q", err, tt.want)
			}
		})
	}
}
