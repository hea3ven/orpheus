//nolint:testpackage // Owns workflow-dependent flag validation.
package workflow

import (
	"strings"
	"testing"
)

func TestControllerValidatesFlagApplicability(t *testing.T) {
	tests := []struct {
		name   string
		action TaskRunAction
		agent  string
		pipe   string
		root   bool
		want   string
	}{
		{name: "dispatch accepts dispatch controls", action: TaskRunActionStartImplementation, agent: "implementer", pipe: "quality", root: true},
		{name: "fresh review accepts review controls", action: TaskRunActionStartReview, agent: "implementer", pipe: "quality"},
		{name: "resumed review rejects pipeline", action: TaskRunActionResumeReview, pipe: "quality", want: "--pipeline cannot affect"},
		{name: "resumed review rejects agent", action: TaskRunActionResumeReview, agent: "implementer", want: "--agent cannot affect"},
		{name: "review rejects target mode", action: TaskRunActionStartReview, root: true, want: "--repo-root only applies"},
		{name: "finalization rejects review controls", action: TaskRunActionRetryFinalization, pipe: "quality", want: "--pipeline cannot affect"},
		{name: "open pr rejects implementation controls", action: TaskRunActionOpenPR, root: true, want: "--repo-root only applies"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAdvanceOptions("op-1", tt.action, AdvanceOptions{AgentName: tt.agent, PipelineName: tt.pipe, RepoRootMode: tt.root})
			if tt.want == "" {
				if err != nil {
					t.Fatalf("validateAdvanceOptions() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateAdvanceOptions() error = %v, want %q", err, tt.want)
			}
		})
	}
}
