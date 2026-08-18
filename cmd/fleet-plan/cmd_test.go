package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CampusTech/fleet-plan/internal/git"
	"github.com/CampusTech/fleet-plan/internal/parser"
)

// ---------- version command ----------

func TestVersionCommand(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	root := buildRootCmd()
	root.SetArgs([]string{"version"})
	err := root.Execute()

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	os.Stdout = old

	if err != nil {
		t.Fatalf("version command error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "fleet-plan") {
		t.Errorf("version output should contain 'fleet-plan', got:\n%s", output)
	}
}

// ---------- global flags ----------

func TestGlobalFlags(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		check func(t *testing.T)
	}{
		{
			name: "format flag",
			args: []string{"--format", "json", "version"},
			check: func(t *testing.T) {
				if flagFormat != "json" {
					t.Errorf("flagFormat: got %q, want json", flagFormat)
				}
			},
		},
		{
			name: "repo flag",
			args: []string{"--repo", "/custom/path", "version"},
			check: func(t *testing.T) {
				if flagRepo != "/custom/path" {
					t.Errorf("flagRepo: got %q", flagRepo)
				}
			},
		},
		{
			name: "no-color flag",
			args: []string{"--no-color", "version"},
			check: func(t *testing.T) {
				if !flagNoColor {
					t.Error("flagNoColor should be true")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flagRepo = "."
			flagFormat = "terminal"
			flagNoColor = false
			flagVerbose = false

			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			root := buildRootCmd()
			root.SetArgs(tt.args)
			root.Execute()

			w.Close()
			var buf bytes.Buffer
			buf.ReadFrom(r)
			os.Stdout = old

			tt.check(t)
		})
	}
}

// ---------- root flags include --team, --default, --verbose ----------

func TestRootFlagsIncludeAllFlags(t *testing.T) {
	root := buildRootCmd()
	root.SetArgs([]string{"--help"})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := root.Execute()

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	os.Stdout = old

	if err != nil {
		t.Fatalf("--help error: %v", err)
	}

	output := buf.String()
	for _, flag := range []string{"--team", "--git", "--base", "--env", "--heading", "--verbose", "--detailed-exitcodes"} {
		if !strings.Contains(output, flag) {
			t.Errorf("help should mention %s, got:\n%s", flag, output)
		}
	}
}

// ---------- requires auth ----------

func TestRequiresAuth(t *testing.T) {
	t.Setenv("FLEET_URL", "")
	t.Setenv("FLEET_TOKEN", "")
	t.Setenv("HOME", t.TempDir())

	root := buildRootCmd()
	root.SetArgs([]string{"--repo", t.TempDir()})
	err := root.Execute()
	if err == nil {
		t.Error("expected error when auth is missing")
	}
	if !strings.Contains(err.Error(), "missing Fleet server URL") {
		t.Errorf("expected missing URL error, got: %v", err)
	}
}

// ---------- resolveDefaultFile ----------

func TestResolveDefaultFile(t *testing.T) {
	writeRepo := func(t *testing.T) string {
		t.Helper()
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "base.yml"),
			[]byte("org_settings:\n  server_settings:\n    server_url: https://base.example.com\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "canary.yml"),
			[]byte("org_settings:\n  server_settings:\n    server_url: https://canary.example.com\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return repo
	}

	t.Run("neither base nor env auto-detects", func(t *testing.T) {
		path, cleanup, err := resolveDefaultFile(t.TempDir(), "", "")
		if err != nil {
			t.Fatalf("resolveDefaultFile: %v", err)
		}
		if path != "" || cleanup != nil {
			t.Errorf("got path=%q cleanup=%v, want empty path and nil cleanup", path, cleanup != nil)
		}
	})

	t.Run("base without env is an error", func(t *testing.T) {
		if _, _, err := resolveDefaultFile(t.TempDir(), "base.yml", ""); err == nil ||
			!strings.Contains(err.Error(), "must be used together") {
			t.Fatalf("expected must-be-used-together error, got %v", err)
		}
	})

	t.Run("env without base is an error", func(t *testing.T) {
		if _, _, err := resolveDefaultFile(t.TempDir(), "", "canary.yml"); err == nil ||
			!strings.Contains(err.Error(), "must be used together") {
			t.Fatalf("expected must-be-used-together error, got %v", err)
		}
	})

	t.Run("merges into the repo and cleans up", func(t *testing.T) {
		repo := writeRepo(t)

		path, cleanup, err := resolveDefaultFile(repo, "base.yml", "canary.yml")
		if err != nil {
			t.Fatalf("resolveDefaultFile: %v", err)
		}
		if cleanup == nil {
			t.Fatal("expected a cleanup func")
		}

		// The merged file must live inside the repo so the parser resolves
		// relative path: refs against the repo root.
		if filepath.Dir(path) != repo {
			t.Errorf("merged file %q is not in repo %q", path, repo)
		}
		// And it must match the .gitignore pattern that keeps a leaked file
		// from being committed.
		if base := filepath.Base(path); !strings.HasPrefix(base, ".fleet-plan-default-") ||
			!strings.HasSuffix(base, ".yml") {
			t.Errorf("temp file name %q does not match .fleet-plan-default-*.yml", base)
		}

		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading merged file: %v", err)
		}
		if !strings.Contains(string(content), "canary.example.com") {
			t.Errorf("overlay did not win the merge:\n%s", content)
		}

		cleanup()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("cleanup did not remove %q (err=%v)", path, err)
		}
	})

	t.Run("absolute paths are used as given", func(t *testing.T) {
		repo := writeRepo(t)
		path, cleanup, err := resolveDefaultFile(repo,
			filepath.Join(repo, "base.yml"), filepath.Join(repo, "canary.yml"))
		if err != nil {
			t.Fatalf("resolveDefaultFile: %v", err)
		}
		defer cleanup()
		if path == "" {
			t.Error("expected a merged file path")
		}
	})

	t.Run("missing base file surfaces the merge error", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "canary.yml"), []byte("org_settings: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		path, _, err := resolveDefaultFile(repo, "missing.yml", "canary.yml")
		if err == nil {
			t.Fatal("expected an error for a missing base file")
		}
		if !strings.Contains(err.Error(), "merging base+env") {
			t.Errorf("unexpected error: %v", err)
		}
		// The temp file must not be left behind on the failure path.
		if path != "" {
			t.Errorf("expected empty path on error, got %q", path)
		}
		entries, err := os.ReadDir(repo)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".fleet-plan-default-") {
				t.Errorf("temp file %q left behind after a failed merge", e.Name())
			}
		}
	})
}

