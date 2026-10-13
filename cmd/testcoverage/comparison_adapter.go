package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	gitadapter "github.com/hea3ven/orpheus/internal/git"
)

type comparisonRunner struct {
	root         string
	work         string
	output       string
	collector    string
	baseRevision string
	log          io.Writer
}

func executeComparison(opts options) (resultErr error) {
	output, err := filepath.Abs(opts.output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	initial := qualityReport{SchemaVersion: reportSchemaVersion, Execution: decision{Status: statusUnevaluated}, CoverageDecision: decision{Status: statusUnevaluated}, Timing: decision{Status: statusUnevaluated}, Comparison: &comparisonEvidence{BaseRevision: opts.compareBase, MergeRevision: opts.compareMerge}}
	// Write a blocking report before setup so setup failure cannot look like pass.
	if err := writeQualityReport(output, initial); err != nil {
		return err
	}
	defer func() {
		if resultErr != nil && initial.Decision.Status == "" {
			initial.Execution = decision{Status: statusExecutionFailed}
			initial, _ = abortComparison(initial, resultErr)
			resultErr = finishReport(output, initial, resultErr)
		}
	}()
	policy, err := loadQualityPolicy(opts.policy)
	if err != nil {
		return err
	}
	runner, cleanup, err := newComparisonRunner(output)
	if err != nil {
		return err
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 16*time.Minute)
	defer cancel()
	if err := runner.prepare(ctx, opts.compareBase, opts.compareMerge); err != nil {
		return err
	}
	result, err := compareRevisions(policy, opts.compareBase, opts.compareMerge, func(revision string, warmup bool, index int) (qualityReport, string, error) {
		return runner.collect(ctx, revision, warmup, index)
	}, func(progress qualityReport) error { return writeQualityReport(output, progress) })
	initial = result
	return finishReport(output, result, err)
}

func newComparisonRunner(output string) (comparisonRunner, func(), error) {
	root, err := os.Getwd()
	if err != nil {
		return comparisonRunner{}, nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return comparisonRunner{}, nil, err
	}
	work, err := os.MkdirTemp("", "orpheus-comparison-*")
	if err != nil {
		return comparisonRunner{}, nil, err
	}
	log, err := os.Create(filepath.Join(filepath.Dir(output), "commands.log"))
	if err != nil {
		_ = os.RemoveAll(work)
		return comparisonRunner{}, nil, err
	}
	// Dolt's version probe otherwise queries for updates and emits a once-only
	// warning, making unchanged tool metadata drift between captures.
	doltConfig := filepath.Join(work, "home", ".dolt", "config_global.json")
	if err := writeJSON(doltConfig, map[string]string{"versioncheck.disabled": "true", "metrics.disabled": "true"}); err != nil {
		_ = log.Close()
		_ = os.RemoveAll(work)
		return comparisonRunner{}, nil, err
	}
	runner := comparisonRunner{root: root, work: work, output: output, collector: self, log: log}
	cleanup := func() {
		for _, name := range []string{"base", "merge"} {
			// Cleanup is local only; never prune other worktrees or operator state.
			_ = runner.gitCommand(context.Background(), root, "worktree", "remove", "--force", filepath.Join(work, name))
		}
		_ = log.Close()
		_ = os.RemoveAll(work)
	}
	return runner, cleanup, nil
}

func (runner *comparisonRunner) prepare(ctx context.Context, base, merge string) error {
	if !commitPattern.MatchString(base) || !commitPattern.MatchString(merge) || base == merge {
		return errors.New("base and merge must be distinct exact full commit IDs")
	}
	command := []string{"show", "-s", "--format=%H %P", merge}
	output, err := runner.runGit(ctx, runner.root, command...)
	if err != nil {
		return fmt.Errorf("verify merge: %w", err)
	}
	identities := strings.Fields(output)
	if len(identities) != 3 || identities[0] != merge || identities[1] != base {
		return errors.New("tested commit is not a synthetic merge with the exact base as first parent")
	}
	for _, item := range []struct{ name, revision string }{{"base", base}, {"merge", merge}} {
		if err := runner.gitCommand(ctx, runner.root, "worktree", "add", "--detach", filepath.Join(runner.work, item.name), item.revision); err != nil {
			return err
		}
	}
	runner.baseRevision = base
	return nil
}

func (runner comparisonRunner) gitCommand(ctx context.Context, dir string, args ...string) error {
	_, err := runner.runGit(ctx, dir, args...)
	return err
}

func (runner comparisonRunner) runGit(ctx context.Context, dir string, args ...string) (string, error) {
	adapter := gitadapter.NewCommandRunner(comparisonEnvironment(runner.work, "setup"))
	_, _ = fmt.Fprintf(runner.log, "git %s\n", strings.Join(args, " "))
	result, err := adapter.Run(ctx, gitadapter.Command{Directory: dir, Args: args})
	_, _ = fmt.Fprint(runner.log, result.Stdout, result.Stderr)
	if err != nil {
		return result.Stdout, fmt.Errorf("git %s: %w: %s", args, err, result.Stderr)
	}
	return result.Stdout, nil
}

func (runner comparisonRunner) collect(ctx context.Context, revision string, warmup bool, index int) (qualityReport, string, error) {
	name := "merge"
	if revision == runner.baseRevision {
		name = "base"
	}
	label := fmt.Sprintf("sample-%d", index)
	if warmup {
		label = "warmup"
	}
	path := filepath.Join(filepath.Dir(runner.output), "samples", name, label, "report.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return qualityReport{}, path, err
	}
	log, err := os.Create(filepath.Join(filepath.Dir(path), "command.log"))
	if err != nil {
		return qualityReport{}, path, err
	}
	defer func() { _ = log.Close() }()
	cmd := exec.CommandContext(ctx, runner.collector, "-measure-revision", revision, "-output", path)
	cmd.Dir = filepath.Join(runner.work, name)
	cmd.Env = comparisonEnvironment(runner.work, name)
	configureProcessCancellation(cmd)
	cmd.Stdout, cmd.Stderr = log, log
	_, _ = fmt.Fprintf(runner.log, "%s %s: %s\n", name, label, cmd.String())
	runErr := cmd.Run()
	contents, readErr := os.ReadFile(path)
	var result qualityReport
	if readErr == nil {
		readErr = json.Unmarshal(contents, &result)
	}
	return result, path, errors.Join(runErr, readErr)
}

func comparisonEnvironment(work, name string) []string {
	values := map[string]string{
		"GOCACHE": filepath.Join(work, "cache-"+name), "GOMODCACHE": filepath.Join(work, "modules"),
		"HOME": filepath.Join(work, "home"), "DOLT_ROOT_PATH": filepath.Join(work, "home"),
		"GOENV": "off", "GOWORK": "off",
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull,
	}
	result := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range coverageEnvironment(os.Environ()) {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
