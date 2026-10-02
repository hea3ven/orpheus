# Task JSON for agents and tooling

`task list --json` returns `TaskSummary[]`, or `[]` when no tasks match.
`task show <id> --json` returns one `Task`, including closed tasks and epics.
`Task` extends the summary at the top level, not under a nested property.

## Format definitions

The Go DTOs and their JSON tags define the fields, types, and omission rules:

- [Summary serializer](../../internal/cli/task_view_json.go):
  `taskViewJSONTaskSummary` and its conversion function are shared by list,
  status, and show. They preserve projected status, semantic detail, epic
  progress, and timestamps. Timestamps are RFC 3339 strings or `null` if unknown.
- [Detailed serializer](../../internal/cli/task_show_json.go):
  `taskViewJSONTask` embeds the summary and adds `description`, `design`,
  `acceptance_criteria`, and `external_ref` strings, plus `labels` and
  `relationships`. Empty text is `""`; absent labels are `[]`. Execution and
  review history are excluded.

Use these definitions rather than maintaining a separate schema copy. The
[workflow contract](../../internal/cli/task_show_json_workflow_test.go) compares
all serialized summary fields with detailed output for the same task state.
[Serialization tests](../../internal/cli/task_show_json_internal_test.go) cover
empty values and relationship normalization.

## Relationship direction

`relationships` contains direct edges, not a transitive graph:

| Field | Meaning |
| --- | --- |
| `parent` | Containing epic ID, or `null` when absent. |
| `children` | Direct child IDs, including closed children. |
| `dependencies` | Prerequisite IDs. These tasks block this task while incomplete. |
| `dependents` | IDs of tasks that depend on this task. This task blocks them while incomplete. |

Parent-child edges are not blocking edges. Relationship arrays contain sorted,
deduplicated IDs; `[]` means none. Completed and unresolved references retain
their IDs. Values contain IDs only, with no per-relationship repository IDs,
`complete`, or `issues` fields.

## Read failures

Show completes required task, relationship-context, and local-state reads before
writing JSON. A required read failure produces a nonzero exit, stderr diagnostics,
and no JSON. Confirmed missing references, blocked dependencies, and other
needs-attention projections are valid results, not read failures.

List and status retain valid JSON arrays on partial repository failures and exit
nonzero with stderr diagnostics. Only status may include `kind: "repo_failure"`
entries; task-list arrays contain summaries only.
