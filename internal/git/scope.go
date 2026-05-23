package git

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/TsekNet/fleet-plan/internal/teamdir"
	"gopkg.in/yaml.v3"
)

// fleetResourcePrefixes lists the directory prefixes for fleet-managed resources.
var fleetResourcePrefixes = []string{"policies/", "queries/", "software/", "profiles/", "scripts/"}

// Scope describes which parts of the repo are affected by a set of changed files.
type Scope struct {
	// IncludeGlobal is true when base.yml, an environment overlay, or labels/ changed.
	IncludeGlobal bool
	// Teams is the deduplicated list of affected team names.
	Teams []string
	// ChangedFiles is the filtered subset of files relevant to fleet-plan.
	ChangedFiles []string
}

// ResolveScope inspects changedFiles against the repo at root and returns the
// affected teams and whether global config is affected.
// envFile is the path to the environment overlay (e.g. "environments/nv.yml").
func ResolveScope(root string, changedFiles []string, envFile string) Scope {
	teamsSeen := map[string]bool{}
	var scope Scope

	for _, f := range changedFiles {
		if strings.Contains(f, "..") {
			continue
		}
		cleaned := filepath.Clean(f)
		if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
			continue
		}
		switch {
		case f == "base.yml", f == envFile, strings.HasPrefix(f, "labels/") && !strings.HasSuffix(f, ".md"):
			scope.IncludeGlobal = true

		case isTeamYAML(f):
			name := readTeamName(filepath.Join(root, f))
			if name != "" && !teamsSeen[name] {
				teamsSeen[name] = true
				scope.Teams = append(scope.Teams, name)
			}

		case isFleetResource(f):
			patterns := buildSearchPatterns(root, f)
			for _, name := range teamsReferencingAny(root, patterns) {
				if !teamsSeen[name] {
					teamsSeen[name] = true
					scope.Teams = append(scope.Teams, name)
				}
			}
		}

		if isFleetResourceOrTeam(f) || f == "base.yml" || f == "default.yml" {
			scope.ChangedFiles = append(scope.ChangedFiles, f)
		}
	}

	return scope
}

// isFleetResource returns true for files under the fleet-managed resource dirs,
// excluding markdown files which are documentation, not fleet config.
func isFleetResource(f string) bool {
	if strings.HasSuffix(f, ".md") {
		return false
	}
	for _, prefix := range fleetResourcePrefixes {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	return false
}

func isFleetResourceOrTeam(f string) bool {
	return isFleetResource(f) || isTeamYAML(f)
}

// isTeamYAML reports whether a repo-relative path is a per-team YAML file
// under any recognized team directory (fleets/ or teams/).
func isTeamYAML(f string) bool {
	if !strings.HasSuffix(f, ".yml") && !strings.HasSuffix(f, ".yaml") {
		return false
	}
	return teamdir.HasPrefix(f)
}

// buildSearchPatterns returns the set of path strings to grep for in team YAMLs.
// For non-YAML files (e.g. install scripts), also includes sibling YAML files.
func buildSearchPatterns(root, f string) []string {
	patterns := []string{"../" + f, f}

	if !strings.HasSuffix(f, ".yml") && !strings.HasSuffix(f, ".yaml") {
		dir := filepath.Dir(f)
		for _, ext := range []string{"*.yml", "*.yaml"} {
			matches, _ := filepath.Glob(filepath.Join(root, dir, ext))
			for _, m := range matches {
				rel, err := filepath.Rel(root, m)
				if err == nil {
					patterns = append(patterns, "../"+rel, rel)
				}
			}
		}
	}
	return patterns
}

// teamsReferencingAny reads every team YAML in the repo (fleets/ or teams/)
// and returns team names whose file content contains any of the given patterns
// (plain string search).
func teamsReferencingAny(root string, patterns []string) []string {
	var matches []string
	for _, dir := range teamdir.Names() {
		for _, ext := range []string{"*.yml", "*.yaml"} {
			m, _ := filepath.Glob(filepath.Join(root, dir, ext))
			matches = append(matches, m...)
		}
	}
	seen := map[string]bool{}
	var names []string
	for _, teamFile := range matches {
		content, err := os.ReadFile(teamFile)
		if err != nil {
			continue
		}
		for _, pat := range patterns {
			if strings.Contains(string(content), pat) {
				name := readTeamName(teamFile)
				if name != "" && !seen[name] {
					seen[name] = true
					names = append(names, name)
				}
				break
			}
		}
	}
	return names
}

// readTeamName extracts the name field from a team YAML file.
func readTeamName(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var t struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(data, &t); err != nil {
		return ""
	}
	return t.Name
}
