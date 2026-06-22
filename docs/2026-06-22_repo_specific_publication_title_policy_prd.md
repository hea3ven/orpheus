# PRD: Repo-Specific Publication Title Policies

## Status

Planned.

Related Beads:

- Epic: `op-qwc` — Support repo-specific publication title policies
- Child tasks: `op-qwc.1` through `op-qwc.7`

## Summary

Orpheus currently assumes that an agent-provided completion summary can be used directly as both the Git commit subject and the pull request title. The current agent guidance favors typed/conventional summaries such as `feat: add thing`.

Some work repositories require a different publication title format, most notably:

```text
[<jira id>] <description>
```

Example:

```text
[TREX-1234] Replaced the config for abc
```

This feature adds repo-level publication policy so each registered repository can define how agents should write summaries and how Orpheus should turn those summaries into commit subjects and PR titles. Unconfigured repositories keep the current behavior.

## Problem

The current Orpheus completion contract blocks use in repositories that enforce Jira-style commit/PR titles.

Current behavior:

- `agent context` tells agents to provide a typed/conventional summary.
- `agent done --summary` records that summary.
- `task done` uses that summary as the commit subject and PR title.

Work-repo requirement:

- The commit subject and PR title must include a Jira/task-tracker key.
- The title description should be a capitalized plain-English description, without a type prefix such as `feat:`.
- The Jira key varies per task and should not be invented by the implementation agent.

## Goals

- Allow repo-specific publication title formats.
- Use Beads `external_ref` as the canonical per-task Jira/task-tracker reference.
- Support work-style titles like `[TREX-1234] Replaced the config for abc`.
- Preserve current default behavior for repos that do not configure a publication policy.
- Make missing required `external_ref` visible before dispatch and impossible to publish accidentally.
- Keep the system deterministic: no LLM title generation, no shell-command templates, and no provider-specific Jira parsing.

## Non-goals

- No Jira API integration or Jira sync.
- No automatic setting of Beads `external_ref`.
- No Jira-specific parsing of URLs or prefixed strings.
- No separate commit-title and PR-title policies.
- No summary-format enforcement beyond agent-facing guidance.
- No replacement of Beads as the task backend.
- No title generation through agents, shell commands, or LLM utility profiles.

## Users and use cases

### Primary user

A developer/operator running Orpheus across personal and work repositories.

### Key use cases

1. **Default repo remains unchanged**
   - A repo without publication policy keeps the current typed summary guidance and uses the completion summary directly as commit subject and PR title.

2. **Work repo with Jira-style titles**
   - The repo is configured with capitalized summary guidance and title template `[{{external_ref}}] {{summary}}`.
   - A task has Beads `external_ref` set to `TREX-1234`.
   - The agent completes with summary `Replaced the config for abc`.
   - Orpheus publishes commit/PR title `[TREX-1234] Replaced the config for abc`.

3. **Missing Jira key is caught early**
   - A repo title template references `{{external_ref}}`.
   - A task has no external ref.
   - Orpheus excludes it from Ready, shows it as Needs attention, and refuses dispatch/publication until the external ref is set.

4. **Existing repo can be updated**
   - A repo registered before this feature can later be configured through a repo config command without re-registration.

## Product decisions

### Repo-level policy

Publication policy belongs to the registered repository, not the agent profile. The same agent profile may run in repos with different title requirements.

### Shared commit/PR title format

One repo-level publication title template applies to both:

- Orpheus-created commit subject.
- Pull request title.

Commit body and PR body keep their existing sources:

- Commit body: completion `description`.
- PR body: completion `detailed_description`.

### Summary guidance is guidance-only

Repos may configure how `agent context` tells agents to write the `--summary`, but Orpheus does not validate capitalization or type-prefix usage.

Initial named styles:

- `typed`: current typed/conventional style, e.g. `<type>: <description>`.
- `capitalized`: capitalized plain-English summary with no type prefix, e.g. `Replaced the config for abc`.

Custom summary guidance text overrides the named style.

### Beads external_ref as canonical task reference

The Jira/task-tracker ID comes from Beads `external_ref`.

