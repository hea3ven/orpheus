# Orpheus

**Orpheus brings structure, visibility, and control to AI coding-agent orchestration.**

Orpheus is a CLI-first orchestration layer for coordinating AI coding agents across tasks, worktrees, and pull requests while keeping the human operator in control.

Inspired by the mythic Orpheus charming wild forces into motion, this project focuses on taming scattered agent runs into a predictable development workflow.

## Project status

Orpheus is an unreleased MVP, currently used only by its author. It has not been
published or released. Commands, configuration, and local state formats can
change without backward-compatibility guarantees. Do not add compatibility layers
or preserve obsolete behavior solely for hypothetical existing users.

Documentation covers the essentials of the current implementation. A completeness
pass is deferred until preparation for the first release.

## Build and use

From this checkout, with Go installed:

```sh
go install ./cmd/orpheus
orpheus --help
```

Ensure the Go install directory is on `PATH`. Runtime workflows need Git, Beads
`bd`, and a configured coding-agent executable. Pull-request publication also
needs authenticated GitHub CLI `gh` access. Configure an [agent profile](docs/user/agents.md)
before running work.

```sh
orpheus repo add /path/to/repository
orpheus status
orpheus task run <task-id>
```

`task run` advances implementation, review, repair, and publication. Use
`orpheus task sync <task-id>` to reconcile a published PR. See the guides for
configuration and recovery rather than running historical validation recipes.

## Documentation

- [Tasks](docs/user/tasks.md): registration, inventory, execution, branch names, stats, and recovery.
- [Reviews](docs/user/reviews.md): approval, pipelines, findings, repairs, and optional experiments.
- [Publication](docs/user/publication.md): PRs, direct merges, titles, and sync.
- [Agent profiles](docs/user/agents.md): Pi, Codex, and custom command configuration.
- [Documentation index](docs/README.md): developer guides, agent instructions, and archive access.

## Development

Follow [AGENTS.md](AGENTS.md) for repository working rules and [CONTEXT.md](CONTEXT.md)
for domain vocabulary. Run `make check` after code changes; it formats, runs both
test lanes once through `make quality`, lints, and builds. Documentation-only edits
and read-only analysis do not require these checks. See
[AGENTS.md](AGENTS.md#validation) for validation scope and
[testing](docs/developer/testing.md) for prerequisites and quality-policy updates.

Routine validation is network-free, credential-free, isolated from operator data,
and prevents real model-agent execution. `orpheus eval review-context` is a
separate live evaluation that may incur costs; never run it implicitly.

## Shell completion

Orpheus generates completion scripts but never changes your shell configuration.
Completion is read-only and best-effort: unavailable repositories or configuration
simply yield fewer suggestions.

Temporary activation:

```sh
# Bash
source <(orpheus completion bash)
# Zsh
source <(orpheus completion zsh)
# Fish
orpheus completion fish | source
```

```powershell
# PowerShell
orpheus completion powershell | Out-String | Invoke-Expression
```

Persistent activation:

```sh
# Bash (bash-completion user directory)
mkdir -p "${BASH_COMPLETION_USER_DIR:-$HOME/.local/share/bash-completion/completions}"
orpheus completion bash > "${BASH_COMPLETION_USER_DIR:-$HOME/.local/share/bash-completion/completions}/orpheus"

# Zsh (add the fpath and compinit lines to ~/.zshrc if not already present)
mkdir -p ~/.zfunc
orpheus completion zsh > ~/.zfunc/_orpheus
printf '%s\n' 'fpath=(~/.zfunc $fpath)' 'autoload -Uz compinit && compinit' >> ~/.zshrc

# Fish
mkdir -p ~/.config/fish/completions
orpheus completion fish > ~/.config/fish/completions/orpheus.fish
```

```powershell
# PowerShell: create the profile directory and file only if missing, then append the startup command.
$profileDirectory = Split-Path -Parent $PROFILE
if (-not (Test-Path -LiteralPath $profileDirectory)) {
  New-Item -ItemType Directory -Path $profileDirectory -Force | Out-Null
}
if (-not (Test-Path -LiteralPath $PROFILE)) {
  New-Item -ItemType File -Path $PROFILE | Out-Null
}
Add-Content -LiteralPath $PROFILE 'orpheus completion powershell | Out-String | Invoke-Expression'
```

## License

MIT. See [LICENSE](LICENSE).
