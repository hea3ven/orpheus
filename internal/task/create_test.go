package task_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
)

type fakeCreateBackend struct {
	tasks   map[string]task.Task
	created task.CreateOptions
	err     error
	partial task.Task
}

func (b *fakeCreateBackend) Get(_ context.Context, id string) (task.Task, error) {
	item, ok := b.tasks[id]
	if !ok {
		return task.Task{}, task.ErrNotFound
	}
	return item, nil
}

func (b *fakeCreateBackend) Create(_ context.Context, opts task.CreateOptions) (task.Task, error) {
	if b.err != nil {
		return b.partial, b.err
	}
	b.created = opts
	return task.Task{ID: "op-new", IssueType: opts.IssueType}, nil
}

func TestCreateServiceCreatesValidatedTaskGraphItem(t *testing.T) {
	for _, itemType := range []struct {
		name            string
		requested, want task.IssueType
		priority        *int
		wantPriority    int
	}{
		{name: "default task", want: task.IssueTypeTask, wantPriority: 2},
		{name: "explicit P0 task", requested: task.IssueTypeTask, want: task.IssueTypeTask, priority: new(0), wantPriority: 0},
		{name: "P4 epic", requested: task.IssueTypeEpic, want: task.IssueTypeEpic, priority: new(4), wantPriority: 4},
	} {
		t.Run(itemType.name, func(t *testing.T) {
			backend := &fakeCreateBackend{tasks: map[string]task.Task{
				"op-parent": {ID: "op-parent", IssueType: task.IssueTypeEpic, Status: task.StatusOpen},
				"op-dep":    {ID: "op-dep", IssueType: task.IssueTypeTask, Status: task.StatusClosed},
			}}
			source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
			service := task.CreateService{
				Sources:        []task.RepositorySource{source},
				BackendFactory: func(task.RepositorySource) (task.CreateBackend, error) { return backend, nil },
			}

			created, err := service.Create(context.Background(), source, task.CreateRequest{
				IssueType:          itemType.requested,
				Priority:           itemType.priority,
				Title:              " Implement graph ",
				Description:        " Build the graph. ",
				Design:             " Keep it neutral. ",
				AcceptanceCriteria: " It works. ",
				ExternalRef:        " PLAN-7 ",
				ParentID:           " op-parent ",
				BlockingIDs:        []string{" op-dep ", "op-dep"},
			})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if created.ID != "op-new" || created.IssueType != itemType.want {
				t.Fatalf("created = %#v", created)
			}
			want := task.CreateOptions{
				Title: "Implement graph", Description: "Build the graph.", Design: "Keep it neutral.",
				AcceptanceCriteria: "It works.", ExternalRef: "PLAN-7", IssueType: itemType.want,
				ParentID: "op-parent", BlockingIDs: []string{"op-dep"}, Priority: new(itemType.wantPriority),
			}
			if !reflect.DeepEqual(backend.created, want) {
				t.Fatalf("backend options = %#v, want %#v", backend.created, want)
			}
		})
	}
}

func TestCreateServiceEnforcesRequiredExternalReferenceBeforeCreation(t *testing.T) {
	for _, test := range []struct {
		name          string
		titleTemplate string
		issueType     task.IssueType
		externalRef   string
		wantCreated   bool
	}{
		{
			name:          "gated task without reference",
			titleTemplate: "[{{external_ref}}] {{summary}}",
			externalRef:   " \t\n ",
		},
		{
			name:          "gated task with reference",
			titleTemplate: "[{{external_ref}}] {{summary}}",
			externalRef:   " PLAN-7 ",
			wantCreated:   true,
		},
		{
			name:          "gated epic without reference",
			titleTemplate: "[{{external_ref}}] {{summary}}",
			issueType:     task.IssueTypeEpic,
			wantCreated:   true,
		},
		{
			name:        "ungated task without reference",
			wantCreated: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
			source.Repository.TitleTemplate = test.titleTemplate
			backend := &fakeCreateBackend{}
			service := task.CreateService{
				Sources:        []task.RepositorySource{source},
				BackendFactory: func(task.RepositorySource) (task.CreateBackend, error) { return backend, nil },
			}

			_, err := service.Create(context.Background(), source, task.CreateRequest{
				Title:              "title",
				Description:        "description",
				AcceptanceCriteria: "acceptance",
				ExternalRef:        test.externalRef,
				IssueType:          test.issueType,
			})
			if test.wantCreated {
				if err != nil {
					t.Fatalf("Create() error = %v", err)
				}
				if backend.created.Title == "" {
					t.Fatal("Create mutator was not called")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "--external-ref <reference>") {
				t.Fatalf("Create() error = %v, want external-reference guidance", err)
			}
			if backend.created.Title != "" {
				t.Fatalf("Create mutator was called with %#v", backend.created)
			}
		})
	}
}

