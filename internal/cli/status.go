package cli

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

func newStatusCommand(opts *rootOptions) *cobra.Command {
	var full bool
	var noTruncate bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the local cross-repository action queue",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runStatus(command, opts, full, noTruncate)
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "show lower-priority groups such as blocked and done/closed")
	cmd.Flags().BoolVar(&noTruncate, "no-truncate", false, "preserve unbounded status table output")
	return cmd
}

func runStatus(command *cobra.Command, opts *rootOptions, full bool, noTruncate bool) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "status"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for status projection")

	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}
	logger.DebugContext(command.Context(), "querying local task snapshots", slog.Int("repo_count", len(taskCtx.Sources)))

	snapshot := taskCtx.Aggregator.Snapshot(command.Context())
	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	runStates, runStateFailures := taskRunStateIndex(paths, snapshot)
	if len(runStateFailures) > 0 {
		snapshot.Failures = append(snapshot.Failures, runStateFailures...)
	}
	projection := status.ProjectWithLocalTaskStates(snapshot, runStates)
	logger.DebugContext(
		command.Context(),
		"projected local status",
		slog.Int("repo_count", len(snapshot.Repositories)),
		slog.Int("failure_count", len(snapshot.Failures)),
		slog.Int("run_state_count", len(runStates)),
	)

	output := command.OutOrStdout()
	renderOptions := statusRenderOptionsForOutput(output, noTruncate, defaultStatusWidthDetector)
	if err := renderStatus(output, projection, full, renderOptions); err != nil {
		return err
	}
	if snapshot.HasFailures() {
		writeRepoFailures(command.ErrOrStderr(), "status", snapshot.Failures)
		return partialRepoFailureError{operation: "status", failures: snapshot.Failures}
	}
	return nil
}

func taskRunStateIndex(
	paths state.Paths,
	snapshot taskmodel.SnapshotResult,
) (status.LocalTaskStateIndex, []taskmodel.RepoFailure) {
	store := taskstate.Service(taskstate.NewStore(paths))
	index := status.LocalTaskStateIndex{}
	failures := make([]taskmodel.RepoFailure, 0)

	for _, repoSnapshot := range snapshot.Repositories {
		for _, taskItem := range repoSnapshot.Tasks {
			state, err := store.Load(repoSnapshot.Repository.ID, taskItem.ID)
			if err != nil {
				failures = append(failures, taskmodel.RepoFailure{
					Repository: repoSnapshot.Repository,
					Source:     "task_state",
					Operation:  "load",
					Err:        err,
				})
				continue
			}
			latest, ok := taskstate.LatestRun(state)
			if !ok {
				continue
			}
			latestCopy := latest
			expectedTargets, err := workflow.ExpectedTargetsForTask(repoSnapshot.Repository, taskItem.ID, paths)
			latestReview, hasReview := taskstate.LatestReview(state)
			latestFinalizationFailure, hasFinalizationFailure := taskstate.LatestFinalizationFailure(state)
			localState := status.LocalTaskState{
				LatestRun:    &latestCopy,
				Finalization: taskstate.FinalizationFacts(state),
			}
			if hasReview {
				localState.LatestReview = &latestReview
			}
			if hasFinalizationFailure {
				localState.LatestFinalizationFailure = &latestFinalizationFailure
			}
			if err == nil {
				localState.ExpectedTargets = &expectedTargets
			}
			index[status.RunStateKey(repoSnapshot.Repository.ID, taskItem.ID)] = localState
		}
	}
	return index, failures
}

type statusRenderOptions struct {
	MaxWidth   int
	NoTruncate bool
}

type statusWidthDetector struct {
	OutputWidth func(io.Writer) (int, bool)
	WatchWidth  func() (int, bool)
}

var defaultStatusWidthDetector = statusWidthDetector{
	OutputWidth: interactiveTerminalWidth,
	WatchWidth:  watchTerminalWidth,
}

type statusRenderLayout struct {
	IncludeRepo    bool
	ShortDetail    bool
	TruncateTitles bool
	MaxWidth       int
}

