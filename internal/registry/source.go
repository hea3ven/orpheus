package registry

import (
	"fmt"
	"strings"
)

// Source returns the registered source. Only old records omit the discriminator.
func (r Repo) Source() string {
	if r.TaskSource == "" {
		return "beads"
	}
	return r.TaskSource
}

// Prefix returns the repository's task identifier prefix.
func (r Repo) Prefix() string {
	if r.Source() == "gig" {
		return r.TaskPrefix
	}
	return r.BeadsPrefix
}

// StorageMode returns the registered source's storage ownership mode.
func (r Repo) StorageMode() string {
	if r.Source() == "gig" {
		return r.TaskMode
	}
	return r.BeadsMode
}

// TaskDir resolves storage without discovering or adopting repository-local stores.
func (s Store) TaskDir(repo Repo) (string, error) {
	normalized, err := normalizeRepo(repo)
	if err != nil {
		return "", err
	}
	if normalized.Source() == "beads" {
		return s.BeadsDir(normalized)
	}
	return s.ManagedGigDir(normalized.ID)
}

// ManagedGigDir returns the directory containing Orpheus-owned gig storage.
func (s Store) ManagedGigDir(repoID string) (string, error) {
	return managedSourceDir(s.paths, repoID, "gig")
}

func normalizeTaskSource(repo *Repo) error {
	repo.TaskSource = strings.TrimSpace(repo.TaskSource)
	repo.TaskMode = strings.TrimSpace(repo.TaskMode)
	repo.TaskPrefix = strings.TrimSpace(repo.TaskPrefix)
	switch repo.Source() {
	case "beads":
		if repo.TaskMode != "" || repo.TaskPrefix != "" {
			return fmt.Errorf("beads repositories use beads_mode and beads_prefix, not task_mode or task_prefix")
		}
	case "gig":
		if repo.TaskMode != "managed" {
			return fmt.Errorf("gig supports only task_mode managed, got %q", repo.TaskMode)
		}
		if repo.TaskPrefix == "" || strings.ContainsAny(repo.TaskPrefix, " /\\\t\r\n") {
			return fmt.Errorf("gig requires a nonempty task_prefix without whitespace or path separators")
		}
		if repo.BeadsMode != "" || repo.BeadsPrefix != "" {
			return fmt.Errorf("gig repositories cannot set beads_mode or beads_prefix")
		}
	default:
		return fmt.Errorf("unsupported task source %q; expected gig or beads", repo.TaskSource)
	}
	return nil
}
