//go:build integration

//nolint:testpackage // Shared invocation-scoped test fixtures.
package cli

import (
	"fmt"
	"github.com/hea3ven/orpheus/internal/testutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeGHPRResponses struct {
	listStdout   string
	listExit     int
	createStdout string
	createExit   int
	statusStdout string
	statusExit   int
}

const fakeGHPRScriptHeader = `#!/bin/sh
{
  pwd
  printf 'ARGC=%%s\n' "$#"
  index=0
  for arg in "$@"; do
    index=$((index + 1))
    printf 'ARG_%%s<<END\n%%s\nEND\n' "$index" "$arg"
  done
  if [ "$1 $2" = "pr create" ]; then
    printf 'STDIN<<END\n'
    cat
    printf '\nEND\n'
  fi
} >> "$FAKE_GH_LOG"
case "$1 $2" in
  "pr list")
    cat %s
    exit %d
    ;;
  "pr create")
    cat %s
    exit %d
    ;;
  "pr view")
`

const fakeGHPRScriptFooter = `    cat %s
    exit %d
    ;;
esac
echo "unexpected gh args: $*" >&2
exit 65
`

func withFakeGHPRResponses(t *testing.T, responses fakeGHPRResponses) string {
	t.Helper()

	binDir := testutil.CanonicalTempDir(t)
	fixtureDir := filepath.Join(binDir, "fixtures")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("create fake gh fixtures: %v", err)
	}

	listStdoutPath, createStdoutPath, statusStdoutPath := writeFakeGHPRFixtures(t, fixtureDir, responses)
	logPath := filepath.Join(binDir, "gh.log")
	script := fmt.Sprintf(
		fakeGHPRScriptHeader,
		shellQuote(listStdoutPath),
		responses.listExit,
		shellQuote(createStdoutPath),
		responses.createExit,
	)

	script += fmt.Sprintf(fakeGHPRScriptFooter, shellQuote(statusStdoutPath), responses.statusExit)

	ghPath := filepath.Join(binDir, "gh")
	if err := writeTestExecutable(ghPath, []byte(script)); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	setTestEnvironment(t, "FAKE_GH_LOG", logPath)
	prependTestPath(t, binDir)
	return logPath
}

func writeFakeGHPRFixtures(t *testing.T, fixtureDir string, responses fakeGHPRResponses) (string, string, string) {
	t.Helper()

	listStdoutPath := filepath.Join(fixtureDir, "list-stdout.txt")
	createStdoutPath := filepath.Join(fixtureDir, "create-stdout.txt")
	statusStdoutPath := filepath.Join(fixtureDir, "status-stdout.txt")
	writeTestFile(t, listStdoutPath, responses.listStdout, "fake gh list stdout")
	writeTestFile(t, createStdoutPath, responses.createStdout, "fake gh create stdout")
	writeTestFile(t, statusStdoutPath, responses.statusStdout, "fake gh status stdout")
	return listStdoutPath, createStdoutPath, statusStdoutPath
}

func writeTestFile(t *testing.T, path string, content string, label string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", label, err)
	}
}

func mainReadyTaskJSON(taskID string, repoPath string) string {
	return `[
		{
			"id":"` + taskID + `",
			"title":"Ready for task done",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.branch":"main","orpheus.worktree":"` + repoPath + `"}
		}
	]`
}

func writeReviewScript(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(testutil.CanonicalTempDir(t), "review-step")
	if err := writeTestExecutable(path, []byte(content)); err != nil {
		t.Fatalf("write review script: %v", err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
