# M2 Global Task Visibility Validation

This note records the validation flow for Milestone 2: presenting task items across all registered repositories through `task list`, `task ready`, `task show`, and local-only `status`.

## Assumptions

- Run from the Orpheus repository root.
- `git`, `go`, and the real `bd` binary are on `PATH`.
- The commands use temporary repositories and isolated XDG roots; they must not read or mutate the operator's real Orpheus or Beads state.
- The snippets pipe empty stdin into `repo add` so Orpheus accepts detected Git values and the default managed Beads prefix non-interactively.
- M2 global task views show active items from all Beads issue types. Closed items remain visible to `status --full` in the `Done / closed` group, but are omitted from `task list` and rejected by `task show` as not active.
- `task ready` is Orpheus' M2 readiness projection over Beads snapshots, not a call to `bd ready`. It uses Beads status/dependency/metadata data plus Orpheus' local `orpheus.pr_url` rule.

## Setup

```bash
set -euo pipefail

ROOT="$(mktemp -d)"
export XDG_CONFIG_HOME="$ROOT/xdg-config"
export XDG_DATA_HOME="$ROOT/xdg-data"
ORPHEUS="$ROOT/bin/orpheus"

mkdir -p "$ROOT/bin" "$ROOT/repos"
go build -o "$ORPHEUS" ./cmd/orpheus

git_repo() {
  repo="$1"
  mkdir -p "$repo"
  git -C "$repo" init -q
  git -C "$repo" checkout -q -b main
  git -C "$repo" \
    -c user.name='Orpheus Validation' \
    -c user.email='orpheus@example.com' \
    commit --allow-empty -q -m 'initial'
}

add_origin_head() {
  repo="$1"
  remote_name="$2"
  git -C "$repo" remote add origin "git@example.com:org/${remote_name}.git"
  git -C "$repo" update-ref refs/remotes/origin/main HEAD
  git -C "$repo" symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/main
}

run_bd() {
  dir="$1"
  shift
  (cd "$dir" && BD_NON_INTERACTIVE=1 bd "$@")
}
```

## Register local and managed Beads repositories

```bash
LOCAL="$ROOT/repos/localm2"
MANAGED="$ROOT/repos/managedm2"

git_repo "$LOCAL"
run_bd "$LOCAL" init --non-interactive --prefix vl --skip-agents --skip-hooks --quiet
add_origin_head "$LOCAL" localm2

git_repo "$MANAGED"
add_origin_head "$MANAGED" managedm2

printf '' | "$ORPHEUS" repo add "$LOCAL"
printf '' | "$ORPHEUS" repo add "$MANAGED"

LOCAL_BEADS_DIR="$($ORPHEUS repo beads-dir vl)"
MANAGED_BEADS_DIR="$($ORPHEUS repo beads-dir managedm2)"
printf 'local=%s\nmanaged=%s\n' "$LOCAL_BEADS_DIR" "$MANAGED_BEADS_DIR"
```

Expected outcome:

- The local repo is registered with Beads mode `local` and prefix `vl`.
- The managed repo is registered with Beads mode `managed` and prefix `managedm2`.
- `repo beads-dir vl` resolves to the local Git repository path.
- `repo beads-dir managedm2` resolves under `$XDG_DATA_HOME/orpheus/repos/managedm2/beads`.

## Create representative Beads items

Create local items that cover all active issue types, Orpheus metadata projection, and blocked/ready dependency states:

```bash
LOCAL_READY="$(
  run_bd "$LOCAL_BEADS_DIR" create 'Local ready with metadata' \
    --type task \
    --priority P1 \
    --metadata '{"orpheus.branch":"task/vl-ready","orpheus.worktree":"/tmp/orpheus/vl-ready"}' \
    --silent
)"
LOCAL_BUG="$(run_bd "$LOCAL_BEADS_DIR" create 'Local bug ready' --type bug --priority P2 --silent)"
LOCAL_EPIC="$(run_bd "$LOCAL_BEADS_DIR" create 'Local epic ready' --type epic --priority P3 --silent)"

LOCAL_BLOCKED="$(run_bd "$LOCAL_BEADS_DIR" create 'Local blocked by active blocker' --type task --priority P2 --silent)"
LOCAL_BLOCKER="$(run_bd "$LOCAL_BEADS_DIR" create 'Local active blocker' --type task --deps "blocks:$LOCAL_BLOCKED" --priority P2 --silent)"
```

