//go:build integration

package cli_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/registry"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/require"
)

func newCommandWorkflow(t *testing.T) *workflowFixture {
	t.Helper()
	root := "/fixture/command-workflows/" + t.Name()
	fixture := newWorkflowFixture(t, filepath.Join(root, "config"), filepath.Join(root, "data"))
	fixture.options.Environment["HOME"] = root + "/home"
	fixture.options.AgentWorkingDirectory = root
	fixture.options.TaskWorkingDirectory = root
	fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		return nil, fmt.Errorf("unexpected task backend for %s", source.Repository.ID)
	}
	fixture.options.Dependencies.ProcessProbe = func(int) (agentexec.ProcessLiveness, error) { return agentexec.ProcessAbsent, nil }
	fixture.options.Dependencies.CaptureUsage = func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		return taskstate.RecordRunUsageOptions{UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureUnknown, Reason: "no supplied usage"}}
	}
	fixture.mustRemainOffDisk(root)
	return fixture
}

func (f *workflowFixture) mustExecute(args ...string) (string, string) {
	f.t.Helper()
	stdout, stderr, err := f.execute(args...)
	require.NoError(f.t, err, "command %v; stderr: %s", args, stderr)
	return stdout, stderr
}

func registerWorkflowRepo(t *testing.T, fixture *workflowFixture, id, name, prefix string) string {
	t.Helper()
	path := "/fixture/repos/" + id
	require.NoError(t, registry.NewStore(fixture.paths).Save(registry.Registry{Repos: []registry.Repo{{ID: id, Name: name, Path: path, DefaultBranch: "main", BeadsMode: registry.BeadsModeLocal, BeadsPrefix: prefix}}}))
	return path
}

type taskSourceResult struct {
	tasks     []taskmodel.Task
	err       error
	getErrors map[string]error
}

type sourceRead struct{ directory, operation, id string }
type taskSourceReads struct {
	mu    sync.Mutex
	calls []sourceRead
}

func (r *taskSourceReads) record(directory, operation, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, sourceRead{directory, operation, id})
}
func (r *taskSourceReads) directories() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var dirs []string
	for _, call := range r.calls {
		dirs = append(dirs, call.directory)
	}
	return dirs
}
func (r *taskSourceReads) count(operation string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, call := range r.calls {
		if call.operation == operation {
			n++
		}
	}
	return n
}

type workflowReadBackend struct {
	result    taskSourceResult
	directory string
	reads     *taskSourceReads
}

func (b workflowReadBackend) Get(_ context.Context, id string) (taskmodel.Task, error) {
	b.reads.record(b.directory, "get", id)
	if err := b.result.getErrors[id]; err != nil {
		return taskmodel.Task{}, err
	}
	if b.result.err != nil {
		return taskmodel.Task{}, b.result.err
	}
	for _, item := range b.result.tasks {
		if item.ID == id {
			return item.Clone(), nil
		}
	}
	return taskmodel.Task{}, taskmodel.ErrNotFound
}
func (b workflowReadBackend) List(context.Context) ([]taskmodel.Task, error) {
	b.reads.record(b.directory, "list", "")
	if b.result.err != nil {
		return nil, b.result.err
	}
	tasks := make([]taskmodel.Task, 0, len(b.result.tasks))
	for _, item := range b.result.tasks {
		tasks = append(tasks, item.Clone())
	}
	return tasks, nil
}
func withWorkflowSources(t *testing.T, fixture *workflowFixture, sources map[string]taskSourceResult) *taskSourceReads {
	t.Helper()
	reads := &taskSourceReads{}
	fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		result, ok := sources[source.BackendDir]
		if !ok {
			return nil, fmt.Errorf("unexpected task source %s", source.BackendDir)
		}
		return workflowReadBackend{result: result, directory: source.BackendDir, reads: reads}, nil
	}
	return reads
}

func workflowTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func setWorkflowEnvironment(t *testing.T, fixture *workflowFixture, key, value string) {
	t.Helper()
	fixture.options.Environment[key] = value
}
