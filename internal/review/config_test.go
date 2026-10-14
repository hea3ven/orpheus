package review_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestLoadConfigValidatesAndResolvesPipelines(t *testing.T) {
	paths := newTestPaths(t)
	if err := testutil.WriteConfigYAML(paths, review.ConfigFile, map[string]any{
		"reviews": map[string]any{
			"default_pipeline": "standard",
			"pipelines": map[string]any{
				"standard": map[string]any{
					"steps": []map[string]any{{
						"kind":    "check",
						"name":    "unit",
						"command": "go",
						"args":    []string{"test", "./..."},
					}},
				},
			},
		},
	}); err != nil {
		t.Fatalf("write config: %v", err)
	}

	config, err := review.LoadConfig(paths)
	if err != nil {
		t.Fatalf("load review config: %v", err)
	}
	pipeline, err := review.ResolvePipeline(config, "", "")
	if err != nil {
		t.Fatalf("resolve pipeline: %v", err)
	}

	if pipeline.Name != "standard" || len(pipeline.Steps) != 1 {
		t.Fatalf("pipeline = %#v, want standard with one step", pipeline)
	}
	if config.MaxAutonomousReviewAttempts != review.DefaultMaxAutonomousReviewAttempts {
		t.Fatalf(
			"max autonomous review attempts = %d, want %d",
			config.MaxAutonomousReviewAttempts,
			review.DefaultMaxAutonomousReviewAttempts,
		)
	}
	step := pipeline.Steps[0]
	if step.Command != "go" || strings.Join(step.Args, " ") != "test ./..." {
		t.Fatalf("step = %#v, want direct command and args", step)
	}
}

func TestLoadConfigAutonomousReviewBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		value     *int
		want      int
		wantError string
	}{
		{"missing config", nil, review.DefaultMaxAutonomousReviewAttempts, ""},
		{"configured", new(2), 2, ""},
		{"zero rejected", new(0), 0, "reviews.max_autonomous_review_attempts must be positive, got 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := newTestPaths(t)
			if tc.value != nil {
				if err := testutil.WriteConfigYAML(paths, review.ConfigFile, map[string]any{"reviews": map[string]any{"max_autonomous_review_attempts": *tc.value}}); err != nil {
					t.Fatal(err)
				}
			}

			config, err := review.LoadConfig(paths)

			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("load error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if config.MaxAutonomousReviewAttempts != tc.want {
				t.Fatalf("budget = %d, want %d", config.MaxAutonomousReviewAttempts, tc.want)
			}
			if !config.IncludePRReviewProcess {
				t.Fatal("review process inclusion should default to true")
			}
		})
	}
}

