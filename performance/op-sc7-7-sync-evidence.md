# Pull-request synchronization migration

## Scope and decisions

Ten CLI scenarios moved from `task_adapter_contract_test.go` to the
external `cli_test` application fixture. Their top-level names remain unchanged.
Commands, sync services, task-state stores, configuration, agent selection, and
operator output remain real. Task sources, Git effects, PR state, agent execution,
and usage capture are semantic collaborators. The fixture disables executable
lookup and keeps registry, configuration, and task state in memory.

Creation, existing-PR recovery, publication failures, and publication idempotency
already use the same application fixture from the preceding migration. They
remain intact. This change adds PR lookup failure/retry and extends the PR fake
with failure injection, per-URL states for batch sync, request observations, and
open-only branch lookup. Identity includes repository, head, base, and URL.

`memorySyncGit` records local and remote heads, whether the default branch is
missing, conflict files, merge-in-progress state, resolution, commits, pushes,
and rollback. Batch sync leaves conflict-free divergence alone; task-ID sync
merges or retries a pending push. Unknown targets and combined conflict completion
fail rather than falling through to an executable. The fake does not implement
Git's merge algorithm or parse Git/gh output.

The only production behavior boundary changed is usage capture in the CLI's
conflict resolver. It now uses the existing `Dependencies.CaptureUsage` hook,
just as dispatch does. Nil still selects `agent.CaptureUsage`. The real resolver
still selects the configured profile, builds the command/environment, records
the child PID, and passes usage into workflow audit persistence. No workflow
outcomes or store implementations were replaced.

The design HTML named in the task is absent from this checkout. This migration
follows `docs/testing.md` and the existing memory-backed application fixtures.

## Assertion map

Every scenario name below has the prefix `TestIntegration`. Unless noted,
the replacement has the same name in `internal/cli/sync_*_workflow_test.go`.
Adapter-specific assertions have the focused owners listed in the next section.

| Removed scenario suffix | Preserved assertions and replacement |
| --- | --- |
| `TaskSyncPollsExistingPRURLWithoutPushOrMutation` | Synced/open output, exact stored URL lookup, no create/list, no task mutation. Unsupported worktree metadata still permits polling but skips Git. Now also checks unchanged state, no pushes, and no conflict operation. |
| `TaskSyncRecordsConflictResolutionUsageTelemetry` | Repair output, changed HEAD, resolved contents effect, started/finished audit, dedicated `sync-codex` profile, execution purpose/status, session ID, 190 tokens, capture status/reason. The exact isolated `CODEX_HOME` map must reach usage capture. Resolution is semantic; real file bytes, merge parents, and remote publication have Git contracts. Also checks durable resolving phase and child PID before the agent effect, launch environment, cleared checkpoint, remote HEAD, and no repeated repair on subsequent sync. |
| `TaskSyncClosesBackendAndRecordsLocalAuditForMergedPR` | Merged/closed output, one closure, no PR creation or metadata write, task-closed event with merged reason/URL/state. Beads owns exact read-before-close command translation. Repeated sync now proves closure and audit idempotency. |
| `TaskSyncExistingPRErrorsDoNotMutateBackendOrAudit` | All six cases remain: invalid stored URL, missing returned URL, malformed returned URL, repository failure, authentication failure, and closed-unmerged PR. Errors propagate without output, task mutation, audit, Git, creation, or lookup. Raw provider payloads and pre-execution URL validation belong to GH contracts. |
| `TaskSyncSkipsClosedTaskWithoutPRPolling` | Skipped/closed/no-changes guidance, no PR polling or backend mutation. Also asserts unchanged local state and no pushes. |
| `TaskSyncSkipsTaskWithoutPRURLAtRepoRoot` | Completed feature-branch task at repository root skips with missing-URL/no-changes guidance. Completion and task state stay unchanged; no publication or PR polling. Real setup commits were incidental to this skip decision. |
| `TaskSyncSkipsMainSoloLocalReadyTaskWithoutPRURL` | Main completion skips with the same missing-URL/no-changes guidance, unchanged completion/task state, and no publication or polling. |
| `TaskSyncAllPollsPRBoundaryTasks` | Only the open PR-boundary task appears and is polled. Completed-but-unpublished task and epic remain excluded. Also excludes closed tasks, retains the unpublished completion, and verifies no publication, metadata writes, or closure. |
| `TaskSyncAllReturnsNonZeroAfterCandidateError` | One grouped candidate error, closed-without-merge detail, empty stderr, nonzero result. Task and audit remain unchanged. |
| `TaskSyncAllGroupsCrossRepoResultsAndReturnsNonZeroAfterFailures` | Alpha open PR, beta merged/closed task, beta closed-unmerged error, gamma scan failure, two-item aggregate failure, three URL polls, unpublished alpha task excluded, no creation/metadata write, beta closure audit. The source error is now `task source unavailable`, not raw `bd unavailable`; Beads owns process-error translation. |

