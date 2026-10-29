package taskstate

import (
	"fmt"
	"time"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/work"
)

// Project derives service and operator views from one validated ownership tree.
// Attempt numbers are display labels; Address, not those labels, identifies work.
// The returned projections must never be serialized as another state authority.
func Project(snapshot work.State) (TaskState, error) {
	if err := snapshot.Validate(); err != nil {
		return TaskState{}, err
	}
	view := TaskState{Work: &snapshot, CurrentWorkflow: len(snapshot.Workflows) - 1, Version: snapshot.Version, RepoID: snapshot.RepositoryID, TaskID: snapshot.TaskID}
	for wi := range snapshot.Workflows {
		w := &snapshot.Workflows[wi]
		if wi == view.CurrentWorkflow && w.Context() != nil {
			prepared := w.Context()
			view.WorkDirectory = WorkDirectory{Path: prepared.Directory}
			view.GitFacts = GitFacts{Branch: prepared.TaskBranch, Worktree: prepared.Directory}
			view.BaselineCommit = prepared.Baseline.Commit
		}
		for pi, phase := range w.Phases {
			address := work.AttemptRef{RepositoryID: snapshot.RepositoryID, TaskID: snapshot.TaskID, Workflow: wi, Phase: pi}
			projectPhase(&view, w, phase, address)
		}
	}
	return view, nil
}

func projectPhase(view *TaskState, w *work.Workflow, phase work.Phase, address work.AttemptRef) {
	switch p := phase.(type) {
	case *work.PreparationPhase:
		if p.EstablishedAt != nil {
			view.Events = append(view.Events, Event{Address: address, Type: EventWorkPrepared, At: *p.EstablishedAt, Branch: p.Context.TaskBranch, Worktree: p.Context.Directory})
		}
	case *work.ImplementationPhase:
		run := projectImplementation(w, p, address)
		view.Runs = append(view.Runs, run)
		projectRunEvents(view, run)
	case *work.ReviewPhase:
		view.Reviews = append(view.Reviews, projectReview(w, p, address))
		projectReviewInterruption(view, view.Reviews[len(view.Reviews)-1])
	case *work.PublicationPhase:
		projectPublicationEvents(view, w, p, address)
		if address.Workflow == view.CurrentWorkflow {
			view.Finalization = projectPublication(w, p)
		}
	case *work.ExternalReviewPhase:
		for si := range p.BranchSyncs {
			address.BranchSync = &si
			projectBranchSync(view, w, &p.BranchSyncs[si], address)
		}
	case *work.FinalizationPhase:
		projectFinalization(view, w, p, address)
	}
}

func projectImplementation(w *work.Workflow, p *work.ImplementationPhase, address work.AttemptRef) RunAttempt {
	run := RunAttempt{Address: address, Attempt: phaseNumber(w, address.Phase, false), Execution: projectExecution(w, &p.Execution, AgentExecutionPurposeImplementation, address)}
	run.Status = run.Execution.Status
	if p.Completion != nil {
		c := p.Completion
		run.Completion = &Completion{Summary: c.Summary, Description: c.Description, DetailedDescription: c.DetailedDescription, TechnicalExplanation: c.TechnicalExplanation, CompletedAt: c.CompletedAt}
		if p.Output != nil {
			output, err := w.Output(work.OutputRef{Phase: address.Phase})
			if err == nil {
				run.Completion.Commit = output.Commit
			}
		}
	}
	if len(p.TargetFindings) > 0 {
		first := p.TargetFindings[0]
		followUp := &ReviewFollowUp{ReviewAttempt: phaseNumber(w, first.Phase, true)}
		for _, target := range p.TargetFindings {
			if target.Phase == first.Phase {
				followUp.FindingIndexes = append(followUp.FindingIndexes, flatFindingIndex(w, target))
			}
		}
		for _, advisory := range p.AdvisoryFindings {
			if advisory.Phase == first.Phase {
				followUp.AdvisoryFindingIndexes = append(followUp.AdvisoryFindingIndexes, flatFindingIndex(w, advisory))
			}
		}
		run.ReviewFollowUp = followUp
	}
	return run
}

