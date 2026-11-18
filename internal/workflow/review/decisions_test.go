package review_test

import (
	"encoding/json"
	"testing"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindingDecisionJSONRequiresExplicitContainment(t *testing.T) {
	var decision review.FindingDecision
	require.NoError(t, json.Unmarshal([]byte(`{"Ref":{"Phase":2,"Step":0,"Finding":0},"Action":"waive","Reason":"Accepted risk"}`), &decision))
	assert.Equal(t, work.FindingRef{Phase: 2, Step: 0, Finding: 0}, decision.Ref)
	assert.Equal(t, review.DecisionWaive, decision.Action)
	assert.Equal(t, "Accepted risk", decision.Reason)
	for _, input := range []string{`{"Ref":{"Phase":2,"Step":0},"Action":"waive"}`, `{"Ref":{"Phase":2,"Step":0,"Finding":null},"Action":"waive"}`, `{"Ref":{"Phase":2,"Step":0,"Finding":0}}`, `{"Ref":{"Phase":2,"Step":0,"Finding":0},"Action":"waive","extra":true}`} {
		assert.Error(t, json.Unmarshal([]byte(input), &decision))
	}
}
