# Contract ownership repair

## Scope

The subsequent [CLI-entry consolidation](op-sc7-8-cli-workflows.md) moves every product workflow into external `cli_test` and supersedes service-level test locations below. Adapter ownership and `*_adapter_test.go` naming remain unchanged.

The approved review covered 184 main-module adapter-labelled declarations and one nested coverage fixture. All 99 CLI product scenarios were replaced or split, rather than relabelled around their old process fixtures. Doctor sync recovery and three evaluation scenarios now compose workflows. Beads relationship contracts call `beads.TaskBackend`, not `task.UpdateService`. Ten review runner scenarios now use semantic command effects.

There are 72 main-module adapter declarations after the repair. This count is an inventory, not a quality metric. They comprise Git 42, agent execution 7, Beads 5, pull requests 8, review 6, taskbranch 2, the coverage tool 1, and CLI fixture infrastructure 1. The nested consumer is a synthetic Go-tool fixture, not a product workflow. Existing direct Git, agentexec, pullrequest, taskbranch, and coverage-tool contracts retain their boundaries.

An adapter test calls its owning package's concrete operation directly. Supporting imports are allowed. A CLI journey does not become an adapter contract merely because it launches a fake executable. Package-owned parsing and file-reader tests remain Go-only units.

## Production defaults

- Doctor now receives CLI cleanup and process-probe dependencies. Its `Effects` supply usage capture, canonical session comparison, worktree existence, and sync recovery. Nil values retain the original adapters. Filesystem inspection failures still count as possibly present, so cleanup remains conservative.
- Review accepts Hunk-command and usage-capture effects. Nil retains the child-process/Hunk implementation and `agent.CaptureUsage`. CLI review forwards its existing capture dependency unless a review-specific capture was supplied.
- Completion uses the invocation's task working directory before `os.Getwd`. Its config fixture uses existing `state.SeedMemoryConfigYAML`; no new storage API was needed.
- Evaluation preparation and execution have private collaborator inputs. Production still initializes Beads **before** seeding the Git baseline, then seeds the task. Pipeline tests still call real `review.RunPipeline`.
- Candidate-readiness policy has a private observation seam. `ValidateReviewCandidateReady` keeps its existing clean-index and candidate-change adapters.

## Concrete assertion owners

| Removed higher-level boundary assertion | Owner retained or added |
| --- | --- |
| Beads list/show argv, filters, issue types, closed items | `internal/beads/beads_test.go`: `TestTaskBackendListFilteredPushesSupportedFieldsAndMatchesQueryAtSource`, `TestTaskBackendListExcludesUnsupportedTypesAndPreservesRelations`, `TestTaskBackendGetParsesShowJSON`, `TestTaskBackendGetReturnsClosedItemsAndRejectsUnsupportedTypes`. Workflow fixtures supply already-decoded task items. |
| Agent argv, cwd, environment, streams, child exits and reaping | `internal/agentexec/launcher_adapter_test.go`, `launcher_environment_adapter_test.go`, and `launcher_cancellation_adapter_test.go` retain direct adapter contracts. Workflows assert semantic launch requests and persisted facts. |
| Clean/dirty/ignored/locked worktree removal | `internal/git/worktree_adapter_test.go`: `TestIntegrationAdapterContractClosedTaskWorktreeCleanupRemovesOnlyCleanDeterministicWorktrees` and `TestIntegrationAdapterContractClosedTaskWorktreeCleanupPreservesLockedWorktrees`. |
| Remote sync completion and checkpoint rollback | `internal/git/conflict_recovery_adapter_test.go`: `TestIntegrationAdapterContractConflictRecoveryCommitsLocallyBeforeSeparatePush` and `TestIntegrationAdapterContractConflictRecoveryRollsBackOnlyMatchingCheckpoint`. |
| Staged-index observation and tracked patches | `internal/git/status_adapter_test.go`: `TestIntegrationAdapterContractHasStagedChangesDistinguishesChangesFromGitFailure` and `TestIntegrationAdapterContractCandidateGitOperationsCaptureAndRestoreTrackedDiff`. |
| Missing candidate versus recorded finalization | New `internal/workflow/review_candidate_internal_test.go:TestReviewCandidateReadinessRequiresCleanIndexAndChangesOrFinalization`. Tests index-error short-circuit, missing candidate rejection, existing finalization, and observation/load errors without Git. |
| Tracked/untracked candidate restoration and reviewer-created files | Review's direct `TestIntegrationAdapterContractCandidateSnapshotRestoresTrackedAndUntrackedMutations` and `TestIntegrationAdapterContractCandidateSnapshotRestoresRedirectedStderrFile`. These exercise review's snapshot adapter, not the whole pipeline. |
| Hunk final polling and command failures | `TestIntegrationAdapterContractHunkManualCommandCapturesNotesAfterCommandExit` and new `TestIntegrationAdapterContractHunkManualCommandHandlesMissingSessionAndExitFailure`. The latter checks missing-session polling, zero/nonzero child exits, argv, cwd, scoped environment, and both streams. Neither needs a Git repository. |
| Codex/Pi session decoding and ambiguity | `internal/agent/codex_usage_test.go` and `pi_usage_test.go`: `TestCaptureCodexUsageCorrelatesSessionAndTokenCount`, `TestCaptureCodexUsageReportsAmbiguousMatches`, `TestCaptureCodexUsageRequiresMatchingSessionCWD`, `TestCapturePiUsageCorrelatesSessionAssistantMessageUsageAndReportedCost`, `TestCapturePiUsageReportsAmbiguousMatches`, and `TestCapturePiUsagePreservesNoAssistantUsageReasonForClosestSession`. |
| Delayed resumed usage and canonical session identity | New `internal/agent/recovery_usage_test.go`: `TestCaptureResumedPiUsageStopsAtLaterLaunchBaseline`, `TestSameCanonicalSessionRequiresMatchingIDAndResolvedFile`, and `TestCapturePiUsageRetainsTokensWithoutReportedCost`. Real JSON/symlink fixtures cover upper token/cost baselines, missing cost, matching IDs, canonical files, and failed resolution. |
| Exact planning/finding file bytes and file errors | New CLI units `TestResolveCreateContentReadsFilesVerbatimAndReportsErrors` and `TestResolveExactlyOneTextReadsFilesVerbatimAndValidatesPresence` in `task_authoring_file_internal_test.go`. |
| Legacy absent interactivity | `internal/taskstate:TestStoreTreatsMissingImplementationInteractivityAsNonInteractive`, plus the agent-context workflow below. |
| Corrupt or mismatched local state | State/taskstate unit decoding and identity tests remain. CLI workflows seed mismatched YAML through memory paths and retain failure propagation or intentional ignore behavior. |

