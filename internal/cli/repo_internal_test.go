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

func TestConfigureRepoSummaryGuidanceInteractiveDefaultsToTyped(t *testing.T) {
	originalIsTerminal := isTerminal
	isTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { isTerminal = originalIsTerminal })

	command := &cobra.Command{}
	command.SetIn(strings.NewReader("\n"))
	stderr := new(bytes.Buffer)
	command.SetErr(stderr)
	repo := registry.Repo{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := configureRepoSummaryGuidance(command, &repo, logger)

	require.NoError(t, err)
	require.Equal(t, registry.SummaryGuidanceStyleTyped, repo.SummaryGuidanceStyle)
	require.Empty(t, repo.SummaryGuidance)
	require.Contains(t, stderr.String(), "Summary guidance style (typed, capitalized, custom) [typed]:")
	require.NotContains(t, stderr.String(), "Custom summary guidance")
}

func TestConfigureRepoSummaryGuidanceInteractiveUsesCapitalizedStyle(t *testing.T) {
	originalIsTerminal := isTerminal
	isTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { isTerminal = originalIsTerminal })

	command := &cobra.Command{}
	command.SetIn(strings.NewReader("capitalized\n"))
	stderr := new(bytes.Buffer)
	command.SetErr(stderr)
	repo := registry.Repo{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := configureRepoSummaryGuidance(command, &repo, logger)

	require.NoError(t, err)
	require.Equal(t, registry.SummaryGuidanceStyleCapitalized, repo.SummaryGuidanceStyle)
	require.Empty(t, repo.SummaryGuidance)
	require.NotContains(t, stderr.String(), "Custom summary guidance")
}

func TestConfigureRepoSummaryGuidanceInteractiveAcceptsCustomGuidance(t *testing.T) {
	originalIsTerminal := isTerminal
	isTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { isTerminal = originalIsTerminal })

	command := &cobra.Command{}
	command.SetIn(strings.NewReader("custom\nUse sentence-case summaries without a type prefix.\n"))
	stderr := new(bytes.Buffer)
	command.SetErr(stderr)
	repo := registry.Repo{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := configureRepoSummaryGuidance(command, &repo, logger)

	require.NoError(t, err)
	require.Equal(t, registry.SummaryGuidanceStyleTyped, repo.SummaryGuidanceStyle)
	require.Equal(t, "Use sentence-case summaries without a type prefix.", repo.SummaryGuidance)
	require.Contains(t, stderr.String(), "Custom summary guidance:")
}

func TestConfigureRepoSummaryGuidanceRejectsInvalidStyle(t *testing.T) {
	originalIsTerminal := isTerminal
	isTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { isTerminal = originalIsTerminal })

	command := &cobra.Command{}
	command.SetIn(strings.NewReader("informal\n"))
	command.SetErr(new(bytes.Buffer))
	repo := registry.Repo{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := configureRepoSummaryGuidance(command, &repo, logger)

	require.ErrorContains(t, err, `summary_guidance_style "informal" is invalid`)
}

func TestConfigureRepoTitleTemplateInteractive(t *testing.T) {
	originalIsTerminal := isTerminal
	isTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { isTerminal = originalIsTerminal })

	command := &cobra.Command{}
	command.SetIn(strings.NewReader("[OPS] {{summary}}\n"))
	stderr := new(bytes.Buffer)
	command.SetErr(stderr)
	repo := registry.Repo{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := configureRepoTitleTemplate(command, &repo, logger)

	require.NoError(t, err)
	require.Equal(t, "[OPS] {{summary}}", repo.TitleTemplate)
	require.Contains(t, stderr.String(), "Publication title template (optional):")
}

func TestConfigureRepoTitleTemplateAcceptsExternalReferencePlaceholder(t *testing.T) {
	originalIsTerminal := isTerminal
	isTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { isTerminal = originalIsTerminal })

	command := &cobra.Command{}
	command.SetIn(strings.NewReader("[{{external_ref}}] {{summary}}\n"))
	command.SetErr(new(bytes.Buffer))
	repo := registry.Repo{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := configureRepoTitleTemplate(command, &repo, logger)

	require.NoError(t, err)
	require.Equal(t, "[{{external_ref}}] {{summary}}", repo.TitleTemplate)
}
