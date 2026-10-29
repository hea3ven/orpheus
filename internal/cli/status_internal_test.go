package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/status"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
)

func TestTaskRunStateIndexLoadsOnlyPersistedProjectionCandidates(t *testing.T) {
	const repoID = "alpha"

	snapshot, store := taskStateLoadingFixture()

	index, failures := taskRunStateIndexForStore(state.Paths{}, store, snapshot)

	if got, want := store.taskIDCalls, []string{repoID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("state inventory calls = %#v, want %#v", got, want)
	}
	if got, want := store.loadCalls, []string{
		stateReaderKey(repoID, "open-history"),
		stateReaderKey(repoID, "corrupt-in-progress"),
		stateReaderKey(repoID, "completion-candidate"),
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("state load calls = %#v, want %#v", got, want)
	}
	if len(failures) != 1 || failures[0].Source != "task_state" || failures[0].Operation != "load" ||
		!strings.Contains(failures[0].Err.Error(), "invalid YAML") {
		t.Fatalf("failures = %#v, want one relevant task-state load failure", failures)
	}
	if len(index) != 2 {
		t.Fatalf("local state index = %#v, want two loaded states", index)
	}
}

func taskStateLoadingFixture() (task.SnapshotResult, *countingTaskStateReader) {
	const repoID = "alpha"

	tasks := make([]task.Task, 0, 108)
	persistedTaskIDs := make([]string, 0, 108)
	for i := range 100 {
		id := fmt.Sprintf("closed-%03d", i)
		tasks = append(tasks, task.Task{ID: id, Status: task.StatusClosed})
		persistedTaskIDs = append(persistedTaskIDs, id)
	}
	tasks = append(tasks,
		task.Task{ID: "has-pr", Status: task.StatusOpen, Metadata: task.Metadata{task.MetadataPRURL: "https://example.test/pr/1"}},
		task.Task{ID: "open-dependency", Status: task.StatusOpen},
		task.Task{ID: "snapshot-blocked", Status: task.StatusOpen, Relations: task.RelationSummary{DependencyIDs: []string{"open-dependency"}}},
		task.Task{ID: "no-persisted-state", Status: task.StatusOpen},
		task.Task{ID: "open-history", Status: task.StatusOpen},
		task.Task{ID: "corrupt-in-progress", Status: task.StatusInProgress},
		task.Task{
			ID:       "completion-candidate",
			Status:   task.StatusOpen,
			Metadata: task.Metadata{task.MetadataBranch: "main", task.MetadataWorktree: "/repo/alpha"},
		},
	)
	persistedTaskIDs = append(persistedTaskIDs,
		"has-pr", "snapshot-blocked", "open-history", "corrupt-in-progress", "completion-candidate",
	)

	store := &countingTaskStateReader{
		taskIDs: map[string][]string{repoID: persistedTaskIDs},
		states: map[string]taskstate.TaskState{
			stateReaderKey(repoID, "open-history"): {
				Runs: []taskstate.RunAttempt{{Attempt: 1, Status: taskstate.RunStatusFailed}},
			},
			stateReaderKey(repoID, "completion-candidate"): {
				Runs: []taskstate.RunAttempt{{Attempt: 1, Status: taskstate.RunStatusSucceeded}},
			},
		},
		loadErrors: map[string]error{
			stateReaderKey(repoID, "corrupt-in-progress"): errors.New("invalid YAML"),
			stateReaderKey(repoID, "closed-000"):          errors.New("irrelevant closed state"),
			stateReaderKey(repoID, "has-pr"):              errors.New("irrelevant PR state"),
			stateReaderKey(repoID, "snapshot-blocked"):    errors.New("irrelevant blocked state"),
		},
	}
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: repoID, Path: "/repo/alpha", DefaultBranch: "main"},
		Tasks:      tasks,
	}}}
	return snapshot, store
}

func TestTaskRunStateIndexIgnoresIrrelevantInventoryFailure(t *testing.T) {
	store := &countingTaskStateReader{
		taskIDErrors: map[string]error{"alpha": errors.New("read task-state directory")},
	}
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha"},
		Tasks: []task.Task{
			{ID: "closed", Status: task.StatusClosed},
			{ID: "has-pr", Status: task.StatusOpen, Metadata: task.Metadata{task.MetadataPRURL: "https://example.test/pr/1"}},
		},
	}}}

	index, failures := taskRunStateIndexForStore(state.Paths{}, store, snapshot)

	if got, want := store.taskIDCalls, []string{"alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("state inventory calls = %#v, want %#v", got, want)
	}
	if len(store.loadCalls) != 0 {
		t.Fatalf("state load calls = %#v, want none", store.loadCalls)
	}
	if len(index) != 0 || len(failures) != 0 {
		t.Fatalf("index = %#v, failures = %#v, want no local state result", index, failures)
	}
}

type countingTaskStateReader struct {
	taskIDs      map[string][]string
	taskIDErrors map[string]error
	taskIDCalls  []string
	states       map[string]taskstate.TaskState
	loadErrors   map[string]error
	loadCalls    []string
}

func (s *countingTaskStateReader) TaskIDs(repoID string) ([]string, error) {
	s.taskIDCalls = append(s.taskIDCalls, repoID)
	if err := s.taskIDErrors[repoID]; err != nil {
		return nil, err
	}
	return s.taskIDs[repoID], nil
}

func (s *countingTaskStateReader) Load(repoID string, taskID string) (taskstate.TaskState, error) {
	key := stateReaderKey(repoID, taskID)
	s.loadCalls = append(s.loadCalls, key)
	if err := s.loadErrors[key]; err != nil {
		return taskstate.TaskState{}, err
	}
	return s.states[key], nil
}

func stateReaderKey(repoID string, taskID string) string {
	return repoID + "/" + taskID
}

func TestStatusRenderOptionsChooseAvailableWidthUnlessUnbounded(t *testing.T) {
	for _, test := range []struct {
		name            string
		outputWidth     int
		outputAvailable bool
		watchAvailable  bool
		noTruncate      bool
		want            statusRenderOptions
	}{
		{name: "terminal takes precedence", outputWidth: 100, outputAvailable: true, watchAvailable: true, want: statusRenderOptions{MaxWidth: 100}},
		{name: "redirected watch uses pane width", watchAvailable: true, want: statusRenderOptions{MaxWidth: 72}},
		{name: "no available width remains unbounded", want: statusRenderOptions{}},
		{name: "explicit unbounded skips detection", noTruncate: true, want: statusRenderOptions{NoTruncate: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			detectCalls := 0
			got := statusRenderOptionsForOutput(io.Discard, test.noTruncate, statusWidthDetector{
				OutputWidth: func(io.Writer) (int, bool) { detectCalls++; return test.outputWidth, test.outputAvailable },
				WatchWidth:  func() (int, bool) { detectCalls++; return 72, test.watchAvailable },
			})
			if got != test.want {
				t.Fatalf("render options = %#v, want %#v", got, test.want)
			}
			if test.noTruncate && detectCalls != 0 {
				t.Fatal("unbounded output queried terminal width")
			}
		})
	}
}

