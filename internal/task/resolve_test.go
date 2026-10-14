package task_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
)

func TestResolveTaskSourceKnownPrefix(t *testing.T) {
	for _, tt := range []struct{ prefix, id, wantID string }{
		{prefix: "op", id: " op-9wh.3 ", wantID: "op-9wh.3"},
		{prefix: "my-admin", id: "my-admin-123", wantID: "my-admin-123"},
	} {
		t.Run(tt.prefix, func(t *testing.T) {
			sources := []task.RepositorySource{
				{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: tt.prefix}, BackendDir: "/fixture/alpha"},
				{Repository: task.Repository{ID: "beta", Name: "Beta", TaskIDPrefix: "bt"}, BackendDir: "/fixture/beta"},
			}
			got, err := task.ResolveTaskSource(sources, tt.id)
			if err != nil {
				t.Fatal(err)
			}
			if got.TaskID != tt.wantID || got.Prefix != tt.prefix || got.Source.Repository.ID != "alpha" || got.Source.Repository.Name != "Alpha" || got.Source.BackendDir != "/fixture/alpha" {
				t.Fatalf("resolved = %#v", got)
			}
		})
	}
}

func TestResolveTaskSourceUnknownPrefixIsActionable(t *testing.T) {
	for _, tt := range []struct {
		name, prefix, id string
		want             []string
	}{
		{name: "unknown prefix", prefix: "op", id: "zz-1", want: []string{"zz-1", "zz", "orpheus repo list", "register the repo"}},
		{name: "no registered prefixes", id: "op-1", want: []string{"no registered repositories have task prefixes", "alpha", "orpheus repo list"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sources := []task.RepositorySource{{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: tt.prefix}, BackendDir: "/fixture/alpha"}}
			_, err := task.ResolveTaskSource(sources, tt.id)
			if !errors.Is(err, task.ErrUnknownTaskPrefix) {
				t.Fatalf("error = %v, want ErrUnknownTaskPrefix", err)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want %q", err, want)
				}
			}
		})
	}
}

func TestResolveTaskSourceMalformedTaskIDsAreActionable(t *testing.T) {
	sources := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "op"}, BackendDir: "/fixture/alpha"},
		{Repository: task.Repository{ID: "admin", TaskIDPrefix: "my-admin"}, BackendDir: "/fixture/admin"},
	}

	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "blank", id: "  ", want: "task id is required"},
		{name: "no separator", id: "op", want: "no task prefix separator"},
		{name: "missing prefix", id: "-1", want: "missing a task prefix"},
		{name: "missing number", id: "op-", want: "missing the task suffix"},
		{name: "hyphenated empty suffix", id: "my-admin-", want: "missing the task suffix"},
		{name: "whitespace", id: "op 1", want: "contains whitespace"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := task.ResolveTaskSource(sources, tt.id)
			if err == nil {
				t.Fatal("resolve malformed task id succeeded, want error")
			}
			if !errors.Is(err, task.ErrMalformedTaskID) {
				t.Fatalf("error = %v, want ErrMalformedTaskID", err)
			}
			if !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "<prefix>") {
				t.Fatalf("error = %v, want actionable malformed-id guidance containing %q", err, tt.want)
			}
		})
	}
}

func TestResolveTaskSourceRejectsCollisionPreventedPrefixes(t *testing.T) {
	sources := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "op"}, BackendDir: "/fixture/alpha"},
		{Repository: task.Repository{ID: "beta", Name: "Beta", TaskIDPrefix: "op"}, BackendDir: "/fixture/beta"},
	}

	_, err := task.ResolveTaskSource(sources, "op-1")
	if err == nil {
		t.Fatal("resolve duplicate registered prefixes succeeded, want error")
	}
	if !errors.Is(err, task.ErrAmbiguousTaskPrefix) {
		t.Fatalf("error = %v, want ErrAmbiguousTaskPrefix", err)
	}
	for _, want := range []string{
		"multiple registered task prefixes",
		"alpha",
		"beta",
		"repair the registry",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want substring %q", err, want)
		}
	}
}
