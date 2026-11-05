package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInteractionResponsesPreservePendingWorkUntilAnExactLegalDecision(t *testing.T) {
	for _, kind := range []InteractionKind{InteractionManualReview, InteractionFindings, InteractionPublication} {
		t.Run(string(kind), func(t *testing.T) {
			snapshot := testutil.ReviewableWork("alpha", "op-review", "/fixture/task", "orpheus/op-review")
			switch kind {
			case InteractionManualReview:
				phase := appendRouteReview(&snapshot.Workflows[0], true)
				phase.Steps = []work.ReviewStep{&work.ManualReviewStep{Slot: 0}}
			case InteractionFindings:
				phase := appendRouteReview(&snapshot.Workflows[0], false)
				phase.Steps = []work.ReviewStep{&work.CheckStep{Slot: 0, Result: &work.CheckResult{ExitCode: routeNumber(7), FinishedAt: routeAt(7)}, Findings: []work.Finding{routeFinding(), routeFinding()}}}
			case InteractionPublication:
				var err error
				snapshot, err = testutil.DeliveredPRWork("alpha", "op-review", "/fixture/task", "orpheus/op-review", "https://example.test/pr/1")
				require.NoError(t, err)
				snapshot.Workflows[0].Phases = snapshot.Workflows[0].Phases[:4]
				publication := snapshot.Workflows[0].Phases[3].(*work.PublicationPhase)
				publication.Consent = nil
				publication.Delivery = nil
				snapshot.Workflows[0].Phases[2].(*work.ReviewPhase).Scope.Requirements = "Task intent\n\n\n\n"
			}
			closedView, err := interactionView(task.Task{ID: "op-review", Status: task.StatusClosed}, snapshot)
			require.NoError(t, err)
			assert.Nil(t, closedView.Pending, "closed tasks cannot request workflow decisions")
			assert.Equal(t, TaskRunActionCompleted, closedView.Next.Action)
			controller, store, boundary := interactionController(t, snapshot)
			view, err := controller.Inspect(context.Background(), "op-review")
			require.NoError(t, err)
			require.NotNil(t, view.Pending)
			assert.Equal(t, kind, view.Pending.ID.Kind)
			assert.Equal(t, "/fixture/task", view.Context.Directory)
			assert.Equal(t, "fixture-output", view.Revision.Code.Commit)
			before, err := store.Load("alpha", "op-review")
			require.NoError(t, err)
			originalID := view.Pending.ID
			view.Context.Directory = "caller mutation"
			preserved, err := store.Load("alpha", "op-review")
			require.NoError(t, err)
			assert.Equal(t, before, preserved)

			for _, change := range []string{"version", "workflow", "phase", "step", "kind"} {
				id := originalID
				switch change {
				case "version":
					id.Version++
				case "workflow":
					id.Owner.Workflow++
				case "phase":
					id.Owner.Phase++
				case "step":
					id.Owner.Step = routeNumber(999)
				case "kind":
					id.Kind = "another decision"
				}
				_, err := controller.Respond(context.Background(), id, PauseResponse{})
				require.ErrorIs(t, err, ErrStaleInteraction)
			}
			_, err = controller.Respond(context.Background(), originalID, PublicationResponse{Action: DecisionApprove})
			require.ErrorContains(t, err, "does not match")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var canceledResponse Response = PauseResponse{}
			if kind == InteractionPublication {
				canceledResponse = PublicationResponse{Action: DecisionAuthorize, Evidence: "Canceled grant"}
			}
			_, err = controller.Respond(ctx, originalID, canceledResponse)
			require.ErrorIs(t, err, context.Canceled)
			paused, err := controller.Respond(context.Background(), originalID, PauseResponse{})
			require.NoError(t, err)
			assert.Equal(t, originalID, paused.Pending.ID)
			assert.Empty(t, boundary.dir, "refused and paused decisions must not inspect or mutate the checkout")
			preserved, err = store.Load("alpha", "op-review")
			require.NoError(t, err)
			assert.Equal(t, before, preserved)

			var response Response = ManualResponse{Action: DecisionApprove}
			wantNext := TaskRunActionPreparePublication
			if kind == InteractionFindings {
				decisions := []FindingDecision{{Ref: view.Pending.Findings[0].Ref, Action: DecisionWaive, Reason: "Accepted risk"}, {Ref: view.Pending.Findings[1].Ref, Action: DecisionDowngrade}}
				_, err = controller.Respond(context.Background(), originalID, FindingsResponse{Decisions: decisions})
				require.ErrorContains(t, err, "requires a reason")
				preserved, err = store.Load("alpha", "op-review")
				require.NoError(t, err)
				assert.Equal(t, before, preserved, "a bad second answer must not apply the first")
				decisions[1].Reason = "Advisory only"
				response = FindingsResponse{Decisions: decisions}
			}
			if kind == InteractionPublication {
				response = PublicationResponse{Action: DecisionAuthorize, Evidence: "Operator accepts this exact displayed plan"}
				wantNext = TaskRunActionPublish
			}
			{
				boundary.err = errors.New("revision changed during the decision")
				_, err = controller.Respond(context.Background(), originalID, response)
				require.ErrorIs(t, err, boundary.err)
				preserved, err = store.Load("alpha", "op-review")
				require.NoError(t, err)
				assert.Equal(t, before, preserved, "a response cannot approve or waive a different checkout")
				boundary.err = nil
			}
			accepted, err := controller.Respond(context.Background(), originalID, response)
			require.NoError(t, err)
			assert.Nil(t, accepted.Pending)
			assert.Equal(t, wantNext, accepted.Next.Action)
			retained, err := store.Load("alpha", "op-review")
			require.NoError(t, err)
			assert.Greater(t, retained.Version, before.Version)
			assert.Len(t, retained.Workflows[0].Phases, len(before.Workflows[0].Phases), "responding never advances phases")
			_, err = controller.Respond(context.Background(), originalID, response)
			require.ErrorIs(t, err, ErrStaleInteraction)
			after, err := store.Load("alpha", "op-review")
			require.NoError(t, err)
			assert.Equal(t, retained, after)
		})
	}
}

