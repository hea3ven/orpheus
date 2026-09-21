# Task workflow migration evidence

## What these journeys exercise

The reference journey is `TestIntegrationWorkflowTaskRunCompletionLeavesTaskAwaitingManualReview` in `internal/cli/task_run_workflow_test.go`. An open task runs, its agent records completion, execution succeeds, and the task remains in progress awaiting manual review. The real `status` command reports `Reviewing`. Completion neither closes the task nor publishes a PR.

Command routing, task eligibility, profile selection and interpolation, workflow orchestration, agent context/completion, registry storage, task-state storage, and status composition are real application paths. Orpheus config and data use memory-backed `state.Paths`. Each task is read once per verification checkpoint through `fixture.loadFinalTask`, which reads `taskstate.Store` and the task backend. Assertions consume the returned state and task instead of reloading either store. Retry has a checkpoint after each invocation; the repair journey separately captures the task-ID inventory to check for unrelated task creation. No assertion reads raw YAML files on disk.

External collaborators have deliberately limited scope:

- The task backend stores tasks and relationships. Its supported mutation, `MarkInProgress`, accepts open tasks and matching in-progress retries, and rejects publication, closed tasks, and conflicting targets. `UpdateGitFacts`, `Create`, `Close`, and `SetPRURL` are unsupported and return errors. This is a local dispatch stub, not a general Beads implementation.
- Git tracks created/reused worktree targets. Candidate changes are an explicit scenario input. Staging and committing return errors. It does not simulate Git object storage, index state, or snapshot restoration.
- The launcher consumes explicit completion, failure, or successful-exit-without-completion outcomes. Unexpected launches fail, and fixture cleanup checks for unused outcomes. Completion invokes real in-process `agent context` and `agent done` commands.
- Process liveness is supplied only where recovery needs it. An unexpected probe fails.
- Review outcomes are supplied. The stub validates the expected pipeline name and step kind/name before recording a manual wait or a kept blocker. The surrounding review lifecycle and targeted repair dispatch are real. These journeys do **not** prove that the review pipeline produces those outcomes.

The repository and task-run memory fixtures live in the external `cli_test` package under `internal/cli`. They construct fresh commands through the production `cli.NewRootCommandWithOptions` API, including the scripted agent's `agent context` and `agent done` invocations. The public options expose invocation inputs and external collaborators, not private root options or assembled stores. The CLI constructs its real registry/task-state stores; fixtures seed and inspect the same storage through those packages' exported APIs. The Go package boundary prevents these workflows from reading or mutating private CLI dependencies. The later repo and initial-dispatch migration is recorded below.

The repo and task fixtures embed a shared `workflowFixture` for memory paths, isolated command options, registry access, command execution, and cleanup guards. Task/repo-specific collaborators remain separate. `TestIntegrationWorkflowRepoAddWithoutLocalBeadsRegistersManagedRepoAndListsIt` uses `aGitRepository`, an explicit `withoutLocalBeads` setup, and visible `repo add` / `repo list` commands. It loads the final registry once and compares it with an expected managed registration built from the supplied repository details. Output assertions consume captured text; Git/Beads inspections and initialization are captured as argument lists so equality checks cover both call counts and arguments. Registration retains a real temporary repository directory for production path validation; Orpheus state and Beads initialization remain in memory or supplied collaborators.

All five task-run journeys and their focused contracts use Testify assertions. Named builders describe open tasks, in-progress epics, parent relationships, completion payloads, and blockers without repeating incidental fields. Agent failure, exit without completion, absent processes, and supplied review outcomes have explicit setup helpers. Exact profile arguments and conflicting metadata stay visible in the contracts that exercise those values. Each scenario shows its CLI invocation with `fixture.execute(...)`, including both run attempts in the retry journey and the real `status` invocation in the completion journey. Output assertion helpers do not execute commands.

`assertIsTaskInAgentContext` verifies both the requested ID and the original seeded title. The fixture keeps a separate copy of seeded tasks so this expectation cannot change with backend mutations. Completion payload comparisons check every authored field. Manual review, publication, launch target, and real status checks have focused names rather than a single catch-all workflow assertion. Retry history, interruption reason and process facts, repair provenance, and child PID 4243 remain visible in their scenario bodies.