func phaseNumber(w *work.Workflow, phase int, review bool) int {
	number := 0
	for _, p := range w.Phases[:phase+1] {
		if _, ok := p.(*work.ImplementationPhase); ok && !review {
			number++
		}
		if _, ok := p.(*work.ReviewPhase); ok && review {
			number++
		}
	}
	return number
}

func projectExecution(w *work.Workflow, e *work.AgentExecution, purpose AgentExecutionPurpose, address work.AttemptRef) AgentExecution {
	view := AgentExecution{Address: address, Process: e.Process, Purpose: purpose, Agent: e.Profile, Profile: e.Profile, Harness: e.Harness, Model: e.Model, Thinking: e.Thinking, Interactive: e.Interactive, SessionName: e.SessionName, Command: e.Command, Args: cloneStrings(e.Args), StartedAt: e.StartedAt, SupervisorPID: e.SupervisorPID, ChildPID: e.ChildPID, DurationMillis: e.Duration.Milliseconds()}
	if e.ObservedModel != "" {
		view.Model, view.ObservedModel = e.ObservedModel, e.ObservedModel
	}
	switch process := e.Process.(type) {
	case *work.RunningProcess:
		view.Status = RunStatusRunning
	case *work.ExitedProcess:
		view.Status = RunStatusFailed
		if *process.Code == 0 {
			view.Status = RunStatusSucceeded
		}
		view.FinishedAt = &process.At
	case *work.InterruptedProcess:
		view.Status, view.FinishedAt = RunStatusInterrupted, &process.At
		view.InterruptionReason, view.InterruptionTrigger = process.Reason, process.Trigger
	case *work.StartFailedProcess:
		view.Status, view.FinishedAt = RunStatusFailed, &process.At
	}
	if e.Session != nil {
		view.Session = &AgentSession{ID: e.Session.ID, LogPath: e.Session.LogPath}
	}
	view.Usage = projectUsage(e.Usage)
	if e.Cost != nil {
		view.UsageCost = &AgentUsageCost{Kind: e.Cost.Kind, Currency: e.Cost.Currency, AmountMicroUSD: e.Cost.AmountMicroUSD, Source: e.Cost.Source, Notes: e.Cost.Notes}
		if e.Cost.Pricing != nil {
			pricing := AgentUsagePricing(*e.Cost.Pricing)
			view.UsageCost.Pricing = &pricing
		}
	}
	view.UsageCapture = AgentUsageCapture{Status: UsageCaptureStatus(e.UsageCapture.Status), Reason: e.UsageCapture.Reason, CandidateCount: e.UsageCapture.CandidateCount, CapturedAt: e.UsageCapture.CapturedAt}
	switch launch := e.Launch.(type) {
	case *work.FreshExecution:
		view.Launch = &AgentLaunch{Mode: AgentLaunchFresh, FallbackReason: launch.FallbackReason}
	case *work.ResumedExecution:
		view.Launch = &AgentLaunch{Mode: AgentLaunchResumed, SourceRunAttempt: phaseNumber(w, launch.From.Phase, false), UsageBaseline: projectUsage(launch.UsageBaseline), CostBaseline: launch.CostBaselineMicroUSD}
		if source, err := w.Execution(launch.From); err == nil && source.Session != nil {
			view.Launch.SourceSession = &AgentSession{ID: source.Session.ID, LogPath: source.Session.LogPath}
		}
	}
	return view
}

func projectUsage(u *work.Usage) *AgentUsage {
	if u == nil {
		return nil
	}
	return &AgentUsage{InputTokens: int(u.InputTokens), CachedInputTokens: int(u.CachedInputTokens), OutputTokens: int(u.OutputTokens), ReasoningOutputTokens: int(u.ReasoningOutputTokens), TotalTokens: int(u.TotalTokens)}
}