Create managed items that cover closed dependencies, in-review metadata, and working status:

```bash
MANAGED_READY_AFTER_CLOSED="$(
  run_bd "$MANAGED_BEADS_DIR" create 'Managed ready after closed dependency' \
    --type task \
    --priority P1 \
    --silent
)"
MANAGED_CLOSED_BLOCKER="$(
  run_bd "$MANAGED_BEADS_DIR" create 'Managed closed dependency' \
    --type task \
    --deps "blocks:$MANAGED_READY_AFTER_CLOSED" \
    --priority P2 \
    --silent
)"
run_bd "$MANAGED_BEADS_DIR" close "$MANAGED_CLOSED_BLOCKER" --reason done --force >/dev/null

MANAGED_REVIEW="$(
  run_bd "$MANAGED_BEADS_DIR" create 'Managed in review' \
    --type task \
    --metadata '{"orpheus.pr_url":"https://example.test/pull/12"}' \
    --silent
)"
MANAGED_WORKING="$(run_bd "$MANAGED_BEADS_DIR" create 'Managed working chore' --type chore --silent)"
run_bd "$MANAGED_BEADS_DIR" update "$MANAGED_WORKING" --status in_progress >/dev/null
```

Expected outcome:

- The local Beads database contains active `task`, `bug`, and `epic` items.
- `$LOCAL_BLOCKED` has an active blocking dependency, so Orpheus classifies it as `Blocked`.
- `$MANAGED_READY_AFTER_CLOSED` has a closed blocking dependency, so Orpheus classifies it as ready.
- `$MANAGED_REVIEW` contains `orpheus.pr_url`, so Orpheus classifies it as `In review` instead of ready.
- `$MANAGED_WORKING` is in progress and appears in the `Working` group.

## Validate `task list`

```bash
"$ORPHEUS" task list
"$ORPHEUS" task list --details
```

Expected outcome:

- Rows from both `localm2` and `managedm2` appear in one table.
- Active items from all issue types appear, including `$LOCAL_READY`, `$LOCAL_BUG`, `$LOCAL_EPIC`, `$LOCAL_BLOCKED`, `$LOCAL_BLOCKER`, `$MANAGED_READY_AFTER_CLOSED`, `$MANAGED_REVIEW`, and `$MANAGED_WORKING`.
- Closed `$MANAGED_CLOSED_BLOCKER` does not appear in `task list`.
- The detailed table includes `REPO_ID`, `TASK_PREFIX`, `BRANCH`, `WORKTREE`, and `PR` columns.
- The `$LOCAL_READY` row projects `orpheus.branch` and `orpheus.worktree` as `task/vl-ready` and `/tmp/orpheus/vl-ready`.
- The `$MANAGED_REVIEW` row projects `orpheus.pr_url` as `https://example.test/pull/12`.

## Validate `task ready`

```bash
"$ORPHEUS" task ready
"$ORPHEUS" task ready --details
```

Expected outcome:

- Ready rows from both repositories appear.
- `$LOCAL_READY`, `$LOCAL_BUG`, `$LOCAL_EPIC`, `$LOCAL_BLOCKER`, and `$MANAGED_READY_AFTER_CLOSED` appear as ready.
- `$LOCAL_BLOCKED` does not appear because its blocker is still open.
- `$MANAGED_REVIEW` does not appear because `orpheus.pr_url` is non-empty.
- `$MANAGED_WORKING` does not appear because it is `in_progress`.
- The command reads snapshots with `bd list --all --limit 0`; it does not delegate selection to backend-native `bd ready`.

## Validate `task show` prefix resolution

```bash
"$ORPHEUS" task show "$LOCAL_READY"
"$ORPHEUS" task show "$LOCAL_BUG"
```

Expected outcome:

- Each command resolves the task id by task prefix and queries only the owning repo.
- Output is backend-neutral, not raw `bd show` JSON.
- Output includes repository ID/name/prefix, task ID/title/status/priority/type/labels, description/design/acceptance criteria when present, and an Orpheus metadata summary.
- The `$LOCAL_READY` detail view shows branch `task/vl-ready`, worktree `/tmp/orpheus/vl-ready`, and PR `-`.
- The `$LOCAL_BUG` detail view demonstrates active non-task issue types are visible in M2.

Validate malformed and unknown prefixes:

```bash
"$ORPHEUS" task show notprefixed  # expected failure
"$ORPHEUS" task show zz-1         # expected failure
```

