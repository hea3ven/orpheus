package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	gitadapter "github.com/hea3ven/orpheus/internal/git"
)

const cacheRegime = "isolated-empty-build-caches;shared-module-cache;one-full-warmup"

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type sourceIdentity struct {
	Revision string `json:"revision"`
	Clean    bool   `json:"clean"`
}

type measurementMetadata struct {
	Source      sourceIdentity    `json:"source"`
	Environment map[string]string `json:"environment"`
}

func executeMeasurement(opts options) error {
	result := qualityReport{SchemaVersion: reportSchemaVersion, Timing: decision{Status: statusNotEvaluated}}
	metadata, err := captureMetadata(opts.measureRevision)
	result.Metadata = &metadata
	if err != nil {
		return finishMeasurement(opts.output, result, err)
	}
	result.Execution = decision{Status: statusUnevaluated}
	result.CoverageDecision = decision{Status: statusNotEvaluated}
	result.Decision = result.Execution
	if err := writeQualityReport(opts.output, result); err != nil {
		return err
	}
	work := filepath.Join(filepath.Dir(opts.output), "profiles")
	result, laneErrors := collectQuality(work, nil)
	result.Metadata = &metadata
	result.MeasurementSamples = 1
	after, identityErr := captureMetadata(opts.measureRevision)
	metadataErr := writeJSON(filepath.Join(filepath.Dir(opts.output), "metadata-after.json"), after)
	if identityErr == nil && !reflect.DeepEqual(metadata, after) {
		identityErr = errors.New("source identity or measurement environment changed during collection")
	}
	return finishMeasurement(opts.output, result, errors.Join(append(laneErrors, identityErr, metadataErr)...))
}

func finishMeasurement(output string, result qualityReport, err error) error {
	result.Timing = decision{Status: statusNotEvaluated}
	result.CoverageDecision = decision{Status: statusNotEvaluated}
	result.Execution = decision{Status: statusPass}
	if err != nil {
		result.Complete = false
		result.Execution = decision{Status: statusExecutionFailed, Findings: []finding{{Kind: "measurement", Message: err.Error()}}}
	}
	for _, lane := range result.Lanes {
		if !lane.Passed || lane.Failure != nil {
			result.Execution.Status = statusTestFailed
		}
	}
	result.Decision = result.Execution
	return finishReport(output, result, err)
}

func captureMetadata(revision string) (measurementMetadata, error) {
	source, err := verifyCheckout(revision)
	if err != nil {
		return measurementMetadata{}, err
	}
	environment, err := resourceEnvironment(os.ReadFile)
	metadata := measurementMetadata{Source: source, Environment: environment}
	if err != nil {
		return metadata, err
	}
	environment["cache_regime"] = cacheRegime
	for _, key := range []string{"GOMAXPROCS", "GOMEMLIMIT", "GOGC", "GODEBUG", "GOTOOLCHAIN", "GOFLAGS", "CGO_CFLAGS", "CGO_LDFLAGS"} {
		environment[key] = os.Getenv(key)
	}
	for _, command := range [][]string{{"go", "version"}, {"go", "env", "-json", "GOOS", "GOARCH", "GOAMD64", "CGO_ENABLED", "GOEXPERIMENT", "GOFLAGS", "GOVERSION", "CC", "CXX", "CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS"}, {"git", "--version"}, {"bd", "--version"}, {"dolt", "version"}} {
		output, err := probeToolCommand(command)
		if err != nil {
			return metadata, fmt.Errorf("probe %s: %w: %s", command, err, output)
		}
		environment[strings.Join(command, " ")] = strings.TrimSpace(string(output))
	}
	for _, tool := range []string{"go", "git", "bd", "dolt"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			return metadata, err
		}
		digest, err := fileDigest(path)
		if err != nil {
			return metadata, err
		}
		environment[tool+"_sha256"] = digest
	}
	self, err := os.Executable()
	if err != nil {
		return metadata, err
	}
	environment["collector_sha256"], err = fileDigest(self)
	return measurementMetadata{Source: source, Environment: environment}, err
}

func probeToolCommand(command []string) ([]byte, error) {
	// Successful metadata comes from stdout only. Stderr may contain warnings
	// about checkout paths, not tool identity; retain it when the probe fails.
	if command[0] == "git" {
		result, err := gitadapter.NewCommandRunner(nil).Run(context.Background(), gitadapter.Command{Args: command[1:]})
		if err != nil {
			return []byte(result.Stdout + result.Stderr), err
		}
		return []byte(result.Stdout), nil
	}
	output, err := exec.Command(command[0], command[1:]...).Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		output = append(output, exitErr.Stderr...)
	}
	return output, err
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func verifyCheckout(revision string) (sourceIdentity, error) {
	root, err := os.Getwd()
	if err != nil {
		return sourceIdentity{}, err
	}
	return verifyCheckoutWithRunner(root, revision, gitadapter.NewCommandRunner(nil))
}

func verifyCheckoutWithRunner(root, revision string, runner gitadapter.CommandRunner) (sourceIdentity, error) {
	if !commitPattern.MatchString(revision) {
		return sourceIdentity{}, errors.New("revision must be an exact full commit ID")
	}
	output, err := runner.Run(context.Background(), gitadapter.Command{Directory: root, Args: []string{"rev-parse", "HEAD"}})
	if err != nil {
		return sourceIdentity{}, fmt.Errorf("read checkout HEAD: %w: %s", err, output.Stderr)
	}
	actual := strings.TrimSpace(output.Stdout)
	if actual != revision {
		return sourceIdentity{}, fmt.Errorf("checkout HEAD %s does not match expected %s", actual, revision)
	}
	output, err = runner.Run(context.Background(), gitadapter.Command{Directory: root, Args: []string{"status", "--porcelain", "--untracked-files=all"}})
	if err != nil {
		return sourceIdentity{}, fmt.Errorf("read checkout status: %w: %s", err, output.Stderr)
	}
	if output.Stdout != "" {
		return sourceIdentity{}, fmt.Errorf("measurement checkout is dirty: %s", output.Stdout)
	}
	return sourceIdentity{Revision: actual, Clean: true}, nil
}
