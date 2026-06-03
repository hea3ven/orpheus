# M3 Interactive Single Task Dispatch Validation

This note records the final end-to-end validation flow for Milestone 3: attached single-task dispatch with deterministic task branches/worktrees and local Orpheus run state.

## Assumptions

- Run from the Orpheus repository root.
- `git`, `go`, and the real `bd` binary are on `PATH`.
- The commands use temporary repositories and isolated XDG roots; they must not read or mutate the operator's real Orpheus or Beads state.
- The validation uses one repo-local Beads test repository and one direct executable agent shim.
- M3 dispatch is attached: agent stdout/stderr stream to the invoking terminal. This note does not require `orpheus task logs`, commits, PR creation, background runners, daemons, tmux nodes, or `orpheus agent done`.

## Setup isolated roots, Orpheus binary, and test repo

```bash
set -euo pipefail

ROOT="$(mktemp -d)"
export XDG_CONFIG_HOME="$ROOT/xdg-config"
export XDG_DATA_HOME="$ROOT/xdg-data"
ORPHEUS="$ROOT/bin/orpheus"
REPO="$ROOT/repos/dispatchm3"
ORIGIN="$ROOT/remotes/dispatchm3.git"
AGENT="$ROOT/bin/validation-agent"
AGENT_LOG="$ROOT/agent.log"
HOLD_RELEASE="$ROOT/release-hold-agent"
ENV_FILE="$ROOT/env.sh"

mkdir -p "$ROOT/bin" "$ROOT/repos" "$ROOT/remotes"
go build -o "$ORPHEUS" ./cmd/orpheus

git init --bare -q "$ORIGIN"
git clone -q "$ORIGIN" "$REPO"
git -C "$REPO" checkout -q -b main
git -C "$REPO" \
  -c user.name='Orpheus Validation' \
  -c user.email='orpheus@example.com' \
  commit --allow-empty -q -m 'initial'
git -C "$REPO" push -q -u origin main
git -C "$REPO" symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/main

(
  cd "$REPO"
  BD_NON_INTERACTIVE=1 bd init \
    --non-interactive \
    --prefix m3 \
    --skip-agents \
    --skip-hooks \
    --quiet
)

printf '' | "$ORPHEUS" repo add "$REPO"
"$ORPHEUS" repo list
BEADS_DIR="$("$ORPHEUS" repo beads-dir m3)"
printf 'root=%s\nconfig=%s\ndata=%s\nbeads=%s\nenv_file=%s\n' \
  "$ROOT" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$BEADS_DIR" "$ENV_FILE"
```

Expected outcome:

- The Orpheus config root is `$XDG_CONFIG_HOME/orpheus` and the data root is `$XDG_DATA_HOME/orpheus`, both under `$ROOT`.
- `repo add` registers `dispatchm3` with default branch `main`, Beads mode `local`, and Beads prefix `m3`.
- `repo beads-dir m3` resolves to the temporary registered repository path, not the operator's real repository.

Save the environment for the second-terminal working-status check later:

```bash
{
  printf 'export ROOT=%q\n' "$ROOT"
  printf 'export XDG_CONFIG_HOME=%q\n' "$XDG_CONFIG_HOME"
  printf 'export XDG_DATA_HOME=%q\n' "$XDG_DATA_HOME"
  printf 'export ORPHEUS=%q\n' "$ORPHEUS"
  printf 'export REPO=%q\n' "$REPO"
  printf 'export BEADS_DIR=%q\n' "$BEADS_DIR"
  printf 'export AGENT_LOG=%q\n' "$AGENT_LOG"
  printf 'export HOLD_RELEASE=%q\n' "$HOLD_RELEASE"
  printf 'export ENV_FILE=%q\n' "$ENV_FILE"
} >"$ENV_FILE"
```

## Create representative Beads tasks