func statusRenderOptionsForOutput(
	output io.Writer,
	noTruncate bool,
	detector statusWidthDetector,
) statusRenderOptions {
	options := statusRenderOptions{NoTruncate: noTruncate}
	if noTruncate {
		return options
	}
	if detector.OutputWidth != nil {
		if width, ok := detector.OutputWidth(output); ok {
			options.MaxWidth = width
			return options
		}
	}
	if detector.WatchWidth != nil {
		if width, ok := detector.WatchWidth(); ok {
			options.MaxWidth = width
		}
	}
	return options
}

func renderStatus(
	output interface{ Write([]byte) (int, error) },
	projection status.Projection,
	full bool,
	options statusRenderOptions,
) error {
	visibleGroups := visibleStatusGroups(projection.Groups, full)
	layout := statusLayoutFor(visibleGroups, options)
	for i, group := range visibleGroups {
		if i > 0 {
			if _, err := fmt.Fprintln(output); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(output, "%s (%d)\n", group.Title, len(group.Entries)); err != nil {
			return err
		}
		if len(group.Entries) == 0 {
			if _, err := fmt.Fprintln(output, "-"); err != nil {
				return err
			}
			continue
		}
		if err := renderStatusEntries(output, group, layout); err != nil {
			return err
		}
	}
	return nil
}

func statusLayoutFor(groups []status.Group, options statusRenderOptions) statusRenderLayout {
	if options.NoTruncate || options.MaxWidth <= 0 {
		return statusRenderLayout{IncludeRepo: true}
	}
	candidates := []statusRenderLayout{
		{IncludeRepo: true, MaxWidth: options.MaxWidth},
		{IncludeRepo: true, ShortDetail: true, MaxWidth: options.MaxWidth},
		{ShortDetail: true, MaxWidth: options.MaxWidth},
	}
	for _, candidate := range candidates {
		if statusGroupsFit(groups, candidate) {
			return candidate
		}
	}
	return statusRenderLayout{
		ShortDetail:    true,
		TruncateTitles: true,
		MaxWidth:       options.MaxWidth,
	}
}

func statusGroupsFit(groups []status.Group, layout statusRenderLayout) bool {
	for _, group := range groups {
		if len(group.Entries) == 0 {
			continue
		}
		headers, rows := statusEntryTable(
			group,
			statusGroupShowsDetail(group.ID),
			layout.IncludeRepo,
			layout.ShortDetail,
		)
		if tableWidth(headers, rows) > layout.MaxWidth {
			return false
		}
	}
	return true
}

func renderStatusEntries(output io.Writer, group status.Group, layout statusRenderLayout) error {
	showDetail := statusGroupShowsDetail(group.ID)
	headers, rows := statusEntryTable(group, showDetail, layout.IncludeRepo, layout.ShortDetail)
	if layout.TruncateTitles {
		rows = truncateStatusTitles(headers, rows, layout.MaxWidth)
	}
	return renderTable(output, headers, rows)
}

func statusEntryTable(
	group status.Group,
	showDetail bool,
	includeRepo bool,
	shortDetail bool,
) ([]string, [][]string) {
	headers := []string{"REPO", "TASK_ID", "P", "TITLE"}
	if !includeRepo {
		headers = headers[1:]
	}
	if showDetail {
		headers = append(headers, "DETAIL")
	}

	rows := make([][]string, 0, len(group.Entries))
	for _, entry := range group.Entries {
		switch entry.Kind {
		case status.EntryTask:
			rows = append(rows, statusTaskEntryTableRow(entry, showDetail, includeRepo, shortDetail))
		case status.EntryRepoFailure:
			rows = append(rows, statusFailureEntryTableRow(entry, showDetail, includeRepo, shortDetail))
		}
	}
	return headers, rows
}

func statusTaskEntryTableRow(entry status.Entry, showDetail bool, includeRepo bool, shortDetail bool) []string {
	row := make([]string, 0, 5)
	if includeRepo {
		row = append(row, entry.Repository.Name)
	}
	row = append(row, entry.Task.ID, strconv.Itoa(entry.Task.Priority), entry.Task.Title)
	if showDetail {
		detail := entry.Detail
		if shortDetail {
			detail = shortStatusDetail(entry, detail)
		}
		row = append(row, detail)
	}
	return row
}

func statusFailureEntryTableRow(entry status.Entry, showDetail bool, includeRepo bool, shortDetail bool) []string {
	detail := entry.Detail
	if detail == "" && entry.Failure != nil {
		detail = entry.Failure.Error()
	}
	title := fmt.Sprintf("repo %s (prefix %s)", entry.Repository.ID, entry.Repository.TaskIDPrefix)

	row := make([]string, 0, 5)
	if includeRepo {
		row = append(row, entry.Repository.Name)
	}
	row = append(row, "-", "-", title)
	if showDetail {
		if shortDetail {
			detail = shortStatusDetail(entry, detail)
		}
		row = append(row, detail)
	}
	return row
}

func shortStatusDetail(entry status.Entry, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return detail
	}
	if entry.Kind == status.EntryRepoFailure {
		if entry.Source != "" && entry.Operation != "" {
			return entry.Source + "/" + entry.Operation + " failed"
		}
		return "repo diagnostic failed"
	}
	if shortPRDetail := shortPullRequestDetail(detail); shortPRDetail != "" {
		return shortPRDetail
	}
	if shortReviewDetail := shortReviewDetail(detail); shortReviewDetail != "" {
		return shortReviewDetail
	}
	if shortCompletionDetail := shortCompletionDetail(detail); shortCompletionDetail != "" {
		return shortCompletionDetail
	}
	switch {
	case detail == "no attached run recorded":
		return "no run"
	case strings.HasPrefix(detail, "backend status is open but local "):
		return "open; " + shortRunDetail(strings.TrimPrefix(detail, "backend status is open but local "))
	case strings.Contains(detail, "; agent exited without completion"):
		return strings.Replace(shortRunDetail(strings.TrimSuffix(detail, "; agent exited without completion")), " succeeded", " succeeded; no completion", 1)
	case strings.HasPrefix(detail, "run attempt "):
		return shortRunDetail(detail)
	case strings.HasPrefix(detail, "missing required external reference;"):
		return "missing external ref"
	case strings.HasPrefix(detail, "missing dependency "):
		return detail
	case strings.HasPrefix(detail, "blocked by "):
		return shortBlockedDetail(detail)
	case strings.HasPrefix(detail, "status ") && strings.HasSuffix(detail, " is not locally actionable"):
		return strings.TrimSuffix(detail, " is not locally actionable")
	default:
		return detail
	}
}

