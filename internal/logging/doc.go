// Package logging constructs application diagnostic loggers for Orpheus.
//
// The loggers built here are for Orpheus app diagnostics: command wiring,
// adapter operations, path decisions, durations, exit codes, and similar
// troubleshooting context. They are intentionally separate from future agent run
// logs, which will capture coding-agent process output and transcripts as user
// data under Orpheus run state.
//
// App diagnostic logs must remain safe and script-friendly: write them to
// stderr, keep command output on stdout, do not log secrets or wholesale
// environments, and keep user-facing errors actionable instead of replacing
// them with log messages.
package logging