func TestRenderStatusEmptyProjectionRendersIntegratedTableOnly(t *testing.T) {
	projection := status.Projection{Groups: []status.Group{
		{ID: status.GroupNeedsAttention, Title: "Needs attention"},
		{ID: status.GroupInReview, Title: "Reviewing"},
		{ID: status.GroupWorking, Title: "Working"},
		{ID: status.GroupIdle, Title: "Idle"},
		{ID: status.GroupReadyToRun, Title: "Ready to run"},
		{ID: status.GroupBlocked, Title: "Blocked"},
		{ID: status.GroupDoneClosed, Title: "Done / closed"},
	}}

	var output bytes.Buffer
	err := renderStatus(&output, projection, false, statusRenderOptions{})
	if err != nil {
		t.Fatalf("render status: %v", err)
	}

	normal := output.String()
	if normal != "TASK_ID  STATUS  P  TITLE  REPO\n" {
		t.Fatalf("normal output = %q, want integrated header only", normal)
	}

	output.Reset()
	err = renderStatus(&output, projection, true, statusRenderOptions{})
	if err != nil {
		t.Fatalf("render full status: %v", err)
	}

	full := output.String()
	if full != "TASK_ID  STATUS  P  TITLE  REPO\n" {
		t.Fatalf("full output = %q, want integrated header only", full)
	}
}

func TestRenderStatusAdaptsColumnsAndDetailsToAvailableWidth(t *testing.T) {
	for _, test := range []struct {
		name         string
		width        int
		id           string
		detail       status.Detail
		fullDetail   string
		wantDetail   string
		hidePriority bool
	}{
		{name: "compact PR and truncated title", width: 62, id: "op-123456", detail: status.Detail{Kind: status.DetailPullRequest, URL: "https://github.test/org/alpha/pull/123456"}, fullDetail: "https://github.test/org/alpha/pull/123456", wantDetail: "PR #123456"},
		{name: "very narrow pane sacrifices priority", width: 44, id: "op-123456789", detail: status.Detail{Kind: status.DetailLocalReview}, fullDetail: "local review; run task run", wantDetail: "loc", hidePriority: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection := status.Projection{Groups: []status.Group{{ID: status.GroupInReview, Title: "Reviewing", Entries: []status.Entry{{
				Kind:       status.EntryTask,
				Repository: task.Repository{ID: "alpha", Name: "Very Long Repository Name", TaskIDPrefix: "op"},
				Task:       task.Task{ID: test.id, Priority: 2, Title: "Implement an extremely long operator status title that cannot fit"},
				Detail:     test.fullDetail, SemanticDetail: test.detail,
			}}}, {ID: status.GroupReadyToRun, Title: "Ready to run", Entries: []status.Entry{{
				Kind: status.EntryTask, Repository: task.Repository{ID: "beta", Name: "Short Repo", TaskIDPrefix: "op"},
				Task: task.Task{ID: "op-ready", Priority: 1, Title: "Ready short"},
			}}}}}
			got := renderProjectedStatusForTest(t, projection, test.width)
			for _, want := range []string{"TASK_ID", "TITLE", "DETAIL", test.id, test.wantDetail, "◉", "op-ready", "..."} {
				if !strings.Contains(got, want) {
					t.Fatalf("output missing %q:\n%s", want, got)
				}
			}
			for _, unwanted := range []string{"REPO", "Very Long Repository Name", "Short Repo", test.fullDetail} {
				if strings.Contains(got, unwanted) {
					t.Fatalf("narrow output retained %q:\n%s", unwanted, got)
				}
			}
			header := strings.SplitN(got, "\n", 2)[0]
			if test.hidePriority && strings.Contains(" "+header+" ", " P ") {
				t.Fatalf("narrow output kept priority:\n%s", got)
			}
			assertStatusLinesWithinWidth(t, got, test.width)
		})
	}
}

func TestRenderStatusNarrowSymbolModePreservesTreeTitleContext(t *testing.T) {
	projection := narrowTreeStatusProjection()

	tests := []struct {
		name        string
		width       int
		symbolMode  bool
		rootPrefix  string
		childPrefix string
	}{
		{
			name:        "forty columns",
			width:       40,
			symbolMode:  true,
			rootPrefix:  "Root title co",
			childPrefix: "Child title c",
		},
		{
			name:        "forty eight columns",
			width:       48,
			symbolMode:  true,
			rootPrefix:  "Root title cont",
			childPrefix: "Child title con",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			err := renderStatus(&output, projection, false, statusRenderOptions{MaxWidth: tt.width})
			if err != nil {
				t.Fatalf("render status: %v", err)
			}

			assertNarrowTreeStatusOutput(t, output.String(), tt.width, tt.symbolMode, tt.rootPrefix, tt.childPrefix)
		})
	}
}

func TestRenderStatusNarrowPanePreservesReadableTreeTitles(t *testing.T) {
	for _, test := range []struct {
		width   int
		symbols bool
	}{{55, true}, {68, true}, {69, true}, {80, false}} {
		t.Run(fmt.Sprintf("width %d", test.width), func(t *testing.T) {
			got := renderProjectedStatusForTest(t, crowdedTreeStatusProjection(), test.width)
			if test.symbols {
				assertCrowdedTreeSymbolOutput(t, got)
			} else {
				assertCrowdedTreeDescriptiveOutput(t, got)
			}
			assertStatusColumnAligned(t, got)
			assertCrowdedTreeTitleContext(t, got)
			assertStatusLinesWithinWidth(t, got, test.width)
		})
	}
}

func TestRenderStatusEpicRowsPreserveActionsAndUseProgressAsFallback(t *testing.T) {
	projection := epicActionStatusProjection()
	projection.Groups = append(projection.Groups, status.Group{ID: status.GroupReadyToRun, Title: "Ready to run", Entries: []status.Entry{{
		Kind: status.EntryTask, Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "op"},
		Task: epicActionTask("op-epic", "Ready epic progress"), Detail: "-",
		EpicProgress: status.Detail{Kind: status.DetailEpicProgress, Completed: 7, Total: 11},
	}}})
	for _, width := range []int{0, 80, 64} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			got := renderProjectedStatusForTest(t, projection, width)
			for _, row := range []struct{ id, fullAction, shortAction, progress string }{
				{"op-pr", "https://github.test/org/alpha/pull/44", "PR #44", "1/3"},
				{"op-review", "review failed operationally; run task run", "failed", "2/5"},
				{"op-run", "run attempt 2 failed", "run #2", "3/8"},
				{"op-blocked", "blocked by op-dep", "blocked", "4/9"},
				{"op-epic", "7/11 done", "7/11", "7/11"},
			} {
				line := renderedStatusTaskLine(t, got, row.id)
				action := row.shortAction
				if width == 0 {
					action = row.fullAction
				}
				if !strings.Contains(line, action) {
					t.Errorf("%s lost action %q: %q\n%s", row.id, action, line, got)
				}
				if (width == 0 || row.id == "op-epic") && !strings.Contains(line, row.progress) {
					t.Errorf("%s lost progress %q: %q\n%s", row.id, row.progress, line, got)
				}
			}
			if width > 0 {
				assertStatusLinesWithinWidth(t, got, width)
			}
		})
	}
}

func renderedStatusTaskLine(t *testing.T, output, taskID string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == taskID {
			return line
		}
	}
	t.Fatalf("task %s is missing from rendered status:\n%s", taskID, output)
	return ""
}

