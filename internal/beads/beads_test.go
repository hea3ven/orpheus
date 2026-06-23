package beads_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/beads"
	"github.com/hea3ven/orpheus/internal/task"
)

type fakeRunner struct {
	calls []fakeCall
}

type fakeCall struct {
	wantDir  string
	wantArgs []string
	result   beads.Result
	err      error
}

const listVisibleTasksStdout = `[
	{
		"id":"op-1",
		"title":"Implement adapter",
		"external_ref":"TREX-1234",
		"description":"Read tasks",
		"design":"Use bd JSON",
		"acceptance_criteria":"Parses metadata",
		"status":"open",
		"priority":2,
		"issue_type":"task",
		"owner":"owner@example.com",
		"created_by":"Hea3veN",
		"created_at":"2026-05-24T06:30:53Z",
		"updated_at":"2026-05-24T07:30:53Z",
		"labels":["m2","mvp"],
		"metadata":{
			"orpheus.branch":"task/op-1",
			"estimate":42,
			"review":true,
			"nested":{"team":"platform"}
		},
		"dependency_count":1,
		"dependent_count":2,
		"parent":"op"
	},
	{"id":"op-2","title":"Closed task","status":"closed","priority":2,"issue_type":"task"},
	{"id":"op-3","title":"Bug","status":"open","priority":2,"issue_type":"bug"}
]`

func (r *fakeRunner) Run(dir string, args ...string) (beads.Result, error) {
	if len(r.calls) == 0 {
		return beads.Result{}, errors.New("unexpected bd call")
	}
	call := r.calls[0]
	r.calls = r.calls[1:]

	if strings.TrimSpace(dir) == "" {
		return beads.Result{}, errors.New("runner dir is empty")
	}
	if call.wantDir != "" && dir != call.wantDir {
		return beads.Result{}, errors.New("unexpected dir: " + dir)
	}
	if strings.Join(args, "\x00") != strings.Join(call.wantArgs, "\x00") {
		return beads.Result{}, errors.New("unexpected args: " + strings.Join(args, " "))
	}
	return call.result, call.err
}

func TestCommandRunnerSanitizesBeadsEnvironment(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bd-fake")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'BEADS_DIR=%s\\n' \"${BEADS_DIR-unset}\"\nprintf 'BD_NON_INTERACTIVE=%s\\n' \"${BD_NON_INTERACTIVE-unset}\"\n"), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("BEADS_DIR", "/tmp/wrong")

	result, err := beads.CommandRunner{Binary: bin}.Run(t.TempDir(), "context")
	if err != nil {
		t.Fatalf("run fake bd: %v", err)
	}
	if strings.Contains(result.Stdout, "BEADS_DIR=/tmp/wrong") || !strings.Contains(result.Stdout, "BEADS_DIR=unset") {
		t.Fatalf("stdout = %q, want sanitized BEADS_DIR", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "BD_NON_INTERACTIVE=1") {
		t.Fatalf("stdout = %q, want BD_NON_INTERACTIVE=1", result.Stdout)
	}
}

func TestInspectLocalWithRunnerDetectsPrefix(t *testing.T) {
	root := newRootWithBeadsDir(t)
	runner := &fakeRunner{calls: []fakeCall{
		{
			wantArgs: []string{"--json", "--readonly", "context"},
			result:   beads.Result{Stdout: `{"beads_dir":"` + filepath.ToSlash(filepath.Join(root, ".beads")) + `"}`},
		},
		{
			wantArgs: []string{"--json", "--readonly", "config", "get", "issue_prefix"},
			result:   beads.Result{Stdout: `{"key":"issue_prefix","value":"op"}`},
		},
	}}

	got, err := beads.InspectLocalWithRunner(root, runner)
	if err != nil {
		t.Fatalf("inspect local: %v", err)
	}
	if got.Root != root || got.BeadsDir != filepath.Join(root, ".beads") || got.Prefix != "op" {
		t.Fatalf("inspection = %#v, want root %q beads dir .beads prefix op", got, root)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner has %d unused calls", len(runner.calls))
	}
}

