package workstate

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/work"
)

// ErrConflict means the proposed state is not based on the stored version.
var ErrConflict = errors.New("workflow persistence version conflict")

// Store loads validated work history and saves validated successors. Its lock is
// independent of the application's global progression lock, so callers may hold
// that lock while saving without allowing an unlocked writer to bypass CAS.
type Store struct{ paths state.Paths }

// NewStore constructs a workflow store without loading or converting any data.
func NewStore(paths state.Paths) Store { return Store{paths: paths} }

// Path returns the workflow document's location. Old task documents at this
// location are rejected, never converted.
func (s Store) Path(repositoryID, taskID string) (string, error) {
	rel, err := relativePath(repositoryID, taskID)
	if err != nil {
		return "", err
	}
	return s.paths.DataPath(rel)
}

// Load returns an independently decoded, validated snapshot. A missing document
// is an empty state at version zero; an empty or old-format file is not absence.
func (s Store) Load(repositoryID, taskID string) (work.State, error) {
	rel, err := relativePath(repositoryID, taskID)
	if err != nil {
		return work.State{}, err
	}
	var loaded document
	if err := s.paths.ReadDataYAML(rel, &loaded); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return work.State{RepositoryID: repositoryID, TaskID: taskID}, nil
		}
		return work.State{}, fmt.Errorf("load workflow %s/%s: %w", repositoryID, taskID, err)
	}
	if loaded.State.RepositoryID != repositoryID || loaded.State.TaskID != taskID {
		return work.State{}, fmt.Errorf("workflow document identity does not match %s/%s", repositoryID, taskID)
	}
	if loaded.State.Version == 0 {
		return work.State{}, fmt.Errorf("stored workflow requires a nonzero persistence version")
	}
	return loaded.State, nil
}

// Save compares expected against an independent reload while holding an exclusive
// document lock. Rejected snapshots/successors never replace the stored bytes.
// Success returns the incremented version; it does not mutate proposed.
func (s Store) Save(proposed work.State, expected work.StateVersion) (work.StateVersion, error) {
	rel, err := relativePath(proposed.RepositoryID, proposed.TaskID)
	if err != nil {
		return 0, err
	}
	if proposed.Version != expected {
		return 0, fmt.Errorf("%w: proposed version %d, expected %d", ErrConflict, proposed.Version, expected)
	}
	if expected == math.MaxUint64 {
		return 0, fmt.Errorf("workflow persistence version exhausted")
	}
	// Freeze the wire representation before comparing with a decoded prior
	// snapshot. In particular, transient time.Time monotonic/location metadata
	// must not make an unchanged recorded observation look rewritten.
	proposed, err = validatedSnapshot(proposed)
	if err != nil {
		return 0, fmt.Errorf("invalid proposed workflow: %w", err)
	}
	err = s.paths.WithDataLock(rel+".lock", func() error {
		previous, err := s.Load(proposed.RepositoryID, proposed.TaskID)
		if err != nil {
			return err
		}
		if previous.Version != expected {
			return fmt.Errorf("%w: stored version %d, expected %d", ErrConflict, previous.Version, expected)
		}
		if err := proposed.ValidateSuccessor(&previous); err != nil {
			return fmt.Errorf("invalid workflow successor: %w", err)
		}
		proposed.Version = expected + 1
		return s.paths.WriteDataYAML(rel, document{State: proposed})
	})
	if err != nil {
		return 0, err
	}
	return expected + 1, nil
}

func validatedSnapshot(value work.State) (work.State, error) {
	encoded, err := Encode(value)
	if err != nil {
		return work.State{}, err
	}
	return Decode(encoded)
}

func relativePath(repositoryID, taskID string) (string, error) {
	for label, value := range map[string]string{"repository": repositoryID, "task": taskID} {
		if value == "" || value != strings.TrimSpace(value) || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") || filepath.VolumeName(value) != "" {
			return "", fmt.Errorf("invalid workflow %s identity %q", label, value)
		}
	}
	return filepath.Join("repos", repositoryID, "tasks", taskID+".yaml"), nil
}
