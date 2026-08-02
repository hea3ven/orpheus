# CLI test isolation

`internal/cli` is the composition root. CLI workflow tests use the shared test
fixture to construct `newRootCommand` with one `invocationDependencies` value.
Each invocation receives its own state paths, environment map, external-command
PATH, and attached-agent launcher environment. The fixture never calls
`t.Setenv` or mutates package wiring.

The independent task, repository, status, doctor, diagnostics, completion, and
creation workflows run with `t.Parallel`. They retain their production command
coverage while using isolated state and fake external tools supplied through
their invocation environment.

## Narrow serial cases

Only tests that exercise a process-wide boundary remain serial:

- `eval_test.go` validates the public XDG-environment validation path.
- Agent-context and task-location tests that call `t.Chdir` remain serial,
  because the working directory is process-global.
- `repo_beads_e2e_test.go` uses real Beads tooling and remains serial by design.

New workflow coverage must use the invocation fixture and run in parallel unless
it needs one of these process-global boundaries.
