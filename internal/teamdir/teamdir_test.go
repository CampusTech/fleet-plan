package teamdir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePrefersFleets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "fleets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "teams"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Resolve(root); got != "fleets" {
		t.Errorf("Resolve = %q, want %q", got, "fleets")
	}
}

func TestResolveFallsBackToTeams(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "teams"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Resolve(root); got != "teams" {
		t.Errorf("Resolve = %q, want %q", got, "teams")
	}
}

func TestResolveDefaultsToFleets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if got := Resolve(root); got != "fleets" {
		t.Errorf("Resolve = %q, want %q (default)", got, "fleets")
	}
}

func TestHasPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"fleets/workstations.yml", true},
		{"teams/workstations.yml", true},
		{"fleetstuff/x.yml", false},
		{"policies/disk.yml", false},
		{"", false},
		{"FLEETS/x.yml", false}, // case-sensitive
	}
	for _, c := range cases {
		if got := HasPrefix(c.path); got != c.want {
			t.Errorf("HasPrefix(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