func projectReview(w *work.Workflow, p *work.ReviewPhase, address work.AttemptRef) ReviewAttempt {
	contract := &w.ReviewContracts[p.Contract]
	view := ReviewAttempt{Address: address, Attempt: phaseNumber(w, address.Phase, true), Pipeline: contract.PipelineKey, StartedAt: p.StartedAt}
	if len(contract.Slots) > 0 {
		view.Step = contract.Slots[0].ConfigKey
	}
	for si, step := range p.Steps {
		ref := address
		ref.Step = &si
		view.Steps = append(view.Steps, projectReviewStep(w, contract, step, ref))
		view.Step = view.Steps[len(view.Steps)-1].Name
		for fi := range ownedStepFindings(step) {
			findingRef := work.FindingRef{Phase: address.Phase, Step: si, Finding: fi}
			finding, _ := w.Finding(findingRef)
			view.Findings = append(view.Findings, projectFinding(w, finding, findingRef, view.Step, ref))
		}
	}
	outcome, _ := w.ReviewOutcome(work.ReviewRef{Phase: address.Phase})
	view.Status = projectReviewStatus(outcome, p)
	view.AutonomousBudgetExhausted = len(p.RepairBudgetStops) > 0
	projectAutomatedDecisions(&view, p)
	if view.Status == ReviewStatusPassed || view.Status == ReviewStatusFailed || view.Status == ReviewStatusBlocked || view.Status == ReviewStatusAborted {
		view.FinishedAt = reviewObservedBoundary(p)
	}
	return view
}

func projectReviewStep(w *work.Workflow, contract *work.ReviewContract, step work.ReviewStep, address work.AttemptRef) ReviewStep {
	view := ReviewStep{Address: address}
	slot := 0
	switch step := step.(type) {
	case *work.CheckStep:
		slot, view.Kind = step.Slot, ReviewStepKindCheck
		if step.Result != nil {
			view.ExitCode = cloneIntPointer(step.Result.ExitCode)
		}
	case *work.AgentReviewStep:
		slot, view.Kind = step.Slot, ReviewStepKindAgentReview
		e := projectExecution(w, &step.Execution, AgentExecutionPurposeReview, address)
		view.Execution = &e
	case *work.ManualReviewStep:
		slot, view.Kind = step.Slot, ReviewStepKindManual
		if exit, ok := step.CommandResult.(*work.ExitedProcess); ok {
			view.ExitCode = cloneIntPointer(exit.Code)
		}
	}
	view.Name = contract.Slots[slot].ConfigKey
	return view
}

func projectReviewStatus(outcome work.ReviewOutcome, p *work.ReviewPhase) ReviewStatus {
	// Execution ownership is independent of an earlier failed slot. Never make
	// an in-flight child disappear behind an aggregate review result.
	if len(p.Steps) > 0 {
		switch step := p.Steps[len(p.Steps)-1].(type) {
		case *work.AgentReviewStep:
			if _, running := step.Execution.Process.(*work.RunningProcess); running {
				return ReviewStatusRunning
			}
		case *work.CheckStep:
			if step.Result == nil && step.StartFailure == nil {
				return ReviewStatusRunning
			}
		}
	}
	if p.Abort != nil {
		return ReviewStatusAborted
	}
	if p.ExecutionFailure != nil || p.AdmissionFailure != nil {
		return ReviewStatusFailed
	}
	if len(p.Steps) > 0 {
		if step, ok := p.Steps[len(p.Steps)-1].(*work.ManualReviewStep); ok && step.Decision == nil && len(step.ReadOnlyFailures) == 0 && !projectManualCommandFailed(step.CommandResult) {
			return ReviewStatusWaitingForManual
		}
	}
	switch outcome {
	case work.ReviewPassed, work.ReviewPassedWithWaivers:
		return ReviewStatusPassed
	case work.ReviewFailed:
		return ReviewStatusFailed
	case work.ReviewChangesRequired:
		return ReviewStatusBlocked
	}
	if len(p.Steps) == 0 {
		return ReviewStatusRunning
	}
	step, manual := p.Steps[len(p.Steps)-1].(*work.ManualReviewStep)
	if !manual {
		return ReviewStatusRunning
	}
	switch step.Decision.(type) {
	case *work.AbortedReview:
		return ReviewStatusAborted
	case nil:
		return ReviewStatusWaitingForManual
	}

	return ReviewStatusRunning
}

