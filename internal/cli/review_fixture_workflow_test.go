//go:build integration

package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/cli"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real CLI, workflow, pipeline and stores own all review decisions. Only
// candidate contents, commands, agents and task-source mutations are supplied.
type reviewWorkflowFixture struct {
	*taskWorkflowFixture
	tasks      *memoryReviewTasks
	candidate  *memoryReviewCandidate
	checks     map[string][]checkResult
	checkCalls []string
}

type checkResult struct {
	code           int
	err            error
	stdout, stderr string
}
type checkExit int

func (e checkExit) Error() string { return fmt.Sprintf("exit status %d", e) }
func (e checkExit) ExitCode() int { return int(e) }

func newReviewWorkflowFixture(t *testing.T, taskID, summary, description string) *reviewWorkflowFixture {
	t.Helper()
	f := newReviewDispatchFixture(t, taskID)
	f.candidate.contents = "reviewed\n"
	recordMainCompletion(t, f.paths, "alpha", taskID, taskWorkflowRepoRoot, summary, description)
	return f
}

func newReviewDispatchFixture(t *testing.T, taskID string) *reviewWorkflowFixture {
	t.Helper()
	item := anInProgressTask(taskID)
	item.Title = "Ready for task done"
	item.Metadata = taskmodel.Metadata{taskmodel.MetadataBranch: "main", taskmodel.MetadataWorktree: taskWorkflowRepoRoot}
	base := newTaskWorkflowFixture(t, item)
	f := &reviewWorkflowFixture{taskWorkflowFixture: base, checks: make(map[string][]checkResult)}
	f.tasks = &memoryReviewTasks{memoryTaskBackend: base.backend}
	f.candidate = &memoryReviewCandidate{memoryDispatchGit: base.git, head: "initial-commit", contents: "fail\n"}
	base.git.targets[taskWorkflowRepoRoot] = memoryGitTarget{branch: "main"}
	base.git.hasCandidateChanges = true
	f.options.Dependencies.TaskBackendFactory = func(taskmodel.RepositorySource) (taskmodel.ReadBackend, error) { return f.tasks, nil }
	f.options.Dependencies.ReviewPipeline = review.RunPipeline
	f.options.Dependencies.FinalizationGit = f.candidate
	f.options.Dependencies.PRProvider = &memoryReviewPR{}
	f.options.Dependencies.ReviewStatus = f.candidate.shortStatus
	f.options.Dependencies.ReviewEffects = review.Effects{CaptureCandidate: f.candidate.capture, RunCommand: f.runCheck}
	t.Cleanup(func() {
		for name, outcomes := range f.checks {
			assert.Empty(t, outcomes, "unused check outcomes for %s", name)
		}
	})
	return f
}

func (f *reviewWorkflowFixture) run(input string, args ...string) (string, string) {
	f.t.Helper()
	stdout, stderr, err := f.runError(input, args...)
	require.NoError(f.t, err, "command %v; stderr: %s", args, stderr)
	return stdout, stderr
}
func (f *reviewWorkflowFixture) runError(input string, args ...string) (string, string, error) {
	f.t.Helper()
	command := cli.NewRootCommandWithOptions(f.options)
	var stdout, stderr bytes.Buffer
	command.SetIn(strings.NewReader(input))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}
