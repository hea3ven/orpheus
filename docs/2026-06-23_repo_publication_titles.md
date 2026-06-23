# Repository Publication Titles

Use a repository publication policy when a repository requires a particular commit subject and pull-request title. The policy is local to the registered repository; it does not change the behavior of other repositories.

## Configure a Jira-style work repository

Inspect the current policy first:

```bash
orpheus repo config get my-work-repo
```

For Jira-style titles, set capitalized summary guidance and a title template that includes the task's Beads `external_ref`:

```bash
orpheus repo config set my-work-repo summary-style capitalized
orpheus repo config set my-work-repo title-template '[{{external_ref}}] {{summary}}'
```

`summary-style capitalized` instructs the implementation agent to provide one capitalized plain-English summary without a commit-type prefix. A custom instruction can replace the named style when needed:

```bash
orpheus repo config set my-work-repo summary-guidance 'Write a capitalized release-note summary, 80 characters or fewer.'
```

The title template supports `{{summary}}` and `{{external_ref}}` only. `{{external_ref}}` is inserted from Beads verbatim after whitespace normalization; Orpheus does not contact Jira or validate a Jira-key format.

Set the Beads reference before dispatching the task. Run `bd` in the repository's configured Beads directory (shown by `orpheus repo beads-dir my-work-repo`):

```bash
bd update op-123 --external-ref TREX-1234
```

Then run and publish normally:

```bash
orpheus task run op-123
# The agent runs `orpheus agent context` and completes with:
# Replaced the config for abc
orpheus task done op-123
```

`task done` uses the rendered title for both the publication commit subject and the pull-request title:

```text
[TREX-1234] Replaced the config for abc
```

For a main/solo run, `task done` commits and pushes the registered default branch. For a worktree or task-branch run, it commits, pushes the task branch, and creates or recovers the pull request. The same repository policy applies in both cases.

## Default repositories

An unconfigured repository retains the existing defaults:

- agents are asked for a typed commit-style summary, such as `feat: replace config for abc`;
- `task done` uses that summary unchanged as the commit subject and pull-request title.

Clear any policy fields to return to the defaults:

```bash
orpheus repo config set my-work-repo summary-guidance ''
orpheus repo config set my-work-repo summary-style ''
orpheus repo config set my-work-repo title-template ''
```

## Missing external reference recovery

If the configured title template contains `{{external_ref}}`, Orpheus excludes an open task with no usable external reference from `task ready` and rejects `task run` before it creates a worktree or starts an agent. The error provides the recovery command:

```text
bd update op-123 --external-ref <reference>
```

If the reference is removed after an agent has completed work, `task done` also fails before it creates a commit, pushes, or calls the pull-request provider. Restore the task's reference with the same `bd update` command, then rerun `task done`; the reviewed changes and completion handoff remain in place.

## Validation coverage

`internal/cli/completion_flows_e2e_test.go` validates the full command flow with local Git repositories and a fake GitHub CLI:

- `TestConfiguredPublicationPolicyEndToEnd` configures a repository after registration, verifies later agent context contains the capitalized-summary instruction, and verifies the commit and PR title are `[TREX-1234] Replaced the config for abc`.
- `TestMissingPublicationExternalReferenceBlocksDispatchAndPublicationEndToEnd` verifies the missing-reference error before dispatch and again before publication after a policy change; it verifies neither path creates a publication commit or PR.
- `TestMainCompletionFlowEndToEnd` and `TestWorktreeLocalReviewTaskDonePRFlowEndToEnd` retain the default publication lifecycle coverage for repositories without a title policy.
