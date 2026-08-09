// Package revieweval runs manually invoked live evaluations for review context.
package revieweval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/beads"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

const (
	HarnessCodex = "codex"
	HarnessPi    = "pi"

	VariantLegacy     = "legacy"
	VariantExhaustive = "exhaustive"

	ScenarioGeneral      = "general"
	ScenarioArchitecture = "architecture"

	defaultCodexModel = "gpt-5.4-mini"
	defaultPiModel    = "openai-codex/gpt-5.4-mini"

	noAgentExecutionRecordedReason = "agent_execution_not_recorded"
)

// Options configures one live review-context evaluation invocation.
type Options struct {
	Harnesses   []string
	Variants    []string
	Scenarios   []string
	Repetitions int
	Complete    bool

	CodexModel string
	PiModel    string
	Thinking   string

	KeepWorkdirs bool
}

// Report is the machine-readable evaluation output.
type Report struct {
	StartedAt         time.Time        `json:"started_at"`
	FinishedAt        time.Time        `json:"finished_at"`
	IsolatedRoot      string           `json:"isolated_root,omitempty"`
	IsolatedRootKept  bool             `json:"isolated_root_kept"`
	Runs              []RunResult      `json:"runs"`
	Aggregates        []Aggregate      `json:"aggregates"`
	TotalCost         CostTotal        `json:"total_evaluation_cost"`
	PiAcceptanceCheck PiAcceptanceGate `json:"pi_acceptance_check"`
}

// RunResult records one harness/context/scenario repetition.
type RunResult struct {
	Harness    string `json:"harness"`
	Model      string `json:"model"`
	Variant    string `json:"context_variant"`
	Scenario   string `json:"scenario"`
	Repetition int    `json:"repetition"`

	RepoPath       string `json:"repo_path"`
	ConfigRoot     string `json:"config_root"`
	DataRoot       string `json:"data_root"`
	ReviewStatus   string `json:"review_status,omitempty"`
	OperationalErr string `json:"operational_error,omitempty"`

	SeededFindings     []KnownFindingReport `json:"seeded_findings"`
	DetectedSeededIDs  []string             `json:"seeded_findings_detected"`
	MissedSeededIDs    []string             `json:"seeded_findings_missed"`
	UnexpectedFindings []FindingReport      `json:"unexpected_findings"`
	FindingRecall      float64              `json:"finding_recall"`
	CompleteSession    bool                 `json:"single_session_completeness"`

	Usage UsageReport `json:"usage"`
	Cost  CostReport  `json:"cost"`
}

// KnownFindingReport describes a seeded defect in the scenario.
type KnownFindingReport struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Detected bool   `json:"detected"`
}