func TestRenderStatusProjectedInProgressEpicUsesProgressInsteadOfNoRun(t *testing.T) {
	epic := projectedStatusTask("op-epic", task.StatusInProgress)
	epic.IssueType = task.IssueTypeEpic
	epic.Relations.ChildCount = 32
	tasks := []task.Task{epic}
	for childNumber := 1; childNumber <= 27; childNumber++ {
		child := projectedStatusTask(fmt.Sprintf("op-epic.%d", childNumber), task.StatusClosed)
		child.Relations.ParentID = epic.ID
		tasks = append(tasks, child)
	}

	projection := status.Project(projectedStatusSnapshot(tasks...))
	entry := projectedStatusEntry(t, projection, status.GroupIdle, epic.ID)
	assertProjectedStatusDetail(t, entry.SemanticDetail, status.Detail{Kind: status.DetailNoRun})
	assertProjectedStatusDetail(t, entry.EpicProgress, status.Detail{
		Kind: status.DetailEpicProgress, Completed: 27, Total: 32,
	})

	tests := []struct {
		name    string
		options statusRenderOptions
		want    string
	}{
		{name: "compact", options: statusRenderOptions{MaxWidth: 88}, want: "27/32"},
		{name: "wide", want: "27/32 done"},
		{name: "no truncate", options: statusRenderOptions{MaxWidth: 48, NoTruncate: true}, want: "27/32 done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := renderStatus(&output, projection, true, tt.options); err != nil {
				t.Fatalf("render status: %v", err)
			}

			got := output.String()
			if !strings.Contains(got, tt.want) {
				t.Fatalf("output missing %q:\n%s", tt.want, got)
			}
			if strings.Contains(got, "no attached run recorded") {
				t.Fatalf("output retained no-run detail:\n%s", got)
			}
			if tt.name == "compact" && strings.Contains(got, "27/32 done") {
				t.Fatalf("compact output retained full progress wording:\n%s", got)
			}
		})
	}
}

func TestRenderStatusProjectionSemanticDetailsAcrossBoundary(t *testing.T) {
	tests := []projectedStatusDetailCase{
		projectedPRDetailCase(),
		projectedEpicProgressDetailCase(),
		projectedRunDetailCase("run running", "op-run", task.StatusInProgress, taskstate.RunAttempt{Attempt: 2, Status: taskstate.RunStatusRunning}, status.GroupWorking, status.Detail{Kind: status.DetailRunRunning, Attempt: 2}, "run attempt 2 is running", "run #2"),
		projectedRunDetailCase("run failed", "op-run-failed", task.StatusInProgress, taskstate.RunAttempt{Attempt: 3, Status: taskstate.RunStatusFailed}, status.GroupNeedsAttention, status.Detail{Kind: status.DetailRunFailed, Attempt: 3}, "run attempt 3 failed", "run #3 failed"),
		projectedRunDetailCase("run incomplete", "op-run-incomplete", task.StatusInProgress, taskstate.RunAttempt{Attempt: 4, Status: taskstate.RunStatusSucceeded}, status.GroupIdle, status.Detail{Kind: status.DetailRunIncomplete, Attempt: 4}, "run attempt 4 succeeded; agent exited without completion", "run #4 incomplete"),
		projectedNoRunDetailCase(),
		projectedRunDetailCase("open run history", "op-open-history", task.StatusOpen, taskstate.RunAttempt{Attempt: 1, Status: taskstate.RunStatusFailed}, status.GroupNeedsAttention, status.Detail{Kind: status.DetailOpenTaskRunHistory, Attempt: 1}, "backend status is open but local run attempt 1 failed", "open; run #1"),
		projectedRunDetailCase("unknown run state", "op-run-unknown", task.StatusInProgress, taskstate.RunAttempt{Attempt: 5, Status: taskstate.RunStatus("lost")}, status.GroupNeedsAttention, status.Detail{Kind: status.DetailRunUnknownState, Attempt: 5, State: "lost"}, "run attempt 5 has status lost", "run #5 lost"),
		projectedLocalReviewDetailCase("local review", nil, status.GroupInReview, status.Detail{Kind: status.DetailLocalReview}, "local review; run task run", "local review"),
		projectedLocalReviewDetailCase("review running", reviewAttemptForStatus(taskstate.ReviewStatusRunning), status.GroupInReview, status.Detail{Kind: status.DetailReviewRunning}, "review running", "running"),
		projectedLocalReviewDetailCase("manual review step", reviewAttemptForStatus(taskstate.ReviewStatusWaitingForManual), status.GroupInReview, status.Detail{Kind: status.DetailReviewManualStep, Step: "inspect"}, "waiting for manual step inspect", "manual inspect"),
		projectedLocalReviewDetailCase("review decision lost", reviewAttemptWithDecisionLost(), status.GroupInReview, status.Detail{Kind: status.DetailReviewDecisionLost}, "review blocker decision interrupted", "decision lost"),
		projectedLocalReviewDetailCase("review decision required", reviewAttemptWithDecisionRequired(), status.GroupInReview, status.Detail{Kind: status.DetailReviewDecisionRequired}, "review blocker decision required", "decision required"),
		projectedLocalReviewDetailCase("review follow-up ready", reviewAttemptWithTargetedBlocker(), status.GroupInReview, status.Detail{Kind: status.DetailReviewFollowUpReady}, "review blockers targeted", "follow-up ready"),
		projectedLocalReviewDetailCase("review budget spent", reviewAttemptWithBudgetSpent(), status.GroupIdle, status.Detail{Kind: status.DetailReviewBudgetSpent, Count: 1}, "review blocked after autonomous attempt budget", "budget spent"),
		projectedLocalReviewDetailCase("review findings", reviewAttemptWithFindings(3), status.GroupIdle, status.Detail{Kind: status.DetailReviewFindings, Count: 3}, "review blocked by 3 finding(s); run task run", "3 findings"),
		projectedLocalReviewDetailCase("review aborted", reviewAttemptForStatus(taskstate.ReviewStatusAborted), status.GroupInReview, status.Detail{Kind: status.DetailReviewAborted}, "review aborted; run task run", "aborted"),
		projectedLocalReviewDetailCase("review failed", reviewAttemptForStatus(taskstate.ReviewStatusFailed), status.GroupNeedsAttention, status.Detail{Kind: status.DetailReviewFailed}, "review failed operationally; run task run", "failed"),
		projectedLocalReviewDetailCase("review passed", reviewAttemptForStatus(taskstate.ReviewStatusPassed), status.GroupInReview, status.Detail{Kind: status.DetailReviewPassed}, "review passed; run task run", "passed"),
		projectedLocalReviewDetailCase("unknown review state", reviewAttemptForStatus(taskstate.ReviewStatus("stalled")), status.GroupNeedsAttention, status.Detail{Kind: status.DetailReviewUnknownState, Attempt: 7, State: "stalled"}, "review attempt 7 has status stalled", "review stalled"),
		projectedParentMissingDetailCase(),
		projectedParentNotEpicDetailCase(),
		projectedParentNotReadyDetailCase(),
		projectedMissingExternalRefDetailCase(),
		projectedWrongTargetDetailCase("wrong PR target", "op-wrong-pr", "orpheus/op-wrong-pr", "/fixture/orpheus/worktrees/op-wrong-pr", status.Detail{Kind: status.DetailWrongPRTarget}, "completion target is not the deterministic Orpheus worktree/team target", "wrong PR target"),
		projectedWrongTargetDetailCase("wrong local target", "op-wrong-local", "main", "/fixture/alpha", status.Detail{Kind: status.DetailWrongLocalTarget}, "completion target is not the deterministic Orpheus main/solo target", "wrong local target"),
		projectedFinalizedButOpenDetailCase(),
		projectedMissingDependencyDetailCase(),
		projectedMissingDependenciesDetailCase(),
		projectedDependencyDetailsMissingCase(),
		projectedBlockedDependencyDetailCase(),
		projectedBlockedDependenciesDetailCase(),
		projectedUnknownTaskStatusDetailCase(),
		projectedRepoFailureDetailCase(),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projection := status.ProjectWithLocalTaskStates(tt.snapshot, tt.localStates)
			entry := projectedStatusEntry(t, projection, tt.groupID, tt.taskID)
			assertProjectedStatusDetail(t, entry.SemanticDetail, tt.wantDetail)
			assertProjectedStatusDetail(t, entry.EpicProgress, tt.wantEpicProgress)

			assertStatusUsesProjectedMeaning(t, projection, tt.wantWide, tt.wantNarrow)
		})
	}
}

