package workflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errPublicationPersistence = errors.New("fixture atomic persistence unavailable")

type publicationStore struct {
	*implementationRecordingStore
	deliveryFailures, closureFailures int
	cleanupLoadFailure                bool
}

func (s *publicationStore) Save(next work.State, expected work.StateVersion) (work.StateVersion, error) {
	old, err := s.implementationRecordingStore.Load(next.RepositoryID, next.TaskID)
	if err != nil {
		return 0, err
	}
	if s.deliveryFailures > 0 && firstPublication(old).Delivery == nil && firstPublication(next).Delivery != nil {
		s.deliveryFailures--
		return 0, errPublicationPersistence
	}
	if s.closureFailures > 0 && sourceClosure(old) == nil && sourceClosure(next) != nil {
		s.closureFailures--
		return 0, errPublicationPersistence
	}
	return s.implementationRecordingStore.Save(next, expected)
}
func (s *publicationStore) Load(repoID, taskID string) (work.State, error) {
	snapshot, err := s.implementationRecordingStore.Load(repoID, taskID)
	if err == nil && s.cleanupLoadFailure && sourceClosure(snapshot) != nil {
		return work.State{}, errPublicationPersistence
	}
	return snapshot, err
}
func firstPublication(snapshot work.State) *work.PublicationPhase {
	for _, phase := range snapshot.Workflows[0].Phases {
		if p, ok := phase.(*work.PublicationPhase); ok {
			return p
		}
	}
	return &work.PublicationPhase{}
}
func sourceClosure(snapshot work.State) *work.TaskClosure {
	for _, phase := range snapshot.Workflows[0].Phases {
		if p, ok := phase.(*work.FinalizationPhase); ok {
			return p.Closure
		}
	}
	return nil
}

type publicationBackend struct {
	items                                      []task.Task
	closed, urls                               []string
	metadataErr, closureErr, errorAfterClosure error
	wrongIdentity                              bool
}

func (b *publicationBackend) Get(_ context.Context, id string) (task.Task, error) {
	for _, item := range b.items {
		if item.ID == id {
			if item.Status == task.StatusClosed && b.errorAfterClosure != nil {
				return task.Task{}, b.errorAfterClosure
			}
			result := item.Clone()
			if b.wrongIdentity {
				result.ID = "another-task"
			}
			return result, nil
		}
	}
	return task.Task{}, task.ErrNotFound
}
func (b *publicationBackend) List(context.Context) ([]task.Task, error) {
	return append([]task.Task(nil), b.items...), nil
}
func (b *publicationBackend) Close(_ context.Context, id string) error {
	if b.closureErr != nil {
		return b.closureErr
	}
	for i := range b.items {
		if b.items[i].ID == id {
			b.items[i].Status = task.StatusClosed
			b.closed = append(b.closed, id)
			return nil
		}
	}
	return task.ErrNotFound
}
func (b *publicationBackend) SetPRURL(_ context.Context, id, url string) error {
	if b.metadataErr != nil {
		return b.metadataErr
	}
	for i := range b.items {
		if b.items[i].ID == id {
			b.items[i].Metadata[task.MetadataPRURL] = url
			b.urls = append(b.urls, url)
			return nil
		}
	}
	return task.ErrNotFound
}

type publicationGit struct {
	source                        gitmeta.Revision
	remote                        map[string]gitmeta.Revision
	taskBranch                    string
	verifyErr, pushErr, directErr error
	pushWrongTree                 bool
	localMerge                    string
	merges                        int
	pushes                        []string
	destinations                  []string
}