## Doctor assertion map

All old names below had the `TestIntegrationAdapterContract` prefix. Replacements use `TestIntegrationWorkflow` with the same suffix in `internal/cli/doctor_usage_workflow_test.go`, unless noted. Capture callbacks check the requested execution directories; stored-price cases reject unexpected capture. The actual doctor traversal, diagnosis, repair, and persistence run in every workflow.

| Scenario suffix | Retained workflow assertions |
| --- | --- |
| `DoctorRecoversCodexUsageForImplementationAndReviewAgent` | Run and review execution selection, captured usage/session persistence, pricing, doctor rows, and stats output. |
| `DoctorDoesNotOverwriteExistingCodexCostWhenRecoveringSession` | Recovered session/usage does not replace an existing cost with the different supplied cost. |
| `DoctorLeavesTotalOnlyCodexUsageCostUnknown` | Total-only tokens survive while category-dependent cost remains unknown. |
| `DoctorStampsStoredCodexCostsWithoutSessionLogRecorrelation` | Stored usage is priced without invoking session capture. |
| `DoctorBackfillSelectsPricingByExecutionStart` | Effective-date price selection follows each execution start, not recovery time. |
| `DoctorFallsBackToRegisteredRepoRootWhenTaskTargetIsMissing` | Capture request uses registered repo root when no recorded target is available. |
| `DoctorRecoversPiUsageAndReportedCost` | Pi tokens, reported cost, matched-session diagnostics, and stats persistence. |
| `DoctorRefreshesStoredPiReportedCost` | Refresh replaces the stale reported cost with the supplied current session cost. |
| `DoctorBoundsDelayedResumedPiRecoveryAtNextLaunch` | Canonical-session comparison is requested; next launch and token/optional-cost baseline constrain capture. Later-run usage is not changed. |
| `DoctorRecoversPiUsageWhenMatchedSessionHasNoReportedCost` | Matched token usage is recovered without inventing a cost. |
| `DoctorDoesNotRecoverPiCostWhenMatchedSessionHasNoReportedCost` | Existing stored usage does not manufacture missing reported cost. |
| `DoctorRecoversUsageForUnfinishedExecution` | Unfinished execution telemetry is recoverable without fabricating a terminal run. |
| `DoctorReportsAmbiguousAndNoMatchWithoutMutating` | Codex ambiguity/no-match diagnostics and candidate details leave persisted execution unchanged. |
| `DoctorReportsAmbiguousPiMatchesWithoutMutating` | Pi ambiguity diagnostics and candidate paths leave state unchanged. |
| `DoctorRecoversSyncConflictTerminalUsage` | Terminal sync-conflict usage/cost update and audit/stat projections. |
| `DoctorPrefersRecordedSyncConflictWorktreeBeforeFallbackDirs` | Capture tries the recorded worktree before fallback directories. |
| `DoctorTraversesAllRegisteredRepos` | Every registered repo is visited without mixing task state. |
| `DoctorRepairsCleanClosedTaskWorktreeAndPreservesDirtyAndLockedWorktrees` | In `doctor_cleanup_workflow_test.go`: dry-run does not remove; fix removes only clean; dirty/locked remain; unlock then retry removes; final report and both removal audit events. |