Additional application scenarios are not included in the before/after selector:

- `TaskSyncUpdatesBranchAndRetriesPushWithoutAnotherMerge` preserves a local
  merge across push failure, then pushes once without a second merge.
- `TaskSyncAllLeavesConflictFreeBranchUnchanged` contrasts batch and explicit
  task-ID policies using the same branch state.
- `TaskSyncConflictFailureRollsBackAndRetries` separately fails the resolver and
  push. Both preserve the checkpoint remote, restore local state, retain failure
  audit, clear the active operation, and allow a successful, idempotent retry.
- `TaskSyncRecoversAmbiguousPushWithoutRepeatingRepair` leaves durable push intent
  after remote inspection fails. A stopped supervisor then recovers by observed
  remote HEAD without another agent, commit, or push. A separately moved remote
  instead leaves an unresolved operation and rejects further sync without rollback.
- `TaskSyncMergedPRRetriesFailedClosure` preserves state on backend failure,
  then closes once and removes only the proven dedicated worktree.
- `PublicationRetriesPRLookupWithoutRepublishing` retains one commit/push while
  retrying a failed provider lookup before creating the PR.

The existing workflow persistence-failure matrix, pushed-phase recovery, and
conflict-disappearing-after-preflight scenarios remain in
`internal/workflow/sync_conflict_integration_test.go`. No recovery scenario was
deleted because it traverses similar code.

## Focused contracts

| Boundary | Owner |
| --- | --- |
| GH repository/head/base arguments, title argument boundaries, body on stdin, scoped executable/environment, parsed identity | New `internal/pullrequest/gh_publication_adapter_test.go:TestIntegrationAdapterContractGHProviderPublicationTranslatesIdentityAndBody`; existing `gh_environment_adapter_test.go` |
| Empty lookup, malformed JSON/URLs, missing URLs, lookup/create provider failures | New `TestIntegrationAdapterContractGHProviderPublicationOutputAndFailures`; missing status URL added to `TestIntegrationAdapterContractGHProviderStatusByURL` |
| Repository and authentication diagnostics with actual process failure | New `TestIntegrationAdapterContractGHProviderStatusClassifiesRepositoryAndAuthenticationFailures`; existing generic/unknown-field contracts |
| Stored URL rejected before starting gh | Retained `TestGHProviderStatusByURLRejectsMalformedURL` |
| Real conflict checkpoint refs, local-only resolved merge, exact merge parents, remote head inspection, separate push and repeated push | New `internal/git/conflict_recovery_adapter_test.go:TestIntegrationAdapterContractConflictRecoveryCommitsLocallyBeforeSeparatePush` |
| Rollback ownership, aborted versus locally completed merge, restored file bytes/clean checkout, unchanged remote, incompatible remote verification | New `TestIntegrationAdapterContractConflictRecoveryRollsBackOnlyMatchingCheckpoint`, with independent in-progress and completed cases |
| Merge/push, conflict-only preflight, remote fast-forward, unresolved merge, clean default changes and rename | Retained `internal/git/worktree_adapter_test.go` sync/conflict contracts |
| Deterministic worktrees, ref/branch validation, divergent refs, safe cleanup, upstream and failed publication pushes | Retained worktree, direct-merge, and publication Git contracts |
| Session matching/token parsing and dedicated resolver profile selection | Retained `internal/agent/codex_usage_test.go` and `profile_test.go` |
| Beads query/mutation translation and read-before-close behavior | Retained `internal/beads` contracts |

An initial quality run exposed Git coverage previously owned only by the broad
CLI conflict journey. The new durable Git contracts restore focused coverage
above the existing floor rather than lowering that floor. Real Git integration
coverage is 68.78%; its accepted floor remains 67.028%.

## Measurements

`op-sc7-7-sync-timing.json` contains the exact ten names and samples. Before
and after binaries were compiled with `go test -c -tags=integration ./internal/cli`
on the same host. Each sample uses the exact-name alternation, `-test.count=1`,
and `-test.parallel=1`. Compilation is excluded. These targeted serial samples
are not inputs to whole-suite timing policy.