func (g *publicationGit) VerifyRevision(_ context.Context, _ string, identity gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	if g.verifyErr != nil {
		return g.verifyErr
	}
	if identity.Commit != g.source.Commit || identity.Ref != "refs/heads/"+g.taskBranch || revision != g.source {
		return errors.New("clean checkout or retention differs from reviewed input")
	}
	return nil
}
func (g *publicationGit) PushTaskBranch(_ context.Context, _ string, branch string) error {
	if g.pushErr != nil {
		return g.pushErr
	}
	g.pushes = append(g.pushes, branch)
	g.remote[branch] = g.source
	if g.pushWrongTree {
		g.remote[branch] = gitmeta.Revision{Commit: g.source.Commit, Tree: "foreign-tree"}
	}
	return nil
}
func (g *publicationGit) PublishedRevision(_ context.Context, _ string, branch string) (gitmeta.Revision, error) {
	actual, ok := g.remote[branch]
	if !ok {
		return gitmeta.Revision{}, errors.New("remote branch not observed")
	}
	return actual, nil
}
func (g *publicationGit) DirectRevisionDelivery(_ context.Context, _ task.Repository, _ string, branch, destination, commit, _ string, output gitmeta.Revision) (gitmeta.Revision, error) {
	if branch != g.taskBranch || destination == branch || commit != "destination" || output != g.source {
		return gitmeta.Revision{}, errors.New("direct delivery input differs from reviewed plan")
	}
	g.destinations = append(g.destinations, destination)
	if g.localMerge == "" {
		g.localMerge = "native-merge"
		g.merges++
	}
	if g.directErr != nil {
		return gitmeta.Revision{}, g.directErr
	}
	delivered := gitmeta.Revision{Commit: g.localMerge, Tree: output.Tree}
	g.remote[destination] = delivered
	return delivered, nil
}

type publicationPR struct {
	open               *pullrequest.CreateRequest
	url                string
	created            []pullrequest.CreateRequest
	findErr, statusErr error
	loseCreateResponse bool
}

func (p *publicationPR) FindOpenByBranch(_ context.Context, req pullrequest.FindOpenByBranchRequest) (pullrequest.PullRequest, bool, error) {
	if p.findErr != nil {
		return pullrequest.PullRequest{}, false, p.findErr
	}
	if p.open != nil && p.open.RepositoryPath == req.RepositoryPath && p.open.HeadBranch == req.HeadBranch && p.open.BaseBranch == req.BaseBranch {
		return pullrequest.PullRequest{URL: p.url}, true, nil
	}
	return pullrequest.PullRequest{}, false, nil
}
func (p *publicationPR) Create(_ context.Context, req pullrequest.CreateRequest) (pullrequest.PullRequest, error) {
	if p.open != nil {
		return pullrequest.PullRequest{}, errors.New("duplicate native PR creation")
	}
	p.created = append(p.created, req)
	p.open = &req
	if p.loseCreateResponse {
		p.loseCreateResponse = false
		return pullrequest.PullRequest{}, errors.New("lost PR creation response")
	}
	return pullrequest.PullRequest{URL: p.url}, nil
}
func (p *publicationPR) StatusByURL(_ context.Context, req pullrequest.StatusByURLRequest) (pullrequest.PullRequestStatus, error) {
	if p.statusErr != nil {
		return pullrequest.PullRequestStatus{}, p.statusErr
	}
	if p.open == nil || req.URL != p.url {
		return pullrequest.PullRequestStatus{}, errors.New("unknown native PR")
	}
	return pullrequest.PullRequestStatus{URL: p.url, State: pullrequest.StateOpen}, nil
}

type committedPublicationFixture struct {
	service workflow.FinalizationService
	store   *publicationStore
	backend *publicationBackend
	git     *publicationGit
	pr      *publicationPR
}