func TestInspectLocalWithRunnerReportsNoLocalWithoutRunningBD(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}

	_, err := beads.InspectLocalWithRunner(root, runner)
	if !errors.Is(err, beads.ErrNoLocal) {
		t.Fatalf("error = %v, want ErrNoLocal", err)
	}
}

func TestInspectLocalWithRunnerDistinguishesBDNoWorkspace(t *testing.T) {
	root := newRootWithBeadsDir(t)
	runner := &fakeRunner{calls: []fakeCall{{
		wantArgs: []string{"--json", "--readonly", "context"},
		result:   beads.Result{Stdout: `{"error":"no_beads_directory","message":"No active beads workspace found."}`},
		err:      errors.New("exit status 1"),
	}}}

	_, err := beads.InspectLocalWithRunner(root, runner)
	if !errors.Is(err, beads.ErrNoLocal) {
		t.Fatalf("error = %v, want ErrNoLocal", err)
	}
}

func TestInspectLocalWithRunnerReportsMissingBD(t *testing.T) {
	root := newRootWithBeadsDir(t)
	runner := &fakeRunner{calls: []fakeCall{{
		wantArgs: []string{"--json", "--readonly", "context"},
		err:      exec.ErrNotFound,
	}}}

	_, err := beads.InspectLocalWithRunner(root, runner)
	if err == nil {
		t.Fatal("inspect local succeeded, want error")
	}
	if !strings.Contains(err.Error(), "bd executable not found") {
		t.Fatalf("error = %v, want missing bd guidance", err)
	}
}

func TestInspectLocalWithRunnerReportsParseFailure(t *testing.T) {
	root := newRootWithBeadsDir(t)
	runner := &fakeRunner{calls: []fakeCall{{
		wantArgs: []string{"--json", "--readonly", "context"},
		result:   beads.Result{Stdout: `not-json`},
	}}}

	_, err := beads.InspectLocalWithRunner(root, runner)
	if err == nil {
		t.Fatal("inspect local succeeded, want error")
	}
	if !strings.Contains(err.Error(), "parse bd context JSON") || !strings.Contains(err.Error(), "not-json") {
		t.Fatalf("error = %v, want actionable parse failure", err)
	}
}

func TestInspectLocalWithRunnerReportsCommandFailure(t *testing.T) {
	root := newRootWithBeadsDir(t)
	runner := &fakeRunner{calls: []fakeCall{{
		wantArgs: []string{"--json", "--readonly", "context"},
		result:   beads.Result{Stderr: "database locked"},
		err:      errors.New("exit status 1"),
	}}}

	_, err := beads.InspectLocalWithRunner(root, runner)
	if err == nil {
		t.Fatal("inspect local succeeded, want error")
	}
	if !strings.Contains(err.Error(), "run bd --json --readonly context") || !strings.Contains(err.Error(), "database locked") {
		t.Fatalf("error = %v, want actionable command failure", err)
	}
}

func TestInspectLocalWithRunnerRequiresPrefix(t *testing.T) {
	root := newRootWithBeadsDir(t)
	runner := &fakeRunner{calls: []fakeCall{
		{
			wantArgs: []string{"--json", "--readonly", "context"},
			result:   beads.Result{Stdout: `{"beads_dir":"` + filepath.ToSlash(filepath.Join(root, ".beads")) + `"}`},
		},
		{
			wantArgs: []string{"--json", "--readonly", "config", "get", "issue_prefix"},
			result:   beads.Result{Stdout: `{"key":"issue_prefix","value":"   "}`},
		},
	}}

	_, err := beads.InspectLocalWithRunner(root, runner)
	if err == nil {
		t.Fatal("inspect local succeeded, want error")
	}
	if !strings.Contains(err.Error(), "issue_prefix is empty") {
		t.Fatalf("error = %v, want prefix guidance", err)
	}
}