`internal/cli/doctor_sync_workflow_test.go:TestIntegrationWorkflowDoctorRecoversSyncConflictState` replaces the doctor's real-Git journey. The pushed and rollback cases retain dry-run non-mutation, operation clearing, checkpoint restoration, and audit events. Git contracts above retain physical refs/conflicts/rollback safety.

## Task inspection, status, and completion

Old names in these tables are suffixes of `TestIntegrationAdapterContract`. Unchanged suffixes now use the workflow prefix. All fixtures use memory-backed stores and typed read collaborators, including completion configuration.

### Task inspection, list, stats, dir, and review display

| Old adapter scenario | New workflow scenario | Preserved product assertions | Moved/removed boundary assertions |
| --- | --- | --- | --- |
| `TaskListListsAllActiveItemsWithStatusProjectionPresentation` | `TestIntegrationWorkflowTaskListListsAllActiveItemsWithStatusProjectionPresentation` | Human columns, repo names, status projection, priorities, PR detail, JSON IDs/status/kind, hidden internal metadata and closed items. | Removed fake `bd` argv log assertion. Covered by Beads owner units for list argv/JSON translation. |
| `TaskListScopesOneRegisteredRepository` | `TestIntegrationWorkflowTaskListScopesOneRegisteredRepository` | Repo ID/name/prefix resolution, excluded repo not queried, JSON repo ID, query/type/date/status filters, sort modes, unknown repo guidance. | Replaced command-log count with semantic read observation. Beads owns `--readonly --sandbox list --all --limit 0` argv. |
| `TaskListReportsSelectedRepositoryFailure` | `TestIntegrationWorkflowTaskListReportsSelectedRepositoryFailure` | Selected repo failure returns partial-failure error, stdout contains failed repo, stderr includes repo ID and backend message, excluded healthy repo is not queried. | Removed process exit-code fixture. Beads owns process error decoding. |
| `TaskListComposesSourceAndProjectedStatusFilters` | `TestIntegrationWorkflowTaskListComposesSourceAndProjectedStatusFilters` | Source-field filter plus projected closed-status filter, text and JSON output contain only `a-closed`. | Removed exact Beads source-filter argv assertion. Beads list-filter translation owns it. |
| `TaskListReportsPartialRepoFailures` | `TestIntegrationWorkflowTaskListReportsPartialRepoFailures` | Healthy repo rows survive another repo failure, failure appears in text/stderr, JSON returns healthy task with error. | Removed fake process stderr/exit fixture. Semantic failure now comes from task source. |
| `TaskShowResolvesPrefixQueriesOnlyResolvedRepoAndRendersDetails` | `TestIntegrationWorkflowTaskShowResolvesPrefixQueriesOnlyResolvedRepoAndRendersDetails` | Prefix resolves repo, details render external ref, description, design, acceptance, labels, metadata projection, history placeholder, no child section. | Replaced Beads `show --id` log with semantic read observation. |
| `TaskShowEpicRendersSortedDirectChildrenFromResolvedRepo` | `TestIntegrationWorkflowTaskShowEpicRendersSortedDirectChildrenFromResolvedRepo` | Direct task/epic children render sorted by type/title, grandchild and unsupported child do not render. | Removed Beads show/list argv assertions. |
| `TaskShowEpicRendersEmptyChildrenState` | `TestIntegrationWorkflowTaskShowEpicRendersEmptyChildrenState` | Empty epic children message. | None. |
| `TaskShowEpicReportsChildQueryFailureWithRepositoryAndParent` | `TestIntegrationWorkflowTaskShowEpicReportsChildQueryFailureWithRepositoryAndParent` | Error includes task ID, repo ID, parent ID, and backend message; stdout/stderr stay empty. | Replaced fake child-list process failure with semantic relationship-list failure. |
| `TaskStatsRendersImplementationExecutionUsage` | `TestIntegrationWorkflowTaskStatsRendersImplementationExecutionUsage` | Execution table, implementation and review rows, command quoting, timestamps, durations, sessions, token usage, estimated cost, totals. | Task source is semantic. Session files/agent capture are not invoked. |
| `TaskStatsRendersSyncConflictResolutionExecutionUsage` | `TestIntegrationWorkflowTaskStatsRendersSyncConflictResolutionExecutionUsage` | Sync-conflict row, profile/harness/model/command, timestamps, duration, session, usage, estimated cost, totals regexp. | Git/sync process behavior stays with Git/doctor owners. |
| `TaskStatsKeepsTokenUsageWhenCostPricingIsUnknown` | `TestIntegrationWorkflowTaskStatsKeepsTokenUsageWhenCostPricingIsUnknown` | Token usage remains visible, pricing unknown reason, zero cost and unknown-cost count. | None. |
| `TaskStatsUsesPiReportedEstimatedCost` | `TestIntegrationWorkflowTaskStatsUsesPiReportedEstimatedCost` | Pi profile/model/session, reported estimated cost amount/kind/source, usage tokens. | Pi usage decoding remains in agent owner tests. |
| `TaskStatsCountsMissingPiUsageCostAsUnknown` | `TestIntegrationWorkflowTaskStatsCountsMissingPiUsageCostAsUnknown` | Unknown Pi usage reason with candidate count, zero cost. | Pi session matching remains in agent owner tests. |
| `TaskStatsAggregateGroupsResolvedTasksByDay` | `TestIntegrationWorkflowTaskStatsAggregateGroupsResolvedTasksByDay` | Throughput day view, unresolved count, workflow medians, consumption filter text, token/cost row. | None. |
| `TaskStatsAggregateGroupsResolvedTasksByMonth` | `TestIntegrationWorkflowTaskStatsAggregateGroupsResolvedTasksByMonth` | Month grouping and zero workflow coverage rows. | None. |
| `TaskStatsAggregateRepoFilterSkipsUnselectedRepoFailures` | `TestIntegrationWorkflowTaskStatsAggregateRepoFilterSkipsUnselectedRepoFailures` | Repo filter output and unselected failed repo not queried. | Removed fake Beads log. |
| `TaskStatsAggregateReceivesOnlyTaskSourceItems` | `TestIntegrationWorkflowTaskStatsAggregateReceivesOnlyTaskSourceItems` | Aggregate counts only task items; corrupt/mismatched epic state does not break aggregate. | Unsupported Beads item filtering remains in Beads owner units. |
| `TaskStatsDirectEpicStatsRemainAvailable` | `TestIntegrationWorkflowTaskStatsDirectEpicStatsRemainAvailable` | Direct epic stats show execution, model, duration, tokens. | None. |
| `TaskShowRendersClosedItemsAndHistory` | `TestIntegrationWorkflowTaskShowRendersClosedItemsAndHistory` | Closed task details and close history line. | None. |
| `TaskShowRendersChronologicalHistoryForClosedEpic` | `TestIntegrationWorkflowTaskShowRendersChronologicalHistoryForClosedEpic` | Chronological visible events, hidden reused/internal run status details. | None. |
| `TaskShowProjectsReviewAttemptMilestonesIntoHistory` | `TestIntegrationWorkflowTaskShowProjectsReviewAttemptMilestonesIntoHistory` | Review started/finished milestones ordered with run history. | None. |
| `TaskShowProjectsReviewFollowUpCreationIntoHistory` | `TestIntegrationWorkflowTaskShowProjectsReviewFollowUpCreationIntoHistory` | Timestamped follow-up creation appears; legacy untimestamped created task is hidden. | None. |
| `TaskShowFailsWhenLocalTaskStateCannotBeLoaded` | `TestIntegrationWorkflowTaskShowFailsWhenLocalTaskStateCannotBeLoaded` | Local state load error is propagated with repo/task identity mismatch. | Raw corrupt YAML source assertion belongs to taskstate decode/identity coverage. |
| `TaskShowRejectsUnsupportedItemsAtTaskSourceBoundary` | `TestIntegrationWorkflowTaskShowRejectsUnsupportedItemsAtTaskSourceBoundary` | CLI propagates semantic unsupported-item error and keeps stdout/stderr empty. | Beads owns issue-type parsing/filtering. |
| `TaskDirPrintsWorktreeDirectory` | `TestIntegrationWorkflowTaskDirPrintsWorktreeDirectory` | Worktree path printed; other repo not queried. | Removed Beads `show --id` argv assertion. |
| `TaskDirPrintsRepoRootForMainTask` | `TestIntegrationWorkflowTaskDirPrintsRepoRootForMainTask` | Main-branch task prints cleaned repo root. | None. |
| `TaskDirReportsMissingAndInconsistentMetadata` | `TestIntegrationWorkflowTaskDirReportsMissingAndInconsistentMetadata` | Missing worktree, missing branch, and inconsistent target diagnostics. | None. |
| `TaskShowReviewDisplaysCrossAttemptFindingHistory` | `TestIntegrationWorkflowTaskShowReviewDisplaysCrossAttemptFindingHistory` | Cross-attempt authoritative finding history, disposition text, guidance. | Existing harmless state-seeding helper reused. |
| `TaskShowReviewGuidesRetryAfterFailedFollowUp` | `TestIntegrationWorkflowTaskShowReviewGuidesRetryAfterFailedFollowUp` | Failed follow-up disposition and retry guidance. | None. |
| `TaskShowReviewGuidesWhenTaskHasNoReviewAttempts` | `TestIntegrationWorkflowTaskShowReviewGuidesWhenTaskHasNoReviewAttempts` | Empty review state and next-step guidance. | None. |
| `TaskShowReviewRendersManuallyAddressedFinding` | `TestIntegrationWorkflowTaskShowReviewRendersManuallyAddressedFinding` | Finding detail shows manual-addressed disposition. | None. |
| `TaskShowReviewGuidesPausedAutomatedBlockerDecision` | `TestIntegrationWorkflowTaskShowReviewGuidesPausedAutomatedBlockerDecision` | Paused blocker-decision state and resume guidance. | None. |
| `TaskShowReviewGuidesInterruptedAutomatedBlockerDecision` | `TestIntegrationWorkflowTaskShowReviewGuidesInterruptedAutomatedBlockerDecision` | Interrupted blocker-decision state and fresh-review guidance. | None. |
| `TaskShowReviewDisplaysClosedTaskReviewState` | `TestIntegrationWorkflowTaskShowReviewDisplaysClosedTaskReviewState` | Closed task review history remains visible. | None. |

