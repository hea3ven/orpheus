package phaseexec

import (
	"fmt"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// ProcessProbe observes local PID liveness without owning task-state mutation.
type ProcessProbe func(pid int) (agentexec.ProcessLiveness, error)

// AttachedExecutionCondition is the safe recovery classification of a running
// local direct-child execution. It is shared by implementation and review
// workflows so the process-liveness policy cannot diverge.
type AttachedExecutionCondition string

const (
	AttachedExecutionNotRunning       AttachedExecutionCondition = "not_running"
	AttachedExecutionRecoverable      AttachedExecutionCondition = "recoverable"
	AttachedExecutionAlreadyRecovered AttachedExecutionCondition = "already_recovered"
	AttachedExecutionLive             AttachedExecutionCondition = "live"
	AttachedExecutionUnverifiable     AttachedExecutionCondition = "unverifiable"
)

// AttachedExecutionInspection contains a condition and operator-facing reason.
type AttachedExecutionInspection struct {
	Condition AttachedExecutionCondition
	Reason    string
}

// InspectAttachedExecution applies the shared stale-process policy without
// mutation. Legacy executions and probe failures remain unverifiable.
func InspectAttachedExecution(execution taskstate.AgentExecution, probe ProcessProbe) AttachedExecutionInspection {
	if execution.SupervisorPID <= 0 {
		return AttachedExecutionInspection{Condition: AttachedExecutionUnverifiable, Reason: "missing_supervisor_pid_legacy_run"}
	}
	if probe == nil {
		probe = agentexec.ProbePID
	}
	supervisor, err := probe(execution.SupervisorPID)
	if err != nil || supervisor == agentexec.ProcessUnknown {
		return AttachedExecutionInspection{Condition: AttachedExecutionUnverifiable, Reason: "supervisor_pid_probe_failed"}
	}
	switch supervisor {
	case agentexec.ProcessLive:
		return AttachedExecutionInspection{Condition: AttachedExecutionLive, Reason: "supervisor_pid_live"}
	case agentexec.ProcessAbsent:
	default:
		return AttachedExecutionInspection{Condition: AttachedExecutionUnverifiable, Reason: "supervisor_pid_probe_failed"}
	}
	if execution.ChildPID <= 0 {
		return AttachedExecutionInspection{Condition: AttachedExecutionRecoverable, Reason: "supervisor_absent_child_pid_not_recorded_assume_not_started"}
	}
	child, err := probe(execution.ChildPID)
	if err != nil || child == agentexec.ProcessUnknown {
		return AttachedExecutionInspection{Condition: AttachedExecutionUnverifiable, Reason: "child_pid_probe_failed"}
	}
	switch child {
	case agentexec.ProcessLive:
		return AttachedExecutionInspection{Condition: AttachedExecutionLive, Reason: "child_pid_live"}
	case agentexec.ProcessAbsent:
		return AttachedExecutionInspection{Condition: AttachedExecutionRecoverable, Reason: "supervisor_and_child_pids_absent"}
	default:
		return AttachedExecutionInspection{Condition: AttachedExecutionUnverifiable, Reason: "child_pid_probe_failed"}
	}
}

// Advice explains the observation and a safe next action without inferring exit
// success from liveness or a completion handoff.
func (i AttachedExecutionInspection) Advice(taskID, purpose string, attempt int) string {
	subject := fmt.Sprintf("Task %s: %s attempt %d", taskID, purpose, attempt)
	inspectionCommand := "orpheus task show workflow " + taskID
	if purpose == "review" {
		inspectionCommand = "orpheus task show review " + taskID
	}
	switch i.Condition {
	case AttachedExecutionLive:
		return fmt.Sprintf("%s is still active (%s); wait for it to finish, then inspect `%s`.", subject, i.Reason, inspectionCommand)
	case AttachedExecutionUnverifiable:
		return fmt.Sprintf("%s is unverifiable (%s); cannot automatically recover it. Inspect `orpheus doctor` and `%s`, and verify that the supervisor and child have stopped before retrying `orpheus task run %s`. Do not start a replacement or capture a revision while execution is uncertain.", subject, i.Reason, inspectionCommand, taskID)
	case AttachedExecutionRecoverable, AttachedExecutionAlreadyRecovered:
		if purpose == "review" {
			return fmt.Sprintf("%s stopped or is recoverable (%s). The candidate may contain reviewer mutations; inspect the worktree with `cd \"$(orpheus task dir %s)\" && git status --short && git diff`, then inspect audit history with `orpheus task show review %s` before running `orpheus task run %s` to reconcile the interruption. Recovery does not imply a successful exit.", subject, i.Reason, taskID, taskID, taskID)
		}
		return fmt.Sprintf("%s stopped and is recoverable (%s); run `orpheus task run %s` to reconcile the interruption and continue unfinished work from the existing files. Recovery does not imply a successful exit.", subject, i.Reason, taskID)
	default:
		return ""
	}
}
