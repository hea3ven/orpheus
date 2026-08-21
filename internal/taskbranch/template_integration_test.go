//go:build integration

package taskbranch_test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/hea3ven/orpheus/internal/taskbranch"
)

func TestIntegrationRejectedRenderedBranchesFailGitCheckRefFormat(t *testing.T) {
	for _, branch := range []string{"HEAD", "feature/.op-1"} {
		t.Run(branch, func(t *testing.T) {
			if _, err := taskbranch.Render(branch, taskbranch.Values{TaskID: "op-1"}); err == nil {
				t.Fatalf("Render(%q) error = nil, want Git ref syntax error", branch)
			}
			if output, err := exec.CommandContext(context.Background(), "git", "check-ref-format", "--branch", branch).CombinedOutput(); err == nil {
				t.Fatalf("git check-ref-format --branch %q succeeded, output %q", branch, output)
			}
		})
	}
}

func TestIntegrationRenderedBranchesPassGitCheckRefFormat(t *testing.T) {
	tests := []struct {
		name     string
		template string
		values   taskbranch.Values
	}{
		{
			name:     "compatibility default",
			template: taskbranch.DefaultTemplate,
			values:   taskbranch.Values{TaskID: "op-1"},
		},
		{
			name:     "task ID",
			template: "feature/{{task_id}}",
			values:   taskbranch.Values{TaskID: "op.1"},
		},
		{
			name:     "all placeholders",
			template: "feature/{{external_ref}}/{{task_title}}-{{task_id}}",
			values: taskbranch.Values{
				TaskID:      "op-1",
				ExternalRef: "OPS: 1",
				TaskTitle:   "Add branch names!",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			branch, err := taskbranch.Render(tt.template, tt.values)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if output, err := exec.CommandContext(context.Background(), "git", "check-ref-format", "--branch", branch).CombinedOutput(); err != nil {
				t.Fatalf("git check-ref-format --branch %q: %v\n%s", branch, err, output)
			}
		})
	}
}
