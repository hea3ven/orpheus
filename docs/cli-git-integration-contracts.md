# CLI and Git integration contracts

This matrix records the process-boundary contracts retained after the test-cost
reduction. It applies the process and overlap findings from the task review
evidence and the stable op-h1v.4 timing baseline.

## Timing evidence

Five uncached integration samples regenerated
`performance/test-timing-baseline.json` with 426 test events. The combined
CLI/Git package median fell from 65.675 seconds to 52.462 seconds, a 20.12%
improvement over the op-h1v.4 baseline. The Git median fell from 15.552 seconds
to 8.548 seconds; the CLI median fell from 50.123 seconds to 43.914 seconds.
The full integration median fell from 68.556 seconds to 62.474 seconds.

The regenerated suite, CLI, and Git budgets are lower than their previous
values, so future timing checks ratchet from the improved measurement rather
than accepting the earlier process cost.

## Coverage evidence

The regenerated coverage baseline records the same complete production
statement denominator for both lanes. Relative to the op-h1v.4 baseline, unit
coverage rises from 9,144 to 9,504 covered statements, while the integration
lane moves from 11,746 to 11,721. The 25 integration-only statements are the
orchestration cases intentionally transferred to the recording runner; the
combined result rises from 14,291 to 14,300 covered statements despite six new
runner-seam statements in the denominator. The runner matrix below documents
the retained assertion and failure-detection value for each transfer.

## Compiled CLI contract

| Contract | Test | Why it remains compiled |
| --- | --- | --- |
| A shell-launched implementation and reviewer can call the installed command form while a repository-root workflow selects no separate task. | `TestIntegrationTaskRunUsesSeparateTaskProposalSelection` | The fake agents execute `orpheus agent done` and `orpheus agent review add` through a binary built from `cmd/orpheus`. This keeps one focused packaging and child-process smoke flow. |

The eight other workflow scenarios that only need an `orpheus` command for
those agent subcommands use the package-scoped immutable Go test-binary helper.
They still execute the real child-process contract, but no longer compile the
same CLI eight additional times.

## Real-Git contract matrix

| Area | Retained real-Git scenarios | Semantics and failures covered |
| --- | --- | --- |
| Repository inspection | `TestIntegrationInspect*` | Root canonicalization, remote selection, `origin/HEAD` and current-branch fallback, non-repository rejection, and diagnostic classification for absent, detached, and malformed refs. |
| Candidate state | `TestIntegrationHasStagedChangesDistinguishesChangesFromGitFailure`, `TestIntegrationCandidateGitOperationsCaptureAndRestoreTrackedDiff` | Staged-versus-command failure, porcelain status, untracked discovery, binary patch capture, cleanup, restoration, and reapplication. |
| Worktree setup | `TestIntegrationSetupTaskWorktree*` | Create, reuse, recreate, mismatched worktree, missing origin, and foreign common-directory handling. |
| Repository-root preparation | `TestIntegrationSetupRepoRoot*`, `TestIntegrationMaterializeRepoRootTaskBranch*` | Default/task branch checkout, clean and dirty checkout policy, fast-forward and divergence checks, and stale local/remote task-branch refusal. |
| Branch synchronization | `TestIntegrationSyncTaskBranchWithDefaultMergesAndPushesCleanDefaultChanges`, `TestIntegrationSyncTaskBranchWithDefaultDetectsConflictWithoutPushing` | A real fetch and merge is pushed, while a real conflict preflight leaves the task branch unchanged. |
| Conflict resolution | `TestIntegrationTaskBranchConflictResolutionCompletesMergeAndPushes`, `TestIntegrationTaskBranchConflictResolutionCompletesMergeWithCleanDefaultRename` | A real conflicted merge is resolved, committed, pushed, and preserves a clean default-branch rename. |
| Direct merge | `TestIntegrationMergeTaskBranchIntoDefaultCreatesMergeWithoutPush`, `TestIntegrationMergeTaskBranchIntoNamedDestinationAndVerifyRemoteBranch` | No-fast-forward local merge without premature push, named-destination checkout, remote branch existence, missing destination, and unsafe-name rejection. |
| Commit verification | `TestIntegrationVerifyCommitMatchesRecordedParentAndMessage` | The recorded publication commit must retain its parent and exact message. |

These tests retain actual Git repositories, worktrees, refs, merges, pushes,
and patch behavior. The matrix intentionally keeps both successful operations
and failure paths whose correctness depends on Git rather than merely on the
orchestration decision.

## Recording-runner matrix and consolidation rationale

`internal/git` now accepts a command runner through `ContextWithRunner`. Unit
tests use a recording runner for the decision matrices below. The runner
asserts the complete command order, directory, arguments, and exit status;
therefore it detects an omitted probe, mutation command, or a changed decision
without starting a subprocess.

| Consolidated decision | Unit test | Prior real-Git scenarios and rationale |
| --- | --- | --- |
| Resolved merge has staged, untracked, or unstaged changes outside the conflict set. | `TestCompleteTaskBranchConflictResolutionRejectsUnexpectedChanges` | The three source states remain named table cases. Their product assertion is the same parser-and-rejection decision; Git's porcelain encoding and actual successful conflict resolution remain covered by the retained real-Git scenarios. |
| A conflict remains unresolved, or its staged resolution contains markers. | `TestCompleteTaskBranchConflictResolutionKeepsUnresolvedFiles`, `TestCompleteTaskBranchConflictResolutionRejectsConflictMarkers` | These cases exercise error propagation and the no-commit/no-push boundary after Git has already reported its state. Real Git conflict creation and successful resolution remain covered above. |
| A recorded direct merge is already remote, or the local destination no longer names it. | `TestValidateRecordedDirectMergeDecisions` | The two remote/local ref outcomes are still separately asserted. The retained direct-merge flows exercise real fetch, merge, and branch behavior. |
| A direct-merge destination is a pseudo-revision or equals the task branch. | `TestMergeTaskBranchIntoDestinationRejectsUnsafeOrMatchingBranches` | Both invalid inputs are rejected before a mutation command. The branch-format and remote-existence process contract remains in the retained named-destination test. |
| The destination is locally ahead because the task branch was fast-forwarded or because another commit followed a merge. | `TestMergeTaskBranchIntoDestinationRejectsLocallyAheadDestination` | Both distinct parent layouts remain table cases and must take the same conservative no-publish path. Real no-fast-forward creation and destination checkout remain covered by the retained direct-merge tests. |
| A synchronized task branch is already current, needs a push, or needs a fast-forward from its remote ref. | `TestSyncFetchedTaskBranchWithDefaultReportsCurrentOrPush`, `TestFastForwardTaskBranchFromOrigin` | The runner checks the ancestry and push decision sequences. The retained real-Git synchronization cases cover actual merge, fetch, push, and conflict-preflight behavior. |

The consolidation is not based on statement overlap alone: each removed
process-heavy test was compared by its assertions and the failure it detects.
Every omitted real-Git setup is represented either by a named recording-runner
case with command-sequence assertions or by a retained real-Git contract that
requires Git's filesystem, ref, merge, or transport semantics.