Users set it through Beads, for example:

```bash
bd update <task-id> --external-ref TREX-1234
```

Orpheus inserts the value verbatim after trimming and collapsing whitespace to a single line. It does not parse Jira URLs, strip prefixes, or enforce a Jira-key regex.

## Requirements

### R1. Repo policy storage

Registered repositories must support optional publication policy fields for:

- Custom summary guidance text.
- Named summary guidance style.
- Publication title template.

Unconfigured repos must behave as they do today.

### R2. Registration-time configuration

Interactive `orpheus repo add` must allow initial publication policy configuration:

- Optional custom summary guidance.
- Optional publication title template.
- Summary guidance style once named styles exist.

The first implementation slices may add these prompts incrementally.

### R3. Agent context guidance

`orpheus agent context` must render effective summary guidance for the active repo:

- Default/unconfigured repos render typed guidance.
- Repos with custom guidance render the custom text.
- Repos with `capitalized` style render capitalized summary guidance.

### R4. Title template rendering

Publication title templates must support deterministic interpolation.

Initial placeholders:

- `{{summary}}`
- `{{external_ref}}`

The template output becomes both:

- Commit subject.
- PR title.

Repos without a template use the completion summary as-is.

### R5. external_ref support

Orpheus must read Beads `external_ref` into its backend-neutral task model and make it available for title interpolation and task views where appropriate.

### R6. Publication-time enforcement

If a repo title template references `{{external_ref}}`, `task done` must fail before commit, push, or PR creation when the task has no usable external ref.

### R7. Readiness/status/dispatch enforcement

For repos whose title template requires `external_ref`:

- Tasks missing external ref are excluded from `task ready`.
- Active pre-PR tasks missing external ref appear under Needs attention with actionable guidance.
- `task run` fails before launching the agent.
- Tasks that already have `orpheus.pr_url` continue through PR sync without new missing-ref gating.

### R8. Repo config command

Existing repos must be configurable after registration through `orpheus repo config <repo>`.

The command must support inspecting, setting, and clearing:

- Custom summary guidance.
- Named summary guidance style.
- Publication title template.

Invalid styles/templates must be rejected before mutating registry state.

## Example workflows

### Configure a work repo

Conceptual flow:

```bash
orpheus repo config my-work-repo \
  --summary-style capitalized \
  --title-template '[{{external_ref}}] {{summary}}'
```

Set the task reference:

```bash
bd update op-123 --external-ref TREX-1234
```

Run and publish:

```bash
orpheus task run op-123
# agent sees capitalized summary guidance in `orpheus agent context`
# agent completes with summary: Replaced the config for abc
orpheus task done op-123
```

Expected publication title:

```text
[TREX-1234] Replaced the config for abc
```

### Default repo

A repo without publication policy continues to guide agents toward typed summaries and publishes the summary directly:

```text
feat: replace config for abc
```

## Sequencing

The approved implementation plan is intentionally vertical and testable:

1. Configure custom summary guidance for agent context.
2. Configure and apply summary-only publication title templates.
3. Add named summary guidance styles.
4. Support `external_ref` interpolation in publication titles.
5. Gate readiness and dispatch on required `external_ref`.
6. Add repo config command for publication policy.
7. Document and validate repo-specific publication titles.

## Acceptance criteria

- A work repo can be configured to use `capitalized` summary guidance and `[{{external_ref}}] {{summary}}` publication titles.
- Given task `external_ref=TREX-1234` and completion summary `Replaced the config for abc`, Orpheus creates commit/PR titles `[TREX-1234] Replaced the config for abc`.
- Missing required external refs are surfaced before dispatch and prevented before publication.
- Existing repos without publication policy keep current agent guidance and title behavior.
- Existing PR lifecycle behavior remains intact.
- Automated or documented validation covers configured work-style repos, unconfigured default repos, and missing-external-ref recovery.

## Open questions

No blocking product questions remain from planning. Future work may revisit:

- Global default publication policies.
- Additional named summary styles.
- Jira/Linear/GitHub issue provider integrations.
- Separate commit-title and PR-title policies if a real repo requires them.