Expected failures:

- `notprefixed` reports `malformed task id` and explains the expected `<prefix>-<number>` shape.
- `zz-1` reports `unknown task id prefix` and suggests `orpheus repo list` or registering the repo.

Validate a closed item is not shown as an active task view:

```bash
"$ORPHEUS" task show "$MANAGED_CLOSED_BLOCKER"  # expected failure
```

Expected failure includes `out of scope for M2 task views`, `expected an active item`, and `status=closed`.

## Validate local-only `status`

```bash
"$ORPHEUS" status
"$ORPHEUS" status --full
```

Expected outcome:

- `status` uses the same Orpheus readiness projection as `task ready` for the `Ready to run` group.
- The visible groups are `Unknown / needs attention`, `In review`, `Working`, and `Ready to run`; `--full` also shows empty or less-actionable groups such as `Blocked` and `Done / closed`.
- `$LOCAL_BLOCKED` appears under `Blocked` with a detail similar to `blocked by $LOCAL_BLOCKER`.
- `$MANAGED_REVIEW` appears under `In review` with its PR URL.
- `$MANAGED_WORKING` appears under `Working`.
- `$MANAGED_CLOSED_BLOCKER` appears under `Done / closed` only when using `--full`.
- The command is local-only: it reads the registry and Beads snapshots and does not require GitHub or network access.

## Validate partial repository failure diagnostics

Create a copy of the registry, append one broken local-Beads repo entry, and run read-only commands:

```bash
REGISTRY="$XDG_DATA_HOME/orpheus/registry.yaml"
cp -f "$REGISTRY" "$ROOT/registry.good.yaml"
cat >>"$REGISTRY" <<EOF
    - id: brokenm2
      name: brokenm2
      path: $ROOT/repos/brokenm2
      remote: git@example.com:org/brokenm2.git
      default_branch: main
      beads_mode: local
      beads_prefix: brokenm2
EOF

"$ORPHEUS" task list >"$ROOT/partial-list.out" 2>"$ROOT/partial-list.err" || true
"$ORPHEUS" task ready >"$ROOT/partial-ready.out" 2>"$ROOT/partial-ready.err" || true
"$ORPHEUS" status --full >"$ROOT/partial-status.out" 2>"$ROOT/partial-status.err" || true

cat "$ROOT/partial-list.out"
cat "$ROOT/partial-list.err"
cat "$ROOT/partial-ready.out"
cat "$ROOT/partial-ready.err"
cat "$ROOT/partial-status.out"
cat "$ROOT/partial-status.err"

cp -f "$ROOT/registry.good.yaml" "$REGISTRY"
```

Expected outcome:

- Each command returns a nonzero exit status.
- Successful repo rows are still printed for `localm2` and `managedm2`.
- Diagnostics identify the failed repository `brokenm2`, its prefix, source `task_backend`, and operation (`list` for `task list`; `snapshot` for `task ready` and `status`).
- `status --full` includes a `brokenm2` diagnostic row in `Unknown / needs attention` and writes the same structured repo failure to stderr.

## Automated coverage

`go test ./...` covers the same package boundaries through executable checks:

- CLI command output and partial-failure behavior in `internal/cli`.
- Local/managed Beads command invocation and JSON parsing in `internal/beads`.
- Cross-repo aggregation, active-item filtering, cloning, and diagnostics in `internal/task`.
- Prefix resolution, malformed ids, unknown prefixes, and ambiguity handling in `internal/task`.
- Local status grouping and readiness classification in `internal/status`.
- Registry/state path isolation in `internal/registry` and `internal/state`.

## Known MVP limitations

- M2 is read-only. It does not claim, assign, run agents, create branches/worktrees, close items, or mutate Orpheus metadata.
- `task ready` intentionally applies Orpheus' local `orpheus.pr_url` rule, so it can differ from backend-native `bd ready` for items already in review.
- Orpheus can only diagnose missing dependencies when the backend snapshot contains a stale or incomplete relation. The normal `bd create` path refuses nonexistent dependency ids, so this case is covered by automated fixtures rather than the happy-path manual setup above.
- Partial failure reporting is per repository. A completely unreadable registry still fails before per-repo aggregation can run.
- Managed Beads state is local to the operator's Orpheus data root and is not stored in the registered Git repository.
- Prefix resolution depends on the registered Beads prefix being unique; repo registration rejects duplicates, but manual registry edits can still create ambiguous configurations for diagnostic testing.
