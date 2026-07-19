package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//nolint:funlen // End-to-end scenario is clearer when the workflow remains linear.
func TestWorktreeCompletionFlowEndToEnd(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths, repoPath := setupCompletionFlowRepo(t)

	const taskID = "op-worktree-completion"
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", taskID))
	must.NoError(err)
	bd := withStatefulCompletionBD(t, completionBDTask{
		RepoPath:           repoPath,
		TaskID:             taskID,
		Title:              "Worktree completion flow",
		Description:        "Validate the team worktree completion path.",
		AcceptanceCriteria: "The agent completion is committed and waits for PR creation.",
	})
	withOrpheusCLIHelper(t)
	agentLogPath := withCompletionFlowAgent(t, completionFlowAgentOptions{
		Command:              "worktree-completion-agent",
		FileName:             "worktree-change.txt",
		Body:                 "worktree implementation",
		Summary:              "Implement worktree completion flow",
		Description:          "Created a worktree validation change.",
		DetailedDescription:  "## Worktree completion\n\nCreated a worktree validation change.",
		TechnicalExplanation: "Technical explanation.",
	})
	writeCompletionFlowAgentConfig(t, paths, "worktree-completion", "worktree-completion-agent")

	stdout, stderr := executeCommand(t, []string{"task", "run", taskID})

	is.Contains(stdout, "completion agent completed")
	is.Contains(stderr, "Review for "+taskID+" is waiting for manual step \"local-review\"")
	is.Contains(stderr, "Resume with `orpheus task review "+taskID+"`")

	agentLog := readFileString(t, agentLogPath)
	prompt := agentLogBlock(t, agentLog, "ORPHEUS_AGENT_PROMPT")
	is.Equal(agent.RenderBootstrapPrompt(), prompt)
	is.Equal(agent.RenderBootstrapPrompt(), agentLogBlock(t, agentLog, "ARG_2"))
	is.Contains(prompt, "Run `orpheus agent context` now")
	is.NotContains(prompt, "Task:")
	is.NotContains(prompt, "Repository:")
	is.NotContains(prompt, "Worktree completion flow")

	contextOutput := agentLogBlock(t, agentLog, "AGENT_CONTEXT")
	for _, want := range []string{
		"# Orpheus Agent Context",
		"- ID: " + taskID,
		"- Title: Worktree completion flow",
		"- Workflow: worktree/team",
		"- Branch: orpheus/" + taskID,
		"- Path: " + worktreePath,
		"- Current directory: " + worktreePath,
		"deterministic task worktree and task branch",
		"PR-ready completion data for feature-branch publication",
		"The human operator will later run `orpheus task review " + taskID + "` to review and publish the feature branch as a pull request",
	} {
		is.Contains(contextOutput, want)
	}

	state := readCompletionTaskState(t, paths, "alpha", taskID)
	must.Len(state.Runs, 1)
	latest := state.Runs[0]
	is.Equal(taskstate.RunStatusSucceeded, latest.Status)
	must.NotNil(latest.Execution.FinishedAt)
	must.NotNil(latest.Completion)
	is.Equal("Implement worktree completion flow", latest.Completion.Summary)
	is.Equal("Created a worktree validation change.", latest.Completion.Description)
	is.Equal("## Worktree completion\n\nCreated a worktree validation change.", latest.Completion.DetailedDescription)
	is.False(latest.Completion.CompletedAt.IsZero())
	is.Empty(latest.Completion.Commit)
	is.Contains(strings.TrimSpace(runGit(t, worktreePath, "status", "--porcelain=v1")), "worktree-change.txt")

	statusOut, statusErr := executeCommand(t, []string{"status"})
	is.Empty(statusErr)
	is.Contains(statusOut, "Reviewing")
	is.Contains(statusOut, taskID)
	is.Contains(statusOut, "local review; run task review")
	is.NotContains(statusOut, "https://")

	is.Equal("in_progress", strings.TrimSpace(readFileString(t, bd.StatusPath)))
	bdLog := readFileString(t, bd.LogPath)
	is.NotContains(bdLog, "close "+taskID)
	is.NotContains(bdLog, "orpheus.pr_url")
}

