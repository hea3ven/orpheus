package workflow

import (
	"context"
	"errors"
	"fmt"
	"github.com/hea3ven/orpheus/internal/workflow/delivery"
	"github.com/hea3ven/orpheus/internal/workflow/finalization"
	"log/slog"
	"os"
	"strings"

	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"

	"path/filepath"

	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

const finalizationLockOperation = "task finalization"

type DeliveryBackendFactory func(task.RepositorySource) (delivery.Backend, error)

// DeliveryConfig supplies invocation resolution and phase dependencies.
type DeliveryConfig struct {
	Paths            state.Paths
	Sources          []task.RepositorySource
	BackendFactory   DeliveryBackendFactory
	Store            phaseexec.Store
	DirectGit        delivery.DirectGit
	ConflictResolver phaseexec.ConflictResolver
	Git              delivery.Git
	PRProvider       pullrequest.Provider
	Logger           *slog.Logger
}

type FinalizeOptions struct {
	TaskID string
	CWD    string
}

type FinalizationResult struct {
	Repository      task.Repository
	Task            task.Task
	Finalization    taskstate.Finalization
	Branch          string
	InputCommit     string
	PRURL           string
	PRRecovered     bool
	WorktreeCleanup *finalization.WorktreeCleanupResult
}

func (s DeliveryConfig) resolvePublicationTask(ctx context.Context, opts FinalizeOptions, store phaseexec.Store) (task.RepositorySource, task.Task, delivery.Backend, error) {
	if strings.TrimSpace(opts.TaskID) != "" {
		resolved, err := task.ResolveTaskSource(s.Sources, opts.TaskID)
		if err != nil {
			return task.RepositorySource{}, task.Task{}, nil, err
		}
		backend, err := s.BackendFactory(resolved.Source)
		if err != nil {
			return task.RepositorySource{}, task.Task{}, nil, err
		}
		item, err := backend.Get(ctx, resolved.TaskID)
		if err == nil && item.ID != resolved.TaskID {
			return task.RepositorySource{}, task.Task{}, nil, errors.New("task source returned a different task identity")
		}
		return resolved.Source, item, backend, err
	}
	cwd, err := currentDirectory(opts.CWD)
	if err != nil {
		return task.RepositorySource{}, task.Task{}, nil, err
	}
	var sources []task.RepositorySource
	var items []task.Task
	var backends []delivery.Backend
	for _, source := range s.Sources {
		backend, err := s.BackendFactory(source)
		if err != nil {
			return task.RepositorySource{}, task.Task{}, nil, err
		}
		candidates, err := backend.List(ctx)
		if err != nil {
			return task.RepositorySource{}, task.Task{}, nil, err
		}
		for _, item := range candidates {
			snapshot, err := store.Load(source.Repository.ID, item.ID)
			if err != nil {
				return task.RepositorySource{}, task.Task{}, nil, err
			}
			inside, err := publicationDirectoryContains(snapshot, cwd)
			if err != nil {
				return task.RepositorySource{}, task.Task{}, nil, err
			}

			if inside {
				sources = append(sources, source)
				items = append(items, item)
				backends = append(backends, backend)
			}
		}
	}
	if len(items) != 1 {
		return task.RepositorySource{}, task.Task{}, nil, fmt.Errorf("cannot infer one workflow-owned publication from this directory; specify a task ID (%d candidates)", len(items))
	}
	return sources[0], items[0], backends[0], nil
}

func publicationDirectoryContains(snapshot work.State, cwd string) (bool, error) {
	if len(snapshot.Workflows) == 0 {
		return false, nil
	}
	prepared := snapshot.Workflows[len(snapshot.Workflows)-1].Context()
	if prepared == nil {
		return false, nil
	}
	relative, err := filepath.Rel(prepared.Directory, cwd)
	if err != nil {
		return false, err
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) && !filepath.IsAbs(relative)), nil
}

func currentDirectory(cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory: %w", err)
		}
	}
	return cleanAbsPath("current directory", cwd)
}
