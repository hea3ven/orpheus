//go:build integration

package cli_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hea3ven/orpheus/internal/pullrequest"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Publication outcomes describe external effects, not workflow decisions. The
// real command and finalization service decide what to retry and persist.
type finalizationWorkflowFixture struct {
	*reviewWorkflowFixture
	publication *memoryPublicationGit
	pr          *memoryPublicationPR
	mutations   *memoryPublicationTasks
}

func newFinalizationFixture(t *testing.T, taskID string) *finalizationWorkflowFixture {
	t.Helper()
	base := newReviewDispatchFixture(t, taskID)
	f := &finalizationWorkflowFixture{reviewWorkflowFixture: base}
	f.publication = &memoryPublicationGit{memoryReviewCandidate: base.candidate, remote: make(map[string]string)}
	f.pr = &memoryPublicationPR{url: "https://github.test/org/alpha/pull/42"}
	f.mutations = &memoryPublicationTasks{memoryReviewTasks: base.tasks}
	f.options.Dependencies.FinalizationGit = f.publication
	f.options.Dependencies.SyncGit = f.publication
	f.options.Dependencies.CleanupGit = f.publication
	f.options.Dependencies.PRProvider = f.pr
	f.options.Dependencies.TaskBackendFactory = func(taskmodel.RepositorySource) (taskmodel.ReadBackend, error) { return f.mutations, nil }
	return f
}

func (f *finalizationWorkflowFixture) completion(taskID, branch, dir, summary, description string, status taskstate.RunStatus) {
	f.t.Helper()
	item := f.backend.tasks[taskID]
	item.Metadata = taskmodel.Metadata{taskmodel.MetadataBranch: branch, taskmodel.MetadataWorktree: dir}
	f.backend.tasks[taskID] = item
	f.git.targets[dir] = memoryGitTarget{branch: branch}
	run, err := f.taskStore.StartRun("alpha", taskID, taskstate.StartRunOptions{Agent: "recorder", Branch: branch, Worktree: dir})
	require.NoError(f.t, err)
	_, err = f.taskStore.CompleteRun("alpha", taskID, run.Attempt, taskstate.CompleteRunOptions{Summary: summary, Description: description, DetailedDescription: "Detailed PR body.", TechnicalExplanation: "Technical explanation."})
	require.NoError(f.t, err)
	if status != taskstate.RunStatusRunning {
		_, err = f.taskStore.FinishRun("alpha", taskID, run.Attempt, status)
		require.NoError(f.t, err)
	}
}

func (f *finalizationWorkflowFixture) passedReview(taskID string) {
	f.t.Helper()
	review, err := f.taskStore.StartReview("alpha", taskID)
	require.NoError(f.t, err)
	_, err = f.taskStore.FinishReview("alpha", taskID, review.Attempt, taskstate.ReviewStatusPassed)
	require.NoError(f.t, err)
}

func newMainFinalizationFixture(t *testing.T, taskID string) *finalizationWorkflowFixture {
	t.Helper()
	f := newFinalizationFixture(t, taskID)
	f.completion(taskID, "main", taskWorkflowRepoRoot, "Implement task done", "Commit reviewed repo-root changes.", taskstate.RunStatusSucceeded)
	f.passedReview(taskID)
	return f
}

func newFeatureFinalizationFixture(t *testing.T, taskID string, repoRoot bool) *finalizationWorkflowFixture {
	t.Helper()
	f := newFinalizationFixture(t, taskID)
	branch, dir := f.expectedTarget(taskID)
	if repoRoot {
		dir = taskWorkflowRepoRoot
	}
	f.completion(taskID, branch, dir, "Ready for PR", "Implemented task branch changes.", taskstate.RunStatusSucceeded)
	f.passedReview(taskID)
	f.options.TaskWorkingDirectory = dir
	return f
}

func (f *finalizationWorkflowFixture) assertUnpublished(taskID string) {
	f.t.Helper()
	state, item := f.loadFinalTask(taskID)
	assert.Equal(f.t, taskmodel.StatusInProgress, item.Status)
	assert.Empty(f.t, item.OrpheusMetadata().PRURL)
	assert.Empty(f.t, taskstate.FinalizationFacts(state).Commit)
	assert.Empty(f.t, f.candidate.commits)
	assert.Empty(f.t, f.publication.remote)
	assert.Empty(f.t, f.pr.created)
	assert.Empty(f.t, f.tasks.closed)
	assert.Equal(f.t, "initial-commit", f.candidate.head)
}

func (f *finalizationWorkflowFixture) assertPublishedPR(taskID string) {
	f.t.Helper()
	state, item := f.loadFinalTask(taskID)
	facts := taskstate.FinalizationFacts(state)
	target, ok := taskstate.GitFactsFor(state)
	require.True(f.t, ok)
	assert.Equal(f.t, taskmodel.StatusInProgress, item.Status)
	assert.Equal(f.t, f.pr.url, item.OrpheusMetadata().PRURL)
	require.NotEmpty(f.t, facts.Commit)
	assert.Equal(f.t, f.candidate.head, facts.Commit)
	assert.Equal(f.t, facts.Commit, f.publication.remote[target.Branch])
	assert.NotNil(f.t, facts.CommittedAt)
	assert.NotNil(f.t, facts.PushedAt)
	assert.Nil(f.t, facts.ClosedAt)
	assert.Empty(f.t, f.tasks.closed)
	require.Len(f.t, state.Runs, 1)
	assert.Equal(f.t, taskstate.RunStatusSucceeded, state.Runs[0].Status)
	require.NotNil(f.t, state.Runs[0].Completion)
	assert.Empty(f.t, state.Runs[0].Completion.Commit)
	assert.Equal(f.t, []string{f.pr.url}, f.mutations.prURLs)
}

