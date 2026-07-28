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
)

type fakeCreateBackend struct {
	tasks   map[string]task.Task
	created task.CreateOptions
	err     error
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
		return task.Task{}, b.err
	}
	b.created = opts
	return task.Task{ID: "op-new", IssueType: opts.IssueType}, nil
}

func TestCreateServiceCreatesValidatedTaskGraphItem(t *testing.T) {
	backend := &fakeCreateBackend{tasks: map[string]task.Task{
		"op-parent": {ID: "op-parent", IssueType: task.IssueTypeEpic, Status: task.StatusOpen},
		"op-dep":    {ID: "op-dep", IssueType: task.IssueTypeTask, Status: task.StatusClosed},
	}}
	source := createTestSource("alpha", "op", t.TempDir())
	service := task.CreateService{
		Sources:        []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.CreateBackend, error) { return backend, nil },
	}

	created, err := service.Create(context.Background(), source, task.CreateRequest{
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
	if created.ID != "op-new" || created.IssueType != task.IssueTypeTask {
		t.Fatalf("created = %#v", created)
	}
	want := task.CreateOptions{
		Title: "Implement graph", Description: "Build the graph.", Design: "Keep it neutral.",
		AcceptanceCriteria: "It works.", ExternalRef: "PLAN-7", IssueType: task.IssueTypeTask,
		ParentID: "op-parent", BlockingIDs: []string{"op-dep"},
	}
	if !reflect.DeepEqual(backend.created, want) {
		t.Fatalf("backend options = %#v, want %#v", backend.created, want)
	}
}

func TestCreateServiceRejectsInvalidRelationsBeforeCreate(t *testing.T) {
	source := createTestSource("alpha", "op", t.TempDir())
	other := createTestSource("beta", "be", t.TempDir())
	cases := []struct {
		name    string
		tasks   map[string]task.Task
		request task.CreateRequest
		want    string
	}{
		{"closed parent", map[string]task.Task{"op-parent": {IssueType: task.IssueTypeEpic, Status: task.StatusClosed}}, task.CreateRequest{ParentID: "op-parent"}, "is closed"},
		{"non epic parent", map[string]task.Task{"op-parent": {IssueType: task.IssueTypeTask, Status: task.StatusOpen}}, task.CreateRequest{ParentID: "op-parent"}, "must be an epic"},
		{"missing parent", nil, task.CreateRequest{ParentID: "op-parent"}, "was not found"},
		{"unsupported dependency", map[string]task.Task{"op-bug": {IssueType: task.IssueTypeBug}}, task.CreateRequest{BlockingIDs: []string{"op-bug"}}, "must be a task or epic"},
		{"cross repository dependency", nil, task.CreateRequest{BlockingIDs: []string{"be-1"}}, "belongs to repository beta"},
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

func TestCreateServiceAcceptsOverlappingPrefixRelationsRegardlessOfSourceOrder(t *testing.T) {
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
				short := createTestSource("short", "op", t.TempDir())
				long := createTestSource("long", "op-long", t.TempDir())
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

				if _, err := service.Create(context.Background(), long, request); err != nil {
					t.Fatalf("Create() error = %v", err)
				}
			})
		}
	}
}

func TestNormalizeCreateOptionsRequiresTaskCreationFields(t *testing.T) {
	for _, opts := range []task.CreateOptions{
		{Description: "description", AcceptanceCriteria: "acceptance"},
		{Title: "title", AcceptanceCriteria: "acceptance"},
		{Title: "title", Description: "description"},
		{Title: "title", Description: "description", AcceptanceCriteria: "acceptance", IssueType: task.IssueTypeBug},
	} {
		if _, err := task.NormalizeCreateOptions(opts); err == nil {
			t.Fatalf("NormalizeCreateOptions(%#v) succeeded", opts)
		}
	}
	got, err := task.NormalizeCreateOptions(task.CreateOptions{Title: "title", Description: "description", AcceptanceCriteria: "acceptance", IssueType: task.IssueTypeEpic})
	if err != nil || got.IssueType != task.IssueTypeEpic {
		t.Fatalf("NormalizeCreateOptions epic = %#v, %v", got, err)
	}
}

func TestResolveCreationSourcePrecedence(t *testing.T) {
	root := t.TempDir()
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
	source := createTestSource("alpha", "op", t.TempDir())
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
