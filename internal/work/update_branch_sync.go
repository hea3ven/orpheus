package work

func (g *changeGuard) branchSync(before, after *BranchSync, mutable bool) {
	g.same(before.StartedAt, after.StartedAt, "branch sync start")
	g.same(before.Checkpoint, after.Checkpoint, "branch sync checkpoint")
	g.conflictResolution(before.ConflictResolution, after.ConflictResolution, mutable && before.LocalCommit == nil)
	if mutable {
		g.assigned(before.LocalCommit, after.LocalCommit, "branch sync local commit")
		g.assigned(before.Push, after.Push, "branch sync push")
		g.assigned(before.Rollback, after.Rollback, "branch sync rollback")
		return
	}
	g.same(before.LocalCommit, after.LocalCommit, "settled branch sync local commit")
	g.same(before.Push, after.Push, "settled branch sync push")
	g.same(before.Rollback, after.Rollback, "settled branch sync rollback")
}

func (g *changeGuard) conflictResolution(before, after *ConflictResolution, mutable bool) {
	if before == nil {
		if !mutable {
			g.same(before, after, "branch sync conflict resolution")
		}
		return
	}
	if after == nil {
		g.same(before, after, "branch sync conflict resolution")
		return
	}
	g.same(before.Files, after.Files, "branch sync conflict files")
	g.execution(before.Execution, after.Execution)
}