func newCommittedPublicationFixture(t *testing.T, authorized, direct, dedicated bool) *committedPublicationFixture {
	t.Helper()
	paths := outputCapturePaths(t)
	directory := "/fixture/repo"
	if dedicated {
		var err error
		directory, err = paths.DataPath("repos/alpha/worktrees/op-1")
		require.NoError(t, err)
	}
	history := testutil.ReviewableWork("alpha", "op-1", directory, "orpheus/op-1")
	history.Version = 1
	w := &history.Workflows[0]
	at := time.Date(2026, 1, 1, 0, 0, 4, 0, time.UTC)
	zero := 0
	w.ReviewContracts = []work.ReviewContract{{PipelineKey: "review", Slots: []work.ReviewSlot{{Kind: work.ReviewAgent, ConfigKey: "reviewer"}}}}
	w.Phases = append(w.Phases, &work.ReviewPhase{Contract: 0, Scope: work.ReviewScope{Requirements: "Task requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "release/next", Commit: "destination"}}, StartedAt: at, Steps: []work.ReviewStep{&work.AgentReviewStep{Slot: 0, Execution: work.AgentExecution{StartedAt: at.Add(time.Second), Process: &work.ExitedProcess{At: at.Add(2 * time.Second), Code: &zero}}}}})
	var target work.PublicationTarget = &work.PullRequestTarget{}
	if direct {
		target = &work.DirectMergeTarget{}
	}
	w.Phases = append(w.Phases, &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: 2}, Target: target, Content: "feat: exact reviewed title\n\nReviewed publication body"}})
	if authorized {
		require.NoError(t, history.AuthorizePublication(0, work.PublicationRef{Phase: 3}, at.Add(3*time.Second), "Explicit fixture grant, never inferred from review"))
	}
	store := &publicationStore{implementationRecordingStore: newImplementationRecordingStore(t, history)}
	backend := &publicationBackend{items: []task.Task{{ID: "op-1", Title: "Original task", Status: task.StatusInProgress, IssueType: task.IssueTypeTask, Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: directory}}}}
	git := &publicationGit{source: gitmeta.Revision{Commit: "fixture-output", Tree: "fixture-tree", RetainedRef: "refs/orpheus/fixture-output"}, taskBranch: "orpheus/op-1", remote: make(map[string]gitmeta.Revision)}
	pr := &publicationPR{url: "https://github.test/org/repo/pull/42"}
	repo := task.Repository{ID: "alpha", Path: "/fixture/repo", DefaultBranch: "main", TaskIDPrefix: "op"}
	service := workflow.FinalizationService{Paths: paths, Store: store, Git: git, PRProvider: pr, Sources: []task.RepositorySource{{Repository: repo}}, BackendFactory: func(task.RepositorySource) (workflow.FinalizationBackend, error) { return backend, nil }}
	return &committedPublicationFixture{service: service, store: store, backend: backend, git: git, pr: pr}
}
func (f *committedPublicationFixture) snapshot(t *testing.T) work.State {
	t.Helper()
	snapshot, err := f.store.implementationRecordingStore.Load("alpha", "op-1")
	require.NoError(t, err)
	return snapshot
}

func TestFinalizeRequiresExactPlanConsentAndSafeCommittedInputBeforeEffects(t *testing.T) {
	for _, scenario := range []string{"no consent", "live handoff", "new local work", "metadata mismatch", "wrong clean HEAD", "blocked source", "wrong source identity"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCommittedPublicationFixture(t, scenario != "no consent", false, false)
			snapshot := f.snapshot(t)
			switch scenario {
			case "live handoff":
				snapshot.Workflows[0].Phases = snapshot.Workflows[0].Phases[:2]
				p := snapshot.Workflows[0].Phases[1].(*work.ImplementationPhase)
				p.Execution.Process = &work.RunningProcess{}
				p.Output = nil
			case "new local work":
				snapshot.Workflows[0].Phases = append(snapshot.Workflows[0].Phases, &work.ImplementationPhase{Input: &work.OutputRef{Phase: 1}, Execution: work.AgentExecution{StartedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Process: &work.RunningProcess{}}})
			case "metadata mismatch":
				f.backend.items[0].Metadata[task.MetadataBranch] = "other"
			case "wrong clean HEAD":
				f.git.verifyErr = errors.New("clean checkout is not the expected reviewed commit")
			case "blocked source":
				f.backend.items[0].Status = task.StatusBlocked
			case "wrong source identity":
				f.backend.wrongIdentity = true
			}
			data, err := workstate.Encode(snapshot)
			require.NoError(t, err)
			f.store.data = data
			before := f.snapshot(t)
			_, err = f.service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
			require.Error(t, err)
			if scenario == "no consent" || scenario == "live handoff" {
				assert.ErrorIs(t, err, workflow.ErrPublicationConsentRequired)
			}
			assert.Equal(t, before, f.snapshot(t))
			assert.Empty(t, f.git.pushes)
			assert.Zero(t, f.git.merges)
			assert.Empty(t, f.pr.created)
			assert.Empty(t, f.backend.urls)
			assert.Empty(t, f.backend.closed)
		})
	}
}

