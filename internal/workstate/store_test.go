package workstate_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreRoundTripRetainsOwnedHistoryAndIndependentSnapshots(t *testing.T) {
	store, _ := newStore(t)
	original := retainedHistory(t)
	original.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Args = append(original.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Args, " - You are an agent dispatched by Orpheus.\n\nRun `orpheus agent context` now.\n")
	encoded, err := workstate.Encode(original)
	require.NoError(t, err)
	decoded, err := workstate.Decode(encoded)
	require.NoError(t, err)
	require.Equal(t, original, decoded, "the standalone codec must use the same validated wire format as storage")
	version, err := store.Save(original, 0)
	require.NoError(t, err)
	require.Equal(t, work.StateVersion(1), version)
	require.Zero(t, original.Version, "save must not mutate its input")
	loaded, err := store.Load("repo", "task")
	require.NoError(t, err)
	original.Version = 1
	require.Equal(t, original, loaded)
	w := &loaded.Workflows[0]
	assert.Equal(t, "feat: original intent", w.FeatureCompletion().Summary)
	assert.Equal(t, "fix: unchanged assessment", w.Phases[7].(*work.ImplementationPhase).Completion.Summary)
	output, ok := w.LatestOutput()
	require.True(t, ok)
	assert.Equal(t, work.OutputRef{Phase: 7}, output)
	code, err := w.Output(output)
	require.NoError(t, err)
	assert.Equal(t, "R2", code.Commit)
	assert.Equal(t, "P2", w.Phases[5].(*work.PublicationPhase).Delivery.(*work.PullRequestDelivery).Head)
	assert.Equal(t, "S1", w.Phases[6].(*work.ExternalReviewPhase).BranchSyncs[0].Push.Head)
	assert.Nil(t, loaded.ActiveWorkflow().FeatureCompletion(), "an empty current workflow must not inherit earlier intent")

	loaded.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Usage.TotalTokens = 99999
	loaded.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[1].(*work.AgentReviewStep).Report.Findings[0].Dispositions[0].(*work.RepairRequired).At = when(99)
	again, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, int64(150), again.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Usage.TotalTokens)
	assert.Zero(t, again.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[1].(*work.AgentReviewStep).Report.Findings[0].Dispositions[0].(*work.RepairRequired).At)
}