func TestInitializeManagedWithRunnerInitializesEmptyDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "managed", "beads")
	runner := &fakeRunner{calls: []fakeCall{{
		wantDir: dir,
		wantArgs: []string{
			"init",
			"--non-interactive",
			"--prefix",
			"alpha",
			"--skip-agents",
			"--skip-hooks",
			"--quiet",
		},
	}}}

	if err := beads.InitializeManagedWithRunner(dir, " alpha ", runner); err != nil {
		t.Fatalf("initialize managed: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("managed directory was not created: info=%v err=%v", info, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner has %d unused calls", len(runner.calls))
	}
}

func TestInitializeManagedWithRunnerRejectsExistingStateBeforeRunningBD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leftover"), []byte("state"), 0o644); err != nil {
		t.Fatalf("write leftover: %v", err)
	}
	runner := &fakeRunner{}

	err := beads.InitializeManagedWithRunner(dir, "alpha", runner)
	if err == nil {
		t.Fatal("initialize managed succeeded, want existing state error")
	}
	if !strings.Contains(err.Error(), "directory already exists and is not empty") {
		t.Fatalf("error = %v, want existing state guidance", err)
	}
}

func TestInitializeManagedWithRunnerReportsCommandFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "beads")
	runner := &fakeRunner{calls: []fakeCall{{
		wantDir: dir,
		wantArgs: []string{
			"init",
			"--non-interactive",
			"--prefix",
			"alpha",
			"--skip-agents",
			"--skip-hooks",
			"--quiet",
		},
		result: beads.Result{Stderr: "cannot initialize"},
		err:    errors.New("exit status 1"),
	}}}

	err := beads.InitializeManagedWithRunner(dir, "alpha", runner)
	if err == nil {
		t.Fatal("initialize managed succeeded, want command failure")
	}
	if !strings.Contains(err.Error(), "run bd init") || !strings.Contains(err.Error(), "cannot initialize") {
		t.Fatalf("error = %v, want actionable command failure", err)
	}
}

func TestTaskBackendListParsesVisibleTasksAndMetadata(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{calls: []fakeCall{{
		wantDir:  dir,
		wantArgs: []string{"--json", "--readonly", "--sandbox", "list", "--all", "--limit", "0"},
		result:   beads.Result{Stdout: listVisibleTasksStdout},
	}}}

	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	got, err := backend.List(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("tasks = %#v, want active, closed, and non-task items", got)
	}

	assertParsedVisibleTask(t, got[0])
	if len(runner.calls) != 0 {
		t.Fatalf("runner has %d unused calls", len(runner.calls))
	}
}

func assertParsedVisibleTask(t *testing.T, taskItem task.Task) {
	t.Helper()

	if taskItem.ID != "op-1" || taskItem.Title != "Implement adapter" || taskItem.Status != task.StatusOpen || taskItem.IssueType != task.IssueTypeTask {
		t.Fatalf("task = %#v, want parsed active task", taskItem)
	}
	if taskItem.ExternalRef != "TREX-1234" {
		t.Fatalf("external reference = %q, want TREX-1234", taskItem.ExternalRef)
	}
	if !reflect.DeepEqual(taskItem.Labels, []string{"m2", "mvp"}) {
		t.Fatalf("labels = %#v, want m2/mvp", taskItem.Labels)
	}
	expectedMetadata := task.Metadata{
		"orpheus.branch": "task/op-1",
		"estimate":       "42",
		"review":         "true",
		"nested":         `{"team":"platform"}`,
	}
	if !reflect.DeepEqual(taskItem.Metadata, expectedMetadata) {
		t.Fatalf("metadata = %#v, want %#v", taskItem.Metadata, expectedMetadata)
	}
	if taskItem.Relations.ParentID != "op" || taskItem.Relations.DependencyCount != 1 || taskItem.Relations.DependentCount != 2 {
		t.Fatalf("relations = %#v, want parent op counts 1/2", taskItem.Relations)
	}
	if taskItem.CreatedAt == nil || !taskItem.CreatedAt.Equal(time.Date(2026, 5, 24, 6, 30, 53, 0, time.UTC)) {
		t.Fatalf("created_at = %v, want parsed UTC time", taskItem.CreatedAt)
	}
}

