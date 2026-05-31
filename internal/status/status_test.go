package status_test

import (
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/status"
	"github.com/hea3ven/orpheus/internal/task"
)

func TestProjectGroupsItemsByLocalM2Policy(t *testing.T) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"},
		Tasks: []task.Task{
			{ID: "a-ready", Title: "ready", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
			{ID: "a-dep", Title: "dependency", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
			{ID: "a-blocked", Title: "blocked", Status: task.StatusOpen, IssueType: task.IssueTypeTask, Relations: task.RelationSummary{DependencyIDs: []string{"a-dep"}}},
			{ID: "a-epic-ready", Title: "epic ready", Status: task.StatusOpen, IssueType: task.IssueTypeEpic},
			{ID: "a-epic-blocked", Title: "epic blocked", Status: task.StatusOpen, IssueType: task.IssueTypeEpic, Relations: task.RelationSummary{DependencyIDs: []string{"a-dep"}}},
			{ID: "a-review", Title: "review", Status: task.StatusInProgress, IssueType: task.IssueTypeTask, Metadata: task.Metadata{task.MetadataPRURL: "https://example.test/pr/1"}},
			{ID: "a-working", Title: "working", Status: task.StatusInProgress, IssueType: task.IssueTypeTask},
			{ID: "a-epic-working", Title: "epic working", Status: task.StatusInProgress, IssueType: task.IssueTypeEpic},
			{ID: "a-done", Title: "done", Status: task.StatusClosed, IssueType: task.IssueTypeTask},
			{ID: "a-epic-done", Title: "epic done", Status: task.StatusClosed, IssueType: task.IssueTypeEpic},
			{ID: "a-unknown", Title: "unknown", Status: task.StatusUnknown, IssueType: task.IssueTypeTask},
		},
	}}}

	got := status.Project(snapshot)

	assertGroupTaskIDs(t, got, status.GroupReadyToRun, []string{"a-ready", "a-dep", "a-epic-ready"})
	assertGroupTaskIDs(t, got, status.GroupWorking, []string{"a-working", "a-epic-working"})
	assertGroupTaskIDs(t, got, status.GroupBlocked, []string{"a-blocked", "a-epic-blocked"})
	assertGroupTaskIDs(t, got, status.GroupInReview, []string{"a-review"})
	assertGroupTaskIDs(t, got, status.GroupDoneClosed, []string{"a-done", "a-epic-done"})
	assertGroupTaskIDs(t, got, status.GroupUnknown, []string{"a-unknown"})

	reviewEntry := groupEntries(t, got, status.GroupInReview)[0]
	if reviewEntry.Detail != "https://example.test/pr/1" {
		t.Fatalf("review detail = %q, want PR URL", reviewEntry.Detail)
	}
	blockedEntry := groupEntries(t, got, status.GroupBlocked)[0]
	if blockedEntry.Detail != "blocked by a-dep" {
		t.Fatalf("blocked detail = %q, want dependency detail", blockedEntry.Detail)
	}
}

func TestProjectTreatsSameRepoClosedDependenciesAsReady(t *testing.T) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"},
		Tasks: []task.Task{
			{ID: "a-dep", Title: "done dependency", Status: task.StatusClosed, IssueType: task.IssueTypeTask},
			{ID: "a-ready", Title: "ready", Status: task.StatusOpen, IssueType: task.IssueTypeTask, Relations: task.RelationSummary{DependencyIDs: []string{"a-dep"}}},
		},
	}}}

	got := status.Project(snapshot)

	assertGroupTaskIDs(t, got, status.GroupReadyToRun, []string{"a-ready"})
	assertGroupTaskIDs(t, got, status.GroupDoneClosed, []string{"a-dep"})
}

func TestProjectTreatsMissingDependenciesAsUnknown(t *testing.T) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"},
		Tasks:      []task.Task{{ID: "a-task", Title: "missing dependency", Status: task.StatusOpen, IssueType: task.IssueTypeTask, Relations: task.RelationSummary{DependencyIDs: []string{"a-missing"}}}},
	}}}

	got := status.Project(snapshot)
	entries := groupEntries(t, got, status.GroupUnknown)

	if len(entries) != 1 || entries[0].Task.ID != "a-task" || entries[0].Detail != "missing dependency a-missing" {
		t.Fatalf("unknown entries = %#v, want missing dependency detail", entries)
	}
	assertGroupTaskIDs(t, got, status.GroupReadyToRun, nil)
}

