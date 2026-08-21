package cli

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
)

func newStatusCommand(opts *rootOptions) *cobra.Command {
	var full bool
	var noTruncate bool
	var sortValue string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the local cross-repository action queue",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runStatus(command, opts, full, noTruncate, sortValue)
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "show lower-priority groups such as blocked and done/closed")
	cmd.Flags().BoolVar(&noTruncate, "no-truncate", false, "preserve unbounded status table output")
	cmd.Flags().StringVar(&sortValue, "sort", string(taskViewSortStatus), "order by status, created, or updated")
	return cmd
}

func runStatus(command *cobra.Command, opts *rootOptions, full bool, noTruncate bool, sortValue string) error {
	sortMode, err := normalizeTaskViewSort(sortValue, taskViewSortStatus)
	if err != nil {
		return err
	}

	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "status"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for status projection")

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContextFromInvocation(deps)
	if err != nil {
		return err
	}
	logger.DebugContext(command.Context(), "querying local task snapshots", slog.Int("repo_count", len(taskCtx.Sources)))

	snapshot := taskCtx.Aggregator.Snapshot(command.Context())
	runStates, runStateFailures := taskRunStateIndex(deps, snapshot)
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
	if err := renderStatusWithSort(output, projection, full, renderOptions, sortMode); err != nil {
		return err
	}
	if snapshot.HasFailures() {
		writeRepoFailures(command.ErrOrStderr(), "status", snapshot.Failures)
		return partialRepoFailureError{operation: "status", failures: snapshot.Failures}
	}
	return nil
}

type taskStateReader interface {
	TaskIDs(repoID string) ([]string, error)
	Load(repoID, taskID string) (taskstate.TaskState, error)
}

func taskRunStateIndex(
	deps *invocationDependencies,
	snapshot taskmodel.SnapshotResult,
) (status.LocalTaskStateIndex, []taskmodel.RepoFailure) {
	return taskRunStateIndexForStore(deps.paths, deps.taskStateStore, snapshot)
}

func taskRunStateIndexForStore(
	paths state.Paths,
	store taskStateReader,
	snapshot taskmodel.SnapshotResult,
) (status.LocalTaskStateIndex, []taskmodel.RepoFailure) {
	return taskRunStateIndexForStoreCandidates(paths, store, snapshot, nil)
}

// taskRunStateIndexForCandidates loads persisted local state only for output
// candidates while retaining full relationship context for eligibility policy.
func taskRunStateIndexForCandidates(
	deps *invocationDependencies,
	snapshot taskmodel.SnapshotResult,
	candidates []taskmodel.RepoTask,
) (status.LocalTaskStateIndex, []taskmodel.RepoFailure) {
	candidateIDs := make(map[string]map[string]bool, len(snapshot.Repositories))
	for _, repository := range snapshot.Repositories {
		candidateIDs[repository.Repository.ID] = make(map[string]bool)
	}
	for _, candidate := range candidates {
		ids := candidateIDs[candidate.Repository.ID]
		if ids == nil {
			ids = make(map[string]bool)
			candidateIDs[candidate.Repository.ID] = ids
		}
		ids[candidate.Task.ID] = true
	}
	return taskRunStateIndexForStoreCandidates(deps.paths, deps.taskStateStore, snapshot, candidateIDs)
}

func taskRunStateIndexForStoreCandidates(
	paths state.Paths,
	store taskStateReader,
	snapshot taskmodel.SnapshotResult,
	candidateIDs map[string]map[string]bool,
) (status.LocalTaskStateIndex, []taskmodel.RepoFailure) {
	index := status.LocalTaskStateIndex{}
	failures := make([]taskmodel.RepoFailure, 0)
	for _, repoSnapshot := range snapshot.Repositories {
		repoIndex, repoFailures := taskRunStateIndexForRepositoryCandidates(
			paths,
			store,
			repoSnapshot,
			candidateIDs[repoSnapshot.Repository.ID],
		)
		for key, localState := range repoIndex {
			index[key] = localState
		}
		failures = append(failures, repoFailures...)
	}
	return index, failures
}

func taskRunStateIndexForRepositoryCandidates(
	paths state.Paths,
	store taskStateReader,
	repoSnapshot taskmodel.RepositorySnapshot,
	selectedTaskIDs map[string]bool,
) (status.LocalTaskStateIndex, []taskmodel.RepoFailure) {
	candidates := status.LocalTaskStateCandidates(repoSnapshot.Repository, repoSnapshot.Tasks)
	if selectedTaskIDs != nil && len(selectedTaskIDs) == 0 {
		return status.LocalTaskStateIndex{}, nil
	}
	persistedTaskIDs, err := store.TaskIDs(repoSnapshot.Repository.ID)
	if err != nil {
		return nil, taskStateInventoryFailure(repoSnapshot.Repository, candidates, err)
	}

	persisted := taskStateIDSet(persistedTaskIDs)
	index := status.LocalTaskStateIndex{}
	failures := make([]taskmodel.RepoFailure, 0)
	for _, taskItem := range repoSnapshot.Tasks {
		if selectedTaskIDs != nil && !selectedTaskIDs[taskItem.ID] {
			continue
		}
		if !persisted[taskItem.ID] || !candidates[taskItem.ID] {
			continue
		}
		localState, ok, err := loadLocalTaskState(paths, store, repoSnapshot.Repository, taskItem)
		if err != nil {
			failures = append(failures, taskStateLoadFailure(repoSnapshot.Repository, err))
			continue
		}
		if ok {
			index[status.RunStateKey(repoSnapshot.Repository.ID, taskItem.ID)] = localState
		}
	}
	return index, failures
}

