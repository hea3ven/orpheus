//go:build integration

package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/cli"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

type memoryTaskBackend struct {
	mu        sync.Mutex
	tasks     map[string]taskmodel.Task
	markError error
	markCalls []string
}

func newMemoryTaskBackend(tasks []taskmodel.Task) *memoryTaskBackend {
	backend := &memoryTaskBackend{tasks: make(map[string]taskmodel.Task, len(tasks))}
	for _, item := range tasks {
		backend.tasks[item.ID] = item.Clone()
	}
	return backend
}

func (b *memoryTaskBackend) Get(_ context.Context, id string) (taskmodel.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	item, ok := b.tasks[id]
	if !ok {
		return taskmodel.Task{}, taskmodel.ErrNotFound
	}
	return item.Clone(), nil
}

func (b *memoryTaskBackend) List(context.Context) ([]taskmodel.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.tasks))
	for id := range b.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]taskmodel.Task, 0, len(ids))
	for _, id := range ids {
		items = append(items, b.tasks[id].Clone())
	}
	return items, nil
}

// This local dispatch stub supports reads and MarkInProgress only. Publication,
// task creation, and target relocation are outside these scenarios and fail closed.
// The accepted dispatch states match the Beads MarkInProgress contract.
func (b *memoryTaskBackend) MarkInProgress(_ context.Context, taskID, branch, worktree string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.markCalls = append(b.markCalls, taskID)
	if b.markError != nil {
		return b.markError
	}
	taskID, branch, worktree = strings.TrimSpace(taskID), strings.TrimSpace(branch), strings.TrimSpace(worktree)
	if taskID == "" || branch == "" || worktree == "" {
		return errors.New("task id, branch, and worktree are required")
	}
	item, ok := b.tasks[taskID]
	if !ok {
		return taskmodel.ErrNotFound
	}
	metadata := item.OrpheusMetadata()
	if strings.TrimSpace(metadata.PRURL) != "" {
		return taskmodel.MutationConflictError{TaskID: taskID, Reason: "PR URL is already set"}
	}
	switch item.Status {
	case taskmodel.StatusOpen:
		item.Status = taskmodel.StatusInProgress
		if item.Metadata == nil {
			item.Metadata = make(taskmodel.Metadata)
		}
		item.Metadata[taskmodel.MetadataBranch] = branch
		item.Metadata[taskmodel.MetadataWorktree] = worktree
		b.tasks[taskID] = item
		return nil
	case taskmodel.StatusInProgress:
		if metadata.HasBranch && strings.TrimSpace(metadata.Branch) == branch && metadata.HasWorktree && strings.TrimSpace(metadata.Worktree) == worktree {
			return nil
		}
	}
	return taskmodel.MutationConflictError{TaskID: taskID, Reason: "task status or existing target prevents dispatch"}
}

func (*memoryTaskBackend) UpdateGitFacts(context.Context, string, string, string) error {
	return errors.New("unexpected UpdateGitFacts: target relocation is outside this dispatch stub")
}

func (*memoryTaskBackend) SetPRURL(context.Context, string, string) error {
	return errors.New("unexpected SetPRURL: publication is outside this dispatch stub")
}

func (*memoryTaskBackend) Close(context.Context, string) error {
	return errors.New("unexpected Close: task closure is outside this dispatch stub")
}

func (*memoryTaskBackend) Create(context.Context, taskmodel.CreateOptions) (taskmodel.Task, error) {
	return taskmodel.Task{}, errors.New("unexpected Create: task creation is outside this dispatch stub")
}

type memoryGitTarget struct {
	branch string
}

type memoryDispatchGit struct {
	mu                  sync.Mutex
	targets             map[string]memoryGitTarget
	setups              []gitmeta.TaskWorktreeSetupResult
	hasCandidateChanges bool
	repoRootError       error
	repoRootSetups      []gitmeta.RepoRootOptions
	worktreeSetups      []gitmeta.TaskWorktreeOptions
}