func assertStatusUsesProjectedMeaning(t *testing.T, projection status.Projection, wantWide, wantNarrow string) {
	t.Helper()
	for _, options := range []statusRenderOptions{{}, {MaxWidth: 52, NoTruncate: true}} {
		var output bytes.Buffer
		if err := renderStatus(&output, projection, true, options); err != nil {
			t.Fatalf("render unbounded status: %v", err)
		}
		if !strings.Contains(output.String(), wantWide) {
			t.Fatalf("unbounded output missing %q:\n%s", wantWide, output.String())
		}
	}
	// Compact output must use projected meaning, not parse presentation text.
	for groupIndex := range projection.Groups {
		for entryIndex := range projection.Groups[groupIndex].Entries {
			entry := &projection.Groups[groupIndex].Entries[entryIndex]
			if entry.SemanticDetail.Kind != "" || entry.EpicProgress.Kind != "" {
				entry.Detail = "misleading fallback should not render"
			}
		}
	}
	const narrowWidth = 88
	narrow := renderProjectedStatusForTest(t, projection, narrowWidth)
	if !strings.Contains(narrow, wantNarrow) {
		t.Fatalf("narrow output missing compact detail %q:\n%s", wantNarrow, narrow)
	}
	if wantWide != wantNarrow && strings.Contains(narrow, wantWide) {
		t.Fatalf("narrow output kept full detail %q:\n%s", wantWide, narrow)
	}
	if strings.Contains(narrow, "misleading fallback") {
		t.Fatalf("compact output parsed presentation text:\n%s", narrow)
	}
	assertStatusLinesWithinWidth(t, narrow, narrowWidth)
}

type projectedStatusDetailCase struct {
	name             string
	snapshot         task.SnapshotResult
	localStates      status.LocalTaskStateIndex
	groupID          status.GroupID
	taskID           string
	wantDetail       status.Detail
	wantEpicProgress status.Detail
	wantWide         string
	wantNarrow       string
}

func projectedPRDetailCase() projectedStatusDetailCase {
	taskItem := projectedStatusTask("op-pr", task.StatusOpen)
	taskItem.Metadata = task.Metadata{task.MetadataPRURL: "https://github.test/org/alpha/pull/44"}
	return projectedStatusDetailCase{
		name:       "pull request",
		snapshot:   projectedStatusSnapshot(taskItem),
		groupID:    status.GroupInReview,
		taskID:     taskItem.ID,
		wantDetail: status.Detail{Kind: status.DetailPullRequest, URL: "https://github.test/org/alpha/pull/44"},
		wantWide:   "https://github.test/org/alpha/pull/44",
		wantNarrow: "PR #44",
	}
}

func projectedEpicProgressDetailCase() projectedStatusDetailCase {
	epic := projectedStatusTask("op-epic", task.StatusOpen)
	epic.IssueType = task.IssueTypeEpic
	epic.Relations.ChildCount = 3
	openChild := projectedStatusTask("op-epic.1", task.StatusOpen)
	openChild.Relations.ParentID = epic.ID
	closedChild := projectedStatusTask("op-epic.2", task.StatusClosed)
	closedChild.Relations.ParentID = epic.ID
	return projectedStatusDetailCase{
		name:             "epic progress",
		snapshot:         projectedStatusSnapshot(epic, openChild, closedChild),
		groupID:          status.GroupReadyToRun,
		taskID:           epic.ID,
		wantEpicProgress: status.Detail{Kind: status.DetailEpicProgress, Completed: 1, Total: 3},
		wantWide:         "1/3 done",
		wantNarrow:       "1/3",
	}
}

func projectedRunDetailCase(
	name string,
	taskID string,
	taskStatus task.Status,
	run taskstate.RunAttempt,
	groupID status.GroupID,
	wantDetail status.Detail,
	wantWide string,
	wantNarrow string,
) projectedStatusDetailCase {
	taskItem := projectedStatusTask(taskID, taskStatus)
	return projectedStatusDetailCase{
		name:        name,
		snapshot:    projectedStatusSnapshot(taskItem),
		localStates: projectedStatusLocalStates(taskItem.ID, status.LocalTaskState{LatestRun: &run}),
		groupID:     groupID,
		taskID:      taskItem.ID,
		wantDetail:  wantDetail,
		wantWide:    wantWide,
		wantNarrow:  wantNarrow,
	}
}

func projectedNoRunDetailCase() projectedStatusDetailCase {
	taskItem := projectedStatusTask("op-no-run", task.StatusInProgress)
	return projectedStatusDetailCase{
		name:       "no run",
		snapshot:   projectedStatusSnapshot(taskItem),
		groupID:    status.GroupIdle,
		taskID:     taskItem.ID,
		wantDetail: status.Detail{Kind: status.DetailNoRun},
		wantWide:   "no attached run recorded",
		wantNarrow: "no run",
	}
}

func projectedLocalReviewDetailCase(
	name string,
	review *taskstate.ReviewAttempt,
	groupID status.GroupID,
	wantDetail status.Detail,
	wantWide string,
	wantNarrow string,
) projectedStatusDetailCase {
	taskItem := projectedReviewReadyTask("op-review")
	run := projectedCompletedRun()
	localState := projectedReviewReadyLocalState(taskItem.ID, "main", "/fixture/alpha")
	localState.LatestRun = &run
	localState.LatestReview = review
	return projectedStatusDetailCase{
		name:        name,
		snapshot:    projectedStatusSnapshot(taskItem),
		localStates: projectedStatusLocalStates(taskItem.ID, localState),
		groupID:     groupID,
		taskID:      taskItem.ID,
		wantDetail:  wantDetail,
		wantWide:    wantWide,
		wantNarrow:  wantNarrow,
	}
}

