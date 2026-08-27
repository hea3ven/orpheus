package task_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
)

type fakeUpdateBackend struct {
	tasks     map[string]task.Task
	getErrors map[string]error
	updated   bool
	opts      task.UpdateOptions
}

func (b *fakeUpdateBackend) Get(_ context.Context, id string) (task.Task, error) {
	if err := b.getErrors[id]; err != nil {
		return task.Task{}, err
	}
	item, ok := b.tasks[id]
	if !ok {
		return task.Task{}, task.ErrNotFound
	}
	return item, nil
}

func (b *fakeUpdateBackend) Update(_ context.Context, opts task.UpdateOptions) (task.Task, error) {
	b.updated = true
	b.opts = opts
	return b.tasks[opts.ID], nil
}

func TestUpdateServiceEnforcesRequiredExternalReferenceBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name          string
		titleTemplate string
		currentType   task.IssueType
		currentRef    string
		externalRef   *string
		wantUpdated   bool
	}{
		{
			name:          "rejects unrelated edit of legacy task without reference",
			titleTemplate: "[{{external_ref}}] {{summary}}",
			currentType:   task.IssueTypeTask,
		},
		{
			name:          "allows unrelated edit with existing reference",
			titleTemplate: "[{{external_ref}}] {{summary}}",
			currentType:   task.IssueTypeTask,
			currentRef:    "PLAN-7",
			wantUpdated:   true,
		},
		{
			name:          "allows repair of legacy task reference",
			titleTemplate: "[{{external_ref}}] {{summary}}",
			currentType:   task.IssueTypeTask,
			externalRef:   stringPtr(" PLAN-7 "),
			wantUpdated:   true,
		},
		{
			name:          "rejects clearing required reference",
			titleTemplate: "[{{external_ref}}] {{summary}}",
			currentType:   task.IssueTypeTask,
			currentRef:    "PLAN-7",
			externalRef:   stringPtr(" \t\n "),
		},
		{
			name:          "allows epic without reference",
			titleTemplate: "[{{external_ref}}] {{summary}}",
			currentType:   task.IssueTypeEpic,
			wantUpdated:   true,
		},
		{
			name:        "allows ungated task without reference",
			currentType: task.IssueTypeTask,
			wantUpdated: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
			source.Repository.TitleTemplate = test.titleTemplate
			backend := updateBackendWithCurrent()
			current := backend.tasks["op-current"]
			current.IssueType = test.currentType
			current.ExternalRef = test.currentRef
			backend.tasks["op-current"] = current

			_, err := updateService(source, backend).Update(context.Background(), source, task.UpdateOptions{
				ID:          "op-current",
				Title:       stringPtr("Updated title"),
				ExternalRef: test.externalRef,
			})
			if test.wantUpdated {
				if err != nil {
					t.Fatalf("Update() error = %v", err)
				}
				if !backend.updated {
					t.Fatal("Update mutator was not called")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "--external-ref <reference>") {
				t.Fatalf("Update() error = %v, want external-reference guidance", err)
			}
			if backend.updated {
				t.Fatalf("Update mutator was called with %#v", backend.opts)
			}
		})
	}
}

func TestUpdateServiceRejectsEmptyRequiredPlanningFieldsBeforeMutation(t *testing.T) {
	for _, request := range []struct {
		name string
		opts task.UpdateOptions
		want string
	}{
		{"title flag", task.UpdateOptions{Title: stringPtr("")}, "title is required"},
		{"description flag", task.UpdateOptions{Description: stringPtr(" \n ")}, "description is required"},
		{"acceptance flag", task.UpdateOptions{AcceptanceCriteria: stringPtr("")}, "acceptance criteria is required"},
	} {
		t.Run(request.name, func(t *testing.T) {
			source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
			backend := updateBackendWithCurrent()
			service := updateService(source, backend)
			request.opts.ID = "op-current"

			_, err := service.Update(context.Background(), source, request.opts)
			if err == nil || !strings.Contains(err.Error(), request.want) {
				t.Fatalf("Update() error = %v, want %q", err, request.want)
			}
			if backend.updated {
				t.Fatalf("Update mutator was called with %#v", backend.opts)
			}
		})
	}
}

