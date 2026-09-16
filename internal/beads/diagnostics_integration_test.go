//go:build integration

package beads_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/beads"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationLocalInspectionDiagnosticsCorrelateCommandsWithRepository(t *testing.T) {
	t.Parallel()
	root := testutil.CanonicalTempDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".beads"), 0o755))
	binary := aDiagnosticBD(t, `case "$*" in
 *context) printf '{"beads_dir":"%s/.beads"}' "$PWD" ;;
 *issue_prefix) printf '{"key":"issue_prefix","value":"op"}' ;;
 *) exit 99 ;;
 esac`)
	var diagnostics bytes.Buffer
	runner := beads.NewInspectLocalRunner(logging.New(&diagnostics, logging.Config{Verbose: true}), slog.String("repo_id", "alpha")).(beads.CommandRunner)
	runner.Binary = binary

	_, err := beads.InspectLocalWithRunner(root, runner)

	require.NoError(t, err)
	for _, operation := range []string{"context", "config"} {
		assertBeadsDiagnostic(t, diagnostics.String(), "beads command finished", "operation="+operation, "repo_id=alpha", "exit_code=0", "status=success")
	}
}

func TestIntegrationLocalInspectionDiagnosticsClassifyMissingWorkspaceAsExpectedAbsence(t *testing.T) {
	t.Parallel()
	root := testutil.CanonicalTempDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".beads"), 0o755))
	binary := aDiagnosticBD(t, `printf '{"error":"no_beads_directory","message":"No active beads workspace found."}'; exit 1`)
	var diagnostics bytes.Buffer
	runner := beads.NewInspectLocalRunner(logging.New(&diagnostics, logging.Config{Verbose: true}), slog.String("repo_id", "alpha")).(beads.CommandRunner)
	runner.Binary = binary

	_, err := beads.InspectLocalWithRunner(root, runner)

	assert.ErrorIs(t, err, beads.ErrNoLocal)
	assertBeadsDiagnostic(t, diagnostics.String(), "beads command finished", "operation=context", "repo_id=alpha", "status=expected_absence", "exit_code=1")
}

func TestIntegrationBeadsCommandDiagnosticsExcludeArgumentsEnvironmentAndOutput(t *testing.T) {
	t.Parallel()
	binary := aDiagnosticBD(t, "printf 'SECRET_STDOUT'; printf 'SECRET_STDERR' >&2; exit 7")
	var diagnostics bytes.Buffer
	runner := beads.CommandRunner{Binary: binary, Logger: logging.New(&diagnostics, logging.Config{Verbose: true}), DiagnosticAttrs: []slog.Attr{slog.String("repo_id", "alpha")}, Environment: []string{"SECRET_ENV=private"}}

	result, err := runner.Run(testutil.CanonicalTempDir(t), "init", "--prefix", "SECRET_ARGUMENT")

	require.Error(t, err)
	assert.Equal(t, "SECRET_STDOUT", result.Stdout)
	assert.Equal(t, "SECRET_STDERR", result.Stderr)
	assertBeadsDiagnostic(t, diagnostics.String(), "beads command finished", "operation=init", "repo_id=alpha", "status=failure", "exit_code=7", "duration_ms=")
	for _, secret := range []string{"SECRET_STDOUT", "SECRET_STDERR", "SECRET_ARGUMENT", "SECRET_ENV", "--prefix"} {
		assert.NotContains(t, diagnostics.String(), secret)
	}
}

func aDiagnosticBD(t *testing.T, body string) string {
	t.Helper()
	binary := filepath.Join(testutil.CanonicalTempDir(t), "bd")
	require.NoError(t, testguard.WriteExecutable(binary, []byte("#!/bin/sh\n"+body+"\n")))
	return binary
}

func assertBeadsDiagnostic(t *testing.T, diagnostics string, fields ...string) {
	t.Helper()
	for _, line := range strings.Split(diagnostics, "\n") {
		matches := true
		for _, field := range fields {
			if !strings.Contains(line, field) {
				matches = false
				break
			}
		}
		if matches {
			return
		}
	}
	require.Fail(t, "missing diagnostic line", "fields: %q\ndiagnostics:\n%s", fields, diagnostics)
}
