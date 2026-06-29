package registry_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
)

func TestStoreLoadMissingRegistryIsEmpty(t *testing.T) {
	store := registry.NewStore(newTestPaths(t))

	got, err := store.Load()
	if err != nil {
		t.Fatalf("load missing registry: %v", err)
	}
	if len(got.Repos) != 0 {
		t.Fatalf("repos = %#v, want empty", got.Repos)
	}
}

func TestStoreLoadEmptyRegistryFileIsEmpty(t *testing.T) {
	paths := newTestPaths(t)
	writeDataFile(t, paths, "registry.yaml", "")
	store := registry.NewStore(paths)

	got, err := store.Load()
	if err != nil {
		t.Fatalf("load empty registry: %v", err)
	}
	if len(got.Repos) != 0 {
		t.Fatalf("repos = %#v, want empty", got.Repos)
	}
}

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	paths := newTestPaths(t)
	store := registry.NewStore(paths)
	want := registry.Registry{Repos: []registry.Repo{{
		ID:                   "orpheus",
		Name:                 "orpheus",
		Path:                 filepath.Join(paths.DataRoot, "..", "repos", "orpheus"),
		Remote:               "git@example.com:org/orpheus.git",
		DefaultBranch:        "main",
		BeadsMode:            registry.BeadsModeLocal,
		BeadsPrefix:          "op",
		SummaryGuidance:      "Use sentence-case summaries without a type prefix.",
		SummaryGuidanceStyle: registry.SummaryGuidanceStyleCapitalized,
		TitleTemplate:        "[OPS] {{summary}}",
		ReviewPipeline:       "go-standard",
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
	if !strings.Contains(string(onDisk), "repos:") ||
		!strings.Contains(string(onDisk), "id: orpheus") ||
		!strings.Contains(string(onDisk), "remote: git@example.com:org/orpheus.git") ||
		!strings.Contains(string(onDisk), "default_branch: main") ||
		!strings.Contains(string(onDisk), "beads_mode: local") ||
		!strings.Contains(string(onDisk), "beads_prefix: op") ||
		!strings.Contains(string(onDisk), "summary_guidance: Use sentence-case summaries without a type prefix.") ||
		!strings.Contains(string(onDisk), "summary_guidance_style: capitalized") ||
		!strings.Contains(string(onDisk), "title_template: '[OPS] {{summary}}'") ||
		!strings.Contains(string(onDisk), "review_pipeline: go-standard") {
		t.Fatalf("registry file is not human-editable YAML: %s", onDisk)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	assertRepos(t, got.Repos, want.Repos)
}

func TestStoreLoadRejectsInvalidSummaryGuidanceStyle(t *testing.T) {
	paths := newTestPaths(t)
	writeDataFile(t, paths, "registry.yaml", `repos:
  - id: alpha
    name: alpha
    path: /tmp/alpha
    summary_guidance_style: informal
`)
	store := registry.NewStore(paths)

	_, err := store.Load()

	if err == nil {
		t.Fatal("load registry with invalid summary guidance style succeeded, want error")
	}
	if !strings.Contains(err.Error(), `summary_guidance_style "informal" is invalid`) ||
		!strings.Contains(err.Error(), `expected "typed" or "capitalized"`) {
		t.Fatalf("error = %v, want actionable style validation", err)
	}
}

func TestStoreLoadMalformedRegistry(t *testing.T) {
	paths := newTestPaths(t)
	writeDataFile(t, paths, "registry.yaml", "repos: [unterminated\n")
	store := registry.NewStore(paths)

	_, err := store.Load()
	if err == nil {
		t.Fatal("load malformed registry succeeded, want error")
	}
	if !strings.Contains(err.Error(), "load repo registry") || !strings.Contains(err.Error(), "registry.yaml") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

func TestStoreLoadAcceptsExternalReferenceTitleTemplate(t *testing.T) {
	paths := newTestPaths(t)
	writeDataFile(t, paths, "registry.yaml", `repos:
  - id: alpha
    name: alpha
    path: /tmp/alpha
    title_template: "[{{external_ref}}] {{summary}}"
`)
	store := registry.NewStore(paths)

	got, err := store.Load()

	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if got.Repos[0].TitleTemplate != "[{{external_ref}}] {{summary}}" {
		t.Fatalf("title template = %q, want external reference template", got.Repos[0].TitleTemplate)
	}
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

func TestStoreLoadRejectsUnknownRegistryFields(t *testing.T) {
	paths := newTestPaths(t)
	writeDataFile(t, paths, "registry.yaml", "repos: []\nunknown: true\n")
	store := registry.NewStore(paths)

	_, err := store.Load()
	if err == nil {
		t.Fatal("load registry with unknown field succeeded, want error")
	}
	if !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("error = %v, want unknown field validation", err)
	}
}

func TestRegistryAddRejectsDuplicateIDNameAndPath(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "alpha")
	existing := registry.Registry{Repos: []registry.Repo{{
		ID:   "alpha",
		Name: "Alpha",
		Path: basePath,
	}}}

	tests := []struct {
		name string
		repo registry.Repo
		want string
	}{
		{
			name: "id",
			repo: registry.Repo{ID: "alpha", Name: "Other", Path: filepath.Join(t.TempDir(), "other")},
			want: "duplicate repo id \"alpha\"",
		},
		{
			name: "name",
			repo: registry.Repo{ID: "other", Name: "Alpha", Path: filepath.Join(t.TempDir(), "other")},
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

func TestRegistryAddRejectsDuplicateBeadsPrefix(t *testing.T) {
	existing := registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha",
		Name:        "Alpha",
		Path:        filepath.Join(t.TempDir(), "alpha"),
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "op",
	}}}

	repo := registry.Repo{
		ID:          "beta",
		Name:        "Beta",
		Path:        filepath.Join(t.TempDir(), "beta"),
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "op",
	}

	reg := existing
	err := reg.Add(repo)
	if err == nil {
		t.Fatal("add duplicate beads prefix succeeded, want error")
	}
	if !strings.Contains(err.Error(), "duplicate beads prefix \"op\"") {
		t.Fatalf("error = %v, want duplicate beads prefix", err)
	}
	assertRepos(t, reg.Repos, existing.Repos)
}

func TestRegistryAddRejectsIDNamePrefixCrossCollision(t *testing.T) {
	existing := registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha-id",
		Name:        "Alpha Name",
		Path:        filepath.Join(t.TempDir(), "alpha"),
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "alpha-prefix",
	}}}

	tests := []struct {
		name string
		repo registry.Repo
		want string
	}{
		{
			name: "name collides with existing prefix",
			repo: registry.Repo{ID: "beta-id", Name: "alpha-prefix", Path: filepath.Join(t.TempDir(), "beta")},
			want: "repo name \"alpha-prefix\" collides with repo[0] beads_prefix",
		},
		{
			name: "prefix collides with existing id",
			repo: registry.Repo{ID: "beta-id", Name: "Beta", Path: filepath.Join(t.TempDir(), "beta"), BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "alpha-id"},
			want: "repo beads_prefix \"alpha-id\" collides with repo[0] id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := existing
			err := reg.Add(tt.repo)
			if err == nil {
				t.Fatal("add cross-colliding repo succeeded, want error")
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
			repo: registry.Repo{ID: "alpha", Name: "alpha", Path: filepath.Join(t.TempDir(), "alpha"), BeadsMode: registry.BeadsModeLocal},
			want: "repo beads_prefix is required",
		},
		{
			name: "prefix requires mode",
			repo: registry.Repo{ID: "alpha", Name: "alpha", Path: filepath.Join(t.TempDir(), "alpha"), BeadsPrefix: "op"},
			want: "repo beads_mode is required",
		},
		{
			name: "invalid mode",
			repo: registry.Repo{ID: "alpha", Name: "alpha", Path: filepath.Join(t.TempDir(), "alpha"), BeadsMode: "nearby", BeadsPrefix: "op"},
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
	root := t.TempDir()
	input := filepath.Join(root, "..", filepath.Base(root), "my-repo", ".")

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
		Path:        filepath.Join(t.TempDir(), "alpha"),
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "alpha-prefix",
	}
	beta := registry.Repo{
		ID:          "beta-id",
		Name:        "Beta Repo",
		Path:        filepath.Join(t.TempDir(), "beta"),
		BeadsMode:   registry.BeadsModeManaged,
		BeadsPrefix: "beta-prefix",
	}
	reg := registry.Registry{Repos: []registry.Repo{alpha, beta}}

	tests := []struct {
		name  string
		token string
		want  registry.Repo
	}{
		{name: "id", token: " alpha-id ", want: alpha},
		{name: "display name", token: "Beta Repo", want: beta},
		{name: "beads prefix", token: "alpha-prefix", want: alpha},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reg.Resolve(tt.token)
			if err != nil {
				t.Fatalf("resolve %q: %v", tt.token, err)
			}
			if got != tt.want {
				t.Fatalf("resolve %q = %#v, want %#v", tt.token, got, tt.want)
			}
		})
	}
}

