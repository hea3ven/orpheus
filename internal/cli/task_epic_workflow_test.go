//go:build integration

package cli_test

import (
	"strings"
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskStartActivatesEligibleEpicAndRejectsOrdinaryTask(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	repo := ownershipAuthoringRepo()
	backend := newOwnershipAuthoringBackend(
		func() taskmodel.Task {
			epic := ownershipAuthoringEpic("op-epic", taskmodel.StatusOpen)
			epic.Title = "Release"
			epic.Relations.ParentID = "op-parent"
			epic.Relations.DependencyIDs = []string{"op-dependency"}
			return epic
		}(),
		ownershipAuthoringEpic("op-parent", taskmodel.StatusInProgress),
		ownershipAuthoringTask("op-dependency", "Prerequisite", taskmodel.StatusClosed, taskmodel.IssueTypeTask),
	)
	setupOwnershipAuthoringWorkflow(t, fixture, repo, backend)

	stdout, stderr := fixture.mustExecute("task", "start", "op-epic")

	is.Empty(stderr)
	is.Equal("Epic op-epic started.\n", stdout)
	is.Equal([]string{"op-epic"}, backend.started)
	is.Equal(taskmodel.StatusInProgress, backend.task("op-epic").Status)
	is.ElementsMatch([]string{"op-epic", "op-parent", "op-dependency"}, backend.getCalls)

	ordinary := newOwnershipAuthoringBackend(ownershipAuthoringTask("op-task", "Implementation", taskmodel.StatusOpen, taskmodel.IssueTypeTask))
	setupOwnershipAuthoringWorkflow(t, fixture, repo, ordinary)
	_, _, err := fixture.execute("task", "start", "op-task")
	require.Error(t, err)
	is.ErrorContains(err, "item is not an epic")
	is.ErrorContains(err, "use `orpheus task run op-task` for the normal task workflow")
	is.Empty(ordinary.started)
}

func TestIntegrationWorkflowTaskStartHonorsParentDependencyAndIdempotency(t *testing.T) {
	for _, tt := range []struct {
		name     string
		tasks    []taskmodel.Task
		wantOut  string
		wantErr  string
		wantCall []string
	}{
		{
			name: "parent not active",
			tasks: []taskmodel.Task{
				func() taskmodel.Task {
					epic := ownershipAuthoringEpic("op-epic", taskmodel.StatusOpen)
					epic.Relations.ParentID = "op-parent"
					return epic
				}(),
				ownershipAuthoringEpic("op-parent", taskmodel.StatusOpen),
			},
			wantErr: "parent epic op-parent must be in progress",
		},
		{
			name: "dependency remains active",
			tasks: []taskmodel.Task{
				func() taskmodel.Task {
					epic := ownershipAuthoringEpic("op-epic", taskmodel.StatusOpen)
					epic.Relations.DependencyIDs = []string{"op-dependency"}
					return epic
				}(),
				ownershipAuthoringTask("op-dependency", "Prerequisite", taskmodel.StatusInProgress, taskmodel.IssueTypeTask),
			},
			wantErr: "blocking dependencies are not closed: op-dependency",
		},
		{
			name:     "already in progress",
			tasks:    []taskmodel.Task{ownershipAuthoringEpic("op-epic", taskmodel.StatusInProgress)},
			wantOut:  "Epic op-epic is already in progress.\n",
			wantCall: nil,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCommandWorkflow(t)
			backend := newOwnershipAuthoringBackend(tt.tasks...)
			setupOwnershipAuthoringWorkflow(t, fixture, ownershipAuthoringRepo(), backend)

			stdout, stderr, err := fixture.execute("task", "start", "op-epic")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Empty(t, stdout)
				assert.Empty(t, stderr)
				assert.Empty(t, backend.started)
				return
			}
			require.NoError(t, err)
			assert.Empty(t, stderr)
			assert.Equal(t, tt.wantOut, stdout)
			assert.Equal(t, tt.wantCall, backend.started)
		})
	}
}

