# Task Views

Orpheus has two cross-repository task views for operators:

- `orpheus status` is the action queue. By default it emphasizes tasks that need attention, are under review, are working, idle, or ready to run. Use `--full` to include blocked and closed items.
- `orpheus task list` is the inventory. It shows every non-closed task and epic, including blocked and other entries that the default action queue hides.

Both views use the same Status Projection entries and table presentation. Status, details, epic hierarchy and progress, and responsive truncation are therefore consistent between the two commands.

Readiness remains an internal classification that populates the `Ready` status entries.

## Inventory filters

`orpheus task list` can narrow the inventory with composable filters:

- `--query` partially matches task IDs and titles without case sensitivity.
- `--type task|epic` and `--status needs-attention|reviewing|working|idle|blocked|closed` are repeatable; repeated values in either category are alternatives.
- `--created-after`, `--created-before`, `--updated-after`, and `--updated-before` accept `YYYY-MM-DD` boundaries.

Categories combine as intersections. Task fields are constrained by the task source, while `--status` filters the shared Status Projection after Orpheus has loaded the parent, dependency, and selected-epic child context required for correct classification. `ready` is an internal readiness classification, not an inventory-filter value.