func ownedStepFindings(step work.ReviewStep) []work.Finding {
	switch step := step.(type) {
	case *work.CheckStep:
		return step.Findings
	case *work.AgentReviewStep:
		if step.Report != nil {
			return step.Report.Findings
		}
	case *work.ManualReviewStep:
		return step.Findings
	}
	return nil
}

func flatFindingIndex(w *work.Workflow, ref work.FindingRef) int {
	p := w.Phases[ref.Phase].(*work.ReviewPhase)
	index := ref.Finding
	for _, step := range p.Steps[:ref.Step] {
		index += len(ownedStepFindings(step))
	}
	return index
}

func projectFinding(w *work.Workflow, finding *work.Finding, ref work.FindingRef, step string, actor work.AttemptRef) ReviewFinding {
	view := ReviewFinding{Actor: &actor, Address: &ref, ReportedType: FindingType(finding.Classification), Type: FindingType(finding.Classification), Title: finding.Title, Description: finding.Description, SuggestedAction: finding.SuggestedAction, Step: step, Decision: finding.Disposition()}
	for _, disposition := range finding.Dispositions {
		if _, promoted := disposition.(*work.PromotedFinding); promoted {
			view.Type = FindingTypeBlocking
		}
	}
	switch decision := view.Decision.(type) {
	case *work.WaivedFinding:
		view.Waiver = decision.Reason
	case *work.DismissedFinding:
		view.Waiver = decision.Reason
	case *work.DowngradedFinding:
		view.Type, view.DowngradeReason = FindingTypeAdvisory, decision.Reason
	case *work.AddressedFinding:
		view.AddressedManually = decision.Reason
	}
	if finding.Proposal != nil {
		view.TaskProposal = ReviewTaskProposal{Title: finding.Proposal.Title, Description: finding.Proposal.Description, AcceptanceCriteria: finding.Proposal.AcceptanceCriteria}
	}
	if finding.CreatedTask != nil {
		view.CreatedTaskID, view.CreatedTaskAt = finding.CreatedTask.TaskID, &finding.CreatedTask.ObservedAt
	}
	for _, pi := range w.RepairPhases(ref) {
		view.TargetedByRunAttempt = phaseNumber(w, pi, false)
	}
	review := w.Phases[ref.Phase].(*work.ReviewPhase)
	owner := review.Steps[ref.Step]
	ownerSlot := stepSlot(owner)
	if reviewer, ok := owner.(*work.AgentReviewStep); ok {
		exit, exited := reviewer.Execution.Process.(*work.ExitedProcess)
		view.NonAuthoritative = !exited || exit == nil || exit.Code == nil || *exit.Code != 0 || len(reviewer.ReadOnlyFailures) > 0 || reviewer.ExecutionFailure != nil
	}
	for _, later := range review.Steps[ref.Step+1:] {
		if stepSlot(later) == ownerSlot {
			view.Superseded = true
			break
		}
	}
	return view
}

func projectPublication(w *work.Workflow, p *work.PublicationPhase) *Finalization {
	destination, _ := w.ReviewDestination(p.Plan.Review)
	view := &Finalization{IntegrationFlow: publication.IntegrationFlowPullRequest, DestinationBranch: destination.Branch}
	if _, direct := p.Plan.Target.(*work.DirectMergeTarget); direct {
		view.IntegrationFlow = publication.IntegrationFlowDirectMerge
	}
	// The model has no publication-start command journal. Consent is not an
	// observed Git/API effect; only independently observed delivery produces facts.
	switch delivery := p.Delivery.(type) {
	case *work.PullRequestDelivery:
		view.Commit, view.PushedAt = delivery.PublicCommit, &delivery.ObservedAt
	case *work.DirectDelivery:
		view.Commit, view.MergeCommit, view.PushedAt = delivery.PublicCommit, delivery.Destination.Commit, &delivery.ObservedAt
	}
	return view
}