### Status

| Old adapter scenario | New workflow scenario | Preserved product assertions | Moved/removed boundary assertions |
| --- | --- | --- | --- |
| `StatusGroupsLocalTaskSnapshots` | `TestIntegrationWorkflowStatusGroupsLocalTaskSnapshots` | Groups, order, full mode, JSON visibility, PR/review/run/dependency details. | Removed Beads and `gh` absence log assertions; semantic reads prove no unexpected source calls. |
| `StatusFullIgnoresCorruptClosedAndPullRequestStates` | `TestIntegrationWorkflowStatusFullIgnoresCorruptClosedAndPullRequestStates` | Full status still renders closed and PR rows when local state load fails. | Raw invalid YAML moved to taskstate owner; workflow uses mismatched YAML through memory paths. |
| `StatusShowsSuccessfulMainRunAsLocalRepoRootReview` | `TestIntegrationWorkflowStatusShowsSuccessfulMainRunAsLocalRepoRootReview` | Successful main-branch run appears as local repo-root review with guidance. | None. |
| `StatusAndTaskListUseLocalRunHistoryOnOpenTaskAsNeedsAttention` | Same new name with workflow prefix | Open backend item with running local run is needs attention; task list still includes it. | None. |
| `StatusAndTaskListRenderEquivalentRowsIdentically` | Same new name with workflow prefix | Shared row rendering for status/task list and different default visibility of blocked rows. | None. |
| `StatusRendersEpicChildrenAsIntegratedTreeRows` | Same new name with workflow prefix | Tree rows, progress, hidden blocked/done children in default status, full mode includes them, task list tree. | None. |
| `StatusReportsRepoFailuresInUnknownGroupAndReturnsError` | Same new name with workflow prefix | Unknown/needs-attention failure row, stderr diagnostics, JSON repo-failure entry plus healthy task. | Process failure source moved to semantic backend; Beads owns process translation. |
| `TaskViewsApplySharedSortModesAcrossRepositories` | Same new name with workflow prefix | Status/list sort modes and JSON order across repos. | None. |

