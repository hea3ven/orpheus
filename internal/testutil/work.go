package testutil

import (
	"time"

	"github.com/hea3ven/orpheus/internal/work"
)

// ReviewableWork is explicit synthetic history for package-owned reader/policy
// fixtures. It does not observe Git or a process; boundary tests must instead use
// their native adapters or semantic collaborators. All fixture identities are
// deliberately recognizable and never become application defaults.
func ReviewableWork(repositoryID, taskID, directory, branch string) work.State {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	zero := 0
	return work.State{RepositoryID: repositoryID, TaskID: taskID, Workflows: []work.Workflow{{Phases: []work.Phase{
		&work.PreparationPhase{EstablishedAt: &at, Context: &work.WorkContext{Directory: directory, TaskBranch: branch, CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: work.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}}},
		&work.ImplementationPhase{Execution: work.AgentExecution{Profile: "fixture-implementer", Harness: "pi", Command: "fixture-agent", StartedAt: at.Add(time.Second), Process: &work.ExitedProcess{At: at.Add(3 * time.Second), Code: &zero}}, Completion: &work.Completion{Summary: "feat: fixture intent", Description: "Synthetic completed implementation", DetailedDescription: "Synthetic PR body", CompletedAt: at.Add(2 * time.Second)}, Output: &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: "fixture-output", Tree: "fixture-tree"}, RetainedRef: "refs/orpheus/fixture-output"}},
	}}}}
}

// DeliveredPRWork adds an explicitly fixture-authorized PR and its independently
// supplied delivery observation. It must never be used by application defaults.
func DeliveredPRWork(repositoryID, taskID, directory, branch, url string) (work.State, error) {
	snapshot := ReviewableWork(repositoryID, taskID, directory, branch)
	w := &snapshot.Workflows[0]
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	zero := 0
	w.ReviewContracts = []work.ReviewContract{{PipelineKey: "fixture-review", Slots: []work.ReviewSlot{{Kind: work.ReviewAgent, ConfigKey: "reviewer"}}}}
	w.Phases = append(w.Phases, &work.ReviewPhase{Contract: 0, Scope: work.ReviewScope{Requirements: "Synthetic task requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}}, StartedAt: at.Add(4 * time.Second), Steps: []work.ReviewStep{&work.AgentReviewStep{Slot: 0, Execution: work.AgentExecution{Profile: "fixture-reviewer", Harness: "pi", Command: "fixture-agent", StartedAt: at.Add(5 * time.Second), Process: &work.ExitedProcess{At: at.Add(6 * time.Second), Code: &zero}}}}}, &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: 2}, Target: &work.PullRequestTarget{}, Content: "Fixture title\n\nFixture PR body"}})
	if err := snapshot.AuthorizePublication(0, work.PublicationRef{Phase: 3}, at.Add(7*time.Second), "Explicit synthetic fixture grant"); err != nil {
		return work.State{}, err
	}
	w.Phases[3].(*work.PublicationPhase).Delivery = &work.PullRequestDelivery{PullRequest: work.PullRequestRef{URL: url}, PublicCommit: "fixture-output", Head: "fixture-output", Tree: "fixture-tree", ObservedAt: at.Add(8 * time.Second), Evidence: "Synthetic independently observed PR and remote head"}
	w.Phases = append(w.Phases, &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: 3}})
	return snapshot, snapshot.Validate()
}