func projectFinalization(view *TaskState, w *work.Workflow, p *work.FinalizationPhase, address work.AttemptRef) {
	if p.Closure != nil && address.Workflow == view.CurrentWorkflow {
		if view.Finalization == nil {
			view.Finalization = &Finalization{}
		}
		view.Finalization.ClosedAt = &p.Closure.At
	}
	if p.Closure != nil {
		event := Event{Address: address, Type: EventTaskClosed, At: p.Closure.At, CloseReason: p.Closure.Reason}
		if acceptance, ok := p.Acceptance.(*work.PRAcceptance); ok {
			external := w.Phases[acceptance.ExternalReview].(*work.ExternalReviewPhase)
			delivery := w.Phases[external.Publication.Phase].(*work.PublicationPhase).Delivery.(*work.PullRequestDelivery)
			event.PRURL = delivery.PullRequest.URL
			event.ObservedPRState = "merged"
		}
		view.Events = append(view.Events, event)
	}
	for _, observation := range p.Cleanup {
		if removed, ok := observation.(*work.WorktreeRemoved); ok {
			view.Events = append(view.Events, Event{Address: address, Type: EventWorktreeRemoved, At: removed.At, Worktree: removed.Path})
		}
	}
}

func projectBranchSync(view *TaskState, w *work.Workflow, sync *work.BranchSync, address work.AttemptRef) SyncConflictOperation {
	prepared := w.Context()
	external := w.Phases[address.Phase].(*work.ExternalReviewPhase)
	publicationPhase := w.Phases[external.Publication.Phase].(*work.PublicationPhase)
	destination, _ := w.ReviewDestination(publicationPhase.Plan.Review)
	operation := SyncConflictOperation{Address: address, ID: fmt.Sprintf("%d/%d/%d", address.Workflow, address.Phase, *address.BranchSync), Branch: prepared.TaskBranch, Worktree: prepared.Directory, DefaultBranch: destination.Branch, Checkpoint: SyncConflictCheckpoint(sync.Checkpoint), CreatedAt: sync.StartedAt, UpdatedAt: sync.StartedAt, Phase: SyncConflictPhasePrepared}
	view.Events = append(view.Events, Event{Address: address, Type: EventBranchSyncStarted, At: sync.StartedAt, Branch: prepared.TaskBranch, DefaultBranch: destination.Branch, Worktree: prepared.Directory, Commit: sync.Checkpoint.LocalHead, Message: fmt.Sprintf("local %s; remote %s; merge source %s", sync.Checkpoint.LocalHead, sync.Checkpoint.RemoteHead, sync.Checkpoint.MergeSource)})
	if sync.ConflictResolution != nil {
		resolution := sync.ConflictResolution
		operation.ConflictFiles = cloneStrings(resolution.Files)
		e := projectExecution(w, &resolution.Execution, AgentExecutionPurposeSyncConflictResolution, address)
		operation.Execution = &e
		operation.Phase = SyncConflictPhaseResolving
		if e.FinishedAt != nil {
			operation.UpdatedAt, operation.Phase = *e.FinishedAt, SyncConflictPhaseUnresolved
		}
		view.Events = append(view.Events, Event{Address: address, Type: EventSyncConflictStarted, At: e.StartedAt, Attempt: *address.BranchSync + 1, DefaultBranch: destination.Branch, PRURL: publicationPhase.Delivery.(*work.PullRequestDelivery).PullRequest.URL, Execution: &e, Branch: prepared.TaskBranch, Worktree: prepared.Directory, ConflictFiles: cloneStrings(resolution.Files)})
		if e.FinishedAt != nil {
			typeOf := EventSyncConflictFailed
			if e.Status == RunStatusSucceeded {
				typeOf = EventSyncConflictFinished
			}
			view.Events = append(view.Events, Event{Address: address, Type: typeOf, At: *e.FinishedAt, Attempt: *address.BranchSync + 1, DefaultBranch: destination.Branch, PRURL: publicationPhase.Delivery.(*work.PullRequestDelivery).PullRequest.URL, Execution: &e, Branch: prepared.TaskBranch, Worktree: prepared.Directory, ConflictFiles: cloneStrings(resolution.Files)})
		}
	}
	if sync.LocalCommit != nil {
		view.Events = append(view.Events, Event{Address: address, Type: EventBranchSyncCommitted, At: sync.LocalCommit.ObservedAt, Branch: prepared.TaskBranch, Worktree: prepared.Directory, Commit: sync.LocalCommit.Head})
		operation.LocalHead, operation.UpdatedAt, operation.Phase = sync.LocalCommit.Head, sync.LocalCommit.ObservedAt, SyncConflictPhaseLocalCompleted
	}
	if sync.Push != nil {
		view.Events = append(view.Events, Event{Address: address, Type: EventBranchSyncPushed, At: sync.Push.ObservedAt, Branch: prepared.TaskBranch, Worktree: prepared.Directory, Commit: sync.Push.Head})
		operation.ObservedRemoteHead, operation.UpdatedAt, operation.Phase = sync.Push.Head, sync.Push.ObservedAt, SyncConflictPhasePushed
	}
	if sync.Rollback != nil {
		rollback := sync.Rollback
		view.Events = append(view.Events, Event{Address: address, Type: EventBranchSyncRolledBack, At: rollback.ObservedAt, Branch: prepared.TaskBranch, DefaultBranch: destination.Branch, Worktree: prepared.Directory, Commit: rollback.LocalHead, Message: rollback.Reason})
	}
	if sync.Rollback == nil && sync.Push == nil && address.Workflow == view.CurrentWorkflow && address.Phase == len(w.Phases)-1 {
		view.ActiveSyncConflict = &operation
	}
	return operation
}