// FindingReport describes a persisted review finding.
type FindingReport struct {
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// UsageReport records the available token categories for one run.
type UsageReport struct {
	Available     bool           `json:"available"`
	Tokens        map[string]int `json:"tokens,omitempty"`
	UnknownReason string         `json:"unknown_reason,omitempty"`
	CaptureStatus string         `json:"capture_status,omitempty"`
	CaptureReason string         `json:"capture_reason,omitempty"`
}

// CostReport records reported or estimated cost for one run.
type CostReport struct {
	Known          bool   `json:"known"`
	Kind           string `json:"kind,omitempty"`
	AmountMicroUSD int64  `json:"amount_micro_usd,omitempty"`
	AmountUSD      string `json:"amount_usd,omitempty"`
	Source         string `json:"source,omitempty"`
	UnknownReason  string `json:"unknown_reason,omitempty"`
}

// CostTotal records aggregate known and unknown cost coverage.
type CostTotal struct {
	KnownRunCount      int    `json:"known_run_count"`
	UnknownRunCount    int    `json:"unknown_run_count"`
	AmountMicroUSD     int64  `json:"amount_micro_usd"`
	AmountUSD          string `json:"amount_usd"`
	IncludesFailedRuns bool   `json:"includes_completed_portions_of_failed_runs"`
}

// Aggregate summarizes equivalent runs.
type Aggregate struct {
	Harness             string         `json:"harness"`
	Model               string         `json:"model"`
	Variant             string         `json:"context_variant"`
	Scenario            string         `json:"scenario"`
	Runs                int            `json:"runs"`
	OperationalErrors   int            `json:"operational_errors"`
	MeanRecall          float64        `json:"mean_recall"`
	CompleteSessions    int            `json:"complete_sessions"`
	MissedBySeedID      map[string]int `json:"missed_by_seed_id"`
	UnexpectedFindings  int            `json:"unexpected_findings"`
	KnownCostMicroUSD   int64          `json:"known_cost_micro_usd"`
	KnownCostUSD        string         `json:"known_cost_usd"`
	KnownCostSources    map[string]int `json:"known_cost_sources,omitempty"`
	KnownCostKinds      map[string]int `json:"known_cost_kinds,omitempty"`
	UnknownCostRuns     int            `json:"unknown_cost_runs"`
	UnknownCostReasons  map[string]int `json:"unknown_cost_reasons,omitempty"`
	UsageAvailableRuns  int            `json:"usage_available_runs"`
	UsageTokens         map[string]int `json:"usage_tokens,omitempty"`
	UnknownUsageRuns    int            `json:"unknown_usage_runs"`
	UnknownUsageReasons map[string]int `json:"unknown_usage_reasons,omitempty"`
}

// PiAcceptanceGate reports the Pi-specific rollout gate when applicable.
type PiAcceptanceGate struct {
	Applicable                            bool `json:"applicable"`
	ExhaustiveCompletesTwoOfThreeGeneral  bool `json:"exhaustive_completes_two_of_three_general"`
	ExhaustiveCompletesTwoOfThreeArch     bool `json:"exhaustive_completes_two_of_three_architecture"`
	ExhaustiveRecallNotBelowLegacyGeneral bool `json:"exhaustive_recall_not_below_legacy_general"`
	ExhaustiveRecallNotBelowLegacyArch    bool `json:"exhaustive_recall_not_below_legacy_architecture"`
	Passed                                bool `json:"passed"`
}

type runSpec struct {
	Harness    string
	Variant    string
	Scenario   string
	Repetition int
}

type scenario struct {
	name               string
	title              string
	description        string
	acceptanceCriteria string
	promptAppend       string
	files              map[string]string
	changes            map[string]string
	knownFindings      []knownFinding
}

type knownFinding struct {
	id      string
	title   string
	matches [][]string
}

type runSetup struct {
	root       string
	repoPath   string
	configBase string
	dataBase   string
	paths      state.Paths
	taskID     string
	attempt    taskstate.ReviewAttempt
	store      taskstate.Store
}

type runExecutor func(context.Context, string, Options, runSpec) RunResult

type previousEnv struct {
	value string
	ok    bool
}

// Run executes the live evaluation and writes a JSON report to stdout.
func Run(ctx context.Context, opts Options, stdout io.Writer, stderr io.Writer) error {
	return runWithExecutor(ctx, opts, stdout, stderr, executeRun)
}

func runWithExecutor(ctx context.Context, opts Options, stdout io.Writer, stderr io.Writer, executor runExecutor) error {
	normalized, err := normalizeOptions(opts)
	if err != nil {
		return err
	}
	specs := evaluationSpecs(normalized)
	if err := ensureRuns(specs); err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "orpheus-review-eval-*")
	if err != nil {
		return fmt.Errorf("create isolated evaluation root: %w", err)
	}
	if !normalized.KeepWorkdirs {
		defer func() {
			_ = os.RemoveAll(root)
		}()
	}

	report := Report{StartedAt: time.Now().UTC(), IsolatedRoot: root, IsolatedRootKept: normalized.KeepWorkdirs}
	for _, spec := range specs {
		_, _ = fmt.Fprintf(
			stderr,
			"running %s/%s/%s repetition %d\n",
			spec.Harness,
			spec.Variant,
			spec.Scenario,
			spec.Repetition,
		)
		report.Runs = append(report.Runs, executor(ctx, root, normalized, spec))
		if err := cleanupRunRoot(root, spec, normalized.KeepWorkdirs); err != nil {
			_, _ = fmt.Fprintf(stderr, "warning: clean up %s: %v\n", runDirectoryName(spec), err)
		}
	}
	report.FinishedAt = time.Now().UTC()
	report.Aggregates = aggregateResults(report.Runs)
	report.TotalCost = totalCost(report.Runs)
	report.PiAcceptanceCheck = piAcceptanceGate(report.Aggregates)

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("write evaluation report: %w", err)
	}
	return nil
}

func cleanupRunRoot(root string, spec runSpec, keepWorkdirs bool) error {
	if keepWorkdirs {
		return nil
	}
	return os.RemoveAll(filepath.Join(root, runDirectoryName(spec)))
}

func normalizeOptions(opts Options) (Options, error) {
	if opts.Complete {
		opts.Harnesses = []string{HarnessPi, HarnessCodex}
		opts.Variants = []string{VariantLegacy, VariantExhaustive}
		opts.Scenarios = []string{ScenarioGeneral, ScenarioArchitecture}
	}
	opts.Harnesses = normalizeSelection(opts.Harnesses, []string{HarnessPi, HarnessCodex})
	opts.Variants = normalizeSelection(opts.Variants, []string{VariantLegacy, VariantExhaustive})
	opts.Scenarios = normalizeSelection(opts.Scenarios, []string{ScenarioGeneral, ScenarioArchitecture})
	if opts.Repetitions <= 0 {
		return Options{}, fmt.Errorf("repetitions must be positive, got %d", opts.Repetitions)
	}
	if opts.CodexModel == "" {
		opts.CodexModel = defaultCodexModel
	}
	if opts.PiModel == "" {
		opts.PiModel = defaultPiModel
	}
	if err := validateSelections("harness", opts.Harnesses, []string{HarnessPi, HarnessCodex}); err != nil {
		return Options{}, err
	}
	if err := validateSelections("variant", opts.Variants, []string{VariantLegacy, VariantExhaustive}); err != nil {
		return Options{}, err
	}
	if err := validateSelections("scenario", opts.Scenarios, []string{ScenarioGeneral, ScenarioArchitecture}); err != nil {
		return Options{}, err
	}
	return opts, nil
}

func normalizeSelection(values []string, all []string) []string {
	if len(values) == 0 {
		return append([]string{}, all...)
	}
	seen := map[string]struct{}{}
	var out []string
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			value := strings.ToLower(strings.TrimSpace(part))
			if value == "" {
				continue
			}
			if value == "all" {
				return append([]string{}, all...)
			}
			if _, ok := seen[value]; !ok {
				seen[value] = struct{}{}
				out = append(out, value)
			}
		}
	}
	return out
}

