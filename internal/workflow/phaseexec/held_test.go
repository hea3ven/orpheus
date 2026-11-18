package phaseexec_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerCallerOwnedScopeRefusesReleaseBeforeSavingIntent(t *testing.T) {
	runner, current := runnerFixture(t)
	ctx := context.Background()
	effects := 0
	err := state.WithGlobalMutationLock(runner.Paths, "coordinated batch", func() error {
		saved, err := runner.ExecuteHeld(ctx, current, phaseexec.Operation{Name: "attached work", Prepare: func(candidate *work.State) (phaseexec.Prepared, error) {
			execution(candidate).SupervisorPID = 33
			return phaseexec.Prepared{Changed: true, Scope: phaseexec.ReleaseForEffect, Run: func(context.Context) phaseexec.Result {
				effects++
				return phaseexec.Result{}
			}}, nil
		}})
		require.ErrorContains(t, err, "caller-owned mutation scope")
		assert.Equal(t, current, saved)
		assert.Zero(t, effects)
		saved, err = runner.ExecuteHeld(ctx, current, phaseexec.Operation{Name: "coordinated effect", Prepare: func(candidate *work.State) (phaseexec.Prepared, error) {
			execution(candidate).SupervisorPID = 33
			return phaseexec.Prepared{Changed: true, Reload: true, Run: func(ctx context.Context) phaseexec.Result {
				path, err := runner.Paths.DataPath(filepath.Join("locks", "mutation.lock"))
				require.NoError(t, err)
				_, err = os.Stat(path)
				require.NoError(t, err, "the caller's scope must remain held during the effect")
				effects++
				_, err = runner.CommitHeld(ctx, func(checkpoint *work.State) (bool, error) {
					execution(checkpoint).ChildPID = 44
					return true, nil
				})
				require.NoError(t, err)
				return phaseexec.Result{Update: func(result *work.State) (bool, error) {
					assert.Equal(t, 44, execution(result).ChildPID, "the ordinary result must see the independent checkpoint")
					execution(result).ObservedModel = "observed-model"
					return true, nil
				}}
			}}, nil
		}})
		require.NoError(t, err)
		assert.Equal(t, work.StateVersion(4), saved.Version)
		assert.Equal(t, 33, execution(&saved).SupervisorPID)
		assert.Equal(t, 44, execution(&saved).ChildPID)
		assert.Equal(t, "observed-model", execution(&saved).ObservedModel)
		assert.Equal(t, 1, effects)
		saved, err = runner.ExecuteHeld(ctx, saved, phaseexec.Operation{Name: "checkpoint-only result", Prepare: func(*work.State) (phaseexec.Prepared, error) {
			return phaseexec.Prepared{Reload: true, Run: func(ctx context.Context) phaseexec.Result {
				_, err := runner.CommitHeld(ctx, func(checkpoint *work.State) (bool, error) {
					execution(checkpoint).Session = &work.Session{ID: "native-session"}
					return true, nil
				})
				return phaseexec.Result{Err: err}
			}}, nil
		}})
		require.NoError(t, err)
		assert.Equal(t, work.StateVersion(5), saved.Version)
		require.NotNil(t, execution(&saved).Session, "nil ordinary updates must still return the checkpointed state")
		assert.Equal(t, "native-session", execution(&saved).Session.ID)
		return nil
	})
	require.NoError(t, err)
}