func TestStoreManualFindingsSurvivePendingAssessmentAndObservedFollowUpCreation(t *testing.T) {
	store, _ := newStore(t)
	manual := &work.ManualReviewStep{Slot: 0, Findings: []work.Finding{
		{Title: "Advisory", Classification: work.FindingAdvisory, Description: "Retain this through EOF", SuggestedAction: "Consider the boundary"},
		{Title: "Separate work", Classification: work.FindingSeparateTask, Description: "Keep this outside the feature", Proposal: &work.FollowUpProposal{Title: "Extract helper", Description: "Isolate the helper", AcceptanceCriteria: "Focused helper tests"}},
	}}
	review := &work.ReviewPhase{StartedAt: when(10), Scope: work.ReviewScope{Requirements: "Feature requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "D"}}, Steps: []work.ReviewStep{manual}}
	snapshot := work.State{RepositoryID: "repo", TaskID: "task", Workflows: []work.Workflow{{
		ReviewContracts: []work.ReviewContract{{PipelineKey: "manual", Slots: []work.ReviewSlot{{Kind: work.ReviewManual, ConfigKey: "inspect"}}}},
		Phases:          []work.Phase{preparation(), effort("feat: original intent", "R1", 1), review},
	}}}
	_, err := store.Save(snapshot, 0)
	require.NoError(t, err)
	pending, err := store.Load("repo", "task")
	require.NoError(t, err)
	ref := work.FindingRef{Phase: 2, Step: 0, Finding: 0}
	note, err := pending.Workflows[0].Finding(ref)
	require.NoError(t, err)
	assert.Equal(t, "Retain this through EOF", note.Description)
	assert.Equal(t, "Consider the boundary", note.SuggestedAction)
	outcome, err := pending.Workflows[0].ReviewOutcome(work.ReviewRef{Phase: 2})
	require.NoError(t, err)
	assert.Equal(t, work.ReviewPending, outcome, "reported findings do not invent an operator assessment")
	step := pending.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[0].(*work.ManualReviewStep)
	assert.Nil(t, step.Decision)
	require.Len(t, step.Findings, 2)
	step.Decision = &work.ApprovedReview{At: when(12)}
	_, err = store.Save(pending, pending.Version)
	require.NoError(t, err)
	approved, err := store.Load("repo", "task")
	require.NoError(t, err)
	followUpRef := work.FindingRef{Phase: 2, Step: 0, Finding: 1}
	followUp, err := approved.Workflows[0].Finding(followUpRef)
	require.NoError(t, err)
	assert.Equal(t, &work.FollowUpProposal{Title: "Extract helper", Description: "Isolate the helper", AcceptanceCriteria: "Focused helper tests"}, followUp.Proposal)
	assert.Nil(t, followUp.CreatedTask)
	followUp.CreatedTask = &work.CreatedFollowUp{TaskID: "op-followup", ObservedAt: when(13)}
	_, err = store.Save(approved, approved.Version)
	require.NoError(t, err)
	retained, err := store.Load("repo", "task")
	require.NoError(t, err)
	observed, err := retained.Workflows[0].Finding(followUpRef)
	require.NoError(t, err)
	assert.Equal(t, "op-followup", observed.CreatedTask.TaskID)
	assert.Equal(t, when(13), observed.CreatedTask.ObservedAt)
	outcome, err = retained.Workflows[0].ReviewOutcome(work.ReviewRef{Phase: 2})
	require.NoError(t, err)
	assert.Equal(t, work.ReviewPassed, outcome)

	for _, scenario := range []string{"late finding", "rewritten proposal", "rewritten observation", "removed observation", "retroactive promotion"} {
		t.Run(scenario, func(t *testing.T) {
			changed, err := store.Load("repo", "task")
			require.NoError(t, err)
			step := changed.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[0].(*work.ManualReviewStep)
			switch scenario {
			case "late finding":
				step.Findings = append(step.Findings, work.Finding{Title: "Late", Classification: work.FindingAdvisory, Description: "After answer"})
			case "rewritten proposal":
				step.Findings[1].Proposal.Title = "Different intent"
			case "rewritten observation":
				step.Findings[1].CreatedTask.TaskID = "another-task"
			case "removed observation":
				step.Findings[1].CreatedTask = nil
			case "retroactive promotion":
				step.Findings[0].Dispositions = []work.FindingDisposition{&work.PromotedFinding{At: when(11)}}
			}
			_, err = store.Save(changed, changed.Version)
			require.Error(t, err)
			unchanged, err := store.Load("repo", "task")
			require.NoError(t, err)
			assert.Equal(t, retained, unchanged, "a refused update must preserve independently reloaded findings and decisions")
		})
	}

	promoted, err := store.Load("repo", "task")
	require.NoError(t, err)
	note, err = promoted.Workflows[0].Finding(ref)
	require.NoError(t, err)
	note.Dispositions = append(note.Dispositions, &work.PromotedFinding{At: when(14)})
	_, err = store.Save(promoted, promoted.Version)
	require.NoError(t, err, "a later decision cannot rewrite the earlier approval's historical view")
	current, err := store.Load("repo", "task")
	require.NoError(t, err)
	outcome, err = current.Workflows[0].ReviewOutcome(work.ReviewRef{Phase: 2})
	require.NoError(t, err)
	assert.Equal(t, work.ReviewChangesRequired, outcome)
	note, err = current.Workflows[0].Finding(ref)
	require.NoError(t, err)
	assert.Equal(t, work.FindingAdvisory, note.Classification, "promotion retains the immutable reported classification")
	note.Dispositions = append(note.Dispositions, &work.WaivedFinding{Waiver: work.Waiver{Reason: "Explicitly accept the promoted concern", At: when(15)}})
	_, err = store.Save(current, current.Version)
	require.NoError(t, err)
	waived, err := store.Load("repo", "task")
	require.NoError(t, err)
	outcome, err = waived.Workflows[0].ReviewOutcome(work.ReviewRef{Phase: 2})
	require.NoError(t, err)
	assert.Equal(t, work.ReviewPassedWithWaivers, outcome)
}

func TestStoreRejectedSavesPreserveIndependentlyReloadedHistory(t *testing.T) {
	store, _ := newStore(t)
	initial := retainedHistory(t)
	_, err := store.Save(initial, 0)
	require.NoError(t, err)
	path, err := store.Path("repo", "task")
	require.NoError(t, err)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	cases := []struct {
		name     string
		change   func(*work.State)
		expected work.StateVersion
		conflict bool
	}{
		{"stale version", func(s *work.State) { s.Version = 0 }, 0, true},
		{"mismatched proposed version", func(s *work.State) { s.Version = 2 }, 1, true},
		{"invalid snapshot", func(s *work.State) { s.Workflows[0].Phases[1].(*work.ImplementationPhase).Output = &work.CodeOutput{} }, 1, false},
		{"rewritten completion", func(s *work.State) {
			s.Workflows[0].Phases[1].(*work.ImplementationPhase).Completion.Description = "Rewritten history"
		}, 1, false},
		{"rewritten usage", func(s *work.State) {
			s.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Usage.TotalTokens++
		}, 1, false},
		{"compacted history", func(s *work.State) { s.Workflows = s.Workflows[:1] }, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proposed, err := store.Load("repo", "task")
			require.NoError(t, err)
			tc.change(&proposed)
			_, err = store.Save(proposed, tc.expected)
			require.Error(t, err)
			if tc.conflict {
				require.ErrorIs(t, err, workstate.ErrConflict)
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			reloaded, err := store.Load("repo", "task")
			require.NoError(t, err)
			initial.Version = 1
			assert.Equal(t, initial, reloaded)
		})
	}
	proposed, err := store.Load("repo", "task")
	require.NoError(t, err)
	proposed.Workflows[2].Phases = append(proposed.Workflows[2].Phases, &work.PreparationPhase{})
	version, err := store.Save(proposed, 1)
	require.NoError(t, err)
	assert.Equal(t, work.StateVersion(2), version)
	_, err = store.Save(proposed, 1)
	require.ErrorIs(t, err, workstate.ErrConflict)
}

func TestStoreSuccessorComparisonUsesPersistedClockObservations(t *testing.T) {
	store, _ := newStore(t)
	observed := time.Now()
	prepared := preparation()
	prepared.EstablishedAt = &observed
	proposed := work.State{RepositoryID: "repo", TaskID: "task", Workflows: []work.Workflow{{Phases: []work.Phase{prepared}}}}
	version, err := store.Save(proposed, 0)
	require.NoError(t, err)
	proposed.Version = version
	proposed.Workflows[0].Phases = append(proposed.Workflows[0].Phases, &work.PreparationPhase{})
	_, err = store.Save(proposed, version)
	require.NoError(t, err, "an unchanged in-memory time with monotonic data must match its persisted observation")
	reloaded, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.True(t, observed.Equal(*reloaded.Workflows[0].Phases[0].(*work.PreparationPhase).EstablishedAt))
	assert.Equal(t, work.StateVersion(2), reloaded.Version)
	require.Len(t, reloaded.Workflows[0].Phases, 2)
}

func TestStoreConcurrentWritersCannotOverwriteTheSameVersion(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.Save(work.State{RepositoryID: "repo", TaskID: "task"}, 0)
	require.NoError(t, err)
	var proposals [2]work.State
	for i := range proposals {
		proposals[i], err = store.Load("repo", "task")
		require.NoError(t, err)
		proposals[i].Workflows = []work.Workflow{{Phases: []work.Phase{&work.PreparationPhase{}}}}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, proposed := range proposals {
		group.Go(func() { <-start; _, saveErr := store.Save(proposed, 1); results <- saveErr })
	}
	close(start)
	group.Wait()
	close(results)
	successes := 0
	for saveErr := range results {
		if saveErr == nil {
			successes++
		} else {
			assert.True(t, errors.Is(saveErr, workstate.ErrConflict) || errors.Is(saveErr, os.ErrExist), "unexpected error: %v", saveErr)
		}
	}
	assert.Equal(t, 1, successes)
	loaded, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, work.StateVersion(2), loaded.Version)
	require.Len(t, loaded.Workflows, 1)
	require.IsType(t, &work.PreparationPhase{}, loaded.Workflows[0].Phases[0])
}

func TestStoreRefusesUnsupportedOrAmbiguousDocumentsWithoutReplacingThem(t *testing.T) {
	store, paths := newStore(t)
	for name, data := range map[string]string{
		"old records":           "version: 4\nrepo_id: repo\ntask_id: task\nruns: []\n",
		"empty file":            "",
		"null document":         "null\n",
		"wrong format":          "format_version: 99\nstate: {}\n",
		"missing state version": "format_version: 1\nstate: {repository_id: repo, task_id: task}\n",
		"null state version":    "format_version: 1\nstate: {version: null, repository_id: repo, task_id: task}\n",
		"stored zero version":   "format_version: 1\nstate: {version: 0, repository_id: repo, task_id: task}\n",
		"foreign task":          "format_version: 1\nstate: {version: 1, repository_id: repo, task_id: other}\n",
		"trailing document":     "format_version: 1\nstate: {version: 1, repository_id: repo, task_id: task}\n---\nstate: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := workstate.Decode([]byte(data))
			// Version zero and foreign identity are valid standalone snapshots;
			// document storage additionally enforces its path and nonzero version.
			if name != "stored zero version" && name != "foreign task" {
				require.Error(t, err)
			}
			if name == "old records" {
				require.ErrorContains(t, err, "archive old task-state files outside the task-state directory")
			}
			path, err := paths.DataPath("repos/repo/tasks/task.yaml")
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte(data), 0o644))
			_, err = store.Load("repo", "task")
			require.Error(t, err)
			_, err = store.Save(work.State{RepositoryID: "repo", TaskID: "task"}, 0)
			require.Error(t, err, "save must not treat a rejected file as missing")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, data, string(after))
		})
	}
	for _, id := range []string{"", "..", "a/b", `a\b`, " spaced ", "a\x00b"} {
		_, err := store.Path("repo", id)
		require.Error(t, err)
	}
}

