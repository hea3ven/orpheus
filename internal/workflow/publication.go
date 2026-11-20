package workflow

import (
	"log/slog"

	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/workflow/delivery"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

type DeliveryBackendFactory func(task.RepositorySource) (delivery.Backend, error)

// DeliveryService delivers authorized publications without starting implementation or review.
type DeliveryService struct {
	Paths            state.Paths
	BackendFactory   DeliveryBackendFactory
	Store            phaseexec.Store
	DirectGit        delivery.DirectGit
	ConflictResolver phaseexec.ConflictResolver
	Git              delivery.Git
	PRProvider       pullrequest.Provider
	Logger           *slog.Logger
	ProcessProbe     phaseexec.ProcessProbe
	Progress         func(Progress) error
}

// PublicationResult reports delivery, not task-source closure or worktree cleanup.
type PublicationResult struct {
	TaskID      string
	Branch      string
	InputCommit string
	PRURL       string
}
