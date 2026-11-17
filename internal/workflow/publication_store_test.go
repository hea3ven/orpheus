package workflow_test

import (
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/require"
)

type implementationRecordingStore struct {
	testing *testing.T
	data    []byte
	failure error
}

func newImplementationRecordingStore(t *testing.T, initial work.State) *implementationRecordingStore {
	t.Helper()
	data, err := workstate.Encode(initial)
	require.NoError(t, err)
	return &implementationRecordingStore{testing: t, data: data}
}

func (s *implementationRecordingStore) Load(_, _ string) (work.State, error) {
	return workstate.Decode(s.data)
}

func (s *implementationRecordingStore) Save(proposed work.State, expected work.StateVersion) (work.StateVersion, error) {
	if s.failure != nil {
		return 0, s.failure
	}
	previous, err := workstate.Decode(s.data)
	require.NoError(s.testing, err)
	require.Equal(s.testing, expected, previous.Version)
	require.Equal(s.testing, expected, proposed.Version)
	if err := proposed.ValidateSuccessor(&previous); err != nil {
		return 0, err
	}
	proposed.Version++
	data, err := workstate.Encode(proposed)
	if err != nil {
		return 0, err
	}
	s.data = data
	return proposed.Version, nil
}

func outputCapturePaths(t *testing.T) state.Paths {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
	return paths
}
