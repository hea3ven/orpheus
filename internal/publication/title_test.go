package publication_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/stretchr/testify/require"
)

func TestRenderTitle(t *testing.T) {
	tests := []struct {
		name     string
		template string
		summary  string
		expected string
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
			name:     "literal template is valid",
			template: "Publish configured changes",
			summary:  "feat: add title templates",
			expected: "Publish configured changes",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := publication.RenderTitle(test.template, test.summary)

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
		{name: "external reference", template: "[{{external_ref}}] {{summary}}"},
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