//nolint:funlen // End-to-end scenario is clearer when the workflow remains linear.
func TestConfiguredPublicationPolicyEndToEnd(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths, repoPath := setupCompletionFlowRepo(t)

	const taskID = "op-trex-title"
	withStatefulCompletionBD(t, completionBDTask{
		RepoPath:           repoPath,
		TaskID:             taskID,
		Title:              "Configured publication policy",
		Description:        "Validate the configured work-repo title workflow.",
		AcceptanceCriteria: "Publication uses the task external reference and capitalized summary.",
		ExternalRef:        "TREX-1234",
	})
	withOrpheusCLIHelper(t)
	agentLogPath := withCompletionFlowAgent(t, completionFlowAgentOptions{
		Command:              "trex-title-agent",
		FileName:             "trex-title-change.txt",
		Body:                 "configured publication implementation",
		Summary:              "Replaced the config for abc",
		Description:          "Replaced the config used for publication validation.",
		DetailedDescription:  "## Configured publication\n\nReplaced the config used for publication validation.",
		TechnicalExplanation: "Technical explanation.",
	})
	writeCompletionFlowAgentConfig(t, paths, "trex-title", "trex-title-agent")

	for _, args := range [][]string{
		{"repo", "config", "set", "alpha", "summary-style", registry.SummaryGuidanceStyleCapitalized},
		{"repo", "config", "set", "alpha", "title-template", "[{{external_ref}}] {{summary}}"},
	} {
		stdout, stderr := executeCommand(t, args)
		is.Empty(stderr)
		is.NotEmpty(stdout)
	}

	runOut, runErr := executeCommand(t, []string{"task", "run", taskID})
	is.Contains(runErr, "Review for "+taskID+" is waiting for manual step \"local-review\"")
	is.Contains(runOut, "completion agent completed")

	contextOutput := agentLogBlock(t, readFileString(t, agentLogPath), "AGENT_CONTEXT")
	is.Contains(contextOutput, "- External reference: TREX-1234")
	is.Contains(contextOutput, "Use one capitalized plain-English summary line")
	is.Contains(contextOutput, "Replaced the config for abc")

	state := readCompletionTaskState(t, paths, "alpha", taskID)
	latest, ok := taskstate.LatestRun(state)
	must.True(ok)
	must.NotNil(latest.Completion)
	target, ok := taskstate.Target(state)
	must.True(ok)
	is.Equal("Replaced the config for abc", latest.Completion.Summary)
	recordPassedReview(t, paths, "alpha", taskID)

	ghLogPath := withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   "[]",
		createStdout: "https://github.test/org/alpha/pull/57\n",
	})
	doneOut, doneErr := executeCommand(t, []string{"task", "done", taskID})
	is.Empty(doneErr)
	is.Contains(doneOut, "created PR https://github.test/org/alpha/pull/57")
	is.Equal(
		"[TREX-1234] Replaced the config for abc\n\nReplaced the config used for publication validation.",
		strings.TrimSpace(runGit(t, target.Worktree, "log", "-1", "--format=%B")),
	)
	is.Contains(readFileString(t, ghLogPath), "[TREX-1234] Replaced the config for abc")
}

//nolint:funlen // End-to-end scenario is clearer when the workflow remains linear.
func TestMissingPublicationExternalReferenceBlocksDispatchAndPublicationEndToEnd(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths, repoPath := setupCompletionFlowRepo(t)

	const taskID = "op-missing-title-ref"
	withStatefulCompletionBD(t, completionBDTask{
		RepoPath:           repoPath,
		TaskID:             taskID,
		Title:              "Missing publication reference",
		Description:        "Validate missing-reference gates.",
		AcceptanceCriteria: "Dispatch and publication require the external reference.",
	})
	withOrpheusCLIHelper(t)
	withCompletionFlowAgent(t, completionFlowAgentOptions{
		Command:              "missing-title-ref-agent",
		FileName:             "missing-title-ref-change.txt",
		Body:                 "missing reference implementation",
		Summary:              "feat: validate missing title reference",
		Description:          "Created a change for missing-reference validation.",
		DetailedDescription:  "## Missing reference\n\nCreated a change for missing-reference validation.",
		TechnicalExplanation: "Technical explanation.",
	})
	writeCompletionFlowAgentConfig(t, paths, "missing-title-ref", "missing-title-ref-agent")

	_, configErr := executeCommand(t, []string{
		"repo", "config", "set", "alpha", "title-template", "[{{external_ref}}] {{summary}}",
	})
	is.Empty(configErr)

	stdout, stderr, runErr := executeCommandWithError(t, []string{"task", "run", taskID})
	must.Error(runErr)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(runErr, "publication title template requires a task external reference")
	is.ErrorContains(runErr, "bd update "+taskID+" --external-ref <reference>")

	worktreePath, pathErr := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", taskID))
	must.NoError(pathErr)
	_, statErr := os.Stat(worktreePath)
	is.ErrorIs(statErr, os.ErrNotExist)

	_, clearErr := executeCommand(t, []string{"repo", "config", "set", "alpha", "title-template", ""})
	is.Empty(clearErr)
	runOut, allowedRunErr := executeCommand(t, []string{"task", "run", taskID})
	is.Contains(allowedRunErr, "Review for "+taskID+" is waiting for manual step \"local-review\"")
	is.Contains(runOut, "completion agent completed")

	state := readCompletionTaskState(t, paths, "alpha", taskID)
	_, ok := taskstate.LatestRun(state)
	must.True(ok)
	target, ok := taskstate.Target(state)
	must.True(ok)
	beforePublication := strings.TrimSpace(runGit(t, target.Worktree, "rev-parse", "HEAD"))
	recordPassedReview(t, paths, "alpha", taskID)

	_, configErr = executeCommand(t, []string{
		"repo", "config", "set", "alpha", "title-template", "[{{external_ref}}] {{summary}}",
	})
	is.Empty(configErr)
	ghLogPath := withFakeGHPRResponses(t, fakeGHPRResponses{})

	doneOut, doneStderr, doneErr := executeCommandWithError(t, []string{"task", "done", taskID})
	is.Empty(doneOut)
	is.Empty(doneStderr)
	must.Error(doneErr)
	is.ErrorContains(doneErr, "publication title template requires a task external reference")
	is.Equal(beforePublication, strings.TrimSpace(runGit(t, target.Worktree, "rev-parse", "HEAD")))
	is.Contains(runGit(t, target.Worktree, "status", "--porcelain=v1"), "missing-title-ref-change.txt")
	_, statErr = os.Stat(ghLogPath)
	is.ErrorIs(statErr, os.ErrNotExist)
}

