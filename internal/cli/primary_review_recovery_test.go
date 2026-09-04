//nolint:testpackage // Guidance helpers are intentionally tested at the CLI boundary.
package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrimaryReviewLivenessGuidanceIsActionable(t *testing.T) {
	tests := []struct {
		name   string
		render func(*bytes.Buffer) error
		want   []string
	}{
		{
			name: "recovered",
			render: func(output *bytes.Buffer) error {
				return renderPrimaryReviewRecoveryGuidance(output, "op-1", "supervisor_and_child_pids_absent")
			},
			want: []string{"candidate may contain reviewer mutations", "orpheus task dir op-1", "git status --short", "git diff", "task show review op-1"},
		},
		{
			name: "live",
			render: func(output *bytes.Buffer) error {
				return renderPrimaryReviewActiveGuidance(output, "op-1", "supervisor_pid_live")
			},
			want: []string{"still active", "wait for it to finish", "task show review op-1"},
		},
		{
			name: "unverifiable",
			render: func(output *bytes.Buffer) error {
				return renderPrimaryReviewUnverifiableGuidance(output, "op-1", "missing_supervisor_pid_legacy_run")
			},
			want: []string{"cannot automatically recover", "orpheus doctor", "task show review op-1"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := test.render(&output); err != nil {
				t.Fatalf("render guidance: %v", err)
			}
			for _, want := range test.want {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("guidance = %q, want %q", output.String(), want)
				}
			}
		})
	}
}