### Completion

| Old adapter scenario | New workflow scenario | Preserved product assertions | Moved/removed boundary assertions |
| --- | --- | --- | --- |
| `CompletionProtocolSuggestsContextAwareValues` | `TestIntegrationWorkflowCompletionProtocolSuggestsContextAwareValues` | Task/show/stats/review/run/edit/start/close/repo/relation/agent/pipeline/repo-config completion choices. | Beads snapshot process calls removed. Configuration uses `state.SeedMemoryConfigYAML`. |
| `CompletionProtocolExcludesEditedEpicFromParentCandidates` | Same new name with workflow prefix | Edited epic excluded from parent candidates. | Removed fake Beads fixture. |
| `CompletionProtocolUsesOneSnapshotAndToleratesRepositoryFailure` | Same new name with workflow prefix | Selected repo relation completion survives another repo failure and hides failed repo values. | Replaced fake command count with semantic source read count. |
| `CompletionProtocolSkipsUnprojectableRepositorySource` | Same new name with workflow prefix | Unprojectable registry entry is skipped. | Removed temp legacy repo directory. |
| `CompletionProtocolScopesCreateRelationsToCurrentDirectory` | Same new name with workflow prefix | Current-directory scoping, outside-directory no results, nested repo ambiguity no results. | Replaced `t.Chdir` and disk directories with `CommandOptions.TaskWorkingDirectory`. |
| `CompletionProtocolTruncatesUnicodeDescriptionsOnRuneBoundaries` | Same new name with workflow prefix | UTF-8 validity, exact truncation, directive output. | Removed fake Beads fixture. |


## Task authoring and agent context

These workflows live in `task_epic_workflow_test.go`, `task_authoring_workflow_test.go`, and `agent_context_workflow_test.go`. They use a recording task backend and memory registry/task state. File-reader units are in `task_authoring_file_internal_test.go`.