func (f *reviewWorkflowFixture) pipelines(name string, pipelines map[string][]map[string]any) {
	f.t.Helper()
	configured := map[string]any{}
	for key, steps := range pipelines {
		configured[key] = map[string]any{"steps": steps}
	}
	f.setConfig("reviews", map[string]any{"default_pipeline": name, "pipelines": configured})
}
func (f *reviewWorkflowFixture) budget(attempts int) {
	f.t.Helper()
	if f.config["reviews"] == nil {
		f.pipelines("default", map[string][]map[string]any{"default": {{"kind": "manual", "name": "local-review"}}})
	}
	f.config["reviews"].(map[string]any)["max_autonomous_review_attempts"] = attempts
	f.saveConfig()
}
func (f *reviewWorkflowFixture) check(name string, outcomes ...checkResult) string {
	f.checks[name] = append(f.checks[name], outcomes...)
	return name
}
func (f *reviewWorkflowFixture) runCheck(opts review.CommandOptions) (*int, error) {
	name := opts.Step.Command
	outcomes := f.checks[name]
	if len(outcomes) == 0 {
		return nil, fmt.Errorf("unexpected review command %q", name)
	}
	result := outcomes[0]
	f.checks[name] = outcomes[1:]
	f.checkCalls = append(f.checkCalls, name)
	if result.err != nil {
		return nil, result.err
	}
	if _, err := fmt.Fprint(opts.Stdout, result.stdout); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprint(opts.Stderr, result.stderr); err != nil {
		return nil, err
	}
	if result.code != 0 {
		return &result.code, checkExit(result.code)
	}
	return &result.code, nil
}

type memoryReviewTasks struct {
	*memoryTaskBackend
	created     []taskmodel.CreateOptions
	createError error
	closed      []string
}

func (b *memoryReviewTasks) Create(_ context.Context, opts taskmodel.CreateOptions) (taskmodel.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.createError != nil {
		return taskmodel.Task{}, b.createError
	}
	id := fmt.Sprintf("op-%d", 41+len(b.created))
	item := taskmodel.Task{ID: id, Title: opts.Title, Description: opts.Description, AcceptanceCriteria: opts.AcceptanceCriteria, IssueType: opts.IssueType, Status: taskmodel.StatusOpen}
	b.tasks[id] = item.Clone()
	b.created = append(b.created, opts)
	return item.Clone(), nil
}
func (b *memoryReviewTasks) Close(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	item, ok := b.tasks[id]
	if !ok {
		return taskmodel.ErrNotFound
	}
	item.Status = taskmodel.StatusClosed
	b.tasks[id] = item
	b.closed = append(b.closed, id)
	return nil
}

// The review fixture supports default-branch and PR publication after approval.
// Direct merge and publication retry contracts live outside these scenarios.
type memoryReviewCandidate struct {
	*memoryDispatchGit
	head, contents string
	staged         bool
	commits        []string
	pushes         []string
}

func (g *memoryReviewCandidate) shortStatus(context.Context, string) (string, error) {
	if g.hasCandidateChanges {
		return "?? reviewed.txt\n", nil
	}
	return "", nil
}
func (g *memoryReviewCandidate) capture(context.Context, string, *slog.Logger, ...slog.Attr) (review.CandidateCheck, error) {
	contents := g.contents
	return func() error {
		if contents != g.contents {
			g.contents = contents
			return errors.New("review step mutated candidate changes; restored the pre-step snapshot and marked review failed")
		}
		return nil
	}, nil
}
func (g *memoryReviewCandidate) CurrentBranch(ctx context.Context, dir string) (string, error) {
	return g.memoryDispatchGit.CurrentBranch(ctx, dir)
}
func (g *memoryReviewCandidate) HasWorkingTreeChanges(ctx context.Context, dir string) (bool, error) {
	return g.memoryDispatchGit.HasWorkingTreeChanges(ctx, dir)
}
func (g *memoryReviewCandidate) HeadCommit(context.Context, string) (string, error) {
	return g.head, nil
}
func (g *memoryReviewCandidate) StageAll(context.Context, string) error { g.staged = true; return nil }
func (g *memoryReviewCandidate) Commit(_ context.Context, _ string, message string) (string, error) {
	if !g.staged || !g.hasCandidateChanges {
		return "", errors.New("commit requires staged candidate changes")
	}
	g.commits = append(g.commits, message)
	g.head = fmt.Sprintf("commit-%d", len(g.commits))
	g.staged = false
	g.hasCandidateChanges = false
	return g.head, nil
}
func (g *memoryReviewCandidate) PushDefaultBranch(_ context.Context, _ string, branch string) error {
	g.pushes = append(g.pushes, branch)
	return nil
}
func (g *memoryReviewCandidate) VerifyCommit(_ context.Context, _ string, commit, parent, message string) error {
	if commit != g.head || parent != "initial-commit" || len(g.commits) != 1 || g.commits[0] != message {
		return errors.New("commit does not match recorded intent")
	}
	return nil
}

