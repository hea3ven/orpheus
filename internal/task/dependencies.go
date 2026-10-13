package task

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// MetadataCrossRepositoryBlocks stores outgoing cross-repository references in
// the consuming task source. Native edges remain repository-local.
const MetadataCrossRepositoryBlocks = "orpheus.cross_repository_blocks"

// DependencyReader resolves every reference through its registered owner.
// It never falls back to another source when ownership or a read is uncertain.
type DependencyReader struct {
	Sources []RepositorySource
	Factory func(RepositorySource) (Getter, error)
}

// Get reads a prerequisite with its own relationships for graph validation.
func (r DependencyReader) Get(ctx context.Context, id string) (Task, error) {
	resolved, err := ResolveTaskSource(r.Sources, id)
	if err != nil {
		return Task{}, err
	}
	backend, err := r.Factory(resolved.Source)
	if err != nil {
		return Task{}, fmt.Errorf("prepare dependency %s in repository %s: %w", id, resolved.Source.Repository.ID, err)
	}
	return getUpdateReference(ctx, backend, resolved.Source, id, "blocking dependency")
}

// ReadDependency may reuse source-supplied rows only for local references.
func (r DependencyReader) ReadDependency(ctx context.Context, item Task, id string) (Task, error) {
	owner, err := ResolveTaskSource(r.Sources, item.ID)
	if err != nil {
		return Task{}, err
	}
	reference, err := ResolveTaskSource(r.Sources, id)
	if err != nil {
		return Task{}, err
	}
	if owner.Source.Repository.ID == reference.Source.Repository.ID {
		for _, related := range item.RelatedItems {
			if related.ID == id {
				return related.Clone(), nil
			}
		}
	}
	return r.Get(ctx, id)
}

func crossRepositoryIDs(sources []RepositorySource, source RepositorySource, ids []string) ([]string, error) {
	var result []string
	for _, id := range ids {
		resolved, err := ResolveTaskSource(sources, id)
		if err != nil {
			return nil, err
		}
		if resolved.Source.Repository.ID != source.Repository.ID {
			result = append(result, id)
		}
	}
	return result, nil
}

// CrossRepositoryBlocks decodes source-owned relationship metadata. Malformed
// metadata is a read failure, never an empty dependency set.
func CrossRepositoryBlocks(metadata Metadata) ([]string, error) {
	raw, ok := metadata[MetadataCrossRepositoryBlocks]
	if !ok {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("decode cross-repository dependencies: %w", err)
	}
	return normalizeIDs(ids), nil
}

// EditCrossRepositoryBlocks preserves unrelated metadata and makes edits
// idempotent. Callers persist the returned metadata through their task source.
func EditCrossRepositoryBlocks(metadata Metadata, add, remove []string) (Metadata, error) {
	ids, err := CrossRepositoryBlocks(metadata)
	if err != nil {
		return nil, err
	}
	ids = normalizeIDs(append(ids, add...))
	ids = slices.DeleteFunc(ids, func(id string) bool { return slices.Contains(remove, id) })
	slices.Sort(ids)
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	result := make(Metadata, len(metadata)+1)
	for key, value := range metadata {
		result[key] = value
	}
	result[MetadataCrossRepositoryBlocks] = string(raw)
	return result, nil
}

// LocalBlockingIDs excludes explicitly cross-repository references from native
// source operations, including removals whose owner is no longer registered.
func LocalBlockingIDs(ids, remote []string) []string {
	var local []string
	for _, id := range ids {
		if !slices.Contains(remote, id) {
			local = append(local, id)
		}
	}
	return local
}