| Old scenario | New coverage | Assertions kept |
| --- | --- | --- |
| `TestIntegrationAdapterContractTaskStartActivatesEligibleEpicAndRejectsOrdinaryTask` | `TestIntegrationWorkflowTaskStartActivatesEligibleEpicAndRejectsOrdinaryTask` | Start output, parent and blocking dependency reads, semantic `StartEpic` request, status mutation, ordinary task guidance, no start mutation on failure. |
| `TestIntegrationAdapterContractTaskStartHonorsParentDependencyAndIdempotency` | `TestIntegrationWorkflowTaskStartHonorsParentDependencyAndIdempotency` | Parent-active policy, active dependency rejection, idempotent in-progress output, no start mutation for rejected or idempotent cases. |
| `TestIntegrationAdapterContractTaskCloseRequiresVerifiedClosedChildrenAndIsIdempotent` | `TestIntegrationWorkflowTaskCloseRequiresVerifiedClosedChildrenAndIsIdempotent` | Close output, active child IDs sorted, incomplete child listing error, ordinary task guidance, idempotent closed output, no close mutation except the completed-epic case. |
| `TestIntegrationAdapterContractTaskEpicLifecycleAdapterFailuresAreSourceNeutral` | `TestIntegrationWorkflowTaskEpicLifecycleFailuresAreSourceNeutral` | Source-neutral start/close errors hide backend/process details while preserving operation-specific diagnostics. |
| `TestIntegrationAdapterContractTaskCreateReadsPlanningFilesAndRendersCreatedTypeAndID` | `TestIntegrationWorkflowTaskCreateRendersCreatedTypeAndIDAndSendsSemanticContent`; `TestResolveCreateContentReadsFilesVerbatimAndReportsErrors` | Create output, created type/id, semantic create request fields. Exact file bytes, inline passthrough, conflict error, and read error moved to CLI unit coverage. |
| `TestIntegrationAdapterContractTaskCreateRequiresExternalReferenceForGatedRepository` | `TestIntegrationWorkflowTaskCreateRequiresExternalReferenceForGatedRepository` | Gated repo rejects missing external reference before mutation, accepts supplied reference, output and semantic create request. |
| `TestIntegrationAdapterContractTaskEditRequiresExternalReferenceForGatedRepository` | `TestIntegrationWorkflowTaskEditRequiresExternalReferenceForGatedRepository` | Gated edit rejects missing external reference before update mutation. |
| `TestIntegrationAdapterContractAgentContextRendersValidatedWorktreeContext` | `TestIntegrationWorkflowAgentContextRendersValidatedWorktreeContext` | Worktree context rendering, output guidance, no Beads wording, one semantic task read, no list or mutation. |
| `TestIntegrationAdapterContractAgentContextRendersRepoRootFeatureBranchContext` | `TestIntegrationWorkflowAgentContextRendersRepoRootFeatureBranchContext` | Repo-root feature-branch target rendering and PR handoff guidance. |
| `TestIntegrationAdapterContractAgentContextRendersNonInteractiveRunGuidanceAfterProfileChanges` | `TestIntegrationWorkflowAgentContextRendersNonInteractiveRunGuidanceAfterProfileChanges` | Persisted run interactivity wins over changed profile config, non-interactive guidance shown. |
| `TestIntegrationAdapterContractAgentContextTreatsMissingRunInteractivityAsNonInteractive` | `TestIntegrationWorkflowAgentContextTreatsMissingRunInteractivityAsNonInteractive`; existing `internal/taskstate` `TestStoreTreatsMissingImplementationInteractivityAsNonInteractive` | Memory YAML with missing `interactive` decodes as non-interactive and renders non-interactive guidance. Taskstate keeps the owner-level legacy decode/readback assertion. |
| `TestIntegrationAdapterContractAgentContextRendersReviewContext` | `TestIntegrationWorkflowAgentContextRendersReviewContext` | Review context output, latest completion, review step, read-only guidance, no implementation interactivity guidance. |
| `TestIntegrationAdapterContractAgentContextRendersReviewFollowUpCompletionHistory` | `TestIntegrationWorkflowAgentContextRendersReviewFollowUpCompletionHistory` | Original completion versus latest fix completion history, no duplicate latest completion section. |
| `TestIntegrationAdapterContractAgentReviewAddRecordsFindingTypesAndRejectsStaleAttempt` | `TestIntegrationWorkflowAgentReviewAddRecordsFindingTypesAndRejectsStaleAttempt`; `TestResolveExactlyOneTextReadsFilesVerbatimAndValidatesPresence` | Blocking, advisory, and separate-task finding persistence; suggested action; task proposal fields; stale review rejection and no mutation. Exact review file-reader bytes, conflict, required, empty, and read-error cases moved to CLI unit coverage. |
| `TestIntegrationAdapterContractAgentReviewAddRejectsInvalidFindingWithoutWriting` | `TestIntegrationWorkflowAgentReviewAddRejectsInvalidFindingWithoutWriting` | Invalid blocking finding rejects before persistence and leaves findings empty. |
| `TestIntegrationAdapterContractAgentContextFailsBeforeRenderingWhenRunIsStale` | `TestIntegrationWorkflowAgentContextFailsBeforeRenderingWhenRunIsStale` | Stale finished run rejects before rendering and leaves stdout/stderr empty. |


## Review workflows