func newMemoryDispatchGit() *memoryDispatchGit {
	return &memoryDispatchGit{targets: make(map[string]memoryGitTarget)}
}

func (g *memoryDispatchGit) SetupRepoRoot(_ context.Context, opts gitmeta.RepoRootOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	g.repoRootSetups = append(g.repoRootSetups, opts)
	if g.repoRootError != nil {
		return gitmeta.TaskWorktreeSetupResult{}, g.repoRootError
	}
	return g.setup(opts.RepoPath, opts.DefaultBranch, gitmeta.TaskWorktreeLifecycleReused), nil
}

func (g *memoryDispatchGit) SetupRepoRootTaskBranch(_ context.Context, opts gitmeta.TaskWorktreeOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	return g.setup(opts.RepoPath, opts.Branch, gitmeta.TaskWorktreeLifecycleTaskBranchCreated), nil
}

func (g *memoryDispatchGit) SetupTaskWorktree(_ context.Context, opts gitmeta.TaskWorktreeOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	g.worktreeSetups = append(g.worktreeSetups, opts)
	worktree, err := opts.Paths.DataPath(filepath.Join("repos", opts.RepoID, "worktrees", opts.TaskID))
	if err != nil {
		return gitmeta.TaskWorktreeSetupResult{}, err
	}
	branch := opts.Branch
	if branch == "" {
		branch = "orpheus/" + opts.TaskID
	}
	return g.setup(worktree, branch, gitmeta.TaskWorktreeLifecycleCreated), nil
}

func (g *memoryDispatchGit) setup(path, branch string, initial gitmeta.TaskWorktreeLifecycle) gitmeta.TaskWorktreeSetupResult {
	g.mu.Lock()
	defer g.mu.Unlock()
	lifecycle := initial
	if existing, ok := g.targets[path]; ok {
		branch = existing.branch
		lifecycle = gitmeta.TaskWorktreeLifecycleReused
	}
	g.targets[path] = memoryGitTarget{branch: branch}
	result := gitmeta.TaskWorktreeSetupResult{Branch: branch, WorktreePath: path, Lifecycle: lifecycle}
	g.setups = append(g.setups, result)
	return result
}

func (g *memoryDispatchGit) CurrentBranch(_ context.Context, dir string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	target, ok := g.targets[dir]
	if !ok {
		return "", fmt.Errorf("unknown memory Git target %q", dir)
	}
	return target.branch, nil
}

func (g *memoryDispatchGit) HasWorkingTreeChanges(context.Context, string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.hasCandidateChanges, nil
}

func (*memoryDispatchGit) StageAll(context.Context, string) error {
	return errors.New("unexpected StageAll: completion must not stage changes")
}

func (*memoryDispatchGit) Commit(context.Context, string, string) (string, error) {
	return "", errors.New("unexpected Commit: completion must not commit changes")
}

func (g *memoryDispatchGit) ValidateReviewCandidate(
	_ context.Context,
	_ workflow.ReviewLifecycleStore,
	_ workflow.ReviewAttemptContext,
	dir string,
) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.targets[dir]; !ok {
		return fmt.Errorf("unknown memory Git target %q", dir)
	}
	if !g.hasCandidateChanges {
		return errors.New("memory Git candidate has no changes")
	}
	return nil
}

func (g *memoryDispatchGit) lastLifecycle() gitmeta.TaskWorktreeLifecycle {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.setups) == 0 {
		return ""
	}
	return g.setups[len(g.setups)-1].Lifecycle
}

type semanticAgentOutcome struct {
	mutateCandidate       func()
	review                bool
	findings              []taskstate.ReviewFinding
	completion            *agent.CompleteOptions
	startFailure          error
	captureContext        bool
	exitWithoutCompletion bool
	err                   error
}

type semanticAgentLaunch struct {
	command     agentexec.Command
	dir         string
	environment map[string]string
}

type semanticAgentLauncher struct {
	options    *cli.CommandOptions
	outcomes   []semanticAgentOutcome
	launches   []semanticAgentLaunch
	contexts   []string
	nextPID    int
	afterStart func() error
}

