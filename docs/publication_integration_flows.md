# Publication integration flows

After a local review passes, Orpheus records one integration flow before it
changes Git, a pull-request provider, or the task backend. The recorded value
is reused by `orpheus task done` retries, even if configuration changes later.

The compatible default is `pull-request`.

## Configure defaults

Set the machine-wide default in Orpheus `config.yaml`:

```yaml
publication:
  integration_flow: direct-merge # or pull-request
```

A repository can override that default:

```bash
orpheus repo config get my-repo integration-flow
orpheus repo config set my-repo integration-flow direct-merge
```

Use an empty value to return a repository to the global default:

```bash
orpheus repo config set my-repo integration-flow ''
```

Only `pull-request` and `direct-merge` are valid non-empty values. The
precedence is a manual-review task selection, repository configuration, global
configuration, then `pull-request`.

## Manual and automated review

A manual review displays the effective flow. At its review-action prompt, enter
`i` to keep or change it. Orpheus stores that task-specific selection before
approval, so it takes precedence over defaults for that task.

A pipeline with no manual step uses the repository/global effective default.
Automated pipelines do not have a task-specific integration-flow override in
the MVP.

## Integration destinations

The registered default branch is the displayed and compatible destination. A
manual reviewer can enter `i`, keep the flow, and then enter an existing named
remote branch as the destination. Orpheus verifies named destinations on
`origin`; it never creates them.

The resolved destination is stored with the task before publication begins and
is immutable once publication starts. Pull-request creation and recovery use it
as the PR base, open-PR sync merges it into the task branch, and direct merge
refreshes and pushes that destination. Automated pipelines always use the
registered default branch.

## Pull requests

`pull-request` keeps the existing lifecycle: Orpheus commits the reviewed task
branch, pushes it, creates or recovers a pull request, and records the URL.

## Direct merges

`direct-merge` commits the reviewed task branch but does **not** push that
branch, call `gh`, create a pull request, or write pull-request metadata.
Orpheus refreshes the recorded integration destination from `origin`, creates
a no-fast-forward merge commit for the task branch, records it, pushes that
destination, and only then closes the backend task.

This works for both deterministic-worktree and repository-root work. A merge
conflict is aborted without pushing the default branch or closing the task. The
error leaves the task open and directs the operator to resolve the conflict
outside Orpheus before retrying.

## Retries

Retries keep the locked flow and destination. The recorded task commit, merge
commit, destination push, and backend closure make the stages idempotent. A
request to change either decision after publication starts is rejected; repair
the recorded publication rather than attempting to switch a partially
published task.
