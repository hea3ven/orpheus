package workflow

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/require"
)

func TestCancellationChecksRecordedExecutionLiveness(t *testing.T) {
	service := CancelService{ProcessProbe: func(pid int) (agentexec.ProcessLiveness, error) {
		switch pid {
		case 10:
			return agentexec.ProcessAbsent, nil
		case 20:
			return agentexec.ProcessLive, nil
		default:
			return agentexec.ProcessUnknown, nil
		}
	}}
	for _, tc := range []struct {
		name      string
		phase     work.Phase
		wantError string
	}{
		{
			name: "child still running after supervisor disappeared",
			phase: &work.ImplementationPhase{Execution: work.AgentExecution{
				SupervisorPID: 10, ChildPID: 20, Process: &work.RunningProcess{},
			}},
			wantError: "implementation execution is live",
		},
		{
			name: "review supervisor cannot be verified",
			phase: &work.ReviewPhase{Steps: []work.ReviewStep{&work.AgentReviewStep{Execution: work.AgentExecution{
				SupervisorPID: 30, Process: &work.RunningProcess{},
			}}}},
			wantError: "review execution is unverifiable",
		},
		{
			name:      "check has no observed result or start failure",
			phase:     &work.ReviewPhase{Steps: []work.ReviewStep{&work.CheckStep{}}},
			wantError: "review check execution is unverifiable",
		},
		{
			name: "prior command success does not verify a reopened inspection",
			phase: &work.ReviewPhase{Steps: []work.ReviewStep{&work.ManualReviewStep{
				InspectionPending: new(true),
				CommandResult:     &work.ExitedProcess{Code: new(0)},
			}}},
			wantError: "manual review command execution is unverifiable",
		},
		{
			name: "stale implementation has no live processes",
			phase: &work.ImplementationPhase{Execution: work.AgentExecution{
				SupervisorPID: 10, ChildPID: 10, Process: &work.RunningProcess{},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := &work.State{Workflows: []work.Workflow{{Phases: []work.Phase{tc.phase}}}}
			err := service.checkCancellation(task.Task{}, snapshot)
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
		})
	}
}

func TestCancellationRequiresPublicationToBeSettledWithoutDelivery(t *testing.T) {
	const url = "https://example.invalid/pr/123"
	delivered := &work.PublicationPhase{Delivery: &work.PullRequestDelivery{PullRequest: work.PullRequestRef{URL: url}}}
	for _, tc := range []struct {
		name      string
		metadata  task.Metadata
		phases    []work.Phase
		wantError string
	}{
		{
			name:   "no effect reserved at a publication decision",
			phases: []work.Phase{&work.PublicationPhase{}},
		},
		{
			name:      "publication could have pushed without recording a PR",
			phases:    []work.Phase{&work.PublicationPhase{Operation: &work.PublicationOperation{Public: &work.CodeIdentity{Commit: "public"}}}},
			wantError: "unresolved publication",
		},
		{
			name:      "recorded merged PR must finalize",
			phases:    []work.Phase{delivered, &work.ExternalReviewPhase{Outcome: &work.PROutcome{State: work.PRMerged}}},
			wantError: "normal finalization",
		},
		{
			name:      "observed direct delivery must finalize",
			phases:    []work.Phase{&work.PublicationPhase{Delivery: &work.DirectDelivery{}}},
			wantError: "normal finalization",
		},
		{
			name:      "closed PR is unrelated to source pointer",
			metadata:  task.Metadata{task.MetadataPRURL: "https://example.invalid/pr/999"},
			phases:    []work.Phase{delivered, &work.ExternalReviewPhase{Outcome: &work.PROutcome{State: work.PRClosedUnmerged}}},
			wantError: "no verified closed-unmerged outcome",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A pending reset does not erase prior external effects.
			snapshot := &work.State{Workflows: []work.Workflow{{Phases: tc.phases}, {}}}
			err := (CancelService{}).checkCancellation(task.Task{Metadata: tc.metadata}, snapshot)
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
		})
	}
}