`internal/review/runner_workflow_test.go` retains real pipeline/store/prompt/terminal behavior for the ten former runner decision scenarios. Their suffixes are unchanged under `TestIntegrationWorkflow`: `RunPipelineInteractivePassingCheckClearsRollingTail`, `RunPipelineInteractiveBlockedCheckLeavesExpandedRollingTail`, `RunPipelinePausesBeforeManualStep`, `RunPipelineHunkManualCommandContinuesWhenSessionMissing`, `RunPipelineGenericManualCommandDoesNotPollHunkNotes`, `RunPipelineHunkManualCommandFailureRemainsOperationalError`, `RunPipelineRestartsBlockedCheckInSameAttempt`, `RunPipelineRestartedBlockerRetainsAuthoritativeNumber`, `RunPipelinePausesAndResumesAutomatedBlockerDecision`, and `RunPipelineRestartFromResumedAutomatedDecisionRerunsStep`.

The other three runner scenarios became direct snapshot/Hunk contracts listed above. Missing-session/exit behavior also has a new direct Hunk contract, rather than relying on a precomputed note list alone.

CLI review scenarios now live in `task_review_interaction_workflow_test.go`. The old prefix is `TestIntegrationAdapterContract`; all replacements below use `TestIntegrationWorkflow`.

| Old suffix | Replacement suffix and assertions |
| --- | --- |
| `TaskRunReviewFollowUpAllowsDirtyMainTarget` | Same suffix. Targeted finding/run provenance, unchanged dirty candidate, reused main/root target, exact session name, semantic launch directory, and no completion invented after the child exits. Process streams belong to agentexec. |
| `TaskReviewRejectsStaleMetadataMirror` | Same suffix. Stale metadata rejects before review creation or output. |
| `TaskReviewRejectsStagedCandidateChanges`, `TaskReviewRejectsMissingCandidateChangesWithoutFinalizationCommit` | `TaskReviewRejectsCandidatePreflightFailuresBeforeStartingReview` table. Propagated candidate rejection, empty output, no review created. Readiness policy and Git observations have separate owners above. |
| `TaskReviewRestoresCandidateChangesMutatedDuringManualStep` | `TaskReviewMarksFailedWhenCandidateChangesMutateDuringManualStep`. Mutation failure, persisted blocking finding, failed attempt, and no finalization. Concrete restoration lives in review's snapshot contracts. |
| `TaskReviewPassingCheckContinuesToManualStep` | Same suffix. Successful check leads into the manual step and persists both outcomes. |
| `TaskReviewConfirmedManualCommandRunsAndRecordsStep` | Same suffix. Confirmation causes the semantic command request and records the step. |
| `TaskReviewImportsHunkBlockingNoteAndBlocksApproval`, `TaskReviewImportsHunkAdvisoryNoteAndAllowsApproval`, `TaskReviewHunkManualCommandWithNoCapturedNotesContinuesPrompt` | `TaskReviewImportsHunkNotesWithSelectedDisposition` table. Imported notes, selected blocking/advisory disposition, no-note continuation, review result, and publication policy. |
| `TaskReviewImportsHunkSeparateTaskNoteAndCreatesFollowUp` | Same suffix. Proposal selection, created task details, and recorded finding/task link. |
| `TaskReviewDeclinedManualCommandAbortsWithoutRunningCommand` | Same suffix. Rejection aborts without command request. |
| `TaskReviewManualCommandEOFConfirmationHandlesUnavailableInput` | Same suffix. EOF/unavailable input decisions and no unintended command execution. |
| `TaskReviewNonZeroCheckRecordsBlockingFindingAndStops` | Same suffix. Nonzero semantic command outcome records blocker and prevents finalization. |
| `TaskReviewAgentReviewStepCapturesCodexUsage`, `TaskReviewAgentReviewStepCapturesPiUsage` | `TaskReviewAgentReviewStepCapturesUsage` table. Invocation capture dependency reaches real pipeline; persisted harness/model/session/tokens/cost/status and capture reason survive finalization. Agent units retain session decoding. |

## Beads, evaluation, and coverage tooling

| Area | Old assertion | New owner/assertion |
| --- | --- | --- |
| Beads parent cycle | `UpdateServiceRejectsRealBeadsParentDescendantCycle` rejected a cycle through `task.UpdateService`. | Removed from Beads act path. Task owns cycle rejection in `TestUpdateServiceRejectsParentDescendantCyclesBeforeMutation`. Beads keeps direct parent relation readback in `TaskBackendReadsParentRelationship`. |
| Cross-type dependencies | `UpdateServiceSupportsCrossTypeBlockingDependencies` accepted task-to-epic and epic-to-task blocking edges. | `TaskBackendSupportsCrossTypeBlockingDependencies` calls `TaskBackend.Update` directly and reads back dependency IDs. |
| Related edge preservation | `UpdateServiceDoesNotRemoveRelatedDependency` preserved a Beads `related` edge. | `TaskBackendDoesNotRemoveRelatedDependency` calls `TaskBackend.Update` directly and verifies the concrete `related` edge with `bd dep list`. |
| Rejection before mutation | `UpdateServiceRejectsNonBlockingDependencyBeforeContentMutation` rejected a blocking add over an existing non-blocking edge before changing content. | `TaskBackendRejectsNonBlockingDependencyBeforeContentMutation` calls `TaskBackend.Update` directly and verifies the title is unchanged. |
| Create dependencies | Direct create/readback of blocking dependencies. | Retained as `TaskBackendCreateRecordsBlockingDependencies`. |
| Evaluation provisioning failure | Full setup plus fake `bd` process reported unknown usage/cost when auth provisioning failed. | `TestIntegrationWorkflowEvalReviewContextReportsUnknownExecutionWhenEnvironmentFails` enters the CLI with semantic provisioning failure and verifies diagnostics, no launch, and unknown usage/cost. |
| Evaluation isolated Beads destination | Full setup plus fake `bd` process checked `.beads` files. | `TestIntegrationWorkflowEvalReviewContextKeepsOperatorBeadsEnvOutOfSeededRepo` verifies preparation requests the isolated repo path and leaves operator Beads env/data untouched. |
| Evaluation session-name prompt argv | Fake Codex executable through the full setup recorded prompt argv. | `TestIntegrationWorkflowEvalReviewContextPersistsExecutionSessionArgsAndPromptEnv` runs real `review.RunPipeline` with semantic candidate and launcher, then verifies stored session name, stored argv, launcher cwd, and prompt env. |
| Nested coverage fixture label | Nested consumer test used the adapter-contract prefix. | Renamed to untagged `TestConsumerCreditsCollaborator`; the outer Go-tool contract selects it exactly inside its nested fixture module. |


