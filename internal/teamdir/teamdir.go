// Package teamdir resolves the directory name that holds per-team YAML files
// inside a fleet-gitops repository.
//
// Fleet originally called these "teams" and stored them in teams/. Starting
// with Fleet PR #40726, the canonical name is "fleets" stored in fleets/.
// Both layouts remain valid: this package prefers fleets/, falls back to
// teams/, and reports which one a given repo uses.
package teamdir

import (
	"os"
	"path/filepath"
	"strings"
)

// Preferred is the name used when neither directory exists yet (e.g. when
// preparing a baseline scratch dir) or when both exist.
const Preferred = "fleets"

// Legacy is the older directory name still accepted for backwards compatibility.
const Legacy = "teams"

// Names returns the candidate directory names in priority order. Useful for
// callers that need to scan both layouts (e.g. git scope detection where the
// changed-file path's prefix is what's known, not the on-disk directory).
func Names() []string { return []string{Preferred, Legacy} }

// Resolve returns the name of the team directory inside root, preferring
// fleets/ over teams/. If neither exists, Preferred is returned so callers
// have a sensible default for error messages and scratch-dir creation.
func Resolve(root string) string {
	for _, name := range Names() {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && info.IsDir() {
			return name
		}
	}
	return Preferred
}

// HasPrefix reports whether the given repo-relative path lives under any
// recognized team directory (fleets/ or teams/).
func HasPrefix(path string) bool {
	for _, name := range Names() {
		if strings.HasPrefix(path, name+"/") {
			return true
		}
	}
	return false
}