func projectedParentMissingDetailCase() projectedStatusDetailCase {
	child := projectedStatusTask("op-child", task.StatusOpen)
	child.Relations.ParentID = "op-parent"
	return projectedStatusDetailCase{
		name:       "parent missing",
		snapshot:   projectedStatusSnapshot(child),
		groupID:    status.GroupNeedsAttention,
		taskID:     child.ID,
		wantDetail: status.Detail{Kind: status.DetailParentMissing, ID: "op-parent"},
		wantWide:   "immediate parent epic op-parent is missing",
		wantNarrow: "parent op-parent missing",
	}
}

func projectedParentNotEpicDetailCase() projectedStatusDetailCase {
	parent := projectedStatusTask("op-parent", task.StatusOpen)
	child := projectedStatusTask("op-child", task.StatusOpen)
	child.Relations.ParentID = parent.ID
	return projectedStatusDetailCase{
		name:       "parent not epic",
		snapshot:   projectedStatusSnapshot(parent, child),
		groupID:    status.GroupNeedsAttention,
		taskID:     child.ID,
		wantDetail: status.Detail{Kind: status.DetailParentNotEpic, ID: parent.ID, State: "task"},
		wantWide:   "immediate parent op-parent has issue_type=task",
		wantNarrow: "parent op-parent not epic",
	}
}

func projectedParentNotReadyDetailCase() projectedStatusDetailCase {
	parent := projectedStatusTask("op-parent", task.StatusOpen)
	parent.IssueType = task.IssueTypeEpic
	child := projectedStatusTask("op-child", task.StatusOpen)
	child.Relations.ParentID = parent.ID
	return projectedStatusDetailCase{
		name:       "parent open",
		snapshot:   projectedStatusSnapshot(parent, child),
		groupID:    status.GroupBlocked,
		taskID:     child.ID,
		wantDetail: status.Detail{Kind: status.DetailParentNotReady, ID: parent.ID, State: "open"},
		wantWide:   "immediate parent epic op-parent is open",
		wantNarrow: "parent op-parent open",
	}
}

func projectedMissingExternalRefDetailCase() projectedStatusDetailCase {
	taskItem := projectedStatusTask("op-missing-ref", task.StatusOpen)
	repository := projectedStatusRepository()
	repository.TitleTemplate = "[{{external_ref}}] {{summary}}"
	return projectedStatusDetailCase{
		name:       "missing external reference",
		snapshot:   task.SnapshotResult{Repositories: []task.RepositorySnapshot{{Repository: repository, Tasks: []task.Task{taskItem}}}},
		groupID:    status.GroupNeedsAttention,
		taskID:     taskItem.ID,
		wantDetail: status.Detail{Kind: status.DetailMissingExternalRef, ID: taskItem.ID},
		wantWide:   "missing required external reference",
		wantNarrow: "missing ext ref",
	}
}

func projectedWrongTargetDetailCase(
	name string,
	taskID string,
	branch string,
	worktree string,
	wantDetail status.Detail,
	wantWide string,
	wantNarrow string,
) projectedStatusDetailCase {
	taskItem := projectedReviewReadyTask(taskID)
	taskItem.Metadata[task.MetadataBranch] = branch
	taskItem.Metadata[task.MetadataWorktree] = worktree
	run := projectedCompletedRun()
	localState := status.LocalTaskState{
		LatestRun: &run,
		GitFacts:  &taskstate.GitFacts{Branch: branch, Worktree: worktree},
	}
	return projectedStatusDetailCase{
		name:        name,
		snapshot:    projectedStatusSnapshot(taskItem),
		localStates: projectedStatusLocalStates(taskItem.ID, localState),
		groupID:     status.GroupNeedsAttention,
		taskID:      taskItem.ID,
		wantDetail:  wantDetail,
		wantWide:    wantWide,
		wantNarrow:  wantNarrow,
	}
}

func projectedFinalizedButOpenDetailCase() projectedStatusDetailCase {
	taskItem := projectedReviewReadyTask("op-finalized-open")
	run := projectedCompletedRun()
	closedAt := time.Date(2026, 6, 3, 11, 0, 0, 0, time.UTC)
	localState := projectedReviewReadyLocalState(taskItem.ID, "main", "/fixture/alpha")
	localState.LatestRun = &run
	localState.Finalization = taskstate.Finalization{ClosedAt: &closedAt}
	return projectedStatusDetailCase{
		name:        "finalized but open",
		snapshot:    projectedStatusSnapshot(taskItem),
		localStates: projectedStatusLocalStates(taskItem.ID, localState),
		groupID:     status.GroupNeedsAttention,
		taskID:      taskItem.ID,
		wantDetail:  status.Detail{Kind: status.DetailFinalizedButOpen},
		wantWide:    "finalization recorded but backend task is not closed",
		wantNarrow:  "finalized but open",
	}
}

func projectedMissingDependencyDetailCase() projectedStatusDetailCase {
	taskItem := projectedStatusTask("op-missing-dep", task.StatusOpen)
	taskItem.Relations.DependencyIDs = []string{"op-gone"}
	return projectedStatusDetailCase{
		name:       "missing dependency",
		snapshot:   projectedStatusSnapshot(taskItem),
		groupID:    status.GroupNeedsAttention,
		taskID:     taskItem.ID,
		wantDetail: status.Detail{Kind: status.DetailMissingDependency, ID: "op-gone", IDs: []string{"op-gone"}, Count: 1},
		wantWide:   "missing dependency op-gone",
		wantNarrow: "missing op-gone",
	}
}

func projectedMissingDependenciesDetailCase() projectedStatusDetailCase {
	taskItem := projectedStatusTask("op-missing-deps", task.StatusOpen)
	taskItem.Relations.DependencyIDs = []string{"op-gone-b", "op-gone-a"}
	return projectedStatusDetailCase{
		name:       "missing dependencies",
		snapshot:   projectedStatusSnapshot(taskItem),
		groupID:    status.GroupNeedsAttention,
		taskID:     taskItem.ID,
		wantDetail: status.Detail{Kind: status.DetailMissingDependencies, IDs: []string{"op-gone-a", "op-gone-b"}, Count: 2},
		wantWide:   "missing dependency op-gone-a, op-gone-b",
		wantNarrow: "2 deps missing",
	}
}

func projectedDependencyDetailsMissingCase() projectedStatusDetailCase {
	taskItem := projectedStatusTask("op-unknown-blockers", task.StatusOpen)
	taskItem.Relations.BlockedByCount = 2
	return projectedStatusDetailCase{
		name:       "dependency details missing",
		snapshot:   projectedStatusSnapshot(taskItem),
		groupID:    status.GroupNeedsAttention,
		taskID:     taskItem.ID,
		wantDetail: status.Detail{Kind: status.DetailDependencyDetailsMissing, Count: 2},
		wantWide:   "dependency details missing for 2 blocker(s)",
		wantNarrow: "2 blockers unknown",
	}
}

func projectedBlockedDependencyDetailCase() projectedStatusDetailCase {
	dependency := projectedStatusTask("op-blocker", task.StatusOpen)
	taskItem := projectedStatusTask("op-blocked", task.StatusOpen)
	taskItem.Relations.DependencyIDs = []string{dependency.ID}
	return projectedStatusDetailCase{
		name:       "blocked dependency",
		snapshot:   projectedStatusSnapshot(dependency, taskItem),
		groupID:    status.GroupBlocked,
		taskID:     taskItem.ID,
		wantDetail: status.Detail{Kind: status.DetailBlockedDependency, ID: dependency.ID, IDs: []string{dependency.ID}, Count: 1},
		wantWide:   "blocked by op-blocker",
		wantNarrow: "blocked op-blocker",
	}
}