//nolint:funlen // End-to-end scenario is clearer when the workflow remains linear.
func TestWorktreeLocalReviewTaskDonePRFlowEndToEnd(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths, repoPath := setupCompletionFlowRepo(t)

	const taskID = "op-m5-sync"
	bd := withStatefulCompletionBD(t, completionBDTask{
		RepoPath:           repoPath,
		TaskID:             taskID,
		Title:              "M5 sync flow",
		Description:        "Validate the reviewed PR publication and sync polling path.",
		AcceptanceCriteria: "Task done creates one PR, and sync polls it as in review.",
	})
	withOrpheusCLIHelper(t)
	withCompletionFlowAgent(t, completionFlowAgentOptions{
		Command:              "m5-sync-agent",
		FileName:             "m5-sync-change.txt",
		Body:                 "m5 implementation",
		Summary:              "Implement M5 sync validation",
		Description:          "Created a change for PR sync validation.",
		DetailedDescription:  "## M5 sync\n\nCreated a PR sync validation change.",
		TechnicalExplanation: "Technical explanation.",
	})
	writeCompletionFlowAgentConfig(t, paths, "m5-sync", "m5-sync-agent")

	runOut, runErr := executeCommand(t, []string{"task", "run", taskID})

	is.Contains(runErr, "Review for "+taskID+" is waiting for manual step \"local-review\"")
	is.Contains(runOut, "completion agent completed")
	state := readCompletionTaskState(t, paths, "alpha", taskID)
	latest, ok := taskstate.LatestRun(state)
	must.True(ok)
	must.NotNil(latest.Completion)
	target, ok := taskstate.Target(state)
	must.True(ok)
	completionCommit := latest.Completion.Commit
	is.Empty(completionCommit)
	is.Equal("orpheus/"+taskID, target.Branch)
	is.NotEmpty(target.Worktree)
	recordPassedReview(t, paths, "alpha", taskID)

	ghLogPath := withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   "[]",
		createStdout: "https://github.test/org/alpha/pull/55\n",
		statusStdout: `{"url":"https://github.test/org/alpha/pull/55","state":"OPEN","merged":false}`,
	})

	doneOut, doneErr := executeCommand(t, []string{"task", "done", taskID})

	is.Empty(doneErr)
	is.Contains(doneOut, "Published "+taskID)
	is.Contains(doneOut, "pushed orpheus/"+taskID)
	is.Contains(doneOut, "created PR https://github.test/org/alpha/pull/55")
	is.Contains(doneOut, "Backend task remains open for PR review")
	publicationCommit := strings.TrimSpace(runGit(t, target.Worktree, "rev-parse", "HEAD"))
	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	pushedCommit := strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/orpheus/"+taskID))
	is.Equal(publicationCommit, pushedCommit)

	statusOut, statusErr := executeCommand(t, []string{"status"})

	is.Empty(statusErr)
	is.Contains(statusOut, "Reviewing")
	is.Contains(statusOut, taskID)
	is.Contains(statusOut, "M5 sync flow")
	is.Contains(statusOut, "https://github.test/org/alpha/pull/55")
	is.NotContains(statusOut, "needs PR")

	ghLogBeforeRerun := readFileString(t, ghLogPath)
	rerunOut, rerunErr := executeCommand(t, []string{"task", "sync", taskID})

	is.Empty(rerunErr)
	is.Contains(rerunOut, "PR https://github.test/org/alpha/pull/55 is still open for review")
	ghLogAfterRerun := readFileString(t, ghLogPath)
	is.Equal(0, strings.Count(ghLogBeforeRerun, "ARG_2<<END\nview\nEND"))
	is.Equal(1, strings.Count(ghLogAfterRerun, "ARG_2<<END\nview\nEND"))
	bdLog := readFileString(t, bd.LogPath)
	is.Equal(1, strings.Count(bdLog, "--set-metadata orpheus.pr_url=https://github.test/org/alpha/pull/55"))

	withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   "unexpected list\n",
		listExit:     66,
		createStdout: "unexpected create\n",
		createExit:   66,
		statusStdout: `{"url":"https://github.test/org/alpha/pull/55","state":"MERGED","merged":true}`,
	})

	mergedOut, mergedErr := executeCommand(t, []string{"task", "sync", taskID})

	is.Empty(mergedErr)
	is.Contains(mergedOut, "PR https://github.test/org/alpha/pull/55 is merged")
	is.Contains(mergedOut, "Backend task was closed")
	is.Equal("closed", strings.TrimSpace(readFileString(t, bd.StatusPath)))

	var mergedState taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", taskID+".yaml"), &mergedState))
	must.NotEmpty(mergedState.Events)
	mergedEvent := mergedState.Events[len(mergedState.Events)-1]
	is.Equal(taskstate.EventTaskClosed, mergedEvent.Type)
	is.Equal(taskstate.CloseReasonPRMerged, mergedEvent.CloseReason)
	is.Equal("https://github.test/org/alpha/pull/55", mergedEvent.PRURL)

	fullStatusOut, fullStatusErr := executeCommand(t, []string{"status", "--full"})
	is.Empty(fullStatusErr)
	is.Contains(fullStatusOut, "Done / closed")
	is.Contains(fullStatusOut, taskID)
}