func validateSelections(label string, values []string, allowed []string) error {
	if len(values) == 0 {
		return fmt.Errorf("%s selection is empty", label)
	}
	allowedSet := map[string]struct{}{}
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := allowedSet[value]; !ok {
			return fmt.Errorf("unsupported %s %q; expected one of %s", label, value, strings.Join(allowed, ", "))
		}
	}
	return nil
}

func evaluationSpecs(opts Options) []runSpec {
	specs := make([]runSpec, 0, len(opts.Harnesses)*len(opts.Variants)*len(opts.Scenarios)*opts.Repetitions)
	for _, harness := range opts.Harnesses {
		for _, variant := range opts.Variants {
			for _, scenarioName := range opts.Scenarios {
				for repetition := 1; repetition <= opts.Repetitions; repetition++ {
					specs = append(specs, runSpec{Harness: harness, Variant: variant, Scenario: scenarioName, Repetition: repetition})
				}
			}
		}
	}
	return specs
}

func executeRun(ctx context.Context, root string, opts Options, spec runSpec) RunResult {
	scenarioDef := scenarioByName(spec.Scenario)
	result := newRunResult(opts, spec, scenarioDef)
	setup, err := prepareRun(ctx, root, scenarioDef, spec)
	if err != nil {
		result.OperationalErr = err.Error()
		recordNoExecutionUsageAndCost(&result)
		scoreFindings(&result, scenarioDef.knownFindings, nil)
		return result
	}
	result.RepoPath = setup.repoPath
	result.ConfigRoot = setup.paths.ConfigRoot
	result.DataRoot = setup.paths.DataRoot

	err = withRunEnvironment(setup, spec, func() error {
		return runPipeline(ctx, opts, spec, scenarioDef, setup)
	})
	stateResult := collectRunState(setup)
	if stateResult.reviewStatus != "" {
		result.ReviewStatus = stateResult.reviewStatus
	}
	if stateResult.execution != nil {
		result.Usage = usageReport(*stateResult.execution)
		result.Cost = costReport(*stateResult.execution)
	} else {
		recordNoExecutionUsageAndCost(&result)
	}
	if err != nil {
		result.OperationalErr = err.Error()
	}
	scoreFindings(&result, scenarioDef.knownFindings, stateResult.findings)
	return result
}

func recordNoExecutionUsageAndCost(result *RunResult) {
	result.Usage = UsageReport{
		UnknownReason: noAgentExecutionRecordedReason,
		CaptureStatus: string(taskstate.UsageCaptureUnknown),
		CaptureReason: "agent execution was not recorded before usage capture",
	}
	result.Cost = CostReport{UnknownReason: noAgentExecutionRecordedReason}
}

func newRunResult(opts Options, spec runSpec, scenarioDef scenario) RunResult {
	return RunResult{
		Harness:    spec.Harness,
		Model:      modelForHarness(opts, spec.Harness),
		Variant:    spec.Variant,
		Scenario:   scenarioDef.name,
		Repetition: spec.Repetition,
	}
}

func prepareRun(ctx context.Context, root string, scenarioDef scenario, spec runSpec) (runSetup, error) {
	runRoot := filepath.Join(root, runDirectoryName(spec))
	repoPath := filepath.Join(runRoot, "repo")
	configBase := filepath.Join(runRoot, "xdg-config")
	dataBase := filepath.Join(runRoot, "xdg-data")
	paths, err := state.NewPaths(filepath.Join(configBase, state.AppName), filepath.Join(dataBase, state.AppName))
	if err != nil {
		return runSetup{}, err
	}
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		return runSetup{}, fmt.Errorf("create scenario repo: %w", err)
	}
	if err := beads.InitializeManaged(repoPath, "op"); err != nil {
		return runSetup{}, fmt.Errorf("initialize isolated Beads state: %w", err)
	}
	if err := seedGitRepo(ctx, repoPath, scenarioDef); err != nil {
		return runSetup{}, err
	}
	taskID, err := seedTask(ctx, repoPath, scenarioDef)
	if err != nil {
		return runSetup{}, err
	}
	if err := registry.NewStore(paths).Save(registry.Registry{Repos: []registry.Repo{isolatedRepo(repoPath)}}); err != nil {
		return runSetup{}, fmt.Errorf("write isolated registry: %w", err)
	}

	store := taskstate.NewStore(paths)
	if err := seedTaskState(store, taskID, repoPath); err != nil {
		return runSetup{}, err
	}
	attempt, err := store.StartReviewWithOptions("review-eval", taskID, taskstate.StartReviewOptions{
		Pipeline: "review-context-eval",
		Step:     "ai-review",
	})
	if err != nil {
		return runSetup{}, fmt.Errorf("start isolated review attempt: %w", err)
	}
	return runSetup{root: runRoot, repoPath: repoPath, configBase: configBase, dataBase: dataBase, paths: paths, taskID: taskID, attempt: attempt, store: store}, nil
}

func runDirectoryName(spec runSpec) string {
	return fmt.Sprintf("%s-%s-%s-%02d", spec.Harness, spec.Variant, spec.Scenario, spec.Repetition)
}

func isolatedRepo(repoPath string) registry.Repo {
	return registry.Repo{
		ID:            "review-eval",
		Name:          "Review Context Evaluation",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}
}

