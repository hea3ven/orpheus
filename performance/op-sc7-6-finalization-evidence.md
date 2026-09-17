# Finalization and publication workflow migration

## Scope

28 scenarios moved from `internal/cli/agent_test.go`, `task_test.go`, and
`completion_flows_e2e_test.go` to the external `cli_test` package. Their names
remain unchanged. Several flag-validation scenarios already avoided processes;
they now use the same isolated application fixture as the completion journeys.
Unused shell-agent, stateful Beads, completion, and log-reading helpers were
removed. The remaining doctor cleanup and CLI-helper contracts stay in place.

The new `completion_*_workflow_test.go`, `finalization_*_workflow_test.go`, and
`publication_*_workflow_test.go` files run fresh public root commands over the
existing memory-backed application. Routing, publication configuration, completion
validation, finalization, sync, status projection, and task-state persistence are
real. Task mutations, candidate changes, Git publication effects, and PR identity
are semantic collaborators. Tests inspect typed task/run/finalization state,
commit messages, remote branch outcomes, PR content, and operator output, not
YAML text or executable argument logs.

Dispatch-to-publication scenarios use the real review pipeline to reach the
manual gate. As before, these publication scenarios seed an approved review
before calling `task done`. Interactive review decisions remain covered by the
review workflow suite.

The shared fixture sets `PATH=/nonexistent`, supplies an isolated invocation
environment, and verifies that fixture roots remain absent from disk. Unknown
agent outcomes, PR status requests, conflict operations, and unsupported Git
operations fail. A separate exec trace confirmed zero child executables in the
migrated scenarios.

Production changes only expose existing external-effect inputs at CLI
composition:

- `CommandOptions.TaskWorkingDirectory` supplies the cwd used by `task done`
  inference, avoiding process-wide `Chdir`. Empty retains cwd discovery.
- `Dependencies.SyncGit` and `CleanupGit` pass the existing workflow interfaces
  to sync and finalization. Nil retains the existing local adapters.

No workflow decisions or persistence implementations were replaced. The design
HTML named in the task was absent from this checkout; this change follows
`docs/testing.md` and the existing memory-backed application fixtures.

## Assertion map

Every name below starts with `TestIntegration`. Each row maps the removed body
to the same-named replacement. Real Git observations now have focused adapter
owners listed in the next section.

