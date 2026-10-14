package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func comparisonOutput(opts options) (string, error) {
	if !opts.compareWorktree {
		return filepath.Abs(opts.output)
	}
	roots, err := localTimingRoots()
	if err != nil {
		return "", err
	}
	temporary, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", err
	}
	if err := outsideTimingRoots(roots, temporary); err != nil {
		return "", err
	}
	parent, name := temporary, ""
	if opts.output != "" {
		output, err := filepath.Abs(opts.output)
		if err != nil {
			return "", err
		}
		parent, name = filepath.Dir(filepath.Dir(output)), filepath.Base(filepath.Dir(output))
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	directory := parent
	if name != "" {
		directory = filepath.Join(parent, name)
	}
	if err := outsideTimingRoots(roots, directory); err != nil {
		return "", err
	}
	if opts.output == "" {
		directory, err = os.MkdirTemp(parent, "orpheus-timing-report-*")
		if err != nil {
			return "", err
		}
		return filepath.Join(directory, "report.json"), nil
	}
	// A fresh directory also prevents replacement of existing sample evidence.
	if err := os.Mkdir(directory, 0o755); err != nil {
		return "", fmt.Errorf("local timing output requires a new directory outside the checkout: %w", err)
	}
	return filepath.Join(directory, filepath.Base(opts.output)), nil
}

func outsideSource(source, path string) error {
	relative, err := filepath.Rel(source, path)
	if err != nil {
		return err
	}
	if relative == "." || filepath.IsLocal(relative) {
		return errors.New("local timing state and reports must be outside the source checkout and Git metadata")
	}
	return nil
}

func localTimingRoots() ([]string, error) {
	var roots []string
	for _, args := range [][]string{{"rev-parse", "--show-toplevel"}, {"rev-parse", "--absolute-git-dir"}, {"rev-parse", "--path-format=absolute", "--git-common-dir"}} {
		output, err := sourceGit(context.Background(), "", args...)
		if err != nil {
			return nil, err
		}
		root, err := filepath.EvalSymlinks(strings.TrimSpace(output))
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, nil
}

func outsideTimingRoots(roots []string, path string) error {
	for _, root := range roots {
		if err := outsideSource(root, path); err != nil {
			return err
		}
	}
	return nil
}
