package task

import (
	"context"
	"fmt"
	"slices"
)

// Membership is authoritative on children, so discovering remote children needs
// an inventory in every registered source, independent of the output filters.
func (a Aggregator) completeEpicMembership(ctx context.Context, snapshot *SnapshotResult, candidates []RepoTask, full bool) {
	seen := snapshotTaskIDs(snapshot)
	for _, candidate := range candidates {
		if candidate.Task.IssueType != IssueTypeEpic {
			continue
		}
		epic := candidate.Task.Clone()
		for _, source := range a.referenceSources {
			listed, err := a.epicSourceChildren(ctx, snapshot, candidate, source, full)
			if err != nil {
				a.markEpicIncomplete(snapshot, candidate.Repository.ID, epic.ID)
				a.appendRelationshipFailure(snapshot, candidate.Repository, epic.ID, "", source.Repository, fmt.Errorf("direct children incomplete for epic %s in repository %s: %w", epic.ID, source.Repository.ID, err))
				continue
			}
			for _, child := range listed {
				if child.Relations.ParentID != epic.ID || !IsTaskSourceItem(child) {
					continue
				}
				epic.Relations.ChildIDs = append(epic.Relations.ChildIDs, child.ID)
				if source.Repository.ID == candidate.Repository.ID {
					appendSnapshotContextTask(snapshot, seen, candidate.Repository, child)
				} else {
					appendRelationshipTask(snapshot, candidate.Repository.ID, child)
				}
			}
		}
		slices.Sort(epic.Relations.ChildIDs)
		epic.Relations.ChildIDs = slices.Compact(epic.Relations.ChildIDs)
		epic.Relations.ChildCount = max(epic.Relations.ChildCount, len(epic.Relations.ChildIDs))
		a.updateEpicMembership(snapshot, candidate.Repository.ID, epic)
	}
}

func (a Aggregator) epicSourceChildren(ctx context.Context, snapshot *SnapshotResult, candidate RepoTask, source RepositorySource, full bool) ([]Task, error) {
	// Complete detail already identifies native children. Context reads have
	// resolved these IDs without inventorying the owner's source again.
	if full || source.Repository.ID == candidate.Repository.ID && candidate.Task.Relations.Complete {
		for _, repository := range snapshot.Repositories {
			if repository.Repository.ID == source.Repository.ID {
				return repository.Tasks, nil
			}
		}
	}
	backend, err := a.factory(source)
	if err != nil {
		return nil, err
	}
	return listChildren(ctx, backend, candidate.Task.ID)
}

func (a Aggregator) updateEpicMembership(snapshot *SnapshotResult, repositoryID string, epic Task) {
	for i := range snapshot.Repositories {
		repository := &snapshot.Repositories[i]
		if repository.Repository.ID != repositoryID {
			continue
		}
		known := make(map[string]bool)
		for _, child := range append(append([]Task(nil), repository.Tasks...), repository.RelationshipTasks...) {
			if child.Relations.ParentID == epic.ID {
				known[child.ID] = true
			}
		}
		if len(known) < epic.Relations.ChildCount {
			a.markEpicIncomplete(snapshot, repositoryID, epic.ID)
		}
		for j := range repository.Tasks {
			if repository.Tasks[j].ID == epic.ID {
				repository.Tasks[j].Relations.ChildIDs = epic.Relations.ChildIDs
				repository.Tasks[j].Relations.ChildCount = epic.Relations.ChildCount
			}
		}
	}
}

func appendRelationshipTask(snapshot *SnapshotResult, repositoryID string, item Task) {
	for i := range snapshot.Repositories {
		repository := &snapshot.Repositories[i]
		if repository.Repository.ID != repositoryID {
			continue
		}
		if !slices.ContainsFunc(repository.RelationshipTasks, func(existing Task) bool { return existing.ID == item.ID }) {
			repository.RelationshipTasks = append(repository.RelationshipTasks, item.Clone())
		}
	}
}

func (Aggregator) markEpicIncomplete(snapshot *SnapshotResult, repositoryID, epicID string) {
	for i := range snapshot.Repositories {
		repository := &snapshot.Repositories[i]
		if repository.Repository.ID == repositoryID && !slices.Contains(repository.IncompleteEpicIDs, epicID) {
			repository.IncompleteEpicIDs = append(repository.IncompleteEpicIDs, epicID)
		}
	}
}