func newStore(t *testing.T) (workstate.Store, state.Paths) {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
	return workstate.NewStore(paths), paths
}

func when(minute int) time.Time { return time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC) }
func number(n int) *int         { return &n }

func effort(summary, revision string, start int) *work.ImplementationPhase {
	return &work.ImplementationPhase{
		Execution:  work.AgentExecution{Profile: "implementer", Harness: "pi", Model: "example", Thinking: "high", Interactive: true, Command: "pi", Args: []string{"--task"}, StartedAt: when(start), Process: &work.ExitedProcess{At: when(start + 2), Code: number(0)}, Session: &work.Session{ID: "native-session", LogPath: "/fixture/session.jsonl"}, Launch: &work.FreshExecution{}, SupervisorPID: 123, ChildPID: 456, Duration: 2 * time.Minute, Usage: &work.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}, Cost: &work.UsageCost{Kind: "estimated", Currency: "USD", AmountMicroUSD: 42, Source: "rates", Pricing: &work.UsagePricing{Model: "example", InputUSDPerMillionTokens: "0.1"}}, UsageCapture: work.UsageCapture{Status: work.UsageCaptured, CapturedAt: timePointer(when(start + 3))}},
		Completion: &work.Completion{Summary: summary, Description: "Effort rationale", DetailedDescription: "# Completion\n\nExact handoff\n", TechnicalExplanation: "Implementation rationale", CompletedAt: when(start + 1)},
		Output:     &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: revision, Tree: "tree-" + revision}, RetainedRef: "refs/orpheus/revisions/" + revision},
	}
}