func projectedBlockedDependenciesDetailCase() projectedStatusDetailCase {
	first := projectedStatusTask("op-blocker-a", task.StatusOpen)
	second := projectedStatusTask("op-blocker-b", task.StatusOpen)
	taskItem := projectedStatusTask("op-blocked-many", task.StatusOpen)
	taskItem.Relations.DependencyIDs = []string{second.ID, first.ID}
	return projectedStatusDetailCase{
		name:       "blocked dependencies",
		snapshot:   projectedStatusSnapshot(first, second, taskItem),
		groupID:    status.GroupBlocked,
		taskID:     taskItem.ID,
		wantDetail: status.Detail{Kind: status.DetailBlockedDependencies, IDs: []string{first.ID, second.ID}, Count: 2},
		wantWide:   "blocked by op-blocker-a, op-blocker-b",
		wantNarrow: "blocked by 2 deps",
	}
}

func projectedUnknownTaskStatusDetailCase() projectedStatusDetailCase {
	taskItem := projectedStatusTask("op-triaged", task.Status("triaged"))
	return projectedStatusDetailCase{
		name:       "unknown task status",
		snapshot:   projectedStatusSnapshot(taskItem),
		groupID:    status.GroupNeedsAttention,
		taskID:     taskItem.ID,
		wantDetail: status.Detail{Kind: status.DetailUnknownTaskStatus, State: "triaged"},
		wantWide:   "status triaged is not locally actionable",
		wantNarrow: "status triaged",
	}
}

func projectedRepoFailureDetailCase() projectedStatusDetailCase {
	repository := projectedStatusRepository()
	repository.ID = "broken"
	repository.Name = "Broken"
	repository.TaskIDPrefix = "br"
	return projectedStatusDetailCase{
		name: "repository failure",
		snapshot: task.SnapshotResult{Failures: []task.RepoFailure{{
			Repository: repository,
			Source:     "task_backend",
			Operation:  "snapshot",
			Err:        errors.New("bd list failed"),
		}}},
		groupID:    status.GroupNeedsAttention,
		wantDetail: status.Detail{Kind: status.DetailRepoFailure, Source: "task_backend", Operation: "snapshot"},
		wantWide:   "task_backend/snapshot: bd list failed",
		wantNarrow: "task_backend/snapshot failed",
	}
}

func projectedStatusEntry(
	t *testing.T,
	projection status.Projection,
	groupID status.GroupID,
	taskID string,
) status.Entry {
	t.Helper()

	for _, group := range projection.Groups {
		if group.ID != groupID {
			continue
		}
		for _, entry := range group.Entries {
			if taskID == "" && entry.Kind == status.EntryRepoFailure {
				return entry
			}
			if entry.Kind == status.EntryTask && entry.Task.ID == taskID {
				return entry
			}
		}
	}
	t.Fatalf("missing projected entry %s in group %s", taskID, groupID)
	return status.Entry{}
}

func assertProjectedStatusDetail(t *testing.T, got status.Detail, want status.Detail) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("semantic detail = %#v, want %#v", got, want)
	}
}

func renderProjectedStatusForTest(t *testing.T, projection status.Projection, width int) string {
	t.Helper()

	options := statusRenderOptions{MaxWidth: width}
	var output bytes.Buffer
	if err := renderStatus(&output, projection, true, options); err != nil {
		t.Fatalf("render status: %v", err)
	}
	return output.String()
}

func projectedStatusSnapshot(tasks ...task.Task) task.SnapshotResult {
	return task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: projectedStatusRepository(),
		Tasks:      tasks,
	}}}
}

func projectedStatusRepository() task.Repository {
	return task.Repository{
		ID:            "alpha",
		Name:          "Alpha",
		TaskIDPrefix:  "op",
		Path:          "/fixture/alpha",
		DefaultBranch: "main",
	}
}

func projectedStatusTask(id string, taskStatus task.Status) task.Task {
	return task.Task{
		ID:        id,
		Priority:  1,
		Title:     "Preserve enough title context when status output is narrow",
		Status:    taskStatus,
		IssueType: task.IssueTypeTask,
	}
}

func projectedReviewReadyTask(id string) task.Task {
	taskItem := projectedStatusTask(id, task.StatusInProgress)
	taskItem.Metadata = task.Metadata{
		task.MetadataBranch:   "main",
		task.MetadataWorktree: "/fixture/alpha",
	}
	return taskItem
}

func projectedStatusLocalStates(taskID string, localState status.LocalTaskState) status.LocalTaskStateIndex {
	return status.LocalTaskStateIndex{
		status.RunStateKey("alpha", taskID): localState,
	}
}

func projectedReviewReadyLocalState(taskID string, branch string, worktree string) status.LocalTaskState {
	return status.LocalTaskState{
		GitFacts:        &taskstate.GitFacts{Branch: branch, Worktree: worktree},
		ExpectedTargets: projectedExpectedTargets(taskID),
	}
}

func projectedExpectedTargets(taskID string) *tasktarget.ExpectedTargets {
	return &tasktarget.ExpectedTargets{
		MainSolo: tasktarget.Target{
			Kind:     tasktarget.TargetMainSolo,
			Branch:   "main",
			Worktree: "/fixture/alpha",
		},
		WorktreeTeam: tasktarget.Target{
			Kind:     tasktarget.TargetWorktreeTeam,
			Branch:   "orpheus/" + taskID,
			Worktree: "/fixture/orpheus/worktrees/" + taskID,
		},
		RepoRootTeam: tasktarget.Target{
			Kind:     tasktarget.TargetRepoRootTeam,
			Branch:   "orpheus/" + taskID,
			Worktree: "/fixture/alpha",
		},
	}
}

func projectedCompletedRun() taskstate.RunAttempt {
	return taskstate.RunAttempt{
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary:              "Done",
			Description:          "Ready for local review.",
			DetailedDescription:  "Detailed PR body.",
			TechnicalExplanation: "Technical explanation.",
			CompletedAt:          time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		},
	}
}

func reviewAttemptForStatus(reviewStatus taskstate.ReviewStatus) *taskstate.ReviewAttempt {
	review := &taskstate.ReviewAttempt{
		Attempt:   7,
		Status:    reviewStatus,
		Pipeline:  "default",
		Step:      "inspect",
		StartedAt: time.Date(2026, 6, 3, 10, 30, 0, 0, time.UTC),
	}
	return review
}

func reviewAttemptWithDecisionLost() *taskstate.ReviewAttempt {
	review := reviewAttemptWithFindings(1)
	review.AutomatedBlockerDecisionInterrupted = true
	return review
}

func reviewAttemptWithDecisionRequired() *taskstate.ReviewAttempt {
	review := reviewAttemptWithFindings(1)
	review.Findings[0].Step = "lint"
	review.Steps = []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindCheck, Name: "lint"}}
	return review
}

