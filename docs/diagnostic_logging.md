# Diagnostic Logging

Orpheus has two distinct kinds of logs:

- **Application diagnostic logs** help troubleshoot Orpheus itself: CLI wiring,
  adapter operations, paths, durations, exit codes, and similar implementation
  details.
- **Agent run logs** are future user-facing records of coding-agent process
  output, transcripts, and run artifacts. They are not implemented by the MVP
  diagnostic logging foundation.

Application diagnostics are configured by the root CLI. In the MVP, `--verbose`
(or `-v`) enables debug-level diagnostics. Diagnostics are written to stderr so
stdout remains available for command output that may be used by scripts.

The `internal/logging` package only constructs standard-library `*slog.Logger`
values from simple CLI-level configuration. Other internal packages should accept
`*slog.Logger` directly, or use `logging.Discard()` in tests, rather than
coupling themselves to CLI flag parsing.

Safety guidelines:

- Do not log environment variables wholesale.
- Do not log secrets, tokens, or credentials.
- Do not log full command output at info level.
- Keep user-facing errors actionable; do not replace them with log-only details.
- Prefer structured fields such as `component`, `operation`, `path`/`cwd`,
  `duration`, and `exit_code` where useful.

M1 intentionally does not introduce log files, daemon logging, JSON log format
flags, or agent run log behavior.