Temporary Go overlays corrupting the rendered task ID and title each made the context assertion fail. After extending the style to the remaining journeys, overlays dropping prior run history and removing the repair callback also failed the corresponding assertions. Production code was unchanged by these checks. That initial readability pass did not migrate additional infrastructure-heavy test families.

Memory storage is integration-test support, not a production storage option. `state.NewMemoryPaths` and its private backend live in `internal/state/memory_integration.go`, compiled only with the `integration` build tag. Keeping them in `state` lets fixtures reuse the private storage boundary without exporting an injection API. The repository/task-run workflow fixtures and CLI constructor contracts use this constructor. The OS/memory conformance suite and standalone config-seeding tests were removed. Path validation, file inventory, YAML persistence, and locking retain OS-backed unit coverage.

Configuration seeding does not expand the production `Paths` API. The fixture-only `Paths.WriteConfigYAML` method was removed. CLI fixtures use `state.SeedMemoryConfigYAML`, compiled only with the `integration` build tag and restricted to memory-backed paths. Package-owned unit fixtures retain an unexported helper in a `_test.go` file. Real config parsing and profile resolution remain unchanged.

`Paths.ListDataFiles` remains a production operation. The real status command calls `taskstate.Store.TaskIDs` through `internal/cli/status.go`, and `TaskIDs` uses this inventory method for both OS and memory storage. Removing that path would reintroduce the memory-backed status gap; it is not a test-only export.

## Timing and executed-process counts

The original measurements were recorded on 2026-09-09. The revised journey was measured on 2026-09-10. Historical measurements below were not rerun during this follow-up. These are single warm-cache samples, not policy limits or a controlled statistical benchmark.

| Measurement | Original local-Git journey, historical | Initial memory journey, historical | Revised completion journey, fresh |
| --- | ---: | ---: | ---: |
| Package elapsed time reported by selected `go test` | 0.317s | 0.054s | 0.055s |
| Warm command wall time | 0.889s | 0.620s | 0.654s |
| Descendant PIDs observed executing programs after test binary startup | 73 | 0 | 0 |
| Those PIDs that executed real Git | 41 | 0 | 0 |
| Other controlled fake/helper executable PIDs | 32 | 0 | 0 |
| Real Beads executable PIDs | 0 | 0 | 0 |
| `gh` executable PIDs | 0 | 0 | 0 |
| Model-agent executable PIDs | 0 | 0 | 0 |

Historical test names were `TestIntegrationWorktreeCompletionFlowEndToEnd` at `beafe2ca4e34` and `TestIntegrationTaskRunCompletesThroughMemoryBackedApplication` in the initial working tree. The latter was renamed and rewritten in this follow-up.

The temporary ptrace tracer `/tmp/orpheus-exectrace` follows descendants and logs `PTRACE_EVENT_EXEC` events. Counts exclude Go compiler, linker, vet, and runner activity before `cli.test` starts. A PID that eventually executed Git is counted in the Git row, rather than again in the helper row. The earlier document called this a count of all descendant processes. More precisely, it counts distinct PIDs observed executing programs; it does not count fork-only children that never exec.

The fresh trace showed the `cli.test` executable followed by the successful test result, with no later exec events. The trace and timing were separate runs:

```sh
TIMEFORMAT='wall_seconds=%3R'
time go test -tags=integration ./internal/cli \
  -run '^TestIntegrationWorkflowTaskRunCompletionLeavesTaskAwaitingManualReview$' -count=1
/tmp/orpheus-exectrace go test -tags=integration ./internal/cli \
  -run '^TestIntegrationWorkflowTaskRunCompletionLeavesTaskAwaitingManualReview$' -count=1
```

The five revised journeys together reported 0.107s in a separate uninstrumented sample. A five-repeat race run also passed. The first attempt to measure wall time used unavailable `/usr/bin/time`; the recorded sample above uses Bash's `time` keyword instead.

