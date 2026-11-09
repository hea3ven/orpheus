package pullrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"strings"

	"github.com/hea3ven/orpheus/internal/logging"
)

// GHProvider implements Provider with the GitHub CLI.
type GHProvider struct {
	Logger      *slog.Logger
	Binary      string
	Environment []string
}

// FindByBranch reconciles any lifecycle outcome by head, including retargeted
// PRs. Filtering by the expected base would mistake retargeting for absence.
func (p GHProvider) FindByBranch(ctx context.Context, req FindByBranchRequest) (PullRequest, bool, error) {
	if err := validateFindRequest(req); err != nil {
		return PullRequest{}, false, err
	}
	output, err := runGH(ctx, p, req.RepositoryPath, "find_pr", "", diagnosticAttrs(req.Diagnostics), "pr", "list",
		"--state", "all",
		"--head", req.HeadBranch,
		"--json", "url",
		"--limit", "2",
	)
	if err != nil {
		return PullRequest{}, false, fmt.Errorf("find GitHub PR for %s -> %s: %w", req.HeadBranch, req.BaseBranch, err)
	}

	var rows []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		return PullRequest{}, false, fmt.Errorf("find GitHub PR: provider output was not JSON: %w", err)
	}
	if len(rows) == 0 {
		return PullRequest{}, false, nil
	}
	if len(rows) != 1 {
		return PullRequest{}, false, errors.New("multiple PRs match publication branch pair; reconcile before retry")
	}
	prURL := strings.TrimSpace(rows[0].URL)
	if !isHTTPURL(prURL) {
		return PullRequest{}, false, fmt.Errorf("find GitHub PR: provider output did not include a valid PR URL")
	}
	return PullRequest{URL: prURL}, true, nil
}

// StatusByURL returns the current lifecycle state for a pull request URL.
func (p GHProvider) StatusByURL(ctx context.Context, req StatusByURLRequest) (PullRequestStatus, error) {
	if err := validateStatusByURLRequest(req); err != nil {
		return PullRequestStatus{}, err
	}
	prURL := strings.TrimSpace(req.URL)
	output, err := runGH(ctx, p, "", "poll_pr", "", diagnosticAttrs(req.Diagnostics), "pr", "view", prURL, "--json", "url,state,mergedAt,headRefName,baseRefName,headRefOid,title,body,isCrossRepository")
	if err != nil {
		return PullRequestStatus{}, fmt.Errorf("poll GitHub PR %s: %w", prURL, err)
	}
	status, err := decodeGHPRStatus(output)
	if err != nil {
		return PullRequestStatus{}, fmt.Errorf("poll GitHub PR %s: %w", prURL, err)
	}
	return status, nil
}

// Create creates a GitHub pull request and returns its URL.
func (p GHProvider) Create(ctx context.Context, req CreateRequest) (PullRequest, error) {
	if err := validateCreateRequest(req); err != nil {
		return PullRequest{}, err
	}
	output, err := runGH(ctx, p, req.RepositoryPath, "create_pr", req.Body, diagnosticAttrs(req.Diagnostics), "pr", "create",
		"--base", req.BaseBranch,
		"--head", req.HeadBranch,
		"--title", req.Title,
		"--body-file", "-",
	)
	if err != nil {
		return PullRequest{}, fmt.Errorf("create GitHub PR for %s -> %s: %w", req.HeadBranch, req.BaseBranch, err)
	}
	prURL := firstHTTPURL(output)
	if prURL == "" {
		return PullRequest{}, fmt.Errorf("create GitHub PR: provider output did not include a valid PR URL")
	}
	return PullRequest{URL: prURL}, nil
}

// Update applies the exact consented narrative to an existing PR.
func (p GHProvider) Update(ctx context.Context, req UpdateRequest) error {
	if err := validateStatusByURLRequest(StatusByURLRequest{URL: req.URL}); err != nil {
		return err
	}
	if strings.TrimSpace(req.RepositoryPath) == "" || strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Body) == "" {
		return errors.New("PR update requires repository, title and body")
	}
	_, err := runGH(ctx, p, req.RepositoryPath, "update_pr", req.Body, diagnosticAttrs(req.Diagnostics), "pr", "edit", req.URL, "--title", req.Title, "--body-file", "-")
	return err
}

func validateStatusByURLRequest(req StatusByURLRequest) error {
	prURL := strings.TrimSpace(req.URL)
	if prURL == "" {
		return errors.New("pull request URL is required")
	}
	if !isHTTPURL(prURL) {
		return fmt.Errorf("pull request URL %q is invalid", req.URL)
	}
	return nil
}

func validateFindRequest(req FindByBranchRequest) error {
	if strings.TrimSpace(req.RepositoryPath) == "" {
		return errors.New("pull request repository path is required")
	}
	if err := validateBranchArg("head branch", req.HeadBranch); err != nil {
		return err
	}
	return validateBranchArg("base branch", req.BaseBranch)
}

func validateCreateRequest(req CreateRequest) error {
	if err := validateFindRequest(FindByBranchRequest{
		RepositoryPath: req.RepositoryPath,
		HeadBranch:     req.HeadBranch,
		BaseBranch:     req.BaseBranch,
	}); err != nil {
		return err
	}
	if strings.TrimSpace(req.Title) == "" {
		return errors.New("pull request title is required")
	}
	if strings.TrimSpace(req.Body) == "" {
		return errors.New("pull request body is required")
	}
	return nil
}