func reviewAttemptWithTargetedBlocker() *taskstate.ReviewAttempt {
	review := reviewAttemptWithFindings(1)
	review.Findings[0].TargetedByRunAttempt = 2
	return review
}

func reviewAttemptWithBudgetSpent() *taskstate.ReviewAttempt {
	review := reviewAttemptWithFindings(1)
	review.AutonomousBudgetExhausted = true
	return review
}

func reviewAttemptWithFindings(count int) *taskstate.ReviewAttempt {
	review := reviewAttemptForStatus(taskstate.ReviewStatusBlocked)
	review.Findings = make([]taskstate.ReviewFinding, 0, count)
	for i := 0; i < count; i++ {
		review.Findings = append(review.Findings, taskstate.ReviewFinding{
			Type:        taskstate.FindingTypeBlocking,
			Title:       "Finding",
			Description: "Fix it",
		})
	}
	return review
}

func assertCrowdedTreeSymbolOutput(t *testing.T, got string) {
	t.Helper()

	assertNarrowTreeSymbolLegend(t, got)
	for _, unexpected := range []string{"Reviewing", "Working"} {
		if strings.Contains(got, unexpected) {
			t.Fatalf("narrow output kept descriptive status %q:\n%s", unexpected, got)
		}
	}
}

func assertCrowdedTreeDescriptiveOutput(t *testing.T, got string) {
	t.Helper()

	if strings.Contains(got, "Legend:") {
		t.Fatalf("wide enough output unexpectedly used symbol legend:\n%s", got)
	}
	for _, want := range []string{"STATUS", "Reviewing", "Working"} {
		if !strings.Contains(got, want) {
			t.Fatalf("wide enough output missing %q:\n%s", want, got)
		}
	}
}

func assertCrowdedTreeTitleContext(t *testing.T, got string) {
	t.Helper()

	for _, want := range []string{
		"Preserve task title context",
		"Add review pipeline status",
		"└─ op-40p.28",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output did not preserve title context %q:\n%s", want, got)
		}
	}
}

func assertStatusColumnAligned(t *testing.T, got string) {
	t.Helper()

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("output has too few lines to check status alignment:\n%s", got)
	}
	headerColumn := statusTokenDisplayColumn(lines[0], "S")
	if headerColumn < 0 {
		headerColumn = statusTokenDisplayColumn(lines[0], "STATUS")
	}
	if headerColumn < 0 {
		t.Fatalf("output header missing status column:\n%s", got)
	}
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "Legend:") || strings.HasPrefix(line, "!") ||
			strings.HasPrefix(line, "◉") || strings.HasPrefix(line, "▶") ||
			strings.HasPrefix(line, "‖") || strings.HasPrefix(line, "○") ||
			strings.HasPrefix(line, "×") || strings.HasPrefix(line, "✓") {
			continue
		}
		column := statusLineDisplayColumn(line)
		if column != headerColumn {
			t.Fatalf("status column = %d, want %d in line %q:\n%s", column, headerColumn, line, got)
		}
	}
}

func statusLineDisplayColumn(line string) int {
	for _, token := range []string{"!", "◉", "▶", "‖", "○", "×", "✓", "Needs attention", "Reviewing", "Working", "Idle", "Ready", "Blocked", "Done / closed"} {
		if column := statusTokenDisplayColumn(line, token); column >= 0 {
			return column
		}
	}
	return -1
}

func statusTokenDisplayColumn(line string, token string) int {
	start := 0
	for start < len(line) {
		index := strings.Index(line[start:], token)
		if index < 0 {
			return -1
		}
		index += start
		before := index == 0 || line[index-1] == ' '
		afterIndex := index + len(token)
		after := afterIndex == len(line) || line[afterIndex] == ' '
		if before && after {
			return displayWidth(line[:index])
		}
		start = afterIndex
	}
	return -1
}

func narrowTreeStatusProjection() status.Projection {
	repository := task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "op"}
	return status.Projection{Groups: []status.Group{
		{
			ID:    status.GroupIdle,
			Title: "Idle",
			Entries: []status.Entry{{
				Kind:           status.EntryTask,
				Repository:     repository,
				Task:           narrowTreeTask("op-epic", "Root title context remains visible", task.StatusInProgress, ""),
				Detail:         "no attached run recorded",
				SemanticDetail: status.Detail{Kind: status.DetailNoRun},
				EpicProgress:   status.Detail{Kind: status.DetailEpicProgress, Completed: 0, Total: 1},
			}},
		},
		{
			ID:    status.GroupReadyToRun,
			Title: "Ready to run",
			Entries: []status.Entry{{
				Kind:       status.EntryTask,
				Repository: repository,
				Task:       narrowTreeTask("op-child", "Child title context remains visible", task.StatusOpen, "op-epic"),
				Detail:     "-",
			}, {
				Kind:       status.EntryTask,
				Repository: repository,
				Task:       narrowTreeTask("op-grandchild", "Grandchild title can spend nested ID space", task.StatusOpen, "op-child"),
				Detail:     "-",
			}},
		},
	}}
}

func crowdedTreeStatusProjection() status.Projection {
	repository := task.Repository{ID: "orpheus", Name: "Orpheus", TaskIDPrefix: "op"}
	return status.Projection{Groups: []status.Group{
		{
			ID:    status.GroupInReview,
			Title: "Reviewing",
			Entries: []status.Entry{{
				Kind:       status.EntryTask,
				Repository: repository,
				Task: task.Task{
					ID:        "op-6i1",
					Priority:  2,
					Title:     "Preserve task title context in narrow status output",
					Status:    task.StatusOpen,
					IssueType: task.IssueTypeTask,
				},
				Detail: "local review; run task run (waiting for manual step manual)",
				SemanticDetail: status.Detail{
					Kind: status.DetailReviewManualStep,
					Step: "manual",
				},
			}, {
				Kind:       status.EntryTask,
				Repository: repository,
				Task: task.Task{
					ID:        "op-40p.28",
					Priority:  1,
					Title:     "Add review pipeline status context",
					Status:    task.StatusOpen,
					IssueType: task.IssueTypeTask,
					Relations: task.RelationSummary{ParentID: "op-40p"},
				},
				Detail:         "review running",
				SemanticDetail: status.Detail{Kind: status.DetailReviewRunning},
			}},
		},
		{
			ID:    status.GroupWorking,
			Title: "Working",
			Entries: []status.Entry{{
				Kind:       status.EntryTask,
				Repository: repository,
				Task: task.Task{
					ID:        "op-40p",
					Priority:  1,
					Title:     "Add task review pipeline status output",
					Status:    task.StatusInProgress,
					IssueType: task.IssueTypeEpic,
					Relations: task.RelationSummary{ChildCount: 32},
				},
				Detail:         "no attached run recorded",
				SemanticDetail: status.Detail{Kind: status.DetailNoRun},
				EpicProgress:   status.Detail{Kind: status.DetailEpicProgress, Completed: 27, Total: 32},
			}},
		},
	}}
}

