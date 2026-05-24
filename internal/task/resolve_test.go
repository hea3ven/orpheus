package task_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
)

func TestResolveTaskSourceKnownPrefix(t *testing.T) {
	sources := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "op"}, BackendDir: "/tmp/alpha"},
		{Repository: task.Repository{ID: "beta", Name: "Beta", TaskIDPrefix: "bt"}, BackendDir: "/tmp/beta"},
	}

	got, err := task.ResolveTaskSource(sources, " op-9wh.3 ")
	if err != nil {
		t.Fatalf("resolve known task prefix: %v", err)
	}
	if got.TaskID != "op-9wh.3" {
		t.Fatalf("task id = %q, want trimmed op-9wh.3", got.TaskID)
	}
	if got.Prefix != "op" {
		t.Fatalf("prefix = %q, want op", got.Prefix)
	}
	if got.Source.Repository.ID != "alpha" ||
		got.Source.Repository.Name != "Alpha" ||
		got.Source.BackendDir != "/tmp/alpha" {
		t.Fatalf("source = %#v, want alpha repo context", got.Source)
	}
}

func TestResolveTaskSourceSupportsHyphenatedRegisteredPrefix(t *testing.T) {
	sources := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "my-admin"}, BackendDir: "/tmp/alpha"},
	}

	got, err := task.ResolveTaskSource(sources, "my-admin-123")
	if err != nil {
		t.Fatalf("resolve hyphenated prefix: %v", err)
	}
	if got.Prefix != "my-admin" || got.Source.Repository.ID != "alpha" {
		t.Fatalf("resolved = %#v, want my-admin alpha", got)
	}

	_, err = task.ResolveTaskSource(sources, "my-admin-")
	if err == nil {
		t.Fatal("resolve hyphenated prefix without task number succeeded, want error")
	}
	if !errors.Is(err, task.ErrMalformedTaskID) ||
		!strings.Contains(err.Error(), "missing the Beads task number") {
		t.Fatalf("error = %v, want malformed missing task number", err)
	}
}

func TestResolveTaskSourceUnknownPrefixIsActionable(t *testing.T) {
	sources := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "op"}, BackendDir: "/tmp/alpha"},
	}

	_, err := task.ResolveTaskSource(sources, "zz-1")
	if err == nil {
		t.Fatal("resolve unknown task prefix succeeded, want error")
	}
	if !errors.Is(err, task.ErrUnknownTaskPrefix) {
		t.Fatalf("error = %v, want ErrUnknownTaskPrefix", err)
	}
	for _, want := range []string{"zz-1", "zz", "orpheus repo list", "register the repo"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want substring %q", err, want)
		}
	}
}

func TestResolveTaskSourceMalformedTaskIDsAreActionable(t *testing.T) {
	sources := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "op"}, BackendDir: "/tmp/alpha"},
	}

	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "blank", id: "  ", want: "task id is required"},
		{name: "no separator", id: "op", want: "no Beads prefix separator"},
		{name: "missing prefix", id: "-1", want: "missing a Beads prefix"},
		{name: "missing number", id: "op-", want: "missing the Beads task number"},
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

func TestResolveTaskSourceMissingRegisteredPrefixes(t *testing.T) {
	sources := []task.RepositorySource{
		{Repository: task.Repository{ID: "legacy", Name: "Legacy"}, BackendDir: "/tmp/legacy"},
	}

	_, err := task.ResolveTaskSource(sources, "op-1")
	if err == nil {
		t.Fatal("resolve without registered prefixes succeeded, want error")
	}
	if !errors.Is(err, task.ErrUnknownTaskPrefix) {
		t.Fatalf("error = %v, want ErrUnknownTaskPrefix", err)
	}
	for _, want := range []string{
		"no registered repositories have Beads prefixes",
		"legacy",
		"orpheus repo list",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want substring %q", err, want)
		}
	}
}

func TestResolveTaskSourceRejectsCollisionPreventedPrefixes(t *testing.T) {
	sources := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "op"}, BackendDir: "/tmp/alpha"},
		{Repository: task.Repository{ID: "beta", Name: "Beta", TaskIDPrefix: "op"}, BackendDir: "/tmp/beta"},
	}

	_, err := task.ResolveTaskSource(sources, "op-1")
	if err == nil {
		t.Fatal("resolve duplicate registered prefixes succeeded, want error")
	}
	if !errors.Is(err, task.ErrAmbiguousTaskPrefix) {
		t.Fatalf("error = %v, want ErrAmbiguousTaskPrefix", err)
	}
	for _, want := range []string{
		"multiple registered Beads prefixes",
		"alpha",
		"beta",
		"repair the registry",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want substring %q", err, want)
		}
	}
}
