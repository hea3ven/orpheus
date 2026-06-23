package cli

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestConfigureRepoSummaryGuidanceInteractive(t *testing.T) {
	originalIsTerminal := isTerminal
	isTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { isTerminal = originalIsTerminal })

	command := &cobra.Command{}
	command.SetIn(strings.NewReader("Use sentence-case summaries without a type prefix.\n"))
	stderr := new(bytes.Buffer)
	command.SetErr(stderr)
	repo := registry.Repo{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := configureRepoSummaryGuidance(command, &repo, logger)

	require.NoError(t, err)
	require.Equal(t, "Use sentence-case summaries without a type prefix.", repo.SummaryGuidance)
	require.Contains(t, stderr.String(), "Custom summary guidance (optional):")
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