func shortReviewDetail(detail string) string {
	switch {
	case detail == "local review; run task review":
		return "local review"
	case detail == "review running":
		return "review running"
	case strings.HasPrefix(detail, "review blocked by "):
		return "review blocked"
	case detail == "review blockers targeted; run task review":
		return "review follow-up ready"
	case detail == "review aborted; run task review":
		return "review aborted"
	case detail == "review failed operationally; run task review":
		return "review failed"
	case detail == "review passed; publication failed; fix publication issue, then run task done":
		return "publication failed"
	case detail == "review passed; run task done":
		return "review passed"
	default:
		return ""
	}
}

func shortCompletionDetail(detail string) string {
	switch detail {
	case "finalization recorded but backend task is not closed":
		return "finalized but open"
	case "completion target is not the deterministic Orpheus worktree/team target":
		return "wrong PR target"
	case "completion target is not the deterministic Orpheus main/solo target":
		return "wrong local target"
	default:
		return ""
	}
}

func shortPullRequestDetail(detail string) string {
	parsed, err := url.Parse(detail)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "pull" && parts[i+1] != "" {
			return "PR #" + parts[i+1]
		}
	}
	return "PR"
}

func shortRunDetail(detail string) string {
	parts := strings.Fields(detail)
	if len(parts) < 3 || parts[0] != "run" || parts[1] != "attempt" {
		return detail
	}
	attempt := parts[2]
	switch {
	case strings.HasSuffix(detail, " is running"):
		return "run " + attempt + " running"
	case strings.HasSuffix(detail, " failed"):
		return "run " + attempt + " failed"
	case strings.HasSuffix(detail, " succeeded"):
		return "run " + attempt + " succeeded"
	case strings.Contains(detail, " has status "):
		_, statusText, _ := strings.Cut(detail, " has status ")
		return "run " + attempt + " " + statusText
	default:
		return detail
	}
}