```bash
run_bd() {
  dir="$1"
  shift
  (cd "$dir" && BD_NON_INTERACTIVE=1 bd "$@")
}

FAIL_TASK="$(
  run_bd "$BEADS_DIR" create 'M3 retry after failed attached run' \
    --type task \
    --priority P1 \
    --description 'Fail once, then retry with the same deterministic branch/worktree.' \
    --acceptance 'The retry records attempt 2 and reuses the same Orpheus metadata.' \
    --silent
)"
WORKING_TASK="$(
  run_bd "$BEADS_DIR" create 'M3 attached working status' \
    --type task \
    --priority P1 \
    --description 'Hold an attached process open long enough to inspect status.' \
    --acceptance 'Status projects the task into Working while the agent is attached.' \
    --silent
)"
READY_TASK="$(run_bd "$BEADS_DIR" create 'M3 ready task' --type task --priority P2 --silent)"
BLOCKED_TASK="$(run_bd "$BEADS_DIR" create 'M3 blocked task' --type task --priority P2 --silent)"
BLOCKER_TASK="$(run_bd "$BEADS_DIR" create 'M3 active blocker' --type task --deps "blocks:$BLOCKED_TASK" --priority P2 --silent)"
REVIEW_TASK="$(
  run_bd "$BEADS_DIR" create 'M3 in review projection' \
    --type task \
    --priority P3 \
    --metadata '{"orpheus.pr_url":"https://example.test/pull/303"}' \
    --silent
)"
UNKNOWN_TASK="$(run_bd "$BEADS_DIR" create 'M3 task state diagnostic' --type task --priority P3 --silent)"

printf 'fail=%s\nworking=%s\nready=%s\nblocked=%s\nblocker=%s\nreview=%s\nunknown=%s\n' \
  "$FAIL_TASK" "$WORKING_TASK" "$READY_TASK" "$BLOCKED_TASK" "$BLOCKER_TASK" "$REVIEW_TASK" "$UNKNOWN_TASK"

{
  printf 'export FAIL_TASK=%q\n' "$FAIL_TASK"
  printf 'export WORKING_TASK=%q\n' "$WORKING_TASK"
  printf 'export READY_TASK=%q\n' "$READY_TASK"
  printf 'export BLOCKED_TASK=%q\n' "$BLOCKED_TASK"
  printf 'export BLOCKER_TASK=%q\n' "$BLOCKER_TASK"
  printf 'export REVIEW_TASK=%q\n' "$REVIEW_TASK"
  printf 'export UNKNOWN_TASK=%q\n' "$UNKNOWN_TASK"
} >>"$ENV_FILE"
```

Expected outcome:

- The IDs use the registered Beads prefix, for example `m3-...`.
- `FAIL_TASK`, `WORKING_TASK`, and `READY_TASK` are open runnable tasks.
- `BLOCKED_TASK` has an active blocker, `BLOCKER_TASK`.
- `REVIEW_TASK` has only `orpheus.pr_url` metadata and is still open.
- `UNKNOWN_TASK` is reserved for the practical unknown-state diagnostic later.

## Configure direct executable agent profiles

Create a small executable that records its arguments, prompt, current directory, Git branch, and Orpheus runtime environment:

