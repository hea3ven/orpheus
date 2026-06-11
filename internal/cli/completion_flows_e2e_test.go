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

func TestWorktreeCompletionFlowEndToEnd(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(registry.NewStore(paths).Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

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
		Command:             "worktree-completion-agent",
		FileName:            "worktree-change.txt",
		Body:                "worktree implementation",
		Summary:             "Implement worktree completion flow",
		Description:         "Created a worktree validation change.",
		DetailedDescription: "## Worktree completion\n\nCreated a worktree validation change.",
	})
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "worktree-completion",
		"agents": map[string]any{
			"worktree-completion": map[string]any{
				"command": "worktree-completion-agent",
				"args":    []string{"--prompt", "{{prompt}}"},
			},
		},
	}))

	stdout, stderr := executeCommand(t, []string{"task", "run", taskID})

	is.Contains(stdout, "completion agent completed")
	is.Empty(stderr)

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
		"Orpheus will create the pull request",
	} {
		is.Contains(contextOutput, want)
	}

	state := readCompletionTaskState(t, paths, "alpha", taskID)
	must.Len(state.Runs, 1)
	latest := state.Runs[0]
	is.Equal(taskstate.RunStatusSucceeded, latest.Status)
	must.NotNil(latest.FinishedAt)
	must.NotNil(latest.Completion)
	is.Equal("Implement worktree completion flow", latest.Completion.Summary)
	is.Equal("Created a worktree validation change.", latest.Completion.Description)
	is.Equal("## Worktree completion\n\nCreated a worktree validation change.", latest.Completion.DetailedDescription)
	is.False(latest.Completion.CompletedAt.IsZero())
	is.NotEmpty(latest.Completion.Commit)
	is.Equal(strings.TrimSpace(runGit(t, worktreePath, "rev-parse", "HEAD")), latest.Completion.Commit)
	is.Empty(strings.TrimSpace(runGit(t, worktreePath, "status", "--porcelain=v1")))
	is.Equal(
		"Implement worktree completion flow\n\nCreated a worktree validation change.",
		strings.TrimSpace(runGit(t, worktreePath, "log", "-1", "--format=%B")),
	)

	statusOut, statusErr := executeCommand(t, []string{"status"})
	is.Empty(statusErr)
	is.Contains(statusOut, "Needs attention (1)")
	is.Contains(statusOut, taskID)
	is.Contains(statusOut, "needs PR")
	is.NotContains(statusOut, "https://")

	is.Equal("in_progress", strings.TrimSpace(readFileString(t, bd.StatusPath)))
	bdLog := readFileString(t, bd.LogPath)
	is.NotContains(bdLog, "close "+taskID)
	is.NotContains(bdLog, "orpheus.pr_url")
}

func TestMainCompletionFlowEndToEnd(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(registry.NewStore(paths).Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

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
		Command:             "main-completion-agent",
		FileName:            "agent-main-change.txt",
		Body:                "main implementation",
		Summary:             "Implement main completion flow",
		Description:         "Created a main-mode validation change.",
		DetailedDescription: "## Main completion\n\nCreated a main-mode validation change.",
	})
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "main-completion",
		"agents": map[string]any{
			"main-completion": map[string]any{
				"command": "main-completion-agent",
				"args":    []string{"--prompt", "{{prompt}}"},
			},
		},
	}))

	stdout, stderr := executeCommand(t, []string{"task", "run", "--main", taskID})

	is.Contains(stdout, "completion agent completed")
	is.Empty(stderr)

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
		"The human operator will later run `orpheus task done " + taskID + "`",
	} {
		is.Contains(contextOutput, want)
	}

	state := readCompletionTaskState(t, paths, "alpha", taskID)
	must.Len(state.Runs, 1)
	latest := state.Runs[0]
	is.Equal(taskstate.RunStatusSucceeded, latest.Status)
	must.NotNil(latest.FinishedAt)
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
	is.Contains(statusOut, "Reviewing (1)")
	is.Contains(statusOut, taskID)
	is.Contains(statusOut, "local review; run task done")

	must.NoError(os.WriteFile(filepath.Join(repoPath, "human-review.txt"), []byte("human reviewed\n"), 0o644))
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
	is.Contains(fullStatusOut, "Done / closed (1)")
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
}

type statefulCompletionBD struct {
	LogPath    string
	StatusPath string
}

func withStatefulCompletionBD(t *testing.T, task completionBDTask) statefulCompletionBD {
	t.Helper()

	binDir := t.TempDir()
	stateDir := filepath.Join(binDir, "state")
	must := require.New(t)
	must.NoError(os.MkdirAll(stateDir, 0o755))
	statusPath := filepath.Join(stateDir, "status")
	branchPath := filepath.Join(stateDir, "branch")
	worktreePath := filepath.Join(stateDir, "worktree")
	logPath := filepath.Join(binDir, "bd.log")
	must.NoError(os.WriteFile(statusPath, []byte("open\n"), 0o644))

	script := fmt.Sprintf(`#!/bin/sh
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

emit_task() {
  status="$(cat "$STATUS_FILE")"
  branch=""
  worktree=""
  if [ -f "$BRANCH_FILE" ]; then
    branch="$(cat "$BRANCH_FILE")"
  fi
  if [ -f "$WORKTREE_FILE" ]; then
    worktree="$(cat "$WORKTREE_FILE")"
  fi

  printf '[{"id":"%%s","title":"%%s","description":"%%s","acceptance_criteria":"%%s","status":"%%s","priority":2,"issue_type":"task","metadata":{' \
    "$TASK_ID" "$TITLE" "$DESCRIPTION" "$ACCEPTANCE" "$status"
  if [ -n "$branch" ] || [ -n "$worktree" ]; then
    printf '"orpheus.branch":"%%s","orpheus.worktree":"%%s"' "$branch" "$worktree"
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
  current="$(git -C "$REPO_PATH" rev-parse HEAD)"
  remote="$(git -C "$REPO_PATH" remote get-url origin)"
  pushed="$(git -C "$remote" rev-parse refs/heads/main 2>/dev/null || true)"
  if [ "$current" != "$pushed" ]; then
    printf 'close before default branch push: head=%%s remote=%%s\n' "$current" "$pushed" >&2
    exit 66
  fi
  printf 'closed\n' > "$STATUS_FILE"
  printf '{}\n'
  exit 0
fi

printf 'unexpected fake bd call: %%s|%%s\n' "$PWD" "$*" >&2
exit 65
`,
		shellQuote(task.TaskID),
		shellQuote(task.Title),
		shellQuote(task.Description),
		shellQuote(task.AcceptanceCriteria),
		shellQuote(task.RepoPath),
		shellQuote(statusPath),
		shellQuote(branchPath),
		shellQuote(worktreePath),
	)

	bdPath := filepath.Join(binDir, "bd")
	must.NoError(os.WriteFile(bdPath, []byte(script), 0o755))
	t.Setenv("FAKE_BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return statefulCompletionBD{LogPath: logPath, StatusPath: statusPath}
}

type completionFlowAgentOptions struct {
	Command             string
	FileName            string
	Body                string
	Summary             string
	Description         string
	DetailedDescription string
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

done_output="$(orpheus agent done --summary %s --description %s --detailed-description %s 2>&1)" || {
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