//nolint:funlen // End-to-end scenario is clearer when the workflow remains linear.
func TestRepoRootLocalReviewTaskDonePRFlowEndToEnd(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths, repoPath := setupCompletionFlowRepo(t)

	const taskID = "op-repo-root-sync"
	branch := "orpheus/" + taskID
	bd := withStatefulCompletionBD(t, completionBDTask{
		RepoPath:           repoPath,
		TaskID:             taskID,
		Title:              "Repo-root PR flow",
		Description:        "Validate repo-root feature branch publication and merge sync.",
		AcceptanceCriteria: "Task done publishes a PR, and sync closes the merged task.",
		CloseBranch:        branch,
	})
	withOrpheusCLIHelper(t)
	agentLogPath := withCompletionFlowAgent(t, completionFlowAgentOptions{
		Command:              "repo-root-sync-agent",
		FileName:             "repo-root-sync-change.txt",
		Body:                 "repo-root implementation",
		Summary:              "Implement repo-root PR validation",
		Description:          "Created a repo-root feature-branch validation change.",
		DetailedDescription:  "## Repo-root PR flow\n\nCreated a repo-root feature-branch validation change.",
		TechnicalExplanation: "Technical explanation.",
	})
	writeCompletionFlowAgentConfig(t, paths, "repo-root-sync", "repo-root-sync-agent")

	runOut, runErr := executeCommand(t, []string{"task", "run", "--repo-root", taskID})

	is.Contains(runErr, "Review for "+taskID+" is waiting for manual step \"local-review\"")
	is.Contains(runOut, "completion agent completed")
	is.Equal(branch, strings.TrimSpace(runGit(t, repoPath, "branch", "--show-current")))

	agentLog := readFileString(t, agentLogPath)
	contextOutput := agentLogBlock(t, agentLog, "AGENT_CONTEXT")
	for _, want := range []string{
		"- Workflow: repo-root/team",
		"- Branch: " + branch,
		"- Path: " + repoPath,
		"- Current directory: " + repoPath,
		"registered repository root on the task branch",
	} {
		is.Contains(contextOutput, want)
	}

	dirOut, dirErr := executeCommand(t, []string{"task", "dir", taskID})
	is.Empty(dirErr)
	is.Equal(repoPath+"\n", dirOut)

	state := readCompletionTaskState(t, paths, "alpha", taskID)
	latest, ok := taskstate.LatestRun(state)
	must.True(ok)
	must.NotNil(latest.Completion)
	target, ok := taskstate.Target(state)
	must.True(ok)
	is.Equal(branch, target.Branch)
	is.Equal(repoPath, target.Worktree)
	is.Empty(latest.Completion.Commit)
	is.Contains(runGit(t, repoPath, "status", "--porcelain=v1"), "repo-root-sync-change.txt")
	recordPassedReview(t, paths, "alpha", taskID)

	ghLogPath := withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   "[]",
		createStdout: "https://github.test/org/alpha/pull/56\n",
		statusStdout: `{"url":"https://github.test/org/alpha/pull/56","state":"OPEN","merged":false}`,
	})

	doneOut, doneErr := executeCommand(t, []string{"task", "done", taskID})

	is.Empty(doneErr)
	is.Contains(doneOut, "Published "+taskID)
	is.Contains(doneOut, "pushed "+branch)
	is.Contains(doneOut, "created PR https://github.test/org/alpha/pull/56")
	is.Contains(doneOut, "Backend task remains open for PR review")
	publicationCommit := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))
	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	pushedCommit := strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/"+branch))
	is.Equal(publicationCommit, pushedCommit)
	is.Empty(strings.TrimSpace(runGit(t, repoPath, "status", "--porcelain=v1")))

	bdLog := readFileString(t, bd.LogPath)
	is.Equal(1, strings.Count(bdLog, "--set-metadata orpheus.pr_url=https://github.test/org/alpha/pull/56"))

	openSyncOut, openSyncErr := executeCommand(t, []string{"task", "sync", taskID})
	is.Empty(openSyncErr)
	is.Contains(openSyncOut, "PR https://github.test/org/alpha/pull/56 is still open for review")
	is.Equal(1, strings.Count(readFileString(t, ghLogPath), "ARG_2<<END\nview\nEND"))

	withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   "unexpected list\n",
		listExit:     66,
		createStdout: "unexpected create\n",
		createExit:   66,
		statusStdout: `{"url":"https://github.test/org/alpha/pull/56","state":"MERGED","merged":true}`,
	})

	mergedSyncOut, mergedSyncErr := executeCommand(t, []string{"task", "sync", taskID})

	is.Empty(mergedSyncErr)
	is.Contains(mergedSyncOut, "PR https://github.test/org/alpha/pull/56 is merged")
	is.Contains(mergedSyncOut, "Backend task was closed")
	is.Equal("closed", strings.TrimSpace(readFileString(t, bd.StatusPath)))

	mergedState := readCompletionTaskState(t, paths, "alpha", taskID)
	must.NotEmpty(mergedState.Events)
	mergedEvent := mergedState.Events[len(mergedState.Events)-1]
	is.Equal(taskstate.EventTaskClosed, mergedEvent.Type)
	is.Equal(taskstate.CloseReasonPRMerged, mergedEvent.CloseReason)
	is.Equal("https://github.test/org/alpha/pull/56", mergedEvent.PRURL)
}

