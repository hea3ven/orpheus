# M1 Repo Registration Validation

This note records the manual validation flow for Milestone 1: registering Beads-backed repositories, resolving their Beads directories, and confirming the machine-local registry state remains inspectable.

## Assumptions

- Run from the Orpheus repository root.
- `git`, `go`, and the real `bd` binary are on `PATH`.
- The commands use temporary repositories and isolated XDG roots; they must not read or mutate the operator's real Orpheus or Beads state.
- The snippets pipe empty stdin into `repo add` so Orpheus accepts detected defaults non-interactively. In an interactive terminal, `repo add` may prompt to confirm detected Git values and the managed Beads prefix; pressing Enter accepts the shown defaults.

## Setup

```bash
set -euo pipefail

ROOT="$(mktemp -d)"
export XDG_CONFIG_HOME="$ROOT/xdg-config"
export XDG_DATA_HOME="$ROOT/xdg-data"
ORPHEUS="$ROOT/bin/orpheus"

go build -o "$ORPHEUS" ./cmd/orpheus

git_repo() {
  repo="$1"
  mkdir -p "$repo"
  git -C "$repo" init
  git -C "$repo" checkout -b main
  git -C "$repo" \
    -c user.name='Orpheus Validation' \
    -c user.email='orpheus@example.com' \
    commit --allow-empty -m 'initial'
}

add_origin_head() {
  repo="$1"
  remote_name="$2"
  git -C "$repo" remote add origin "git@example.com:org/${remote_name}.git"
  git -C "$repo" update-ref refs/remotes/origin/main HEAD
  git -C "$repo" symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/main
}
```

## Local Beads registration flow

```bash
LOCAL="$ROOT/repos/localrepo"
git_repo "$LOCAL"
(cd "$LOCAL" && bd init --non-interactive --prefix local --skip-agents --skip-hooks --quiet)
add_origin_head "$LOCAL" localrepo

printf '' | "$ORPHEUS" repo add "$LOCAL"
"$ORPHEUS" repo list
LOCAL_BEADS_DIR="$($ORPHEUS repo beads-dir localrepo)"
PREFIX_BEADS_DIR="$($ORPHEUS repo beads-dir local)"
printf 'id/name resolved: %s\nprefix resolved: %s\n' "$LOCAL_BEADS_DIR" "$PREFIX_BEADS_DIR"
(cd "$LOCAL_BEADS_DIR" && bd list)
```

Expected outcome:

- `repo add` prints one tab-separated `Added repo ...` line containing `localrepo`, the repo path, `main`, `local`, and `local`.
- `repo list` includes the headers `ID`, `NAME`, `PATH`, `REMOTE`, `DEFAULT_BRANCH`, `BEADS_MODE`, and `BEADS_PREFIX`.
- `repo beads-dir localrepo` and `repo beads-dir local` both print the registered Git repo path.
- `bd list` runs successfully from the resolved directory.

## Managed Beads registration flow

```bash
MANAGED="$ROOT/repos/managedrepo"
git_repo "$MANAGED"
add_origin_head "$MANAGED" managedrepo

printf '' | "$ORPHEUS" repo add "$MANAGED"
"$ORPHEUS" repo list
MANAGED_BEADS_DIR="$($ORPHEUS repo beads-dir managedrepo)"
printf 'managed resolved: %s\n' "$MANAGED_BEADS_DIR"
(cd "$MANAGED_BEADS_DIR" && bd config get issue_prefix)
(cd "$MANAGED_BEADS_DIR" && bd list)
```

Expected outcome:

- `repo add` prints one `Added repo ...` line containing `managedrepo`, the repo path, `main`, `managed`, and the default managed prefix `managedrepo`.
- `repo list` shows both the local and managed repositories with their Beads modes and prefixes.
- `repo beads-dir managedrepo` prints `$XDG_DATA_HOME/orpheus/repos/managedrepo/beads`. In the non-interactive path the managed id/name/prefix are all `managedrepo`, so this single token exercises all three identifiers for the managed repo.
- `bd config get issue_prefix` reports `managedrepo`.
- `bd list` runs successfully from the managed Beads directory.