func TestUpdateServiceRejectsUnsupportedTargetTypeBeforeMutation(t *testing.T) {
	source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
	backend := updateBackendWithCurrent()
	backend.tasks["op-current"] = task.Task{ID: "op-current", IssueType: task.IssueTypeBug, Status: task.StatusOpen}

	_, err := updateService(source, backend).Update(context.Background(), source, task.UpdateOptions{ID: "op-current"})
	if err == nil || !strings.Contains(err.Error(), "unsupported item type") {
		t.Fatalf("Update() error = %v, want unsupported type", err)
	}
	if backend.updated {
		t.Fatal("Update mutator was called")
	}
}

func TestUpdateServiceRejectsParentDescendantCyclesBeforeMutation(t *testing.T) {
	for _, relation := range []struct {
		name  string
		tasks map[string]task.Task
	}{
		{
			name: "direct descendant",
			tasks: map[string]task.Task{
				"op-current": currentUpdateTask(),
				"op-child":   {ID: "op-child", IssueType: task.IssueTypeEpic, Status: task.StatusOpen, Relations: task.RelationSummary{ParentID: "op-current"}},
			},
		},
		{
			name: "transitive descendant",
			tasks: map[string]task.Task{
				"op-current": currentUpdateTask(),
				"op-child":   {ID: "op-child", IssueType: task.IssueTypeEpic, Status: task.StatusOpen, Relations: task.RelationSummary{ParentID: "op-middle"}},
				"op-middle":  {ID: "op-middle", IssueType: task.IssueTypeEpic, Status: task.StatusOpen, Relations: task.RelationSummary{ParentID: "op-current"}},
			},
		},
	} {
		t.Run(relation.name, func(t *testing.T) {
			source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
			backend := &fakeUpdateBackend{tasks: relation.tasks}
			_, err := updateService(source, backend).Update(context.Background(), source, task.UpdateOptions{ID: "op-current", ParentID: stringPtr("op-child")})
			if err == nil || !strings.Contains(err.Error(), "parent-child cycle") {
				t.Fatalf("Update() error = %v, want parent cycle", err)
			}
			if backend.updated {
				t.Fatal("Update mutator was called")
			}
		})
	}
}

func TestUpdateServiceRejectsForeignRelationshipReferencesBeforeMutation(t *testing.T) {
	source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
	other := createTestSource("beta", "be", testutil.CanonicalTempDir(t))
	for _, request := range []struct {
		name string
		opts task.UpdateOptions
	}{
		{"parent", task.UpdateOptions{ParentID: stringPtr("be-parent")}},
		{"added dependency", task.UpdateOptions{AddBlockingIDs: []string{"be-dependency"}}},
		{"removed dependency", task.UpdateOptions{RemoveBlockingIDs: []string{"be-dependency"}}},
	} {
		t.Run(request.name, func(t *testing.T) {
			backend := updateBackendWithCurrent()
			service := task.UpdateService{
				Sources:        []task.RepositorySource{source, other},
				BackendFactory: func(task.RepositorySource) (task.UpdateBackend, error) { return backend, nil },
			}
			request.opts.ID = "op-current"
			_, err := service.Update(context.Background(), source, request.opts)
			if err == nil || !strings.Contains(err.Error(), "belongs to repository beta") {
				t.Fatalf("Update() error = %v, want foreign repository rejection", err)
			}
			if backend.updated {
				t.Fatal("Update mutator was called")
			}
		})
	}
}