//nolint:funlen // End-to-end scenario is clearer when the workflow remains linear.
func TestMainCompletionFlowEndToEnd(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths, repoPath := setupCompletionFlowRepo(t)

	const taskID = "op-main-completion"
	bd := withStatefulCompletionBD(t, completionBDTask{
		RepoPath:           repoPath,
		TaskID:             taskID,
		Title:              "Main completion flow",
		Description:        "Validate the solo main completion path.",
		AcceptanceCriteria: "The agent leaves changes for local review and task done finalizes them.",
	})
	withOrpheusCLIHelper(t)
	agentLogPath := withCompletionFlowAgent(t, completionFlowAgentOptions{
		Command:              "main-completion-agent",
		FileName:             "agent-main-change.txt",
		Body:                 "main implementation",
		Summary:              "Implement main completion flow",
		Description:          "Created a main-mode validation change.",
		DetailedDescription:  "## Main completion\n\nCreated a main-mode validation change.",
		TechnicalExplanation: "Technical explanation.",
	})
	writeCompletionFlowAgentConfig(t, paths, "main-completion", "main-completion-agent")

	stdout, stderr := executeCommand(t, []string{"task", "run", "--main", taskID})

	is.Contains(stdout, "completion agent completed")
	is.Contains(stderr, "Review for "+taskID+" is waiting for manual step \"local-review\"")
	is.Contains(stderr, "Resume with `orpheus task review "+taskID+"`")

	agentLog := readFileString(t, agentLogPath)
	prompt := agentLogBlock(t, agentLog, "ORPHEUS_AGENT_PROMPT")
	is.Equal(agent.RenderBootstrapPrompt(), prompt)
	is.Equal(agent.RenderBootstrapPrompt(), agentLogBlock(t, agentLog, "ARG_2"))
	is.NotContains(prompt, "Main completion flow")

	contextOutput := agentLogBlock(t, agentLog, "AGENT_CONTEXT")
	for _, want := range []string{
		"# Orpheus Agent Context",
		"- ID: " + taskID,
		"- Title: Main completion flow",
		"- Workflow: main/solo",
		"- Branch: main",
		"- Path: " + repoPath,
		"registered repository root on the registered default branch",
		"Orpheus will record local-review-ready completion data",
		"The human operator will later run `orpheus task review " + taskID + "`",
	} {
		is.Contains(contextOutput, want)
	}

	state := readCompletionTaskState(t, paths, "alpha", taskID)
	must.Len(state.Runs, 1)
	latest := state.Runs[0]
	is.Equal(taskstate.RunStatusSucceeded, latest.Status)
	must.NotNil(latest.Execution.FinishedAt)
	must.NotNil(latest.Completion)
	is.Equal("Implement main completion flow", latest.Completion.Summary)
	is.Equal("Created a main-mode validation change.", latest.Completion.Description)
	is.Equal("## Main completion\n\nCreated a main-mode validation change.", latest.Completion.DetailedDescription)
	is.False(latest.Completion.CompletedAt.IsZero())
	is.Empty(latest.Completion.Commit)
	is.Contains(runGit(t, repoPath, "status", "--porcelain=v1"), "agent-main-change.txt")
	is.NotContains(runGit(t, repoPath, "log", "--oneline", "--max-count=1"), "Implement main completion flow")

	statusOut, statusErr := executeCommand(t, []string{"status"})
	is.Empty(statusErr)
	is.Contains(statusOut, "Reviewing")
	is.Contains(statusOut, taskID)
	is.Contains(statusOut, "local review; run task review")

	must.NoError(os.WriteFile(filepath.Join(repoPath, "human-review.txt"), []byte("human reviewed\n"), 0o644))
	recordPassedReview(t, paths, "alpha", taskID)
	doneOut, doneErr := executeCommand(t, []string{"task", "done", taskID})
	is.Empty(doneErr)
	is.Contains(doneOut, "Finalized "+taskID)

	commit := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))
	is.Contains(doneOut, commit)
	is.Equal(
		"Implement main completion flow\n\nCreated a main-mode validation change.",
		strings.TrimSpace(runGit(t, repoPath, "log", "-1", "--format=%B")),
	)
	is.Empty(strings.TrimSpace(runGit(t, repoPath, "status", "--porcelain=v1")))
	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	is.Equal(commit, strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/main")))
	is.Equal("closed", strings.TrimSpace(readFileString(t, bd.StatusPath)))

	finalState := readCompletionTaskState(t, paths, "alpha", taskID)
	facts := taskstate.FinalizationFacts(finalState)
	is.Equal(commit, facts.Commit)
	must.NotNil(facts.CommittedAt)
	must.NotNil(facts.PushedAt)
	must.NotNil(facts.ClosedAt)

	fullStatusOut, fullStatusErr := executeCommand(t, []string{"status", "--full"})
	is.Empty(fullStatusErr)
	is.Contains(fullStatusOut, "Done / closed")
	is.Contains(fullStatusOut, taskID)
	is.Contains(fullStatusOut, "Main completion flow")
}

func TestOrpheusCLIHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_ORPHEUS_CLI_HELPER") != "1" {
		return
	}

	marker := -1
	for i, arg := range os.Args {
		if arg == "--" {
			marker = i
			break
		}
	}
	if marker < 0 {
		_, _ = fmt.Fprintln(os.Stderr, "missing -- before orpheus helper args")
		os.Exit(2)
	}

	command := cli.NewRootCommand()
	command.SetIn(os.Stdin)
	command.SetOut(os.Stdout)
	command.SetErr(os.Stderr)
	command.SetArgs(os.Args[marker+1:])
	if err := command.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type completionBDTask struct {
	RepoPath           string
	TaskID             string
	Title              string
	Description        string
	AcceptanceCriteria string
	ExternalRef        string
	CloseBranch        string
}

type statefulCompletionBD struct {
	LogPath    string
	StatusPath string
}

func setupCompletionFlowRepo(t *testing.T) (state.Paths, string) {
	t.Helper()

	root := newTestState(t)
	paths := currentTestPaths(t)
	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	require.NoError(t, registry.NewStore(paths).Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	return paths, repoPath
}

func writeCompletionFlowAgentConfig(t *testing.T, paths state.Paths, name string, command string) {
	t.Helper()

	require.NoError(t, paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{"implementer": name},
			"profiles": map[string]any{
				name: map[string]any{
					"command": command,
					"args":    []string{"--prompt", "{{prompt}}"},
				},
			},
		},
	}))
}

