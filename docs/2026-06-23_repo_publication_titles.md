# Repository Publication Titles

Use publication policies when repositories require particular commit subjects and pull-request titles. Configure machine-wide defaults in the shared Orpheus configuration file (`$XDG_CONFIG_HOME/orpheus/config.yaml`), then add repository values only where they differ:

```yaml
publication:
  summary_guidance: "Write a capitalized release-note summary, 80 characters or fewer."
  summary_guidance_style: capitalized
  title_template: "[{{external_ref}}] {{summary}}"
```

All three values are optional. Orpheus resolves each independently in this order:

1. The repository value in the registry.
2. The global `publication` value in `config.yaml`.
3. The compatibility default: typed summaries and an unchanged summary title.

New registrations leave these repository values unset unless an operator explicitly chooses an override, so configured global defaults apply immediately.

A custom `summary_guidance` takes precedence over the resolved named
`summary_guidance_style` when Orpheus instructs an agent to write its completion
summary. This does not prevent the style setting from being inherited or
inspected.

## Configure a Jira-style work repository

Inspect the stored repository values and their effective policy first:

```bash
orpheus repo config get my-work-repo
```

For Jira-style titles, set capitalized summary guidance and a title template that includes the task's external reference:

```bash
orpheus repo config set my-work-repo summary-style capitalized
orpheus repo config set my-work-repo title-template '[{{external_ref}}] {{summary}}'
```

`summary-style capitalized` instructs the implementation agent to provide one capitalized plain-English summary without a commit-type prefix. A custom instruction can replace the named style when needed:

```bash
orpheus repo config set my-work-repo summary-guidance 'Write a capitalized release-note summary, 80 characters or fewer.'
```

The title template supports `{{summary}}` and `{{external_ref}}` only. `{{external_ref}}` is inserted verbatim after whitespace normalization; Orpheus does not contact Jira or validate a Jira-key format.

Set the task reference before dispatching:

```bash
orpheus task edit op-123 --external-ref TREX-1234
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

For a main/solo run, `task done` commits and pushes the registered default branch. For a worktree or task-branch run, it commits, pushes the task branch, and creates or recovers the pull request. The same resolved publication policy applies in both cases.

## Default repositories and clearing overrides

An unconfigured repository with no global publication settings retains the existing defaults:

- agents are asked for a typed commit-style summary, such as `feat: replace config for abc`;
- `task done` uses that summary unchanged as the commit subject and pull-request title.

Clear a repository field to inherit the corresponding global value. If no global value is configured, clearing returns that field to its compatibility default:

```bash
orpheus repo config set my-work-repo summary-guidance ''
orpheus repo config set my-work-repo summary-style ''
orpheus repo config set my-work-repo title-template ''
```

## Missing external reference recovery

If the configured title template contains `{{external_ref}}`, the Status Projection marks an open task with no usable external reference as needing attention. It appears in the `orpheus status` action queue and in the `orpheus task list` inventory. `task run` rejects the task before it creates a worktree or starts an agent. The status detail and error provide the recovery command:

```text
orpheus task edit op-123 --external-ref <reference>
```

If the reference is removed after an agent has completed work, `task done` also fails before it creates a commit, pushes, or calls the pull-request provider. Restore the task's reference with the same `orpheus task edit` command, then rerun `task done`; the reviewed changes and completion handoff remain in place.

## Validation coverage

`internal/cli/completion_flows_e2e_test.go` validates the full command flow with local Git repositories and a fake GitHub CLI:

- `TestConfiguredPublicationPolicyEndToEnd` validates repository-level policy values.
- `TestIntegrationGlobalPublicationPolicyEndToEnd` validates global guidance/style and title-template rendering through agent context, commit creation, and pull-request publication.
- `TestIntegrationRepoAddInheritsGlobalSummaryStyleInAgentContext` verifies a non-interactive repository registration keeps its summary style unset and inherits a global capitalized-style instruction.
- `TestIntegrationGlobalTitleTemplateRequiresExternalReferenceInStatusAndDispatch` verifies global title templates gate both status and dispatch when a task reference is missing.
- `TestMissingPublicationExternalReferenceBlocksDispatchAndPublicationEndToEnd` verifies the missing-reference error before dispatch and again before publication after a repository policy change; it verifies neither path creates a publication commit or PR.
- `TestMainCompletionFlowEndToEnd` and `TestWorktreeLocalReviewTaskDonePRFlowEndToEnd` retain the default publication lifecycle coverage for repositories without a title policy.
