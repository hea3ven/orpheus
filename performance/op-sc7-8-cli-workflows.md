# CLI-entry workflow consolidation

## Contract

Product workflow tests enter through `cli.NewRootCommandWithOptions` in `internal/cli`, use external `package cli_test`, and live in `*_workflow_test.go`. The fixtures compose real routing, application services, review pipelines, and stores. They replace external task sources, Git, process launches, terminal observations, and usage capture with semantic collaborators. They do not replace the application decision under test.

Adapter scenarios remain in owner-local `*_adapter_test.go` files. Pure renderer mechanics and private service policy remain units. The synthetic coverage consumer is `TestConsumerCreditsCollaborator`, selected by its owning coverage-tool contract, not a product workflow.

The previously internal CLI workflows now reuse `workflowFixture`, `CommandOptions.Environment`, memory paths, and typed external collaborators. They no longer require `testpackage` suppressions, process-global fixture lookup, or private production JSON types. JSON assertions decode the public protocol into independent test DTOs.

## Assertion map

Names in the tables omit `TestIntegrationWorkflow` unless an explicit unit prefix is shown. New workflow owners are all `internal/cli`. Rows that consolidate existing coverage name the actual scenario, rather than retaining duplicate service-level tests.

### Doctor and evaluation

| Previous scenario | Current owner and assertions |
| --- | --- |
| Doctor `DoctorRecoversSyncConflictState` | `doctor_sync_workflow_test.go`, same name. Actual `doctor` and `doctor --fix` commands retain pushed and rollback cases, dry-run non-mutation, operation clearing, checkpoint restoration, and audit events. Physical Git safety stays in Git contracts. |
| Evaluation `ExecuteRunReportsUsageAndCostUnknownWhenProvisioningFailsBeforeExecution` | `EvalReviewContextReportsUnknownExecutionWhenEnvironmentFails`: failure report, unknown usage/cost, no execution and no launcher. |
| Evaluation `PrepareRunRequestsIsolatedTaskProvisioningWhenOperatorBeadsEnvIsSet` | `EvalReviewContextKeepsOperatorBeadsEnvOutOfSeededRepo`: isolated repo path, Beads/Git/task seed order, operator environment and directory unchanged. Concrete Beads environment sanitization stays with Beads contracts. |
| Evaluation `RunPipelineRecordsCodexPromptArgWithEvaluationSessionName` | `EvalReviewContextPersistsExecutionSessionArgsAndPromptEnv`: actual CLI evaluation, real review pipeline, persisted session name and argv, launch cwd, prompt argument and environment agreement. `EvalReviewContextRunsPipelineWithSafeBoundaries` also checks the JSON report, token/cost totals, scenario selection and isolated workdir. |

Evaluation uses explicit `revieweval.Effects` through `CommandOptions.Dependencies`, not context values or a canned report. Tests supply all process/model effects and isolate agent configuration. Production defaults still run only when a user explicitly invokes evaluation.

### Review lifecycle

The previous owner was `internal/workflow/review_lifecycle_test.go`. Old names below omit the common `ReviewLifecycle` suffix prefix.

