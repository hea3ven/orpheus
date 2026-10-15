package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	gitadapter "github.com/hea3ven/orpheus/internal/git"
)

type comparisonRunner struct {
	root               string
	work               string
	output             string
	collector          string
	checkoutRepository string
	log                io.Writer
}

func executeComparison(opts options) (resultErr error) {
	output, err := comparisonOutput(opts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	if opts.compareWorktree {
		fmt.Printf("Local timing evidence: %s\n", filepath.Dir(output))
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
	runner, cleanup, err := newComparisonRunner(output)
	if err != nil {
		return err
	}
	defer cleanup()
	deadline, stop := context.WithTimeout(context.Background(), 16*time.Minute)
	defer stop()
	ctx, cancel := signal.NotifyContext(deadline, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := runner.prepareComparison(ctx, opts, initial.Comparison); err != nil {
		return err
	}
	if err := writeQualityReport(output, initial); err != nil {
		return err
	}
	policyPath := opts.policy
	if opts.compareWorktree {
		policyPath = filepath.Join(runner.work, "merge", ".quality.yml")
	}
	policy, err := loadQualityPolicy(policyPath)
	if err != nil {
		return err
	}
	result, err := runner.compare(ctx, policy, initial.Comparison)
	initial = result
	return finishReport(output, result, err)
}

func (runner *comparisonRunner) prepareComparison(ctx context.Context, opts options, evidence *comparisonEvidence) error {
	if !opts.compareWorktree {
		base, err := runner.prepare(ctx, opts.compareHead, opts.compareMerge)
		evidence.BaseRevision = base
		return err
	}
	snapshot, base, err := runner.prepareWorkingTree(ctx, opts.compareBase)
	evidence.BaseRevision, evidence.MergeRevision = base, snapshot.Revision
	evidence.Snapshot = &snapshot
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(filepath.Dir(runner.output), "snapshot.json"), snapshot); err != nil {
		return err
	}
	return runner.buildCollector(ctx)
}

func (runner comparisonRunner) compare(ctx context.Context, policy localQualityPolicy, evidence *comparisonEvidence) (qualityReport, error) {
	annotate := func(result *qualityReport) { result.Comparison.Snapshot = evidence.Snapshot }
	result, err := compareRevisions(policy, evidence.BaseRevision, evidence.MergeRevision, func(subject, revision string, warmup bool, index int) (qualityReport, string, error) {
		return runner.collect(ctx, subject, revision, warmup, index)
	}, func(progress qualityReport) error {
		annotate(&progress)
		return writeQualityReport(runner.output, progress)
	})
	annotate(&result)
	return result, err
}

func newComparisonRunner(output string) (*comparisonRunner, func(), error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	work, err := os.MkdirTemp("", "orpheus-comparison-*")
	if err != nil {
		return nil, nil, err
	}
	log, err := os.Create(filepath.Join(filepath.Dir(output), "commands.log"))
	if err != nil {
		_ = os.RemoveAll(work)
		return nil, nil, err
	}
	// Dolt's version probe otherwise queries for updates and emits a once-only
	// warning, making unchanged tool metadata drift between captures.
	doltConfig := filepath.Join(work, "home", ".dolt", "config_global.json")
	if err := writeJSON(doltConfig, map[string]string{"versioncheck.disabled": "true", "metrics.disabled": "true"}); err != nil {
		_ = log.Close()
		_ = os.RemoveAll(work)
		return nil, nil, err
	}
	runner := &comparisonRunner{root: root, work: work, output: output, collector: self, log: log}
	cleanup := func() {
		for _, name := range []string{"base", "merge"} {
			if runner.checkoutRepository == "" {
				break
			}
			// Cleanup is local only; never prune other worktrees or operator state.
			_ = runner.gitCommand(context.Background(), runner.checkoutRepository, "worktree", "remove", "--force", filepath.Join(work, name))
		}
		_ = log.Close()
		_ = os.RemoveAll(work)
	}
	return runner, cleanup, nil
}

func (runner *comparisonRunner) prepare(ctx context.Context, head, merge string) (string, error) {
	if !commitPattern.MatchString(head) || !commitPattern.MatchString(merge) || head == merge {
		return "", errors.New("PR head and tested merge must be distinct exact full commit IDs")
	}
	output, err := runner.runGit(ctx, runner.root, "log", "-1", "--no-walk", "--format=%H %P", merge)
	if err != nil {
		return "", fmt.Errorf("verify tested merge %s: %w", merge, err)
	}
	identities := strings.Fields(output)
	if len(identities) != 3 || identities[0] != merge || identities[1] == identities[2] {
		return "", fmt.Errorf("tested commit %s must be a two-parent synthetic merge", merge)
	}
	base := identities[1]
	if identities[2] != head {
		return base, fmt.Errorf("tested merge %s second parent %s does not match expected PR head %s", merge, identities[2], head)
	}
	for _, item := range []struct{ name, revision string }{{"base (first parent)", base}, {"PR head (second parent)", head}} {
		if err := runner.gitCommand(ctx, runner.root, "cat-file", "-e", item.revision+"^{commit}"); err != nil {
			return base, fmt.Errorf("verify %s commit %s: %w", item.name, item.revision, err)
		}
	}
	return base, runner.prepareCheckouts(ctx, base, merge)
}

func (runner *comparisonRunner) prepareCheckouts(ctx context.Context, base, merge string) error {
	runner.checkoutRepository = runner.root
	for _, item := range []struct{ name, revision string }{{"base", base}, {"merge", merge}} {
		if err := runner.gitCommand(ctx, runner.root, "worktree", "add", "--detach", filepath.Join(runner.work, item.name), item.revision); err != nil {
			return err
		}
	}
	return nil
}

func (runner comparisonRunner) gitCommand(ctx context.Context, dir string, args ...string) error {
	_, err := runner.runGit(ctx, dir, args...)
	return err
}

func (runner comparisonRunner) runGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runner.runGitInput(ctx, dir, "", args...)
}

func (runner comparisonRunner) runGitInput(ctx context.Context, dir, input string, args ...string) (string, error) {
	adapter := gitadapter.NewCommandRunner(comparisonEnvironment(runner.work, "setup"))
	_, _ = fmt.Fprintf(runner.log, "git %s\n", strings.Join(args, " "))
	result, err := adapter.Run(ctx, gitadapter.Command{Directory: dir, Args: args, Input: input})
	_, _ = fmt.Fprint(runner.log, result.Stdout, result.Stderr)
	if err != nil {
		return result.Stdout, fmt.Errorf("git %s: %w: %s", args, err, result.Stderr)
	}
	return result.Stdout, nil
}

func (runner comparisonRunner) collect(ctx context.Context, name, revision string, warmup bool, index int) (qualityReport, string, error) {
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
		testBinaryCacheEnvironment: filepath.Join(work, "test-binaries-"+name),
		"HOME":                     filepath.Join(work, "home"), "DOLT_ROOT_PATH": filepath.Join(work, "home"),
		"GOENV": "off", "GOWORK": "off",
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull,
		"GIT_AUTHOR_NAME": "Orpheus timing snapshot", "GIT_AUTHOR_EMAIL": "timing@example.invalid",
		"GIT_COMMITTER_NAME": "Orpheus timing snapshot", "GIT_COMMITTER_EMAIL": "timing@example.invalid",
		"GIT_AUTHOR_DATE": "2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE": "2000-01-01T00:00:00Z",
		"GIT_TERMINAL_PROMPT": "0",
	}
	result := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range gitRoutingEnvironment(coverageEnvironment(os.Environ())) {
		key, _, _ := strings.Cut(entry, "=")
		// Inherited configuration can enable operator hooks in private checkouts.
		if key == "GIT_CONFIG" || strings.HasPrefix(key, "GIT_CONFIG_") {
			continue
		}
		if _, replaced := values[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
