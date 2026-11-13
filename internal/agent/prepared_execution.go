package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// PreparedExecution keeps a selected command and session unchanged between
// workflow reservation and launch. It owns no durable state.
type PreparedExecution struct {
	command     CommandSnapshot
	launch      *taskstate.AgentLaunch
	purpose     taskstate.AgentExecutionPurpose
	sessionName string
	environment []string
}

// ImplementationPreparation supplies operator selection and, for a repair only,
// the implementation history from which a compatible session may be selected.
type ImplementationPreparation struct {
	Profile          string
	SessionName      string
	Workdir          string
	RepairHistory    *taskstate.TaskState
	UsageEnvironment map[string]string
}

// PrepareImplementation selects an implementer and optionally a repair session.
// Ordinary implementation never resumes an earlier effort.
func PrepareImplementation(config Config, opts ImplementationPreparation) (PreparedExecution, error) {
	command, err := config.ResolveImplementerCommandWithValues(opts.Profile, InterpolationValues{SessionName: opts.SessionName})
	if err != nil {
		return PreparedExecution{}, fmt.Errorf("resolve agent profile: %w", err)
	}
	var launch *taskstate.AgentLaunch
	if opts.RepairHistory != nil {
		command, launch = PrepareFollowUpResume(FollowUpResumeOptions{
			Command: command, State: *opts.RepairHistory, ExecutionDir: opts.Workdir, Env: opts.UsageEnvironment,
		})
	}
	return PreparedExecution{command: command, launch: launch, purpose: taskstate.AgentExecutionPurposeImplementation, sessionName: opts.SessionName}, nil
}

// ReviewPreparation identifies a configured review-agent step, not its durable
// attempt address, which the workflow supplies after recording the step.
type ReviewPreparation struct {
	Profile     string
	SessionName string
	Attempt     int
	Step        string
}

// PrepareReview selects a reviewer without resuming an implementation session.
func PrepareReview(config Config, opts ReviewPreparation) (PreparedExecution, error) {
	command, err := config.ResolveReviewerCommandWithValues(opts.Profile, InterpolationValues{SessionName: opts.SessionName})
	if err != nil {
		return PreparedExecution{}, err
	}
	return PreparedExecution{
		command: command, purpose: taskstate.AgentExecutionPurposeReview, sessionName: opts.SessionName,
		environment: []string{"ORPHEUS_AGENT_PURPOSE=review", "ORPHEUS_REVIEW_ATTEMPT=" + strconv.Itoa(opts.Attempt), "ORPHEUS_REVIEW_STEP=" + opts.Step},
	}, nil
}

// PrepareConflictResolution selects the conflict-specific profile, falling back
// to the implementer default. Conflict resolution always starts a fresh session.
func PrepareConflictResolution(config Config, taskID string, files []string) (PreparedExecution, error) {
	sessionName := "sync-conflict"
	if id := strings.TrimSpace(taskID); id != "" {
		sessionName += "-" + id
	}
	command, err := config.ResolveSyncConflictResolverCommand(InterpolationValues{SessionName: sessionName})
	if err != nil {
		return PreparedExecution{}, fmt.Errorf("resolve conflict agent profile: %w", err)
	}
	return PreparedExecution{
		command: command, purpose: taskstate.AgentExecutionPurposeSyncConflictResolution, sessionName: sessionName,
		environment: []string{"ORPHEUS_AGENT_PURPOSE=conflict_resolution", "ORPHEUS_CONFLICT_FILES=" + strings.Join(files, "\n")},
	}, nil
}