| Scenario suffix | Preserved assertions and semantic replacement |
| --- | --- |
| `TaskDoneCommitsPushesClosesAndRecordsFinalization` | Finalized output includes commit; exact message excludes task/tool decoration; main remote points to commit; backend closes; commit, push, and close timestamps persist. Also checks succeeded run and no PR. |
| `TaskDoneRequiresPassedReview` | Missing-review error and next-command guidance; no output, commit, push, closure, or PR; candidate remains changed. |
| `TaskDoneRefusesRunningCompletionWithoutInteractiveConfirmation` | Explicit confirmation error, empty output, unchanged candidate and HEAD, running run preserved, no publication or closure. |
| `TaskDonePublishesPRReadyTaskBranch` | Published/pushed/created output and open-task guidance; exact title, detailed body, no tool/task decoration; repository/head/base request identity; remote branch equals publication commit; backend PR URL and open state. Unexpected PR status polling fails. |
| `TaskDoneRecoversExistingBranchPR` | Existing branch PR is recovered without creation; output, backend URL, pushed commit, and open task preserved. Also checks recovered event. |
| `TaskDoneFeatureBranchPushFailureIsNonZero` | Push/origin error and empty streams. Now also checks committed-but-unpushed state, no PR or closure, and successful retry without a second commit. Actual unavailable-origin behavior has a Git contract. |
| `TaskDoneInfersSingleMainReadyTaskFromRepoRootAndUsesOverrides` | Inference selects the ready task; operator summary/body become the exact commit message; stored completion remains unchanged and task closes. Supplied cwd replaces `Chdir`. |
| `TaskDoneInfersRepoRootFeatureBranchTask` | Repository-root inference publishes the expected branch/PR, persists backend URL, leaves task open, and pushes the recorded commit. |
| `TaskDoneInfersWorktreeTask` | Dedicated-worktree inference preserves the same publication, metadata, open-task, and remote-commit assertions. |
| `TaskDoneRejectsRemovedDetailsOverride` | Removed flag error and empty output. Also checks no state/publication mutation. |
| `TaskDoneWithoutTaskIDRequiresExactRegisteredRepoRoot` | Nested cwd is rejected with explicit task-ID guidance and empty output; no publication. |
| `TaskDoneRefusesNoChangesWithoutRecordedFinalizationCommit` | Both missing-changes and missing-recorded-commit diagnostics remain; no publication. |
| `TaskDoneRetriesPushAndCloseFromRecordedFinalizationCommit` | Retry preserves commit, pushes main, closes task, and records timestamps. Instead of only seeding a commit fact, the test now creates it through a failed push, then fails closure, then succeeds. A fourth invocation leaves one commit, successful push, and closure. |
| `AgentDoneRecordsMainCompletionForLocalReview` | Recorded-completion output, all four completion fields, running status, unchanged backend task, uncommitted candidate, no publication. |
| `AgentDoneRejectsMissingDescription` | Required description diagnostic and empty output; no runs or publication written. |
| `AgentDoneRejectsMissingDetailedDescription` | Required detailed-body diagnostic and empty output; no runs or publication written. |
| `AgentDoneRejectsMultipleDetailedDescriptionSources` | Exclusive-source diagnostic before reading a file; fixture-only filename replaces an unnecessary temporary file; no state mutation. |
| `AgentDoneRejectsRemovedDetailsFlag` | Removed flag diagnostic and empty output; no state mutation. |
| `AgentDoneRejectsMissingTechnicalExplanation` | Required explanation diagnostic and empty output; no state mutation. |
| `AgentDoneRejectsMultipleTechnicalExplanationSources` | Exclusive-source diagnostic before file access; no state mutation. |
| `AgentDoneRepeatedMainCompletionIsNoopWithGuidance` | First completion remains authoritative; repeated-completion guidance and diagnostic event retain attempt and all four requested replacement fields. No publication. |
| `AgentDoneCommitsWorktreeCompletion` | Despite the historical name, this scenario asserts that completion does not commit. Running status, technical explanation, empty commit/error, changed candidate, and task-run guidance remain. |
| `AgentDoneRequiresMainWorkingTreeChangesBeforeWriting` | Clean candidate errors before completion persistence; running run and nil completion remain; no publication. |
| `ConfiguredPublicationPolicyEndToEnd` | Repository config commands affect agent context and capitalized-summary guidance; external reference renders in context, commit, and PR title; raw completion summary persists; publication succeeds. |
| `GlobalPublicationPolicyEndToEnd` | Global custom guidance overrides named-style instructions; raw completion and rendered external-reference title/message remain distinct; publication succeeds. |
| `MissingPublicationExternalReferenceBlocksDispatchAndPublicationEndToEnd` | Missing reference blocks dispatch before worktree setup/agent launch; clearing policy allows completion; restoring policy blocks publication without changing candidate, HEAD, task PR metadata, or remote state. |
| `WorktreeLocalReviewTaskDonePRFlowEndToEnd` | Dispatch pauses for manual review with uncommitted completion and deterministic target; publication creates one PR and pushes recorded commit; status shows Reviewing, not needs-PR; open sync polls once; merged sync closes task, removes only dedicated worktree, records close/cleanup events, and shows Done / closed. PR metadata is written once. |
| `RepoRootLocalReviewTaskDonePRFlowEndToEnd` | Dispatch stays on main at the repository root; agent context explains deferred branch creation; task dir and candidate remain correct; publication materializes and pushes task branch; open sync polls once; merged sync closes task with PR reason/URL and retains repository root. One PR and metadata write. |

Additional application scenarios are outside the before/after timing set:

- `PublicationRetriesPRCreationAndMetadataWithoutRepublishing` separately fails
  PR creation and task metadata persistence. Retry either creates or recovers the
  PR, retains one commit/push/PR, and records publication failure. A subsequent
  `task done` redirects to sync without side effects.
- `PublicationDirectMergeRetriesPushAndClose` exercises the distinct direct-merge
  outcome at the repository root. A failed destination push preserves the merge;
  a failed close preserves the push. Retry produces one task commit and one
  merge, pushes only main, returns the checkout to main, closes the task, and
  never creates a PR or records PR metadata.