func TestCreateServiceRejectsInvalidRelationsBeforeCreate(t *testing.T) {
	source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
	other := createTestSource("beta", "be", testutil.CanonicalTempDir(t))
	cases := []struct {
		name    string
		tasks   map[string]task.Task
		request task.CreateRequest
		want    string
	}{
		{"child waits on own parent", map[string]task.Task{"op-parent": {ID: "op-parent", IssueType: task.IssueTypeEpic, Status: task.StatusOpen}}, task.CreateRequest{ParentID: "op-parent", BlockingIDs: []string{"op-parent"}}, "circular"},
		{"closed parent", map[string]task.Task{"op-parent": {IssueType: task.IssueTypeEpic, Status: task.StatusClosed}}, task.CreateRequest{ParentID: "op-parent"}, "is closed"},
		{"non epic parent", map[string]task.Task{"op-parent": {IssueType: task.IssueTypeTask, Status: task.StatusOpen}}, task.CreateRequest{ParentID: "op-parent"}, "must be an epic"},
		{"missing parent", nil, task.CreateRequest{ParentID: "op-parent"}, "was not found"},
		{"unsupported dependency", map[string]task.Task{"op-bug": {IssueType: task.IssueTypeBug}}, task.CreateRequest{BlockingIDs: []string{"op-bug"}}, "must be a task or epic"},
		{"missing cross repository dependency", nil, task.CreateRequest{BlockingIDs: []string{"be-1"}}, "was not found in repository beta"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeCreateBackend{tasks: tt.tasks}
			service := task.CreateService{Sources: []task.RepositorySource{source, other}, BackendFactory: func(task.RepositorySource) (task.CreateBackend, error) { return backend, nil }}
			request := tt.request
			request.Title, request.Description, request.AcceptanceCriteria = "title", "description", "acceptance"
			_, err := service.Create(context.Background(), source, request)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Create() error = %v, want %q", err, tt.want)
			}
			if backend.created.Title != "" {
				t.Fatalf("Create was called with %#v", backend.created)
			}
		})
	}
}

func TestCreateServiceRejectsAmbiguousDependenciesRegardlessOfSourceOrder(t *testing.T) {
	for _, relation := range []struct {
		name    string
		request task.CreateRequest
		tasks   map[string]task.Task
	}{
		{
			name:    "parent",
			request: task.CreateRequest{ParentID: "op-long-parent"},
			tasks: map[string]task.Task{
				"op-long-parent": {ID: "op-long-parent", IssueType: task.IssueTypeEpic, Status: task.StatusOpen},
			},
		},
		{
			name:    "blocking dependency",
			request: task.CreateRequest{BlockingIDs: []string{"op-long-dependency"}},
			tasks: map[string]task.Task{
				"op-long-dependency": {ID: "op-long-dependency", IssueType: task.IssueTypeTask, Status: task.StatusOpen},
			},
		},
	} {
		for _, sourcesReversed := range []bool{false, true} {
			t.Run(relation.name+"/sources reversed="+fmt.Sprint(sourcesReversed), func(t *testing.T) {
				short := createTestSource("short", "op", testutil.CanonicalTempDir(t))
				long := createTestSource("long", "op-long", testutil.CanonicalTempDir(t))
				sources := []task.RepositorySource{short, long}
				if sourcesReversed {
					sources = []task.RepositorySource{long, short}
				}
				backend := &fakeCreateBackend{tasks: relation.tasks}
				service := task.CreateService{
					Sources:        sources,
					BackendFactory: func(task.RepositorySource) (task.CreateBackend, error) { return backend, nil },
				}
				request := relation.request
				request.Title, request.Description, request.AcceptanceCriteria = "title", "description", "acceptance"

				_, err := service.Create(context.Background(), long, request)
				if !errors.Is(err, task.ErrAmbiguousTaskPrefix) {
					t.Fatalf("Create() error = %v, want ambiguous prefix", err)
				}
				if backend.created.Title != "" {
					t.Fatal("mutated before rejecting ambiguous dependency")
				}
			})
		}
	}
}

