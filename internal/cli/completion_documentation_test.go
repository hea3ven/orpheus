package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompletionDocumentationPreservesExistingPowerShellProfile(t *testing.T) {
	t.Parallel()

	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	require.NoError(t, err)

	instructions := string(readme)
	assert.Contains(t, instructions, "$profileDirectory = Split-Path -Parent $PROFILE")
	assert.Contains(t, instructions, "New-Item -ItemType Directory -Path $profileDirectory -Force | Out-Null")
	assert.Contains(t, instructions, "if (-not (Test-Path -LiteralPath $PROFILE)) {\n  New-Item -ItemType File -Path $PROFILE | Out-Null\n}")
	assert.Contains(t, instructions, "Add-Content -LiteralPath $PROFILE")
	assert.NotContains(t, instructions, "New-Item -ItemType File -Force $PROFILE")
}