func seedGitRepo(ctx context.Context, repoPath string, scenarioDef scenario) error {
	if err := runCommand(ctx, repoPath, "git", "init", "-b", "main"); err != nil {
		return fmt.Errorf("initialize git repo: %w", err)
	}
	if err := runCommand(ctx, repoPath, "git", "config", "user.email", "review-eval@example.test"); err != nil {
		return fmt.Errorf("configure git email: %w", err)
	}
	if err := runCommand(ctx, repoPath, "git", "config", "user.name", "Review Eval"); err != nil {
		return fmt.Errorf("configure git user: %w", err)
	}
	for path, content := range scenarioDef.files {
		if err := writeScenarioFile(repoPath, path, content); err != nil {
			return err
		}
	}
	if err := runCommand(ctx, repoPath, "git", "add", "."); err != nil {
		return fmt.Errorf("stage baseline repo: %w", err)
	}
	if err := runCommand(ctx, repoPath, "git", "commit", "-m", "baseline"); err != nil {
		return fmt.Errorf("commit baseline repo: %w", err)
	}
	for path, content := range scenarioDef.changes {
		if err := writeScenarioFile(repoPath, path, content); err != nil {
			return err
		}
	}
	return nil
}

func writeScenarioFile(repoPath string, rel string, content string) error {
	path := filepath.Join(repoPath, filepath.Clean(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create scenario directory for %s: %w", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write scenario file %s: %w", rel, err)
	}
	return nil
}

func seedTask(ctx context.Context, repoPath string, scenarioDef scenario) (string, error) {
	backend, err := beads.NewTaskBackend(repoPath)
	if err != nil {
		return "", err
	}
	created, err := backend.Create(ctx, taskmodel.CreateOptions{
		Title:              scenarioDef.title,
		Description:        scenarioDef.description,
		AcceptanceCriteria: scenarioDef.acceptanceCriteria,
		IssueType:          taskmodel.IssueTypeTask,
	})
	if err != nil {
		return "", fmt.Errorf("create isolated Beads task: %w", err)
	}
	if err := backend.MarkInProgress(ctx, created.ID, "main", repoPath); err != nil {
		return "", fmt.Errorf("mark isolated task in progress: %w", err)
	}
	return created.ID, nil
}

func seedTaskState(store taskstate.Store, taskID string, repoPath string) error {
	run, err := store.StartRun("review-eval", taskID, taskstate.StartRunOptions{
		Agent:    "seed-implementer",
		Branch:   "main",
		Worktree: repoPath,
	})
	if err != nil {
		return fmt.Errorf("start isolated implementation run: %w", err)
	}
	_, err = store.CompleteRun("review-eval", taskID, run.Attempt, taskstate.CompleteRunOptions{
		Summary:              "Implement seeded review scenario",
		Description:          "Seeded multiple independent defects for live review-context evaluation.",
		DetailedDescription:  "This run intentionally leaves known defects in the working tree so reviewer recall can be measured.",
		TechnicalExplanation: "The scenario is synthetic and isolated from operator repositories and normal Orpheus state.",
	})
	if err != nil {
		return fmt.Errorf("complete isolated implementation run: %w", err)
	}
	if _, err := store.FinishRun("review-eval", taskID, run.Attempt, taskstate.RunStatusSucceeded); err != nil {
		return fmt.Errorf("finish isolated implementation run: %w", err)
	}
	return nil
}

func runCommand(ctx context.Context, dir string, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func withRunEnvironment(setup runSetup, spec runSpec, run func() error) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable for agent context shim: %w", err)
	}
	binDir := filepath.Join(setup.root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create shim bin dir: %w", err)
	}
	shimPath := filepath.Join(binDir, "orpheus")
	if err := os.WriteFile(shimPath, []byte(shimScript(executable)), 0o755); err != nil {
		return fmt.Errorf("write orpheus shim: %w", err)
	}

	codexHome := filepath.Join(setup.root, "codex")
	piDir := filepath.Join(setup.root, "pi")
	piSessionDir := filepath.Join(setup.root, "pi-sessions")
	for _, dir := range []string{filepath.Join(setup.root, "home"), codexHome, piDir, piSessionDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create isolated environment directory: %w", err)
		}
	}
	switch spec.Harness {
	case HarnessCodex:
		if err := provisionHarnessConfig(operatorCodexRoot, codexHome); err != nil {
			return fmt.Errorf("provision Codex auth/config: %w", err)
		}
	case HarnessPi:
		if err := provisionHarnessConfig(operatorPiRoot, piDir); err != nil {
			return fmt.Errorf("provision Pi auth/config: %w", err)
		}
	}

	env := map[string]string{
		"XDG_CONFIG_HOME":                   setup.configBase,
		"XDG_DATA_HOME":                     setup.dataBase,
		"PATH":                              binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME":                              filepath.Join(setup.root, "home"),
		"CODEX_HOME":                        codexHome,
		"PI_CODING_AGENT_DIR":               piDir,
		"PI_CODING_AGENT_SESSION_DIR":       piSessionDir,
		"ORPHEUS_EXHAUSTIVE_REVIEW_CONTEXT": exhaustiveFlag(spec.Variant),
	}
	return withEnv(env, run)
}

func provisionHarnessConfig(sourceRoot func() (string, error), targetRoot string) error {
	source, err := sourceRoot()
	if err != nil {
		return err
	}
	if source == "" {
		return nil
	}
	info, err := os.Stat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", source)
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	return copyHarnessConfig(source, targetRoot)
}

func operatorCodexRoot() (string, error) {
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return absoluteCleanEnvPath("CODEX_HOME", value)
	}
	home, err := operatorHome()
	if err != nil || home == "" {
		return home, err
	}
	return filepath.Join(home, ".codex"), nil
}