func TestUpdateServiceFiltersAbsentBlockingRemovalsBeforeMutation(t *testing.T) {
	source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
	backend := updateBackendWithCurrent()
	backend.tasks["op-current"] = task.Task{
		ID: "op-current", Title: "Current plan", Description: "Current description", AcceptanceCriteria: "Current acceptance",
		IssueType: task.IssueTypeTask, Status: task.StatusOpen,
		Relations: task.RelationSummary{DependencyIDs: []string{"op-blocking"}},
	}
	backend.tasks["op-blocking"] = task.Task{ID: "op-blocking", IssueType: task.IssueTypeTask, Status: task.StatusOpen}

	_, err := updateService(source, backend).Update(context.Background(), source, task.UpdateOptions{
		ID:                "op-current",
		RemoveBlockingIDs: []string{"op-absent", "op-blocking"},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !backend.updated {
		t.Fatal("Update mutator was not called")
	}
	if want := []string{"op-blocking"}; !reflect.DeepEqual(backend.opts.RemoveBlockingIDs, want) {
		t.Fatalf("RemoveBlockingIDs = %#v, want %#v", backend.opts.RemoveBlockingIDs, want)
	}
}

func TestUpdateServiceRejectsRemovalOfUnsupportedBlockingDependencyBeforeMutation(t *testing.T) {
	source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
	backend := updateBackendWithCurrent()
	backend.tasks["op-current"] = task.Task{
		ID: "op-current", Title: "Current plan", Description: "Current description", AcceptanceCriteria: "Current acceptance",
		IssueType: task.IssueTypeTask, Status: task.StatusOpen,
		Relations: task.RelationSummary{DependencyIDs: []string{"op-bug"}},
	}
	backend.tasks["op-bug"] = task.Task{ID: "op-bug", IssueType: task.IssueTypeBug, Status: task.StatusOpen}

	_, err := updateService(source, backend).Update(context.Background(), source, task.UpdateOptions{
		ID:                "op-current",
		RemoveBlockingIDs: []string{"op-bug"},
	})
	if err == nil || !strings.Contains(err.Error(), "must be a task or epic") {
		t.Fatalf("Update() error = %v, want unsupported dependency rejection", err)
	}
	if backend.updated {
		t.Fatal("Update mutator was called")
	}
}

func TestUpdateServicePreservesLongFormPlanningContent(t *testing.T) {
	source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
	backend := updateBackendWithCurrent()
	description := "  indented description\n"
	design := "\tcode block\n\n"
	acceptance := "  - indented acceptance\n"

	_, err := updateService(source, backend).Update(context.Background(), source, task.UpdateOptions{
		ID:                 "op-current",
		Description:        &description,
		Design:             &design,
		AcceptanceCriteria: &acceptance,
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !backend.updated {
		t.Fatal("Update mutator was not called")
	}
	if backend.opts.Description == nil || *backend.opts.Description != description {
		t.Fatalf("Description = %#v, want %#v", backend.opts.Description, description)
	}
	if backend.opts.Design == nil || *backend.opts.Design != design {
		t.Fatalf("Design = %#v, want %#v", backend.opts.Design, design)
	}
	if backend.opts.AcceptanceCriteria == nil || *backend.opts.AcceptanceCriteria != acceptance {
		t.Fatalf("AcceptanceCriteria = %#v, want %#v", backend.opts.AcceptanceCriteria, acceptance)
	}
}

func TestUpdateServiceFailsClosedWhenDependencyGraphCannotBeRead(t *testing.T) {
	source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
	backend := updateBackendWithCurrent()
	backend.tasks["op-dependency"] = task.Task{
		ID: "op-dependency", IssueType: task.IssueTypeTask, Status: task.StatusOpen,
		Relations: task.RelationSummary{DependencyIDs: []string{"op-unreadable"}},
	}
	backend.getErrors = map[string]error{"op-unreadable": errors.New("backend unavailable")}

	_, err := updateService(source, backend).Update(context.Background(), source, task.UpdateOptions{
		ID: "op-current", AddBlockingIDs: []string{"op-dependency"},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot inspect blocking dependency") {
		t.Fatalf("Update() error = %v, want graph read failure", err)
	}
	if backend.updated {
		t.Fatal("Update mutator was called")
	}
}

func updateService(source task.RepositorySource, backend *fakeUpdateBackend) task.UpdateService {
	return task.UpdateService{
		Sources:        []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.UpdateBackend, error) { return backend, nil },
	}
}

func updateBackendWithCurrent() *fakeUpdateBackend {
	return &fakeUpdateBackend{tasks: map[string]task.Task{"op-current": currentUpdateTask()}}
}

func currentUpdateTask() task.Task {
	return task.Task{
		ID: "op-current", Title: "Current plan", Description: "Current description", AcceptanceCriteria: "Current acceptance",
		IssueType: task.IssueTypeTask, Status: task.StatusOpen,
	}
}

func stringPtr(value string) *string { return &value }