func workflowCompletionRuns(view TaskState) (CompletionRunHistory, error) {
	latest, ok := LatestRun(view)
	if !ok || latest.Completion == nil {
		return CompletionRunHistory{}, fmt.Errorf("current implementation completion is required")
	}
	w := &view.Work.Workflows[view.CurrentWorkflow]
	if _, err := w.Output(work.OutputRef{Phase: latest.Address.Phase}); err != nil {
		return CompletionRunHistory{}, fmt.Errorf("current implementation needs safely captured output: %w", err)
	}
	feature := w.FeatureCompletion()
	if feature == nil {
		return CompletionRunHistory{}, fmt.Errorf("owning workflow has no safely captured feature completion")
	}
	for _, run := range view.Runs {
		if run.Address.Workflow != view.CurrentWorkflow {
			continue
		}
		p := w.Phases[run.Address.Phase].(*work.ImplementationPhase)
		if p.Completion == feature {
			return CompletionRunHistory{Original: run, Latest: latest}, nil
		}
	}
	return CompletionRunHistory{}, fmt.Errorf("feature completion owner is missing from the workflow view")
}

func stepSlot(step work.ReviewStep) int {
	switch step := step.(type) {
	case *work.CheckStep:
		return step.Slot
	case *work.AgentReviewStep:
		return step.Slot
	case *work.ManualReviewStep:
		return step.Slot
	}
	return -1
}

func projectManualCommandFailed(result work.ProcessResult) bool {
	if result == nil {
		return false
	}
	exit, ok := result.(*work.ExitedProcess)
	return !ok || exit == nil || exit.Code == nil || *exit.Code != 0
}

