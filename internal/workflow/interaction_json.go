package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/hea3ven/orpheus/internal/work"
)

// UnmarshalJSON refuses omitted/null required containment indexes. Zero is a
// real owner, not a fallback for an incomplete machine response.
func (id *InteractionID) UnmarshalJSON(data []byte) error {
	var wire struct {
		Owner *struct {
			RepositoryID *string
			TaskID       *string
			Workflow     *int
			Phase        *int
			Step         *int
			BranchSync   *int
		}
		Version *work.StateVersion
		Kind    *InteractionKind
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.Owner == nil || wire.Owner.RepositoryID == nil || wire.Owner.TaskID == nil || wire.Owner.Workflow == nil || wire.Owner.Phase == nil || wire.Version == nil || wire.Kind == nil {
		return fmt.Errorf("interaction identity requires explicit owner, repository, task, workflow, phase, version, and kind")
	}
	*id = InteractionID{Owner: work.AttemptRef{RepositoryID: *wire.Owner.RepositoryID, TaskID: *wire.Owner.TaskID, Workflow: *wire.Owner.Workflow, Phase: *wire.Owner.Phase, Step: wire.Owner.Step, BranchSync: wire.Owner.BranchSync}, Version: *wire.Version, Kind: *wire.Kind}
	return nil
}