Every fixture sets `PATH=/nonexistent` and checks that its `/fixture/...` config, data, and repository roots do not exist on disk. Those checks guard against accidental use of the fixture paths. Neither alone proves that an absolute-path executable was not launched or that unrelated disk access never occurred. The exec trace provides the separate executable evidence for the reference journey.

## Assertion map

Names below are full Go test names. All application journeys and launch contracts retain the integration build tag and `TestIntegration` prefix.

### Removed `TestIntegrationWorktreeCompletionFlowEndToEnd`

| Previous behavior/assertion | Current owner and boundary |
| --- | --- |
| CLI dispatch selects an eligible task and deterministic worktree | Real `task run` in `TestIntegrationWorkflowTaskRunCompletionLeavesTaskAwaitingManualReview`; exact persisted target and launch directory in `TestIntegrationWorkflowTaskRunConfiguredDefaultPreservesLaunchContextAndAuditFacts`. Real branch/worktree mutation remains in `TestIntegrationAdapterContractSetupTaskWorktreeCreatesAndReusesDeterministicWorktree`. |
| Bootstrap prompt reaches argv/environment without embedding task context | Real profile resolution and launch capture in `TestIntegrationWorkflowTaskRunConfiguredDefaultPreservesLaunchContextAndAuditFacts`; content exclusions remain in `TestRenderBootstrapPromptTellsAgentToFetchContext`. |
| Agent context includes selected task, branch, work directory, and execution contract | Real `agent context` in the configured-default contract. Detailed publication and one-time-completion wording remains in `TestRenderActiveContextIncludesWorktreeContract`. |
| Completion fields, timestamp, succeeded execution, no completion commit | Real `agent done` and semantic-store assertions in the completion journey; execution metadata and exact audit ordering in the configured-default contract. `TestCompletionServiceCompletesWorktreeRunWithoutCommit` retains focused service rules. |
| Worktree file remains uncommitted on disk | `TestIntegrationWorkflowAgentDoneCommitsWorktreeCompletion` checks the semantic candidate-change signal and empty completion commit/error; it does not claim a real Git boundary. `TestIntegrationAdapterContractCandidateGitOperationsCaptureAndRestoreTrackedDiff` owns real working-tree and index capture and restoration. |
| Manual review pauses and prints resume guidance | The memory journey **supplies** the manual-wait outcome, then verifies lifecycle persistence and real status composition. `TestIntegrationWorkflowRunPipelinePausesBeforeManualStep` owns real pipeline pausing. `TestIntegrationWorkflowWorktreeLocalReviewTaskDonePRFlowEndToEnd` retains the real CLI manual-wait diagnostic. |
| Real status command reports `Reviewing` and next action | `TestIntegrationWorkflowTaskRunCompletionLeavesTaskAwaitingManualReview` executes real `status --no-truncate`. The manual projection/rendering helper was removed. `TestStoreTaskIDsDiscoversOnlyDirectValidYAMLFilesInSortedOrder` covers inventory on disk; the workflow exercises memory inventory through the real status command. |
| Backend stays in progress, no close or PR publication | Explicit task status, absent PR metadata, and absent finalization assertions in the completion journey. Unsupported backend publication/closure operations fail rather than silently succeeding. |
| Controlled agent output and recursive executable packaging | `TestIntegrationAdapterContractAttachedLauncherForwardsWorkingDirectoryArgumentsAndStreams` owns process streams. `TestIntegrationBinaryE2ETaskRunUsesSeparateTaskProposalSelection` compiles `cmd/orpheus`, launches controlled implementer and reviewer children, and verifies their recursive calls into the CLI. The old test-binary helper entrypoint was removed because no scenario used it. |

### Removed `TestIntegrationTaskRunExecutesImplementerDefaultAttachedFromDeterministicWorktree`

