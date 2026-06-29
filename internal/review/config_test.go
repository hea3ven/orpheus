package review_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
)

func TestLoadConfigValidatesAndResolvesPipelines(t *testing.T) {
	paths := newTestPaths(t)
	if err := paths.WriteConfigYAML(review.ConfigFile, map[string]any{
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
	step := pipeline.Steps[0]
	if step.Command != "go" || strings.Join(step.Args, " ") != "test ./..." {
		t.Fatalf("step = %#v, want direct command and args", step)
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
			if err := paths.WriteConfigYAML(review.ConfigFile, test.data); err != nil {
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

	root := t.TempDir()
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	return paths
}