func shortBlockedDetail(detail string) string {
	dependencies := strings.Split(strings.TrimPrefix(detail, "blocked by "), ",")
	count := 0
	for _, dependency := range dependencies {
		if strings.TrimSpace(dependency) != "" {
			count++
		}
	}
	if count <= 1 {
		return detail
	}
	return fmt.Sprintf("blocked by %d deps", count)
}

func truncateStatusTitles(headers []string, rows [][]string, maxWidth int) [][]string {
	titleIndex := -1
	for i, header := range headers {
		if header == "TITLE" {
			titleIndex = i
			break
		}
	}
	if titleIndex < 0 {
		return rows
	}
	widths := tableColumnWidths(headers, rows)
	fixedWidth := 0
	for i, width := range widths {
		if i == titleIndex {
			continue
		}
		fixedWidth += width
	}
	fixedWidth += statusTablePaddingWidth(len(headers))
	titleWidth := maxWidth - fixedWidth
	if titleWidth < 1 {
		titleWidth = 1
	}

	truncated := make([][]string, 0, len(rows))
	for _, row := range rows {
		next := append([]string(nil), row...)
		if titleIndex < len(next) {
			next[titleIndex] = truncateCell(next[titleIndex], titleWidth)
		}
		truncated = append(truncated, next)
	}
	return truncated
}

func tableWidth(headers []string, rows [][]string) int {
	widths := tableColumnWidths(headers, rows)
	total := statusTablePaddingWidth(len(widths))
	for _, width := range widths {
		total += width
	}
	return total
}

func tableColumnWidths(headers []string, rows [][]string) []int {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = displayWidth(sanitizeTableCell(header))
	}
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				continue
			}
			widths[i] = max(widths[i], displayWidth(sanitizeTableCell(cell)))
		}
	}
	return widths
}

func statusTablePaddingWidth(columnCount int) int {
	if columnCount <= 1 {
		return 0
	}
	return (columnCount - 1) * 2
}

func truncateCell(value string, width int) string {
	if width <= 0 || displayWidth(value) <= width {
		return value
	}
	runes := []rune(value)
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func displayWidth(value string) int {
	return len([]rune(value))
}

func interactiveTerminalWidth(output io.Writer) (int, bool) {
	file, ok := output.(*os.File)
	if !ok {
		return 0, false
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return 0, false
	}
	width, ok := terminalWidth(file.Fd())
	if !ok {
		return 0, false
	}
	return width, true
}

func watchTerminalWidth() (int, bool) {
	if !runningUnderWatch() {
		return 0, false
	}
	terminal, err := os.Open("/dev/tty")
	if err != nil {
		return 0, false
	}
	defer func() {
		_ = terminal.Close()
	}()
	return terminalWidth(terminal.Fd())
}

func visibleStatusGroups(groups []status.Group, full bool) []status.Group {
	if full {
		return groups
	}

	visible := make([]status.Group, 0, len(groups))
	for _, group := range groups {
		if statusGroupHiddenByDefault(group.ID) {
			continue
		}
		visible = append(visible, group)
	}
	return visible
}

func statusGroupHiddenByDefault(groupID status.GroupID) bool {
	return groupID == status.GroupBlocked || groupID == status.GroupDoneClosed
}

func statusGroupShowsDetail(groupID status.GroupID) bool {
	switch groupID {
	case status.GroupReadyToRun, status.GroupDoneClosed:
		return false
	default:
		return true
	}
}
