package task

import (
	"context"
	"fmt"
)

// A child waits for parent activation, not parent completion. Conversely, epic
// completion waits for child completion. Keeping those two milestones separate
// avoids mistaking normal membership for a cycle while detecting mixed waits.
type waitNode struct {
	id         string
	completion bool
}

type waitGraph struct {
	reader   RelationshipReader
	proposed Task
	items    map[string]Task
	states   map[waitNode]uint8
}

func validateWaitGraph(ctx context.Context, reader RelationshipReader, proposed Task) error {
	graph := waitGraph{reader: reader, proposed: proposed, items: map[string]Task{proposed.ID: proposed}, states: make(map[waitNode]uint8)}
	// Completion includes activation and all epic descendants. Reparenting an
	// existing epic can introduce circular waits through those descendants.
	return graph.visit(ctx, waitNode{id: proposed.ID, completion: true})
}

func (g *waitGraph) visit(ctx context.Context, node waitNode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch g.states[node] {
	case 1:
		return fmt.Errorf("relationship change would create a dependency cycle or circular parent/dependency wait")
	case 2:
		return nil
	}
	g.states[node] = 1
	next, err := g.targets(ctx, node)
	if err != nil {
		return err
	}
	for _, target := range next {
		if err := g.visit(ctx, target); err != nil {
			return err
		}
	}
	g.states[node] = 2
	return nil
}

func (g *waitGraph) targets(ctx context.Context, node waitNode) ([]waitNode, error) {
	item, ok := g.items[node.id]
	if !ok {
		var err error
		item, err = g.reader.Get(ctx, node.id)
		if err != nil {
			return nil, err
		}
		g.items[node.id] = item
	}
	if node.completion {
		return g.completionTargets(ctx, item)
	}
	var next []waitNode
	if item.Relations.ParentID != "" {
		next = append(next, waitNode{id: item.Relations.ParentID})
	}
	for _, id := range item.Relations.DependencyIDs {
		next = append(next, waitNode{id: id, completion: true})
	}
	return next, nil
}

func (g *waitGraph) completionTargets(ctx context.Context, item Task) ([]waitNode, error) {
	next := []waitNode{{id: item.ID}}
	if item.IssueType != IssueTypeEpic {
		return next, nil
	}
	direct, err := g.reader.Children(ctx, item)
	if err != nil {
		return nil, err
	}
	for _, child := range direct {
		// The proposed item replaces its persisted membership, including removals.
		if child.ID != g.proposed.ID {
			next = append(next, waitNode{id: child.ID, completion: true})
		}
	}
	if g.proposed.Relations.ParentID == item.ID {
		next = append(next, waitNode{id: g.proposed.ID, completion: true})
	}
	return next, nil
}
