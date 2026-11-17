package review

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/hea3ven/orpheus/internal/work"
)

// UnmarshalJSON requires every finding index, including explicit zero indexes.
func (decision *FindingDecision) UnmarshalJSON(data []byte) error {
	var wire struct {
		Ref    *struct{ Phase, Step, Finding *int }
		Action *DecisionAction
		Reason string
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.Ref == nil || wire.Ref.Phase == nil || wire.Ref.Step == nil || wire.Ref.Finding == nil || wire.Action == nil {
		return fmt.Errorf("finding response requires explicit phase, step, finding, and action")
	}
	*decision = FindingDecision{Ref: work.FindingRef{Phase: *wire.Ref.Phase, Step: *wire.Ref.Step, Finding: *wire.Ref.Finding}, Action: *wire.Action, Reason: wire.Reason}
	return nil
}
