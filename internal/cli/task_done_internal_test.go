package cli

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

func TestConfirmRunningCompletionFinalizationRequiresAffirmativeAnswer(t *testing.T) {
	original := taskDoneInputIsTerminal
	taskDoneInputIsTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { taskDoneInputIsTerminal = original })

	tests := []struct {
		name      string
		input     string
		wantAllow bool
	}{
		{name: "yes", input: "yes\n", wantAllow: true},
		{name: "short yes", input: "y\n", wantAllow: true},
		{name: "no", input: "no\n"},
		{name: "empty", input: "\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			command := &cobra.Command{}
			command.SetIn(strings.NewReader(tt.input))
			command.SetErr(&stderr)

			got, err := confirmRunningCompletionFinalization(command, workflow.RunningCompletionConfirmation{
				TaskID:  "op-1",
				Attempt: 3,
				Summary: "Implemented fix",
			})
			if err != nil {
				t.Fatalf("confirm: %v", err)
			}
			if got != tt.wantAllow {
				t.Fatalf("confirmed = %v, want %v", got, tt.wantAllow)
			}
			output := stderr.String()
			for _, want := range []string{
				"Warning: latest run attempt 3 for task op-1 is still recorded as running",
				"Recorded completion summary: Implemented fix",
				"Finalize anyway? [y/N]:",
			} {
				if !strings.Contains(output, want) {
					t.Fatalf("stderr = %q, want %q", output, want)
				}
			}
		})
	}
}

func TestTaskReviewLifecycleFrontendConfirmsRunningCompletionWithSharedReader(t *testing.T) {
	original := taskDoneInputIsTerminal
	taskDoneInputIsTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { taskDoneInputIsTerminal = original })

	var stderr bytes.Buffer
	command := &cobra.Command{}
	command.SetIn(strings.NewReader(""))
	command.SetErr(&stderr)
	reader := bufio.NewReader(strings.NewReader("a\ny\n"))
	if line, err := reader.ReadString('\n'); err != nil || line != "a\n" {
		t.Fatalf("prime shared reader = %q, %v", line, err)
	}
	frontend := taskReviewLifecycleFrontend{command: command, reader: reader}

	confirmed, err := frontend.ConfirmRunningCompletionFinalization(
		workflow.ReviewAttemptContext{},
		workflow.RunningCompletionConfirmation{TaskID: "op-1", Attempt: 1, Summary: "Reviewed"},
	)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !confirmed {
		t.Fatal("confirmed = false, want true")
	}
	output := stderr.String()
	if !strings.Contains(output, "Finalize anyway? [y/N]:") {
		t.Fatalf("stderr = %q, want finalization prompt", output)
	}
}

func TestConfirmRunningCompletionFinalizationRefusesNonInteractiveInput(t *testing.T) {
	original := taskDoneInputIsTerminal
	taskDoneInputIsTerminal = func(io.Reader) bool { return false }
	t.Cleanup(func() { taskDoneInputIsTerminal = original })

	var stderr bytes.Buffer
	command := &cobra.Command{}
	command.SetIn(strings.NewReader("yes\n"))
	command.SetErr(&stderr)

	got, err := confirmRunningCompletionFinalization(command, workflow.RunningCompletionConfirmation{
		TaskID:  "op-1",
		Attempt: 3,
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if got {
		t.Fatal("confirmed = true, want false for non-interactive input")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