| Measurement | Before | After |
| --- | ---: | ---: |
| Elapsed sample 1 | 0.981s | 0.070s |
| Elapsed sample 2 | 0.957s | 0.063s |
| Elapsed sample 3 | 0.923s | 0.066s |
| Median | 0.957s | 0.066s |
| Descendant PIDs executing programs | 295 | 0 |
| Descendant PIDs executing Git | 186 | 0 |

The selected application scenarios take 93.1% less elapsed time. A separate
Linux ptrace run follows fork, vfork, clone, and exec events, excluding the root
binary and compilation. An expanded selector including the new application
recovery scenarios also records zero child executables. Neither migration
measurement invokes real gh, Beads, or a model agent.

The added real contracts cost additional work. Three samples of the new Git
contracts have a 1.368s median; the new GH contract selector has a 0.103s median.
These contracts add safety assertions beyond the removed journey, so the 93.1%
figure is not a claim of whole-suite speedup. Binaries, logs, and tracer source
are under `artifacts/test-coverage/sync-migration/` and
`artifacts/test-coverage/sync-repair/`. The final comparison excludes the
logging-only sync scenario on both sides, rather than counting its removal as
a workflow speedup.

## Review follow-up

The review requested removal of logging-only testing, not a replacement logging
fixture. Thirty top-level logging-only scenarios were removed across CLI, agent,
Beads, Git, logging, PR, review, and workflow packages. This includes
`VerboseTaskSyncCLIDiagnosticsUsesSyncStatusKey`, which was initially migrated.
Its log-key and correlation assertions are deliberately retired, not covered by
a replacement. `docs/testing.md` now prohibits logging assertions and
separates them from operator-facing output and persisted audit data.

Mixed scenarios retain their functional assertions without recording or
asserting logs:

| Area | Retained contract |
| --- | --- |
| Git inspection | Detached HEAD uses origin's default; malformed origin HEAD falls back to the current branch. Missing origin HEAD already has a functional fallback scenario. |
| Review command cancellation | Cancellation before start returns the cancellation error, no exit code, and no captured notes. |
| Review pipeline | Candidate mutation restores file contents and fails the pipeline. |
| Review lifecycle | Staged candidates stop before the pipeline; manual findings persist with their step identity. |
| Publication | Exact title/body, commit/push facts, PR identity, backend state, and audit events. |
| Batch sync | Open/merged outcomes, aggregated failures, provider request URLs, and closure effects. |

Unused diagnostic fixtures, log parsers, and their orphaned CLI repository setup
helpers were removed. Production logging is unchanged. Persisted completion,
recovery, and usage audit assertions remain.

Both review advisories are addressed. The usage scenario seeds an isolated
`CODEX_HOME` and checks the entire capture environment. Every `memorySyncGit`
operation validates repository, destination branch, task branch, and worktree
identity before phase checks or effects. A 40-case matrix passes unknown
identities through all ten operations and verifies rejection without mutation.
The expanded zero-executable trace includes this matrix.

## Validation and policy

Targeted CLI/provider/Git checks, three race repetitions of the application
sync scenarios and fake-contract matrix, both vet configurations, and
integration-tagged lint pass.

The initial implementation's five complete policy samples raised only the PR
adapter coverage floor and timing allowance. That retained change moves the
floor from 82.354% to 85.075% and the timing ceiling from 0.431s to 0.695s.

After removing logging checks, another five complete comparable samples passed
with 1,096 unit and 567 integration test events. The reviewed follow-up policy
diff reflects that intentional coverage removal:

- Both logging coverage floors are now 50.778%, with 52.778% measured through
  remaining application paths. The obsolete logging unit timing bound is removed.
- The Beads integration coverage floor moves from 64.667% to 62.060%, with
  64.060% measured after removing diagnostic-only process scenarios.
- Git's coverage floor stays at 67.028%. No timing ceiling was raised in this
  follow-up; the earlier PR adapter allowance remains.

The lower floors are a consequence of the explicit no-logging-testing policy,
not a substitute for fixing a functional regression. Initial follow-up runs also
exposed orphaned diagnostic helpers and a transient doctor timing failure;
neither was accepted by changing the doctor bound.

Final follow-up `make check` passed formatting, both lanes, lint, and CLI build.
A separate final `make quality` also passed. Both vet configurations,
integration-tagged lint, and three race repetitions passed as well.