| Previous scenario | Current scenario or owner unit |
| --- | --- |
| `RunRecordsBlockedOutcomeWithoutCobra` | `TaskReviewNonZeroCheckRecordsBlockingFindingAndStops`, `TaskReviewCheckBlockerKeepAcceptsEOFAnswer`: blocked state, finding step, no finalization. Cobra independence is no longer a workflow assertion. |
| `FreshReviewGuardPreservesKeptAutomatedBlocker` | `TaskRunRecoversHardStoppedAutomatedBlockerDecision`: keep persists eligibility without launching an agent. |
| `FreshReviewGuardKeepsFailedBlockerForTargetedFollowUp` | `TaskReviewKeepsFailedFreshBlockerEligibleForFollowUp`: original completion timestamp, blocked state, untargeted finding index, no check rerun or agent launch. |
| `FreshReviewGuardAllowsFailedBlockerDisposition` | `TaskReviewFreshBlockerDispositionsStartFreshCLIPipeline`: failed-review blocker can be addressed before a fresh attempt. |
| `FreshReviewGuardRecordsManualAddressAndWaiver` | `TaskReviewFreshBlockerDispositionsStartFreshCLIPipeline`: manual address and waiver, new attempt without copied findings, finalization after passing review. |
| `FreshReviewGuardKeepsInterruptedAutomatedBlockerEligibleForFollowUp` | `TaskRunRecoversHardStoppedAutomatedBlockerDecision`: keep supersedes interrupted-decision state. |
| `FreshReviewGuardInterruptionDoesNotMutateOrStart` | `TaskRunAfterInterruptedAutomatedBlockerDecisionRequiresFreshReview`: interrupted input leaves state unchanged and launches nothing. |
| `RunRejectsStagedCandidateBeforePipeline` | `TaskReviewRejectsCandidatePreflightFailuresBeforeStartingReview`: clean-index error before review persistence. |
| `ManualPromptPersistsFindingsThroughWorkflowRecorder` | `TaskReviewBlockingFindingBlocksWithoutFinalizing`, `TaskReviewManualInputLossReplaysRecordedFindings`: manual findings persist with their step and survive input loss. |
| `ConfirmsRunningCompletionBeforeFinalizing` | `TaskDoneConfirmsRunningCompletionWithoutRewritingRunStatus`: approve/decline prompt, finalization and closure only on approval, run remains running. The old direct service entry bypassed CLI routing; a running implementation without passed review is correctly reported as active by `task run`. |
| `PreparesPipelineBeforeFreshReviewTransition` | `TaskReviewInvalidReviewAgentConfigDoesNotStartFreshAttempt`: invalid config does not create an attempt. |
| `PreparesPipelineBeforeResumeReviewTransition` | `TaskReviewInvalidReviewAgentConfigDoesNotResumeManualAttempt`: waiting attempt remains intact. |
| `RecoversBeforeReplacementConfiguration` | `ReviewPrimaryProcessRecoveryKeepsFindingsForAudit`: recovery precedes replacement config, interrupts stopped execution, preserves audit findings. |
| `ReturnsTypedPrimaryReviewLivenessOutcomes` | Same CLI scenario's absent/live/unknown table: operator guidance, interruption only when absent, no new launch or publication. |
| `StopsWhenPrimaryWasConcurrentlyRecovered` | Owner units `TestPrepareTaskRunMarksConcurrentlyRecoveredPrimaryForInspectionStop` and `TestReviewStartupStopsAfterConcurrentPrimaryRecovery`: stale-store revalidation and startup-stop/outcome policy. No new store-replacement seam in the CLI. |
| `RejectsClosedTaskBeforeStartingReview` | `TaskRunRejectsClosedTaskWithoutChangingRecordedState`: closed task rejected before Git, agent, or review mutation. |
| `MissingAgentRunnerDoesNotStartFollowUpRun` | Owner unit `TestAutonomousReviewFollowUpRequiresRunnerBeforeAnyEffects`: invalid service wiring fails before collaborators run. The CLI always supplies its runner. |
| `UsageErrorDoesNotFailSuccessfulAutonomousFollowUp` | `TaskReviewUsageWriteErrorDoesNotFailSuccessfulAutonomousFollowUp`: storage failure is reported, but completed repair remains succeeded with completion/provenance/targeted finding; no invented usage or publication. |
| `RunAfterCompletedRunStartsReviewAndPropagatesAgent` | `TaskReviewResumedAutonomousFollowUpPreservesSelectedImplementer`: selected implementer survives targeted repair and second review. |
| `AutonomousFollowUpResumesCompatibleSession` | `TaskReviewAutonomousFollowUpResumesCompatiblePiSession`: resume arguments, source attempt/session, usage baseline, completed review. |
| `RunAfterCompletedRunSkipsReviewWithoutCompletion` | `TaskRunSuccessfulExitWithoutCompletionAllowsOrdinaryRetry`: ordinary retry, not review, after completion-less success. |

### Sync conflict lifecycle

| Previous scenario | Current scenario and assertions |
| --- | --- |
| `SyncConflictStopsExternalMutationsWhenPhasePersistenceFails` | `TaskSyncConflictStopsExternalMutationsWhenPhasePersistenceFails`: six failed durable-phase writes, exact begin/resolve/commit/push/rollback effects, and durable phase checked before mutations. |
| `SyncAllDoesNotPushWhenConflictDisappearsAfterPreflight` | `TaskSyncAllClearsConflictThatDisappearsAfterPreflight`: CLI `--all`, already-current result, no resolver/commit/push/rollback, cleared checkpoint, task remains open. |
| `SyncConflictRecoversPushAfterPushedPhasePersistenceFails` | `TaskSyncConflictRecoversPushAfterPushedPhasePersistenceFails`: first invocation leaves durable push intent after remote push; retry verifies remote head, clears operation and records finished audit event without repeating repair or publication. |

