# Diagnostic file logging

Application diagnostics are separate from normal command output, agent
transcripts, raw subprocess output, and persisted task events.

File logging is disabled by default. To enable it, add this section to
`$XDG_CONFIG_HOME/orpheus/config.yaml` (normally `~/.config/orpheus/config.yaml`):

```yaml
logging:
  file:
    enabled: true
    level: debug
```

`level` selects the minimum severity: `debug`, `info`, `warn`, or `error`.
Omitting it defaults to `debug` (DEBUG and above). Unsupported values produce a
configuration error. Omitting the section or setting `enabled: false` creates no
log storage. Isolated `eval` commands do not load normal global configuration,
so they retain terminal diagnostics only.

Orpheus appends diagnostics to `$XDG_STATE_HOME/orpheus/orpheus.log`, defaulting to
`~/.local/state/orpheus/orpheus.log`. `XDG_STATE_HOME`, when set, must be an
absolute path. Existing config and data locations do not change. Newly created
log directories are owner-only (`0700`) and the log file is owner-only (`0600`),
subject to a more restrictive umask. Existing permissions are not changed.
Storage is created on the first event meeting the file threshold.

File and terminal filtering are independent. Stderr still receives INFO and
above, or DEBUG and above with `--verbose` / `-v`. Enabling a DEBUG file captures
debug diagnostics without requiring `--verbose`; selecting `error` for the file
does not suppress warnings or verbose diagnostics on stderr. An event meeting
both thresholds goes to both destinations. Stdout and command output are unchanged.

File events use UTC timestamps with milliseconds, aligned severity labels, the
process ID, optional `component/operation`, and trailing `key=value` details:

```text
2026-09-12 14:30:12.592Z DEBUG [18432] workflow/task_sync: task sync workflow finished | task_id=op-123 status=success duration_ms=247
```

Each event occupies one physical line. Strings are quoted and escaped when
needed; nested detail groups use dotted keys. There are no colors or source
locations. Parent and child Orpheus invocations append to the same file rather
than truncating it. If opening or writing the file fails, Orpheus warns once on
stderr and disables file output for that invocation without failing the command.

Cleanup is operator-managed: Orpheus does not rotate, retain, compress, or prune
this file. Remove or truncate it when no Orpheus command is active, or disable
file logging before cleanup. Diagnostic files can contain repository paths and
other operational context; inspect them before sharing.