| Previous behavior/assertion | Current owner and boundary |
| --- | --- |
| Configured implementer default, interactive flag, interpolated session/prompt, literal argv | `TestIntegrationWorkflowTaskRunConfiguredDefaultPreservesLaunchContextAndAuditFacts` seeds memory configuration with a selected default and a decoy profile. Production config loading and command resolution stay real. The fixture no longer supplies command snapshots. |
| Launch cwd, argv, ORPHEUS environment, persisted profile/command/session/target, process facts | The same configured-default application contract checks those values explicitly. `TestIntegrationAdapterContractAttachedLauncherForwardsWorkingDirectoryArgumentsAndStreams`, `TestIntegrationAdapterContractAttachedLauncherUsesConfiguredEnvironment`, and `TestIntegrationAdapterContractAttachedLauncherReportsDirectChildPIDBeforeWait` retain OS launch semantics. |
| Successful exit without completion, second normal invocation, attempts 1 and 2, worktree reuse, retained history | `TestIntegrationWorkflowTaskRunSuccessfulExitWithoutCompletionAllowsOrdinaryRetry` enters the normal CLI route twice and asserts no completion or review, unchanged first attempt, reused target, and the created/start/finish/reused/start/finish audit sequence. Interrupted recovery is not used as a substitute. |
| Beads read/list/update command translation | The journey reads semantic tasks. Retained `TestTaskBackendListParsesVisibleTasksAndMetadata`, `TestTaskBackendListExcludesUnsupportedTypesAndPreservesRelations`, and `TestTaskBackendMarkInProgressUpdatesOpenTaskStatusAndMetadata` own adapter translation. The memory backend's dispatch rules are checked against the same accepted/conflicting state cases in `TestIntegrationWorkflowDispatchStubRejectsConflictingTaskStateWithoutMutation`; it is not advertised as a complete backend contract. |
| Obsolete diagnostic strings are absent | The old negative checks for `Orpheus M3 WIP` and `running attached agent` were intentionally removed. They describe retired wording rather than a current workflow decision. Current implementation headers and error output remain asserted. |

### Removed `TestIntegrationTaskRunRecordsFailedAttemptWhenAgentExitsNonZero`

| Previous behavior/assertion | Current owner and boundary |
| --- | --- |
| CLI propagates agent failure, records failed attempt and finish time | `TestIntegrationWorkflowTaskRunAgentFailureRecordsFailedAttempt` supplies a launcher error, checks propagation, run/execution failure, timestamp, and setup/start/failed-finish events through the real CLI/workflow. It also asserts no completion, review, publication, or task closure. |
| Real child exits 7 and both streams reach the caller | `TestIntegrationAdapterContractAttachedLauncherForwardsWorkingDirectoryArgumentsAndStreams` executes a controlled child and asserts its exit error and stdout/stderr. The memory journey's injected error is not process-supervision coverage. |

## Separate recovery and repair stories

- `TestIntegrationWorkflowTaskRunAbsentProcessesInterruptPreviousAttemptBeforeRetry` seeds a running attempt with supervisor 100 and child 101. Both are explicitly absent. It checks interruption reason/trigger, preservation of the old attempt, worktree reuse, and a successful replacement attempt. The replacement deliberately exits without completion so recovery is not mixed with another completion story.
- `TestIntegrationWorkflowTaskRunBlockingReviewDispatchesTargetedRepair` supplies a named blocking finding and kept decision, then exercises real autonomous repair dispatch and follow-up profile resolution. It checks review-attempt/finding provenance, repair context/session, persisted repair child PID 4243, reused target, second review, and no unrelated task creation or publication. The PID assertion covers the autonomous-repair call site's `OnStart` wiring; `TestIntegrationAdapterContractAttachedLauncherReportsDirectChildPIDBeforeWait` separately covers the process adapter invoking a supplied callback. A temporary Go overlay removing only the repair callback made the journey fail with child PID 0 instead of 4243. The unmodified journey and focused launcher contract both passed. `TestIntegrationWorkflowRunPipelinePausesAndResumesAutomatedBlockerDecision` retains real automated blocker production/decision handling. Candidate snapshot/restore and review tool execution remain outside the memory fixture.
- Ordinary retry uses a standalone task. The parent relationship previously mixed into that scenario now belongs to `TestIntegrationWorkflowTaskRunDispatchesChildOfInProgressEpic`, which checks successful child dispatch and an unchanged parent. `TestIntegrationWorkflowTaskRunRejectsChildOfInactiveEpicBeforeSetup` now covers the inactive-parent rejection path through the public workflow fixture.
- `TestIntegrationWorkflowDispatchStubRejectsUnsupportedMutations` and `TestIntegrationWorkflowScriptedAgentRejectsUnexpectedOrUnspecifiedLaunch` check that unsupported fixture behavior cannot silently pass.

