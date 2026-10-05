package git

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Repos using the lib/ layout with a plain default.yml (fleet-gitops PR 135).
func TestResolveScopeLibLayoutAndDefaultYML(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T, root string) {
		writeTeamFileIn(t, root, "fleets", "workstations.yml", "Workstations",
			"policies:\n  - path: ../lib/macos/policies/dor-installed.yml\n")
		def := "labels:\n- path: ./lib/all/labels/dor-exempt.yml\npolicies:\n- path: ./lib/all/policies.yml\n"
		if err := os.WriteFile(filepath.Join(root, "default.yml"), []byte(def), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name       string
		changed    []string
		wantGlobal bool
		wantTeams  []string
	}{
		{name: "default.yml sets IncludeGlobal", changed: []string{"default.yml"}, wantGlobal: true},
		{name: "lib label referenced by default.yml sets IncludeGlobal", changed: []string{"lib/all/labels/dor-exempt.yml"}, wantGlobal: true},
		{name: "lib policy referenced by team infers team", changed: []string{"lib/macos/policies/dor-installed.yml"}, wantTeams: []string{"Workstations"}},
		{name: "lib README is ignored", changed: []string{"lib/macos/policies/README.md"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			setup(t, root)
			got := ResolveScope(root, tt.changed, "")
			if got.IncludeGlobal != tt.wantGlobal {
				t.Errorf("IncludeGlobal = %v, want %v", got.IncludeGlobal, tt.wantGlobal)
			}
			if !slices.Equal(got.Teams, tt.wantTeams) {
				t.Errorf("Teams = %v, want %v", got.Teams, tt.wantTeams)
			}
		})
	}
}
