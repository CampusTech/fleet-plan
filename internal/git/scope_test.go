package git

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTeamFile creates a team YAML file in root/teams/ with the given name
// and optional body content referencing resources. The legacy teams/ name
// is kept here so existing test cases continue exercising backwards compat.
func writeTeamFile(t *testing.T, root, filename, teamName, body string) {
	t.Helper()
	writeTeamFileIn(t, root, "teams", filename, teamName, body)
}

// writeTeamFileIn is the same as writeTeamFile but lets the caller pick the
// directory name (used to exercise the canonical fleets/ layout).
func writeTeamFileIn(t *testing.T, root, dirName, filename, teamName, body string) {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "name: " + teamName + "\n" + body
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setup         func(t *testing.T, root string) // create files in temp dir
		changedFiles  []string
		envFile       string
		wantGlobal    bool
		wantTeams     []string
		wantChanged   []string
		wantTeamCount int // -1 to skip count check
	}{
		{
			name: "policy change infers team",
			setup: func(t *testing.T, root string) {
				writeTeamFile(t, root, "workstations.yml", "Workstations",
					"policies:\n  - path: ../policies/disk-encrypt.yml\n")
			},
			changedFiles:  []string{"policies/disk-encrypt.yml"},
			wantGlobal:    false,
			wantTeams:     []string{"Workstations"},
			wantChanged:   []string{"policies/disk-encrypt.yml"},
			wantTeamCount: 1,
		},
		{
			name:          "base.yml sets IncludeGlobal",
			setup:         func(t *testing.T, root string) {},
			changedFiles:  []string{"base.yml"},
			wantGlobal:    true,
			wantTeamCount: 0,
		},
		{
			name:          "environment overlay sets IncludeGlobal",
			setup:         func(t *testing.T, root string) {},
			changedFiles:  []string{"environments/nv.yml"},
			envFile:       "environments/nv.yml",
			wantGlobal:    true,
			wantTeamCount: 0,
		},
		{
			name:          "labels/ change sets IncludeGlobal",
			setup:         func(t *testing.T, root string) {},
			changedFiles:  []string{"labels/managed.yml"},
			wantGlobal:    true,
			wantTeamCount: 0,
		},
		{
			name:          "labels/README.md does not set IncludeGlobal",
			setup:         func(t *testing.T, root string) {},
			changedFiles:  []string{"labels/README.md"},
			wantGlobal:    false,
			wantTeamCount: 0,
		},
		{
			name:          "non-fleet file ignored",
			setup:         func(t *testing.T, root string) {},
			changedFiles:  []string{"README.md"},
			wantGlobal:    false,
			wantTeamCount: 0,
		},
		{
			name:          "README under resource dir ignored",
			setup:         func(t *testing.T, root string) {},
			changedFiles:  []string{"policies/README.md", "scripts/README.md", "queries/README.md"},
			wantGlobal:    false,
			wantTeamCount: 0,
			wantChanged:   nil,
		},
		{
			name:          "path traversal skipped",
			setup:         func(t *testing.T, root string) {},
			changedFiles:  []string{"../etc/passwd"},
			wantGlobal:    false,
			wantTeamCount: 0,
		},
		{
			name: "team YAML change resolves team name",
			setup: func(t *testing.T, root string) {
				writeTeamFile(t, root, "infra.yml", "Infrastructure", "")
			},
			changedFiles:  []string{"teams/infra.yml"},
			wantGlobal:    false,
			wantTeams:     []string{"Infrastructure"},
			wantChanged:   []string{"teams/infra.yml"},
			wantTeamCount: 1,
		},
		{
			name: "script change finds referencing team",
			setup: func(t *testing.T, root string) {
				writeTeamFile(t, root, "alpha.yml", "Alpha",
					"controls:\n  scripts:\n    - path: ../scripts/setup.ps1\n")
			},
			changedFiles:  []string{"scripts/setup.ps1"},
			wantGlobal:    false,
			wantTeams:     []string{"Alpha"},
			wantChanged:   []string{"scripts/setup.ps1"},
			wantTeamCount: 1,
		},
		{
			name: "multiple changes deduplicate teams",
			setup: func(t *testing.T, root string) {
				writeTeamFile(t, root, "ws.yml", "Workstations",
					"policies:\n  - path: ../policies/a.yml\n  - path: ../policies/b.yml\n")
			},
			changedFiles:  []string{"policies/a.yml", "policies/b.yml"},
			wantGlobal:    false,
			wantTeams:     []string{"Workstations"},
			wantTeamCount: 1,
		},
		{
			name: "fleets/ team YAML change resolves team name",
			setup: func(t *testing.T, root string) {
				writeTeamFileIn(t, root, "fleets", "infra.yml", "Infrastructure", "")
			},
			changedFiles:  []string{"fleets/infra.yml"},
			wantGlobal:    false,
			wantTeams:     []string{"Infrastructure"},
			wantChanged:   []string{"fleets/infra.yml"},
			wantTeamCount: 1,
		},
		{
			name: "resource change finds referencing fleets/ team",
			setup: func(t *testing.T, root string) {
				writeTeamFileIn(t, root, "fleets", "alpha.yml", "Alpha",
					"controls:\n  scripts:\n    - path: ../scripts/setup.ps1\n")
			},
			changedFiles:  []string{"scripts/setup.ps1"},
			wantGlobal:    false,
			wantTeams:     []string{"Alpha"},
			wantChanged:   []string{"scripts/setup.ps1"},
			wantTeamCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			tt.setup(t, root)

			scope := ResolveScope(root, tt.changedFiles, tt.envFile)

			if scope.IncludeGlobal != tt.wantGlobal {
				t.Errorf("IncludeGlobal: got %v, want %v", scope.IncludeGlobal, tt.wantGlobal)
			}

			if tt.wantTeamCount >= 0 && len(scope.Teams) != tt.wantTeamCount {
				t.Errorf("team count: got %d, want %d (teams: %v)", len(scope.Teams), tt.wantTeamCount, scope.Teams)
			}

			for _, want := range tt.wantTeams {
				if !contains(scope.Teams, want) {
					t.Errorf("expected team %q in %v", want, scope.Teams)
				}
			}

			for _, want := range tt.wantChanged {
				if !contains(scope.ChangedFiles, want) {
					t.Errorf("expected %q in ChangedFiles %v", want, scope.ChangedFiles)
				}
			}
		})
	}
}

func TestIsFleetResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want bool
	}{
		{"policies/disk.yml", true},
		{"queries/hw.yml", true},
		{"software/win/app.yml", true},
		{"profiles/wifi.mobileconfig", true},
		{"scripts/setup.ps1", true},
		{"teams/alpha.yml", false},
		{"README.md", false},
		{"base.yml", false},
		{"labels/managed.yml", false},
		{"policies/README.md", false},
		{"scripts/README.md", false},
		{"queries/README.md", false},
		{"software/README.md", false},
		{"profiles/README.md", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if got := isFleetResource(tt.path); got != tt.want {
				t.Errorf("isFleetResource(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsTeamYAML(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"fleets/workstations.yml", true},
		{"teams/workstations.yml", true},
		{"fleets/infra.yaml", true},
		{"teams/infra.yaml", true},
		{"fleets/workstations.md", false},
		{"policies/disk.yml", false},
		{"fleets-extra/x.yml", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isTeamYAML(c.path); got != c.want {
			t.Errorf("isTeamYAML(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
