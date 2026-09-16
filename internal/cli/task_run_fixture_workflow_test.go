//go:build integration

package cli_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/review"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	taskWorkflowConfigRoot = "/fixture/task-workflow/xdg-config/orpheus"
	taskWorkflowDataRoot   = "/fixture/task-workflow/xdg-data/orpheus"
	taskWorkflowRepoRoot   = "/fixture/task-workflow/repos/alpha"
)

type taskWorkflowFixture struct {
	*workflowFixture
	taskStore           taskstate.Store
	backend             *memoryTaskBackend
	git                 *memoryDispatchGit
	agent               *semanticAgentLauncher
	probedPIDs          map[int]int
	initialTasks        map[string]taskmodel.Task
	reviewPipelineCalls int
}

func newTaskWorkflowFixture(t *testing.T, tasks ...taskmodel.Task) *taskWorkflowFixture {
	t.Helper()
	base := newWorkflowFixture(t, taskWorkflowConfigRoot, taskWorkflowDataRoot)
	base.mustRemainOffDisk(taskWorkflowRepoRoot)
	backend := newMemoryTaskBackend(tasks)
	git := newMemoryDispatchGit()
	fixture := &taskWorkflowFixture{
		workflowFixture: base,
		taskStore:       taskstate.NewStore(base.paths),
		backend:         backend,
		git:             git,
		probedPIDs:      make(map[int]int),
		initialTasks:    make(map[string]taskmodel.Task, len(tasks)),
	}
	for _, item := range tasks {
		fixture.initialTasks[item.ID] = item.Clone()
	}
	launcher := &semanticAgentLauncher{options: &fixture.options, nextPID: 4242}
	fixture.agent = launcher
	fixture.options.Dependencies.TaskBackendFactory = func(taskmodel.RepositorySource) (taskmodel.ReadBackend, error) { return backend, nil }
	fixture.options.Dependencies.DispatchGit = git
	fixture.options.Dependencies.AgentGit = git
	fixture.options.Dependencies.ReviewCandidate = git
	fixture.options.Dependencies.AgentLauncher = launcher
	fixture.options.Dependencies.CaptureUsage = func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		return taskstate.RecordRunUsageOptions{UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureUnknown, Reason: "not supplied by scenario"}}
	}
	fixture.options.Dependencies.ProcessProbe = func(pid int) (agentexec.ProcessLiveness, error) {
		return agentexec.ProcessUnknown, fmt.Errorf("unexpected process probe for PID %d", pid)
	}
	fixture.options.Dependencies.ReviewPipeline = func(review.PipelineRunOptions) (review.PipelineOutcome, error) {
		return review.PipelineOutcome{}, errors.New("unexpected review pipeline invocation")
	}
	t.Cleanup(func() {
		assert.Empty(t, launcher.outcomes, "all scripted agent outcomes must be consumed")
	})

	fixture.withRegisteredRepos(taskWorkflowRepository())
	return fixture
}

func (f *taskWorkflowFixture) expectedTarget(taskID string) (string, string) {
	f.t.Helper()
	worktree, err := f.paths.DataPath(filepath.Join("repos", "alpha", "worktrees", taskID))
	require.NoError(f.t, err, "resolve expected worktree")
	return "orpheus/" + taskID, worktree
}

func (f *taskWorkflowFixture) loadFinalTask(taskID string) (taskstate.TaskState, taskmodel.Task) {
	f.t.Helper()
	localState, err := f.taskStore.Load("alpha", taskID)
	require.NoError(f.t, err, "load task state for %s", taskID)
	item, err := f.backend.Get(context.Background(), taskID)
	require.NoError(f.t, err, "load task %s", taskID)
	return localState, item
}

func (f *taskWorkflowFixture) loadFinalTaskIDs() []string {
	f.t.Helper()
	items, err := f.backend.List(context.Background())
	require.NoError(f.t, err, "list final tasks")
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}

func (f *taskWorkflowFixture) configureImplementer(name string, profile agent.Profile) {
	f.t.Helper()
	f.configureAgentProfiles(agent.AgentDefaults{Implementer: name}, map[string]agent.Profile{name: profile})
}

func (f *taskWorkflowFixture) configureAgentProfiles(defaults agent.AgentDefaults, profiles map[string]agent.Profile) {
	f.t.Helper()
	configured := make(map[string]any, len(profiles))
	for name, profile := range profiles {
		configured[name] = map[string]any{
			"command": profile.Command, "args": profile.Args, "interactive": profile.Interactive,
			"harness": profile.Harness, "model": profile.Model, "thinking": profile.Thinking, "prompt_append": profile.PromptAppend,
		}
	}
	f.config["agents"] = map[string]any{
		"defaults": defaults,
		"profiles": configured,
	}
	f.saveConfig()
}

func (f *taskWorkflowFixture) configureReviewPipeline(name string, steps []review.Step) {
	f.t.Helper()
	f.config["reviews"] = map[string]any{
		"default_pipeline": name,
		"pipelines":        map[string]any{name: map[string]any{"steps": steps}},
	}
	f.saveConfig()
}

func (f *taskWorkflowFixture) seedRunningAttempt(taskID string, supervisorPID, childPID int) {
	f.t.Helper()
	branch, worktree := f.expectedTarget(taskID)
	item := f.backend.tasks[taskID]
	item.Metadata = taskmodel.Metadata{taskmodel.MetadataBranch: branch, taskmodel.MetadataWorktree: worktree}
	f.backend.tasks[taskID] = item
	_, err := f.git.SetupTaskWorktree(context.Background(), gitmeta.TaskWorktreeOptions{
		RepoID: "alpha", RepoName: "Alpha Repo", RepoPath: taskWorkflowRepoRoot,
		DefaultBranch: "main", TaskID: taskID, Branch: branch, Paths: f.paths,
	})
	require.NoError(f.t, err, "seed worktree")
	run, err := f.taskStore.StartRun("alpha", taskID, taskstate.StartRunOptions{
		Agent: "recorder", Branch: branch, Worktree: worktree, WorkDirectory: worktree, SupervisorPID: supervisorPID,
	})
	require.NoError(f.t, err, "seed running attempt")
	_, err = f.taskStore.RecordRunChildPID("alpha", taskID, run.Attempt, childPID)
	require.NoError(f.t, err, "seed child PID")
}

func taskWorkflowRepository() registry.Repo {
	return registry.Repo{ID: "alpha", Name: "Alpha Repo", Path: taskWorkflowRepoRoot, DefaultBranch: "main", BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op"}
}
