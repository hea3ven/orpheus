package beads_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/beads"
)

type fakeRunner struct {
	calls []fakeCall
}

type fakeCall struct {
	wantDir  string
	wantArgs []string
	result   beads.Result
	err      error
}

func (r *fakeRunner) Run(dir string, args ...string) (beads.Result, error) {
	if len(r.calls) == 0 {
		return beads.Result{}, errors.New("unexpected bd call")
	}
	call := r.calls[0]
	r.calls = r.calls[1:]

	if strings.TrimSpace(dir) == "" {
		return beads.Result{}, errors.New("runner dir is empty")
	}
	if call.wantDir != "" && dir != call.wantDir {
		return beads.Result{}, errors.New("unexpected dir: " + dir)
	}
	if strings.Join(args, "\x00") != strings.Join(call.wantArgs, "\x00") {
		return beads.Result{}, errors.New("unexpected args: " + strings.Join(args, " "))
	}
	return call.result, call.err
}

func TestCommandRunnerSanitizesBeadsEnvironment(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bd-fake")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'BEADS_DIR=%s\\n' \"${BEADS_DIR-unset}\"\nprintf 'BD_NON_INTERACTIVE=%s\\n' \"${BD_NON_INTERACTIVE-unset}\"\n"), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("BEADS_DIR", "/tmp/wrong")

	result, err := beads.CommandRunner{Binary: bin}.Run(t.TempDir(), "context")
	if err != nil {
		t.Fatalf("run fake bd: %v", err)
	}
	if strings.Contains(result.Stdout, "BEADS_DIR=/tmp/wrong") || !strings.Contains(result.Stdout, "BEADS_DIR=unset") {
		t.Fatalf("stdout = %q, want sanitized BEADS_DIR", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "BD_NON_INTERACTIVE=1") {
		t.Fatalf("stdout = %q, want BD_NON_INTERACTIVE=1", result.Stdout)
	}
}

func TestInspectLocalWithRunnerDetectsPrefix(t *testing.T) {
	root := newRootWithBeadsDir(t)
	runner := &fakeRunner{calls: []fakeCall{
		{
			wantArgs: []string{"--json", "--readonly", "context"},
			result:   beads.Result{Stdout: `{"beads_dir":"` + filepath.ToSlash(filepath.Join(root, ".beads")) + `"}`},
		},
		{
			wantArgs: []string{"--json", "--readonly", "config", "get", "issue_prefix"},
			result:   beads.Result{Stdout: `{"key":"issue_prefix","value":"op"}`},
		},
	}}

	got, err := beads.InspectLocalWithRunner(root, runner)
	if err != nil {
		t.Fatalf("inspect local: %v", err)
	}
	if got.Root != root || got.BeadsDir != filepath.Join(root, ".beads") || got.Prefix != "op" {
		t.Fatalf("inspection = %#v, want root %q beads dir .beads prefix op", got, root)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner has %d unused calls", len(runner.calls))
	}
}

func TestInspectLocalWithRunnerReportsNoLocalWithoutRunningBD(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}

	_, err := beads.InspectLocalWithRunner(root, runner)
	if !errors.Is(err, beads.ErrNoLocal) {
		t.Fatalf("error = %v, want ErrNoLocal", err)
	}
}

func TestInspectLocalWithRunnerDistinguishesBDNoWorkspace(t *testing.T) {
	root := newRootWithBeadsDir(t)
	runner := &fakeRunner{calls: []fakeCall{{
		wantArgs: []string{"--json", "--readonly", "context"},
		result:   beads.Result{Stdout: `{"error":"no_beads_directory","message":"No active beads workspace found."}`},
		err:      errors.New("exit status 1"),
	}}}

	_, err := beads.InspectLocalWithRunner(root, runner)
	if !errors.Is(err, beads.ErrNoLocal) {
		t.Fatalf("error = %v, want ErrNoLocal", err)
	}
}

func TestInspectLocalWithRunnerReportsMissingBD(t *testing.T) {
	root := newRootWithBeadsDir(t)
	runner := &fakeRunner{calls: []fakeCall{{
		wantArgs: []string{"--json", "--readonly", "context"},
		err:      exec.ErrNotFound,
	}}}

	_, err := beads.InspectLocalWithRunner(root, runner)
	if err == nil {
		t.Fatal("inspect local succeeded, want error")
	}
	if !strings.Contains(err.Error(), "bd executable not found") {
		t.Fatalf("error = %v, want missing bd guidance", err)
	}
}

