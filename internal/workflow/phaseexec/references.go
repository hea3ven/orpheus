package phaseexec

import (
	"encoding/hex"
	"fmt"

	"github.com/hea3ven/orpheus/internal/work"
)

func RevisionNamespace(ref work.AttemptRef) string {
	// Hex encodes external repository/task components without inventing another
	// identity. Positions remain the stable parent/index workflow addresses.
	return fmt.Sprintf("refs/orpheus/revisions/%s/%s/%d/%d", hex.EncodeToString([]byte(ref.RepositoryID)), hex.EncodeToString([]byte(ref.TaskID)), ref.Workflow, ref.Phase)
}

func PublicationInputNamespace(ref work.AttemptRef) string {
	// Immutable commit-keyed refs are shared by publication plans in one workflow.
	// Retention does not depend on which plan occurrence is awaiting a decision.
	return fmt.Sprintf("refs/orpheus/publication-inputs/%s/%s/%d", hex.EncodeToString([]byte(ref.RepositoryID)), hex.EncodeToString([]byte(ref.TaskID)), ref.Workflow)
}