const statefulCompletionBDScript = `#!/bin/sh
set -eu
{
  pwd
  printf '%%s\n' "$*"
} >> "$FAKE_BD_LOG"

TASK_ID=%s
TITLE=%s
DESCRIPTION=%s
ACCEPTANCE=%s
REPO_PATH=%s
STATUS_FILE=%s
BRANCH_FILE=%s
WORKTREE_FILE=%s
PR_URL_FILE=%s
CLOSE_BRANCH=%s
EXTERNAL_REF=%s

emit_task() {
  status="$(cat "$STATUS_FILE")"
  branch=""
  worktree=""
  pr_url=""
  if [ -f "$BRANCH_FILE" ]; then
    branch="$(cat "$BRANCH_FILE")"
  fi
  if [ -f "$WORKTREE_FILE" ]; then
    worktree="$(cat "$WORKTREE_FILE")"
  fi
  if [ -f "$PR_URL_FILE" ]; then
    pr_url="$(cat "$PR_URL_FILE")"
  fi

  printf '[{"id":"%%s","title":"%%s","description":"%%s","acceptance_criteria":"%%s","external_ref":"%%s","status":"%%s","priority":2,"issue_type":"task","metadata":{' \
    "$TASK_ID" "$TITLE" "$DESCRIPTION" "$ACCEPTANCE" "$EXTERNAL_REF" "$status"
  comma=""
  if [ -n "$branch" ] || [ -n "$worktree" ]; then
    printf '%%s' "$comma"
    printf '"orpheus.branch":"%%s","orpheus.worktree":"%%s"' "$branch" "$worktree"
    comma=","
  fi
  if [ -n "$pr_url" ]; then
    printf '%%s"orpheus.pr_url":"%%s"' "$comma" "$pr_url"
  fi
  printf '}}]\n'
}

if [ "${1-}" = "--json" ] && [ "${2-}" = "--readonly" ] && [ "${3-}" = "--sandbox" ] && [ "${4-}" = "list" ] && [ "${5-}" = "--all" ] && [ "${6-}" = "--limit" ] && [ "${7-}" = "0" ] && [ "$#" -eq 7 ]; then
  emit_task
  exit 0
fi

if [ "${1-}" = "--json" ] && [ "${2-}" = "--readonly" ] && [ "${3-}" = "--sandbox" ] && [ "${4-}" = "show" ] && [ "${5-}" = "--id" ] && [ "${6-}" = "$TASK_ID" ] && [ "$#" -eq 6 ]; then
  emit_task
  exit 0
fi

if [ "${1-}" = "--json" ] && [ "${2-}" = "--sandbox" ] && [ "${3-}" = "update" ] && [ "${4-}" = "$TASK_ID" ]; then
  shift 4
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --status)
        shift
        printf '%%s\n' "$1" > "$STATUS_FILE"
        ;;
      --set-metadata)
        shift
        case "$1" in
          orpheus.branch=*) printf '%%s\n' "${1#orpheus.branch=}" > "$BRANCH_FILE" ;;
          orpheus.worktree=*) printf '%%s\n' "${1#orpheus.worktree=}" > "$WORKTREE_FILE" ;;
          orpheus.pr_url=*) printf '%%s\n' "${1#orpheus.pr_url=}" > "$PR_URL_FILE" ;;
        esac
        ;;
    esac
    shift
  done
  printf '{}\n'
  exit 0
fi

if [ "${1-}" = "--json" ] && [ "${2-}" = "--sandbox" ] && [ "${3-}" = "close" ] && [ "${4-}" = "$TASK_ID" ] && [ "$#" -eq 4 ]; then
  dirty="$(git -C "$REPO_PATH" status --porcelain=v1)"
  if [ -n "$dirty" ]; then
    printf 'close before reviewed changes were committed:\n%%s\n' "$dirty" >&2
    exit 66
  fi
  current="$(git -C "$REPO_PATH" rev-parse "$CLOSE_BRANCH")"
  remote="$(git -C "$REPO_PATH" remote get-url origin)"
  pushed="$(git -C "$remote" rev-parse "refs/heads/$CLOSE_BRANCH" 2>/dev/null || true)"
  if [ "$current" != "$pushed" ]; then
    printf 'close before branch push: branch=%%s head=%%s remote=%%s\n' "$CLOSE_BRANCH" "$current" "$pushed" >&2
    exit 66
  fi
  printf 'closed\n' > "$STATUS_FILE"
  printf '{}\n'
  exit 0
fi

printf 'unexpected fake bd call: %%s|%%s\n' "$PWD" "$*" >&2
exit 65
`

