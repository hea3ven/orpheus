# Task Views

Orpheus has two cross-repository task views for operators:

- `orpheus status` is the action queue. By default it emphasizes tasks that need attention, are under review, are working, idle, or ready to run. Use `--full` to include blocked and closed items.
- `orpheus task list` is the inventory. It shows every non-closed task and epic, including blocked and other entries that the default action queue hides.

Both views use the same Status Projection entries and table presentation. Status, details, epic hierarchy and progress, and responsive truncation are therefore consistent between the two commands.

Readiness remains an internal classification that populates the `Ready` status entries.