func recordMainCompletion(t *testing.T, paths state.Paths, repoID string, taskID string, repoPath string, summary string, description string) {
	t.Helper()
	store := taskstate.NewStore(paths)
	attempt, err := store.StartRun(repoID, taskID, taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: repoPath,
	})
	if err != nil {
		t.Fatalf("start main run: %v", err)
	}
	if _, err := store.CompleteRun(repoID, taskID, attempt.Attempt, taskstate.CompleteRunOptions{
		Summary:              summary,
		Description:          description,
		DetailedDescription:  "Detailed PR body.",
		TechnicalExplanation: "Technical explanation.",
	}); err != nil {
		t.Fatalf("complete main run: %v", err)
	}
	if _, err := store.FinishRun(repoID, taskID, attempt.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish main run: %v", err)
	}
}

func recordReviewFollowUpCompletion(
	t *testing.T,
	paths state.Paths,
	repoID string,
	taskID string,
	repoPath string,
	reviewAttempt int,
	summary string,
	description string,
) {
	t.Helper()
	store := taskstate.NewStore(paths)
	attempt, err := store.StartRun(repoID, taskID, taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: repoPath,
		ReviewFollowUp: &taskstate.ReviewFollowUp{
			ReviewAttempt:  reviewAttempt,
			FindingIndexes: []int{0},
		},
	})
	if err != nil {
		t.Fatalf("start review follow-up run: %v", err)
	}
	if _, err := store.CompleteRun(repoID, taskID, attempt.Attempt, taskstate.CompleteRunOptions{
		Summary:              summary,
		Description:          description,
		DetailedDescription:  "Detailed follow-up body.",
		TechnicalExplanation: "Technical explanation.",
	}); err != nil {
		t.Fatalf("complete review follow-up run: %v", err)
	}
	if _, err := store.FinishRun(repoID, taskID, attempt.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish review follow-up run: %v", err)
	}
}

func (f *reviewWorkflowFixture) reviewers(outcomes ...semanticAgentOutcome) {
	f.t.Helper()
	f.configureAgentProfiles(agent.AgentDefaults{Implementer: "implementer", Reviewer: "reviewer"}, map[string]agent.Profile{
		"implementer": {Command: "unused-implementer"},
		"reviewer":    {Command: "review-agent", Args: []string{"{{session_name}} - {{prompt}}"}},
	})
	for i := range outcomes {
		outcomes[i].review = true
	}
	f.agent.outcomes = append(f.agent.outcomes, outcomes...)
}
func (f *reviewWorkflowFixture) repairExitsWithoutCompletion() {
	f.t.Helper()
	f.configureAgentProfiles(agent.AgentDefaults{Implementer: "followup", Reviewer: "reviewer"}, map[string]agent.Profile{"followup": {Command: "followup-agent"}, "reviewer": {Command: "review-agent"}})
	f.agent.outcomes = append(f.agent.outcomes, semanticAgentOutcome{exitWithoutCompletion: true, captureContext: true})
}

type memoryReviewPR struct{ created *pullrequest.CreateRequest }