func TestReadyRowsUsesCanonicalReadinessPolicyForEligibleIssueTypes(t *testing.T) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"},
		Tasks: []task.Task{
			{ID: "a-task", Title: "task", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
			{ID: "a-bug", Title: "bug", Status: task.StatusOpen, IssueType: task.IssueTypeBug},
			{ID: "a-chore", Title: "chore", Status: task.StatusOpen, IssueType: task.IssueTypeChore},
			{ID: "a-unknown-type", Title: "unknown type", Status: task.StatusOpen, IssueType: task.IssueTypeUnknown},
			{ID: "a-epic", Title: "epic", Status: task.StatusOpen, IssueType: task.IssueTypeEpic},
			{ID: "a-epic-working", Title: "epic working", Status: task.StatusInProgress, IssueType: task.IssueTypeEpic},
			{ID: "a-epic-done", Title: "epic done", Status: task.StatusClosed, IssueType: task.IssueTypeEpic},
			{ID: "a-review", Title: "review", Status: task.StatusOpen, IssueType: task.IssueTypeTask, Metadata: task.Metadata{task.MetadataPRURL: "https://example.test/pr/2"}},
			{ID: "a-started", Title: "started", Status: task.StatusInProgress, IssueType: task.IssueTypeTask},
			{ID: "a-closed", Title: "closed", Status: task.StatusClosed, IssueType: task.IssueTypeTask},
		},
	}}}

	got := status.ReadyRows(snapshot)
	ids := make([]string, 0, len(got))
	for _, row := range got {
		ids = append(ids, row.Task.ID)
	}

	expected := []string{"a-task", "a-bug", "a-chore", "a-unknown-type", "a-epic"}
	if len(ids) != len(expected) {
		t.Fatalf("ready ids = %v, want %v", ids, expected)
	}
	for i := range ids {
		if ids[i] != expected[i] {
			t.Fatalf("ready ids = %v, want %v", ids, expected)
		}
	}
}

func TestProjectAddsStructuredRepoFailuresToUnknownNeedsAttention(t *testing.T) {
	failureErr := errors.New("bd list failed")
	snapshot := task.SnapshotResult{Failures: []task.RepoFailure{{
		Repository: task.Repository{ID: "broken", Name: "Broken", TaskIDPrefix: "br"},
		Source:     "task_backend",
		Operation:  "snapshot",
		Err:        failureErr,
	}}}

	got := status.Project(snapshot)
	entries := groupEntries(t, got, status.GroupUnknown)

	if len(entries) != 1 {
		t.Fatalf("unknown entries = %#v, want one repo failure", entries)
	}
	entry := entries[0]
	if entry.Kind != status.EntryRepoFailure || entry.Repository.ID != "broken" || entry.Source != "task_backend" || entry.Operation != "snapshot" || !errors.Is(entry.Failure, failureErr) {
		t.Fatalf("unknown entry = %#v, want structured broken repo failure", entry)
	}
}

func assertGroupTaskIDs(t *testing.T, projection status.Projection, groupID status.GroupID, expected []string) {
	t.Helper()

	entries := groupEntries(t, projection, groupID)
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Kind != status.EntryTask {
			continue
		}
		got = append(got, entry.Task.ID)
	}
	if len(got) != len(expected) {
		t.Fatalf("group %s task ids = %v, want %v", groupID, got, expected)
	}
	for i := range got {
		if got[i] != expected[i] {
			t.Fatalf("group %s task ids = %v, want %v", groupID, got, expected)
		}
	}
}

func groupEntries(t *testing.T, projection status.Projection, groupID status.GroupID) []status.Entry {
	t.Helper()

	for _, group := range projection.Groups {
		if group.ID == groupID {
			return group.Entries
		}
	}
	t.Fatalf("missing group %s", groupID)
	return nil
}
