package task

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CheckRepositoryRemovalRelationships refuses deletion while dependencies or
// epic membership cross the repository boundary. Every source is read, including
// closed items; partial list relationships are completed through Get.
func CheckRepositoryRemovalRelationships(ctx context.Context, sources []RepositorySource, factory BackendFactory, repositoryID string) error {
	var failures []error
	relationships := make(map[string]struct{})
	for _, source := range sources {
		if err := inspectRemovalRelationships(ctx, sources, factory, source, repositoryID, relationships); err != nil {
			failures = append(failures, fmt.Errorf("repository %s: %w", source.Repository.ID, err))
		}
	}
	var result error
	if len(failures) > 0 {
		result = fmt.Errorf("cannot complete cross-repository relationship check; repair task-source reads before retrying removal: %w", errors.Join(failures...))
	}
	if len(relationships) > 0 {
		lines := make([]string, 0, len(relationships))
		for relationship := range relationships {
			lines = append(lines, relationship)
		}
		sort.Strings(lines)
		result = errors.Join(result, fmt.Errorf("cross-repository relationships prevent removal:\n  %s\nremove these relationships before deleting the repository", strings.Join(lines, "\n  ")))
	}
	return result
}

func inspectRemovalRelationships(ctx context.Context, sources []RepositorySource, factory BackendFactory, source RepositorySource, target string, relationships map[string]struct{}) error {
	backend, err := factory(source)
	if err != nil {
		return err
	}
	items, err := backend.List(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if !item.Relations.Complete {
			detail, err := backend.Get(ctx, item.ID)
			if err != nil {
				return fmt.Errorf("read relationships for task %s: %w", item.ID, err)
			}
			if !detail.Relations.Complete {
				return fmt.Errorf("task %s has incomplete relationships", item.ID)
			}
			item = detail
		}
		if err := collectRemovalRelationships(sources, source.Repository.ID, target, item, relationships); err != nil {
			return err
		}
	}
	return nil
}

func collectRemovalRelationships(sources []RepositorySource, owner, target string, item Task, relationships map[string]struct{}) error {
	groups := []struct {
		ids     []string
		kind    string
		reverse bool
	}{
		{item.Relations.DependencyIDs, "depends on", false},
		{item.Relations.DependentIDs, "depends on", true},
		{[]string{item.Relations.ParentID}, "belongs to epic", false},
		{item.Relations.ChildIDs, "belongs to epic", true},
	}
	for _, group := range groups {
		for _, id := range group.ids {
			if strings.TrimSpace(id) == "" {
				continue
			}
			resolved, err := ResolveTaskSource(sources, id)
			if err != nil {
				return fmt.Errorf("task %s relationship %s: %w", item.ID, id, err)
			}
			other := resolved.Source.Repository.ID
			if other == owner || (owner != target && other != target) {
				continue
			}
			from, to := owner+"/"+item.ID, other+"/"+id
			if group.reverse {
				from, to = to, from
			}
			relationships[from+" "+group.kind+" "+to] = struct{}{}
		}
	}
	return nil
}
