package revieweval

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/hea3ven/orpheus/internal/workflow/implementation"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workstate"
)

// prepareCommittedFixture stages synthetic scenario files through a real observed
// child, then retains an ordinary commit. It never pretends a model implemented
// the intentionally defective fixture or invents a successful process exit.
func prepareCommittedFixture(ctx context.Context, paths state.Paths, repoPath, taskID string) (work.ReviewScope, error) {
	branch := "orpheus/" + taskID
	if err := runCommand(ctx, repoPath, "git", "checkout", "-b", branch); err != nil {
		return work.ReviewScope{}, err
	}
	baseline, err := gitmeta.PrepareBaseline(ctx, repoPath, branch, "refs/orpheus/eval/baseline", nil)
	if err != nil {
		return work.ReviewScope{}, err
	}

	runner := phaseexec.Runner{Store: workstate.NewStore(paths), Paths: paths, RepositoryID: "review-eval", TaskID: taskID}
	observedAt := time.Now().UTC()
	current, err := runner.Commit(ctx, func(current *work.State) (bool, error) {
		// This explicit evaluation creates its own isolated synthetic history from
		// the baseline just observed above, never from operator workflow state.
		current.Workflows = []work.Workflow{{Phases: []work.Phase{&work.PreparationPhase{Context: &work.WorkContext{Directory: repoPath, TaskBranch: branch, CheckpointRef: baseline.RetainedRef, Baseline: work.CodeIdentity{Commit: baseline.Commit, Tree: baseline.Tree}}, EstablishedAt: &observedAt}}}}
		return implementation.ReserveChange(implementation.Reservation{Agent: "synthetic-fixture-stager", Harness: "git", Command: "git", Args: []string{"add", "--all"}}, observedAt)(current)
	})
	if err != nil {
		return work.ReviewScope{}, err
	}
	view, err := taskstate.Project(current)
	if err != nil {
		return work.ReviewScope{}, err
	}
	run, _ := taskstate.LatestRun(view)
	_, err = runner.Commit(ctx, implementation.CompletionChange(run.Address, taskstate.CompleteRunOptions{CommitSubject: "Prepare synthetic review scenario", CommitBody: "Retain deliberately defective fixture input for review-context evaluation.", PRDescription: "This is isolated synthetic benchmark setup, not model-authored implementation.", TechnicalExplanation: "The fixture stager has a real observed process boundary; review consumes its retained native Git output."}, time.Now().UTC()))
	if err != nil {
		return work.ReviewScope{}, err
	}

	result := agent.Execute(ctx, agentexec.AttachedLauncher{}, agent.ExecutionOptions{
		Command: agentexec.Command{Name: "synthetic-fixture-stager", Command: "git", Args: []string{"add", "--all"}},
		Launch:  agentexec.LaunchOptions{Dir: repoPath, Stdout: io.Discard, Stderr: io.Discard, OnStart: func(pid int) error { return implementation.RecordChildPID(runner, run.Address, pid) }},
		CaptureUsage: func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
			return taskstate.RecordRunUsageOptions{UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureUnknown, Reason: "Synthetic fixture staging is not a model execution"}}
		},
	})
	if err := implementation.RecordExecution(ctx, phaseexec.AgentResults{Commit: runner.Commit}, run.Address, result); err != nil {
		return work.ReviewScope{}, fmt.Errorf("prepare synthetic fixture process: %w", err)
	}
	capture := implementation.Service{Paths: paths, Store: workstate.NewStore(paths), Git: implementation.LocalOutputGit{}}
	if _, err := capture.Capture(ctx, run.Address); err != nil {
		return work.ReviewScope{}, err
	}
	// main remains the actual local integration baseline while the scenario is
	// captured on its isolated task branch. No remote or delivery is fabricated.
	return work.ReviewScope{Output: work.OutputRef{Phase: run.Address.Phase}, Destination: &work.Destination{Remote: ".", Branch: "main", Commit: baseline.Commit}}, nil
}
