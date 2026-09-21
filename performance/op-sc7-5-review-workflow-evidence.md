# Review and repair workflow migration

## Scope

The historical migration moved 37 scenarios from the process-heavy task fixture to the external `cli_test` package's `review_*_workflow_test.go` files. Their current names carry the `TestIntegrationWorkflow...` prefix. Process-backed task and review scenarios remain in adapter-contract files; later curation moved boundary-free command registration, flag validation, guidance, and completion rendering into workflow files. Unused shell-agent, delayed-input, config, and completion helpers were removed.

These journeys use the real root command, routing, config loading, workflow,
review pipeline, finding recorder, task source interfaces and task-state store.
They do not supply a passed, blocked or paused pipeline result. Reviewer outcomes
call the real `agent review add` command in process. Implementation and repair
outcomes call `agent context` and `agent done` through the public CLI constructor.

The shared application fixture uses memory-backed registry, config and task
state. Its collaborators hold candidate contents, commits, pushes, created tasks,
task metadata, PR identity and agent outcomes. The repair check reads candidate
contents, so a successful repair changes the check result. Unexpected commands,
agent purposes or exhausted outcome queues fail. Unused scripted outcomes fail
cleanup. Every fixture sets `PATH=/nonexistent`, supplies an isolated invocation
environment, and verifies that its `/fixture/...` roots remain absent on disk.
The exec trace below independently checks for child executables.

The production additions are external-effect inputs, not replacement workflow
implementations:

- `review.Effects` supplies candidate capture/check/restore and check-command
  execution. The default still uses the existing Git snapshot and `os/exec`
  adapters. A command exit error supports `ExitCode() int`, including the real
  `exec.ExitError`. Start failures still have no exit code.
- CLI and workflow accept a review Git-status reader and the existing
  finalization Git interface. CLI also accepts the existing PR provider
  interface. Finalization still runs through the real service.
- Repair execution uses the invocation's existing usage-capture dependency,
  just as initial implementation execution does.

The design HTML named in the task was absent from this checkout. This migration
follows `docs/testing.md` and the established application fixture.

## Assertion map

Every name below starts with `TestIntegration`. Each row maps the removed
infrastructure-heavy body to the same-named memory-backed body. Unless noted,
output checks, finding fields, review/run history, and failure branches remain.
Git-file or fake-command-log assertions now inspect the supplied semantic state;
actual process and Git behavior has separate owners below.

