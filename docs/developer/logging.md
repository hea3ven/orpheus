# Diagnostic logging

Application diagnostics troubleshoot Orpheus operations, paths, durations, and
exit codes. They are separate from agent output, harness session logs, and
persisted task audit events.

The root CLI configures a standard-library `*slog.Logger`. `--verbose` or `-v`
enables debug diagnostics on stderr, leaving stdout for normal command output
and JSON. The logging package does not provide stored agent transcripts or a
`task logs` command.

`internal/logging` constructs loggers and provides operation spans, status fields,
and exit-code helpers. Other packages accept `*slog.Logger` directly rather than
parsing CLI flags. Use `logging.Discard()` when a logger is required in tests.

Safety rules:

- Do not log environment variables wholesale, secrets, tokens, or credentials.
- Do not log full command output at info level.
- Return actionable errors; do not replace them with log-only diagnostics.
- Prefer structured operation, component, path, duration, and exit-code fields.

Do not write logging-only tests or assert diagnostic text. Persisted task events
and usage records are application data and remain testable contracts. See
[testing](testing.md#behavioral-assertions).