func TestRegistryResolveUnknownIsActionable(t *testing.T) {
	reg := registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha-id",
		Name:        "Alpha Repo",
		Path:        filepath.Join(t.TempDir(), "alpha"),
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "alpha-prefix",
	}}}

	_, err := reg.Resolve("missing")
	if err == nil {
		t.Fatal("resolve unknown repo succeeded, want error")
	}
	if !strings.Contains(err.Error(), "repo \"missing\" is not registered") || !strings.Contains(err.Error(), "orpheus repo list") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

func TestManagedBeadsDirUsesRepoIDUnderDataRoot(t *testing.T) {
	paths := newTestPaths(t)
	store := registry.NewStore(paths)

	got, err := store.ManagedBeadsDir(" alpha ")
	if err != nil {
		t.Fatalf("managed beads dir: %v", err)
	}
	want := filepath.Join(paths.DataRoot, "repos", "alpha", "beads")
	if got != want {
		t.Fatalf("managed beads dir = %q, want %q", got, want)
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
	paths := newTestPaths(t)
	store := registry.NewStore(paths)
	localPath := filepath.Join(t.TempDir(), "local")

	tests := []struct {
		name string
		repo registry.Repo
		want string
	}{
		{
			name: "local mode uses repo path",
			repo: registry.Repo{ID: "local", Name: "Local", Path: localPath, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "lp"},
			want: localPath,
		},
		{
			name: "managed mode uses data root",
			repo: registry.Repo{ID: "managed", Name: "Managed", Path: filepath.Join(t.TempDir(), "managed"), BeadsMode: registry.BeadsModeManaged, BeadsPrefix: "mp"},
			want: filepath.Join(paths.DataRoot, "repos", "managed", "beads"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.BeadsDir(tt.repo)
			if err != nil {
				t.Fatalf("beads dir: %v", err)
			}
			if got != tt.want {
				t.Fatalf("beads dir = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStoreBeadsDirRejectsRepoWithoutBeadsMode(t *testing.T) {
	store := registry.NewStore(newTestPaths(t))
	repo := registry.Repo{ID: "alpha", Name: "alpha", Path: filepath.Join(t.TempDir(), "alpha")}

	_, err := store.BeadsDir(repo)
	if err == nil {
		t.Fatal("beads dir without mode succeeded, want error")
	}
	if !strings.Contains(err.Error(), "has no beads_mode") || !strings.Contains(err.Error(), "registry.yaml") {
		t.Fatalf("error is not actionable: %v", err)
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
		if got[i] != want[i] {
			t.Fatalf("repo[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}