func TestTaskBackendGetParsesShowJSON(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{calls: []fakeCall{{
		wantDir:  dir,
		wantArgs: []string{"--json", "--readonly", "--sandbox", "show", "--id", "op-9wh.2"},
		result: beads.Result{Stdout: `[
			{
				"id":"op-9wh.2",
				"title":"Implement Beads CLI task adapter",
				"description":"Implement the Beads-backed read adapter.",
				"design":"Reuse the runner pattern.",
				"acceptance_criteria":"Adapter implements read interfaces.",
				"status":"in_progress",
				"priority":2,
				"issue_type":"task",
				"assignee":"Hea3veN",
				"owner":"owner@example.com",
				"created_by":"Hea3veN",
				"created_at":"2026-05-15T21:37:54Z",
				"updated_at":"2026-05-24T06:27:34Z",
				"started_at":"2026-05-24T06:27:34Z",
				"labels":["m2","m2-task"],
				"metadata":{"orpheus.worktree":"/tmp/worktree"},
				"dependencies":[
					{"id":"op-9wh","dependency_type":"parent-child"},
					{"id":"op-9wh.1","dependency_type":"blocks"}
				],
				"dependents":[{"id":"op-9wh.4","dependency_type":"blocks"}]
			}
		]`},
	}}}

	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	got, err := backend.Get(context.Background(), "op-9wh.2")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.ID != "op-9wh.2" || got.Status != task.StatusInProgress || got.Assignee != "Hea3veN" {
		t.Fatalf("task = %#v, want parsed op-9wh.2", got)
	}
	if got.Design != "Reuse the runner pattern." || got.AcceptanceCriteria != "Adapter implements read interfaces." {
		t.Fatalf("task detail = %#v, want design and acceptance", got)
	}
	if got.Metadata[task.MetadataWorktree] != "/tmp/worktree" {
		t.Fatalf("metadata = %#v, want worktree", got.Metadata)
	}
	if got.Relations.ParentID != "op-9wh" || !reflect.DeepEqual(got.Relations.DependencyIDs, []string{"op-9wh.1"}) {
		t.Fatalf("dependencies = %#v, want parent and blocking dependency", got.Relations)
	}
	if !reflect.DeepEqual(got.Relations.DependentIDs, []string{"op-9wh.4"}) {
		t.Fatalf("dependents = %#v, want op-9wh.4", got.Relations.DependentIDs)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner has %d unused calls", len(runner.calls))
	}
}

func TestTaskBackendGetReturnsClosedOrNonTaskItemsForShowScope(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{calls: []fakeCall{
		{
			wantDir:  dir,
			wantArgs: []string{"--json", "--readonly", "--sandbox", "show", "--id", "op-closed"},
			result:   beads.Result{Stdout: `[{"id":"op-closed","title":"done","status":"closed","priority":2,"issue_type":"task"}]`},
		},
		{
			wantDir:  dir,
			wantArgs: []string{"--json", "--readonly", "--sandbox", "show", "--id", "op-bug"},
			result:   beads.Result{Stdout: `[{"id":"op-bug","title":"bug","status":"open","priority":2,"issue_type":"bug"}]`},
		},
	}}

	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	closed, err := backend.Get(context.Background(), "op-closed")
	if err != nil {
		t.Fatalf("get closed item: %v", err)
	}
	if closed.Status != task.StatusClosed || closed.IssueType != task.IssueTypeTask {
		t.Fatalf("closed item = %#v, want closed task returned", closed)
	}

	bug, err := backend.Get(context.Background(), "op-bug")
	if err != nil {
		t.Fatalf("get bug item: %v", err)
	}
	if bug.Status != task.StatusOpen || bug.IssueType != task.IssueTypeBug {
		t.Fatalf("bug item = %#v, want open bug returned", bug)
	}
}