func projectAutomatedDecisions(view *ReviewAttempt, phase *work.ReviewPhase) {
	found, kept := false, true
	var decisionAt time.Time
	for _, finding := range view.Findings {
		// Targeting later repair work is not a disposition of the original report.
		// Keep the operator's explicit automated decision visible in that history.
		finding.TargetedByRunAttempt = 0
		if finding.NonAuthoritative || finding.Superseded || finding.Address == nil || !IsOpenBlockingReviewFinding(finding) {
			continue
		}
		step := phase.Steps[finding.Address.Step]
		if _, manual := step.(*work.ManualReviewStep); manual {
			continue
		}
		found = true
		switch decision := finding.Decision.(type) {
		case *work.RepairRequired:
			if decision.At.IsZero() {
				kept = false
			}
			if decision.At.After(decisionAt) {
				decisionAt = decision.At
			}
		case *work.PromotedFinding:
			if decision.At.After(decisionAt) {
				decisionAt = decision.At
			}
		default:
			kept = false
		}
	}
	view.AutomatedBlockerDecisionKept = found && kept
	if !found || len(phase.Steps) == 0 || phase.AdmissionFailure != nil || phase.ExecutionFailure != nil || phase.Abort != nil || projectSafetyFailure(phase) {
		return
	}
	pauses := projectDecisionPauses(phase.Steps[len(phase.Steps)-1])
	if len(pauses) > 0 {
		last := pauses[len(pauses)-1]
		view.AutomatedBlockerDecisionInterrupted = last.Kind == work.DecisionInputUnavailable && last.At.After(decisionAt)
	}
	if !kept {
		view.Status = ReviewStatusWaitingForAutomatedDecision
	} else if view.Status == ReviewStatusFailed {
		view.Status = ReviewStatusBlocked
	}
}

func projectDecisionPauses(step work.ReviewStep) []work.AutomatedDecisionPause {
	switch step := step.(type) {
	case *work.CheckStep:
		return step.DecisionPauses
	case *work.AgentReviewStep:
		return step.DecisionPauses
	}
	return nil
}

func projectSafetyFailure(phase *work.ReviewPhase) bool {
	for index, step := range phase.Steps {
		superseded := false
		for _, later := range phase.Steps[index+1:] {
			if stepSlot(later) == stepSlot(step) {
				superseded = true
				break
			}
		}
		if superseded {
			continue
		}
		switch step := step.(type) {
		case *work.CheckStep:
			if len(step.ReadOnlyFailures) > 0 || step.StartFailure != nil || step.Result == nil || (*step.Result.ExitCode != 0 && len(step.Findings) == 0) {
				return true
			}
		case *work.AgentReviewStep:
			exit, ok := step.Execution.Process.(*work.ExitedProcess)
			if !ok || exit.Code == nil || *exit.Code != 0 || len(step.ReadOnlyFailures) > 0 || step.ExecutionFailure != nil {
				return true
			}
		case *work.ManualReviewStep:
			if projectManualCommandFailed(step.CommandResult) || len(step.ReadOnlyFailures) > 0 {
				return true
			}
		}
	}
	if len(phase.Steps) > 0 {
		_, manual := phase.Steps[len(phase.Steps)-1].(*work.ManualReviewStep)
		return manual
	}
	return false
}