| Scenario suffix | Preserved assertions and replacement for infrastructure observations |
| --- | --- |
| `TaskReviewApproveFinalizesAndRecordsPassedAttempt` | Invalid action reprompts; completion and technical explanation render; status and manual gate render; approval passes with no findings and records a commit. Also checks closed task, closure call and default-branch push. |
| `TaskReviewManualContextShowsOriginalAndLatestFollowUpCompletion` | Original and latest repair descriptions render, intermediate repair does not; publication commit uses original completion text. Commit message comes from the candidate fake rather than Git log. |
| `TaskReviewBlockingFindingBlocksWithoutFinalizing` | Blocking, advisory and separate-task finding order/types; blocked action menu; blocked review; unchanged HEAD and no finalization. |
| `TaskReviewAdvisoryAndSeparateTaskFindingsDoNotBlockApproval` | Nonblocking notes allow approval; declining task creation leaves created-task ID and timestamp absent. |
| `TaskReviewCreatesSelectedSeparateTaskFollowUp` | Selected proposal creates op-41 and timestamps its finding; exact title, description, provenance, acceptance criteria and issue type now use `task.CreateOptions`; created task is observable as open. |
| `TaskReviewCanAbortWhenSeparateTaskCreationFails` | Database error offers continuation; declining aborts; no finding linkage, task creation, commit, push or closure. |
| `TaskReviewAbortDoesNotFinalize` | Abort output/status; unchanged HEAD and no finalization. |
| `TaskReviewManualInputLossReplaysRecordedFindings` | Partial input persists advisory and blocker; next invocation replays both, resumes the same attempt, and blocks without duplicating findings or finalizing. |
| `TaskReviewInvalidReviewAgentConfigDoesNotStartFreshAttempt` | Invalid reviewer config fails before creating a review. |
| `TaskReviewInvalidReviewAgentConfigDoesNotResumeManualAttempt` | Invalid config leaves the paused attempt, status and step unchanged. |
| `TaskReviewCheckBlockerReasonEOFRecordsInterrupted` | Separate downgrade/waive cases lose input before a reason; finding stays blocking with neither reason; interrupted rather than kept; no finalization. |
| `TaskReviewCheckBlockerKeepAcceptsEOFAnswer` | Keep without trailing newline is accepted; budget is exhausted without interruption or publication. |
| `TaskReviewCheckBlockerReasonAcceptsEOFAnswer` | Downgrade and waiver reasons without trailing newline persist and permit publication, with distinct finding types/resolution fields. |
| `TaskReviewCheckBlockerDowngradeContinuesPipeline` | Downgrade reason and advisory type persist; manual step runs and approval finalizes. |
| `TaskReviewCheckBlockerWaiverContinuesPipeline` | Cancel alias records waiver while preserving blocking type; pipeline passes. |
| `TaskReviewCheckStartFailureMarksOperationalFailure` | Explicit supplied start failure produces a failed review, recorded step with nil exit code, and no finding. Real missing-executable behavior stays in process contracts. |
| `TaskRunAfterInterruptedAutomatedBlockerDecisionRequiresFreshReview` | Interrupted classification cannot launch a repair; fresh disposition input loss leaves the blocker untargeted and preserves one implementation run. |
| `TaskRunRecoversHardStoppedAutomatedBlockerDecision` | Persisted unfinished decision is recovered before any agent launch; keep sets kept, clears interruption and leaves the finding untargeted. |
| `TaskReviewInterruptedAutomatedBlockerRecoveryReusesRecordedPipeline` | Recovery reuses strict rather than repository-default pipeline; manual address reason precedes rerun; downgrade persists in a second strict review. The default check is unscripted and fails if invoked. |
| `TaskReviewResumesManualWaitingAttempt` | Resume uses the same attempt and manual step; completed check is not rerun. An unexpected check fails instead of relying only on missing stdout. |
| `TaskReviewRejectsConflictingPipelineForManualWaitingAttempt` | Conflicting override errors without replacing paused pipeline/step/status. |
| `TaskReviewPipelineOverridePrecedence` | CLI selection wins over repository/global defaults, runs only its check and persists its pipeline. |
| `TaskReviewPipelineAliasResolvesToGlobalPipeline` | Repository alias resolves to the named global pipeline; wrong pipelines cannot run. |
| `TaskReviewUnknownPipelineIncludesRepoAliases` | Error lists configured pipeline and alias. |
| `TaskReviewAgentReviewStepLaunchesReviewerAndPassesWithoutFindings` | Reviewer directory, environment, prompt, argv, session and execution facts; passed empty review and finalization. Child PID is explicitly asserted. Millisecond duration may be zero for an in-process fake; real elapsed-time behavior remains in process tests. |
| `TaskReviewAgentReviewBlockingFindingStopsPipeline` | Real `agent review add` persists and numbers the blocker; manual gate does not run; HEAD and finalization remain unchanged. |
| `TaskReviewAgentReviewMixedAutomatedBlockerDecisions` | Three findings receive keep/downgrade/waive decisions; `task show review` renders each resolution; subsequent repair targets only the kept blocker. Incomplete repair output replaces shell stdout/stderr checks. |
| `TaskReviewPromotesAgentReviewAdvisoryAndTargetsFollowUp` | Advisory details and menus, selective promotion, EOF pause, replay, manual advisory, budget guidance, and repair targeting only the promoted finding. In-memory input replaces the temporary input file. |
| `TaskReviewAgentReviewNonZeroExitMarksOperationalFailure` | Reviewer failure propagates and records failed review/step without findings or finalization. Stream forwarding belongs to the launcher contract. |
| `TaskRunAutonomousReviewFollowUpRepairsCheckAndPublishes` | Initial completion fails check; kept blocker launches repair; changed candidate passes second review; two noninteractive runs, finding targeting, no exhausted budget, commit and task PR metadata. |
| `TaskRunAttachedManualBlockerRepairsAndApprovalFinalizes` | Same-command implementation, manual blocker, targeted repair, approval and PR publication; headers, finding/run provenance and absence of premature handoff diagnostics. No delayed shell marker is needed because the semantic agent does not consume operator input. |
| `TaskReviewManualBlockerExhaustsBudgetWithoutExtraLaunch` | Two manual blockers exhaust budget after one repair; correct headers/targeting; no third run and final blocker stays untargeted. |
| `TaskRunPreservedManualBlockerExhaustsFreshBudget` | Existing blocker consumes the one-attempt budget; no agent launch, extra run or finding target. |
| `TaskRunAutonomousReviewLoopExhaustsPersistentCheckBlockers` | Persistent candidate failure consumes two reviews/runs; final blocker stays open and untargeted; no commit; both review and task views show exhausted-budget guidance. |
| `TaskReviewResumedAutonomousFollowUpPreservesSelectedImplementer` | Initial selected implementer pauses at manual gate; changing default does not change repair implementer; repair changes candidate, second review passes and publishes. |
| `TaskRunResumesPausedAutomatedBlockerDecision` | Pause, restart, pause, waive across three invocations retain attempt identity and one finding; only blocked step reruns. Semantic execution history replaces marker-file counts. |
| `TaskReviewFollowUpHeaderWriteFailureRecordsStartFailure` | Header writer failure prevents launch, records failed start event/error and preserves blocker eligibility for retry. |

