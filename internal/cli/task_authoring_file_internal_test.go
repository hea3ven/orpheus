package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveCreateContentReadsFilesVerbatimAndReportsErrors(t *testing.T) {
	dir := testutil.CanonicalTempDir(t)
	path := filepath.Join(dir, "planning.md")
	require.NoError(t, os.WriteFile(path, []byte("# Description\n\nLong form.\n"), 0o644))

	got, err := resolveCreateContent("", path, "--description", "--description-file")
	require.NoError(t, err)
	assert.Equal(t, "# Description\n\nLong form.\n", got)

	got, err = resolveCreateContent(" inline text ", "", "--description", "--description-file")
	require.NoError(t, err)
	assert.Equal(t, " inline text ", got)

	_, err = resolveCreateContent("inline", path, "--description", "--description-file")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--description cannot be combined with --description-file")

	_, err = resolveCreateContent("", filepath.Join(dir, "missing.md"), "--description", "--description-file")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read --description-file")
}

func TestResolveExactlyOneTextReadsFilesVerbatimAndValidatesPresence(t *testing.T) {
	dir := testutil.CanonicalTempDir(t)
	path := filepath.Join(dir, "finding.md")
	emptyPath := filepath.Join(dir, "empty.md")
	require.NoError(t, os.WriteFile(path, []byte("Finding details.\nKeep spacing.\n"), 0o644))
	require.NoError(t, os.WriteFile(emptyPath, []byte(" \n\t"), 0o644))

	got, err := resolveExactlyOneText("description", "", path)
	require.NoError(t, err)
	assert.Equal(t, "Finding details.\nKeep spacing.\n", got)

	got, err = resolveExactlyOneText("description", " inline details ", "")
	require.NoError(t, err)
	assert.Equal(t, " inline details ", got)

	_, err = resolveExactlyOneText("description", "inline", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "use exactly one of --description or --description-file")

	_, err = resolveExactlyOneText("description", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "description is required")

	_, err = resolveExactlyOneText("description", "", emptyPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "description is required; file is empty")

	_, err = resolveExactlyOneText("description", "", filepath.Join(dir, "missing.md"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read description file")
}