`state.SetMemoryDataWriteError` is integration-only and rejects OS-backed paths. It fails proposed data writes while retaining actual serialization and store behavior. A capture-result fake alone could not prove these persistence-ordering assertions.

### Review pipeline

Previous owners were `internal/review/runner_workflow_test.go` and `paired_review_workflow_test.go`. Old names below omit `RunPipeline`; new pipeline names have the common prefix `ReviewPipeline` and suffix `ThroughCLI`.

| Previous scenario | Current scenario and assertions |
| --- | --- |
| `InteractiveAgentReviewOutputDependsOnProfileMode` | `ReviewPipelineAttachedReviewerPassesOutputThroughCLI` and `ReviewPipelineLabelsAndIsolatesPairedRollingOutputThroughCLI`: attached streams versus non-interactive rolling output. |
| `InteractiveAgentReviewNonBlockingFindingLeavesLiveTail` | `ReviewPipelineNoninteractiveAdvisoryLeavesLiveTailThroughCLI`: advisory does not clear the live tail. |
| `AgentReviewUsesEffectivePromptInCommandAndEnvironment` | `ReviewPipelinePassesPromptModelEnvironmentAndChildPIDThroughCLI`: effective prompt, model, argv/environment agreement. |
| `PersistsPrimaryReviewerProcessFacts` | Same scenario: execution purpose, harness/model, child PID persisted through actual launch callback. |
| `InteractivePassingAgentReviewClearsWrappedRollingTail` | Paired rolling-output CLI scenario checks pipeline clear policy; owner unit `TestRollingTailFinishClearClearsWrappedOutput` checks wrapped cell clearing. |
| `InteractivePassingCheckClearsRollingTail` | `ReviewPipelinePassingCheckClearsRollingTailThroughCLI`: passing check clears tail. |
| `InteractiveBlockedCheckLeavesExpandedRollingTail` | `ReviewPipelineBlockedCheckExpandsRollingTailThroughCLI`: blocker expands bounded tail, output stays off stdout. |
| `PausesBeforeManualStep` | `TaskReviewPassingCheckContinuesToManualStep`, `TaskReviewResumesManualWaitingAttempt`: check precedes manual pause, existing attempt resumes. |
| `HunkManualCommandContinuesWhenSessionMissing` | `TaskReviewImportsHunkNotesWithSelectedDisposition`, empty-notes case. Missing-session process details stay in the direct Hunk contract. |
| `GenericManualCommandDoesNotPollHunkNotes` | `TaskReviewConfirmedManualCommandRunsAndRecordsStep`: generic command uses the command collaborator, not Hunk polling. |
| `HunkManualCommandFailureRemainsOperationalError` | `ReviewPipelineHunkManualCommandFailureRemainsOperationalErrorThroughCLI`: operational failure, failed review, no findings prompt or publication. |
| `RestartsBlockedCheckInSameAttempt` | `ReviewPipelineRestartsBlockedCheckInSameAttemptThroughCLI`: rerun in same attempt, only successful step retained. |
| `RestartedBlockerRetainsAuthoritativeNumber` | `ReviewPipelineRestartedCheckRetainsFindingNumberThroughCLI`: rerun blocker retains number 1. |
| `PausesAndResumesAutomatedBlockerDecision` | `TaskRunResumesPausedAutomatedBlockerDecision`: persisted pause and subsequent decision. |
| `RestartFromResumedAutomatedDecisionRerunsStep` | `ReviewPipelineRestartFromResumedAutomatedDecisionRerunsCheckThroughCLI`: resume, restart, rerun, no stale findings. |
| `PairedReviewerAdmitsAlternateFindingAfterPrimary` | `ReviewPipelineAdmitsAlternateFindingAfterPrimaryThroughCLI`: role-aware child `agent review add`, alternate admission, authoritative findings. |
| `RestartedPairedReviewDiscardsPriorExecution` | `ReviewPipelineRestartedPairedReviewKeepsOnlyRerunComparisonThroughCLI`: rerun replaces steps/comparisons/findings. |
| `PairedReviewerKeepsDuplicateAndExcludedFindingsNonAuthoritative` | `ReviewPipelineClassifiesDuplicateAndExcludedAlternateFindingsThroughCLI`: raw classifications retained, no extra authoritative finding. |
| `PairedReviewerLabelsAndIsolatesRollingOutput` | `ReviewPipelineLabelsAndIsolatesPairedRollingOutputThroughCLI`: role labels, primary clear before alternate. |
| `PairedReviewerKeepsExpandedPrimaryBlockerTailBeforeAlternate` | `ReviewPipelineKeepsExpandedPrimaryBlockerTailBeforeAlternateThroughCLI`: primary blocker expands before alternate starts. |
| `PairedReviewerLabelsAttachedOutput` | `ReviewPipelineLabelsAttachedPairedOutputThroughCLI`: ordered labels and attributed stdout/stderr. |
| `PairedReviewerHeaderWriteFailureDoesNotRecordPrimaryExecution` | `ReviewPipelineReviewerHeaderFailureDoesNotLaunchOrRecordStepThroughCLI`: header write failure prevents execution and step persistence. |
| `PairedReviewerResolutionFailureIsPersisted` | `ReviewPipelineMissingAlternateProfilePersistsComparisonFailureThroughCLI`: alternate failure recorded, primary passes. |
| `PairedReviewerAlternateFailureDoesNotFailPrimary` | `ReviewPipelineAlternateFailureKeepsPrimaryAuthoritativeThroughCLI`: failed alternate execution does not fail primary. |
| `PairedReviewerInterruptedInputBlocksWithoutAdmission` | `ReviewPipelineInterruptedAlternateDecisionBlocksWithoutAdmissionThroughCLI`: interrupted comparison, no admission, fresh-review guidance. |
| `PairedReviewerPrimaryFailureSkipsAlternate` | `ReviewPipelinePrimaryFailureSkipsAlternateThroughCLI`: one launch, no comparison. |
| `WithoutAlternatePreservesLegacyReviewerEnvironment` | `ReviewPipelinePassesPromptModelEnvironmentAndChildPIDThroughCLI`: no reviewer-role environment for a single reviewer. |