func TestLoadConfigIncludePRReviewProcess(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		want bool
	}{
		{
			name: "unset defaults true",
			data: map[string]any{"reviews": map[string]any{}},
			want: true,
		},
		{
			name: "explicit false",
			data: map[string]any{
				"reviews": map[string]any{"include_pr_review_process": false},
			},
			want: false,
		},
		{
			name: "explicit true",
			data: map[string]any{
				"reviews": map[string]any{"include_pr_review_process": true},
			},
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths := newTestPaths(t)
			if err := testutil.WriteConfigYAML(paths, review.ConfigFile, test.data); err != nil {
				t.Fatalf("write config: %v", err)
			}

			config, err := review.LoadConfig(paths)
			if err != nil {
				t.Fatalf("load review config: %v", err)
			}
			if config.IncludePRReviewProcess != test.want {
				t.Fatalf("include PR review process = %t, want %t", config.IncludePRReviewProcess, test.want)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidReviewPipelines(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		want string
	}{
		{
			name: "empty pipeline",
			data: map[string]any{
				"reviews": map[string]any{
					"pipelines": map[string]any{
						"empty": map[string]any{"steps": []map[string]any{}},
					},
				},
			},
			want: "steps must contain at least one step",
		},
		{
			name: "invalid kind",
			data: map[string]any{
				"reviews": map[string]any{
					"pipelines": map[string]any{
						"bad": map[string]any{
							"steps": []map[string]any{{"kind": "script", "name": "bad"}},
						},
					},
				},
			},
			want: "kind \"script\" is invalid",
		},
		{
			name: "missing check command",
			data: map[string]any{
				"reviews": map[string]any{
					"pipelines": map[string]any{
						"bad": map[string]any{
							"steps": []map[string]any{{"kind": "check", "name": "unit"}},
						},
					},
				},
			},
			want: "command is required for check steps",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths := newTestPaths(t)
			if err := testutil.WriteConfigYAML(paths, review.ConfigFile, test.data); err != nil {
				t.Fatalf("write config: %v", err)
			}

			_, err := review.LoadConfig(paths)
			if err == nil {
				t.Fatal("load invalid config succeeded, want error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestLoadConfigStepNamesAreUniqueWithinEachPipeline(t *testing.T) {
	for _, samePipeline := range []bool{true, false} {
		name := "reuse across pipelines"
		if samePipeline {
			name = "duplicate after trimming"
		}
		t.Run(name, func(t *testing.T) {
			paths := newTestPaths(t)
			steps := []map[string]any{{"kind": "manual", "name": " local-review "}}
			pipelines := map[string]any{"standard": map[string]any{"steps": steps}}
			second := map[string]any{"kind": "check", "name": "local-review", "command": "make"}
			if samePipeline {
				pipelines["standard"] = map[string]any{"steps": append(steps, second)}
			} else {
				pipelines["strict"] = map[string]any{"steps": []map[string]any{second}}
			}
			if err := testutil.WriteConfigYAML(paths, review.ConfigFile, map[string]any{"reviews": map[string]any{"pipelines": pipelines}}); err != nil {
				t.Fatal(err)
			}

			config, err := review.LoadConfig(paths)

			if samePipeline {
				want := "reviews.pipelines.standard.steps contains duplicate step name \"local-review\" after trimming whitespace"
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("load error = %v, want %q", err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, pipeline := range []string{"standard", "strict"} {
				if got := config.Pipelines[pipeline].Steps[0].Name; got != "local-review" {
					t.Fatalf("%s step name = %q", pipeline, got)
				}
			}
		})
	}
}

func TestResolvePipelinePrecedenceAndBuiltinFallback(t *testing.T) {
	config := review.Config{
		DefaultPipeline: "global",
		Pipelines: map[string]review.Pipeline{
			"global": {Steps: []review.Step{{Kind: review.KindManual, Name: "global-step"}}},
			"repo":   {Steps: []review.Step{{Kind: review.KindManual, Name: "repo-step"}}},
			"cli":    {Steps: []review.Step{{Kind: review.KindManual, Name: "cli-step"}}},
		},
	}

	pipeline, err := review.ResolvePipeline(config, "cli", "repo")
	if err != nil {
		t.Fatalf("resolve CLI pipeline: %v", err)
	}
	if pipeline.Name != "cli" {
		t.Fatalf("pipeline = %q, want cli", pipeline.Name)
	}

	pipeline, err = review.ResolvePipeline(config, "", "repo")
	if err != nil {
		t.Fatalf("resolve repo pipeline: %v", err)
	}
	if pipeline.Name != "repo" {
		t.Fatalf("pipeline = %q, want repo", pipeline.Name)
	}

	pipeline, err = review.ResolvePipeline(review.Config{}, "", "")
	if err != nil {
		t.Fatalf("resolve built-in pipeline: %v", err)
	}
	if pipeline.Name != "default" || pipeline.Steps[0].Name != "local-review" {
		t.Fatalf("pipeline = %#v, want built-in manual", pipeline)
	}
}

func newTestPaths(t *testing.T) state.Paths {
	t.Helper()

	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(
		filepath.Join(root, "config", state.AppName),
		filepath.Join(root, "data", state.AppName),
	)
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	return paths
}