func operatorPiRoot() (string, error) {
	if value := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); value != "" {
		return absoluteCleanEnvPath("PI_CODING_AGENT_DIR", value)
	}
	home, err := operatorHome()
	if err != nil || home == "" {
		return home, err
	}
	return filepath.Join(home, ".pi", "agent"), nil
}

func operatorHome() (string, error) {
	if value := strings.TrimSpace(os.Getenv("HOME")); value != "" {
		return absoluteCleanEnvPath("HOME", value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(home) == "" {
		return "", nil
	}
	return absoluteCleanEnvPath("HOME", home)
}

func absoluteCleanEnvPath(key string, value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be absolute, got %q", key, value)
	}
	return filepath.Clean(value), nil
}

func copyHarnessConfig(sourceRoot string, targetRoot string) error {
	return filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if shouldSkipHarnessConfigPath(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(targetRoot, rel)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return copySymlinkTarget(path, target)
		case entry.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyRegularFile(path, target, info.Mode().Perm())
		default:
			return nil
		}
	})
}

func shouldSkipHarnessConfigPath(rel string) bool {
	first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return first == "sessions"
}

func copySymlinkTarget(source string, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("refuse symlinked harness config directory %q; replace it with real files or point the harness config root at the resolved directory", source)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refuse symlinked non-regular harness config %q", source)
	}
	return copyRegularFile(source, target, info.Mode().Perm())
}

