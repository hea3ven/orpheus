package workflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutputCaptureRefusesUnsafeOrStaleEffortsBeforeGitOrPersistence(t *testing.T) {
	for _, scenario := range []string{"running with handoff", "failed process", "interrupted process", "no handoff", "handoff after exit", "wrong workflow", "wrong task", "wrong phase", "review step callback"} {
		t.Run(scenario, func(t *testing.T) {
			snapshot := outputCaptureSnapshot()
			phase := snapshot.Workflows[0].Phases[1].(*work.ImplementationPhase)
			ref := work.AttemptRef{RepositoryID: "repo", TaskID: "task", Phase: 1}
			switch scenario {
			case "running with handoff":
				phase.Execution.Process = &work.RunningProcess{}
			case "failed process":
				phase.Execution.Process.(*work.ExitedProcess).Code = captureNumber(1)
			case "interrupted process":
				phase.Execution.Process = &work.InterruptedProcess{At: captureTime(3), Reason: "supervision lost"}
			case "no handoff":
				phase.Completion = nil
			case "handoff after exit":
				phase.Completion.CompletedAt = captureTime(4)
			case "wrong workflow":
				ref.Workflow = 1
			case "wrong task":
				ref.TaskID = "another task"
			case "wrong phase":
				ref.Phase = 0
			case "review step callback":
				ref.Step = captureNumber(0)
			}
			writes, gitEffects := 0, 0
			store := outputCaptureStore{load: func(string, string) (work.State, error) { return snapshot, nil }, save: func(work.State, work.StateVersion) (work.StateVersion, error) { writes++; return 0, nil }}
			git := outputCaptureGit{capture: func(context.Context, gitmeta.RevisionCapture) (gitmeta.Revision, error) {
				gitEffects++
				return gitmeta.Revision{}, nil
			}, verify: func(context.Context, string, gitmeta.CheckoutIdentity, gitmeta.Revision) error {
				gitEffects++
				return nil
			}}
			service := workflow.ImplementationService{Paths: outputCapturePaths(t), Store: store, Git: git}

			result, err := service.Capture(context.Background(), ref)

			require.Error(t, err)
			assert.Empty(t, result.Commit)
			assert.Zero(t, writes)
			assert.Zero(t, gitEffects)
			assert.Nil(t, phase.Output)
			assert.Equal(t, work.StateVersion(1), snapshot.Version)
		})
	}
}

