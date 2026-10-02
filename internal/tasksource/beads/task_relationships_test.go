package beads_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/tasksource/beads"
)

func relationshipShowCall(t *testing.T, show, outgoing, incoming string) fakeCall {
	t.Helper()
	var items []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(show), &items); err != nil {
		t.Fatal(err)
	}
	items[0]["dependencies"] = json.RawMessage(outgoing)
	items[0]["dependents"] = json.RawMessage(incoming)
	payload, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return fakeCall{
		wantArgs: []string{"--json", "--readonly", "--sandbox", "show", "--id", "op-item", "--include-dependents"},
		result:   beads.Result{Stdout: string(payload)},
	}
}

func TestTaskBackendGetClassifiesCompleteDirectRelationshipsInOneCall(t *testing.T) {
	runner := &fakeRunner{calls: []fakeCall{relationshipShowCall(t,
		`[{"id":"op-item","issue_type":"epic","parent":"op-parent","dependency_count":7,"dependent_count":9}]`,
		`[
			{"id":"op-parent","dependency_type":"parent-child","issue_type":"epic","status":"in_progress","title":"Parent"},
			{"id":"edge-parent","issue_id":"op-item","depends_on_id":"op-parent","type":"parent-child"},
			{"id":"op-blocker","dependency_type":"blocks","issue_type":"task","status":"closed","title":"Blocker","metadata":{"orpheus.pr_url":"https://example.test/pr/1"}},
			{"issue_id":"op-item","depends_on_id":"op-blocker","type":"blocks"},
			{"id":"op-cross-blocker","dependency_type":"orpheus-blocks","issue_type":"epic","status":"open"},
			{"id":"op-related","dependency_type":"related"},
			{"depends_on_id":"op-unresolved","type":"blocks"}
		]`,
		`[
			{"id":"op-child","dependency_type":"parent-child","issue_type":"task","status":"closed","title":"Child"},
			{"id":"edge-child","issue_id":"op-child","depends_on_id":"op-item","type":"parent-child"},
			{"id":"op-dependent","dependency_type":"blocks","issue_type":"epic","status":"open"},
			{"id":"edge-dependent","issue_id":"op-dependent","depends_on_id":"op-item","type":"blocks"},
			{"id":"op-cross-dependent","dependency_type":"orpheus-blocks"},
			{"id":"op-tracks","dependency_type":"tracks"},
			{"id":"op-related","dependency_type":"related"},
			{"issue_id":"op-unresolved-child","depends_on_id":"op-item","type":"parent-child"},
			{"issue_id":"op-unresolved-dependent","depends_on_id":"op-item","type":"blocks"}
		]`,
	)}}
	backend, err := beads.NewTaskBackendWithRunner("/fixture/beads", runner)
	if err != nil {
		t.Fatal(err)
	}

	got, err := backend.Get(context.Background(), "op-item")

	if err != nil {
		t.Fatal(err)
	}
	want := task.RelationSummary{
		Complete: true, ParentID: "op-parent",
		ChildIDs:        []string{"op-child", "op-unresolved-child"},
		DependencyIDs:   []string{"op-blocker", "op-cross-blocker", "op-unresolved"},
		DependentIDs:    []string{"op-cross-dependent", "op-dependent", "op-unresolved-dependent"},
		DependencyCount: 3, DependentCount: 3, BlockedByCount: 3, BlockingCount: 3, ChildCount: 2,
	}
	if !reflect.DeepEqual(got.Relations, want) {
		t.Fatalf("relations = %#v, want %#v", got.Relations, want)
	}
	var relatedIDs []string
	for _, item := range got.RelatedItems {
		relatedIDs = append(relatedIDs, item.ID)
		if item.ID == "op-child" && item.Relations.ParentID != "op-item" {
			t.Fatalf("child = %#v", item)
		}
		if item.ID == "op-blocker" && item.Metadata[task.MetadataPRURL] != "https://example.test/pr/1" {
			t.Fatalf("blocker metadata = %#v", item.Metadata)
		}
	}
	if want := []string{"op-parent", "op-blocker", "op-cross-blocker", "op-child", "op-dependent"}; !reflect.DeepEqual(relatedIDs, want) {
		t.Fatalf("related IDs = %v, want %v", relatedIDs, want)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unused calls = %d", len(runner.calls))
	}
}

