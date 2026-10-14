package task

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// MetadataCrossRepositoryParent stores a parent on the child in its owning source.
const MetadataCrossRepositoryParent = "orpheus.cross_repository_parent"

// CrossRepositoryParent returns the source-owned remote parent identifier.
func CrossRepositoryParent(metadata Metadata) string {
	return strings.TrimSpace(metadata[MetadataCrossRepositoryParent])
}

// IncludeCrossRepositoryParent rejects conflicting native and metadata parents.
func IncludeCrossRepositoryParent(relations RelationSummary, metadata Metadata) (RelationSummary, error) {
	parent := CrossRepositoryParent(metadata)
	if parent != "" {
		if relations.ParentID != "" && relations.ParentID != parent {
			return RelationSummary{}, fmt.Errorf("conflicting native and cross-repository parents %s and %s", relations.ParentID, parent)
		}
		relations.ParentID = parent
	}
	return relations, nil
}

func validateEligibleParent(parent Task, id string) error {
	if parent.IssueType != IssueTypeEpic {
		return fmt.Errorf("parent %q must be an epic", id)
	}
	if parent.Status != StatusOpen && parent.Status != StatusInProgress {
		return fmt.Errorf("parent epic %q is %s; expected open or in_progress", id, parent.Status)
	}
	return nil
}

func validateParentChange(ctx context.Context, reader RelationshipReader, itemID, parentID string) error {
	parent, err := reader.Get(ctx, parentID)
	if err != nil {
		return err
	}
	if err := validateEligibleParent(parent, parentID); err != nil {
		return err
	}
	visited := make(map[string]bool)
	for id := parentID; id != ""; {
		if id == itemID || visited[id] {
			return fmt.Errorf("setting parent %q would create a parent-child cycle", parentID)
		}
		visited[id] = true
		id = parent.Relations.ParentID
		if id != "" {
			parent, err = reader.Get(ctx, id)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// RelationshipReader resolves parents and discovers direct children across all
// registered sources. Child links stay authoritative in the child's repository;
// no reverse index or proxy task is maintained.
type RelationshipReader struct {
	Sources []RepositorySource
	Factory func(RepositorySource) (Getter, error)
}

// Get reads a relationship through its registered owner.
func (r RelationshipReader) Get(ctx context.Context, id string) (Task, error) {
	resolved, err := ResolveTaskSource(r.Sources, id)
	if err != nil {
		return Task{}, err
	}
	backend, err := r.Factory(resolved.Source)
	if err != nil {
		return Task{}, fmt.Errorf("prepare relationship %s in repository %s: %w", id, resolved.Source.Repository.ID, err)
	}
	return getUpdateReference(ctx, backend, resolved.Source, id, "relationship")
}

// Children returns all direct children, including closed children. A failed
// inventory or a missing declared child is an error, never an empty result.
func (r RelationshipReader) Children(ctx context.Context, parent Task) ([]Task, error) {
	children := make(map[string]Task)
	for _, source := range r.Sources {
		backend, err := r.Factory(source)
		if err != nil {
			return nil, fmt.Errorf("direct children incomplete for %s in repository %s: %w", parent.ID, source.Repository.ID, err)
		}
		listed, err := listChildren(ctx, backend, parent.ID)
		if err != nil {
			return nil, fmt.Errorf("direct children incomplete for %s in repository %s: %w", parent.ID, source.Repository.ID, err)
		}
		for _, child := range listed {
			owner, err := ResolveTaskSource(r.Sources, child.ID)
			if err != nil {
				return nil, err
			}
			if owner.Source.Repository.ID != source.Repository.ID {
				return nil, fmt.Errorf("child %s has inconsistent repository ownership", child.ID)
			}
			children[child.ID] = child.Clone()
		}
	}
	for _, id := range parent.Relations.ChildIDs {
		if _, ok := children[id]; ok {
			continue
		}
		child, err := r.Get(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("direct children incomplete for %s: %w", parent.ID, err)
		}
		if child.Relations.ParentID != parent.ID {
			return nil, fmt.Errorf("direct children incomplete for %s: child %s has a different parent", parent.ID, id)
		}
		children[id] = child
	}
	if parent.Relations.ChildCount > len(children) {
		return nil, fmt.Errorf("direct children incomplete for %s: source reports %d but only %d could be inspected", parent.ID, parent.Relations.ChildCount, len(children))
	}
	result := make([]Task, 0, len(children))
	for _, child := range children {
		result = append(result, child)
	}
	slices.SortFunc(result, func(a, b Task) int { return strings.Compare(a.ID, b.ID) })
	return result, nil
}

func listChildren(ctx context.Context, backend Getter, parentID string) ([]Task, error) {
	if lister, ok := backend.(FilteredLister); ok {
		return lister.ListFiltered(ctx, ListFilter{ParentID: parentID})
	}
	lister, ok := backend.(Lister)
	if !ok {
		return nil, fmt.Errorf("task source does not support direct-child inventory")
	}
	listed, err := lister.List(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(listed, func(item Task) bool { return item.Relations.ParentID != parentID }), nil
}