func TestOutputCaptureRetainsGitRecoveryAndOwnHandoffsWithoutPublication(t *testing.T) {
	saveFailure := errors.New("atomic save unavailable")
	gitFailure := errors.New("hook failure")
	verifyFailure := errors.New("checkout differs from retained output")
	ref := work.AttemptRef{RepositoryID: "repo", TaskID: "task", Phase: 1}
	var recorded *work.CodeOutput
	version, captures, verifications, commits := work.StateVersion(1), 0, 0, 0
	repair, persistedRepair := false, false
	store := outputCaptureStore{
		load: func(string, string) (work.State, error) {
			snapshot := outputCaptureSnapshot()
			snapshot.Version = version
			if recorded != nil {
				captured := *recorded
				snapshot.Workflows[0].Phases[1].(*work.ImplementationPhase).Output = &captured
			}
			if repair {
				p := *snapshot.Workflows[0].Phases[1].(*work.ImplementationPhase)
				p.Input, p.Output = &work.OutputRef{Phase: 1}, nil
				completion := *p.Completion
				completion.Summary, completion.Description = "fix: unchanged assessment", "Repair rationale"
				completion.CompletedAt = captureTime(5)
				p.Completion = &completion
				p.Execution.StartedAt = captureTime(4)
				p.Execution.Process = &work.ExitedProcess{At: captureTime(6), Code: captureNumber(0)}
				if persistedRepair {
					p.Output = &work.ReusedOutput{Input: *p.Input}
				}
				snapshot.Workflows[0].Phases = append(snapshot.Workflows[0].Phases, &p)
			}
			return snapshot, nil
		},
		save: func(snapshot work.State, basis work.StateVersion) (work.StateVersion, error) {
			require.Equal(t, version, basis)
			require.NoError(t, snapshot.Validate())
			if saveFailure != nil {
				return 0, saveFailure
			}
			if repair {
				require.Equal(t, &work.ReusedOutput{Input: work.OutputRef{Phase: 1}}, snapshot.Workflows[0].Phases[2].(*work.ImplementationPhase).Output)
				persistedRepair = true
			} else {
				captured := *snapshot.Workflows[0].Phases[1].(*work.ImplementationPhase).Output.(*work.CodeOutput)
				recorded = &captured
			}
			version++
			return version, nil
		},
	}
	var retained gitmeta.Revision
	git := outputCaptureGit{
		capture: func(_ context.Context, opts gitmeta.RevisionCapture) (gitmeta.Revision, error) {
			captures++
			assert.Equal(t, "/fixture/work", opts.Directory)
			assert.Equal(t, "refs/heads/task-branch", opts.Input.Ref)
			if repair {
				assert.Equal(t, "R1", opts.Input.Commit)
				assert.Equal(t, "fix: unchanged assessment\n\nRepair rationale", opts.Message)
				return gitmeta.Revision{Commit: "R1", Tree: "T1", RetainedRef: opts.Ref + "/output", Reused: true}, nil
			}
			assert.Equal(t, "B", opts.Input.Commit)
			assert.Equal(t, "feat: original intent\n\nFeature rationale", opts.Message)
			if gitFailure != nil {
				return gitmeta.Revision{}, gitFailure
			}
			if retained.Commit == "" {
				commits++
				retained = gitmeta.Revision{Commit: "R1", Tree: "T1", RetainedRef: opts.Ref + "/output"}
			}
			return retained, nil
		},
		verify: func(_ context.Context, dir string, expected gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
			verifications++
			assert.Equal(t, "/fixture/work", dir)
			assert.Equal(t, gitmeta.CheckoutIdentity{Commit: "R1", Ref: "refs/heads/task-branch"}, expected)
			assert.Equal(t, retained.Commit, revision.Commit)
			assert.Equal(t, retained.RetainedRef, revision.RetainedRef, "reuse must verify the original owner's retained ref")
			return verifyFailure
		},
	}
	service := workflow.ImplementationService{Paths: outputCapturePaths(t), Store: store, Git: git}
	_, err := service.Capture(context.Background(), ref)
	require.ErrorIs(t, err, gitFailure)
	assert.Nil(t, recorded)
	assert.Zero(t, verifications)
	gitFailure = nil
	_, err = service.Capture(context.Background(), ref)
	require.ErrorIs(t, err, verifyFailure)
	assert.Nil(t, recorded)
	verifyFailure = nil
	_, err = service.Capture(context.Background(), ref)
	require.ErrorIs(t, err, saveFailure)
	assert.ErrorContains(t, err, "retained Git output could not be saved")
	assert.Nil(t, recorded)
	unchanged, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Nil(t, unchanged.Workflows[0].Phases[1].(*work.ImplementationPhase).Output)
	saveFailure = nil
	result, err := service.Capture(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: "R1", Tree: "T1"}, RetainedRef: "refs/orpheus/revisions/7265706f/7461736b/0/1/output"}, result)
	assert.Equal(t, 1, commits, "a save failure leaves the same recoverable Git output")
	assert.Equal(t, work.StateVersion(2), version)
	captureCount := captures
	_, err = service.Capture(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, captureCount, captures, "already recorded output is reverified, not recaptured")
	assert.Equal(t, work.StateVersion(2), version, "a read-only retry must not save another version")

	repair, ref.Phase = true, 2
	_, err = service.Capture(context.Background(), ref)
	require.NoError(t, err)
	snapshot, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, "feat: original intent", snapshot.Workflows[0].FeatureCompletion().Summary)
	assert.Equal(t, "fix: unchanged assessment", snapshot.Workflows[0].Phases[2].(*work.ImplementationPhase).Completion.Summary)
	latest, ok := snapshot.Workflows[0].LatestOutput()
	require.True(t, ok)
	assert.Equal(t, work.OutputRef{Phase: 2}, latest)
	assert.Equal(t, work.StateVersion(3), version)
	assert.Equal(t, 1, commits)
	require.Len(t, snapshot.Workflows[0].Phases, 3, "capture/reuse must not invent review approval, consent, delivery or finalization")
}