func TestTaskBackendMarkInProgressUpdatesOpenTaskStatusAndMetadata(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{calls: []fakeCall{
		{
			wantDir:  dir,
			wantArgs: []string{"--json", "--readonly", "--sandbox", "show", "--id", "op-1"},
			result:   beads.Result{Stdout: `[{"id":"op-1","title":"task","status":"open","priority":2,"issue_type":"task","metadata":{"team":"platform"}}]`},
		},
		{
			wantDir: dir,
			wantArgs: []string{
				"--json",
				"--sandbox",
				"update",
				"op-1",
				"--status",
				"in_progress",
				"--set-metadata",
				"orpheus.branch=orpheus/op-1",
				"--set-metadata",
				"orpheus.worktree=/tmp/op-1",
			},
		},
	}}

	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	if err := backend.MarkInProgress(context.Background(), "op-1", "orpheus/op-1", "/tmp/op-1"); err != nil {
		t.Fatalf("mark in progress: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner has %d unused calls", len(runner.calls))
	}
}

func TestTaskBackendMarkInProgressTreatsMatchingInProgressTaskAsSuccess(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{calls: []fakeCall{{
		wantDir:  dir,
		wantArgs: []string{"--json", "--readonly", "--sandbox", "show", "--id", "op-2"},
		result: beads.Result{Stdout: `[{"id":"op-2","title":"task","status":"in_progress","priority":2,"issue_type":"task","metadata":{` +
			`"orpheus.branch":"orpheus/op-2","orpheus.worktree":"/tmp/op-2"}}]`},
	}}}

	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	if err := backend.MarkInProgress(context.Background(), "op-2", "orpheus/op-2", "/tmp/op-2"); err != nil {
		t.Fatalf("mark in progress: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner has %d unused calls", len(runner.calls))
	}
}

func TestTaskBackendMarkInProgressReportsMutationConflicts(t *testing.T) {
	tests := []struct {
		name    string
		stdout  string
		wantErr string
	}{
		{
			name:    "in-progress missing metadata",
			stdout:  `[{"id":"op-3","title":"task","status":"in_progress","priority":2,"issue_type":"task"}]`,
			wantErr: "orpheus.branch is missing",
		},
		{
			name:    "in-progress different branch",
			stdout:  `[{"id":"op-3","title":"task","status":"in_progress","priority":2,"issue_type":"task","metadata":{"orpheus.branch":"other","orpheus.worktree":"/tmp/op-3"}}]`,
			wantErr: `orpheus.branch is "other", expected "orpheus/op-3"`,
		},
		{
			name:    "closed task",
			stdout:  `[{"id":"op-3","title":"task","status":"closed","priority":2,"issue_type":"task"}]`,
			wantErr: "task is closed",
		},
		{
			name:    "pr url set",
			stdout:  `[{"id":"op-3","title":"task","status":"open","priority":2,"issue_type":"task","metadata":{"orpheus.pr_url":"https://example.test/pr/3"}}]`,
			wantErr: "orpheus.pr_url is already set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			runner := &fakeRunner{calls: []fakeCall{{
				wantDir:  dir,
				wantArgs: []string{"--json", "--readonly", "--sandbox", "show", "--id", "op-3"},
				result:   beads.Result{Stdout: tt.stdout},
			}}}
			backend, err := beads.NewTaskBackendWithRunner(dir, runner)
			if err != nil {
				t.Fatalf("create backend: %v", err)
			}

			err = backend.MarkInProgress(context.Background(), "op-3", "orpheus/op-3", "/tmp/op-3")
			if !errors.Is(err, task.ErrMutationConflict) {
				t.Fatalf("error = %v, want ErrMutationConflict", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("runner has %d unused calls", len(runner.calls))
			}
		})
	}
}

