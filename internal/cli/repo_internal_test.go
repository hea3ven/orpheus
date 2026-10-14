package cli

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestTerminalDetectionRejectsNonTTYCharacterDevice(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, devNull.Close())
	})

	require.False(t, readerIsTerminal(devNull))
	require.False(t, writerIsTerminal(devNull))
}

func TestRepoWizardCollectsPublicationSettings(t *testing.T) {
	tests := []struct {
		name, input                string
		interactive                bool
		style                      string
		guidance, title, wantError string
	}{
		{name: "noninteractive"},
		{name: "inherited", interactive: true, input: "\n"},
		{name: "capitalized", interactive: true, input: "capitalized\n", style: registry.SummaryGuidanceStyleCapitalized, title: "[OPS] {{summary}}"},
		{name: "custom", interactive: true, input: "custom\nUse sentence-case summaries without a type prefix.\n", guidance: "Use sentence-case summaries without a type prefix.", title: "[{{external_ref}}] {{summary}}"},
		{name: "invalid", interactive: true, input: "informal\n", wantError: `summary_guidance_style "informal" is invalid`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := isTerminal
			isTerminal = func(io.Reader) bool { return test.interactive }
			t.Cleanup(func() { isTerminal = original })
			command := &cobra.Command{}
			command.SetIn(strings.NewReader(test.input))
			var stderr bytes.Buffer
			command.SetErr(&stderr)
			var repo registry.Repo
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))

			err := configureRepoSummaryGuidance(command, &repo, logger)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				require.Empty(t, repo.SummaryGuidanceStyle)
				return
			}
			require.NoError(t, err)
			command.SetIn(strings.NewReader(test.title + "\n"))
			require.NoError(t, configureRepoTitleTemplate(command, &repo, logger))

			require.Equal(t, test.style, repo.SummaryGuidanceStyle)
			require.Equal(t, test.guidance, repo.SummaryGuidance)
			require.Equal(t, test.title, repo.TitleTemplate)
			if !test.interactive {
				require.Empty(t, stderr.String())
			} else {
				require.Contains(t, stderr.String(), "Summary guidance style")
				require.Contains(t, stderr.String(), "Publication title template")
				require.Equal(t, test.guidance != "", strings.Contains(stderr.String(), "Custom summary guidance:"))
			}
		})
	}
}