type implementationRecordingStore struct {
	testing *testing.T
	data    []byte
	failure error
}

func newImplementationRecordingStore(t *testing.T, initial work.State) *implementationRecordingStore {
	t.Helper()
	data, err := workstate.Encode(initial)
	require.NoError(t, err)
	return &implementationRecordingStore{testing: t, data: data}
}

func (s *implementationRecordingStore) Load(_, _ string) (work.State, error) {
	return workstate.Decode(s.data)
}

func (s *implementationRecordingStore) Save(proposed work.State, expected work.StateVersion) (work.StateVersion, error) {
	if s.failure != nil {
		return 0, s.failure
	}
	previous, err := workstate.Decode(s.data)
	require.NoError(s.testing, err)
	require.Equal(s.testing, expected, previous.Version)
	require.Equal(s.testing, expected, proposed.Version)
	if err := proposed.ValidateSuccessor(&previous); err != nil {
		return 0, err
	}
	proposed.Version++
	data, err := workstate.Encode(proposed)
	if err != nil {
		return 0, err
	}
	s.data = data
	return proposed.Version, nil
}

type outputCaptureStore struct {
	load func(string, string) (work.State, error)
	save func(work.State, work.StateVersion) (work.StateVersion, error)
}

func (s outputCaptureStore) Load(repo, task string) (work.State, error) { return s.load(repo, task) }
func (s outputCaptureStore) Save(value work.State, version work.StateVersion) (work.StateVersion, error) {
	return s.save(value, version)
}

type outputCaptureGit struct {
	capture func(context.Context, gitmeta.RevisionCapture) (gitmeta.Revision, error)
	verify  func(context.Context, string, gitmeta.CheckoutIdentity, gitmeta.Revision) error
}

func (g outputCaptureGit) CaptureRevision(ctx context.Context, opts gitmeta.RevisionCapture) (gitmeta.Revision, error) {
	return g.capture(ctx, opts)
}
func (g outputCaptureGit) VerifyRevision(ctx context.Context, dir string, expected gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	return g.verify(ctx, dir, expected, revision)
}

func captureTime(minute int) time.Time { return time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC) }
func captureNumber(n int) *int         { return &n }

func outputCaptureSnapshot() work.State {
	preparedAt := captureTime(0)
	return work.State{RepositoryID: "repo", TaskID: "task", Version: 1, Workflows: []work.Workflow{{Phases: []work.Phase{
		&work.PreparationPhase{Context: &work.WorkContext{Directory: "/fixture/work", TaskBranch: "task-branch", CheckpointRef: "refs/orpheus/checkpoint/task", Baseline: work.CodeIdentity{Commit: "B", Tree: "TB"}}, EstablishedAt: &preparedAt},
		&work.ImplementationPhase{Execution: work.AgentExecution{Profile: "implementer", Command: "pi", StartedAt: captureTime(1), Process: &work.ExitedProcess{At: captureTime(3), Code: captureNumber(0)}}, Completion: &work.Completion{Summary: "feat: original intent", Description: "Feature rationale", DetailedDescription: "# Original feature", TechnicalExplanation: "Implementation notes", CompletedAt: captureTime(2)}},
	}}}}
}

func outputCapturePaths(t *testing.T) state.Paths {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
	return paths
}
