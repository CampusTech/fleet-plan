package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CampusTech/fleet-plan/internal/teamdir"
)

// CheckoutBaseline extracts the base-branch versions of the given files into a
// temporary directory that mirrors the repo layout. It uses "git show <ref>:<path>"
// so it works in shallow clones without a full checkout.
//
// The caller must call the returned cleanup function to remove the temp dir.
// If the base ref is unavailable or a file doesn't exist at the base ref, that
// file is silently skipped (it may be newly added in the MR).
func CheckoutBaseline(repoRoot string, baseRef string, files []string) (tmpRoot string, cleanup func(), err error) {
	if baseRef == "" {
		return "", nil, fmt.Errorf("no base ref provided")
	}

	tmpRoot, err = os.MkdirTemp("", "fleet-plan-baseline-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup = func() { os.RemoveAll(tmpRoot) }

	// Resolve which files we need: the explicitly changed files, plus any
	// files they reference (path: directives in team YAML). We start with
	// the team files themselves, then do a second pass for references.
	needed := collectBaselineFiles(repoRoot, baseRef, files)

	var extracted int
	var baseLayout string
	for _, f := range needed {
		content, err := gitShow(repoRoot, baseRef, f)
		if err != nil {
			// File doesn't exist at base ref (newly added), skip.
			continue
		}

		// Record the team directory the base ref actually uses, inferred
		// from the files that exist at the base ref. This must not be taken
		// from repoRoot: if the current worktree migrated teams/ -> fleets/,
		// pre-creating the worktree's layout would mask the base ref's legacy
		// team files and make baseline parsing ignore them.
		if baseLayout == "" {
			for _, name := range teamdir.Names() {
				if strings.HasPrefix(f, name+"/") {
					baseLayout = name
					break
				}
			}
		}

		dst := filepath.Join(tmpRoot, f)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("creating dir for %s: %w", f, err)
		}
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("writing %s: %w", f, err)
		}
		extracted++
	}

	if extracted == 0 {
		cleanup()
		return "", nil, fmt.Errorf("no baseline files could be extracted")
	}

	// Ensure the base ref's team directory exists so teamdir.Resolve picks the
	// same layout the base ref used rather than falling back to the preferred
	// name. Writing an extracted team file already creates this dir; the
	// explicit MkdirAll only matters when the base layout was detected but is
	// otherwise empty.
	if baseLayout != "" {
		os.MkdirAll(filepath.Join(tmpRoot, baseLayout), 0o755)
	}

	return tmpRoot, cleanup, nil
}

// collectBaselineFiles returns the full set of files needed for baseline parsing.
// This includes the changed team files and any path: references they contain.
func collectBaselineFiles(repoRoot, baseRef string, changedFiles []string) []string {
	seen := make(map[string]bool)
	var result []string

	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}

	// Start with all fleet-relevant changed files.
	for _, f := range changedFiles {
		add(f)
	}

	// For each team or config file, extract it from base ref and scan for path:
	// references. Also extract any referenced resource files so the parser can
	// resolve them.
	for _, f := range changedFiles {
		if !teamdir.HasPrefix(f) && f != "base.yml" && f != "default.yml" {
			continue
		}
		content, err := gitShow(repoRoot, baseRef, f)
		if err != nil {
			continue
		}
		for _, ref := range extractPathRefs(content, f) {
			add(ref)
			// Resource YAML files may reference scripts, profiles, etc.
			refContent, err := gitShow(repoRoot, baseRef, ref)
			if err != nil {
				continue
			}
			for _, nested := range extractPathRefs(refContent, ref) {
				add(nested)
			}
		}
	}

	// Only include default.yml/base.yml if they're already in changedFiles.
	// Extracting them unconditionally causes over-subtraction for MRs that
	// don't touch global config.

	return result
}

// extractPathRefs scans YAML content for "path:" directives and returns the
// resolved file paths relative to repo root.
func extractPathRefs(content []byte, sourceFile string) []string {
	var refs []string
	sourceDir := filepath.Dir(sourceFile)
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- path:") && !strings.HasPrefix(trimmed, "path:") {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}
		ref := strings.TrimSpace(parts[1])
		// Remove quotes if present.
		ref = strings.Trim(ref, `"'`)
		if ref == "" {
			continue
		}
		// Resolve relative to the source file's directory.
		resolved := filepath.Join(sourceDir, ref)
		resolved = filepath.Clean(resolved)
		if !strings.HasPrefix(resolved, "..") {
			refs = append(refs, resolved)
		}
	}
	return refs
}

// gitShow runs "git show <ref>:<path>" and returns the file content.
func gitShow(repoRoot, ref, path string) ([]byte, error) {
	cmd := exec.Command("git", "show", ref+":"+path)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show %s:%s: %w", ref, path, err)
	}
	return out, nil
}