func TestCreateServiceRequiresPlanningFieldsBeforeMutation(t *testing.T) {
	for _, tt := range []struct {
		name    string
		request task.CreateRequest
		want    string
	}{
		{name: "missing title", request: task.CreateRequest{Description: "description", AcceptanceCriteria: "acceptance"}, want: "title"},
		{name: "missing description", request: task.CreateRequest{Title: "title", AcceptanceCriteria: "acceptance"}, want: "description"},
		{name: "missing acceptance", request: task.CreateRequest{Title: "title", Description: "description"}, want: "acceptance"},
		{name: "unsupported type", request: task.CreateRequest{Title: "title", Description: "description", AcceptanceCriteria: "acceptance", IssueType: task.IssueTypeBug}, want: "unsupported item type"},
		{name: "negative priority", request: task.CreateRequest{Title: "title", Description: "description", AcceptanceCriteria: "acceptance", Priority: new(-1)}, want: "invalid priority"},
		{name: "priority above backlog", request: task.CreateRequest{Title: "title", Description: "description", AcceptanceCriteria: "acceptance", Priority: new(5)}, want: "invalid priority"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := createTestSource("alpha", "op", "/fixture/alpha")
			backend := &fakeCreateBackend{}
			service := task.CreateService{Sources: []task.RepositorySource{source}, BackendFactory: func(task.RepositorySource) (task.CreateBackend, error) { return backend, nil }}
			_, err := service.Create(t.Context(), source, tt.request)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("create error = %v, want %q", err, tt.want)
			}
			if backend.created.Title != "" {
				t.Fatalf("invalid request mutated backend: %#v", backend.created)
			}
		})
	}
}

func TestResolveCreationSourcePrecedence(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	alpha := createTestSource("alpha", "op", filepath.Join(root, "alpha"))
	beta := createTestSource("beta", "be", filepath.Join(root, "beta"))
	sources := []task.RepositorySource{alpha, beta}

	got, err := task.ResolveCreationSource(sources, task.CreationSourceOptions{Repository: "be", ActiveRepositoryID: "alpha", CurrentDirectory: alpha.Repository.Path})
	if err != nil || got.Repository.ID != "beta" {
		t.Fatalf("explicit source = %#v, %v", got, err)
	}
	got, err = task.ResolveCreationSource(sources, task.CreationSourceOptions{ActiveRepositoryID: "alpha", CurrentDirectory: beta.Repository.Path})
	if err != nil || got.Repository.ID != "alpha" {
		t.Fatalf("active source = %#v, %v", got, err)
	}
	got, err = task.ResolveCreationSource(sources, task.CreationSourceOptions{CurrentDirectory: filepath.Join(beta.Repository.Path, "nested")})
	if err != nil || got.Repository.ID != "beta" {
		t.Fatalf("cwd source = %#v, %v", got, err)
	}
	_, err = task.ResolveCreationSource(sources, task.CreationSourceOptions{CurrentDirectory: root})
	if err == nil || !strings.Contains(err.Error(), "pass --repo") {
		t.Fatalf("outside source error = %v", err)
	}
}

func TestCreateServiceBackendErrorIsPreserved(t *testing.T) {
	source := createTestSource("alpha", "op", testutil.CanonicalTempDir(t))
	backendErr := errors.New("Beads backend unavailable")
	service := task.CreateService{Sources: []task.RepositorySource{source}, BackendFactory: func(task.RepositorySource) (task.CreateBackend, error) { return nil, backendErr }}
	_, err := service.Create(context.Background(), source, task.CreateRequest{Title: "title", Description: "description", AcceptanceCriteria: "acceptance"})
	if !errors.Is(err, backendErr) {
		t.Fatalf("error = %v, want backend error", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "beads") {
		t.Fatalf("error = %v, want source-neutral task-facing error", err)
	}
}

func createTestSource(id string, prefix string, path string) task.RepositorySource {
	return task.RepositorySource{Repository: task.Repository{ID: id, Name: id, TaskIDPrefix: prefix, Path: path}, BackendDir: path}
}

func TestCreateServiceReportsCreatedIDWhenSetupFails(t *testing.T) {
	source := createTestSource("alpha", "op", "/fixture")
	setupErr := errors.New("dependency write failed")
	backend := &fakeCreateBackend{err: setupErr, partial: task.Task{ID: "op-created"}}
	service := task.CreateService{Sources: []task.RepositorySource{source}, BackendFactory: func(task.RepositorySource) (task.CreateBackend, error) { return backend, nil }}

	created, err := service.Create(t.Context(), source, task.CreateRequest{Title: "title", Description: "description", AcceptanceCriteria: "acceptance"})

	if !errors.Is(err, setupErr) || created.ID != "op-created" {
		t.Fatalf("Create() = %+v, %v; want partial task and original failure", created, err)
	}
	for _, text := range []string{"op-created", "was created", "task edit", "do not repeat task create"} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("error %q missing recovery guidance %q", err, text)
		}
	}
}

func (b *fakeCreateBackend) List(context.Context) ([]task.Task, error) {
	var items []task.Task
	for _, item := range b.tasks {
		items = append(items, item.Clone())
	}
	return items, nil
}
