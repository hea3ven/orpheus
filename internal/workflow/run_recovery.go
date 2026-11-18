package workflow

import (
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
)

func applyExecutionRecovery(route *TaskRunRoute, snapshot taskstate.TaskState, probe phaseexec.ProcessProbe) {
	if route.Action == TaskRunActionCompleted || route.Action == TaskRunActionNeedsAttention {
		return
	}
	var execution taskstate.AgentExecution
	purpose := "implementation"
	if run, ok := taskstate.ActiveRun(snapshot); ok {
		execution = run.Execution
		route.Action, route.Attempt = TaskRunActionImplementationActive, run.Attempt
	} else if reviewer, ok := review.ActiveExecution(snapshot); ok {
		purpose, execution = "review", reviewer.Execution
		route.Action, route.Attempt = TaskRunActionReviewActive, reviewer.Attempt
	} else {
		return
	}
	inspection := phaseexec.AttachedExecutionInspection{Condition: phaseexec.AttachedExecutionUnverifiable, Reason: "process_observation_unavailable"}
	if probe != nil {
		inspection = phaseexec.InspectAttachedExecution(execution, probe)
	}
	route.Recovery = &inspection
	route.Advice = inspection.Advice(snapshot.TaskID, purpose, route.Attempt)
}