Optional distinct managed-prefix check, for an interactive terminal:

```bash
MANAGED_CUSTOM="$ROOT/repos/managedcustom"
git_repo "$MANAGED_CUSTOM"
add_origin_head "$MANAGED_CUSTOM" managedcustom
"$ORPHEUS" repo add "$MANAGED_CUSTOM"
# Press Enter for Git remote and default branch, then type: custommanaged
"$ORPHEUS" repo beads-dir custommanaged
```

Expected outcome: the final command prints `$XDG_DATA_HOME/orpheus/repos/managedcustom/beads`, proving a managed repo can resolve by a Beads prefix distinct from its id/name.

## Human-readable state files

```bash
REGISTRY="$XDG_DATA_HOME/orpheus/registry.yaml"
sed -n '1,120p' "$REGISTRY"
```

Expected outcome: the registry is plain YAML similar to:

```yaml
repos:
  - id: localrepo
    name: localrepo
    path: /tmp/.../repos/localrepo
    remote: git@example.com:org/localrepo.git
    default_branch: main
    beads_mode: local
    beads_prefix: local
  - id: managedrepo
    name: managedrepo
    path: /tmp/.../repos/managedrepo
    remote: git@example.com:org/managedrepo.git
    default_branch: main
    beads_mode: managed
    beads_prefix: managedrepo
```

## Duplicate and ambiguity checks

Run these in fresh temporary roots or after resetting `XDG_CONFIG_HOME`/`XDG_DATA_HOME`, because each scenario intentionally creates an invalid duplicate registration attempt.

### Duplicate derived id/name

```bash
DUP_ROOT="$(mktemp -d)"
export XDG_CONFIG_HOME="$DUP_ROOT/xdg-config"
export XDG_DATA_HOME="$DUP_ROOT/xdg-data"
git_repo "$DUP_ROOT/one/collide"
add_origin_head "$DUP_ROOT/one/collide" collide-one
git_repo "$DUP_ROOT/two/collide"
add_origin_head "$DUP_ROOT/two/collide" collide-two
printf '' | "$ORPHEUS" repo add "$DUP_ROOT/one/collide"
printf '' | "$ORPHEUS" repo add "$DUP_ROOT/two/collide"  # expected failure
```

Expected failure includes `duplicate repo id "collide"`. Because M1 derives both id and name from the repository directory basename, this validates the derived id/name collision path.

### Duplicate path

```bash
PATH_ROOT="$(mktemp -d)"
export XDG_CONFIG_HOME="$PATH_ROOT/xdg-config"
export XDG_DATA_HOME="$PATH_ROOT/xdg-data"
git_repo "$PATH_ROOT/repos/samepath"
add_origin_head "$PATH_ROOT/repos/samepath" samepath
printf '' | "$ORPHEUS" repo add "$PATH_ROOT/repos/samepath"
printf '' | "$ORPHEUS" repo add "$PATH_ROOT/repos/samepath/."  # expected failure
```

Expected failure includes `duplicate repo path`.

### Duplicate Beads prefix

```bash
PREFIX_ROOT="$(mktemp -d)"
export XDG_CONFIG_HOME="$PREFIX_ROOT/xdg-config"
export XDG_DATA_HOME="$PREFIX_ROOT/xdg-data"
git_repo "$PREFIX_ROOT/repos/prefixone"
(cd "$PREFIX_ROOT/repos/prefixone" && bd init --non-interactive --prefix shared --skip-agents --skip-hooks --quiet)
add_origin_head "$PREFIX_ROOT/repos/prefixone" prefixone
git_repo "$PREFIX_ROOT/repos/prefixtwo"
(cd "$PREFIX_ROOT/repos/prefixtwo" && bd init --non-interactive --prefix shared --skip-agents --skip-hooks --quiet)
add_origin_head "$PREFIX_ROOT/repos/prefixtwo" prefixtwo
printf '' | "$ORPHEUS" repo add "$PREFIX_ROOT/repos/prefixone"
printf '' | "$ORPHEUS" repo add "$PREFIX_ROOT/repos/prefixtwo"  # expected failure
```

Expected failure includes `duplicate beads prefix "shared"`.

