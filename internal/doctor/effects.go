package doctor

import (
	"os"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

// Effects supplies observations and external operations used by diagnostics.
// Nil fields retain the local filesystem, session and Git adapters.
type Effects struct {
	CaptureUsage   func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions
	SameSession    func(*taskstate.AgentSession, *taskstate.AgentSession) (bool, error)
	WorktreeExists func(string) bool
	SyncGit        workflow.SyncConflictRecoveryGit
}

func (e Effects) withDefaults() Effects {
	if e.CaptureUsage == nil {
		e.CaptureUsage = agent.CaptureUsage
	}
	if e.SameSession == nil {
		e.SameSession = agent.SameCanonicalSession
	}
	if e.WorktreeExists == nil {
		e.WorktreeExists = func(path string) bool {
			_, err := os.Lstat(path)
			return !os.IsNotExist(err)
		}
	}
	if e.SyncGit == nil {
		e.SyncGit = workflow.LocalSyncGit{}
	}
	return e
}

type usageReader struct {
	environment map[string]string
	capture     func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions
	sameSession func(*taskstate.AgentSession, *taskstate.AgentSession) (bool, error)
}