`TerminalCapabilities` injects invocation-local terminal observations without a PTY or process-global monkeypatch. Production defaults retain OS probes. `agent review add` now reads reviewer role from the same invocation environment as the other child inputs.

The tab/wide-cell, control-character sanitization, resize, wrapped-line clearing, and stderr-color mechanics from `rolling_tail_regression_workflow_test.go` are focused units in `internal/review/rolling_tail_internal_test.go`. They directly exercise the private renderer without a store or pipeline. CLI tests above retain the decision of when the pipeline clears, expands, or leaves output live.

## Dispatch workflows and validation

Earlier dispatch workflows also used supplied terminal review outcomes. They now call the real pipeline. `TaskRunCompletionLeavesTaskAwaitingManualReview` and `TaskRunConfiguredDefaultPreservesLaunchContextAndAuditFacts` reach the actual manual pause. `TaskRunBlockingReviewDispatchesTargetedRepair` launches the semantic reviewer, keeps its recorded blocker through CLI input, dispatches repair, and reaches manual review after the second real pipeline run. The fake pipeline-recording helpers were removed.

The inventory contains 286 product workflow declarations, all in external `cli_test`, and 72 owner-local adapter declarations with the standardized filename suffix. This is an ownership inventory, not a target test count.

Validation passed:

- `make check`: formatting, both measured lanes, unit-tag lint, and CLI build.
- `go vet ./...` and `go vet -tags=integration ./...`.
- `golangci-lint run --build-tags=integration ./...`.
- `git diff --check`.

The first complete check caught the nested coverage fixture's stale integration build tag after its synthetic test was renamed. It is now an ordinary test inside the separate fixture module; the outer integration contract still explicitly selects it. The next check passed both test lanes and requested policy refresh for the intentional ownership/coverage changes.

`make quality-policy-update` collected five complete unit-then-integration samples, with default package concurrency and `-parallel=1`, after all implementation agents finished. The reviewed update changed 11 bounds: five coverage floors, three timing ceilings, and removal of three obsolete package-work ceilings. Renderer mechanics moved from integration to units, raising review unit coverage and lowering its integration-only floor. CLI evaluation composition raised evaluation integration coverage. Doctor, evaluation, and workflow still receive cross-package integration coverage but no longer own selected integration tests. Unit suite, agent, and taskstate timing ceilings reflect this batch's repeated measurements, not a claimed code speedup or slowdown. No category-aware policy or CI behavior changed.

After refresh, `make check` passed with unit coverage 55.38% and integration coverage 64.45%. Logs and the complete five-sample report are under `artifacts/test-coverage/cli-entry/`. No live model evaluation, commit, or push was run.