func TestFinalizeCommittedPRRecoversLostCreationAndPersistenceWithoutAnotherImplementationRevision(t *testing.T) {
	for _, failure := range []string{"lost creation response", "native status unavailable", "delivery save unavailable", "metadata update unavailable"} {
		t.Run(failure, func(t *testing.T) {
			f := newCommittedPublicationFixture(t, true, false, false)
			grant := *firstPublication(f.snapshot(t)).Consent
			switch failure {
			case "lost creation response":
				f.pr.loseCreateResponse = true
			case "native status unavailable":
				f.pr.statusErr = errors.New("provider status unavailable")
			case "delivery save unavailable":
				f.store.deliveryFailures = 1
			case "metadata update unavailable":
				f.backend.metadataErr = errors.New("source metadata unavailable")
			}
			_, err := f.service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
			require.Error(t, err)
			require.Len(t, f.pr.created, 1)
			assert.Equal(t, "feat: exact reviewed title", f.pr.created[0].Title)
			assert.Equal(t, "Reviewed publication body", f.pr.created[0].Body)
			assert.Equal(t, "release/next", f.pr.created[0].BaseBranch)
			assert.Equal(t, "orpheus/op-1", f.pr.created[0].HeadBranch)
			assert.Empty(t, f.backend.closed)
			if failure != "metadata update unavailable" {
				assert.Nil(t, firstPublication(f.snapshot(t)).Delivery)
			} else {
				require.NotNil(t, firstPublication(f.snapshot(t)).Delivery)
			}
			f.pr.statusErr = nil
			f.backend.metadataErr = nil
			f.service.Sources[0].Repository.DefaultBranch = "new-configured-default"

			result, err := f.service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})

			require.NoError(t, err)
			assert.Equal(t, "https://github.test/org/repo/pull/42", result.PRURL)
			assert.Equal(t, "fixture-output", result.Finalization.Commit)
			assert.Equal(t, "release/next", result.Finalization.DestinationBranch, "retry must consume the plan, not current configuration")
			assert.Equal(t, task.StatusInProgress, result.Task.Status)
			assert.Len(t, f.pr.created, 1)
			assert.Equal(t, []string{f.pr.url}, f.backend.urls)
			saved := f.snapshot(t)
			assert.Equal(t, grant, *firstPublication(saved).Consent)
			require.Len(t, saved.Workflows[0].Phases, 5)
			assert.IsType(t, &work.ExternalReviewPhase{}, saved.Workflows[0].LastPhase())
			assert.Equal(t, "feat: fixture intent", saved.Workflows[0].FeatureCompletion().Summary)
			assert.Equal(t, "fixture-output", f.git.source.Commit)
			assert.Equal(t, "fixture-tree", f.git.remote["orpheus/op-1"].Tree)
		})
	}
}

func TestFinalizeDoesNotTurnProviderOrRemoteUncertaintyIntoPublication(t *testing.T) {
	lookupFailure := errors.New("provider lookup unavailable")
	for _, scenario := range []string{"provider lookup error", "remote tree mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCommittedPublicationFixture(t, true, false, false)
			if scenario == "provider lookup error" {
				f.pr.findErr = lookupFailure
			} else {
				f.git.pushWrongTree = true
			}
			before := f.snapshot(t)
			_, err := f.service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
			require.Error(t, err)
			if scenario == "provider lookup error" {
				assert.ErrorIs(t, err, lookupFailure)
			} else {
				assert.Contains(t, err.Error(), "origin branch differs")
			}
			assert.Equal(t, before, f.snapshot(t))
			assert.Empty(t, f.pr.created, "lookup error is not confirmed absence")
			assert.Empty(t, f.backend.urls)
			assert.Empty(t, f.backend.closed)
		})
	}
}