Additional application coverage:

- `TestIntegrationWorkflowReviewFailedRepairCanRetryWithoutLosingBlocker` runs runtime
  failure, start failure and successful-exit-without-completion cases through a
  later successful retry. It preserves the failed/incomplete run, blocker
  eligibility, third-run targeting and passed second review.
- `TestIntegrationWorkflowReviewPrimaryProcessRecoveryKeepsFindingsForAudit` supplies
  absent, live and unknown process facts through CLI recovery. It checks
  unchanged running state where recovery is unsafe, failed interrupted state
  where both processes are absent, audit-only findings, actionable output,
  no repair launch and no publication. Invalid replacement configuration does
  not prevent recovery.
- `TestIntegrationWorkflowReviewMutationFailsBeforePublication` changes the candidate
  during the semantic reviewer. The real pipeline invokes the supplied snapshot
  check, fails review, restores contents and prevents publication.

## Retained process and Git contracts

No migrated journey starts a model agent or contacts a provider. `TestIntegrationWorkflowRunPipelineAgentReviewUsesEffectivePromptInCommandAndEnvironment` checks the options passed to a semantic launcher, and `TestIntegrationWorkflowRunPipelinePersistsPrimaryReviewerProcessFacts` checks persistence from a fabricated PID callback; neither claims a child-process boundary. The concrete contracts are listed below.