func epicActionStatusProjection() status.Projection {
	repository := task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "op"}
	return status.Projection{Groups: []status.Group{
		{
			ID:    status.GroupNeedsAttention,
			Title: "Needs attention",
			Entries: []status.Entry{{
				Kind:       status.EntryTask,
				Repository: repository,
				Task:       epicActionTask("op-review", "Review failed epic"),
				Detail:     "review failed operationally; run task run",
				SemanticDetail: status.Detail{
					Kind: status.DetailReviewFailed,
				},
				EpicProgress: status.Detail{Kind: status.DetailEpicProgress, Completed: 2, Total: 5},
			}, {
				Kind:       status.EntryTask,
				Repository: repository,
				Task:       epicActionTask("op-run", "Run failed epic"),
				Detail:     "run attempt 2 failed",
				SemanticDetail: status.Detail{
					Kind:    status.DetailRunFailed,
					Attempt: 2,
				},
				EpicProgress: status.Detail{Kind: status.DetailEpicProgress, Completed: 3, Total: 8},
			}},
		},
		{
			ID:    status.GroupInReview,
			Title: "Reviewing",
			Entries: []status.Entry{{
				Kind:           status.EntryTask,
				Repository:     repository,
				Task:           epicActionTask("op-pr", "Pull request epic"),
				Detail:         "https://github.test/org/alpha/pull/44",
				SemanticDetail: status.Detail{Kind: status.DetailPullRequest, URL: "https://github.test/org/alpha/pull/44"},
				EpicProgress:   status.Detail{Kind: status.DetailEpicProgress, Completed: 1, Total: 3},
			}},
		},
		{
			ID:    status.GroupBlocked,
			Title: "Blocked",
			Entries: []status.Entry{{
				Kind:           status.EntryTask,
				Repository:     repository,
				Task:           epicActionTask("op-blocked", "Blocked dependency epic"),
				Detail:         "blocked by op-dep",
				SemanticDetail: status.Detail{Kind: status.DetailBlockedDependency, ID: "op-dep"},
				EpicProgress:   status.Detail{Kind: status.DetailEpicProgress, Completed: 4, Total: 9},
			}},
		},
	}}
}

func epicActionTask(id string, title string) task.Task {
	return task.Task{
		ID:        id,
		Priority:  1,
		Title:     title,
		Status:    task.StatusInProgress,
		IssueType: task.IssueTypeEpic,
	}
}

func narrowTreeTask(id string, title string, taskStatus task.Status, parentID string) task.Task {
	taskItem := task.Task{
		ID:        id,
		Priority:  1,
		Title:     title,
		Status:    taskStatus,
		IssueType: task.IssueTypeTask,
		Relations: task.RelationSummary{ParentID: parentID},
	}
	if parentID == "" {
		taskItem.IssueType = task.IssueTypeEpic
		taskItem.Relations.ChildCount = 1
	} else if id == "op-child" {
		taskItem.IssueType = task.IssueTypeEpic
		taskItem.Relations.ChildCount = 1
	}
	return taskItem
}

func assertNarrowTreeStatusOutput(
	t *testing.T,
	got string,
	width int,
	symbolMode bool,
	rootPrefix string,
	childPrefix string,
) {
	t.Helper()

	for _, want := range []string{"TASK_ID", "TITLE", "DETAIL"} {
		if !strings.Contains(got, want) {
			t.Fatalf("narrow output missing %q:\n%s", want, got)
		}
	}
	if symbolMode {
		assertNarrowTreeSymbolLegend(t, got)
	} else if strings.Contains(got, "Legend:") {
		t.Fatalf("symbol legend rendered even though descriptive labels fit:\n%s", got)
	}
	if !strings.Contains(got, rootPrefix) || !strings.Contains(got, childPrefix) {
		t.Fatalf("narrow output did not preserve title prefixes:\n%s", got)
	}
	if !strings.Contains(got, "└─ op-child") {
		t.Fatalf("narrow output did not preserve first-level tree row:\n%s", got)
	}
	if !strings.Contains(got, "  └─ op-grandchild") {
		t.Fatalf("narrow output did not preserve nested tree row:\n%s", got)
	}
	assertStatusLinesWithinWidth(t, got, width)
}

func assertNarrowTreeSymbolLegend(t *testing.T, got string) {
	t.Helper()

	for _, want := range []string{"S", "▶", "○ ready", "Legend:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("narrow output missing %q:\n%s", want, got)
		}
	}
}

func TestRenderStatusFallsBackSafelyForUnparseablePullRequestNumber(t *testing.T) {
	projection := manualReviewStatusProjection("misleading fallback text")
	projection.Groups[0].Entries[0].SemanticDetail = status.Detail{Kind: status.DetailPullRequest, URL: "not a url"}
	got := renderProjectedStatusForTest(t, projection, 64)
	if !strings.Contains(got, "PR") || strings.Contains(got, "misleading") || strings.Contains(got, "not a url") {
		t.Fatalf("unparseable PR was not safely summarized:\n%s", got)
	}
	assertStatusLinesWithinWidth(t, got, 64)
}

func manualReviewStatusProjection(detail string) status.Projection {
	return status.Projection{Groups: []status.Group{{
		ID:    status.GroupInReview,
		Title: "Reviewing",
		Entries: []status.Entry{{
			Kind: status.EntryTask,
			Repository: task.Repository{
				ID:           "alpha",
				Name:         "Repository Name",
				TaskIDPrefix: "op",
			},
			Task: task.Task{
				ID:       "op-manual",
				Priority: 2,
				Title:    "Resume the paused review attempt",
			},
			Detail:         detail,
			SemanticDetail: status.Detail{Kind: status.DetailReviewManualStep, Step: "local-review"},
		}},
	}}}
}

func TestRenderStatusNoTruncatePreservesUnboundedOutput(t *testing.T) {
	projection := status.Projection{Groups: []status.Group{{
		ID:    status.GroupInReview,
		Title: "Reviewing",
		Entries: []status.Entry{{
			Kind: status.EntryTask,
			Repository: task.Repository{
				ID:           "alpha",
				Name:         "Very Long Repository Name",
				TaskIDPrefix: "op",
			},
			Task: task.Task{
				ID:       "op-123456",
				Priority: 2,
				Title:    "Implement an extremely long operator status title that cannot fit",
			},
			Detail:         "https://github.test/org/alpha/pull/123456",
			SemanticDetail: status.Detail{Kind: status.DetailPullRequest, URL: "https://github.test/org/alpha/pull/123456"},
		}},
	}}}

	var output bytes.Buffer
	err := renderStatus(&output, projection, true, statusRenderOptions{MaxWidth: 48, NoTruncate: true})
	if err != nil {
		t.Fatalf("render status: %v", err)
	}

	got := output.String()
	for _, want := range []string{
		"REPO",
		"Very Long Repository Name",
		"Implement an extremely long operator status title that cannot fit",
		"https://github.test/org/alpha/pull/123456",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unbounded output missing %q:\n%s", want, got)
		}
	}
	if !hasStatusLineWiderThan(got, 48) {
		t.Fatalf("unbounded output unexpectedly fit 48 columns:\n%s", got)
	}
}

func assertStatusLinesWithinWidth(t *testing.T, output string, width int) {
	t.Helper()

	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		if displayWidth(line) > width {
			t.Fatalf("line width = %d, want <= %d:\n%s", displayWidth(line), width, output)
		}
	}
}

func hasStatusLineWiderThan(output string, width int) bool {
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		if displayWidth(line) > width {
			return true
		}
	}
	return false
}