func withStatefulCompletionBD(t *testing.T, task completionBDTask) statefulCompletionBD {
	t.Helper()

	binDir := t.TempDir()
	stateDir := filepath.Join(binDir, "state")
	must := require.New(t)
	must.NoError(os.MkdirAll(stateDir, 0o755))
	statusPath := filepath.Join(stateDir, "status")
	branchPath := filepath.Join(stateDir, "branch")
	worktreePath := filepath.Join(stateDir, "worktree")
	prURLPath := filepath.Join(stateDir, "pr-url")
	logPath := filepath.Join(binDir, "bd.log")
	must.NoError(os.WriteFile(statusPath, []byte("open\n"), 0o644))

	script := fmt.Sprintf(
		statefulCompletionBDScript,
		shellQuote(task.TaskID),
		shellQuote(task.Title),
		shellQuote(task.Description),
		shellQuote(task.AcceptanceCriteria),
		shellQuote(task.RepoPath),
		shellQuote(statusPath),
		shellQuote(branchPath),
		shellQuote(worktreePath),
		shellQuote(prURLPath),
		shellQuote(completionCloseBranch(task)),
		shellQuote(task.ExternalRef),
	)

	bdPath := filepath.Join(binDir, "bd")
	must.NoError(os.WriteFile(bdPath, []byte(script), 0o755))
	t.Setenv("FAKE_BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return statefulCompletionBD{LogPath: logPath, StatusPath: statusPath}
}

func completionCloseBranch(task completionBDTask) string {
	if task.CloseBranch != "" {
		return task.CloseBranch
	}
	return "main"
}

type completionFlowAgentOptions struct {
	Command              string
	FileName             string
	Body                 string
	Summary              string
	Description          string
	DetailedDescription  string
	TechnicalExplanation string
}

func withCompletionFlowAgent(t *testing.T, opts completionFlowAgentOptions) string {
	t.Helper()

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, opts.Command+".log")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
{
  printf 'PWD=%%s\n' "$PWD"
  printf 'ARG_COUNT=%%s\n' "$#"
  index=0
  for arg in "$@"; do
    index=$((index + 1))
    printf 'ARG_%%s<<END\n%%s\nEND\n' "$index" "$arg"
  done
  printf 'ORPHEUS_REPO_ID=%%s\n' "$ORPHEUS_REPO_ID"
  printf 'ORPHEUS_TASK_ID=%%s\n' "$ORPHEUS_TASK_ID"
  printf 'ORPHEUS_WORKTREE=%%s\n' "$ORPHEUS_WORKTREE"
  printf 'ORPHEUS_BRANCH=%%s\n' "$ORPHEUS_BRANCH"
  printf 'ORPHEUS_AGENT_PROMPT<<END\n%%s\nEND\n' "$ORPHEUS_AGENT_PROMPT"
} >> "$FAKE_COMPLETION_AGENT_LOG"

context_output="$(orpheus agent context 2>&1)" || {
  status=$?
  printf 'AGENT_CONTEXT_ERROR<<END\n%%s\nEND\n' "$context_output" >> "$FAKE_COMPLETION_AGENT_LOG"
  exit "$status"
}
printf 'AGENT_CONTEXT<<END\n%%s\nEND\n' "$context_output" >> "$FAKE_COMPLETION_AGENT_LOG"

printf '%%s\n' %s > "$PWD/%s"

done_output="$(orpheus agent done --summary %s --description %s --detailed-description %s --technical-explanation %s 2>&1)" || {
  status=$?
  printf 'AGENT_DONE_ERROR<<END\n%%s\nEND\n' "$done_output" >> "$FAKE_COMPLETION_AGENT_LOG"
  exit "$status"
}
printf 'AGENT_DONE<<END\n%%s\nEND\n' "$done_output" >> "$FAKE_COMPLETION_AGENT_LOG"
printf 'completion agent completed\n'
`,
		shellQuote(opts.Body),
		opts.FileName,
		shellQuote(opts.Summary),
		shellQuote(opts.Description),
		shellQuote(opts.DetailedDescription),
		shellQuote(opts.TechnicalExplanation),
	)

	agentPath := filepath.Join(binDir, opts.Command)
	if err := os.WriteFile(agentPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake completion agent: %v", err)
	}
	t.Setenv("FAKE_COMPLETION_AGENT_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func withOrpheusCLIHelper(t *testing.T) string {
	t.Helper()

	binDir := t.TempDir()
	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	script := fmt.Sprintf(`#!/bin/sh
GO_WANT_ORPHEUS_CLI_HELPER=1 exec %s -test.run=TestOrpheusCLIHelperProcess -- "$@"
`, shellQuote(testBinary))

	orpheusPath := filepath.Join(binDir, "orpheus")
	if err := os.WriteFile(orpheusPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write orpheus helper: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return orpheusPath
}

func readCompletionTaskState(t *testing.T, paths state.Paths, repoID string, taskID string) taskstate.TaskState {
	t.Helper()

	var taskState taskstate.TaskState
	if err := paths.ReadDataYAML(filepath.Join("repos", repoID, "tasks", taskID+".yaml"), &taskState); err != nil {
		t.Fatalf("read task state: %v", err)
	}
	return taskState
}

func readFileString(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func agentLogBlock(t *testing.T, log string, marker string) string {
	t.Helper()

	startMarker := marker + "<<END\n"
	start := strings.Index(log, startMarker)
	if start < 0 {
		t.Fatalf("log missing %s block:\n%s", marker, log)
	}
	bodyStart := start + len(startMarker)
	end := strings.Index(log[bodyStart:], "\nEND")
	if end < 0 {
		t.Fatalf("log block %s missing END:\n%s", marker, log)
	}
	return log[bodyStart : bodyStart+end]
}
