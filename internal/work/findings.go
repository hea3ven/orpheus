package work

import "time"

func (f *Finding) blockingAt(cutoff time.Time) bool {
	if f.Classification == FindingBlocking {
		return true
	}
	for _, disposition := range f.Dispositions {
		if promoted, ok := disposition.(*PromotedFinding); ok && promoted != nil && observedBy(promoted.At, cutoff) {
			return true
		}
	}
	return false
}

// RepairPhases derives reverse navigation from the targets owned by each effort.
// A pending repair is retained even before output capture.
func (w *Workflow) RepairPhases(ref FindingRef) []int {
	var result []int
	for i, phase := range w.Phases {
		p, ok := phase.(*ImplementationPhase)
		if !ok || p == nil {
			continue
		}
		for _, target := range p.TargetFindings {
			if sameFinding(target, ref) {
				result = append(result, i)
				break
			}
		}
	}
	return result
}