func copyRegularFile(source string, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() {
		_ = input.Close()
	}()

	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func shimScript(executable string) string {
	return "#!/bin/sh\nexec " + shellQuote(executable) + " \"$@\"\n"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func exhaustiveFlag(variant string) string {
	if variant == VariantExhaustive {
		return "1"
	}
	return "0"
}

func withEnv(values map[string]string, run func() error) error {
	old := map[string]previousEnv{}
	for key, value := range values {
		oldValue, ok := os.LookupEnv(key)
		old[key] = previousEnv{value: oldValue, ok: ok}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	defer restoreEnv(old)
	return run()
}

func restoreEnv(old map[string]previousEnv) {
	for key, previous := range old {
		if previous.ok {
			_ = os.Setenv(key, previous.value)
			continue
		}
		_ = os.Unsetenv(key)
	}
}

func runPipeline(ctx context.Context, opts Options, spec runSpec, scenarioDef scenario, setup runSetup) error {
	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context:     ctx,
		Store:       setup.store,
		RepoID:      "review-eval",
		TaskID:      setup.taskID,
		Branch:      "main",
		Workdir:     setup.repoPath,
		Attempt:     setup.attempt,
		Pipeline:    evaluationPipeline(),
		SessionName: evaluationSessionName(setup.taskID, scenarioDef),
		Stdin:       nil,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		AgentConfig: agentConfig(opts, spec.Harness, scenarioDef.promptAppend),
		RecordPrimaryChildPID: func(stepName string, pid int) error {
			return recordEvaluatorPrimaryChildPID(setup, stepName, pid)
		},
		AgentLauncher: agentexec.AttachedLauncher{},
		PromptAutomatedBlockers: func(review.AutomatedBlockerReview) ([]review.AutomatedBlockerDecision, error) {
			return nil, nil
		},
	})
	if outcome.Status != "" {
		if finishErr := finishReview(setup, outcome.Status); err == nil && finishErr != nil {
			return finishErr
		}
	}
	if err != nil {
		if finishErr := finishReview(setup, taskstate.ReviewStatusFailed); finishErr != nil {
			return errors.Join(err, finishErr)
		}
	}
	return err
}

func recordEvaluatorPrimaryChildPID(setup runSetup, stepName string, pid int) error {
	return workflow.RecordPrimaryReviewChildPID(setup.paths, nil, setup.store, "review-eval", setup.taskID, setup.attempt.Attempt, stepName, pid)
}

func evaluationSessionName(taskID string, scenarioDef scenario) string {
	title := strings.TrimSpace(scenarioDef.title)
	if title == "" {
		return "Reviewing " + taskID
	}
	return fmt.Sprintf("Reviewing %s %s", taskID, title)
}

func finishReview(setup runSetup, status taskstate.ReviewStatus) error {
	_, err := setup.store.FinishReview("review-eval", setup.taskID, setup.attempt.Attempt, status)
	if err != nil {
		return fmt.Errorf("finish isolated review attempt as %s: %w", status, err)
	}
	return nil
}

func evaluationPipeline() review.Pipeline {
	return review.Pipeline{
		Name: "review-context-eval",
		Steps: []review.Step{{
			Kind:  review.KindAgentReview,
			Name:  "ai-review",
			Agent: "reviewer",
		}},
	}
}

func agentConfig(opts Options, harness string, promptAppend string) agent.Config {
	return agent.Config{
		Defaults: agent.AgentDefaults{Implementer: "implementer", Reviewer: "reviewer"},
		Agents: map[string]agent.Profile{
			"implementer": {Command: "true"},
			"reviewer": {
				Harness:      harness,
				Model:        modelForHarness(opts, harness),
				Thinking:     opts.Thinking,
				Interactive:  false,
				PromptAppend: promptAppend,
			},
		},
	}
}

func modelForHarness(opts Options, harness string) string {
	if harness == HarnessPi {
		return opts.PiModel
	}
	return opts.CodexModel
}

type collectedState struct {
	reviewStatus string
	findings     []taskstate.ReviewFinding
	execution    *taskstate.AgentExecution
}

func collectRunState(setup runSetup) collectedState {
	stateValue, err := setup.store.Load("review-eval", setup.taskID)
	if err != nil {
		return collectedState{}
	}
	latest, ok := taskstate.LatestReview(stateValue)
	if !ok {
		return collectedState{}
	}
	collected := collectedState{reviewStatus: string(latest.Status), findings: latest.Findings}
	for index := len(latest.Steps) - 1; index >= 0; index-- {
		if latest.Steps[index].Execution != nil {
			collected.execution = latest.Steps[index].Execution
			break
		}
	}
	return collected
}

func scoreFindings(result *RunResult, known []knownFinding, findings []taskstate.ReviewFinding) {
	detected := map[string]bool{}
	matchedFinding := make([]bool, len(findings))
	for index, finding := range findings {
		if id := matchKnownFinding(finding, known, detected); id != "" {
			detected[id] = true
			matchedFinding[index] = true
		}
	}
	for _, seeded := range known {
		report := KnownFindingReport{ID: seeded.id, Title: seeded.title, Detected: detected[seeded.id]}
		result.SeededFindings = append(result.SeededFindings, report)
		if report.Detected {
			result.DetectedSeededIDs = append(result.DetectedSeededIDs, seeded.id)
		} else {
			result.MissedSeededIDs = append(result.MissedSeededIDs, seeded.id)
		}
	}
	for index, finding := range findings {
		if !matchedFinding[index] {
			result.UnexpectedFindings = append(result.UnexpectedFindings, findingReport(finding))
		}
	}
	if len(known) > 0 {
		result.FindingRecall = float64(len(result.DetectedSeededIDs)) / float64(len(known))
	}
	result.CompleteSession = len(result.MissedSeededIDs) == 0 && successfulReviewCompletion(*result)
}

func successfulReviewCompletion(run RunResult) bool {
	if run.OperationalErr != "" {
		return false
	}
	switch taskstate.ReviewStatus(run.ReviewStatus) {
	case taskstate.ReviewStatusPassed, taskstate.ReviewStatusBlocked:
		return true
	default:
		return false
	}
}

func matchKnownFinding(finding taskstate.ReviewFinding, known []knownFinding, detected map[string]bool) string {
	text := strings.ToLower(finding.Title + "\n" + finding.Description + "\n" + finding.SuggestedAction)
	for _, seeded := range known {
		if detected[seeded.id] {
			continue
		}
		if matchesAllGroups(text, seeded.matches) {
			return seeded.id
		}
	}
	return ""
}

func matchesAllGroups(text string, groups [][]string) bool {
	for _, group := range groups {
		if !matchesAnyTerm(text, group) {
			return false
		}
	}
	return true
}

func matchesAnyTerm(text string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(text, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

func findingReport(finding taskstate.ReviewFinding) FindingReport {
	return FindingReport{
		Type:        string(finding.Type),
		Title:       finding.Title,
		Description: finding.Description,
	}
}

func usageReport(execution taskstate.AgentExecution) UsageReport {
	report := UsageReport{
		CaptureStatus: string(execution.UsageCapture.Status),
		CaptureReason: execution.UsageCapture.Reason,
	}
	if execution.Usage == nil {
		report.UnknownReason = "usage_not_recorded"
		return report
	}
	report.Available = true
	report.Tokens = map[string]int{
		"input_tokens":            execution.Usage.InputTokens,
		"cached_input_tokens":     execution.Usage.CachedInputTokens,
		"output_tokens":           execution.Usage.OutputTokens,
		"reasoning_output_tokens": execution.Usage.ReasoningOutputTokens,
		"total_tokens":            execution.Usage.TotalTokens,
	}
	return report
}

func costReport(execution taskstate.AgentExecution) CostReport {
	resolved := agent.ResolveExecutionUsageCost(execution)
	if !resolved.Known {
		return CostReport{UnknownReason: resolved.UnknownReason}
	}
	source := strings.TrimSpace(resolved.Cost.Pricing.Source)
	if source == "" && execution.UsageCost != nil {
		source = execution.UsageCost.Source
	}
	return CostReport{
		Known:          true,
		Kind:           resolved.Cost.Kind,
		AmountMicroUSD: resolved.Cost.AmountMicroUSD,
		AmountUSD:      agent.FormatUsageCostUSD(resolved.Cost.AmountMicroUSD),
		Source:         source,
	}
}

func aggregateResults(runs []RunResult) []Aggregate {
	groups := map[string]*Aggregate{}
	for _, run := range runs {
		key := strings.Join([]string{run.Harness, run.Model, run.Variant, run.Scenario}, "\x00")
		aggregate := groups[key]
		if aggregate == nil {
			aggregate = &Aggregate{
				Harness:        run.Harness,
				Model:          run.Model,
				Variant:        run.Variant,
				Scenario:       run.Scenario,
				MissedBySeedID: map[string]int{},
			}
			groups[key] = aggregate
		}
		applyRunToAggregate(aggregate, run)
	}
	aggregates := make([]Aggregate, 0, len(groups))
	for _, aggregate := range groups {
		if aggregate.Runs > 0 {
			aggregate.MeanRecall /= float64(aggregate.Runs)
		}
		aggregate.KnownCostUSD = agent.FormatUsageCostUSD(aggregate.KnownCostMicroUSD)
		aggregates = append(aggregates, *aggregate)
	}
	sort.Slice(aggregates, func(i, j int) bool {
		return aggregateSortKey(aggregates[i]) < aggregateSortKey(aggregates[j])
	})
	return aggregates
}

func applyRunToAggregate(aggregate *Aggregate, run RunResult) {
	aggregate.Runs++
	aggregate.MeanRecall += run.FindingRecall
	if run.OperationalErr != "" {
		aggregate.OperationalErrors++
	}
	if run.CompleteSession && successfulReviewCompletion(run) {
		aggregate.CompleteSessions++
	}
	for _, id := range run.MissedSeededIDs {
		aggregate.MissedBySeedID[id]++
	}
	aggregate.UnexpectedFindings += len(run.UnexpectedFindings)
	if run.Cost.Known {
		aggregate.KnownCostMicroUSD += run.Cost.AmountMicroUSD
		incrementStringCount(&aggregate.KnownCostSources, run.Cost.Source)
		incrementStringCount(&aggregate.KnownCostKinds, run.Cost.Kind)
	} else {
		aggregate.UnknownCostRuns++
		incrementStringCount(&aggregate.UnknownCostReasons, run.Cost.UnknownReason)
	}
	if run.Usage.Available {
		aggregate.UsageAvailableRuns++
		if len(run.Usage.Tokens) > 0 && aggregate.UsageTokens == nil {
			aggregate.UsageTokens = map[string]int{}
		}
		for category, tokens := range run.Usage.Tokens {
			aggregate.UsageTokens[category] += tokens
		}
	} else {
		aggregate.UnknownUsageRuns++
		incrementStringCount(&aggregate.UnknownUsageReasons, run.Usage.UnknownReason)
	}
}

func incrementStringCount(counts *map[string]int, value string) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		normalized = "unknown"
	}
	if *counts == nil {
		*counts = map[string]int{}
	}
	(*counts)[normalized]++
}

func aggregateSortKey(aggregate Aggregate) string {
	return strings.Join([]string{aggregate.Harness, aggregate.Variant, aggregate.Scenario}, "\x00")
}

func totalCost(runs []RunResult) CostTotal {
	var total CostTotal
	total.IncludesFailedRuns = true
	for _, run := range runs {
		if run.Cost.Known {
			total.KnownRunCount++
			total.AmountMicroUSD += run.Cost.AmountMicroUSD
		} else {
			total.UnknownRunCount++
		}
	}
	total.AmountUSD = agent.FormatUsageCostUSD(total.AmountMicroUSD)
	return total
}

func piAcceptanceGate(aggregates []Aggregate) PiAcceptanceGate {
	gate := PiAcceptanceGate{Applicable: true}
	legacyGeneral, okLegacyGeneral := aggregateFor(aggregates, HarnessPi, VariantLegacy, ScenarioGeneral)
	exhaustiveGeneral, okExhaustiveGeneral := aggregateFor(aggregates, HarnessPi, VariantExhaustive, ScenarioGeneral)
	legacyArch, okLegacyArch := aggregateFor(aggregates, HarnessPi, VariantLegacy, ScenarioArchitecture)
	exhaustiveArch, okExhaustiveArch := aggregateFor(aggregates, HarnessPi, VariantExhaustive, ScenarioArchitecture)
	if !okLegacyGeneral || !okExhaustiveGeneral || !okLegacyArch || !okExhaustiveArch {
		return PiAcceptanceGate{}
	}
	gate.ExhaustiveCompletesTwoOfThreeGeneral = exhaustiveGeneral.Runs >= 3 && exhaustiveGeneral.CompleteSessions >= 2
	gate.ExhaustiveCompletesTwoOfThreeArch = exhaustiveArch.Runs >= 3 && exhaustiveArch.CompleteSessions >= 2
	gate.ExhaustiveRecallNotBelowLegacyGeneral = exhaustiveGeneral.MeanRecall >= legacyGeneral.MeanRecall
	gate.ExhaustiveRecallNotBelowLegacyArch = exhaustiveArch.MeanRecall >= legacyArch.MeanRecall
	gate.Passed = gate.ExhaustiveCompletesTwoOfThreeGeneral &&
		gate.ExhaustiveCompletesTwoOfThreeArch &&
		gate.ExhaustiveRecallNotBelowLegacyGeneral &&
		gate.ExhaustiveRecallNotBelowLegacyArch
	return gate
}

func aggregateFor(aggregates []Aggregate, harness string, variant string, scenarioName string) (Aggregate, bool) {
	for _, aggregate := range aggregates {
		if aggregate.Harness == harness && aggregate.Variant == variant && aggregate.Scenario == scenarioName {
			return aggregate, true
		}
	}
	return Aggregate{}, false
}

func scenarioByName(name string) scenario {
	switch name {
	case ScenarioArchitecture:
		return architectureScenario()
	default:
		return generalScenario()
	}
}

func generalScenario() scenario {
	return scenario{
		name:               ScenarioGeneral,
		title:              "Review checkout hardening",
		description:        "Harden checkout token validation, profile decoding, and price-cache freshness.",
		acceptanceCriteria: "Empty tokens are rejected, malformed profile JSON returns an error, and price updates invalidate cached totals.",
		files:              generalBaselineFiles(),
		changes:            generalChangedFiles(),
		knownFindings: []knownFinding{
			{id: "empty-token-auth-bypass", title: "Empty tokens authenticate successfully", matches: [][]string{{"empty token", "blank token", "missing token", "no token"}, {"auth", "token"}}},
			{id: "json-decode-error-ignored", title: "Profile JSON decode errors are ignored", matches: [][]string{{"json", "decode", "unmarshal"}, {"error", "err"}, {"ignore", "nil"}}},
			{id: "cache-invalidation-removed", title: "Price cache invalidation was removed", matches: [][]string{{"cache"}, {"invalidate", "stale", "delete", "clear"}}},
		},
	}
}

func architectureScenario() scenario {
	return scenario{
		name:               ScenarioArchitecture,
		title:              "Review order architecture boundaries",
		description:        "Keep order workflow boundaries clean while adding expedited order support.",
		acceptanceCriteria: "Domain code stays transport-agnostic, API handlers depend on services rather than concrete stores, and store lifecycle ownership remains explicit.",
		promptAppend:       "Review from an architecture perspective. Focus on module boundaries, dependency direction, data ownership, lifecycle management, and long-term maintainability.",
		files:              architectureBaselineFiles(),
		changes:            architectureChangedFiles(),
		knownFindings: []knownFinding{
			{id: "domain-imports-http", title: "Domain package imports HTTP transport concerns", matches: [][]string{{"domain"}, {"http", "transport"}, {"dependency", "coupling", "boundary"}}},
			{id: "api-bypasses-service-store", title: "API handler bypasses service boundary for storage", matches: [][]string{{"api", "handler"}, {"store", "storage", "sqlstore"}, {"service", "bypass", "boundary"}}},
			{id: "global-shared-store", title: "Package-level store hides lifecycle ownership", matches: [][]string{{"global", "package-level", "singleton"}, {"store"}, {"lifecycle", "ownership", "shared"}}},
		},
	}
}

func generalBaselineFiles() map[string]string {
	return map[string]string{
		".gitignore": ".beads/\n",
		"go.mod":     "module example.test/checkout\n\ngo 1.22\n",
		"checkout/auth.go": `package checkout

func Authenticate(token string, lookup func(string) bool) bool {
	if token == "" {
		return false
	}
	return lookup(token)
}
`,
		"checkout/profile.go": `package checkout

import "encoding/json"

type Profile struct {
	ID string ` + "`json:\"id\"`" + `
}

func DecodeProfile(raw []byte) (Profile, error) {
	var profile Profile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}
`,
		"checkout/prices.go": `package checkout

var totalCache = map[string]int{}

func UpdatePrice(id string, cents int) {
	invalidateTotal(id)
	totalCache[id] = cents
}

func invalidateTotal(id string) {
	delete(totalCache, id)
}
`,
	}
}

func generalChangedFiles() map[string]string {
	return map[string]string{
		"checkout/auth.go": `package checkout

func Authenticate(token string, lookup func(string) bool) bool {
	if token == "" {
		return true
	}
	return lookup(token)
}
`,
		"checkout/profile.go": `package checkout

import "encoding/json"

type Profile struct {
	ID string ` + "`json:\"id\"`" + `
}

func DecodeProfile(raw []byte) (Profile, error) {
	var profile Profile
	_ = json.Unmarshal(raw, &profile)
	return profile, nil
}
`,
		"checkout/prices.go": `package checkout

var totalCache = map[string]int{}

func UpdatePrice(id string, cents int) {
	totalCache[id] = cents
}

func invalidateTotal(id string) {
	delete(totalCache, id)
}
`,
	}
}

func architectureBaselineFiles() map[string]string {
	return map[string]string{
		".gitignore": ".beads/\n",
		"go.mod":     "module example.test/orders\n\ngo 1.22\n",
		"internal/domain/order.go": `package domain

type Order struct {
	ID     string
	Status string
}
`,
		"internal/orders/service.go": `package orders

import "example.test/orders/internal/domain"

type Store interface {
	Save(domain.Order) error
}

type Service struct {
	store Store
}

func NewService(store Store) Service {
	return Service{store: store}
}
`,
		"internal/api/handler.go": `package api

import "example.test/orders/internal/orders"

type Handler struct {
	service orders.Service
}

func NewHandler(service orders.Service) Handler {
	return Handler{service: service}
}
`,
		"internal/storage/sqlstore/store.go": `package sqlstore

import "example.test/orders/internal/domain"

type Store struct{}

func (Store) Save(domain.Order) error {
	return nil
}
`,
	}
}

func architectureChangedFiles() map[string]string {
	return map[string]string{
		"internal/domain/order.go": `package domain

import "net/http"

type Order struct {
	ID     string
	Status string
}

func StatusFromRequest(request *http.Request) string {
	return request.URL.Query().Get("status")
}
`,
		"internal/api/handler.go": `package api

import (
	"example.test/orders/internal/domain"
	"example.test/orders/internal/storage/sqlstore"
)

type Handler struct{}

func NewHandler() Handler {
	return Handler{}
}

func (Handler) SaveExpedited(order domain.Order) error {
	return sqlstore.SharedStore.Save(order)
}
`,
		"internal/storage/sqlstore/store.go": `package sqlstore

import "example.test/orders/internal/domain"

var SharedStore = Store{}

type Store struct{}

func (Store) Save(domain.Order) error {
	return nil
}
`,
	}
}

var errNoEvaluationRuns = errors.New("no evaluation runs selected")

func ensureRuns(specs []runSpec) error {
	if len(specs) == 0 {
		return errNoEvaluationRuns
	}
	return nil
}