type memoryPublicationTasks struct {
	*memoryReviewTasks
	closeError, metadataError error
	prURLs                    []string
}

func (b *memoryPublicationTasks) Close(ctx context.Context, id string) error {
	if b.closeError != nil {
		return b.closeError
	}
	return b.memoryReviewTasks.Close(ctx, id)
}
func (b *memoryPublicationTasks) SetPRURL(ctx context.Context, id, url string) error {
	if b.metadataError != nil {
		return b.metadataError
	}
	if err := b.memoryReviewTasks.SetPRURL(ctx, id, url); err != nil {
		return err
	}
	b.prURLs = append(b.prURLs, url)
	return nil
}

type memoryPublicationGit struct {
	*memoryReviewCandidate
	remote          map[string]string
	pushError       error
	mergeCommit     string
	cleanupError    error
	cleanupAttempts []string
	merges          int
}

func (g *memoryPublicationGit) PushDefaultBranch(_ context.Context, _ string, branch string) error {
	if g.pushError != nil {
		return g.pushError
	}
	g.pushes = append(g.pushes, branch)
	g.remote[branch] = g.head
	return nil
}
func (g *memoryPublicationGit) PushTaskBranch(ctx context.Context, dir, branch string) error {
	return g.PushDefaultBranch(ctx, dir, branch)
}
func (g *memoryPublicationGit) MergeTaskBranchIntoDestination(_ context.Context, repo taskmodel.Repository, destination, branch string) (string, error) {
	hasBranch := false
	for _, target := range g.targets {
		if target.branch == branch {
			hasBranch = true
			break
		}
	}
	if !hasBranch || g.hasCandidateChanges || len(g.commits) != 1 {
		return "", errors.New("merge requires committed task branch")
	}
	g.merges++
	g.mergeCommit = "merge-commit"
	g.head = g.mergeCommit
	g.targets[repo.Path] = memoryGitTarget{branch: destination}
	return g.mergeCommit, nil
}
func (g *memoryPublicationGit) ValidateRecordedDirectMerge(_ context.Context, _ taskmodel.Repository, destination, commit string) (bool, error) {
	if commit == "" || commit != g.mergeCommit || commit != g.head {
		return false, fmt.Errorf("unexpected recorded merge %q", commit)
	}
	return g.remote[destination] == commit, nil
}

type memoryPublicationPR struct {
	url            string
	existing       *pullrequest.CreateRequest
	created        []pullrequest.CreateRequest
	createError    error
	state          pullrequest.State
	statusReads    int
	findReads      int
	findError      error
	statusError    error
	statuses       map[string]pullrequest.State
	statusRequests []pullrequest.StatusByURLRequest
}

func (p *memoryPublicationPR) FindOpenByBranch(_ context.Context, req pullrequest.FindOpenByBranchRequest) (pullrequest.PullRequest, bool, error) {
	p.findReads++
	if p.findError != nil {
		return pullrequest.PullRequest{}, false, p.findError
	}
	if p.state != pullrequest.StateClosed && p.state != pullrequest.StateMerged && p.existing != nil && p.existing.RepositoryPath == req.RepositoryPath && p.existing.HeadBranch == req.HeadBranch && p.existing.BaseBranch == req.BaseBranch {
		return pullrequest.PullRequest{URL: p.url}, true, nil
	}
	return pullrequest.PullRequest{}, false, nil
}
func (p *memoryPublicationPR) Create(_ context.Context, req pullrequest.CreateRequest) (pullrequest.PullRequest, error) {
	if p.createError != nil {
		return pullrequest.PullRequest{}, p.createError
	}
	if p.existing != nil {
		return pullrequest.PullRequest{}, errors.New("duplicate PR creation")
	}
	p.created = append(p.created, req)
	p.existing = &req
	return pullrequest.PullRequest{URL: p.url}, nil
}
func (p *memoryPublicationPR) StatusByURL(_ context.Context, req pullrequest.StatusByURLRequest) (pullrequest.PullRequestStatus, error) {
	p.statusRequests = append(p.statusRequests, req)
	p.statusReads++
	if p.statusError != nil {
		return pullrequest.PullRequestStatus{}, p.statusError
	}
	if state, ok := p.statuses[req.URL]; ok {
		return pullrequest.PullRequestStatus{URL: req.URL, State: state}, nil
	}
	if req.URL != p.url || p.state == "" {
		return pullrequest.PullRequestStatus{}, errors.New("unexpected PR status lookup")
	}
	return pullrequest.PullRequestStatus{URL: p.url, State: p.state}, nil
}