func (l *semanticAgentLauncher) Run(_ context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
	if len(l.outcomes) == 0 {
		return fmt.Errorf("unexpected agent launch: %s in %s; no scripted outcome remains", command.Name, opts.Dir)
	}
	outcome := l.outcomes[0]
	l.outcomes = l.outcomes[1:]
	if outcome.completion == nil && outcome.err == nil && outcome.startFailure == nil && !outcome.exitWithoutCompletion && !outcome.review {
		return errors.New("agent outcome must explicitly specify completion, failure, or exit without completion")
	}
	environment := parseEnvironment(opts.Env)
	if outcome.review != (environment["ORPHEUS_AGENT_PURPOSE"] == "review") {
		return errors.New("scripted agent outcome does not match launch purpose")
	}
	l.launches = append(l.launches, semanticAgentLaunch{command: command, dir: opts.Dir, environment: environment})
	if outcome.startFailure != nil {
		return outcome.startFailure
	}
	if opts.OnStart != nil {
		if err := opts.OnStart(l.nextPID); err != nil {
			return err
		}
		l.nextPID++
	}
	if l.afterStart != nil {
		if err := l.afterStart(); err != nil {
			return err
		}
	}
	if outcome.completion != nil || outcome.captureContext {
		if err := l.captureContext(opts, environment); err != nil {
			return err
		}
	}
	if outcome.mutateCandidate != nil {
		outcome.mutateCandidate()
	}
	if outcome.review {
		for _, finding := range outcome.findings {
			if err := l.recordFinding(opts, environment, finding); err != nil {
				return err
			}
		}
	}
	if outcome.completion != nil {
		if err := l.recordCompletion(opts, environment, *outcome.completion); err != nil {
			return err
		}
	}
	return outcome.err
}

func (l *semanticAgentLauncher) captureContext(opts agentexec.LaunchOptions, environment map[string]string) error {
	command := cli.NewRootCommandWithOptions(l.childOptions(opts.Dir, environment))
	stdout := new(bytes.Buffer)
	command.SetIn(opts.Stdin)
	command.SetOut(stdout)
	command.SetErr(opts.Stderr)
	command.SetArgs([]string{"agent", "context"})
	if err := command.Execute(); err != nil {
		return err
	}
	l.contexts = append(l.contexts, stdout.String())
	return nil
}

func (l *semanticAgentLauncher) recordCompletion(opts agentexec.LaunchOptions, environment map[string]string, completion agent.CompleteOptions) error {
	command := cli.NewRootCommandWithOptions(l.childOptions(opts.Dir, environment))
	command.SetIn(opts.Stdin)
	command.SetOut(opts.Stdout)
	command.SetErr(opts.Stderr)
	command.SetArgs([]string{
		"agent", "done",
		"--summary", completion.Summary,
		"--description", completion.Description,
		"--detailed-description", completion.DetailedDescription,
		"--technical-explanation", completion.TechnicalExplanation,
	})
	return command.Execute()
}

func (l *semanticAgentLauncher) childOptions(dir string, environment map[string]string) cli.CommandOptions {
	options := *l.options
	options.Environment = maps.Clone(l.options.Environment)
	for key, value := range environment {
		options.Environment[key] = value
	}
	options.AgentWorkingDirectory = dir
	return options
}

func parseEnvironment(entries []string) map[string]string {
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func (l *semanticAgentLauncher) recordFinding(opts agentexec.LaunchOptions, environment map[string]string, finding taskstate.ReviewFinding) error {
	command := cli.NewRootCommandWithOptions(l.childOptions(opts.Dir, environment))
	command.SetIn(opts.Stdin)
	command.SetOut(opts.Stdout)
	command.SetErr(opts.Stderr)
	command.SetArgs([]string{"agent", "review", "add", "--type", string(finding.Type), "--title", finding.Title, "--description", finding.Description, "--suggested-action", finding.SuggestedAction})
	return command.Execute()
}