| Boundary | Owner |
| --- | --- |
| Live streaming, cancellation and reaping the direct child | New `TestIntegrationAdapterContractAttachedLauncherStreamsBeforeExitAndReapsCanceledChild` cancels on the first stdout write from a controlled busy-loop shell. The context must report `context.Canceled`, not the five-second safety deadline, and the reported PID must be absent after `Run`. No sleeps or repository fixtures. |
| stdout/stderr, exit status, argv and cwd | `TestIntegrationAdapterContractAttachedLauncherForwardsWorkingDirectoryArgumentsAndStreams` |
| Invocation environment and direct PID callback | `TestIntegrationAdapterContractAttachedLauncherUsesConfiguredEnvironment`, `TestIntegrationAdapterContractAttachedLauncherReportsDirectChildPIDBeforeWait` |
| Missing executable versus started process | `TestIntegrationAdapterContractAttachedLauncherMissingExecutableDoesNotReportProcessStarted` |
| Review command and Hunk environment | `TestIntegrationAdapterContractReviewCommandUsesScopedEnvironment`, `TestIntegrationAdapterContractHunkNotesUseScopedEnvironment` |
| Review stream display and rolling tails | `TestIntegrationWorkflowRunPipelineInteractivePassingCheckClearsRollingTail` and `TestIntegrationWorkflowRunPipelineInteractiveBlockedCheckLeavesExpandedRollingTail` run controlled commands. Fake-launcher display behavior lives in `TestIntegrationWorkflowRunPipelineInteractiveAgentReviewOutputDependsOnProfileMode`, `TestIntegrationWorkflowRunPipelineInteractiveAgentReviewNonBlockingFindingLeavesLiveTail`, `TestIntegrationWorkflowRunPipelineInteractivePassingAgentReviewClearsWrappedRollingTail`, and the focused rolling-tail workflows. |
| Snapshot capture, mutation detection and restoration | `TestIntegrationWorkflowTaskReviewMarksFailedWhenCandidateChangesMutateDuringManualStep`, `TestIntegrationAdapterContractCandidateSnapshotRestoresTrackedAndUntrackedMutations`, `TestIntegrationAdapterContractCandidateSnapshotRestoresRedirectedStderrFile`, and `TestIntegrationAdapterContractCandidateGitOperationsCaptureAndRestoreTrackedDiff` |
| Staged/missing candidate and stale metadata rejection | Retained `TaskReviewRejectsStagedCandidateChanges`, `TaskReviewRejectsMissingCandidateChangesWithoutFinalizationCommit`, `TaskReviewRejectsStaleMetadataMirror` integration tests |
| Hunk polling/import and confirmed/manual-command process protocol | Retained Hunk and manual-command scenarios in `task_adapter_contract_test.go` and `internal/review` |
| Compiled command packaging with recursive agent commands | `TestIntegrationBinaryE2ETaskRunUsesSeparateTaskProposalSelection` |
| Codex/Pi session usage capture | Retained `TaskReviewAgentReviewStepCapturesCodexUsage` and `TaskReviewAgentReviewStepCapturesPiUsage` integration tests |
| Task adapter translation | `TestTaskBackendCreateCreatesStandaloneTask`, `TestTaskBackendCreatePassesGraphAndOptionalFields`, `TestTaskBackendSetPRURLWritesMetadata`, `TestTaskBackendCloseClosesOpenTask`; real Beads creation remains in `TestIntegrationAdapterContractBeadsRelationshipContracts/TaskBackendCreateRecordsBlockingDependencies` |

## Measurements

The checked-in `op-sc7-5-review-workflow-timing.json` records the 37 scenario
names, three wall-time samples and exec counts. Both binaries use the same Go
installation and host. Each was compiled once, then run with the same exact-name
filter, `-test.count=1 -test.parallel=1`. Compilation is excluded. Serial execution
makes the old parallel tests and new invocation-scoped fixtures comparable; these
numbers are not whole-suite policy measurements.

| Measurement | Before | After |
| --- | ---: | ---: |
| Wall time sample 1 | 5.869s | 0.726s |
| Wall time sample 2 | 5.721s | 0.791s |
| Wall time sample 3 | 5.792s | 0.742s |
| Median | 5.792s | 0.742s |
| Distinct descendant PIDs observed executing programs | 1,735 | 0 |
| Descendant PIDs that executed Git | 902 | 0 |
| Other controlled helper PIDs | 833 | 0 |
| Real Beads, gh or model-agent executable PIDs | 0 | 0 |

Median elapsed time fell by 87.2%. The separate Linux ptrace trace follows fork,
vfork, clone and exec events from the compiled test binary. Counts exclude the
root test process and Go build activity. A PID that executed Git belongs only to
the Git row. Fork-only children are not counted. Before-migration exec paths were
Git, Bash, cat, touch and the controlled CLI test helper. The after trace contained
only the root test binary. Raw local output is under
`artifacts/test-coverage/review-migration/`.

The three additional workflow tests and the new cancellation contract were not
included in this before/after comparison.

## Validation and policy

Initial `make check` and `make quality` passed both functional lanes but reported
coverage bounds requiring an intentional refresh. Five complete samples from
`make quality-policy-update` passed with stable test counts and changed only:

- Integration repository floor, 65.218% to 65.757%.
- Agent execution integration floor, 75.193% to 80.456%, after adding the
  cancellation/reaping contract.
- Beads integration floor, 66.724% to 64.667%. Measured coverage is 66.667%.
  Removing CLI shell fixtures removes incidental Beads adapter execution from
  this lane. Creation, PR metadata and closure translation retain the focused
  package-owned unit contracts listed above. The updater restores its configured
  two-percentage-point package headroom.

No unit coverage floor or timing ceiling changed. Final `make check` and a separate `make quality` both passed. The latter ran
1,100 unit test events and 505 integration test events, with a 55.12s integration
selected-test total. Targeted migrated journeys, three race-test
repetitions, five cancellation-test repetitions, both vet configurations and
integration-tagged lint passed.
