package task

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrMalformedTaskID indicates a task id cannot be parsed as a prefixed Beads id.
	ErrMalformedTaskID = errors.New("malformed task id")

	// ErrUnknownTaskPrefix indicates a task id prefix does not match a registered repository.
	ErrUnknownTaskPrefix = errors.New("unknown task id prefix")

	// ErrAmbiguousTaskPrefix indicates registry-derived prefixes cannot resolve a task id deterministically.
	ErrAmbiguousTaskPrefix = errors.New("ambiguous task id prefix")
)

// ResolvedTaskSource is the registered repository context for a task-specific command.
type ResolvedTaskSource struct {
	// TaskID is the trimmed task id supplied by the user.
	TaskID string

	// Prefix is the Beads issue prefix that matched TaskID.
	Prefix string

	// Source is the registered repository source that owns tasks with Prefix.
	Source RepositorySource
}

// ResolveTaskSource resolves a prefixed Beads task id to one registered repository source.
//
// Resolution uses only the supplied registry-derived sources. It does not construct a
// backend and does not call Beads or Git.
func ResolveTaskSource(sources []RepositorySource, taskID string) (ResolvedTaskSource, error) {
	id, parsedPrefix, err := parsePrefixedTaskID(taskID)
	if err != nil {
		return ResolvedTaskSource{}, err
	}

	matches := make([]ResolvedTaskSource, 0, 1)
	knownPrefixCount := 0
	missingPrefixRepos := []string{}

	for _, source := range sources {
		prefix := strings.TrimSpace(source.Repository.TaskIDPrefix)
		if prefix == "" {
			missingPrefixRepos = append(missingPrefixRepos, source.Repository.ID)
			continue
		}

		knownPrefixCount++
		if !strings.HasPrefix(id, prefix+"-") {
			continue
		}
		if len(id) == len(prefix)+1 {
			return ResolvedTaskSource{}, fmt.Errorf(
				"%w: task id %q is missing the Beads task number after prefix %q; "+
					"expected <prefix>-<number>, for example %s-123",
				ErrMalformedTaskID,
				id,
				prefix,
				prefix,
			)
		}

		source.Repository.TaskIDPrefix = prefix
		matches = append(matches, ResolvedTaskSource{
			TaskID: id,
			Prefix: prefix,
			Source: source,
		})
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return ResolvedTaskSource{}, unknownTaskPrefixError(id, parsedPrefix, knownPrefixCount, missingPrefixRepos)
	default:
		return ResolvedTaskSource{}, ambiguousTaskPrefixError(id, matches)
	}
}

func parsePrefixedTaskID(taskID string) (id string, prefix string, err error) {
	id = strings.TrimSpace(taskID)
	if id == "" {
		return "", "", fmt.Errorf(
			"%w: task id is required; pass a Beads task id like <prefix>-123",
			ErrMalformedTaskID,
		)
	}
	if strings.ContainsAny(id, " \t\n\r") {
		return "", "", fmt.Errorf(
			"%w: task id %q contains whitespace; pass a single Beads task id like <prefix>-123",
			ErrMalformedTaskID,
			id,
		)
	}

	separator := strings.Index(id, "-")
	if separator < 0 {
		return "", "", fmt.Errorf(
			"%w: task id %q has no Beads prefix separator; expected <prefix>-<number>, for example op-123",
			ErrMalformedTaskID,
			id,
		)
	}
	if separator == 0 {
		return "", "", fmt.Errorf(
			"%w: task id %q is missing a Beads prefix before '-'; expected <prefix>-<number>, for example op-123",
			ErrMalformedTaskID,
			id,
		)
	}
	if separator == len(id)-1 {
		return "", "", fmt.Errorf(
			"%w: task id %q is missing the Beads task number after '-'; expected <prefix>-<number>, for example op-123",
			ErrMalformedTaskID,
			id,
		)
	}

	return id, id[:separator], nil
}

func unknownTaskPrefixError(id string, parsedPrefix string, knownPrefixCount int, missingPrefixRepos []string) error {
	if knownPrefixCount == 0 {
		message := fmt.Sprintf(
			"task id %q uses Beads prefix %q, but no registered repositories have Beads prefixes; "+
				"run `orpheus repo list` to inspect the registry or register the repo",
			id,
			parsedPrefix,
		)
		if len(missingPrefixRepos) > 0 {
			message += fmt.Sprintf("; repositories missing prefixes: %s", strings.Join(missingPrefixRepos, ", "))
		}
		return fmt.Errorf("%w: %s", ErrUnknownTaskPrefix, message)
	}

	return fmt.Errorf(
		"%w: task id %q uses Beads prefix %q, which is not registered; "+
			"run `orpheus repo list` to see registered prefixes or register the repo",
		ErrUnknownTaskPrefix,
		id,
		parsedPrefix,
	)
}

func ambiguousTaskPrefixError(id string, matches []ResolvedTaskSource) error {
	descriptions := make([]string, 0, len(matches))
	for _, match := range matches {
		descriptions = append(
			descriptions,
			fmt.Sprintf("%s (repo %s)", match.Prefix, match.Source.Repository.ID),
		)
	}

	return fmt.Errorf(
		"%w: task id %q matches multiple registered Beads prefixes: %s; "+
			"repo registration should prevent prefix collisions, run `orpheus repo list` and repair the registry",
		ErrAmbiguousTaskPrefix,
		id,
		strings.Join(descriptions, ", "),
	)
}
