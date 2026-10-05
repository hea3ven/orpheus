package gig

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/NeerajG03/gig"
	"github.com/hea3ven/orpheus/internal/task"
)

func (b Backend) MarkInProgress(ctx context.Context, id, branch, worktree string) error {
	branch, worktree = strings.TrimSpace(branch), strings.TrimSpace(worktree)
	if branch == "" || worktree == "" {
		return errors.New("branch and worktree are required")
	}
	return b.mutate(ctx, id, func(store *sdk.Store, stored *storedTask) error {
		item := &stored.item
		metadata := item.OrpheusMetadata()
		if strings.TrimSpace(metadata.PRURL) != "" {
			return conflict(id, "PR URL is already set")
		}
		switch item.Status {
		case task.StatusInProgress:
			if !metadata.HasBranch || !metadata.HasWorktree || strings.TrimSpace(metadata.Branch) != branch || strings.TrimSpace(metadata.Worktree) != worktree {
				return conflict(id, "in-progress task metadata does not match deterministic branch/worktree")
			}
			return nil
		case task.StatusOpen:
			item.Metadata[task.MetadataBranch], item.Metadata[task.MetadataWorktree] = branch, worktree
			return start(ctx, store, stored)
		default:
			return conflict(id, "status is not eligible for dispatch")
		}
	})
}

func (b Backend) StartEpic(ctx context.Context, id string) error {
	return b.mutate(ctx, id, func(store *sdk.Store, stored *storedTask) error {
		item := &stored.item
		if item.IssueType != task.IssueTypeEpic {
			return conflict(id, "item is not an epic")
		}
		switch item.Status {
		case task.StatusInProgress:
			return nil
		case task.StatusOpen:
			return start(ctx, store, stored)
		default:
			return conflict(id, "status is not eligible to start")
		}
	})
}

// Persist execution pointers before the status transition. A failed transition
// leaves an open task that a matching dispatch can safely retry.
func start(ctx context.Context, store *sdk.Store, stored *storedTask) error {
	if stored.item.StartedAt == nil {
		now := time.Now().UTC()
		stored.item.StartedAt = &now
	}
	if err := saveMetadata(ctx, store, *stored); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.UpdateStatus(stored.item.ID, sdk.StatusInProgress, "orpheus")
}

func (b Backend) UpdateGitFacts(ctx context.Context, id, branch, worktree string) error {
	branch, worktree = strings.TrimSpace(branch), strings.TrimSpace(worktree)
	if branch == "" || worktree == "" {
		return errors.New("branch and worktree are required")
	}
	return b.mutate(ctx, id, func(store *sdk.Store, stored *storedTask) error {
		item := &stored.item
		if item.Status != task.StatusInProgress {
			return conflict(id, "task is not in progress")
		}
		if strings.TrimSpace(item.OrpheusMetadata().PRURL) != "" {
			return conflict(id, "PR URL is already set")
		}
		item.Metadata[task.MetadataBranch], item.Metadata[task.MetadataWorktree] = branch, worktree
		return saveMetadata(ctx, store, *stored)
	})
}

func (b Backend) SetPRURL(ctx context.Context, id, prURL string) error {
	prURL = strings.TrimSpace(prURL)
	if prURL == "" {
		return errors.New("PR URL is required")
	}
	return b.mutate(ctx, id, func(store *sdk.Store, stored *storedTask) error {
		stored.item.Metadata[task.MetadataPRURL] = prURL
		return saveMetadata(ctx, store, *stored)
	})
}

func (b Backend) Close(ctx context.Context, id string) error {
	return b.mutate(ctx, id, func(store *sdk.Store, stored *storedTask) error {
		item := &stored.item
		if item.Status == task.StatusClosed {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := store.CloseTask(item.ID, "", "orpheus"); err != nil {
			return fmt.Errorf("close gig task %s failed; closure may already be committed, inspect task and dependents before retrying: %w", item.ID, err)
		}
		return nil
	})
}

var (
	_ task.ReadBackend      = Backend{}
	_ task.FilteredLister   = Backend{}
	_ task.CreateMutator    = Backend{}
	_ task.UpdateMutator    = Backend{}
	_ task.DispatchBackend  = Backend{}
	_ task.EpicStartMutator = Backend{}
	_ task.GitFactsMutator  = Backend{}
	_ task.SyncBackend      = Backend{}
)