func TestFinalizeDirectDeliveryKeepsLocalMergeRemoteDeliveryAndSourceClosureIndependent(t *testing.T) {
	for _, failure := range []string{"native push failed", "source close failed", "closure save failed", "source closure inspection failed"} {
		t.Run(failure, func(t *testing.T) {
			f := newCommittedPublicationFixture(t, true, true, false)
			switch failure {
			case "native push failed":
				f.git.directErr = errors.New("destination push unavailable")
			case "source close failed":
				f.backend.closureErr = errors.New("source close unavailable")
			case "closure save failed":
				f.store.closureFailures = 1
			case "source closure inspection failed":
				f.backend.errorAfterClosure = errors.New("source read unavailable")
			}
			_, err := f.service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
			require.Error(t, err)
			assert.Nil(t, sourceClosure(f.snapshot(t)))
			if failure == "native push failed" {
				assert.Empty(t, f.backend.closed)
				assert.Nil(t, firstPublication(f.snapshot(t)).Delivery)
			} else {
				require.NotNil(t, firstPublication(f.snapshot(t)).Delivery)
			}
			assert.Empty(t, f.pr.created)
			f.git.directErr = nil
			f.backend.closureErr = nil
			f.backend.errorAfterClosure = nil

			result, err := f.service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})

			require.NoError(t, err)
			assert.Equal(t, task.StatusClosed, result.Task.Status)
			assert.Equal(t, "native-merge", result.Finalization.MergeCommit)
			assert.Equal(t, "fixture-output", result.Finalization.Commit)
			assert.Equal(t, "release/next", result.Finalization.DestinationBranch)
			assert.Nil(t, result.WorktreeCleanup, "repository-root checkout is never removed")
			assert.Equal(t, 1, f.git.merges)
			assert.Equal(t, []string{"op-1"}, f.backend.closed)
			require.NotNil(t, sourceClosure(f.snapshot(t)))
			assert.Equal(t, "fixture-tree", f.git.remote["release/next"].Tree)
			assert.Equal(t, "fixture-output", f.git.source.Commit)
		})
	}
}

func TestFinalizeInferenceUsesRecordedDirectoryAndRejectsAmbiguity(t *testing.T) {
	f := newCommittedPublicationFixture(t, true, false, false)
	// An irrelevant source item must not render a branch template or invent a
	// publication. The sole authorized plan in this directory remains inferable.
	f.backend.items = append(f.backend.items, task.Task{ID: "op-unrelated", Title: "Other", Status: task.StatusOpen})
	// This semantic store owns only one document; use an explicit absence for the
	// unrelated task instead of returning the other task's persisted authority.
	boundary := publicationInferenceStore{WorkStore: f.store}
	f.service.Store = boundary
	result, err := f.service.Finalize(context.Background(), workflow.FinalizeOptions{CWD: "/fixture/repo/subdir"})
	require.NoError(t, err)
	assert.Equal(t, "op-1", result.Task.ID)
	f = newCommittedPublicationFixture(t, true, false, false)
	_, err = f.service.Finalize(context.Background(), workflow.FinalizeOptions{CWD: "/fixture/elsewhere"})
	require.ErrorContains(t, err, "specify a task ID")
	assert.Empty(t, f.pr.created)
}

type publicationInferenceStore struct{ workflow.WorkStore }

func (s publicationInferenceStore) Load(repoID, taskID string) (work.State, error) {
	if taskID == "op-unrelated" {
		return work.State{RepositoryID: repoID, TaskID: taskID}, nil
	}
	return s.WorkStore.Load(repoID, taskID)
}