`internal/cli/eval_workflow_test.go` enters the evaluation command with semantic Beads/Git/task, environment, usage, and launch effects. It retains real evaluation/reporting and pipeline composition. Temporary disk stores the isolated evaluation run and report paths. `TestWithRunEnvironmentRejectsRelativeCodexHomeBeforeExecution` preserves concrete invalid-config rejection in a Go-only owner unit, alongside existing auth/config copy and session-isolation units.

The only CLI adapter-labelled declaration is `TestIntegrationAdapterContractCLIRepositoryFixtureCreatesIndependentOrigins`. It checks test infrastructure still used by `TestIntegrationBinaryE2ETaskRunUsesSeparateTaskProposalSelection`, not a CLI product contract. Orphaned fake Beads/agent/Hunk/session helpers were deleted. Remaining executable helpers are named `binary_*_fixture_integration_test.go` and support the recursive binary scenario.

## Quality policy review before CLI-entry consolidation

The policy refresh uses five complete unit-then-integration samples, Go's normal package concurrency, and `-parallel=1`, with no concurrent implementation agents or other test runs. The two lanes, category selection, policy schema, and CI behavior are unchanged.

Integration-only coverage decreases are intentional ownership changes: CLI no longer decodes agent logs or Beads output, invokes task-service policy through a Beads contract, or performs full evaluation provisioning. Memory stores remove incidental OS-storage coverage. Owner units and direct adapter contracts listed above retain those assertions. Unit coverage is 54.72%; integration coverage is 63.89% after the direct Hunk error cases. Coverage floors changed only for the repository and affected agent/Beads/evaluation/state/task packages. The four faster doctor/review/evaluation/workflow package timing bounds tightened. Agentexec's measured process timing required a higher bound. Transient unit timing overages did not survive the five-sample refresh and were not accepted into policy. A later Git timing overage prompted another five complete samples on the final source. That batch passed without changing the Git ceiling or any other bound.

## Validation before CLI-entry consolidation

- `make check` passed for the ownership repair before the naming-only follow-up: formatting, both measured lanes, lint, and CLI build. Quality decision was `pass`.
- `go vet ./...` and `go vet -tags=integration ./...` passed.
- `GOTOOLCHAIN=go1.26.3 golangci-lint run --build-tags=integration ./...` passed.
- `git diff --check` passed.
- Focused CLI, doctor, review, evaluation, Beads, agent, coverage-tool, and readiness-policy checks were run during implementation. Early fixture/compile errors were corrected before complete validation. The first complete check caught two direct `TempDir` calls in the new file-reader units; both now use `testutil.CanonicalTempDir`.
- Two five-sample policy runs completed. The first updated the reviewed 11 bounds; the second checked the final source and left policy unchanged. No live evaluation was run.

Final quality coverage: unit 54.72%, integration 63.89%. Selected package work was 3.69s and 51.71s; command wall time was 4.56s and 28.69s. Wall time is not a policy input. Local validation reports are under `artifacts/test-coverage/ownership/` and the current quality report is `artifacts/test-coverage/report.json`.


## Adapter filename follow-up

All 72 adapter scenarios now reside in `*_adapter_test.go` files. Test names, build tags, assertions, and measured lane selection are unchanged. The CLI repository-fixture self-check was extracted from `helpers_test.go` into `repository_fixture_adapter_test.go`; its helpers remain shared with binary E2E. Review's private-adapter tests keep a documented same-package exception.

For this naming-only change, both lanes passed and coverage was unchanged. `make check` stopped at timing-budget violations in the unit suite, agent/taskstate units, and workflow integration. No timing relaxation was made for a filename change. Both vet configurations, integration-tag lint, separate `make lint build`, and `git diff --check` passed.
