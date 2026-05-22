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
	if strings.Join(args, "\x00") != strings.Join(call.wantArgs, "\x00") {
		return beads.Result{}, errors.New("unexpected args: " + strings.Join(args, " "))
	}
	return call.result, call.err
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

func newRootWithBeadsDir(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	return root
}
