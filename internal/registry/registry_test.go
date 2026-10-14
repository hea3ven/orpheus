package registry_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	paths := newTestPaths(t)
	store := registry.NewStore(paths)
	want := registry.Registry{Repos: []registry.Repo{{
		ID:                     "orpheus",
		Name:                   "orpheus",
		Path:                   filepath.Join(testutil.CanonicalTempDir(t), "repos", "orpheus"),
		Remote:                 "git@example.com:org/orpheus.git",
		DefaultBranch:          "main",
		BeadsMode:              registry.BeadsModeLocal,
		BeadsPrefix:            "op",
		SummaryGuidance:        "Use sentence-case summaries without a type prefix.",
		SummaryGuidanceStyle:   registry.SummaryGuidanceStyleCapitalized,
		TitleTemplate:          "[{{external_ref}}] {{summary}}",
		IncludePRReviewProcess: boolPtr(false),
		ReviewPipeline:         "go-standard",
		ReviewPipelineAliases: map[string]string{
			"quick": "go-standard",
		},
	}}}
	want.Repos[0].Path = filepath.Clean(want.Repos[0].Path)

	if err := store.Save(want); err != nil {
		t.Fatalf("save registry: %v", err)
	}

	registryPath, err := paths.DataPath("registry.yaml")
	if err != nil {
		t.Fatalf("registry path: %v", err)
	}
	onDisk, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry file: %v", err)
	}
	if !strings.Contains(string(onDisk), "repos:") || !strings.Contains(string(onDisk), "review_pipeline_aliases:") {
		t.Fatalf("registry file is not human-editable YAML: %s", onDisk)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	assertRepos(t, got.Repos, want.Repos)
}