// ---------- resolveBaselineDefault ----------

func TestResolveBaselineDefault(t *testing.T) {
	t.Run("no base+env falls back to the baseline default.yml", func(t *testing.T) {
		baseRoot := t.TempDir()
		want := filepath.Join(baseRoot, "default.yml")
		if err := os.WriteFile(want, []byte("org_settings: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := resolveBaselineDefault(baseRoot, t.TempDir(), "", ""); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("no base+env and no default.yml returns empty", func(t *testing.T) {
		if got := resolveBaselineDefault(t.TempDir(), t.TempDir(), "", ""); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("merges the baseline base.yml with the branch overlay", func(t *testing.T) {
		baseRoot, repoRoot := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(baseRoot, "base.yml"),
			[]byte("org_settings:\n  server_settings:\n    server_url: https://old.example.com\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoRoot, "canary.yml"),
			[]byte("org_settings:\n  server_settings:\n    server_url: https://canary.example.com\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		got := resolveBaselineDefault(baseRoot, repoRoot, "base.yml", "canary.yml")
		if got != filepath.Join(baseRoot, "default.yml") {
			t.Fatalf("got %q, want the baseline default.yml", got)
		}
		content, err := os.ReadFile(got)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "canary.example.com") {
			t.Errorf("branch overlay did not win:\n%s", content)
		}
	})

	t.Run("missing baseline base.yml returns empty", func(t *testing.T) {
		repoRoot := t.TempDir()
		if err := os.WriteFile(filepath.Join(repoRoot, "canary.yml"), []byte("org_settings: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := resolveBaselineDefault(t.TempDir(), repoRoot, "base.yml", "canary.yml"); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("missing branch overlay returns empty", func(t *testing.T) {
		baseRoot := t.TempDir()
		if err := os.WriteFile(filepath.Join(baseRoot, "base.yml"), []byte("org_settings: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := resolveBaselineDefault(baseRoot, t.TempDir(), "base.yml", "missing.yml"); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// ---------- buildHeading ----------

func TestBuildHeading(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://fleet.example.com", "Planned changes for [fleet.example.com](https://fleet.example.com)"},
		{"http://fleet.example.com", "Planned changes for [http://fleet.example.com](http://fleet.example.com)"},
	}
	for _, tt := range tests {
		if got := buildHeading(tt.url); got != tt.want {
			t.Errorf("buildHeading(%q): got %q, want %q", tt.url, got, tt.want)
		}
	}
}

// ---------- resolveCIScope ----------

func TestResolveCIScope(t *testing.T) {
	t.Run("unknown platform runs a full diff", func(t *testing.T) {
		defaultFile := ""
		scope, skip := resolveCIScope(git.Env{}, t.TempDir(), "", &defaultFile, nil)
		if skip {
			t.Error("skip: got true, want false")
		}
		if len(scope.Teams) != 0 || scope.IncludeGlobal {
			t.Errorf("expected an empty scope, got %+v", scope)
		}
	})

	t.Run("changed-file lookup failure runs a full diff", func(t *testing.T) {
		// A GitHub env with no token fails gitHubReady, and with no base SHA
		// or target branch the git diff fallback fails too.
		ci := git.Env{Platform: git.PlatformGitHub, GitHubAPIURL: "https://api.github.com", GitHubRepo: "o/r", GitHubPRNumber: "1"}
		defaultFile := ""
		scope, skip := resolveCIScope(ci, t.TempDir(), "", &defaultFile, nil)
		if skip {
			t.Error("skip: got true, want false")
		}
		if len(scope.Teams) != 0 {
			t.Errorf("expected an empty scope, got %+v", scope)
		}
	})
}

// ---------- end-to-end run against a stubbed Fleet API ----------

// stubFleetAPI answers every GET with an empty JSON object, which the client
// decodes into zero-valued state. Everything in the repo then reads as ADDED,
// which is enough to exercise runDiff end to end without a live Fleet.
func stubFleetAPI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("fleet-plan issued a %s request to %s; the client must be read-only", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runCLI(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()

	old := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stdout = w

	root := buildRootCmd()
	root.SetArgs(args)
	err = root.Execute()

	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, copyErr := buf.ReadFrom(r); copyErr != nil {
		t.Fatal(copyErr)
	}
	return buf.String(), err
}

func TestRunDiffAgainstStubbedFleet(t *testing.T) {
	repo := filepath.Join("..", "..", "testdata")

	tests := []struct {
		name      string
		format    string
		wantInOut []string
	}{
		{
			name:      "terminal",
			format:    "terminal",
			wantInOut: []string{"Workstations"},
		},
		{
			name:      "json",
			format:    "json",
			wantInOut: []string{`"team"`, `"added"`},
		},
		{
			name:      "markdown",
			format:    "markdown",
			wantInOut: []string{"| Change | Team | Type | Resource | Details |", "ADDED"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := stubFleetAPI(t)
			t.Setenv("FLEET_PLAN_INSECURE", "1")
			t.Setenv("FLEET_URL", srv.URL)
			t.Setenv("FLEET_TOKEN", "test-token")
			t.Setenv("HOME", t.TempDir())

			out, err := runCLI(t, "--repo", repo, "--format", tt.format, "--no-color")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			for _, want := range tt.wantInOut {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestRunDiffDetailedExitCodeReturnsSentinel(t *testing.T) {
	srv := stubFleetAPI(t)
	t.Setenv("FLEET_PLAN_INSECURE", "1")
	t.Setenv("FLEET_URL", srv.URL)
	t.Setenv("FLEET_TOKEN", "test-token")
	t.Setenv("HOME", t.TempDir())

	_, err := runCLI(t, "--repo", filepath.Join("..", "..", "testdata"),
		"--detailed-exitcodes", "--no-color")
	// The sentinel, not a real failure: main turns it into exit code 2 only
	// after runDiff's deferred temp-file cleanup has run.
	if !errors.Is(err, errChangesDetected) {
		t.Fatalf("expected errChangesDetected, got %v", err)
	}
}

func TestRunDiffRepoPathErrors(t *testing.T) {
	tests := []struct {
		name    string
		repo    func(t *testing.T) string
		wantErr string
	}{
		{
			name:    "missing path",
			repo:    func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") },
			wantErr: "does not exist",
		},
		{
			name: "path is a file",
			repo: func(t *testing.T) string {
				f := filepath.Join(t.TempDir(), "file.yml")
				if err := os.WriteFile(f, []byte("x: 1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return f
			},
			wantErr: "is not a directory",
		},
		{
			name: "repo with an empty teams dir has no teams",
			repo: func(t *testing.T) string {
				root := t.TempDir()
				if err := os.MkdirAll(filepath.Join(root, "teams"), 0o755); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantErr: "no teams found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := stubFleetAPI(t)
			t.Setenv("FLEET_PLAN_INSECURE", "1")
			t.Setenv("FLEET_URL", srv.URL)
			t.Setenv("FLEET_TOKEN", "test-token")
			t.Setenv("HOME", t.TempDir())

			_, err := runCLI(t, "--repo", tt.repo(t))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunDiffUnknownTeamIsAnError(t *testing.T) {
	srv := stubFleetAPI(t)
	t.Setenv("FLEET_PLAN_INSECURE", "1")
	t.Setenv("FLEET_URL", srv.URL)
	t.Setenv("FLEET_TOKEN", "test-token")
	t.Setenv("HOME", t.TempDir())

	_, err := runCLI(t, "--repo", filepath.Join("..", "..", "testdata"), "--team", "No Such Team")
	if err == nil || !strings.Contains(err.Error(), "no teams matching") {
		t.Fatalf("expected no-teams-matching error, got %v", err)
	}
}

// gitLabStub answers the two GitLab endpoints --git mode uses: the MR changes
// list and the MR notes collection.
func gitLabStub(t *testing.T, changedFiles []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/changes"):
			changes := make([]map[string]string, 0, len(changedFiles))
			for _, f := range changedFiles {
				changes = append(changes, map[string]string{"new_path": f, "old_path": f})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"changes": changes})
		case strings.HasSuffix(r.URL.Path, "/notes") && r.Method == http.MethodGet:
			fmt.Fprint(w, `[]`)
		default:
			fmt.Fprint(w, `{"id":9}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setGitLabEnv(t *testing.T, apiURL string) {
	t.Helper()
	t.Setenv("CI_MERGE_REQUEST_IID", "2")
	t.Setenv("CI_API_V4_URL", apiURL)
	t.Setenv("CI_PROJECT_ID", "1")
	t.Setenv("CI_PROJECT_URL", "https://gitlab.example.com/group/repo")
	t.Setenv("FLEET_PLAN_BOT", "glpat-token")
	t.Setenv("CI_MERGE_REQUEST_DIFF_BASE_SHA", "")
	t.Setenv("CI_MERGE_REQUEST_TARGET_BRANCH_NAME", "")
	t.Setenv("GITHUB_EVENT_NAME", "")
}

func TestRunDiffGitModePostsComment(t *testing.T) {
	fleet := stubFleetAPI(t)
	gitlab := gitLabStub(t, []string{"teams/workstations.yml"})

	t.Setenv("FLEET_PLAN_INSECURE", "1")
	t.Setenv("FLEET_URL", fleet.URL)
	t.Setenv("FLEET_TOKEN", "test-token")
	t.Setenv("HOME", t.TempDir())
	setGitLabEnv(t, gitlab.URL)

	out, err := runCLI(t, "--repo", filepath.Join("..", "..", "testdata"),
		"--git", "--format", "markdown", "--no-color")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// --git derives the heading from the Fleet URL and the marker used to find
	// an existing note on a later run.
	for _, want := range []string{"Planned changes for", "<!-- fleet-plan-marker -->", "Workstations"} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown output missing %q:\n%s", want, out)
		}
	}
}

func TestRunDiffGitModeSkipsWhenNoFleetFilesChanged(t *testing.T) {
	fleet := stubFleetAPI(t)
	gitlab := gitLabStub(t, []string{"README.md", ".gitlab-ci.yml"})

	t.Setenv("FLEET_PLAN_INSECURE", "1")
	t.Setenv("FLEET_URL", fleet.URL)
	t.Setenv("FLEET_TOKEN", "test-token")
	t.Setenv("HOME", t.TempDir())
	setGitLabEnv(t, gitlab.URL)

	out, err := runCLI(t, "--repo", filepath.Join("..", "..", "testdata"), "--git", "--no-color")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(out, "ADDED") {
		t.Errorf("expected no diff output when no fleet files changed:\n%s", out)
	}
}

func TestResolveCIScopeFromChangedFiles(t *testing.T) {
	tests := []struct {
		name          string
		changedFiles  []string
		wantSkip      bool
		wantTeams     int
		wantGlobal    bool
		wantDefaulted bool
	}{
		{
			name:         "team file resolves that team",
			changedFiles: []string{"teams/workstations.yml"},
			wantTeams:    1,
		},
		{
			// base.yml (not default.yml) is what ResolveScope treats as global.
			name:          "base.yml pulls in global config",
			changedFiles:  []string{"base.yml"},
			wantGlobal:    true,
			wantDefaulted: true,
		},
		{
			name:         "unrelated files skip the diff",
			changedFiles: []string{"README.md"},
			wantSkip:     true,
		},
	}

	repo := filepath.Join("..", "..", "testdata")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitlab := gitLabStub(t, tt.changedFiles)
			t.Setenv("FLEET_PLAN_INSECURE", "1")
			setGitLabEnv(t, gitlab.URL)

			defaultFile := ""
			scope, skip := resolveCIScope(git.Detect(), repo, "", &defaultFile, nil)

			if skip != tt.wantSkip {
				t.Fatalf("skip: got %v, want %v", skip, tt.wantSkip)
			}
			if len(scope.Teams) != tt.wantTeams {
				t.Errorf("teams: got %v, want %d", scope.Teams, tt.wantTeams)
			}
			if scope.IncludeGlobal != tt.wantGlobal {
				t.Errorf("IncludeGlobal: got %v, want %v", scope.IncludeGlobal, tt.wantGlobal)
			}
			if tt.wantDefaulted && defaultFile == "" {
				t.Error("expected defaultFile to be set to the repo default.yml")
			}
		})
	}
}

// ---------- exit codes ----------

func TestRunExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args func(t *testing.T, fleetURL string) []string
		want int
	}{
		{
			name: "version succeeds",
			args: func(_ *testing.T, _ string) []string { return []string{"version"} },
			want: 0,
		},
		{
			name: "no changes to report",
			args: func(t *testing.T, _ string) []string {
				// An empty teams dir parses cleanly with nothing to diff.
				root := t.TempDir()
				if err := os.MkdirAll(filepath.Join(root, "teams"), 0o755); err != nil {
					t.Fatal(err)
				}
				return []string{"--repo", root, "--no-color"}
			},
			want: 1, // "no teams found" is a usage error, not a clean run
		},
		{
			name: "detailed exit code reports changes as 2",
			args: func(_ *testing.T, _ string) []string {
				return []string{
					"--repo", filepath.Join("..", "..", "testdata"),
					"--detailed-exitcodes", "--no-color",
				}
			},
			want: 2,
		},
		{
			name: "changes without --detailed-exitcodes still exits 0",
			args: func(_ *testing.T, _ string) []string {
				return []string{"--repo", filepath.Join("..", "..", "testdata"), "--no-color"}
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := stubFleetAPI(t)
			t.Setenv("FLEET_PLAN_INSECURE", "1")
			t.Setenv("FLEET_URL", srv.URL)
			t.Setenv("FLEET_TOKEN", "test-token")
			t.Setenv("HOME", t.TempDir())

			// Discard stdout rather than piping it: the full testdata diff is
			// larger than a pipe buffer, and nothing here reads the other end.
			old := os.Stdout
			devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			os.Stdout = devnull
			got := run(tt.args(t, srv.URL))
			_ = devnull.Close()
			os.Stdout = old

			if got != tt.want {
				t.Errorf("exit code: got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestHasNoTeam(t *testing.T) {
	tests := []struct {
		name  string
		teams []parser.ParsedTeam
		want  bool
	}{
		{name: "no teams at all"},
		{
			name:  "ordinary teams only",
			teams: []parser.ParsedTeam{{Name: "Workstations", SourceFile: "teams/workstations.yml"}},
		},
		{
			name: "teams layout no-team file",
			teams: []parser.ParsedTeam{
				{Name: "Workstations", SourceFile: "teams/workstations.yml"},
				{Name: "No team", SourceFile: "teams/no-team.yml"},
			},
			want: true,
		},
		{
			name:  "fleets layout unassigned file",
			teams: []parser.ParsedTeam{{Name: "Unassigned", SourceFile: "fleets/unassigned.yml"}},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasNoTeam(tt.teams); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
