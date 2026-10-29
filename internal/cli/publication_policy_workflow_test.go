//go:build integration

package cli_test

import (
	"fmt"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowConfiguredPublicationPolicyEndToEnd(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(fmt.Sprintf("global_%t", global), func(t *testing.T) {
			f := newFinalizationFixture(t, "op-trex-title")
			item := anOpenTask("op-trex-title")
			item.ExternalRef = "  TREX-1234 \n"
			f.backend.tasks[item.ID] = item
			completion := agent.CompleteOptions{Summary: "Replaced the config for abc", Description: "Replaced the config used for publication validation.", DetailedDescription: "## Configured publication\n\nReplaced the config used for publication validation.", TechnicalExplanation: "Technical explanation."}
			f.withCompletingAgent(completion)
			if global {
				f.setConfig("publication", map[string]any{"summary_guidance": "Write a concise global release note.", "summary_guidance_style": registry.SummaryGuidanceStyleCapitalized, "title_template": "[{{external_ref}}] {{summary}}"})
			} else {
				for _, args := range [][]string{
					{"repo", "config", "set", "alpha", "summary-style", registry.SummaryGuidanceStyleCapitalized},
					{"repo", "config", "set", "alpha", "title-template", "[{{external_ref}}] {{summary}}"},
				} {
					stdout, stderr := f.run("", args...)
					assert.Empty(t, stderr)
					assert.NotEmpty(t, stdout)
				}

			}
			stdout, stderr := f.run("", "task", "run", item.ID)

			assert.Contains(t, stderr, "Review for "+item.ID+" is waiting for manual step \"local-review\"")
			assert.Contains(t, stdout, "Recorded completion for "+item.ID)
			require.Len(t, f.agent.contexts, 1)
			assert.Contains(t, f.agent.contexts[0], "- External reference: TREX-1234")
			if global {
				assert.Contains(t, f.agent.contexts[0], "Write a concise global release note.")
				assert.NotContains(t, f.agent.contexts[0], "Use one capitalized plain-English summary line")
			} else {
				assert.Contains(t, f.agent.contexts[0], "Use one capitalized plain-English summary line")
			}
			if !global {
				assert.Contains(t, f.agent.contexts[0], "Replaced the config for abc")
			}
			state, _ := f.loadFinalTask(item.ID)
			assertCompletionRecorded(t, completion, state.Runs[0].Completion)
			f.passedReview(item.ID)

			stdout, stderr = f.run("", "task", "done", item.ID)

			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "observed PR "+f.pr.url)
			assert.Equal(t, []string{"Replaced the config for abc\n\nReplaced the config used for publication validation."}, f.candidate.commits)
			require.Len(t, f.pr.created, 1)
			assert.Equal(t, "[TREX-1234] Replaced the config for abc", f.pr.created[0].Title)
			assert.Contains(t, f.pr.created[0].Body, completion.DetailedDescription)
			f.assertPublishedPR(item.ID)
			state, _ = f.loadFinalTask(item.ID)
			var publicationEvents []taskstate.EventType
			for _, event := range state.Events {
				if event.Type == taskstate.EventChangesPushed || event.Type == taskstate.EventPRObserved {
					publicationEvents = append(publicationEvents, event.Type)
				}
			}
			assert.Equal(t, []taskstate.EventType{taskstate.EventChangesPushed, taskstate.EventPRObserved}, publicationEvents)
		})
	}
}

func TestIntegrationWorkflowMissingPublicationExternalReferenceBlocksDispatchAndPublicationEndToEnd(t *testing.T) {
	f := newFinalizationFixture(t, "op-missing-title-ref")
	item := anOpenTask("op-missing-title-ref")
	f.backend.tasks[item.ID] = item
	f.withCompletingAgent(aCompletion())
	f.run("", "repo", "config", "set", "alpha", "title-template", "[{{external_ref}}] {{summary}}")

	stdout, stderr, err := f.runError("", "task", "run", item.ID)

	require.ErrorContains(t, err, "missing required external reference")
	assert.ErrorContains(t, err, "orpheus task edit "+item.ID+" --external-ref <reference>")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	assert.Empty(t, f.agent.launches)
	assert.Empty(t, f.git.worktreeSetups)
	state, _ := f.loadFinalTask(item.ID)
	assert.Empty(t, state.Runs)
	f.run("", "repo", "config", "set", "alpha", "title-template", "")
	stdout, stderr = f.run("", "task", "run", item.ID)
	assert.Contains(t, stderr, "is waiting for manual step")
	assert.Contains(t, stdout, "Recorded completion for "+item.ID)
	state, _ = f.loadFinalTask(item.ID)
	_, ok := taskstate.LatestRun(state)
	require.True(t, ok)
	f.passedReview(item.ID)
	f.run("", "repo", "config", "set", "alpha", "title-template", "[{{external_ref}}] {{summary}}")

	stdout, stderr, err = f.runError("", "task", "done", item.ID)

	require.NoError(t, err)
	assert.Contains(t, stdout, "Published "+item.ID)
	assert.Empty(t, stderr)
	assert.False(t, f.git.hasCandidateChanges)
	f.assertPublishedPR(item.ID)
	require.Len(t, f.pr.created, 1)
	assert.Equal(t, "feat: add search filters", f.pr.created[0].Title, "later config edits cannot rewrite an authorized plan")
}
