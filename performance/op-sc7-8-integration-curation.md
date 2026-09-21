# Integration test curation

This review classifies integration tests by the boundary they prove. The categories are visible in every top-level integration test name. They do not affect the unit and integration lanes, `.quality.yml`, or the quality workflow.

## Classification map

### Workflow integration

`TestIntegrationWorkflow...` tests enter through the in-process CLI in `internal/cli`, using `package cli_test`, `*_workflow_test.go`, and the shared application fixtures. They run routing, services, review pipelines, and stores with semantic replacements at external boundaries. No product workflow tests remain in `internal/workflow`, `internal/doctor`, `internal/review`, or `internal/revieweval`. The [CLI-entry assertion map](op-sc7-8-cli-workflows.md) records the consolidation.

Private terminal-renderer mechanics and focused workflow policy remain owner units. Concrete adapters retain owner-local contracts. The nested coverage consumer is a synthetic Go-tool fixture named `TestConsumerCreditsCollaborator`, not a product workflow.

Fixture-only integration files such as `*_fixture_workflow_test.go`, `task_run_collaborators_workflow_test.go`, and `testmain_test.go` support those tests but contain no top-level scenarios.

### Adapter contract

`TestIntegrationAdapterContract...` tests call the owning package's concrete operation directly. Supporting imports do not change ownership. Calling a CLI or service to exercise a downstream adapter is workflow composition, not a contract test. The [ownership repair record](op-sc7-8-contract-ownership.md) maps the replaced assertions.

Retained boundaries:

| Owner | Boundary retained and why |
| --- | --- |
| `cmd/testcoverage` | Runs a nested `go test` command to prove cross-package `-coverpkg` profile attribution. An in-memory substitute cannot establish the Go tool's profile behavior. |
| `internal/agentexec` | Starts controlled child processes to prove PATH safety, argv, cwd, environment, stream timing, cancellation, PID reporting, exit handling, and reaping. |
| `internal/beads` | Uses isolated real Beads workspaces for database initialization, relationship behavior, schema repair, and environment sanitization. Command translation that does not need a database remains in unit tests. |
| `internal/cli` fixture infrastructure | `TestIntegrationAdapterContractCLIRepositoryFixtureCreatesIndependentOrigins` checks the seeded repository helper still needed by binary E2E. No CLI product scenario remains adapter-labelled. |
| `internal/git` | Owns repository inspection, snapshots, worktrees, commits, refs, merges, conflicts, rollback, push, publication, and status against isolated local repositories and bare remotes. |
| `internal/pullrequest` | Executes controlled `gh` substitutes to prove argument and stdin translation, scoped environment, JSON parsing, and provider exit diagnostics without a network. |
| `internal/review` | Direct candidate snapshots restore tracked/untracked bytes and remove reviewer-created files. Direct Hunk command contracts prove post-exit polling, missing-session handling, argv/cwd/environment/streams, and failed exits. Scoped command and Hunk environment contracts also remain. Pipeline decisions use semantic effects in workflows. |
| `internal/taskbranch` | Calls `git check-ref-format` because acceptance of rendered branch names is Git's contract. |

### Binary E2E

Only tests whose assertions require a compiled binary use `TestIntegrationBinaryE2E...`:

- `internal/cli/task_binary_e2e_test.go:TestIntegrationBinaryE2ETaskRunUsesSeparateTaskProposalSelection` compiles `cmd/orpheus`. Controlled implementer and reviewer processes recursively invoke the resulting binary, so packaging, environment handoff, child exits, and command startup are part of the scenario.
- `internal/agentexec/custom_binary_e2e_test.go:TestIntegrationBinaryE2ECustomNamedTestBinaryRetainsSafetyGate` compiles and starts a custom-named Go test binary to prove startup-time test-process detection still activates the model-agent safety gate.
- `internal/testguard/binary_e2e_test.go:TestIntegrationBinaryE2EProductionBinaryNamedTestDoesNotEnableTestMode` compiles and starts a production probe named like a Go test binary. Startup-time test-process detection is the behavior under test.

No other integration scenario needs a compiled binary. Workflow branches use application composition, and executable adapters use controlled process contracts without compiling the application under test.

## Prefix-validation assertion map

The task show and task dir malformed/unknown-prefix scenarios now use the memory-backed repository-lookup fixture in `internal/cli/repo_lookup_workflow_test.go`. They prove command validation and repository guidance, not a filesystem or process contract. The fixture disables executable lookup and does not persist registry data to disk.

| Former adapter scenario | Workflow replacement and retained assertions |
| --- | --- |
| `TestIntegrationAdapterContractTaskShowReportsMalformedAndUnknownPrefixes` | `TestIntegrationWorkflowTaskShowReportsMalformedAndUnknownPrefixes`: malformed ID and expected format, unknown prefix, repo-list and registration guidance, empty stdout and stderr for both failures. |
| `TestIntegrationAdapterContractTaskDirReportsMalformedAndUnknownPrefixes` | `TestIntegrationWorkflowTaskDirReportsMalformedAndUnknownPrefixes`: the same assertions for `task dir`. |

## Removed helper assertion map

The shared CLI fixture used to create an immutable shell wrapper around the current Go test binary. No remaining scenario invoked that wrapper. The curation removed `TestIntegrationOrpheusCLIHelperIsSharedAndImmutable` and its `TestIntegrationOrpheusCLIHelperProcess` entrypoint instead of preserving unused binary infrastructure.

| Removed assertion | Retained owner |
| --- | --- |
| Recursive agent commands can call a packaged Orpheus command | `TestIntegrationBinaryE2ETaskRunUsesSeparateTaskProposalSelection` builds `cmd/orpheus` and has both controlled agent roles call it. |
| Child argv, cwd, environment, streams, exit status, PID, cancellation, and reaping | Focused `TestIntegrationAdapterContract...` scenarios in `internal/agentexec`. |
| Independent seeded worktrees and local bare origins | `TestIntegrationAdapterContractCLIRepositoryFixtureCreatesIndependentOrigins`; the shared seeded repository fixture remains. |
| Wrapper immutability | Removed with the wrapper. It was a test-implementation property, not an Orpheus product contract. |

No production assertion was dropped or merged without an owner above.

## Review use

Use the category prefix to inspect one family, then narrow by package or test name. For example:

```bash
make coverage-audit COVERAGE_AUDIT_ARGS="-audit-package ./internal/agentexec -audit-run '^TestIntegrationAdapterContract'"
```

The audit first lists matching tests in the selected packages, then launches one profile per match. Running `make coverage-audit` without selectors remains the explicit full-suite audit.
