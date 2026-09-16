# CLI test isolation

`internal/cli` is the composition root. New workflow integration tests belong in
`package cli_test` and construct commands through
`cli.NewRootCommandWithOptions`. Do not expose private helpers through
`export_test.go` or add `testpackage` suppressions for workflow fixtures.

`cli.CommandOptions` supplies state paths, an invocation environment, an optional
agent working directory, and external collaborators. The CLI constructs the real
registry and task-state stores and uses the same command tree as
`cli.NewRootCommand`. Nil collaborators select production adapters, so memory
fixtures must explicitly replace the external effects their scenarios use.

The repository and task-run workflows use this public constructor. Their
fixtures own the memory storage, semantic task backend, and scripted agents.
The shared `workflowFixture` owns memory paths, public command options,
registry access, captured command streams, and cleanup checks that state roots
stay off disk. Repo and task fixtures embed it but keep their scenario-specific
collaborators separate. Repo registration still uses a real temporary directory
because the production command validates that directory before Git inspection.

Setup and final-state reads use exported registry/task-state APIs. Scripted agents
execute `agent context` and `agent done` through fresh public commands, sharing
storage but supplying their own environment and working directory.

Construct a fresh command for each invocation. The constructor copies the supplied
environment map and paths value; storage and collaborator instances remain shared.
An explicit environment replaces the invocation's inherited environment. An empty
map does not fall back to the process environment. Supplied paths override XDG
resolution and determine the state roots passed to children.

## Existing fixtures

Repo registration/configuration/lookup and initial task dispatch use public
workflow fixtures. Real Beads initialization, local detection, database usability,
and subprocess diagnostics belong in `internal/beads`; process launch contracts
belong in `internal/agentexec`. Initial dispatch supplies usage-capture results
and verifies their persistence. Session-file parsing stays in `internal/agent`.

Deferred review, repair-loop, publication, finalization, and unrelated CLI tests
still use the private invocation fixture. That
fixture scopes state paths, external-command PATH, and attached-agent environment
to each invocation without changing package wiring. Migrate these tests to the
public constructor when changing their fixtures; do not extend private access to
new workflow families. Focused private-helper unit tests may remain in
`package cli` in `*_internal_test.go` files.

## Process-global boundaries

Run independent workflows in parallel unless they exercise process-global state:

- Public environment-resolution contracts use `t.Setenv`.
- Agent-context and task-location tests that call `t.Chdir` remain serial.
- Workflow fixtures remain serial because they set process `PATH=/nonexistent` as
  an additional guard against accidental executable launches.
