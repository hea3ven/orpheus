# Task branch templates

Orpheus derives each task branch from a template. Configure a machine-wide
default in the shared Orpheus configuration file (`$XDG_CONFIG_HOME/orpheus/config.yaml`):

```yaml
tasks:
  branch_template: "feature/{{external_ref}}/{{task_title}}"
```

Set a repository-specific override with:

```sh
orpheus repo config set <repo> branch-template 'work/{{task_id}}-{{task_title}}'
```

Inspect the stored repository value and its effective result with:

```sh
orpheus repo config get <repo> branch-template
```

## Precedence

Orpheus resolves the first configured value in this order:

1. The repository `branch_template` in the repository registry.
2. The global `tasks.branch_template` in `config.yaml`.
3. The compatibility default `orpheus/{{task_id}}`.

An empty repository value clears its override. An empty global value retains the
compatibility default.

## Placeholders and normalization

Templates may contain literal text plus these placeholders:

- `{{task_id}}`
- `{{external_ref}}`
- `{{task_title}}`

A template that uses a missing task field fails before Orpheus creates a branch
or worktree, or writes task metadata. Values are normalized deterministically:
letters, digits, `_`, and `-` are retained; each contiguous run of other
characters becomes one `-`; leading and trailing replacement dashes are
removed. Dynamic values cannot add branch path separators. Literal `/` values
in the template can deliberately create branch namespaces.

After rendering, Orpheus validates the complete result as a Git branch ref.
Invalid literal ref syntax is rejected rather than silently changed, including
components that begin with `.` and the reserved name `HEAD`. A rendered task
branch must also differ from
the repository's registered default branch. Different tasks that render the
same branch, including the compatibility default after normalization, are
rejected if that branch is already recorded for another task.

## Retries and repository-root runs

Worktree and legacy repository-root task-branch runs record their rendered
branch in task metadata and local state. That recorded branch remains
authoritative for retries, reviews, publication recovery, sync, and status even
if configuration changes later.

A new `orpheus task run --repo-root` still begins on the registered default
branch. Once local review has passed, publication materializes the configured
task branch and records it before creating the pull request.
