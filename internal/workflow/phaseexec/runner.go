package phaseexec

import (
	"context"
	"errors"
	"log/slog"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
)

type Store interface {
	Load(repositoryID, taskID string) (work.State, error)
	Save(work.State, work.StateVersion) (work.StateVersion, error)
}

// Runner is bound to a single task. Independent recording uses Commit while an
// attached effect runs without the progression lock.
type Runner struct {
	Store        Store
	Paths        state.Paths
	RepositoryID string
	TaskID       string
	Logger       *slog.Logger
}

func (r Runner) Load(ctx context.Context) (work.State, error) {
	if err := ctx.Err(); err != nil {
		return work.State{}, err
	}
	return r.Store.Load(r.RepositoryID, r.TaskID)
}

func (r Runner) lock(ctx context.Context, name string, f func() error) error {
	return state.WithGlobalMutationLockLogger(ctx, r.Paths, name, r.Logger, f)
}

// Execute saves required intent before the effect and returns only committed
// state. ReleaseForEffect also opts into reloading independent child/checkpoint
// writes before applying the ordinary result, even when its Update is nil.
func (r Runner) Execute(ctx context.Context, current work.State, op Operation) (work.State, error) {
	return r.execute(ctx, current, op, r.lock, true)
}

// ExecuteHeld preserves an existing coordinated mutation scope, such as batch
// sync. The caller must already hold the global mutation lock. Operations in
// that scope cannot release it to permit independent writers.
func (r Runner) ExecuteHeld(ctx context.Context, current work.State, op Operation) (work.State, error) {
	return r.execute(ctx, current, op, func(_ context.Context, _ string, f func() error) error { return f() }, false)
}

func (r Runner) execute(ctx context.Context, current work.State, op Operation, lock func(context.Context, string, func() error) error, canRelease bool) (work.State, error) {
	committed := current
	var prepared Prepared
	var observed Result
	err := lock(ctx, op.Name, func() error {
		candidate, err := copyState(committed)
		if err != nil {
			return err
		}
		prepared, err = op.Prepare(&candidate)
		if err != nil {
			return err
		}
		if prepared.Scope == ReleaseForEffect && !canRelease {
			return errors.New("operation cannot release a caller-owned mutation scope")
		}
		if prepared.Changed {
			saved, err := r.save(candidate)
			if err != nil {
				return err
			}
			committed = saved
		}
		if prepared.Scope == ReleaseForEffect {
			return nil
		}
		if prepared.Run != nil {
			observed = prepared.Run(ctx)
		}
		if prepared.Reload {
			latest, err := r.Store.Load(r.RepositoryID, r.TaskID)
			if err != nil {
				return errors.Join(observed.Err, err)
			}
			committed = latest
		}
		return errors.Join(observed.Err, r.record(&committed, observed))
	})
	if err != nil {
		return committed, err
	}
	if prepared.Scope != ReleaseForEffect {
		return committed, observed.Err
	}
	if prepared.Run != nil {
		observed = prepared.Run(ctx)
	}
	// Observed exits and recovery facts survive cancellation of their effect.
	recordErr := lock(context.WithoutCancel(ctx), op.Name, func() error {
		latest, err := r.Store.Load(r.RepositoryID, r.TaskID)
		if err != nil {
			return err
		}
		committed = latest
		return r.record(&committed, observed)
	})
	return committed, errors.Join(observed.Err, recordErr)
}

func (r Runner) Commit(ctx context.Context, change Change) (work.State, error) {
	var committed work.State
	err := r.lock(ctx, "record phase result", func() error {
		var err error
		committed, err = r.CommitHeld(ctx, change)
		return err
	})
	return committed, err
}

// CommitHeld records a checkpoint in an existing coordinated mutation scope.
// Like Commit, it loads current state so it cannot erase earlier observations.
func (r Runner) CommitHeld(ctx context.Context, change Change) (work.State, error) {
	committed, err := r.Load(ctx)
	if err != nil {
		return committed, err
	}
	err = r.record(&committed, Result{Update: change})
	return committed, err
}

func (r Runner) record(committed *work.State, result Result) error {
	if result.Update == nil {
		return nil
	}
	candidate, err := copyState(*committed)
	if err != nil {
		return err
	}
	changed, err := result.Update(&candidate)
	if err != nil {
		return err
	}
	if changed {
		saved, err := r.save(candidate)
		if err != nil {
			return err
		}
		*committed = saved
	}
	return nil
}

func (r Runner) save(candidate work.State) (work.State, error) {
	version, err := r.Store.Save(candidate, candidate.Version)
	if err != nil {
		return work.State{}, err
	}
	candidate.Version = version
	return candidate, nil
}

func copyState(current work.State) (work.State, error) {
	data, err := workstate.Encode(current)
	if err != nil {
		return work.State{}, err
	}
	return workstate.Decode(data)
}

// AgentResults retains the deliberately independent write orderings. A failed
// first write never suppresses the second attempt or its error.
type AgentResults struct {
	Commit func(context.Context, Change) (work.State, error)
}

// The errors are returned separately in write order so phase owners can retain
// their purpose-specific diagnostics without merging the persistence attempts.
func (r AgentResults) UsageThenProcess(ctx context.Context, usage, process Change) (work.State, error, error) {
	ctx = context.WithoutCancel(ctx)
	_, first := r.Commit(ctx, usage)
	current, second := r.Commit(ctx, process)
	return current, first, second
}
func (r AgentResults) ProcessThenUsage(ctx context.Context, process, usage Change) (work.State, error, error) {
	ctx = context.WithoutCancel(ctx)
	_, first := r.Commit(ctx, process)
	current, second := r.Commit(ctx, usage)
	return current, first, second
}