## Follow-up validation and policy

The first readability `make check` ran both lanes successfully, then returned `policy_update_required` for increased state-package coverage. The required five-sample `make quality-policy-update` passed. That refresh raised two coverage floors for `internal/state`: unit 76.346% to 80.105%, integration 69.654% to 72.386%.

The later state-API follow-up added integration-only config fixture contracts. Both lanes passed, but the new integration timing entry for `internal/state` required another five-sample policy update. The reviewed update adds only that package's 0.290s timing ceiling, based on a 0.040s median. No coverage floor was lowered and no existing timing ceiling changed. `go list` confirmed that normal builds exclude the config-seeding helper, while the production inventory caller remains in `taskstate.Store.TaskIDs`.

The fixture-style rewrite passed `make check` and a separate `make quality` against the refreshed policy. Targeted state/taskstate checks, the memory journeys and fixture contracts, a five-repeat race run, `go vet` for both build configurations, formatting, `git diff --check`, and integration-tagged lint also passed. Tagged lint exposed three existing mechanical issues in retained contracts, which were corrected: a missing `t.Helper`, an unused lint suppression, and a redundant embedded-field selector.

For the subsequent explicit-command adjustment, the targeted journeys and contracts passed. Both full validation runs passed their functional tests but returned `timing_budget_exceeded`. The separate `make quality` measured an 87.945s integration suite against an 84.214s ceiling and 6.504s for Git against a 5.178s ceiling. Lint, build, tagged lint, both vet configurations, and diff checks passed separately. This adjustment changes only command visibility in the fixture tests; policy bounds were not loosened.

The later single-snapshot follow-up passed targeted checks, five race-test repetitions, lint, both vet configurations, and build. Full functional tests passed again, but `make check` and `make quality` still returned timing-budget failures. The latest quality sample measured 85.423s for the integration suite and 5.628s for Git against the same ceilings. Policy bounds remain unchanged.

The integration-only memory-storage follow-up removed the conformance suite and standalone config-fixture tests, and kept inventory and constructor unit checks OS-backed. `go list` confirms normal state builds contain only `backend.go`, `lock.go`, and `state.go`; both memory storage and config seeding are excluded. Targeted state/taskstate tests, repository/task memory workflows, five race-test repetitions, both vet configurations, and both lint configurations passed.

The required five-sample policy refresh reflects the removal of covered memory code from the unit lane: the state coverage floor changes from 80.105% to 75.130%, with 77.130% measured coverage. The obsolete state integration timing entry is removed because that package no longer owns integration test bodies. Proposed increases to the unit suite, integration suite, and Git timing ceilings were rejected as unrelated to this change. Final `make check` and a separate `make quality` both passed with those existing timing ceilings preserved.

The public-CLI follow-up passed `make check`, a separate `make quality`, both lint and vet configurations, and five race-test repetitions of the memory workflows and constructor contracts. The contracts check copying of supplied paths/environment, explicit-empty versus inherited environment, and propagation of the selected XDG roots without modifying caller inputs. All six repository/task memory test files compile in `cli_test`, so they cannot access private CLI symbols. Quality policy is unchanged in this follow-up.

The shared-fixture and repo-readability follow-up passed `make check`, a separate `make quality`, both lint/vet configurations, and five race-test repetitions of the memory workflows and public-constructor contracts. No production code or quality-policy bounds changed in this follow-up.


## Repo and initial-dispatch migration

