package implementation

import (
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// Reservation describes the selected implementation execution.
type Reservation struct {
	Context     *work.WorkContext
	Agent       string
	Profile     string
	Selection   taskstate.AgentSelection
	Harness     string
	Model       string
	Thinking    string
	Interactive bool

	Command     string
	Args        []string
	SessionName string

	// WorkDirectory is immutable after the first run. Branch is an evolving Git
	// fact; Worktree mirrors it in this transient service request.
	WorkDirectory  string
	BaselineCommit string
	Branch         string
	Worktree       string

	ReviewFollowUp *taskstate.ReviewFollowUp
	Launch         *taskstate.AgentLaunch
	SupervisorPID  int
}