## Focused external contracts

The application fake does not claim to reproduce ref validation, merge ancestry,
Git staging, upstream configuration, or safe worktree removal.

| Boundary | Contract owner |
| --- | --- |
| Real staging, commit body, clean checkout, remote ref, task upstream, and unchanged remote main during task-branch publication | New `internal/git/publication_test.go:TestIntegrationPublicationCommitAndPushPreserveMessageAndRemoteRef`, with separate main and task-branch cases |
| Real failed push to an unavailable local origin, contextual error, unchanged local commit | New `TestIntegrationPublicationPushReportsUnavailableOriginWithoutChangingCommit`, with separate main and task-branch cases |
| Recorded commit parent/message verification | Retained `TestIntegrationVerifyCommitMatchesRecordedParentAndMessage` |
| Merge commit ancestry, local-only merge, idempotent merge, and named destination verification | Retained `internal/git/direct_merge_test.go` contracts |
| Branch materialization, reviewed changes, and stale/divergent local/remote refs | Retained `TestIntegrationMaterializeRepoRootTaskBranch*` contracts |
| Deterministic worktree creation, reuse, branch/path rejection, and safe cleanup | Retained `internal/git/worktree_test.go` contracts and doctor dirty/locked worktree scenario |
| Task branch sync, conflicts, and merge pushes | Retained Git sync/conflict contracts and workflow recovery scenarios |
| PR CLI translation and provider failures | Retained `internal/pullrequest` tests |
| Task mutation translation and real Beads behavior | Retained `internal/beads` unit and integration tests |
| Child process, completion diagnostics, and recursive CLI packaging | Retained launcher, verbose-agent-completion, and CLI-helper contracts |

## Measurements

`op-sc7-6-finalization-timing.json` records the 28 scenario names, three elapsed
samples per binary, and exec counts. Both binaries were compiled once on the same
host with `go test -c -tags=integration ./internal/cli`. Each sample used the exact
name alternation with `-test.count=1 -test.parallel=1`. Compilation is excluded.
These are targeted serial measurements, not whole-suite policy measurements.

| Measurement | Before | After |
| --- | ---: | ---: |
| Wall sample 1 | 3.663s | 0.296s |
| Wall sample 2 | 3.588s | 0.266s |
| Wall sample 3 | 3.321s | 0.263s |
| Median | 3.588s | 0.266s |
| Descendant PIDs executing programs | 834 | 0 |
| Descendant PIDs executing Git | 487 | 0 |
| Other controlled helper PIDs | 347 | 0 |

Median elapsed time fell by 92.6%. The separate Linux ptrace measurement follows
fork, vfork, clone, and exec events. Counts exclude the root binary and compilation;
a PID that executed Git belongs to the Git row even if it also executed another
program. Before-migration executables were Git, Bash, cat, and the controlled
CLI-helper test binary. Neither run used real Beads, gh, or model agents. Local
logs, binaries, and the tracer source are under
`artifacts/test-coverage/finalization-migration/`.

## Validation and policy

The initial functional lanes passed. Quality requested a refresh because workflow
integration coverage increased. The first five-sample
`make quality-policy-update` passed with stable counts and changed two bounds:

- Workflow integration coverage floor increased from 68.191% to 70.696%; measured
  coverage is 72.696%.
- CLI integration timing ceiling decreased from 23.105s to 11.054s, based on a
  7.369s median.

Some validation runs hit timing ceilings under host load, including a run
concurrent with lint. A second five-sample policy update left the Git ceiling
unchanged and reduced the integration suite ceiling from 84.214s to 62.151s,
based on a 49.721s median. No coverage floor was lowered and no timing ceiling
was raised. Serial validation then passed without relaxing the intermittently
exceeded ceilings. One initial check invocation also exceeded the command timeout
and was rerun with a longer timeout.

Final `make check` passed formatting, both lanes, lint, and build. A separate
final `make quality` also passed. The lanes report 1,100 unit and 515 integration
test events. Targeted workflows passed three race repetitions. Both vet
configurations, integration-tagged lint, and the new Git contracts passed.