func timePointer(at time.Time) *time.Time { return &at }

func preparation() *work.PreparationPhase {
	return &work.PreparationPhase{EstablishedAt: timePointer(when(0)), Context: &work.WorkContext{Directory: "/fixture/work", TaskBranch: "task-branch", CheckpointRef: "refs/orpheus/checkpoint/task", Baseline: work.CodeIdentity{Commit: "B", Tree: "tree-B"}}}
}

func review(output, start int) *work.ReviewPhase {
	return &work.ReviewPhase{StartedAt: when(start), Scope: work.ReviewScope{Requirements: "Feature requirements", Output: work.OutputRef{Phase: output}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "D"}}, Steps: []work.ReviewStep{&work.CheckStep{Slot: 0, Result: &work.CheckResult{ExitCode: number(0), FinishedAt: when(start + 1)}}, &work.AgentReviewStep{Slot: 1, Execution: effort("review", "unused", start+2).Execution, Report: &work.ReviewReport{Evidence: "Inspected full task"}}, &work.ManualReviewStep{Slot: 2, Decision: &work.ApprovedReview{At: when(start + 5)}}}}
}

func retainedHistory(t *testing.T) work.State {
	t.Helper()
	contract := work.ReviewContract{PipelineKey: "default", Slots: []work.ReviewSlot{{Kind: work.ReviewCheck, ConfigKey: "tests"}, {Kind: work.ReviewAgent, ConfigKey: "security"}, {Kind: work.ReviewManual, ConfigKey: "operator"}}}
	first := review(1, 10)
	first.Steps = first.Steps[:2]
	first.Steps[1].(*work.AgentReviewStep).Report.Findings = []work.Finding{{Title: "Unsafe input", Description: "Validate the input", Classification: work.FindingBlocking, Dispositions: []work.FindingDisposition{&work.RepairRequired{}}}}
	repair := effort("fix: repair input validation", "R2", 20)
	repair.Input = &work.OutputRef{Phase: 1}
	repair.TargetFindings = []work.FindingRef{{Phase: 2, Step: 1, Finding: 0}}
	repair.Execution.Launch = &work.ResumedExecution{From: work.ExecutionRef{Phase: 1}, UsageBaseline: &work.Usage{TotalTokens: 150}}
	repair.Execution.Usage = nil
	repair.Execution.UsageCapture = work.UsageCapture{Status: work.UsageUnknown, Reason: "Incremental counters unavailable"}
	second := review(3, 30)
	second.Scope.DeltaFrom = &work.OutputRef{Phase: 1}
	second.Steps[1].(*work.AgentReviewStep).Report.Verifications = []work.FindingVerification{{Finding: work.FindingRef{Phase: 2, Step: 1, Finding: 0}, Resolution: work.FindingResolved, Evidence: "Validated malicious input"}}
	pub := &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: 4}, Target: &work.PullRequestTarget{}, Content: "Original feature description"}}
	original := work.Workflow{ReviewContracts: []work.ReviewContract{contract}, Phases: []work.Phase{preparation(), effort("feat: original intent", "R1", 1), first, repair, second, pub}}
	s := work.State{RepositoryID: "repo", TaskID: "task", Workflows: []work.Workflow{original}}
	require.NoError(t, s.AuthorizePublication(0, work.PublicationRef{Phase: 5}, when(40), "Operator explicitly authorizes PR publication"))
	pub.Delivery = &work.PullRequestDelivery{PullRequest: work.PullRequestRef{URL: "https://example.test/pull/1"}, PublicCommit: "P2", Head: "P2", Tree: "tree-R2", ObservedAt: when(41), Evidence: "Verified provider and remote head"}
	maintenance := &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: 5}, BranchSyncs: []work.BranchSync{{StartedAt: when(42), Checkpoint: work.BranchSyncCheckpoint{LocalHead: "P2", RemoteHead: "P2", MergeSource: "D2"}, LocalCommit: &work.BranchSyncCommit{Head: "S1", ObservedAt: when(43)}, Push: &work.BranchSyncPush{Head: "S1", ObservedAt: when(44), Evidence: "Verified remote ref"}}}}
	unchanged := effort("fix: unchanged assessment", "unused", 45)
	unchanged.Input = &work.OutputRef{Phase: 3}
	unchanged.Output = &work.ReusedOutput{Input: work.OutputRef{Phase: 3}}
	s.Workflows[0].Phases = append(s.Workflows[0].Phases, maintenance, unchanged)
	direct := &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: 2}, Target: &work.DirectMergeTarget{}, Content: "Second workflow publication"}}
	s.Workflows = append(s.Workflows, work.Workflow{ReviewContracts: []work.ReviewContract{contract}, Phases: []work.Phase{preparation(), effort("feat: second workflow", "R3", 50), review(1, 53), direct}})
	require.NoError(t, s.AuthorizePublication(1, work.PublicationRef{Phase: 3}, when(60), "Operator explicitly authorizes direct delivery"))
	direct.Delivery = &work.DirectDelivery{PublicCommit: "P3", Destination: work.CodeIdentity{Commit: "M3", Tree: "tree-R3"}, ObservedAt: when(61), Evidence: "Verified destination ref"}
	s.Workflows[1].Phases = append(s.Workflows[1].Phases, &work.FinalizationPhase{Acceptance: &work.DirectAcceptance{Publication: work.PublicationRef{Phase: 3}}, Closure: &work.TaskClosure{At: when(62), Reason: "delivered", Evidence: "Source confirmed closure"}, Cleanup: []work.CleanupObservation{&work.WorktreeRetained{Path: "/fixture/work", At: when(63), Reason: "Dirty files", Evidence: "Cleanup refused"}, &work.WorktreeRemoved{Path: "/fixture/work", At: when(64), Evidence: "Removed"}}})
	s.Workflows = append(s.Workflows, work.Workflow{})
	require.NoError(t, s.Validate())
	return s
}