func TestStoreLoadsRegistryAndRejectsInvalidConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name, contents string
		missing        bool
		wantErrors     []string
	}{
		{name: "missing", missing: true},
		{name: "empty"},
		{name: "valid empty", contents: "repos: []\n"},
		{name: "invalid alias", contents: "repos:\n  - id: alpha\n    name: alpha\n    path: /fixture/alpha\n    review_pipeline_aliases:\n      \" \": standard\n", wantErrors: []string{"review_pipeline_aliases name is required"}},
		{name: "invalid summary style", contents: "repos:\n  - id: alpha\n    name: alpha\n    path: /fixture/alpha\n    summary_guidance_style: informal\n", wantErrors: []string{`summary_guidance_style "informal" is invalid`, `expected "typed" or "capitalized"`}},
		{name: "malformed YAML", contents: "repos: [unterminated\n", wantErrors: []string{"load repo registry", "registry.yaml"}},
		{name: "unknown field", contents: "repos: []\nunknown: true\n", wantErrors: []string{"field unknown not found"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			paths := newTestPaths(t)
			if !tt.missing {
				writeDataFile(t, paths, "registry.yaml", tt.contents)
			}
			got, err := registry.NewStore(paths).Load()
			if len(tt.wantErrors) != 0 {
				if err == nil {
					t.Fatal("invalid registry loaded successfully")
				}
				for _, want := range tt.wantErrors {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("load error = %v, want %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("load registry: %v", err)
			}
			if len(got.Repos) != 0 {
				t.Fatalf("repos = %#v, want empty", got.Repos)
			}
		})
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func TestRepoEffectivePublicationPolicyAppliesCompatibilityDefaults(t *testing.T) {
	tests := []struct {
		name string
		repo registry.Repo
		want registry.PublicationPolicy
	}{
		{
			name: "legacy repo",
			repo: registry.Repo{},
			want: registry.PublicationPolicy{SummaryGuidanceStyle: registry.SummaryGuidanceStyleTyped},
		},
		{
			name: "custom guidance overrides configured style",
			repo: registry.Repo{
				SummaryGuidance:      "  Write a concise release note.  ",
				SummaryGuidanceStyle: registry.SummaryGuidanceStyleCapitalized,
				TitleTemplate:        "  [OPS] {{summary}}  ",
			},
			want: registry.PublicationPolicy{
				SummaryGuidance:      "Write a concise release note.",
				SummaryGuidanceStyle: registry.SummaryGuidanceStyleCapitalized,
				TitleTemplate:        "[OPS] {{summary}}",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.repo.EffectivePublicationPolicy(); got != test.want {
				t.Fatalf("effective policy = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestRegistryAddRejectsDuplicateIDNameAndPath(t *testing.T) {
	basePath := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	existing := registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha",
		Name:        "Alpha",
		Path:        basePath,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "op",
	}}}

	tests := []struct {
		name string
		repo registry.Repo
		want string
	}{
		{name: "duplicate prefix", repo: registry.Repo{ID: "beta", Name: "Beta", Path: "/fixture/beta", BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op"}, want: "duplicate task prefix \"op\""},
		{name: "name collides with prefix", repo: registry.Repo{ID: "beta", Name: "op", Path: "/fixture/beta"}, want: "repo name \"op\" collides with repo[0] task prefix"},
		{name: "prefix collides with id", repo: registry.Repo{ID: "beta", Name: "Beta", Path: "/fixture/beta", BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "alpha"}, want: "repo task prefix \"alpha\" collides with repo[0] id"},
		{
			name: "id",
			repo: registry.Repo{ID: "alpha", Name: "Other", Path: filepath.Join(testutil.CanonicalTempDir(t), "other")},
			want: "duplicate repo id \"alpha\"",
		},
		{
			name: "name",
			repo: registry.Repo{ID: "other", Name: "Alpha", Path: filepath.Join(testutil.CanonicalTempDir(t), "other")},
			want: "duplicate repo name \"Alpha\"",
		},
		{
			name: "path",
			repo: registry.Repo{ID: "other", Name: "Other", Path: filepath.Join(basePath, "..", "alpha")},
			want: "duplicate repo path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := existing
			err := reg.Add(tt.repo)
			if err == nil {
				t.Fatal("add duplicate repo succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			assertRepos(t, reg.Repos, existing.Repos)
		})
	}
}

func TestRegistryAddValidatesBeadsModeAndPrefixTogether(t *testing.T) {
	tests := []struct {
		name string
		repo registry.Repo
		want string
	}{
		{
			name: "local mode requires prefix",
			repo: registry.Repo{ID: "alpha", Name: "alpha", Path: filepath.Join(testutil.CanonicalTempDir(t), "alpha"), BeadsMode: registry.BeadsModeLocal},
			want: "repo beads_prefix is required",
		},
		{
			name: "prefix requires mode",
			repo: registry.Repo{ID: "alpha", Name: "alpha", Path: filepath.Join(testutil.CanonicalTempDir(t), "alpha"), BeadsPrefix: "op"},
			want: "repo beads_mode is required",
		},
		{
			name: "invalid mode",
			repo: registry.Repo{ID: "alpha", Name: "alpha", Path: filepath.Join(testutil.CanonicalTempDir(t), "alpha"), BeadsMode: "nearby", BeadsPrefix: "op"},
			want: "repo beads_mode \"nearby\" is invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := registry.Registry{}
			err := reg.Add(tt.repo)
			if err == nil {
				t.Fatal("add invalid repo succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestNewRepoFromPathDerivesIdentityAndNormalizesPath(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	input := root + "/../" + filepath.Base(root) + "/my-repo/."

	got, err := registry.NewRepoFromPath(input)
	if err != nil {
		t.Fatalf("new repo from path: %v", err)
	}

	wantPath := filepath.Join(root, "my-repo")
	if got.ID != "my-repo" || got.Name != "my-repo" || got.Path != wantPath {
		t.Fatalf("repo = %#v, want id/name my-repo path %q", got, wantPath)
	}
}

func TestNewRepoFromPathRejectsBlankAndRoot(t *testing.T) {
	tests := []string{"", "   ", filepath.VolumeName(filepath.Clean(string(os.PathSeparator))) + string(os.PathSeparator)}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := registry.NewRepoFromPath(input)
			if err == nil {
				t.Fatal("new repo from invalid path succeeded, want error")
			}
		})
	}
}

func TestStoreSaveRejectsInvalidRegistry(t *testing.T) {
	store := registry.NewStore(newTestPaths(t))

	err := store.Save(registry.Registry{Repos: []registry.Repo{{ID: "missing-path", Name: "missing-path"}}})
	if err == nil {
		t.Fatal("save invalid registry succeeded, want error")
	}
	if !strings.Contains(err.Error(), "save repo registry") || !strings.Contains(err.Error(), "repo path is required") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

func TestRegistryResolveByIDNameAndBeadsPrefix(t *testing.T) {
	alpha := registry.Repo{
		ID:          "alpha-id",
		Name:        "Alpha Repo",
		Path:        filepath.Join(testutil.CanonicalTempDir(t), "alpha"),
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "alpha-prefix",
	}
	beta := registry.Repo{
		ID:          "beta-id",
		Name:        "Beta Repo",
		Path:        filepath.Join(testutil.CanonicalTempDir(t), "beta"),
		BeadsMode:   registry.BeadsModeManaged,
		BeadsPrefix: "beta-prefix",
	}
	reg := registry.Registry{Repos: []registry.Repo{alpha, beta}}

	tests := []struct {
		name      string
		token     string
		want      registry.Repo
		wantError bool
	}{
		{name: "unknown", token: "missing", wantError: true},
		{name: "id", token: " alpha-id ", want: alpha},
		{name: "display name", token: "Beta Repo", want: beta},
		{name: "beads prefix", token: "alpha-prefix", want: alpha},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reg.Resolve(tt.token)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), "repo \"missing\" is not registered") || !strings.Contains(err.Error(), "orpheus repo list") {
					t.Fatalf("resolve error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve %q: %v", tt.token, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("resolve %q = %#v, want %#v", tt.token, got, tt.want)
			}
		})
	}
}

func TestManagedBeadsDirRejectsUnsafeRepoIDs(t *testing.T) {
	paths := newTestPaths(t)
	for _, repoID := range []string{"", ".", "..", "nested/alpha", `nested\\alpha`} {
		t.Run(repoID, func(t *testing.T) {
			_, err := registry.ManagedBeadsDir(paths, repoID)
			if err == nil {
				t.Fatal("managed beads dir succeeded, want error")
			}
		})
	}
}

func TestStoreBeadsDirUsesRepoMode(t *testing.T) {
	paths, dataRoot := newTestPathsWithDataRoot(t)
	store := registry.NewStore(paths)
	localPath := filepath.Join(testutil.CanonicalTempDir(t), "local")

	tests := []struct {
		name      string
		repo      registry.Repo
		want      string
		wantError bool
	}{
		{name: "missing mode", repo: registry.Repo{ID: "alpha", Name: "alpha", Path: "/fixture/alpha"}, wantError: true},
		{
			name: "local mode uses repo path",
			repo: registry.Repo{ID: "local", Name: "Local", Path: localPath, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "lp"},
			want: localPath,
		},
		{
			name: "managed mode uses data root",
			repo: registry.Repo{ID: " managed ", Name: "Managed", Path: filepath.Join(testutil.CanonicalTempDir(t), "managed"), BeadsMode: registry.BeadsModeManaged, BeadsPrefix: "mp"},
			want: filepath.Join(dataRoot, "repos", "managed", "beads"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.BeadsDir(tt.repo)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), "has no beads_mode") || !strings.Contains(err.Error(), "registry.yaml") {
					t.Fatalf("beads dir error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("beads dir: %v", err)
			}
			if got != tt.want {
				t.Fatalf("beads dir = %q, want %q", got, tt.want)
			}
		})
	}
}

func newTestPaths(t *testing.T) state.Paths {
	t.Helper()
	paths, _ := newTestPathsWithDataRoot(t)
	return paths
}

func newTestPathsWithDataRoot(t *testing.T) (state.Paths, string) {
	t.Helper()

	root := testutil.CanonicalTempDir(t)
	dataRoot := filepath.Join(root, "data")
	paths, err := state.NewPaths(filepath.Join(root, "config"), dataRoot)
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	return paths, dataRoot
}

func writeDataFile(t *testing.T, paths state.Paths, rel string, content string) {
	t.Helper()

	path, err := paths.DataPath(rel)
	if err != nil {
		t.Fatalf("data path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir data file parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
}

func assertRepos(t *testing.T, got []registry.Repo, want []registry.Repo) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("repo count = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range got {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("repo[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}
