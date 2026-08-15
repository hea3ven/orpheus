package workflow_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/workflow"
)

func TestSelectSeparateTaskCandidatesParsesSelection(t *testing.T) {
	t.Parallel()

	candidates := []workflow.SeparateTaskCandidate{{Index: 2}, {Index: 4}, {Index: 8}}
	selected, err := workflow.SelectSeparateTaskCandidates(candidates, "3, 1, 3")
	if err != nil {
		t.Fatalf("select candidates: %v", err)
	}
	if len(selected) != 2 || selected[0].Index != 8 || selected[1].Index != 2 {
		t.Fatalf("selected = %#v, want candidate indexes 8 and 2", selected)
	}
}