func TestIntegrationWorkflowTaskCloseRequiresVerifiedClosedChildrenAndIsIdempotent(t *testing.T) {
	for _, tt := range []struct {
		name     string
		tasks    []taskmodel.Task
		wantOut  string
		wantErr  string
		wantCall []string
	}{
		{
			name: "closes fully completed epic",
			tasks: []taskmodel.Task{
				func() taskmodel.Task {
					epic := ownershipAuthoringEpic("op-epic", taskmodel.StatusInProgress)
					epic.Relations.ChildCount = 2
					return epic
				}(),
				func() taskmodel.Task {
					child := ownershipAuthoringTask("op-child-a", "Child A", taskmodel.StatusClosed, taskmodel.IssueTypeTask)
					child.Relations.ParentID = "op-epic"
					return child
				}(),
				func() taskmodel.Task {
					child := ownershipAuthoringEpic("op-child-b", taskmodel.StatusClosed)
					child.Relations.ParentID = "op-epic"
					return child
				}(),
			},
			wantOut:  "Epic op-epic closed.\n",
			wantCall: []string{"op-epic"},
		},
		{
			name: "reports active child ids",
			tasks: []taskmodel.Task{
				func() taskmodel.Task {
					epic := ownershipAuthoringEpic("op-epic", taskmodel.StatusInProgress)
					epic.Relations.ChildCount = 2
					return epic
				}(),
				func() taskmodel.Task {
					child := ownershipAuthoringTask("op-child-z", "Child Z", taskmodel.StatusOpen, taskmodel.IssueTypeTask)
					child.Relations.ParentID = "op-epic"
					return child
				}(),
				func() taskmodel.Task {
					child := ownershipAuthoringTask("op-child-a", "Child A", taskmodel.StatusInProgress, taskmodel.IssueTypeTask)
					child.Relations.ParentID = "op-epic"
					return child
				}(),
			},
			wantErr: "direct child items are still active: op-child-a, op-child-z",
		},
		{
			name: "refuses incomplete child listing",
			tasks: []taskmodel.Task{
				func() taskmodel.Task {
					epic := ownershipAuthoringEpic("op-epic", taskmodel.StatusInProgress)
					epic.Relations.ChildCount = 2
					return epic
				}(),
				func() taskmodel.Task {
					child := ownershipAuthoringTask("op-child", "Child", taskmodel.StatusClosed, taskmodel.IssueTypeTask)
					child.Relations.ParentID = "op-epic"
					return child
				}(),
			},
			wantErr: "source reports 2 child items but only 1 could be inspected",
		},
		{
			name:     "already closed",
			tasks:    []taskmodel.Task{ownershipAuthoringEpic("op-epic", taskmodel.StatusClosed)},
			wantOut:  "Epic op-epic is already closed.\n",
			wantCall: nil,
		},
		{
			name:    "ordinary task uses normal workflow guidance",
			tasks:   []taskmodel.Task{ownershipAuthoringTask("op-epic", "Not epic", taskmodel.StatusInProgress, taskmodel.IssueTypeTask)},
			wantErr: "use `orpheus task run op-epic` for the normal task workflow",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCommandWorkflow(t)
			backend := newOwnershipAuthoringBackend(tt.tasks...)
			setupOwnershipAuthoringWorkflow(t, fixture, ownershipAuthoringRepo(), backend)

			stdout, stderr, err := fixture.execute("task", "close", "op-epic")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Empty(t, stdout)
				assert.Empty(t, stderr)
				assert.Empty(t, backend.closed)
				return
			}
			require.NoError(t, err)
			assert.Empty(t, stderr)
			assert.Equal(t, tt.wantOut, stdout)
			assert.Equal(t, tt.wantCall, backend.closed)
		})
	}
}

func TestIntegrationWorkflowTaskEpicLifecycleFailuresAreSourceNeutral(t *testing.T) {
	for _, tt := range []struct {
		name      string
		command   string
		status    taskmodel.Status
		failure   string
		wantError string
	}{
		{name: "start", command: "start", status: taskmodel.StatusOpen, failure: "Beads update failed in /fixture/repos/alpha", wantError: "cannot start epic op-epic"},
		{name: "close", command: "close", status: taskmodel.StatusInProgress, failure: "bd close failed in /fixture/repos/alpha", wantError: "cannot close epic op-epic"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCommandWorkflow(t)
			backend := newOwnershipAuthoringBackend(ownershipAuthoringEpic("op-epic", tt.status))
			if tt.command == "start" {
				backend.startError = sourceNeutralFailure(tt.failure)
			} else {
				backend.closeError = sourceNeutralFailure(tt.failure)
			}
			setupOwnershipAuthoringWorkflow(t, fixture, ownershipAuthoringRepo(), backend)

			stdout, stderr, err := fixture.execute("task", tt.command, "op-epic")

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantError)
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
			for _, forbidden := range []string{"Beads", "/fixture/repos/alpha", "bd update", "bd close"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("task %s error = %q, must not expose %q", tt.command, err, forbidden)
				}
			}
		})
	}
}