func interactionController(t *testing.T, snapshot work.State) (Controller, workstate.Store, *candidateReadinessGit) {
	t.Helper()
	paths := newDispatchTestPaths(t)
	store := workstate.NewStore(paths)
	_, err := store.Save(snapshot, 0)
	require.NoError(t, err)
	boundary := &candidateReadinessGit{}
	backend := interactionBackend{item: task.Task{ID: "op-review", Title: "Task intent", Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-review", task.MetadataWorktree: "/fixture/task"}, IssueType: task.IssueTypeTask, Status: task.StatusInProgress}}
	controller := Controller{Delivery: FinalizationService{Git: interactionPublicationGit{boundary: boundary}}, ReviewLifecycleService: ReviewLifecycleService{Paths: paths, RunStore: taskstate.NewStore(paths), CandidateGit: boundary, Sources: []task.RepositorySource{{Repository: task.Repository{ID: "alpha", TaskIDPrefix: "op"}}}, BackendFactory: func(task.RepositorySource) (ReviewLifecycleBackend, error) { return backend, nil }}}
	return controller, store, boundary
}

func TestReviewProgressFailurePreservesAssessmentsAndSettlesUnstartedExecutions(t *testing.T) {
	for _, kind := range []string{review.KindManual, review.KindCheck, review.KindAgentReview} {
		t.Run(kind, func(t *testing.T) {
			snapshot := testutil.ReviewableWork("alpha", "op-review", "/fixture/task", "orpheus/op-review")
			phase := appendRouteReview(&snapshot.Workflows[0], kind == review.KindManual)
			snapshot.Workflows[0].ReviewContracts[0].Slots[0].Kind = work.ReviewStepKind(kind)
			if kind == review.KindManual {
				phase.Steps = []work.ReviewStep{&work.ManualReviewStep{Slot: 0}}
			}
			controller, store, _ := interactionController(t, snapshot)
			controller.Frontend = &freshReviewFrontend{}
			failure := errors.New("terminal disconnected")
			controller.Progress = func(Progress) error { return failure }
			controller.ReviewEffects.CaptureCandidate = func(context.Context, string, review.CommittedCandidate, *slog.Logger, ...slog.Attr) (review.CandidateCheck, error) {
				return func() error { return nil }, nil
			}
			launcher := &unstartedReviewLauncher{}
			controller.AgentLauncher = launcher
			projected, err := controller.RunStore.Load("alpha", "op-review")
			require.NoError(t, err)
			before, err := store.Load("alpha", "op-review")
			require.NoError(t, err)
			attempt, _ := taskstate.LatestReview(projected)
			pipeline := review.Pipeline{Name: "quality", Steps: []review.Step{{Name: "inspect", Kind: kind}}}
			execution := review.FreshExecution()
			if kind == review.KindManual {
				execution, err = review.ResumeExecution(pipeline, attempt)
				require.NoError(t, err)
			}
			current := ReviewAttemptContext{ReviewContext: ReviewContext{Source: task.RepositorySource{Repository: task.Repository{ID: "alpha"}}, Task: task.Task{ID: "op-review", Status: task.StatusInProgress}, store: controller.RunStore, Workdir: "/fixture/task"}, Review: attempt, Pipeline: pipeline, execution: execution, AgentConfig: agent.Config{Defaults: agent.AgentDefaults{Implementer: "reviewer", Reviewer: "reviewer"}, Agents: map[string]agent.Profile{"reviewer": {Command: "not-an-agent"}}}}

			_, err = controller.executeReviewAttempt(context.Background(), current)

			require.ErrorIs(t, err, failure)
			assert.False(t, launcher.called)
			after, err := store.Load("alpha", "op-review")
			require.NoError(t, err)
			if kind == review.KindManual {
				assert.Equal(t, before, after, "output failure must leave the exact pending assessment intact")
				return
			}
			recorded := after.Workflows[0].LastPhase().(*work.ReviewPhase)
			assert.Nil(t, recorded.ExecutionFailure)
			require.Len(t, recorded.Steps, 1)
			if check, ok := recorded.Steps[0].(*work.CheckStep); ok {
				require.NotNil(t, check.StartFailure)
				assert.Contains(t, check.StartFailure.Reason, failure.Error())
			} else {
				_, failed := recorded.Steps[0].(*work.AgentReviewStep).Execution.Process.(*work.StartFailedProcess)
				assert.True(t, failed, "no live execution may be left when its header prevented launch")
			}
		})
	}
}

type unstartedReviewLauncher struct{ called bool }

func (l *unstartedReviewLauncher) Run(context.Context, agentexec.Command, agentexec.LaunchOptions) error {
	l.called = true
	return errors.New("unexpected agent launch")
}

func TestInteractionJSONRequiresExplicitContainment(t *testing.T) {
	valid := `{"Owner":{"RepositoryID":"alpha","TaskID":"op-review","Workflow":0,"Phase":2,"Step":0},"Version":4,"Kind":"manual_review"}`
	var id InteractionID
	require.NoError(t, json.Unmarshal([]byte(valid), &id))
	assert.Equal(t, work.AttemptRef{RepositoryID: "alpha", TaskID: "op-review", Workflow: 0, Phase: 2, Step: routeNumber(0)}, id.Owner)
	assert.Equal(t, work.StateVersion(4), id.Version)
	assert.Equal(t, InteractionManualReview, id.Kind)
	for _, input := range []string{
		`{}`, `{"Owner":null,"Version":4,"Kind":"manual_review"}`,
		`{"Owner":{"RepositoryID":"alpha","TaskID":"op-review","Phase":2},"Version":4,"Kind":"manual_review"}`,
		`{"Owner":{"RepositoryID":"alpha","TaskID":"op-review","Workflow":null,"Phase":2},"Version":4,"Kind":"manual_review"}`,
		`{"Owner":{"RepositoryID":"alpha","TaskID":"op-review","Workflow":0,"Phase":2},"Kind":"manual_review"}`,
		`{"Owner":{"RepositoryID":"alpha","TaskID":"op-review","Workflow":0,"Phase":2},"Version":4,"Kind":"manual_review","extra":true}`,
	} {
		assert.Error(t, json.Unmarshal([]byte(input), &id))
	}
	var decision FindingDecision
	require.NoError(t, json.Unmarshal([]byte(`{"Ref":{"Phase":2,"Step":0,"Finding":0},"Action":"waive","Reason":"Accepted risk"}`), &decision))
	assert.Equal(t, work.FindingRef{Phase: 2, Step: 0, Finding: 0}, decision.Ref)
	assert.Equal(t, DecisionWaive, decision.Action)
	assert.Equal(t, "Accepted risk", decision.Reason)
	for _, input := range []string{`{"Ref":{"Phase":2,"Step":0},"Action":"waive"}`, `{"Ref":{"Phase":2,"Step":0,"Finding":null},"Action":"waive"}`, `{"Ref":{"Phase":2,"Step":0,"Finding":0}}`, `{"Ref":{"Phase":2,"Step":0,"Finding":0},"Action":"waive","extra":true}`} {
		assert.Error(t, json.Unmarshal([]byte(input), &decision))
	}
}

type interactionBackend struct {
	ReviewLifecycleBackend
	item task.Task
}

func (b interactionBackend) Get(context.Context, string) (task.Task, error) {
	return b.item.Clone(), nil
}

type interactionPublicationGit struct {
	FinalizationGit
	boundary *candidateReadinessGit
}

func (g interactionPublicationGit) ObservePublicationInput(ctx context.Context, req gitmeta.PublicationInputRequest) (gitmeta.PublicationInputObservation, error) {
	err := g.boundary.VerifyRevision(ctx, req.Directory, gitmeta.CheckoutIdentity{Commit: req.Reviewed.Commit, Ref: "refs/heads/" + req.Branch}, req.Reviewed)
	return gitmeta.PublicationInputObservation{Revision: req.Reviewed, Relationship: "reviewed"}, err
}

func (b interactionBackend) List(context.Context) ([]task.Task, error) {
	return []task.Task{b.item.Clone()}, nil
}

func (g interactionPublicationGit) ObserveDestination(ctx context.Context, dir, branch string) (work.Destination, error) {
	return g.boundary.ObserveDestination(ctx, dir, branch)
}