func TestTaskBackendGetRejectsConflictingParentsAndMalformedRelationships(t *testing.T) {
	for _, tt := range []struct{ name, show, outgoing, incoming, want string }{
		{"conflicting explicit parent", `[{"id":"op-item","issue_type":"task","parent":"op-first"}]`, `[{"id":"op-second","dependency_type":"parent-child"}]`, `[]`, "conflicting parents"},
		{"conflicting parent edges", `[{"id":"op-item","issue_type":"task"}]`, `[{"id":"op-first","dependency_type":"parent-child"},{"id":"op-second","dependency_type":"parent-child"}]`, `[]`, "conflicting parents"},
		{"missing parent identifier", `[{"id":"op-item","issue_type":"task"}]`, `[{"dependency_type":"parent-child"}]`, `[]`, "without an identifier"},
		{"missing blocker identifier", `[{"id":"op-item","issue_type":"task"}]`, `[{"issue_id":"op-item","type":"blocks"}]`, `[]`, "without an identifier"},
		{"missing child identifier", `[{"id":"op-item","issue_type":"task"}]`, `[]`, `[{"depends_on_id":"op-item","type":"parent-child"}]`, "without an identifier"},
		{"invalid related item", `[{"id":"op-item","issue_type":"task"}]`, `[]`, `[{"id":"op-child","issue_type":"task","status":"closed","created_at":"invalid","dependency_type":"parent-child"}]`, "created_at"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{calls: []fakeCall{relationshipShowCall(t, tt.show, tt.outgoing, tt.incoming)}}
			backend, err := beads.NewTaskBackendWithRunner("/fixture/beads", runner)
			if err != nil {
				t.Fatal(err)
			}

			got, err := backend.Get(context.Background(), "op-item")

			if err == nil || !strings.Contains(err.Error(), tt.want) || got.ID != "" {
				t.Fatalf("Get() = %#v, %v, want error containing %q and no task", got, err, tt.want)
			}
		})
	}
}

func TestTaskBackendGetRequiresSuccessfulRelationshipReads(t *testing.T) {
	for _, failure := range []string{"command", "JSON"} {
		t.Run(failure, func(t *testing.T) {
			call := relationshipShowCall(t, `[{"id":"op-item","issue_type":"task"}]`, `[]`, `[]`)
			if failure == "command" {
				call.err = errors.New("relationship read failed")
			} else {
				call.result.Stdout = `not JSON`
			}
			runner := &fakeRunner{calls: []fakeCall{call}}
			backend, err := beads.NewTaskBackendWithRunner("/fixture/beads", runner)
			if err != nil {
				t.Fatal(err)
			}

			got, err := backend.Get(context.Background(), "op-item")

			if err == nil || !reflect.DeepEqual(got, task.Task{}) {
				t.Fatalf("Get() = %#v, %v, want no task and an error", got, err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("unused calls = %d", len(runner.calls))
			}
		})
	}
}

func TestTaskBackendGetRejectsIncompleteRelationshipsWithoutRecovery(t *testing.T) {
	for _, tt := range []struct{ name, show, outgoing, incoming, want string }{
		{"missing outgoing rows", `[{"id":"op-item","issue_type":"task","dependency_count":1}]`, `[]`, `[]`, `get Beads task "op-item": incomplete dependencies: source reports 1 but returned 0`},
		{"partial outgoing rows", `[{"id":"op-item","issue_type":"task","dependency_count":2}]`, `[{"id":"op-blocker","dependency_type":"blocks"}]`, `[]`, `get Beads task "op-item": incomplete dependencies: source reports 2 but returned 1`},
		{"missing incoming rows", `[{"id":"op-item","issue_type":"epic","dependent_count":1}]`, `[]`, `[]`, `get Beads task "op-item": incomplete dependents: source reports 1 but returned 0`},
		{"partial incoming rows", `[{"id":"op-item","issue_type":"epic","dependent_count":2}]`, `[]`, `[{"id":"op-child","dependency_type":"parent-child"}]`, `get Beads task "op-item": incomplete dependents: source reports 2 but returned 1`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{calls: []fakeCall{relationshipShowCall(t, tt.show, tt.outgoing, tt.incoming)}}
			backend, err := beads.NewTaskBackendWithRunner("/fixture/beads", runner)
			if err != nil {
				t.Fatal(err)
			}

			got, err := backend.Get(context.Background(), "op-item")

			if err == nil || err.Error() != tt.want || !reflect.DeepEqual(got, task.Task{}) {
				t.Fatalf("Get() = %#v, %v, want zero task and %q", got, err, tt.want)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("unused calls = %d", len(runner.calls))
			}
		})
	}
}

func TestTaskBackendGetPreservesUnsupportedReferenceWithoutExposingRelatedItem(t *testing.T) {
	runner := &fakeRunner{calls: []fakeCall{relationshipShowCall(t, `[{"id":"op-item","issue_type":"task","dependency_count":2}]`,
		`[{"id":"op-parent","issue_type":"epic","status":"in_progress","title":"Parent","dependency_type":"parent-child"},
		{"id":"op-bug","issue_type":"bug","status":"open","dependency_type":"blocks"}]`, `[]`),
	}}
	backend, err := beads.NewTaskBackendWithRunner("/fixture/beads", runner)
	if err != nil {
		t.Fatal(err)
	}

	got, err := backend.Get(context.Background(), "op-item")

	if err != nil {
		t.Fatal(err)
	}
	if !got.Relations.Complete || got.Relations.ParentID != "op-parent" || !reflect.DeepEqual(got.Relations.DependencyIDs, []string{"op-bug"}) {
		t.Fatalf("relations = %#v", got.Relations)
	}
	if len(got.RelatedItems) != 1 || got.RelatedItems[0].ID != "op-parent" || got.RelatedItems[0].Title != "Parent" {
		t.Fatalf("related items = %#v", got.RelatedItems)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unused calls = %d", len(runner.calls))
	}
}
