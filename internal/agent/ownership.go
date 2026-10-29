package agent

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hea3ven/orpheus/internal/work"
)

// OwnerEnvironment carries explicit zero-based ownership across a child boundary.
func OwnerEnvironment(ref work.AttemptRef) []string {
	env := []string{fmt.Sprintf("ORPHEUS_WORKFLOW=%d", ref.Workflow), fmt.Sprintf("ORPHEUS_PHASE=%d", ref.Phase)}
	if ref.Step != nil {
		env = append(env, fmt.Sprintf("ORPHEUS_STEP_INDEX=%d", *ref.Step))
	}
	if ref.BranchSync != nil {
		env = append(env, fmt.Sprintf("ORPHEUS_BRANCH_SYNC_INDEX=%d", *ref.BranchSync))
	}
	return env
}

func (r ActiveContextResolver) ownerReference(repoID, taskID string) (work.AttemptRef, error) {
	ref := work.AttemptRef{RepositoryID: repoID, TaskID: taskID}
	var err error
	ref.Workflow, err = r.requiredIndex("ORPHEUS_WORKFLOW")
	if err != nil {
		return ref, err
	}
	ref.Phase, err = r.requiredIndex("ORPHEUS_PHASE")
	if err != nil {
		return ref, err
	}
	for _, field := range []struct {
		name   string
		target **int
	}{
		{"ORPHEUS_STEP_INDEX", &ref.Step}, {"ORPHEUS_BRANCH_SYNC_INDEX", &ref.BranchSync},
	} {
		if strings.TrimSpace(r.envValue(field.name)) == "" {
			continue
		}
		index, err := r.requiredIndex(field.name)
		if err != nil {
			return ref, err
		}
		*field.target = &index
	}
	if ref.Step != nil && ref.BranchSync != nil {
		return ref, fmt.Errorf("a child cannot own both review and branch-sync work")
	}
	return ref, nil
}

func (r ActiveContextResolver) requiredIndex(name string) (int, error) {
	raw, err := r.requiredEnv(name)
	if err != nil {
		return 0, err
	}
	index, err := strconv.Atoi(raw)
	if err != nil || index < 0 {
		return 0, fmt.Errorf("%s must be an explicit non-negative index", name)
	}
	return index, nil
}