### ID/name/prefix cross-collisions

Prefix colliding with an existing id:

```bash
CROSS_ROOT="$(mktemp -d)"
export XDG_CONFIG_HOME="$CROSS_ROOT/xdg-config"
export XDG_DATA_HOME="$CROSS_ROOT/xdg-data"
git_repo "$CROSS_ROOT/repos/claimed"
add_origin_head "$CROSS_ROOT/repos/claimed" claimed
printf '' | "$ORPHEUS" repo add "$CROSS_ROOT/repos/claimed"
git_repo "$CROSS_ROOT/repos/other"
(cd "$CROSS_ROOT/repos/other" && bd init --non-interactive --prefix claimed --skip-agents --skip-hooks --quiet)
add_origin_head "$CROSS_ROOT/repos/other" other
printf '' | "$ORPHEUS" repo add "$CROSS_ROOT/repos/other"  # expected failure
```

Expected failure includes `repo beads_prefix "claimed" collides with repo[0] id`.

Derived id/name colliding with an existing prefix:

```bash
CROSS_ROOT_2="$(mktemp -d)"
export XDG_CONFIG_HOME="$CROSS_ROOT_2/xdg-config"
export XDG_DATA_HOME="$CROSS_ROOT_2/xdg-data"
git_repo "$CROSS_ROOT_2/repos/source"
(cd "$CROSS_ROOT_2/repos/source" && bd init --non-interactive --prefix taken --skip-agents --skip-hooks --quiet)
add_origin_head "$CROSS_ROOT_2/repos/source" source
printf '' | "$ORPHEUS" repo add "$CROSS_ROOT_2/repos/source"
git_repo "$CROSS_ROOT_2/repos/taken"
add_origin_head "$CROSS_ROOT_2/repos/taken" taken
printf '' | "$ORPHEUS" repo add "$CROSS_ROOT_2/repos/taken"  # expected failure
```

Expected failure includes `repo id "taken" collides with repo[0] beads_prefix`.

## Verbose diagnostics

Reset to the main validation state if you ran the duplicate scenarios first:

```bash
export XDG_CONFIG_HOME="$ROOT/xdg-config"
export XDG_DATA_HOME="$ROOT/xdg-data"
```

Default output should be script-friendly:

```bash
"$ORPHEUS" repo beads-dir localrepo >"$ROOT/stdout.default" 2>"$ROOT/stderr.default"
cat "$ROOT/stdout.default"
cat "$ROOT/stderr.default"
```

Expected outcome: stdout contains only the resolved path and stderr is empty.

Verbose mode should emit debug diagnostics to stderr without changing stdout:

```bash
"$ORPHEUS" --verbose repo beads-dir localrepo >"$ROOT/stdout.verbose" 2>"$ROOT/stderr.verbose"
cat "$ROOT/stdout.verbose"
cat "$ROOT/stderr.verbose"
```

Expected outcome:

- stdout still contains only the resolved path.
- stderr contains `level=DEBUG`, `operation=repo_beads_dir`, the requested token, the resolved `repo_id`, `beads_mode`, `beads_prefix`, and `beads_dir`.

## Automated coverage

`go test ./...` covers the same package boundaries through executable checks:

- CLI orchestration and command output in `internal/cli`.
- Registry persistence, duplicate validation, and resolver ambiguity in `internal/registry`.
- State path resolution and YAML read/write behavior in `internal/state`.
- Local/managed Beads adapter behavior in `internal/beads`.
- Git root, remote, and default-branch inspection in `internal/git`.
- Diagnostic logging defaults and verbose stderr behavior in `internal/logging`.

## Known MVP limitations

- M1 only registers repositories and resolves Beads directories; global task listing starts in M2.
- Managed Beads state is local to the operator's Orpheus data root and is not stored in the registered Git repository.
- Non-interactive `repo add` accepts detected Git values and the derived managed prefix; overriding those values requires the interactive wizard or manually editing the YAML registry.
- Git remote URLs and default branches are inspected from local metadata only. Orpheus does not contact the remote during M1 registration.
- Duplicate ids/names are based on the repository directory basename until a later milestone introduces richer naming or edit commands.
