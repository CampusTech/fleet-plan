package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TsekNet/fleet-plan/internal/teamdir"
)

func TestExtractPathRefs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		content    string
		sourceFile string
		want       []string
	}{
		{
			name:       "policy path refs",
			content:    "  - path: ../policies/windows/filevault.yml\n  - path: ../policies/linux/ssh.yml\n",
			sourceFile: "teams/workstations.yml",
			want:       []string{"policies/windows/filevault.yml", "policies/linux/ssh.yml"},
		},
		{
			name:       "software path ref",
			content:    "    path: ../software/mac/slack/slack.yml\n",
			sourceFile: "teams/workstations.yml",
			want:       []string{"software/mac/slack/slack.yml"},
		},
		{
			name:       "quoted path",
			content:    "  - path: \"../queries/windows/hardware.yml\"\n",
			sourceFile: "teams/test.yml",
			want:       []string{"queries/windows/hardware.yml"},
		},
		{
			name:       "no path refs",
			content:    "name: TestTeam\nagent_options:\n  config:\n    options:\n      logger_plugin: tls\n",
			sourceFile: "teams/test.yml",
			want:       nil,
		},
		{
			name:       "nested ref from software yaml",
			content:    "path: install.ps1\n",
			sourceFile: "software/windows/agent/agent.yml",
			want:       []string{"software/windows/agent/install.ps1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractPathRefs([]byte(tt.content), tt.sourceFile)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d refs, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("ref[%d]: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCheckoutBaseline_NoBaseRef(t *testing.T) {
	t.Parallel()
	_, _, err := CheckoutBaseline("/tmp", "", []string{"teams/test.yml"})
	if err == nil {
		t.Fatal("expected error for empty base ref")
	}
}

func TestCheckoutBaseline_ExtractsFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "test@test.com")
	gitRun(t, dir, "config", "user.name", "Test")

	// Create initial commit with a team file and a policy file.
	os.MkdirAll(filepath.Join(dir, "teams"), 0o755)
	os.MkdirAll(filepath.Join(dir, "policies", "linux"), 0o755)
	os.WriteFile(filepath.Join(dir, "teams", "test.yml"), []byte("name: Test\npolicies:\n  - path: ../policies/linux/ssh.yml\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "policies", "linux", "ssh.yml"), []byte("name: SSH\nquery: SELECT 1;\n"), 0o644)
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")

	baseSHA := gitOutput(t, dir, "rev-parse", "HEAD")

	// Modify the team file on a new "branch" (just a new commit).
	os.WriteFile(filepath.Join(dir, "teams", "test.yml"), []byte("name: Test\npolicies:\n  - path: ../policies/linux/ssh.yml\nqueries:\n  - path: ../queries/new.yml\n"), 0o644)
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "add query")

	// CheckoutBaseline should extract the base version.
	tmpRoot, cleanup, err := CheckoutBaseline(dir, baseSHA, []string{"teams/test.yml"})
	if err != nil {
		t.Fatalf("CheckoutBaseline: %v", err)
	}
	defer cleanup()

	// Verify the team file was extracted with the base content.
	content, err := os.ReadFile(filepath.Join(tmpRoot, "teams", "test.yml"))
	if err != nil {
		t.Fatalf("reading extracted team file: %v", err)
	}
	if !strings.Contains(string(content), "name: Test") {
		t.Errorf("expected team file content, got: %s", content)
	}
	if strings.Contains(string(content), "queries") {
		t.Errorf("base version should not contain queries section, got: %s", content)
	}

	// Verify the referenced policy file was also extracted.
	policyContent, err := os.ReadFile(filepath.Join(tmpRoot, "policies", "linux", "ssh.yml"))
	if err != nil {
		t.Fatalf("reading extracted policy file: %v", err)
	}
	if !strings.Contains(string(policyContent), "name: SSH") {
		t.Errorf("expected policy file content, got: %s", policyContent)
	}
}

// TestCheckoutBaseline_UsesBaseLayoutNotWorktree covers the migration case:
// the base ref stores per-team YAML under teams/, but the current worktree has
// migrated to fleets/. The extracted baseline must reflect the base ref's
// teams/ layout; pre-creating the worktree's preferred fleets/ directory would
// make teamdir.Resolve pick fleets/ and cause baseline parsing to ignore the
// extracted legacy team files.
func TestCheckoutBaseline_UsesBaseLayoutNotWorktree(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "test@test.com")
	gitRun(t, dir, "config", "user.name", "Test")

	// Base commit: legacy teams/ layout.
	os.MkdirAll(filepath.Join(dir, "teams"), 0o755)
	os.WriteFile(filepath.Join(dir, "teams", "test.yml"), []byte("name: Test\n"), 0o644)
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")
	baseSHA := gitOutput(t, dir, "rev-parse", "HEAD")

	// Worktree migrates teams/ -> fleets/ (rename shows as delete + add).
	os.Rename(filepath.Join(dir, "teams"), filepath.Join(dir, "fleets"))
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "migrate to fleets")

	// The changed set for a migration includes both the deleted teams/ file
	// and the added fleets/ file; only the teams/ one exists at the base ref.
	tmpRoot, cleanup, err := CheckoutBaseline(dir, baseSHA, []string{"teams/test.yml", "fleets/test.yml"})
	if err != nil {
		t.Fatalf("CheckoutBaseline: %v", err)
	}
	defer cleanup()

	// The base team file must be extracted under teams/.
	if _, err := os.Stat(filepath.Join(tmpRoot, "teams", "test.yml")); err != nil {
		t.Fatalf("base teams/test.yml not extracted: %v", err)
	}

	// No empty fleets/ directory should shadow the base teams/ layout.
	if info, err := os.Stat(filepath.Join(tmpRoot, "fleets")); err == nil && info.IsDir() {
		t.Errorf("baseline pre-created fleets/ directory, masking the base teams/ layout")
	}

	// teamdir.Resolve on the baseline must select the base ref's layout.
	if got := teamdir.Resolve(tmpRoot); got != "teams" {
		t.Errorf("teamdir.Resolve(baseline) = %q, want %q", got, "teams")
	}
}

func TestCollectBaselineFiles_OnlyIncludesChangedFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "test@test.com")
	gitRun(t, dir, "config", "user.name", "Test")

	os.MkdirAll(filepath.Join(dir, "teams"), 0o755)
	os.WriteFile(filepath.Join(dir, "default.yml"), []byte("org_settings:\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "teams", "test.yml"), []byte("name: Test\n"), 0o644)
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")

	sha := gitOutput(t, dir, "rev-parse", "HEAD")

	// default.yml not in changedFiles, should NOT be collected.
	files := collectBaselineFiles(dir, sha, []string{"teams/test.yml"})
	for _, f := range files {
		if f == "default.yml" {
			t.Errorf("default.yml should not be collected when not in changedFiles, got: %v", files)
		}
	}

	// default.yml in changedFiles, should be collected.
	files = collectBaselineFiles(dir, sha, []string{"default.yml"})
	found := false
	for _, f := range files {
		if f == "default.yml" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected default.yml when in changedFiles, got: %v", files)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