func (p *memoryReviewPR) FindOpenByBranch(_ context.Context, req pullrequest.FindOpenByBranchRequest) (pullrequest.PullRequest, bool, error) {
	if p.created != nil && p.created.RepositoryPath == req.RepositoryPath && p.created.HeadBranch == req.HeadBranch && p.created.BaseBranch == req.BaseBranch {
		return pullrequest.PullRequest{URL: "https://github.test/org/alpha/pull/2"}, true, nil
	}
	return pullrequest.PullRequest{}, false, nil
}
func (p *memoryReviewPR) Create(_ context.Context, req pullrequest.CreateRequest) (pullrequest.PullRequest, error) {
	if p.created != nil {
		return pullrequest.PullRequest{}, errors.New("unexpected duplicate PR creation")
	}
	p.created = &req
	return pullrequest.PullRequest{URL: "https://github.test/org/alpha/pull/2"}, nil
}
func (*memoryReviewPR) StatusByURL(context.Context, pullrequest.StatusByURLRequest) (pullrequest.PullRequestStatus, error) {
	return pullrequest.PullRequestStatus{}, errors.New("unexpected PR status lookup")
}
func (b *memoryReviewTasks) UpdateGitFacts(_ context.Context, id, branch, worktree string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	item, ok := b.tasks[id]
	if !ok {
		return taskmodel.ErrNotFound
	}
	if item.OrpheusMetadata().PRURL != "" {
		return taskmodel.MutationConflictError{TaskID: id, Reason: "already published"}
	}
	item.Metadata[taskmodel.MetadataBranch] = branch
	item.Metadata[taskmodel.MetadataWorktree] = worktree
	b.tasks[id] = item
	return nil
}
func (b *memoryReviewTasks) SetPRURL(_ context.Context, id, url string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	item, ok := b.tasks[id]
	if !ok {
		return taskmodel.ErrNotFound
	}
	item.Metadata[taskmodel.MetadataPRURL] = url
	b.tasks[id] = item
	return nil
}
func (g *memoryReviewCandidate) MaterializeTaskBranch(_ context.Context, repo taskmodel.Repository, _ string, branch string, _ state.Paths) (gitmeta.TaskWorktreeSetupResult, error) {
	g.targets[repo.Path] = memoryGitTarget{branch: branch}
	return gitmeta.TaskWorktreeSetupResult{Branch: branch, WorktreePath: repo.Path}, nil
}
func (g *memoryReviewCandidate) PushTaskBranch(ctx context.Context, dir, branch string) error {
	return g.PushDefaultBranch(ctx, dir, branch)
}

func (f *reviewWorkflowFixture) completingRepairs(name string, count int, repair bool) {
	f.t.Helper()
	f.configureImplementer(name, agent.Profile{Command: "unused-implementer"})
	for i := 0; i < count; i++ {
		completion := aRepairCompletion()
		outcome := semanticAgentOutcome{completion: &completion}
		if repair && i == count-1 {
			outcome.mutateCandidate = func() { f.candidate.contents = "pass\n" }
		}
		f.agent.outcomes = append(f.agent.outcomes, outcome)
	}
}
func (f *reviewWorkflowFixture) candidateCheck() string {
	f.options.Dependencies.ReviewEffects.RunCommand = func(opts review.CommandOptions) (*int, error) {
		if opts.Step.Command != "candidate-check" {
			return nil, fmt.Errorf("unexpected candidate check %q", opts.Step.Command)
		}
		f.checkCalls = append(f.checkCalls, opts.Step.Name)
		code := 0
		if f.candidate.contents != "pass\n" {
			code = 7
			return &code, checkExit(code)
		}
		return &code, nil
	}
	return "candidate-check"
}

var _ workflow.FinalizationGit = (*memoryReviewCandidate)(nil)

func (*memoryReviewCandidate) VerifyRemoteDestination(context.Context, taskmodel.Repository, string) error {
	return errors.New("unexpected named publication destination")
}
func (*memoryReviewCandidate) MergeTaskBranchIntoDestination(context.Context, taskmodel.Repository, string, string) (string, error) {
	return "", errors.New("unexpected direct merge")
}
func (*memoryReviewCandidate) ValidateRecordedDirectMerge(context.Context, taskmodel.Repository, string, string) (bool, error) {
	return false, errors.New("unexpected direct merge retry")
}
func (*memoryReviewCandidate) ValidateMaterializedTaskBranchRetry(context.Context, taskmodel.Repository, string, string, state.Paths) error {
	return errors.New("unexpected publication retry")
}
