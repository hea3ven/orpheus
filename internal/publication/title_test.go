package publication_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/stretchr/testify/require"
)

func TestRenderTitle(t *testing.T) {
	tests := []struct {
		name        string
		template    string
		summary     string
		externalRef string
		expected    string
	}{
		{
			name:     "empty template preserves summary",
			summary:  "feat: add title templates",
			expected: "feat: add title templates",
		},
		{
			name:     "template interpolates summary",
			template: "[OPS] {{summary}}",
			summary:  "feat: add title templates",
			expected: "[OPS] feat: add title templates",
		},
		{
			name:        "template interpolates normalized external reference",
			template:    "[{{external_ref}}] {{summary}}",
			summary:     "Replaced the config for abc",
			externalRef: " \n TREX-1234\t\n ",
			expected:    "[TREX-1234] Replaced the config for abc",
		},
		{
			name:     "literal template is valid",
			template: "Publish configured changes",
			summary:  "feat: add title templates",
			expected: "Publish configured changes",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := publication.RenderTitle(test.template, test.summary, test.externalRef)

			require.NoError(t, err)
			require.Equal(t, test.expected, got)
		})
	}
}

func TestValidateTitleTemplateRejectsUnsupportedPlaceholders(t *testing.T) {
	tests := []struct {
		name     string
		template string
	}{
		{name: "template expression", template: "{{ .Summary }}"},
		{name: "unexpected closing delimiter", template: "summary }}"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := publication.ValidateTitleTemplate(test.template)

			require.Error(t, err)
			require.Contains(t, err.Error(), "publication title template")
		})
	}
}

func TestRenderTitleRejectsMissingExternalReferenceWhenRequired(t *testing.T) {
	_, err := publication.RenderTitle("[{{external_ref}}] {{summary}}", "Publish task", " \t\n ")

	require.EqualError(t, err, "publication title template requires a task external reference")
}