func validateBranchArg(label string, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("pull request %s is required", label)
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("pull request %s %q is unsafe", label, branch)
	}
	return nil
}

func decodeGHPRStatus(output string) (PullRequestStatus, error) {
	var row struct {
		URL             string `json:"url"`
		State           string `json:"state"`
		MergedAt        string `json:"mergedAt"`
		HeadBranch      string `json:"headRefName"`
		BaseBranch      string `json:"baseRefName"`
		HeadCommit      string `json:"headRefOid"`
		CrossRepository bool   `json:"isCrossRepository"`
		Title           string `json:"title"`
		Body            string `json:"body"`
	}
	if err := json.Unmarshal([]byte(output), &row); err != nil {
		return PullRequestStatus{}, fmt.Errorf("provider output was not JSON: %w", err)
	}
	prURL := strings.TrimSpace(row.URL)
	if !isHTTPURL(prURL) {
		return PullRequestStatus{}, errors.New("provider output did not include a valid PR URL")
	}
	status := PullRequestStatus{URL: prURL, HeadBranch: row.HeadBranch, BaseBranch: row.BaseBranch, HeadCommit: row.HeadCommit, CrossRepository: row.CrossRepository, Title: row.Title, Body: row.Body}
	if strings.TrimSpace(row.MergedAt) != "" {
		status.State = StateMerged
		return status, nil
	}

	switch strings.ToUpper(strings.TrimSpace(row.State)) {
	case "OPEN":
		status.State = StateOpen
		return status, nil
	case "MERGED":
		status.State = StateMerged
		return status, nil
	case "CLOSED":
		status.State = StateClosed
		return status, nil
	default:
		return PullRequestStatus{}, fmt.Errorf("provider output included unsupported PR state %q", row.State)
	}
}

func runGH(
	ctx context.Context,
	provider GHProvider,
	dir string,
	operation string,
	stdin string,
	attrs []slog.Attr,
	args ...string,
) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	startAttrs := []slog.Attr{
		slog.String("component", "github"),
		slog.String("operation", operation),
		slog.String("cwd", dir),
	}
	startAttrs = append(startAttrs, attrs...)
	span := logging.Start(ctx, provider.Logger, "github cli command", startAttrs...)
	binary := provider.Binary
	if binary == "" {
		binary = "gh"
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = dir
	if provider.Environment != nil {
		command.Env = append([]string{}, provider.Environment...)
	}
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	finishAttrs := ghExitAttrs(command, err)
	span.FinishError(ctx, err, finishAttrs...)
	if err == nil {
		return stdout.String(), nil
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" {
		message = strings.TrimSpace(stdout.String())
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("gh CLI executable not found; install GitHub CLI or ensure gh is on PATH: %w", err)
	}
	if message == "" {
		message = err.Error()
	}
	return "", classifyGHError(message, err)
}

func diagnosticAttrs(diagnostics DiagnosticContext) []slog.Attr {
	attrs := make([]slog.Attr, 0, 4)
	if repoID := strings.TrimSpace(diagnostics.RepoID); repoID != "" {
		attrs = append(attrs, slog.String("repo_id", repoID))
	}
	if taskID := strings.TrimSpace(diagnostics.TaskID); taskID != "" {
		attrs = append(attrs, slog.String("task_id", taskID))
	}
	if branch := strings.TrimSpace(diagnostics.Branch); branch != "" {
		attrs = append(attrs, slog.String("branch", branch))
	}
	if diagnostics.HasPR {
		attrs = append(attrs, slog.Bool("has_pr", true))
	}
	return attrs
}

func ghExitAttrs(command *exec.Cmd, err error) []slog.Attr {
	if command != nil && command.ProcessState != nil {
		return []slog.Attr{slog.Int("exit_code", command.ProcessState.ExitCode())}
	}
	if exitCode, ok := logging.ExitCode(err); ok {
		return []slog.Attr{slog.Int("exit_code", exitCode)}
	}
	return nil
}

func classifyGHError(message string, err error) error {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "authenticate") ||
		strings.Contains(lower, "authorization") ||
		strings.Contains(lower, "not logged in") ||
		strings.Contains(lower, "gh auth login") ||
		strings.Contains(lower, "login required"):
		return fmt.Errorf("gh authentication failed or is missing: %w: %s", err, message)
	case strings.Contains(lower, "not a git repository") ||
		strings.Contains(lower, "no git remotes") ||
		strings.Contains(lower, "could not resolve") ||
		strings.Contains(lower, "repository not found"):
		return fmt.Errorf("GitHub repository or remote could not be resolved by gh: %w: %s", err, message)
	default:
		return fmt.Errorf("gh provider command failed: %w: %s", err, message)
	}
}

func firstHTTPURL(output string) string {
	for _, field := range strings.Fields(output) {
		field = strings.Trim(field, "\"'()[]{}<>.,")
		if isHTTPURL(field) {
			return field
		}
	}
	return ""
}

func isHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