```bash
cat >"$AGENT" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

mode=""
log=""
release=""
prompt=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --mode)
      mode="$2"
      shift 2
      ;;
    --log)
      log="$2"
      shift 2
      ;;
    --release)
      release="$2"
      shift 2
      ;;
    --prompt)
      prompt="$2"
      shift 2
      ;;
    *)
      echo "unexpected argument: $1" >&2
      exit 64
      ;;
  esac
done

{
  printf 'mode=%s\n' "$mode"
  printf 'pwd=%s\n' "$PWD"
  printf 'branch=%s\n' "$(git branch --show-current)"
  printf 'ORPHEUS_REPO_ID=%s\n' "$ORPHEUS_REPO_ID"
  printf 'ORPHEUS_TASK_ID=%s\n' "$ORPHEUS_TASK_ID"
  printf 'ORPHEUS_WORKTREE=%s\n' "$ORPHEUS_WORKTREE"
  printf 'ORPHEUS_BRANCH=%s\n' "$ORPHEUS_BRANCH"
  printf 'ORPHEUS_AGENT_PROMPT<<END\n%s\nEND\n' "$ORPHEUS_AGENT_PROMPT"
  printf 'ARG_PROMPT<<END\n%s\nEND\n' "$prompt"
} >>"$log"

printf 'attached stdout mode=%s\n' "$mode"
printf 'attached stderr mode=%s\n' "$mode" >&2

case "$mode" in
  pass)
    exit 0
    ;;
  fail)
    exit 7
    ;;
  hold)
    while [ ! -f "$release" ]; do
      sleep 1
    done
    exit 0
    ;;
  *)
    echo "unknown mode: $mode" >&2
    exit 65
    ;;
esac
SH
chmod +x "$AGENT"

mkdir -p "$XDG_CONFIG_HOME/orpheus"
cat >"$XDG_CONFIG_HOME/orpheus/config.yaml" <<EOF
default_agent: pass
agents:
  pass:
    command: "$AGENT"
    args:
      - "--mode"
      - "pass"
      - "--log"
      - "$AGENT_LOG"
      - "--prompt"
      - "{{prompt}}"
  fail:
    command: "$AGENT"
    args:
      - "--mode"
      - "fail"
      - "--log"
      - "$AGENT_LOG"
      - "--prompt"
      - "{{prompt}}"
  hold:
    command: "$AGENT"
    args:
      - "--mode"
      - "hold"
      - "--log"
      - "$AGENT_LOG"
      - "--release"
      - "$HOLD_RELEASE"
      - "--prompt"
      - "{{prompt}}"
EOF
```

Expected outcome:

- The profile uses a direct executable path in `command`; Orpheus does not invoke an implicit shell.
- Each profile passes a structured `args` array.
- The `{{prompt}}` token appears in an argument and should be replaced with the rendered M3 dispatch prompt before launch.
- `pass` is the default profile; `fail` and `hold` are explicit `--agent` selections.

## Validate initial ready/status projections

```bash
"$ORPHEUS" task list --details
"$ORPHEUS" task ready --details
"$ORPHEUS" status --full
```

Expected outcome:

- `task list --details` shows all active tasks from the registered test repo.
- `task ready --details` includes open runnable tasks such as `$FAIL_TASK`, `$WORKING_TASK`, `$READY_TASK`, `$BLOCKER_TASK`, and `$UNKNOWN_TASK`.
- `task ready --details` excludes `$BLOCKED_TASK` because it is blocked and `$REVIEW_TASK` because it has `orpheus.pr_url`.
- `status --full` shows:
  - `$REVIEW_TASK` in `In review` with `https://example.test/pull/303`.
  - `$READY_TASK` in `Ready to run`.
  - `$BLOCKED_TASK` in `Blocked` with detail similar to `blocked by $BLOCKER_TASK`.

## Run a failing attached dispatch

```bash
EXPECTED_BRANCH="orpheus/$FAIL_TASK"
EXPECTED_WORKTREE="$XDG_DATA_HOME/orpheus/repos/dispatchm3/worktrees/$FAIL_TASK"
STATE_FILE="$XDG_DATA_HOME/orpheus/repos/dispatchm3/tasks/$FAIL_TASK.yaml"

if "$ORPHEUS" task run --agent fail "$FAIL_TASK"; then
  echo 'expected failing agent to exit non-zero' >&2
  exit 1
fi
```

Expected outcome:

- The agent runs attached to the terminal: `attached stdout mode=fail` appears on stdout and `attached stderr mode=fail` appears on stderr.
- The command exits nonzero because the selected agent profile exits `7`.
- The deterministic branch is `orpheus/$FAIL_TASK`.
- The deterministic worktree is `$XDG_DATA_HOME/orpheus/repos/dispatchm3/worktrees/$FAIL_TASK`.

Validate worktree, prompt interpolation, runtime environment, task metadata, and failed run state:

```bash
test "$(git -C "$EXPECTED_WORKTREE" branch --show-current)" = "$EXPECTED_BRANCH"
grep -F "mode=fail" "$AGENT_LOG"
grep -F "pwd=$EXPECTED_WORKTREE" "$AGENT_LOG"
grep -F "branch=$EXPECTED_BRANCH" "$AGENT_LOG"
grep -F "ORPHEUS_REPO_ID=dispatchm3" "$AGENT_LOG"
grep -F "ORPHEUS_TASK_ID=$FAIL_TASK" "$AGENT_LOG"
grep -F "ORPHEUS_WORKTREE=$EXPECTED_WORKTREE" "$AGENT_LOG"
grep -F "ORPHEUS_BRANCH=$EXPECTED_BRANCH" "$AGENT_LOG"
grep -F 'You are an attached implementation agent dispatched by Orpheus.' "$AGENT_LOG"
grep -F -- "- ID: $FAIL_TASK" "$AGENT_LOG"
grep -F -- "- Deterministic branch: $EXPECTED_BRANCH" "$AGENT_LOG"
grep -F -- "- Deterministic worktree: $EXPECTED_WORKTREE" "$AGENT_LOG"

"$ORPHEUS" task show "$FAIL_TASK"
cat "$STATE_FILE"
"$ORPHEUS" status
```

Expected outcome:

- The worktree exists and is checked out on `$EXPECTED_BRANCH`.
- `ARG_PROMPT` in `$AGENT_LOG` contains the rendered task prompt, proving `{{prompt}}` interpolation occurred.
- `ORPHEUS_AGENT_PROMPT` contains the same prompt in the runtime environment.
- The other runtime variables are present: `ORPHEUS_REPO_ID`, `ORPHEUS_TASK_ID`, `ORPHEUS_WORKTREE`, and `ORPHEUS_BRANCH`.
- `task show` reports backend status `in_progress` and Orpheus metadata:
  - Branch: `$EXPECTED_BRANCH`
  - Worktree: `$EXPECTED_WORKTREE`
  - PR: `-`
- `$STATE_FILE` is a per-task YAML file under the isolated Orpheus data root.
- The state file contains attempt `1`, status `failed`, the selected agent profile `fail`, the direct command path, resolved args, and trace events:
  - `worktree_created`
  - `run_started`
  - `run_finished` with status `failed`
- `status` projects `$FAIL_TASK` into `Failed / needs retry` with detail similar to `run attempt 1 failed`.

## Retry using the same branch/worktree

Retry the same task with the default `pass` profile. The task is already `in_progress` with matching Orpheus branch/worktree metadata, so M3 should reuse the deterministic target and append a new attempt.

```bash
"$ORPHEUS" task run "$FAIL_TASK"

cat "$STATE_FILE"
"$ORPHEUS" status
```

Expected outcome:

- The retry runs attached and exits successfully.
- The task remains tied to the same `$EXPECTED_BRANCH` and `$EXPECTED_WORKTREE`.
- The state file now contains attempt `2` with status `succeeded`.
- The event stream appends:
  - `worktree_reused`
  - `run_started`
  - `run_finished` with status `succeeded`
- `status` no longer shows the task in `Failed / needs retry`; because M3 does not infer implementation completion, it projects `$FAIL_TASK` into `Idle` with detail similar to `run attempt 2 succeeded; M3 does not infer implementation completion`.

## Validate Working while an attached agent is still running

This step uses two terminals and no background runner. Terminal A stays attached to the agent. Terminal B only observes the isolated state.

In terminal A, start the holding profile. If this is not the original setup shell, first source the `env_file` path printed during setup.

```bash
# Optional when using a fresh shell: . /tmp/.../env.sh
rm -f "$HOLD_RELEASE"
"$ORPHEUS" task run --agent hold "$WORKING_TASK"
```

Expected terminal A behavior:

- The command prints `attached stdout mode=hold` and `attached stderr mode=hold`.
- The process stays attached and does not return until `$HOLD_RELEASE` is created.

While terminal A is still attached, run this in terminal B, replacing the path with the `env_file` value printed during setup:

```bash
. /tmp/.../env.sh
"$ORPHEUS" status
"$ORPHEUS" task show "$WORKING_TASK"
```

Expected terminal B outcome:

- `status` projects `$WORKING_TASK` into `Working` with detail similar to `run attempt 1 is running`.
- `task show` shows backend status `in_progress` with branch `orpheus/$WORKING_TASK` and worktree `$XDG_DATA_HOME/orpheus/repos/dispatchm3/worktrees/$WORKING_TASK`.

Release the attached process from terminal B:

```bash
touch "$HOLD_RELEASE"
```

Expected terminal A outcome:

- The attached `task run --agent hold` command exits successfully.
- A later `"$ORPHEUS" status` projects `$WORKING_TASK` into `Idle` because its latest attempt succeeded and M3 has no completion/PR-ready handshake yet.

## Validate practical Unknown diagnostics

A normal Beads create/update flow refuses many malformed dependency states, so this practical unknown check corrupts only an isolated per-task Orpheus state file and then removes it.

```bash
UNKNOWN_STATE="$XDG_DATA_HOME/orpheus/repos/dispatchm3/tasks/$UNKNOWN_TASK.yaml"
mkdir -p "$(dirname "$UNKNOWN_STATE")"
printf 'not: [valid\n' >"$UNKNOWN_STATE"

if "$ORPHEUS" status --full >"$ROOT/status-unknown.out" 2>"$ROOT/status-unknown.err"; then
  echo 'expected status to report the invalid task state file' >&2
  exit 1
fi
cat "$ROOT/status-unknown.out"
cat "$ROOT/status-unknown.err"

rm -f "$UNKNOWN_STATE"
```

Expected outcome:

- `status --full` returns nonzero because one local task state file cannot be read.
- The `Unknown / needs attention` group contains a diagnostic row for repo `dispatchm3`.
- Stderr contains a structured failure with `source=task_state` and `operation=latest_run`.
- Removing `$UNKNOWN_STATE` restores normal status projection.

## Final status projection checklist

After releasing the holding agent and removing the intentionally invalid unknown-state file, run:

```bash
"$ORPHEUS" status --full
"$ORPHEUS" task ready --details
```

Expected outcome:

- `Failed / needs retry` is empty after the successful retry.
- `Working` is empty after the holding agent exits.
- `Idle` includes `$FAIL_TASK` and `$WORKING_TASK` because their latest attached attempts succeeded but M3 does not infer implementation completion.
- `In review` includes `$REVIEW_TASK` because `orpheus.pr_url` is set.
- `Ready to run` includes `$READY_TASK`, `$BLOCKER_TASK`, and `$UNKNOWN_TASK`.
- `Blocked` includes `$BLOCKED_TASK` when using `--full`.
- `Done / closed` may be empty because this M3 flow does not require closing tasks.
- `task ready --details` uses the same local projection and excludes failed, working, idle, blocked, and in-review tasks.

## Automated coverage

`go test ./...` covers the same M3 package boundaries through executable checks:

- Agent profile loading, validation, direct command resolution, and `{{prompt}}` interpolation in `internal/agent`.
- Attached process launch and runtime environment wiring in `internal/cli` tests.
- Deterministic branch/worktree creation, reuse, repo-root mode, and safety checks in `internal/git`.
- Beads `MarkInProgress` mutation and metadata conflict handling in `internal/beads`.
- Per-task run attempts and trace event persistence in `internal/taskstate`.
- M3 status projection over local run state in `internal/status` and `internal/cli`.

## Known MVP limitations validated by scope

- M3 streams attached agent output; it does not require log capture or `task logs` in this validation note.
- M3 records run success/failure but does not interpret a successful run as implementation complete.
- M3 does not require `orpheus agent done`, Orpheus-owned commits, PR creation, background execution, daemon behavior, or tmux/runner nodes.
- A stale `running` attempt cannot be reconciled automatically in M3; the state file is intentionally human-readable for manual repair.
- The deterministic task branch/worktree path is local machine state and is stored in Beads metadata for operator visibility.