The next cleanup removes `repo_test.go` and `repo_beads_e2e_test.go`, 22
initial-dispatch tests from `task_adapter_contract_test.go`, eight repo/dispatch diagnostic tests,
and three fully scoped cases from `completion_flows_e2e_test.go`. Remaining
function bodies in the latter three files are unchanged. Their review,
repair-loop, publication, and finalization journeys remain for later op-sc7 tasks.
Unused Git/Beads initialization helpers, copied-database fixture cleanup, fake
agent-config helpers, and the old header-failure writer were removed with their
last callers.

| Retired assertions | Current owner |
| --- | --- |
| Repo policy get/set/clear, global fallback, invalid-policy nonmutation | `repo_config_workflow_test.go`, seeded registry/config only |
| Local/managed Beads directory lookup and lookup diagnostics | `repo_lookup_workflow_test.go`, explicit ID/name/prefix cases |
| Root discovery result, missing-remote warnings, registration conflicts, local/managed registration/list, initialization failure and lock ordering | `repo_registration_workflow_test.go` and `repo_workflow_test.go`; supplied Git/Beads outcomes, real CLI validation and registry persistence |
| Real managed initialization, local discovery and usable databases | `internal/beads/initialization_adapter_test.go`; real `bd`, isolated environment and databases |
| Managed schema drift repair | Existing `TestIntegrationAdapterContractManagedTaskBackendRepairsRealBeadsSchemaDrift` in `internal/beads`; `TestIntegrationWorkflowRepoRegistrationSelectsStatusBackendAndMaintenanceOwnership` checks the CLI selects the right source and maintenance authorization |
| Initial-dispatch eligibility, repo-root versus worktree target, retry metadata, competing owner, dirty-root setup failure, backend mutation conflict, active attempt and lock boundaries | `task_run_dispatch_workflow_test.go`; dirty Git detection itself stays in `internal/git` |
| Codex command construction, explicit profile override, unknown profile, Pi usage persistence and correlation inputs | `task_run_profiles_workflow_test.go`; session parsing remains in `internal/agent/pi_usage_test.go` |
| Start failure without child PID versus runtime failure; header failure before launch | Dispatch workflow assertions plus real missing-executable/no-OnStart contract in `internal/agentexec/launcher_environment_adapter_test.go` |
| Global summary guidance inherited after repo registration; global title-reference gate in status/dispatch; deprecated `--main` guidance | `task_run_configuration_workflow_test.go` and the external-reference table in `task_run_dispatch_workflow_test.go` |
| CLI diagnostic correlation, persistence/lock spans, lifecycle classification and prompt exclusions | `diagnostics_workflow_test.go` |
| Beads subprocess exit codes, repository correlation, expected absence and secret-output exclusions | `internal/beads/diagnostics_integration_test.go` |

The task fixture supplies initial-dispatch usage capture through
`cli.Dependencies.CaptureUsage`. Its production default remains
`agent.CaptureUsage`. This separates reading session files from the CLI's duty to
pass correlation inputs and persist the returned session, tokens and cost.
Review and repair capture paths are unchanged.

All workflow command invocations remain visible as `fixture.execute(...)`.
Rejected dispatch assertions consume an already loaded task-state snapshot.
Configuration and lookup tests no longer initialize Git repositories or Beads
workspaces merely to seed registry entries. Registration still needs an empty
real directory for the production path check, but never invokes Git or Beads.


Final validation passed `make check`, a separate `make quality`, tagged lint for
CLI/Beads/agentexec, both `go vet` configurations, and five race-test repetitions
of all 65 workflow/constructor tests. The five-sample policy refresh raised the
state integration coverage floor from 72.386% to 74.655% and tightened the CLI
integration timing ceiling from 47.345s to 23.105s, based on a 15.403s median.
The initial Beads timing overrun did not recur in the refresh or final checks;
its existing 42.452s ceiling remains unchanged. No timing ceiling was loosened
and no coverage floor was lowered by this migration.

## Review and repair continuation

The next migration runs review and repair through the real pipeline over this
application fixture. See [the review assertion map and measurements](op-sc7-5-review-workflow-evidence.md).
The dispatch-only scenarios above still intentionally supply review outcomes;
the new review journeys do not.
