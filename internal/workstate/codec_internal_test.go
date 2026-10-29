package workstate

import (
	"reflect"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRequiredAddressesCannotDecodeAbsentNullOrNonIntegerAsZero(t *testing.T) {
	for _, tc := range []struct {
		value  any
		fields []string
	}{
		{work.OutputRef{}, []string{"phase"}},
		{work.ReviewRef{}, []string{"phase"}},
		{work.PublicationRef{}, []string{"phase"}},
		{work.ExecutionRef{}, []string{"phase"}},
		{work.FindingRef{}, []string{"phase", "step", "finding"}},
		{work.AttemptRef{}, []string{"workflow", "phase"}},
		{work.ReviewPhase{}, []string{"contract"}},
		{work.CheckStep{}, []string{"slot"}},
		{work.AgentReviewStep{}, []string{"slot"}},
		{work.ManualReviewStep{}, []string{"slot"}},
		{work.PRAcceptance{}, []string{"external_review"}},
	} {
		t.Run(reflect.TypeOf(tc.value).Name(), func(t *testing.T) {
			for _, required := range tc.fields {
				for _, invalid := range []string{"omitted", "null", "0.0", `"0"`} {
					t.Run(required+"/"+invalid, func(t *testing.T) {
						node := invalidAddressNode(t, tc.fields, required, invalid)
						into := reflect.New(reflect.TypeOf(tc.value)).Elem()
						require.ErrorContains(t, decodeValue(&node, into), "requires an explicit integer")
					})
				}
			}
			zero, err := encodeValue(reflect.ValueOf(tc.value))
			require.NoError(t, err)
			into := reflect.New(reflect.TypeOf(tc.value)).Elem()
			require.NoError(t, decodeValue(zero, into), "explicit zero is a real address")
			assert.Equal(t, tc.value, into.Interface())
		})
	}
}

func TestRequiredWholeReferencesCannotDecodeOmissionAsZeroAddress(t *testing.T) {
	for _, tc := range []struct {
		value any
		field string
	}{
		{work.DirectAcceptance{}, "publication"},
		{work.ExternalReviewPhase{}, "publication"},
		{work.ReusedOutput{}, "input"},
		{work.ResumedExecution{}, "from"},
		{work.PublicationPlan{}, "review"},
		{work.ReviewScope{}, "output"},
	} {
		t.Run(reflect.TypeOf(tc.value).Name(), func(t *testing.T) {
			for _, text := range []string{"{}", "{" + tc.field + ": null}"} {
				var node yaml.Node
				require.NoError(t, yaml.Unmarshal([]byte(text), &node))
				into := reflect.New(reflect.TypeOf(tc.value)).Elem()
				require.Error(t, decodeValue(node.Content[0], into))
			}
		})
	}
}

func invalidAddressNode(t *testing.T, fields []string, required, invalid string) yaml.Node {
	t.Helper()
	values := make(map[string]any)
	for _, name := range fields {
		values[name] = 0
	}
	var node yaml.Node
	require.NoError(t, node.Encode(values))
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value != required {
			continue
		}
		if invalid == "omitted" {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return node
		}
		var replacement yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(invalid), &replacement))
		node.Content[i+1] = replacement.Content[0]
		return node
	}
	t.Fatalf("address fixture has no field %q", required)
	return node
}

