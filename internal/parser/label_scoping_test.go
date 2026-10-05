package parser

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Profile label scoping was parsed into rawProfileRef and then dropped.
func TestParseRepoProfileLabelScoping(t *testing.T) {
	root := t.TempDir()
	fleetsDir := filepath.Join(root, "fleets")
	profDir := filepath.Join(root, "lib", "macos", "configuration-profiles")
	for _, d := range []string{fleetsDir, profDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(profDir, "dor.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	teamYAML := `name: T1
team_settings: {}
controls:
  apple_settings:
    configuration_profiles:
      - path: ../lib/macos/configuration-profiles/dor.json
        labels_exclude_any: [exempt]
        labels_include_all: [a, b]
`
	if err := os.WriteFile(filepath.Join(fleetsDir, "t1.yml"), []byte(teamYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := ParseRepo(root, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	p := repo.Teams[0].Profiles[0]
	if !slices.Equal(p.LabelsExcludeAny, []string{"exempt"}) || !slices.Equal(p.LabelsIncludeAll, []string{"a", "b"}) {
		t.Errorf("profile labels: exclude=%v include_all=%v", p.LabelsExcludeAny, p.LabelsIncludeAll)
	}
}
