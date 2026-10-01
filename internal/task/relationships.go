package task

import (
	"context"
	"fmt"
	"sort"
)

// ReadRelated reuses a supplied related row, or reads it when the source only
// supplied an identifier. Call Get directly when its own relationships are needed.
func ReadRelated(ctx context.Context, backend Getter, item Task, id string) (Task, error) {
	for _, related := range item.RelatedItems {
		if related.ID == id {
			return related.Clone(), nil
		}
	}
	return backend.Get(ctx, id)
}

// ReadChildren returns the direct children identified by a detail read, reusing
// supplied rows. Missing or unsupported children are errors, not empty results.
func ReadChildren(ctx context.Context, backend Getter, parent Task) ([]Task, error) {
	children := make([]Task, 0, len(parent.Relations.ChildIDs))
	for _, id := range parent.Relations.ChildIDs {
		child, err := ReadRelated(ctx, backend, parent, id)
		if err != nil {
			return nil, fmt.Errorf("inspect direct child %s of %s: %w", id, parent.ID, err)
		}
		children = append(children, child)
	}
	sort.Slice(children, func(i, j int) bool { return children[i].ID < children[j].ID })
	return children, nil
}