func TestInspectLocalWithRunnerReportsParseFailure(t *testing.T) {
	root := newRootWithBeadsDir(t)
	runner := &fakeRunner{calls: []fakeCall{{
		wantArgs: []string{"--json", "--readonly", "context"},
		result:   beads.Result{Stdout: `not-json`},
	}}}

	_, err := beads.InspectLocalWithRunner(root, runner)
	if err == nil {
		t.Fatal("inspect local succeeded, want error")
	}
	if !strings.Contains(err.Error(), "parse bd context JSON") || !strings.Contains(err.Error(), "not-json") {
		t.Fatalf("error = %v, want actionable parse failure", err)
	}
}

func TestInspectLocalWithRunnerReportsCommandFailure(t *testing.T) {
	root := newRootWithBeadsDir(t)
	runner := &fakeRunner{calls: []fakeCall{{
		wantArgs: []string{"--json", "--readonly", "context"},
		result:   beads.Result{Stderr: "database locked"},
		err:      errors.New("exit status 1"),
	}}}

	_, err := beads.InspectLocalWithRunner(root, runner)
	if err == nil {
		t.Fatal("inspect local succeeded, want error")
	}
	if !strings.Contains(err.Error(), "run bd --json --readonly context") || !strings.Contains(err.Error(), "database locked") {
		t.Fatalf("error = %v, want actionable command failure", err)
	}
}

func TestInspectLocalWithRunnerRequiresPrefix(t *testing.T) {
	root := newRootWithBeadsDir(t)
	runner := &fakeRunner{calls: []fakeCall{
		{
			wantArgs: []string{"--json", "--readonly", "context"},
			result:   beads.Result{Stdout: `{"beads_dir":"` + filepath.ToSlash(filepath.Join(root, ".beads")) + `"}`},
		},
		{
			wantArgs: []string{"--json", "--readonly", "config", "get", "issue_prefix"},
			result:   beads.Result{Stdout: `{"key":"issue_prefix","value":"   "}`},
		},
	}}

	_, err := beads.InspectLocalWithRunner(root, runner)
	if err == nil {
		t.Fatal("inspect local succeeded, want error")
	}
	if !strings.Contains(err.Error(), "issue_prefix is empty") {
		t.Fatalf("error = %v, want prefix guidance", err)
	}
}

func TestInitializeManagedWithRunnerInitializesEmptyDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "managed", "beads")
	runner := &fakeRunner{calls: []fakeCall{{
		wantDir: dir,
		wantArgs: []string{
			"init",
			"--non-interactive",
			"--prefix",
			"alpha",
			"--skip-agents",
			"--skip-hooks",
			"--quiet",
		},
	}}}

	if err := beads.InitializeManagedWithRunner(dir, " alpha ", runner); err != nil {
		t.Fatalf("initialize managed: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("managed directory was not created: info=%v err=%v", info, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner has %d unused calls", len(runner.calls))
	}
}

func TestInitializeManagedWithRunnerRejectsExistingStateBeforeRunningBD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leftover"), []byte("state"), 0o644); err != nil {
		t.Fatalf("write leftover: %v", err)
	}
	runner := &fakeRunner{}

	err := beads.InitializeManagedWithRunner(dir, "alpha", runner)
	if err == nil {
		t.Fatal("initialize managed succeeded, want existing state error")
	}
	if !strings.Contains(err.Error(), "directory already exists and is not empty") {
		t.Fatalf("error = %v, want existing state guidance", err)
	}
}

func TestInitializeManagedWithRunnerReportsCommandFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "beads")
	runner := &fakeRunner{calls: []fakeCall{{
		wantDir: dir,
		wantArgs: []string{
			"init",
			"--non-interactive",
			"--prefix",
			"alpha",
			"--skip-agents",
			"--skip-hooks",
			"--quiet",
		},
		result: beads.Result{Stderr: "cannot initialize"},
		err:    errors.New("exit status 1"),
	}}}

	err := beads.InitializeManagedWithRunner(dir, "alpha", runner)
	if err == nil {
		t.Fatal("initialize managed succeeded, want command failure")
	}
	if !strings.Contains(err.Error(), "run bd init") || !strings.Contains(err.Error(), "cannot initialize") {
		t.Fatalf("error = %v, want actionable command failure", err)
	}
}

func newRootWithBeadsDir(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	return root
}