func projectRunEvents(view *TaskState, run RunAttempt) {
	e := run.Execution
	view.Events = append(view.Events, Event{Address: run.Address, Type: EventRunStarted, Status: RunStatusRunning, At: e.StartedAt, Attempt: run.Attempt, Agent: e.Agent})
	if run.Completion != nil {
		view.Events = append(view.Events, Event{Address: run.Address, Type: EventCompletionRecorded, Status: RunStatusRunning, At: run.Completion.CompletedAt, Attempt: run.Attempt, Agent: e.Agent})
	}
	if e.FinishedAt == nil {
		return
	}
	event := Event{Address: run.Address, Type: EventRunFinished, At: *e.FinishedAt, Attempt: run.Attempt, Status: e.Status, Agent: e.Agent, Execution: &e}
	switch process := e.Process.(type) {
	case *work.InterruptedProcess:
		event.Type = EventRunInterrupted
		event.InterruptionReason = process.Reason
		event.InterruptionTrigger = process.Trigger
	case *work.StartFailedProcess:
		event.Type = EventRunStartFailed
		event.Error = process.Reason
	}
	view.Events = append(view.Events, event)
}
func projectReviewInterruption(view *TaskState, review ReviewAttempt) {
	for _, step := range review.Steps {
		if step.Execution == nil {
			continue
		}
		if interrupted, ok := step.Execution.Process.(*work.InterruptedProcess); ok {
			view.Events = append(view.Events, Event{Address: step.Address, Type: EventReviewInterrupted, At: interrupted.At, Attempt: review.Attempt, Status: RunStatusInterrupted, Agent: step.Execution.Agent, Execution: step.Execution, InterruptionReason: interrupted.Reason, InterruptionTrigger: interrupted.Trigger})
		}
	}
}
func reviewObservedBoundary(phase *work.ReviewPhase) *time.Time {
	var latest time.Time
	observe := func(at time.Time) {
		if at.After(latest) {
			latest = at
		}
	}
	if phase.Abort != nil {
		observe(phase.Abort.At)
	}
	if phase.AdmissionFailure != nil {
		observe(phase.AdmissionFailure.At)
	}
	if phase.ExecutionFailure != nil {
		observe(phase.ExecutionFailure.At)
	}
	for _, step := range phase.Steps {
		switch step := step.(type) {
		case *work.CheckStep:
			if step.Result != nil {
				observe(step.Result.FinishedAt)
			}
			if step.StartFailure != nil {
				observe(step.StartFailure.At)
			}
			for _, failure := range step.ReadOnlyFailures {
				observe(failure.At)
			}
		case *work.AgentReviewStep:
			observe(projectProcessFinish(step.Execution.Process))
			if step.ExecutionFailure != nil {
				observe(step.ExecutionFailure.At)
			}
			for _, failure := range step.ReadOnlyFailures {
				observe(failure.At)
			}
		case *work.ManualReviewStep:
			observe(projectProcessFinish(step.CommandResult))
			switch assessment := step.Decision.(type) {
			case *work.ApprovedReview:
				observe(assessment.At)
			case *work.RequestedChanges:
				observe(assessment.At)
			case *work.AbortedReview:
				observe(assessment.At)
			}
			for _, failure := range step.ReadOnlyFailures {
				observe(failure.At)
			}
		}
	}
	if latest.IsZero() {
		return nil
	}
	return &latest
}
func projectProcessFinish(result work.ProcessResult) time.Time {
	switch result := result.(type) {
	case *work.ExitedProcess:
		return result.At
	case *work.InterruptedProcess:
		return result.At
	case *work.StartFailedProcess:
		return result.At
	}
	return time.Time{}
}

func projectPublicationEvents(view *TaskState, w *work.Workflow, p *work.PublicationPhase, address work.AttemptRef) {
	destination, _ := w.ReviewDestination(p.Plan.Review)
	switch delivered := p.Delivery.(type) {
	case *work.PullRequestDelivery:
		view.Events = append(view.Events, Event{Address: address, Type: EventChangesPushed, At: delivered.ObservedAt, Branch: w.Context().TaskBranch, PushTarget: w.Context().TaskBranch, Commit: delivered.PublicCommit}, Event{Address: address, Type: EventPRObserved, At: delivered.ObservedAt, PRURL: delivered.PullRequest.URL, Branch: w.Context().TaskBranch})
	case *work.DirectDelivery:
		view.Events = append(view.Events, Event{Address: address, Type: EventChangesPushed, At: delivered.ObservedAt, Branch: destination.Branch, PushTarget: destination.Branch, Commit: delivered.Destination.Commit})
	}
}