func taskStateInventoryFailure(
	repository taskmodel.Repository,
	candidates map[string]bool,
	err error,
) []taskmodel.RepoFailure {
	if len(candidates) == 0 {
		return nil
	}
	return []taskmodel.RepoFailure{{
		Repository: repository,
		Source:     "task_state",
		Operation:  "list",
		Err:        err,
	}}
}

func taskStateIDSet(taskIDs []string) map[string]bool {
	persisted := make(map[string]bool, len(taskIDs))
	for _, taskID := range taskIDs {
		persisted[taskID] = true
	}
	return persisted
}

func taskStateLoadFailure(repository taskmodel.Repository, err error) taskmodel.RepoFailure {
	return taskmodel.RepoFailure{
		Repository: repository,
		Source:     "task_state",
		Operation:  "load",
		Err:        err,
	}
}

func loadLocalTaskState(
	paths state.Paths,
	store taskStateReader,
	repository taskmodel.Repository,
	taskItem taskmodel.Task,
) (status.LocalTaskState, bool, error) {
	state, err := store.Load(repository.ID, taskItem.ID)
	if err != nil {
		return status.LocalTaskState{}, false, err
	}
	latest, hasLatestRun := taskstate.LatestRun(state)
	if !hasLatestRun && state.ActiveSyncConflict == nil {
		return status.LocalTaskState{}, false, nil
	}

	localState := status.LocalTaskState{
		Runs:               append([]taskstate.RunAttempt(nil), state.Runs...),
		Finalization:       taskstate.FinalizationFacts(state),
		ActiveSyncConflict: state.ActiveSyncConflict,
	}
	if hasLatestRun {
		latestCopy := latest
		localState.LatestRun = &latestCopy
	}
	if target, hasTarget := taskstate.GitFactsFor(state); hasTarget {
		localState.GitFacts = &target
	}
	if latestReview, hasReview := taskstate.LatestReview(state); hasReview {
		localState.LatestReview = &latestReview
	}
	if latestFinalizationFailure, hasFinalizationFailure := taskstate.LatestFinalizationFailure(state); hasFinalizationFailure {
		localState.LatestFinalizationFailure = &latestFinalizationFailure
	}
	recordedBranch := ""
	if localState.GitFacts != nil {
		recordedBranch = localState.GitFacts.Branch
	}
	if expectedTargets, err := tasktarget.ExpectedTargetsForTaskOrRecordedBranch(repository, taskItem, recordedBranch, paths); err == nil {
		localState.ExpectedTargets = &expectedTargets
	}
	return localState, true, nil
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

const statusSymbolPreferredTitleWidth = 35

type statusRenderLayout struct {
	IncludeRepo     bool
	IncludePriority bool
	ShortDetail     bool
	SymbolStatus    bool
	TruncateTitles  bool
	ShowLegend      bool
	MaxWidth        int
	MaxDetailWidth  int
	TaskIDWidth     int
}

type statusDisplayRow struct {
	Entry          status.Entry
	StatusOrder    int
	Status         string
	TaskID         string
	TreeDepth      int
	Detail         string
	SemanticDetail status.Detail
	EpicProgress   status.Detail
	ShowDetail     bool
}

type statusTaskKey struct {
	RepoID string
	TaskID string
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
	return renderStatusWithSort(output, projection, full, options, taskViewSortStatus)
}

func renderStatusWithSort(
	output interface{ Write([]byte) (int, error) },
	projection status.Projection,
	full bool,
	options statusRenderOptions,
	sortMode taskViewSort,
) error {
	visibleGroups := visibleStatusGroups(projection.Groups, full)
	rows := statusDisplayRowsForSort(visibleGroups, sortMode)
	layout := statusLayoutFor(rows, options)
	return renderStatusRows(output, rows, layout)
}

func statusLayoutFor(rows []statusDisplayRow, options statusRenderOptions) statusRenderLayout {
	if options.NoTruncate || options.MaxWidth <= 0 {
		return statusRenderLayout{IncludeRepo: true, IncludePriority: true}
	}
	candidates := []statusRenderLayout{
		{IncludeRepo: true, IncludePriority: true, MaxWidth: options.MaxWidth},
		{IncludeRepo: true, IncludePriority: true, ShortDetail: true, MaxWidth: options.MaxWidth},
		{IncludePriority: true, ShortDetail: true, MaxWidth: options.MaxWidth},
		{ShortDetail: true, MaxWidth: options.MaxWidth},
	}
	for _, candidate := range candidates {
		if statusRowsFit(rows, candidate) {
			return candidate
		}
	}
	symbolPreferenceTarget := min(statusSymbolPreferredTitleWidth, maxStatusTitleWidth(rows))
	for _, candidate := range candidates[1:] {
		candidate.TruncateTitles = true
		candidate = alignResponsiveStatusColumn(rows, candidate)
		budget := statusTitleBudget(rows, candidate)
		if budget >= symbolPreferenceTarget {
			return candidate
		}
	}
	symbolLayout := statusRenderLayout{
		ShortDetail:    true,
		SymbolStatus:   true,
		TruncateTitles: true,
		ShowLegend:     true,
		MaxWidth:       options.MaxWidth,
	}
	symbolLayout = alignResponsiveStatusColumn(rows, symbolLayout)
	symbolLayout = capStatusDetailWidth(rows, symbolLayout, symbolPreferenceTarget)
	return symbolLayout
}

func statusRowsFit(rows []statusDisplayRow, layout statusRenderLayout) bool {
	headers, tableRows := statusEntryTable(rows, layout)
	if layout.TruncateTitles {
		tableRows = truncateStatusTitles(headers, tableRows, layout)
		return responsiveStatusTableWidth(headers, tableRows, layout) <= layout.MaxWidth
	}
	return tableWidth(headers, tableRows) <= layout.MaxWidth
}

func renderStatusRows(output io.Writer, statusRows []statusDisplayRow, layout statusRenderLayout) error {
	headers, rows := statusEntryTable(statusRows, layout)
	if layout.TruncateTitles {
		rows = truncateStatusTitles(headers, rows, layout)
		if err := renderResponsiveStatusTable(output, headers, rows, layout); err != nil {
			return err
		}
	} else if err := renderTable(output, headers, rows); err != nil {
		return err
	}
	if !layout.ShowLegend {
		return nil
	}
	for _, line := range statusSymbolLegendLines(layout.MaxWidth) {
		if _, err := io.WriteString(output, line+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func statusEntryTable(
	statusRows []statusDisplayRow,
	layout statusRenderLayout,
) ([]string, [][]string) {
	statusHeader := "STATUS"
	if layout.SymbolStatus {
		statusHeader = "S"
	}
	headers := []string{"TASK_ID", statusHeader}
	if layout.IncludePriority {
		headers = append(headers, "P")
	}
	headers = append(headers, "TITLE")
	if layout.IncludeRepo {
		headers = append(headers, "REPO")
	}
	includeDetail := statusRowsShowDetail(statusRows)
	if includeDetail {
		headers = append(headers, "DETAIL")
	}

	rows := make([][]string, 0, len(statusRows))
	for _, row := range statusRows {
		switch row.Entry.Kind {
		case status.EntryTask:
			rows = append(rows, statusTaskEntryTableRow(row, includeDetail, layout))
		case status.EntryRepoFailure:
			rows = append(rows, statusFailureEntryTableRow(row, includeDetail, layout))
		}
	}
	return headers, rows
}

func statusTaskEntryTableRow(
	entryRow statusDisplayRow,
	includeDetail bool,
	layout statusRenderLayout,
) []string {
	entry := entryRow.Entry
	row := make([]string, 0, 6)
	row = append(row, entryRow.TaskID, statusRowLabel(entryRow, layout.SymbolStatus))
	if layout.IncludePriority {
		row = append(row, strconv.Itoa(entry.Task.Priority))
	}
	row = append(row, statusDisplayTitleForLayout(entry.Task, layout))
	if layout.IncludeRepo {
		row = append(row, entry.Repository.Name)
	}
	if includeDetail {
		row = append(row, statusRenderedDetail(entryRow, layout))
	}
	return row
}

func statusFailureEntryTableRow(
	entryRow statusDisplayRow,
	includeDetail bool,
	layout statusRenderLayout,
) []string {
	entry := entryRow.Entry
	detail := entryRow.Detail
	if detail == "" && entry.Failure != nil {
		detail = entry.Failure.Error()
	}
	title := fmt.Sprintf("repo %s (prefix %s)", entry.Repository.ID, entry.Repository.TaskIDPrefix)

	row := make([]string, 0, 6)
	row = append(row, "-", statusRowLabel(entryRow, layout.SymbolStatus))
	if layout.IncludePriority {
		row = append(row, "-")
	}
	row = append(row, title)
	if layout.IncludeRepo {
		row = append(row, entry.Repository.Name)
	}
	if includeDetail {
		entryRow.Detail = detail
		row = append(row, statusRenderedDetail(entryRow, layout))
	}
	return row
}

func statusDisplayTitle(taskItem taskmodel.Task) string {
	if taskItem.IssueType != taskmodel.IssueTypeEpic {
		return taskItem.Title
	}
	return "◆ " + taskItem.Title
}

func statusDisplayTitleForLayout(taskItem taskmodel.Task, layout statusRenderLayout) string {
	if layout.TruncateTitles && taskItem.IssueType == taskmodel.IssueTypeEpic {
		return taskItem.Title
	}
	return statusDisplayTitle(taskItem)
}

func statusDisplayRows(visibleGroups []status.Group) []statusDisplayRow {
	return statusDisplayRowsForSort(visibleGroups, taskViewSortStatus)
}

func statusDisplayRowsForSort(visibleGroups []status.Group, sortMode taskViewSort) []statusDisplayRow {
	rows := make([]statusDisplayRow, 0)
	for statusOrder, group := range visibleGroups {
		for _, entry := range group.Entries {
			row := statusDisplayRow{
				Entry:          entry,
				StatusOrder:    statusOrder,
				Status:         statusDisplayLabel(group),
				Detail:         entry.Detail,
				SemanticDetail: entry.SemanticDetail,
				EpicProgress:   entry.EpicProgress,
				ShowDetail:     statusGroupShowsDetail(group.ID),
			}
			if entry.Kind == status.EntryTask {
				row.TaskID = entry.Task.ID
				if entry.Task.IssueType == taskmodel.IssueTypeEpic {
					row.ShowDetail = true
					if entry.Task.Status == taskmodel.StatusInProgress {
						row.Status = "Working"
					}
				}
			}
			rows = append(rows, row)
		}
	}
	sortStatusDisplayRows(rows, sortMode)
	return statusTreeRows(rows)
}

func sortStatusDisplayRows(rows []statusDisplayRow, sortMode taskViewSort) {
	sort.Slice(rows, func(i, j int) bool {
		return statusDisplayRowLess(rows[i], rows[j], sortMode)
	})
}

func statusDisplayRowLess(left statusDisplayRow, right statusDisplayRow, sortMode taskViewSort) bool {
	switch sortMode {
	case taskViewSortStatus:
		if left.StatusOrder != right.StatusOrder {
			return left.StatusOrder < right.StatusOrder
		}
		if comparison := compareStatusDisplayRowPriority(left, right); comparison != 0 {
			return comparison < 0
		}
		if comparison := compareDescendingTimestamps(left.Entry.Task.CreatedAt, right.Entry.Task.CreatedAt); comparison != 0 {
			return comparison < 0
		}
	case taskViewSortCreated:
		if comparison := compareDescendingTimestamps(left.Entry.Task.CreatedAt, right.Entry.Task.CreatedAt); comparison != 0 {
			return comparison < 0
		}
	case taskViewSortUpdated:
		if comparison := compareDescendingTimestamps(left.Entry.Task.UpdatedAt, right.Entry.Task.UpdatedAt); comparison != 0 {
			return comparison < 0
		}
	}
	return statusDisplayRowIdentityLess(left, right)
}

func compareStatusDisplayRowPriority(left statusDisplayRow, right statusDisplayRow) int {
	leftPriority, leftOK := statusDisplayRowPriority(left)
	rightPriority, rightOK := statusDisplayRowPriority(right)
	switch {
	case leftOK && !rightOK:
		return -1
	case !leftOK && rightOK:
		return 1
	case !leftOK && !rightOK:
		return 0
	case leftPriority < rightPriority:
		return -1
	case leftPriority > rightPriority:
		return 1
	default:
		return 0
	}
}

func statusDisplayRowPriority(row statusDisplayRow) (int, bool) {
	if row.Entry.Kind != status.EntryTask {
		return 0, false
	}
	return row.Entry.Task.Priority, true
}

func compareDescendingTimestamps(left *time.Time, right *time.Time) int {
	switch {
	case left == nil && right != nil:
		return 1
	case left != nil && right == nil:
		return -1
	case left == nil && right == nil:
		return 0
	case left.After(*right):
		return -1
	case right.After(*left):
		return 1
	default:
		return 0
	}
}

func statusDisplayRowIdentityLess(left statusDisplayRow, right statusDisplayRow) bool {
	if left.Entry.Repository.ID != right.Entry.Repository.ID {
		return left.Entry.Repository.ID < right.Entry.Repository.ID
	}
	if left.Entry.Task.ID != right.Entry.Task.ID {
		return left.Entry.Task.ID < right.Entry.Task.ID
	}
	if left.Entry.Kind != right.Entry.Kind {
		return left.Entry.Kind < right.Entry.Kind
	}
	if left.Entry.Source != right.Entry.Source {
		return left.Entry.Source < right.Entry.Source
	}
	if left.Entry.Operation != right.Entry.Operation {
		return left.Entry.Operation < right.Entry.Operation
	}
	return left.Entry.Detail < right.Entry.Detail
}

func statusDisplayLabel(group status.Group) string {
	if group.ID == status.GroupReadyToRun {
		return "Ready"
	}
	return group.Title
}

func statusRowsShowDetail(rows []statusDisplayRow) bool {
	for _, row := range rows {
		if row.ShowDetail {
			return true
		}
	}
	return false
}

func statusTreeRows(rows []statusDisplayRow) []statusDisplayRow {
	childrenByParent, hasVisibleParent := statusTreeChildIndex(rows, visibleEpicKeys(rows))

	ordered := make([]statusDisplayRow, 0, len(rows))
	rendered := make(map[int]struct{}, len(rows))
	var appendNode func(int, string, string, string, int)
	appendNode = func(index int, displayPrefix string, childPrefix string, marker string, depth int) {
		if _, ok := rendered[index]; ok {
			return
		}
		rendered[index] = struct{}{}

		row := rows[index]
		row.TreeDepth = depth
		if marker != "" && row.Entry.Kind == status.EntryTask {
			row.TaskID = displayPrefix + marker + row.Entry.Task.ID
		}
		ordered = append(ordered, row)

		key, ok := rowTreeKey(row)
		if !ok {
			return
		}
		children := unrenderedChildren(childrenByParent[key], rendered)
		for i, childIndex := range children {
			childMarker := "├─ "
			nextChildPrefix := childPrefix + "│ "
			if i == len(children)-1 {
				childMarker = "└─ "
				nextChildPrefix = childPrefix + "  "
			}
			appendNode(childIndex, childPrefix, nextChildPrefix, childMarker, depth+1)
		}
	}

	for i := range rows {
		if _, ok := hasVisibleParent[i]; ok {
			continue
		}
		appendNode(i, "", "", "", 0)
	}
	for i := range rows {
		appendNode(i, "", "", "", 0)
	}
	return ordered
}

func visibleEpicKeys(rows []statusDisplayRow) map[statusTaskKey]struct{} {
	visibleEpics := make(map[statusTaskKey]struct{})
	for _, row := range rows {
		if row.Entry.Kind == status.EntryTask && row.Entry.Task.IssueType == taskmodel.IssueTypeEpic {
			visibleEpics[statusKey(row.Entry.Repository.ID, row.Entry.Task.ID)] = struct{}{}
		}
	}
	return visibleEpics
}

func statusTreeChildIndex(
	rows []statusDisplayRow,
	visibleEpics map[statusTaskKey]struct{},
) (map[statusTaskKey][]int, map[int]struct{}) {
	childrenByParent := make(map[statusTaskKey][]int)
	hasVisibleParent := make(map[int]struct{})
	for i, row := range rows {
		if row.Entry.Kind != status.EntryTask {
			continue
		}
		parentID := strings.TrimSpace(row.Entry.Task.Relations.ParentID)
		if parentID == "" {
			continue
		}
		parentKey := statusKey(row.Entry.Repository.ID, parentID)
		if _, ok := visibleEpics[parentKey]; !ok {
			continue
		}
		childrenByParent[parentKey] = append(childrenByParent[parentKey], i)
		hasVisibleParent[i] = struct{}{}
	}
	return childrenByParent, hasVisibleParent
}

func rowTreeKey(row statusDisplayRow) (statusTaskKey, bool) {
	if row.Entry.Kind != status.EntryTask || row.Entry.Task.IssueType != taskmodel.IssueTypeEpic {
		return statusTaskKey{}, false
	}
	return statusKey(row.Entry.Repository.ID, row.Entry.Task.ID), true
}

func unrenderedChildren(children []int, rendered map[int]struct{}) []int {
	unrendered := make([]int, 0, len(children))
	for _, child := range children {
		if _, ok := rendered[child]; ok {
			continue
		}
		unrendered = append(unrendered, child)
	}
	return unrendered
}

func statusKey(repoID string, taskID string) statusTaskKey {
	return statusTaskKey{RepoID: repoID, TaskID: taskID}
}

func statusRowLabel(row statusDisplayRow, symbol bool) string {
	if symbol {
		return statusSymbol(row.Status)
	}
	return row.Status
}

func statusSymbol(label string) string {
	switch label {
	case "Needs attention":
		return "!"
	case "Reviewing":
		return "◉"
	case "Working":
		return "▶"
	case "Idle":
		return "‖"
	case "Ready":
		return "○"
	case "Blocked":
		return "×"
	case "Done / closed":
		return "✓"
	default:
		return "!"
	}
}

func statusRenderedDetail(row statusDisplayRow, layout statusRenderLayout) string {
	if !row.ShowDetail {
		return ""
	}
	detail := fullStatusDetail(row)
	if layout.ShortDetail {
		detail = compactStatusDetailForRow(row)
	}
	if layout.MaxDetailWidth > 0 {
		detail = truncateCell(detail, layout.MaxDetailWidth)
	}
	return detail
}

func fullStatusDetail(row statusDisplayRow) string {
	detail := strings.TrimSpace(row.Detail)
	progress := fullEpicProgressDetail(row.EpicProgress)
	if progress == "" {
		return detail
	}
	if detail == "" || detail == "-" || epicProgressIsPrimaryDetail(row) {
		return progress
	}
	return detail + "; " + progress
}

func compactStatusDetailForRow(row statusDisplayRow) string {
	if epicProgressIsPrimaryDetail(row) {
		return compactEpicProgressDetail(row.EpicProgress)
	}
	if row.SemanticDetail.Kind != status.DetailNone && row.SemanticDetail.Kind != status.DetailClosed {
		return compactStatusDetail(row.SemanticDetail, row.Detail)
	}
	progress := compactEpicProgressDetail(row.EpicProgress)
	if progress != "" {
		return progress
	}
	return compactStatusDetail(row.SemanticDetail, row.Detail)
}

// epicProgressIsPrimaryDetail reports whether an epic's child progress should
// replace its non-actionable workflow detail.
func epicProgressIsPrimaryDetail(row statusDisplayRow) bool {
	if compactEpicProgressDetail(row.EpicProgress) == "" {
		return false
	}
	switch row.SemanticDetail.Kind {
	case status.DetailNone, status.DetailClosed, status.DetailNoRun:
		return true
	default:
		return false
	}
}

func compactStatusDetail(detail status.Detail, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	switch detail.Kind {
	case status.DetailNone, status.DetailClosed:
		return ""
	case status.DetailPullRequest:
		return compactPullRequestDetail(detail.URL)
	case status.DetailEpicProgress:
		return compactEpicProgressDetail(detail)
	case status.DetailMissingExternalRef:
		return "missing ext ref"
	case status.DetailWrongPRTarget:
		return "wrong PR target"
	case status.DetailWrongLocalTarget:
		return "wrong local target"
	case status.DetailFinalizedButOpen:
		return "finalized but open"
	case status.DetailUnknownTaskStatus:
		return "status " + valueOrUnknown(detail.State)
	case status.DetailRepoFailure:
		return valueOrUnknown(detail.Source) + "/" + valueOrUnknown(detail.Operation) + " failed"
	}
	if compact, ok := compactReviewDetail(detail); ok {
		return compact
	}
	if compact, ok := compactRunDetail(detail); ok {
		return compact
	}
	if compact, ok := compactParentEpicDetail(detail); ok {
		return compact
	}
	if compact, ok := compactDependencyDetail(detail); ok {
		return compact
	}
	return fallback
}

func fullEpicProgressDetail(detail status.Detail) string {
	progress := compactEpicProgressDetail(detail)
	if progress == "" {
		return ""
	}
	return progress + " done"
}

func compactEpicProgressDetail(detail status.Detail) string {
	if detail.Kind != status.DetailEpicProgress {
		return ""
	}
	return fmt.Sprintf("%d/%d", detail.Completed, detail.Total)
}

func compactReviewDetail(detail status.Detail) (string, bool) {
	switch detail.Kind {
	case status.DetailLocalReview:
		return "local review", true
	case status.DetailReviewRunning:
		return "running", true
	case status.DetailReviewManualStep:
		return "manual " + valueOrUnknown(detail.Step), true
	case status.DetailReviewDecisionLost:
		return "decision lost", true
	case status.DetailReviewDecisionRequired:
		return "decision required", true
	case status.DetailReviewDecisionPaused:
		return "decision paused", true
	case status.DetailReviewFollowUpReady:
		return "follow-up ready", true
	case status.DetailReviewBudgetSpent:
		return "budget spent", true
	case status.DetailReviewFindings:
		return countLabel(detail.Count, "finding"), true
	case status.DetailReviewAborted:
		return "aborted", true
	case status.DetailReviewFailed:
		return "failed", true
	case status.DetailPrimaryReviewInterrupted:
		return "reviewer interrupted", true
	case status.DetailReviewPassed:
		return "passed", true
	case status.DetailReviewPublishFailed:
		return "publish failed", true
	case status.DetailReviewUnknownState:
		return "review " + valueOrUnknown(detail.State), true
	default:
		return "", false
	}
}

func compactRunDetail(detail status.Detail) (string, bool) {
	switch detail.Kind {
	case status.DetailNoRun:
		return "no run", true
	case status.DetailRunRunning:
		return runAttemptCompact(detail.Attempt), true
	case status.DetailRunFailed:
		return runAttemptCompact(detail.Attempt) + " failed", true
	case status.DetailRunInterrupted:
		return runAttemptCompact(detail.Attempt) + " interrupted", true
	case status.DetailRunIncomplete:
		return runAttemptCompact(detail.Attempt) + " incomplete", true
	case status.DetailRunUnknownState:
		return runAttemptCompact(detail.Attempt) + " " + valueOrUnknown(detail.State), true
	case status.DetailOpenTaskRunHistory:
		return "open; " + runAttemptCompact(detail.Attempt), true
	default:
		return "", false
	}
}

func compactParentEpicDetail(detail status.Detail) (string, bool) {
	switch detail.Kind {
	case status.DetailParentMissing:
		return "parent " + valueOrUnknown(detail.ID) + " missing", true
	case status.DetailParentNotEpic:
		return "parent " + valueOrUnknown(detail.ID) + " not epic", true
	case status.DetailParentNotReady:
		return "parent " + valueOrUnknown(detail.ID) + " " + valueOrUnknown(detail.State), true
	default:
		return "", false
	}
}

func compactDependencyDetail(detail status.Detail) (string, bool) {
	switch detail.Kind {
	case status.DetailMissingDependency:
		return "missing " + valueOrUnknown(detail.ID), true
	case status.DetailMissingDependencies:
		return fmt.Sprintf("%d deps missing", normalizedCount(detail.Count, len(detail.IDs))), true
	case status.DetailDependencyDetailsMissing:
		return fmt.Sprintf("%d blockers unknown", detail.Count), true
	case status.DetailBlockedDependency:
		return "blocked " + valueOrUnknown(detail.ID), true
	case status.DetailBlockedDependencies:
		return fmt.Sprintf("blocked by %d deps", normalizedCount(detail.Count, len(detail.IDs))), true
	default:
		return "", false
	}
}

func compactPullRequestDetail(detailURL string) string {
	detailURL = strings.TrimSpace(detailURL)
	if detailURL == "" {
		return "PR"
	}
	parsed, err := url.Parse(detailURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "PR"
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "pull" && parts[i+1] != "" {
			return "PR #" + parts[i+1]
		}
	}
	return "PR"
}

func runAttemptCompact(attempt int) string {
	if attempt <= 0 {
		return "run"
	}
	return fmt.Sprintf("run #%d", attempt)
}

func countLabel(count int, singular string) string {
	if count == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %ss", count, singular)
}

func normalizedCount(count int, fallback int) int {
	if count > 0 {
		return count
	}
	return fallback
}

func valueOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func maxStatusTitleWidth(rows []statusDisplayRow) int {
	maxWidth := 1
	for _, row := range rows {
		if row.Entry.Kind != status.EntryTask {
			continue
		}
		maxWidth = max(maxWidth, displayWidth(statusDisplayTitle(row.Entry.Task)))
	}
	return maxWidth
}

func capStatusDetailWidth(
	rows []statusDisplayRow,
	layout statusRenderLayout,
	titleTarget int,
) statusRenderLayout {
	detailWidth := statusDetailColumnWidth(rows, layout)
	if detailWidth <= 0 {
		return layout
	}
	currentBudget := statusTitleBudget(rows, layout)
	if currentBudget >= titleTarget {
		return layout
	}

	detailIndex := statusColumnIndex(rows, layout, "DETAIL")
	if detailIndex < 0 {
		return layout
	}
	headers, tableRows := statusEntryTable(rows, layout)
	widths := tableColumnWidths(headers, tableRows)
	titleIndex := statusHeaderIndex(headers, "TITLE")
	maxDetailWidth := detailWidth
	for i, row := range rows {
		if !statusTitleBudgetRowEligible(row) || i >= len(tableRows) {
			continue
		}
		fixedWithoutTitleAndDetail := statusTablePaddingWidth(len(headers))
		for column, width := range widths {
			if column == titleIndex || column == detailIndex {
				continue
			}
			width = responsiveStatusColumnWidth(tableRows[i], column, width, layout)
			fixedWithoutTitleAndDetail += width
		}
		allowed := layout.MaxWidth - fixedWithoutTitleAndDetail - titleTarget
		maxDetailWidth = min(maxDetailWidth, allowed)
	}
	if maxDetailWidth < displayWidth(headers[detailIndex]) {
		maxDetailWidth = displayWidth(headers[detailIndex])
	}
	if maxDetailWidth < detailWidth {
		layout.MaxDetailWidth = maxDetailWidth
	}
	return layout
}

func statusDetailColumnWidth(rows []statusDisplayRow, layout statusRenderLayout) int {
	detailIndex := statusColumnIndex(rows, layout, "DETAIL")
	if detailIndex < 0 {
		return 0
	}
	headers, tableRows := statusEntryTable(rows, layout)
	widths := tableColumnWidths(headers, tableRows)
	if detailIndex >= len(widths) {
		return 0
	}
	return widths[detailIndex]
}

func alignResponsiveStatusColumn(rows []statusDisplayRow, layout statusRenderLayout) statusRenderLayout {
	if !layout.TruncateTitles {
		return layout
	}
	taskIDWidth := displayWidth("TASK_ID")
	for _, row := range rows {
		if !statusTitleBudgetRowEligible(row) {
			continue
		}
		taskIDWidth = max(taskIDWidth, displayWidth(sanitizeTableCell(row.TaskID)))
	}
	layout.TaskIDWidth = taskIDWidth
	return layout
}

func statusTitleBudget(rows []statusDisplayRow, layout statusRenderLayout) int {
	if layout.MaxWidth <= 0 {
		return maxStatusTitleWidth(rows)
	}
	headers, tableRows := statusEntryTable(rows, layout)
	widths := tableColumnWidths(headers, tableRows)
	titleIndex := statusHeaderIndex(headers, "TITLE")
	if titleIndex < 0 {
		return 0
	}
	budget := 0
	foundEligible := false
	for i, row := range rows {
		if !statusTitleBudgetRowEligible(row) || i >= len(tableRows) {
			continue
		}
		rowBudget := statusRowTitleBudget(widths, tableRows[i], titleIndex, layout)
		if !foundEligible || rowBudget < budget {
			budget = rowBudget
		}
		foundEligible = true
	}
	if foundEligible {
		return budget
	}
	return statusHeaderTitleBudget(headers, widths, titleIndex, layout)
}

func statusColumnIndex(rows []statusDisplayRow, layout statusRenderLayout, name string) int {
	headers, _ := statusEntryTable(rows, layout)
	for i, header := range headers {
		if header == name {
			return i
		}
	}
	return -1
}

func statusSymbolLegendLines(maxWidth int) []string {
	items := []string{
		"! needs attention",
		"◉ reviewing",
		"▶ working",
		"‖ idle",
		"○ ready",
		"× blocked",
		"✓ done",
	}
	if maxWidth <= 0 {
		return []string{"Legend: " + strings.Join(items, "  ")}
	}

	lines := make([]string, 0, 3)
	line := truncateCell("Legend:", maxWidth)
	for _, item := range items {
		item = truncateCell(item, maxWidth)
		candidate := line + "  " + item
		if displayWidth(candidate) <= maxWidth {
			line = candidate
			continue
		}
		lines = append(lines, line)
		line = item
	}
	lines = append(lines, line)
	return lines
}

func truncateStatusTitles(headers []string, rows [][]string, layout statusRenderLayout) [][]string {
	titleIndex := statusHeaderIndex(headers, "TITLE")
	if titleIndex < 0 {
		return rows
	}
	widths := tableColumnWidths(headers, rows)

	truncated := make([][]string, 0, len(rows))
	for _, row := range rows {
		next := append([]string(nil), row...)
		if titleIndex < len(next) {
			titleWidth := statusRowTitleBudget(widths, next, titleIndex, layout)
			if titleWidth < 1 {
				titleWidth = 1
			}
			next[titleIndex] = truncateCell(next[titleIndex], titleWidth)
		}
		truncated = append(truncated, next)
	}
	return truncated
}

func renderResponsiveStatusTable(output io.Writer, headers []string, rows [][]string, layout statusRenderLayout) error {
	titleIndex := statusHeaderIndex(headers, "TITLE")
	if titleIndex < 0 || layout.MaxWidth <= 0 {
		return renderTable(output, headers, rows)
	}
	widths := tableColumnWidths(headers, rows)
	if err := renderResponsiveStatusTableRow(output, headers, widths, titleIndex, layout); err != nil {
		return err
	}
	for _, row := range rows {
		if err := renderResponsiveStatusTableRow(output, row, widths, titleIndex, layout); err != nil {
			return err
		}
	}
	return nil
}

func responsiveStatusTableWidth(headers []string, rows [][]string, layout statusRenderLayout) int {
	titleIndex := statusHeaderIndex(headers, "TITLE")
	if titleIndex < 0 || layout.MaxWidth <= 0 {
		return tableWidth(headers, rows)
	}
	widths := tableColumnWidths(headers, rows)
	renderedWidth := responsiveStatusTableRowWidth(headers, widths, titleIndex, layout)
	for _, row := range rows {
		renderedWidth = max(
			renderedWidth,
			responsiveStatusTableRowWidth(row, widths, titleIndex, layout),
		)
	}
	return renderedWidth
}

func responsiveStatusTableRowWidth(cells []string, widths []int, titleIndex int, layout statusRenderLayout) int {
	titleWidth := statusRowTitleBudget(widths, cells, titleIndex, layout)
	if titleWidth < 1 {
		titleWidth = 1
	}
	total := statusTablePaddingWidth(len(widths))
	for i, width := range widths {
		width = responsiveStatusColumnWidth(cells, i, width, layout)
		if i == titleIndex {
			cell := ""
			if i < len(cells) {
				cell = sanitizeTableCell(cells[i])
			}
			width = min(displayWidth(cell), titleWidth)
			if i < len(widths)-1 {
				width = titleWidth
			}
		}
		total += width
	}
	return total
}

func renderResponsiveStatusTableRow(
	output io.Writer,
	cells []string,
	widths []int,
	titleIndex int,
	layout statusRenderLayout,
) error {
	sanitized := make([]string, 0, len(cells))
	for _, cell := range cells {
		sanitized = append(sanitized, sanitizeTableCell(cell))
	}
	titleWidth := statusRowTitleBudget(widths, sanitized, titleIndex, layout)
	if titleWidth < 1 {
		titleWidth = 1
	}
	for i, width := range widths {
		cell := ""
		if i < len(sanitized) {
			cell = sanitized[i]
		}
		cellWidth := responsiveStatusColumnWidth(sanitized, i, width, layout)
		if i == titleIndex {
			cellWidth = titleWidth
			cell = truncateCell(cell, titleWidth)
		}
		if _, err := io.WriteString(output, cell); err != nil {
			return err
		}
		if i == len(widths)-1 {
			continue
		}
		padding := cellWidth - displayWidth(cell) + 2
		if padding < 2 {
			padding = 2
		}
		if _, err := io.WriteString(output, strings.Repeat(" ", padding)); err != nil {
			return err
		}
	}
	_, err := io.WriteString(output, "\n")
	return err
}

func responsiveStatusColumnWidth(cells []string, column int, width int, layout statusRenderLayout) int {
	if column != 0 || len(cells) == 0 {
		return width
	}
	cellWidth := displayWidth(sanitizeTableCell(cells[0]))
	if layout.TaskIDWidth > 0 {
		return max(cellWidth, layout.TaskIDWidth)
	}
	return cellWidth
}

func statusRowTitleBudget(
	widths []int,
	row []string,
	titleIndex int,
	layout statusRenderLayout,
) int {
	fixedWidth := statusTablePaddingWidth(len(widths))
	for i, width := range widths {
		if i == titleIndex {
			continue
		}
		width = responsiveStatusColumnWidth(row, i, width, layout)
		fixedWidth += width
	}
	return layout.MaxWidth - fixedWidth
}

func statusHeaderTitleBudget(headers []string, widths []int, titleIndex int, layout statusRenderLayout) int {
	return statusRowTitleBudget(widths, headers, titleIndex, layout)
}

func statusTitleBudgetRowEligible(row statusDisplayRow) bool {
	return row.Entry.Kind != status.EntryTask || row.TreeDepth <= 1
}

func statusHeaderIndex(headers []string, name string) int {
	for i, header := range headers {
		if header == name {
			return i
		}
	}
	return -1
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
	if width <= 0 {
		return ""
	}
	if displayWidth(value) <= width {
		return value
	}
	if width <= 3 {
		return runewidth.Truncate(value, width, "")
	}
	return runewidth.Truncate(value, width, "...")
}

func displayWidth(value string) int {
	return runewidth.StringWidth(value)
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
	visible := make([]status.Group, 0, len(groups))
	for _, group := range groups {
		if statusGroupHiddenWhenEmpty(group) {
			continue
		}
		if !full && statusGroupHiddenByDefault(group.ID) {
			continue
		}
		visible = append(visible, group)
	}
	return visible
}

func statusGroupHiddenWhenEmpty(group status.Group) bool {
	return group.ID == status.GroupNeedsAttention && len(group.Entries) == 0
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