func TestClosedAlternativesKeepTheirKindsAndRejectUnknownOrWrongFamilies(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		kind   string
		value  any
		family reflect.Type
	}{
		{"preparation", &work.PreparationPhase{}, reflect.TypeFor[work.Phase]()},
		{"implementation", &work.ImplementationPhase{Execution: work.AgentExecution{Process: &work.RunningProcess{}}}, reflect.TypeFor[work.Phase]()},
		{"review", &work.ReviewPhase{}, reflect.TypeFor[work.Phase]()},
		{"publication", &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: 2}, Target: &work.PullRequestTarget{ExpectedHead: "P1"}}}, reflect.TypeFor[work.Phase]()},
		{"external_review", &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: 3}}, reflect.TypeFor[work.Phase]()},
		{"finalization", &work.FinalizationPhase{Acceptance: &work.PRAcceptance{ExternalReview: 4, ObservedAt: at, Evidence: "Provider confirmed merge"}}, reflect.TypeFor[work.Phase]()},
		{"check", &work.CheckStep{Slot: 2, Waiver: &work.Waiver{At: at, Reason: "Known failure"}}, reflect.TypeFor[work.ReviewStep]()},
		{"agent_review", &work.AgentReviewStep{Slot: 1, Execution: work.AgentExecution{Process: &work.InterruptedProcess{At: at, Reason: "Supervisor lost", Trigger: "recovery"}}}, reflect.TypeFor[work.ReviewStep]()},
		{"manual_review", &work.ManualReviewStep{Slot: 0, Findings: []work.Finding{{Title: "Problem", Classification: work.FindingBlocking, Description: "Details", Dispositions: []work.FindingDisposition{&work.RepairRequired{}}}}, Decision: &work.AbortedReview{At: at, Reason: "Operator stopped"}}, reflect.TypeFor[work.ReviewStep]()},
		{"approved", &work.ApprovedReview{At: at}, reflect.TypeFor[work.ManualAssessment]()},
		{"requested_changes", &work.RequestedChanges{At: at}, reflect.TypeFor[work.ManualAssessment]()},
		{"aborted", &work.AbortedReview{At: at, Reason: "Stopped"}, reflect.TypeFor[work.ManualAssessment]()},
		{"running", &work.RunningProcess{}, reflect.TypeFor[work.ProcessResult]()},
		{"exited", &work.ExitedProcess{At: at}, reflect.TypeFor[work.ProcessResult]()},
		{"interrupted", &work.InterruptedProcess{At: at, Reason: "Unknown outcome", Trigger: "inspect"}, reflect.TypeFor[work.ProcessResult]()},
		{"start_failed", &work.StartFailedProcess{At: at, Reason: "No executable"}, reflect.TypeFor[work.ProcessResult]()},
		{"fresh", &work.FreshExecution{FallbackReason: "No matching session"}, reflect.TypeFor[work.ExecutionLaunch]()},
		{"resumed", &work.ResumedExecution{From: work.ExecutionRef{Phase: 1}}, reflect.TypeFor[work.ExecutionLaunch]()},
		{"code", &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: "R1", Tree: "T1"}, RetainedRef: "refs/orpheus/R1"}, reflect.TypeFor[work.Output]()},
		{"reused", &work.ReusedOutput{Input: work.OutputRef{Phase: 1}}, reflect.TypeFor[work.Output]()},
		{"repair_required", &work.RepairRequired{}, reflect.TypeFor[work.FindingDisposition]()},
		{"promoted", &work.PromotedFinding{At: at}, reflect.TypeFor[work.FindingDisposition]()},
		{"waived", &work.WaivedFinding{Waiver: work.Waiver{At: at, Reason: "Accept risk"}}, reflect.TypeFor[work.FindingDisposition]()},
		{"dismissed", &work.DismissedFinding{Waiver: work.Waiver{At: at, Reason: "Not applicable"}}, reflect.TypeFor[work.FindingDisposition]()},
		{"downgraded", &work.DowngradedFinding{Waiver: work.Waiver{At: at, Reason: "Advisory"}}, reflect.TypeFor[work.FindingDisposition]()},
		{"addressed", &work.AddressedFinding{Waiver: work.Waiver{At: at, Reason: "Fixed manually"}}, reflect.TypeFor[work.FindingDisposition]()},
		{"pull_request_target", &work.PullRequestTarget{Existing: &work.PullRequestRef{URL: "https://example.test/pull/1"}, ExpectedHead: "P1"}, reflect.TypeFor[work.PublicationTarget]()},
		{"direct_merge_target", &work.DirectMergeTarget{}, reflect.TypeFor[work.PublicationTarget]()},
		{"pull_request_delivery", &work.PullRequestDelivery{Head: "P1", Tree: "T1", ObservedAt: at, Evidence: "Verified"}, reflect.TypeFor[work.Delivery]()},
		{"direct_delivery", &work.DirectDelivery{Destination: work.CodeIdentity{Commit: "M1", Tree: "T1"}, ObservedAt: at, Evidence: "Remote confirmed"}, reflect.TypeFor[work.Delivery]()},
		{"pr_acceptance", &work.PRAcceptance{ExternalReview: 4, ObservedAt: at, Evidence: "Merged"}, reflect.TypeFor[work.Acceptance]()},
		{"direct_acceptance", &work.DirectAcceptance{Publication: work.PublicationRef{Phase: 3}}, reflect.TypeFor[work.Acceptance]()},
		{"worktree_removed", &work.WorktreeRemoved{Path: "/fixture/work", At: at, Evidence: "Removed"}, reflect.TypeFor[work.CleanupObservation]()},
		{"worktree_retained", &work.WorktreeRetained{Path: "/fixture/work", At: at, Reason: "Dirty", Evidence: "Refused"}, reflect.TypeFor[work.CleanupObservation]()},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			encoded, err := encodeAlternative(reflect.ValueOf(tc.value))
			require.NoError(t, err)
			require.Equal(t, "kind", encoded.Content[0].Value)
			assert.Equal(t, tc.kind, encoded.Content[1].Value)
			into := reflect.New(tc.family).Elem()
			require.NoError(t, decodeValue(encoded, into))
			assert.Equal(t, tc.value, into.Interface())
			encoded.Content[1].Value = "unknown_kind"
			require.ErrorContains(t, decodeValue(encoded, into), "unsupported")
			encoded.Content[1].Value = "worktree_removed"
			if tc.family != reflect.TypeFor[work.CleanupObservation]() {
				require.ErrorContains(t, decodeValue(encoded, into), "unsupported")
			}
		})
	}
}

func TestWorkflowCodecRejectsAmbiguousMappingsAndPreservesOptionalIndexes(t *testing.T) {
	for _, text := range []string{
		"{phase: 0, phase: 1}",
		"{phase: 0, unknown: value}",
		"{phase: 0, step: -1, branch_sync: invalid}",
		"{phase: &alias 0, step: *alias}",
	} {
		var doc yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(text), &doc))
		var into work.ExecutionRef
		require.Error(t, decodeValue(doc.Content[0], reflect.ValueOf(&into).Elem()))
	}
	for _, text := range []string{"{kind: running}", "{kind: running, value: null}", "{kind: running, value: {}, ignored: true}"} {
		var doc yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(text), &doc))
		var into work.ProcessResult
		require.Error(t, decodeValue(doc.Content[0], reflect.ValueOf(&into).Elem()))
	}
	for _, tc := range []struct {
		text    string
		present bool
	}{{"{phase: 0}", false}, {"{phase: 0, step: null}", false}, {"{phase: 0, step: 0}", true}} {
		var doc yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(tc.text), &doc))
		var into work.ExecutionRef
		require.NoError(t, decodeValue(doc.Content[0], reflect.ValueOf(&into).Elem()))
		if tc.present {
			require.NotNil(t, into.Step)
			assert.Zero(t, *into.Step)
		} else {
			assert.Nil(t, into.Step)
		}
		assert.Nil(t, into.BranchSync)
	}
}
