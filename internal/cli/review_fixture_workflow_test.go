//go:build integration

package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/work"

	"github.com/hea3ven/orpheus/internal/workstate"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/cli"
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
	tasks        *memoryReviewTasks
	candidate    *memoryReviewCandidate
	checks       map[string][]checkResult
	hunkCommands map[string][]hunkCommandResult
	checkCalls   []string
	hunkCalls    []string
}

type checkResult struct {
	code           int
	err            error
	stdout, stderr string
}

type hunkCommandResult struct {
	code           int
	err            error
	notes          []review.HunkNote
	stdout, stderr string
}
type checkExit int

func (e checkExit) Error() string { return fmt.Sprintf("exit status %d", e) }
func (e checkExit) ExitCode() int { return int(e) }

func newReviewWorkflowFixture(t *testing.T, taskID, summary, description string) *reviewWorkflowFixture {
	t.Helper()
	f := newReviewDispatchFixture(t, taskID)
	f.candidate.contents = "reviewed\n"
	recordRepoRootCompletion(t, f.paths, "alpha", taskID, taskWorkflowRepoRoot, summary, description)
	return f
}

func newReviewDispatchFixture(t *testing.T, taskID string) *reviewWorkflowFixture {
	t.Helper()
	item := anInProgressTask(taskID)
	item.Title = "Ready for task done"
	item.Metadata = taskmodel.Metadata{taskmodel.MetadataBranch: "orpheus/" + taskID, taskmodel.MetadataWorktree: taskWorkflowRepoRoot}
	base := newTaskWorkflowFixture(t, item)
	f := &reviewWorkflowFixture{taskWorkflowFixture: base, checks: make(map[string][]checkResult), hunkCommands: make(map[string][]hunkCommandResult)}
	f.tasks = &memoryReviewTasks{memoryTaskBackend: base.backend}
	f.candidate = &memoryReviewCandidate{memoryDispatchGit: base.git, head: "initial-commit", contents: "fail\n"}
	base.git.targets[taskWorkflowRepoRoot] = memoryGitTarget{branch: "orpheus/" + taskID}
	base.git.hasCandidateChanges = true
	base.git.heads[taskWorkflowRepoRoot] = "initial-commit"
	base.git.retained["refs/orpheus/fixture-baseline"] = gitmeta.Revision{Commit: "initial-commit", Tree: "initial-tree", RetainedRef: "refs/orpheus/fixture-baseline"}
	f.options.Dependencies.TaskBackendFactory = func(taskmodel.RepositorySource) (taskmodel.ReadBackend, error) { return f.tasks, nil }
	f.options.Dependencies.ReviewPipeline = review.RunPipeline
	f.options.Dependencies.FinalizationGit = f.candidate
	f.options.Dependencies.OutputGit = f.candidate
	f.options.Dependencies.ReviewCandidateGit = f.candidate
	f.options.Dependencies.PRProvider = &memoryReviewPR{}
	f.options.Dependencies.ReviewStatus = f.candidate.ShortStatus
	f.options.Dependencies.ReviewEffects = review.Effects{CaptureCandidate: f.candidate.capture, RunCommand: f.runCheck, RunHunkCommand: f.runHunkCommand}
	t.Cleanup(func() {
		for name, outcomes := range f.checks {
			assert.Empty(t, outcomes, "unused check outcomes for %s", name)
		}
		for name, outcomes := range f.hunkCommands {
			assert.Empty(t, outcomes, "unused Hunk command outcomes for %s", name)
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
	var stdout, stderr bytes.Buffer
	err := f.runWithWriters(input, &stdout, &stderr, args...)
	return stdout.String(), stderr.String(), err
}
func (f *reviewWorkflowFixture) runWithWriters(input string, stdout, stderr io.Writer, args ...string) error {
	f.t.Helper()
	command := cli.NewRootCommandWithOptions(f.options)
	command.SetIn(strings.NewReader(input))
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs(args)
	return command.Execute()
}
func (f *reviewWorkflowFixture) forceInteractiveReviewOutput(width int) {
	f.t.Helper()
	f.options.Dependencies.Terminal = cli.TerminalCapabilities{
		InputIsTerminal:  func(io.Reader) bool { return false },
		OutputIsTerminal: func(io.Writer) bool { return true },
		OutputWidth: func(io.Writer) (int, bool) {
			return width, true
		},
	}
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

func (f *reviewWorkflowFixture) hunkCommand(name string, outcomes ...hunkCommandResult) string {
	f.hunkCommands[name] = append(f.hunkCommands[name], outcomes...)
	return name
}

func (f *reviewWorkflowFixture) runHunkCommand(opts review.HunkCommandOptions) (*int, []review.HunkNote, error) {
	name := opts.Step.Command
	outcomes := f.hunkCommands[name]
	if len(outcomes) == 0 {
		return nil, nil, fmt.Errorf("unexpected Hunk review command %q", name)
	}
	result := outcomes[0]
	f.hunkCommands[name] = outcomes[1:]
	f.hunkCalls = append(f.hunkCalls, name)
	if result.err != nil {
		return nil, nil, result.err
	}
	if _, err := fmt.Fprint(opts.Stdout, result.stdout); err != nil {
		return nil, nil, err
	}
	if _, err := fmt.Fprint(opts.Stderr, result.stderr); err != nil {
		return nil, nil, err
	}
	if result.code != 0 {
		return &result.code, result.notes, checkExit(result.code)
	}
	return &result.code, result.notes, nil
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
	head, contents       string
	destinationHeads     map[string]string
	observedDestinations []string
	snapshots            int
	commits              []string
	pushes               []string
}

func (g *memoryReviewCandidate) capture(ctx context.Context, dir string, expected review.CommittedCandidate, _ *slog.Logger, _ ...slog.Attr) (review.CandidateCheck, error) {
	if err := g.VerifyRevision(ctx, dir, expected.Identity, expected.Revision); err != nil {
		return nil, err
	}
	g.snapshots++
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
func (g *memoryReviewCandidate) StageAll(context.Context, string) error {
	g.hasStagedChanges = true
	return nil
}
func (g *memoryReviewCandidate) Commit(_ context.Context, _ string, message string) (string, error) {
	if !g.hasStagedChanges || !g.hasCandidateChanges {
		return "", errors.New("commit requires staged candidate changes")
	}
	g.commits = append(g.commits, message)
	g.head = fmt.Sprintf("commit-%d", len(g.commits))
	g.hasStagedChanges = false
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

func recordRepoRootCompletion(t *testing.T, paths state.Paths, repoID string, taskID string, repoPath string, summary string, description string) {
	t.Helper()
	store := taskstate.NewStore(paths)
	attempt, err := store.StartRun(repoID, taskID, taskstate.StartRunOptions{
		Agent: "recorder", Harness: "pi", Command: "fixture-agent",
		Context:        &work.WorkContext{Directory: repoPath, TaskBranch: "orpheus/" + taskID, CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: work.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
		Branch:         "orpheus/" + taskID,
		Worktree:       repoPath,
		BaselineCommit: "initial-commit",
	})
	if err != nil {
		t.Fatalf("start main run: %v", err)
	}
	if _, err := store.CompleteImplementation(attempt.Address, taskstate.CompleteRunOptions{
		Summary:              summary,
		Description:          description,
		DetailedDescription:  "Detailed PR body.",
		TechnicalExplanation: "Technical explanation.",
	}); err != nil {
		t.Fatalf("complete main run: %v", err)
	}
	if _, err := store.FinishObservedRun(attempt.Address, &work.ExitedProcess{Code: fixtureExitCode(0), At: time.Now().UTC()}, 0); err != nil {
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
		Agent: "recorder", Harness: "pi", Command: "fixture-agent",
		Context:        &work.WorkContext{Directory: repoPath, TaskBranch: "orpheus/" + taskID, CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: work.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
		Branch:         "orpheus/" + taskID,
		Worktree:       repoPath,
		BaselineCommit: "initial-commit",
		ReviewFollowUp: &taskstate.ReviewFollowUp{
			ReviewAttempt:  reviewAttempt,
			FindingIndexes: []int{0},
		},
	})
	if err != nil {
		t.Fatalf("start review follow-up run: %v", err)
	}
	if _, err := store.CompleteImplementation(attempt.Address, taskstate.CompleteRunOptions{
		Summary:              summary,
		Description:          description,
		DetailedDescription:  "Detailed follow-up body.",
		TechnicalExplanation: "Technical explanation.",
	}); err != nil {
		t.Fatalf("complete review follow-up run: %v", err)
	}
	if _, err := store.FinishObservedRun(attempt.Address, &work.ExitedProcess{Code: fixtureExitCode(0), At: time.Now().UTC()}, 0); err != nil {
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

func loadLatestReview(t *testing.T, fixture *reviewWorkflowFixture, taskID string) taskstate.ReviewAttempt {
	t.Helper()
	var state taskstate.TaskState
	require.NoError(t, fixture.paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", taskID+".yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	require.True(t, ok)
	return latest
}

func fixtureExitCode(code int) *int { return &code }
func (g *memoryReviewCandidate) CaptureRevision(ctx context.Context, opts gitmeta.RevisionCapture) (gitmeta.Revision, error) {
	if g.heads[opts.Directory] == "" {
		g.heads[opts.Directory] = g.head
		g.trees[g.head] = "initial-tree"
	}
	if g.head != g.heads[opts.Directory] {
		return gitmeta.Revision{}, errors.New("candidate and checkout heads disagree")
	}
	desired := fmt.Sprintf("tree-%x", sha256.Sum256([]byte(g.contents)))
	// The fixture models raw candidate content, so a second unchanged effort can
	// explicitly reuse code rather than pretending every handoff changes Git.
	g.hasCandidateChanges = g.trees[g.head] != desired
	revision, err := g.memoryDispatchGit.CaptureRevision(ctx, opts)
	if err != nil {
		return gitmeta.Revision{}, err
	}
	if !revision.Reused {
		g.commits = append(g.commits, opts.Message)
		revision.Tree = desired
		g.trees[revision.Commit] = desired
		g.retained[revision.RetainedRef] = revision
	}
	g.head = revision.Commit
	return revision, nil
}
func (g *memoryReviewCandidate) ObserveDestination(ctx context.Context, dir, branch string) (work.Destination, error) {
	g.observedDestinations = append(g.observedDestinations, branch)
	if g.destinationHeads == nil {
		return g.memoryDispatchGit.ObserveDestination(ctx, dir, branch)
	}
	commit, ok := g.destinationHeads[branch]
	if !ok {
		return work.Destination{}, errors.New("unknown native destination branch")
	}
	return work.Destination{Remote: "origin", Branch: branch, Commit: commit}, nil
}

func (g *memoryReviewCandidate) VerifyRevision(ctx context.Context, dir string, identity gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	if g.head != revision.Commit {
		return errors.New("candidate is not at the reviewed commit")
	}
	if len(g.commits) > 0 && g.trees[g.head] != fmt.Sprintf("tree-%x", sha256.Sum256([]byte(g.contents))) {
		return errors.New("candidate content differs from its committed tree")
	}
	return g.memoryDispatchGit.VerifyRevision(ctx, dir, identity, revision)
}
func (*memoryReviewCandidate) PublishedRevision(context.Context, string, string) (gitmeta.Revision, error) {
	return gitmeta.Revision{}, errors.New("unexpected publication before explicit authorization")
}
func (*memoryReviewCandidate) DirectRevisionDelivery(context.Context, taskmodel.Repository, string, string, string, string, string, gitmeta.Revision) (gitmeta.Revision, error) {
	return gitmeta.Revision{}, errors.New("unexpected direct delivery before explicit authorization")
}

// beginConfiguredFixtureReview arranges admission from explicitly captured
// semantic Git output and the complete configured contract. It does not supply
// reviewer exits, findings, operator decisions, consent or a pipeline outcome.
func beginConfiguredFixtureReview(f *reviewWorkflowFixture, store taskstate.Store, taskID string, opts taskstate.StartReviewOptions) (taskstate.ReviewAttempt, error) {
	view, err := store.Load("alpha", taskID)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	run, ok := taskstate.LatestRun(view)
	if !ok {
		return taskstate.ReviewAttempt{}, errors.New("fixture needs a completed implementation")
	}
	service := workflow.ImplementationService{Paths: f.paths, Store: workstate.NewStore(f.paths), Git: f.candidate}
	if _, err := service.Capture(context.Background(), run.Address); err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	configured, err := review.LoadConfig(f.paths)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	pipeline, err := review.ResolvePipeline(configured, opts.Pipeline, "")
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	opts.Pipeline = pipeline.Name
	opts.Contract = work.ReviewContract{PipelineKey: pipeline.Name}
	for _, step := range pipeline.Steps {
		opts.Contract.Slots = append(opts.Contract.Slots, work.ReviewSlot{Kind: work.ReviewStepKind(step.Kind), ConfigKey: step.Name})
	}
	opts.Scope = work.ReviewScope{Requirements: "Explicit fixture task requirements", Output: work.OutputRef{Phase: run.Address.Phase}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}}
	return store.StartReviewWithOptions("alpha", taskID, opts)
}
