package work

func (v *validator) execution(e *AgentExecution, active bool) {
	v.check(!e.StartedAt.IsZero(), "execution requires observed start time")
	switch process := e.Process.(type) {
	case *RunningProcess:
		v.check(process != nil, "process result is nil")
		v.check(active, "running execution must belong to the active last effort")
	case *ExitedProcess:
		v.check(process != nil && process.Code != nil && !process.At.IsZero() && !process.At.Before(e.StartedAt), "exit requires explicit code and observed finish time after start")
	case *InterruptedProcess:
		v.check(process != nil && process.Reason != "" && !process.At.IsZero() && !process.At.Before(e.StartedAt), "interruption requires reason and observed time")
	case *StartFailedProcess:
		v.check(process != nil && process.Reason != "" && !process.At.IsZero() && !process.At.Before(e.StartedAt), "start failure requires reason and observed time")
	default:
		v.check(false, "explicit process result is required")
	}
	v.check(e.SupervisorPID >= 0 && e.ChildPID >= 0, "native process IDs cannot be negative")
	v.check(e.Duration >= 0, "execution duration cannot be negative")
	v.usage(e.Usage)
	if e.Cost != nil {
		v.check(e.Cost.AmountMicroUSD >= 0, "usage cost cannot be negative")
	}
	c := e.UsageCapture
	v.check(c.Status == "" || c.Status == UsageCaptured || c.Status == UsageUnknown || c.Status == UsageAmbiguous, "invalid usage capture status")
	v.check((c.Status != UsageUnknown && c.Status != UsageAmbiguous) || c.Reason != "", "unknown or ambiguous usage requires a reason")
	v.check(c.CandidateCount >= 0, "usage candidate count cannot be negative")
	v.launch(e)
}

func (v *validator) usage(u *Usage) {
	if u != nil {
		v.check(u.InputTokens >= 0 && u.OutputTokens >= 0 && u.CachedInputTokens >= 0 && u.ReasoningOutputTokens >= 0 && u.TotalTokens >= 0, "usage measurements cannot be negative")
	}
}

func (v *validator) launch(e *AgentExecution) {
	switch launch := e.Launch.(type) {
	case nil:
	case *FreshExecution:
		v.check(launch != nil, "fresh launch is nil")
	case *ResumedExecution:
		if launch == nil {
			v.check(false, "resumed launch is nil")
			return
		}
		earlier := launch.From.Phase >= 0 && launch.From.Phase < v.phase
		if launch.From.Phase == v.phase {
			if launch.From.Step != nil && v.step >= 0 {
				earlier = *launch.From.Step >= 0 && *launch.From.Step < v.step
			}
			if launch.From.BranchSync != nil && v.sync >= 0 {
				earlier = *launch.From.BranchSync >= 0 && *launch.From.BranchSync < v.sync
			}
		}
		v.check(earlier, "resumed execution requires an earlier source effort")
		source, err := v.w.Execution(launch.From)
		v.check(err == nil, "resumed source execution: %v", err)
		if err == nil {
			v.check(successful(source) && source.Session != nil && source.Session.ID != "", "resumption requires successful source and native session")
			v.check(e.Harness == source.Harness, "resumption must use the source harness")
			v.check(e.Session == nil || source.Session == nil || e.Session.ID == source.Session.ID, "resumed native session must match source")
		}
		v.usage(launch.UsageBaseline)
		v.check(launch.CostBaselineMicroUSD == nil || *launch.CostBaselineMicroUSD >= 0, "resumption cost baseline cannot be negative")
	default:
		v.check(false, "invalid launch alternative")
	}
}
