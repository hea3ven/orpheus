package taskbranch_test

import (
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskbranch"
)

func TestResolveTemplatePrecedence(t *testing.T) {
	tests := []struct {
		name       string
		repository string
		global     string
		want       string
	}{
		{name: "repository overrides global", repository: "repo/{{task_id}}", global: "global/{{task_id}}", want: "repo/{{task_id}}"},
		{name: "global applies when repository is unset", global: "global/{{task_id}}", want: "global/{{task_id}}"},
		{name: "compatibility default", want: taskbranch.DefaultTemplate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskbranch.ResolveTemplate(tt.repository, tt.global); got != tt.want {
				t.Fatalf("ResolveTemplate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderNormalizesSupportedValues(t *testing.T) {
	got, err := taskbranch.Render("feature/{{external_ref}}/{{task_title}}-{{task_id}}", taskbranch.Values{
		TaskID:      "OPS-42",
		ExternalRef: " Jira: OPS/42 ",
		TaskTitle:   "Add global & repository branches!",
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	want := "feature/Jira-OPS-42/Add-global-repository-branches-OPS-42"
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRequiresExternalRef(t *testing.T) {
	if !taskbranch.RequiresExternalRef("feature/{{external_ref}}") {
		t.Fatal("template external reference was not detected")
	}
	if taskbranch.RequiresExternalRef("feature/{{task_id}}") {
		t.Fatal("task ID template incorrectly requires external reference")
	}
}

func TestRenderPreservesCompatibilityDefault(t *testing.T) {
	got, err := taskbranch.Render("", taskbranch.Values{TaskID: "op-7"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got != "orpheus/op-7" {
		t.Fatalf("Render() = %q, want compatibility branch", got)
	}
}

func TestRenderRejectsMissingPlaceholderValuesAndInvalidRefs(t *testing.T) {
	tests := []struct {
		name     string
		template string
		values   taskbranch.Values
		want     string
	}{
		{name: "task ID", template: "feature/{{task_id}}", want: "requires a task ID"},
		{name: "external ref", template: "feature/{{external_ref}}", values: taskbranch.Values{TaskID: "op-1"}, want: "external reference"},
		{name: "task title", template: "feature/{{task_title}}", values: taskbranch.Values{TaskID: "op-1"}, want: "requires a task title"},
		{name: "invalid literal ref", template: "feature//{{task_id}}", values: taskbranch.Values{TaskID: "op-1"}, want: "empty path component"},
		{name: "dot-prefixed component", template: "feature/.{{task_id}}", values: taskbranch.Values{TaskID: "op-1"}, want: "invalid Git ref syntax"},
		{name: "reserved HEAD", template: "HEAD", values: taskbranch.Values{TaskID: "op-1"}, want: "invalid Git ref syntax"},
		{name: "reserved HEAD from task title", template: "{{task_title}}", values: taskbranch.Values{TaskID: "op-1", TaskTitle: "HEAD"}, want: "invalid Git ref syntax"},
		{name: "unsupported placeholder", template: "feature/{{unknown}}", values: taskbranch.Values{TaskID: "op-1"}, want: "unsupported placeholder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := taskbranch.Render(tt.template, tt.values)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Render() error = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestValidateBranchRejectsGitRefSyntax keeps the pure pre-mutation validation
// aligned with the ref rules enforced by git check-ref-format --branch.
func TestValidateBranchRejectsGitRefSyntax(t *testing.T) {
	branches := []string{
		"-feature/op-1",
		".feature/op-1",
		"feature/.op-1",
		"feature/op-1.",
		"feature/op-1.lock",
		"feature//op-1",
		"feature/../op-1",
		"feature/op..1",
		"feature/@{op-1",
		"@",
		"HEAD",
		"feature/op 1",
		"feature/op~1",
		"feature/op\\1",
	}
	for _, branch := range branches {
		t.Run(branch, func(t *testing.T) {
			if err := taskbranch.ValidateBranch(branch); err == nil {
				t.Fatalf("ValidateBranch(%q) error = nil, want Git ref syntax error", branch)
			}
		})
	}
}

func TestLoadConfigReadsAndValidatesGlobalTemplate(t *testing.T) {
	paths, err := state.NewPaths(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.WriteConfigYAML("config.yaml", map[string]any{
		"tasks":  map[string]any{"branch_template": "global/{{task_title}}"},
		"agents": map[string]any{"defaults": map[string]any{"implementer": "ignored"}},
	}); err != nil {
		t.Fatal(err)
	}
	config, err := taskbranch.LoadConfig(paths)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.Template != "global/{{task_title}}" {
		t.Fatalf("template = %q", config.Template)
	}

	if err := paths.WriteConfigYAML("config.yaml", map[string]any{
		"tasks": map[string]any{"branch_template": "global/{{unknown}}"},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = taskbranch.LoadConfig(paths)
	if err == nil || !strings.Contains(err.Error(), "unsupported placeholder") {
		t.Fatalf("LoadConfig() error = %v, want validation error", err)
	}
}