func TestTaskBackendMarkInProgressReportsUpdateCommandFailure(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{calls: []fakeCall{
		{
			wantDir:  dir,
			wantArgs: []string{"--json", "--readonly", "--sandbox", "show", "--id", "op-4"},
			result:   beads.Result{Stdout: `[{"id":"op-4","title":"task","status":"open","priority":2,"issue_type":"task"}]`},
		},
		{
			wantDir: dir,
			wantArgs: []string{
				"--json",
				"--sandbox",
				"update",
				"op-4",
				"--status",
				"in_progress",
				"--set-metadata",
				"orpheus.branch=orpheus/op-4",
				"--set-metadata",
				"orpheus.worktree=/tmp/op-4",
			},
			result: beads.Result{Stderr: "database locked"},
			err:    errors.New("exit status 1"),
		},
	}}
	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	err = backend.MarkInProgress(context.Background(), "op-4", "orpheus/op-4", "/tmp/op-4")
	if err == nil {
		t.Fatal("mark in progress succeeded, want update failure")
	}
	if !strings.Contains(err.Error(), "run bd --json --sandbox update op-4") || !strings.Contains(err.Error(), "database locked") {
		t.Fatalf("error = %v, want update command output", err)
	}
}

func TestTaskBackendSetPRURLWritesMetadata(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{calls: []fakeCall{{
		wantDir: dir,
		wantArgs: []string{
			"--json",
			"--sandbox",
			"update",
			"op-5",
			"--set-metadata",
			"orpheus.pr_url=https://github.test/org/repo/pull/5",
		},
	}}}
	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	if err := backend.SetPRURL(context.Background(), " op-5 ", " https://github.test/org/repo/pull/5 "); err != nil {
		t.Fatalf("set PR URL: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner has %d unused calls", len(runner.calls))
	}
}

func TestTaskBackendSetPRURLValidatesRequiredInputs(t *testing.T) {
	backend, err := beads.NewTaskBackendWithRunner(t.TempDir(), &fakeRunner{})
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	if err := backend.SetPRURL(context.Background(), "", "https://github.test/org/repo/pull/5"); err == nil ||
		!strings.Contains(err.Error(), "task id is required") {
		t.Fatalf("missing task id error = %v", err)
	}
	if err := backend.SetPRURL(context.Background(), "op-5", " "); err == nil ||
		!strings.Contains(err.Error(), "PR URL is required") {
		t.Fatalf("missing PR URL error = %v", err)
	}
}

func TestTaskBackendCloseClosesOpenTask(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{calls: []fakeCall{
		{
			wantDir:  dir,
			wantArgs: []string{"--json", "--readonly", "--sandbox", "show", "--id", "op-1"},
			result:   beads.Result{Stdout: `[{"id":"op-1","title":"task","status":"in_progress","priority":2,"issue_type":"task"}]`},
		},
		{
			wantDir:  dir,
			wantArgs: []string{"--json", "--sandbox", "close", "op-1"},
		},
	}}
	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	if err := backend.Close(context.Background(), "op-1"); err != nil {
		t.Fatalf("close task: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner has %d unused calls", len(runner.calls))
	}
}

func TestTaskBackendCloseTreatsAlreadyClosedTaskAsSuccess(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{calls: []fakeCall{{
		wantDir:  dir,
		wantArgs: []string{"--json", "--readonly", "--sandbox", "show", "--id", "op-2"},
		result:   beads.Result{Stdout: `[{"id":"op-2","title":"task","status":"closed","priority":2,"issue_type":"task"}]`},
	}}}
	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	if err := backend.Close(context.Background(), "op-2"); err != nil {
		t.Fatalf("close already closed task: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner has %d unused calls", len(runner.calls))
	}
}

func TestTaskBackendReportsCommandFailureWithOutput(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{calls: []fakeCall{{
		wantDir:  dir,
		wantArgs: []string{"--json", "--readonly", "--sandbox", "list", "--all", "--limit", "0"},
		result:   beads.Result{Stdout: `{"error":"query_failed"}`, Stderr: "database locked"},
		err:      errors.New("exit status 1"),
	}}}

	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	_, err = backend.List(context.Background())
	if err == nil {
		t.Fatal("list succeeded, want command failure")
	}
	if !strings.Contains(err.Error(), "run bd --json --readonly --sandbox list --all --limit 0") ||
		!strings.Contains(err.Error(), "query_failed") ||
		!strings.Contains(err.Error(), "database locked") {
		t.Fatalf("error = %v, want command and output context", err)
	}
}

func newRootWithBeadsDir(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	return root
}
