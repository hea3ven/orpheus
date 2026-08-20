# Task Views

Orpheus has two cross-repository task views for operators:

- `orpheus status` is the action queue. By default it emphasizes tasks that need attention, are under review, are working, idle, or ready to run. Use `--full` to include blocked and closed items.
- `orpheus task list` is the inventory. It shows every non-closed task and epic, including blocked and other entries that the default action queue hides.

Both views use the same Status Projection entries and table presentation. Status, details, epic hierarchy and progress, and responsive truncation are therefore consistent between the two commands.

Readiness remains an internal classification that populates the `Ready` status entries.

## JSON for agents and scripts

Use `--json` when an agent or script needs the selected task-view entries without parsing a terminal table:

```sh
orpheus status --json
orpheus status --full --json
orpheus task list --json --type task --status reviewing
```

Each command writes one deterministic JSON array to standard output. JSON uses the same final selection and ordering as its table view, but contains semantic values rather than table labels, tree prefixes, truncation, or placeholders. Task entries have `kind: "task"`, repository identity, task identity and fields, the projected Orpheus `status`, semantic `detail`, optional epic progress, and creation/update timestamps. It never exposes the task source lifecycle status. `ready` is valid in JSON output, even though it is not a `task list --status` filter.

`status --json` has the same default visibility as `status`; add `--full` for blocked and closed entries. Status output can also contain `kind: "repo_failure"` diagnostics. `task list --json` contains only selected task and epic entries. On partial repository failures, standard output remains a valid JSON array, diagnostics go to standard error, and the command exits nonzero. Empty results are `[]`.

## Inventory filters

`orpheus task list` can narrow the inventory with composable filters:

- `--query` partially matches task IDs and titles without case sensitivity.
- `--type task|epic` and `--status needs-attention|reviewing|working|idle|blocked|closed` are repeatable; repeated values in either category are alternatives.
- `--created-after`, `--created-before`, `--updated-after`, and `--updated-before` accept `YYYY-MM-DD` boundaries.

Categories combine as intersections. Task fields are constrained by the task source, while `--status` filters the shared Status Projection after Orpheus has loaded the parent, dependency, and selected-epic child context required for correct classification. `ready` is an internal readiness classification, not an inventory-filter value.