// Execution returns the selected facts for the workflow to record before launch.
// The workflow assigns the attempt address, start time and supervisor identity.
func (p PreparedExecution) Execution() taskstate.AgentExecution {
	selection := p.command.AgentSelection()
	execution := taskstate.AgentExecution{
		Purpose: p.purpose, Status: taskstate.RunStatusRunning,
		Agent: p.command.AgentName, Profile: p.command.AgentName,
		Harness: selection.Harness, Model: selection.Model, Thinking: selection.Thinking,
		Command: p.command.Command, Args: append([]string{}, p.command.Args...),
		SessionName: p.sessionName, Launch: clonePreparedLaunch(p.launch),
	}
	if p.purpose == taskstate.AgentExecutionPurposeImplementation {
		execution.Interactive = p.command.Interactive
	}
	return execution
}

// Interactive reports whether a selected reviewer needs attached input/output.
func (p PreparedExecution) Interactive() bool { return p.command.Interactive }

// PreparedRunOptions binds a prepared selection to its recorded owner and the
// caller's process effects. Command, prompt and session selection stay private.
type PreparedRunOptions struct {
	Execution taskstate.AgentExecution
	Workdir   string
	Branch    string
	// Environment carries invocation-scoped overrides such as XDG roots.
	Environment      []string
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
	RecordChildPID   func(int) error
	UsageEnvironment map[string]string
	CaptureUsage     func(UsageCaptureOptions) taskstate.RecordRunUsageOptions
	Logger           *slog.Logger
	Attempt          int
}

// Run assembles and executes the frozen selection. It does not reload profiles,
// select another session, persist observations or decide workflow success.
func (p PreparedExecution) Run(ctx context.Context, launcher agentexec.Launcher, opts PreparedRunOptions) ExecutionResult {
	if p.purpose == "" {
		return ExecutionResult{Err: errors.New("prepared agent execution is required")}
	}
	owner := opts.Execution.Address
	environment := []string{
		"ORPHEUS_REPO_ID=" + owner.RepositoryID,
		"ORPHEUS_TASK_ID=" + owner.TaskID,
		"ORPHEUS_WORKTREE=" + opts.Workdir,
		"ORPHEUS_BRANCH=" + opts.Branch,
		"ORPHEUS_AGENT_PROMPT=" + p.command.Prompt,
	}
	environment = append(environment, p.environment...)
	environment = append(environment, OwnerEnvironment(owner)...)
	environment = append(environment, opts.Environment...)
	stdin := opts.Stdin
	if p.purpose == taskstate.AgentExecutionPurposeReview && !p.command.Interactive {
		stdin = nil
	}
	if p.purpose == taskstate.AgentExecutionPurposeSyncConflictResolution {
		stdin = strings.NewReader("")
	}
	return Execute(ctx, launcher, ExecutionOptions{
		Command: p.command.ExecCommand(),
		Launch: agentexec.LaunchOptions{
			Dir: opts.Workdir, Env: environment, Stdin: stdin, Stdout: opts.Stdout, Stderr: opts.Stderr,
			OnStart: opts.RecordChildPID,
		},
		Usage: UsageCaptureOptions{
			SessionName: p.sessionName, StartedAt: opts.Execution.StartedAt,
			Env: opts.UsageEnvironment, Launch: clonePreparedLaunch(p.launch),
			Logger: opts.Logger, RepoID: owner.RepositoryID, TaskID: owner.TaskID, Attempt: opts.Attempt,
		},
		CaptureUsage: opts.CaptureUsage,
	})
}

func clonePreparedLaunch(launch *taskstate.AgentLaunch) *taskstate.AgentLaunch {
	if launch == nil {
		return nil
	}
	cloned := *launch
	cloned.SourceSession = cloneAgentSession(launch.SourceSession)
	cloned.UsageBaseline = cloneAgentUsage(launch.UsageBaseline)
	cloned.CostBaseline = cloneInt64(launch.CostBaseline)
	if launch.SourceExecution != nil {
		source := *launch.SourceExecution
		if source.Step != nil {
			step := *source.Step
			source.Step = &step
		}
		if source.BranchSync != nil {
			sync := *source.BranchSync
			source.BranchSync = &sync
		}
		cloned.SourceExecution = &source
	}
	return &cloned
}
