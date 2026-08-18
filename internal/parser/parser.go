// Package parser reads fleet-gitops YAML files, resolves path: references,
// and produces normalized ParsedRepo structures for diffing. The schema and
// validation rules are derived from Fleet's Go source code, not documentation.
package parser

import (
	"fmt"
	"html"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/CampusTech/fleet-plan/internal/teamdir"
	"gopkg.in/yaml.v3"
)

// safePath ensures a resolved file path stays within the repo root.
// It resolves symlinks to prevent traversal via symlinked paths.
func safePath(root, resolved string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolving repo root: %w", err)
	}
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	// Resolve symlinks to get real paths. If the file doesn't exist yet,
	// EvalSymlinks will fail — fall back to the non-symlink check.
	if realResolved, err := filepath.EvalSymlinks(absResolved); err == nil {
		absResolved = realResolved
	}
	if realRoot, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = realRoot
	}
	if !strings.HasPrefix(absResolved, absRoot+string(filepath.Separator)) && absResolved != absRoot {
		return fmt.Errorf("path %q escapes repository root %q", resolved, root)
	}
	return nil
}

// ValidPlatforms lists the platform identifiers Fleet accepts
// (from server/fleet/policies.go and queries.go).
var ValidPlatforms = map[string]bool{
	"darwin":  true,
	"windows": true,
	"linux":   true,
	"chrome":  true,
}

// ValidLogging lists the query logging types Fleet accepts
// (from server/fleet/queries.go).
var ValidLogging = map[string]bool{
	"snapshot":                     true,
	"differential":                 true,
	"differential_ignore_removals": true,
}

// ValidTopLevelKeys lists the top-level YAML keys fleetctl gitops accepts
// (from pkg/spec/gitops.go).
var ValidTopLevelKeys = map[string]bool{
	"name":          true,
	"team_settings": true,
	"org_settings":  true,
	"agent_options": true,
	"controls":      true,
	"policies":      true,
	"queries":       true,
	"software":      true,
	"labels":        true,
	// Newer Fleet schema additions accepted by `fleetctl gitops`.
	// `reports:` is the renamed `queries:` (fleetdm/fleet#40726); we parse
	// it fully via rawTeamFile.Reports. `settings:` is currently accepted
	// as opaque — field-level diffing isn't implemented but the absence of
	// an error keeps the rest of the diff trustworthy.
	"settings": true,
	"reports":  true,
}

// ValidLabelMembershipTypes lists the label membership types Fleet accepts
// (from server/fleet/labels.go).
var ValidLabelMembershipTypes = map[string]bool{
	"dynamic":     true,
	"manual":      true,
	"host_vitals": true,
}

// ---------- Parsed types ----------

// ParsedRepo represents the fully parsed fleet-gitops repository.
type ParsedRepo struct {
	Teams  []ParsedTeam
	Labels []ParsedLabel
	Global *ParsedGlobal // from default.yml (org_settings, agent_options, controls, policies, queries)
	Errors []ParseError
}

// ParsedGlobal holds global configuration parsed from default.yml.
type ParsedGlobal struct {
	OrgSettings  map[string]any // raw org_settings as nested map
	AgentOptions map[string]any // raw agent_options as nested map
	Controls     map[string]any // raw controls as nested map
	Policies     []ParsedPolicy
	Queries      []ParsedQuery
	SourceFile   string
}

// ParsedTeam represents a single team's configuration.
type ParsedTeam struct {
	Name       string
	Policies   []ParsedPolicy
	Queries    []ParsedQuery
	Software   ParsedSoftware
	Profiles   []ParsedProfile
	Scripts    []ParsedScript
	SourceFile string
}

// ParsedScript represents a script under controls.scripts.
type ParsedScript struct {
	Name       string // filename extracted from path (e.g., "foo.ps1")
	Path       string // resolved absolute path
	Content    string // file content (read at parse time)
	SourceFile string // team YAML that referenced it
}

// ParsedPolicy represents a policy from YAML.
type ParsedPolicy struct {
	Name             string   `yaml:"name"`
	Description      string   `yaml:"description"`
	Resolution       string   `yaml:"resolution"`
	Query            string   `yaml:"query"`
	Platform         string   `yaml:"platform"`
	Critical         bool     `yaml:"critical"`
	LabelsIncludeAny []string `yaml:"labels_include_any"`
	LabelsExcludeAny []string `yaml:"labels_exclude_any"`
	SourceFile       string   `yaml:"-"`
}

// ParsedQuery represents a query from YAML.
type ParsedQuery struct {
	Name       string `yaml:"name"`
	Query      string `yaml:"query"`
	Interval   uint   `yaml:"interval"`
	Platform   string `yaml:"platform"`
	Logging    string `yaml:"logging"`
	SourceFile string `yaml:"-"`
}

// ParsedSoftware holds all software types for a team.
type ParsedSoftware struct {
	Packages        []ParsedSoftwarePackage `yaml:"packages"`
	FleetMaintained []ParsedFleetApp        `yaml:"fleet_maintained_apps"`
	AppStoreApps    []ParsedAppStoreApp     `yaml:"app_store_apps"`
}

// ParsedSoftwarePackage represents a custom software package.
type ParsedSoftwarePackage struct {
	URL         string   `yaml:"url"`
	HashSHA256  string   `yaml:"hash_sha256"`
	SelfService bool     `yaml:"self_service"`
	Categories  []string `yaml:"categories"`
	SourceFile  string   `yaml:"-"`
	RefPath     string   `yaml:"-"`
	SourceFiles []string `yaml:"-"` // all referenced file paths (install/uninstall scripts, pre_install_query)
}

// ParsedFleetApp represents a Fleet-maintained app.
type ParsedFleetApp struct {
	Slug              string   `yaml:"slug"`
	SelfService       bool     `yaml:"self_service"`
	Categories        []string `yaml:"categories"`
	InstallScript     string   `yaml:"-"` // resolved file content
	UninstallScript   string   `yaml:"-"`
	PreInstallQuery   string   `yaml:"-"`
	PostInstallScript string   `yaml:"-"`
	SourceFiles       []string `yaml:"-"` // all referenced file paths (for changed-file filtering)
}

// ParsedAppStoreApp represents an App Store app.
type ParsedAppStoreApp struct {
	AppStoreID  string   `yaml:"app_store_id"`
	SelfService bool     `yaml:"self_service"`
	Categories  []string `yaml:"categories"`
}

// ParsedLabel represents a label from YAML.
type ParsedLabel struct {
	Name                string `yaml:"name"`
	Description         string `yaml:"description"`
	Query               string `yaml:"query"`
	Platform            string `yaml:"platform"`
	LabelMembershipType string `yaml:"label_membership_type"`
	SourceFile          string `yaml:"-"`
}

// ParsedProfile represents an MDM profile reference.
type ParsedProfile struct {
	Path       string `yaml:"path"`
	Name       string `yaml:"-"` // extracted from file content (PayloadDisplayName, etc.)
	Platform   string `yaml:"-"` // inferred from file extension
	SourceFile string `yaml:"-"`
}

// ParseError represents a parse/validation error with file context.
type ParseError struct {
	File    string
	Line    int
	Message string
}

func (e ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.File, e.Message)
}

// ---------- Team YAML raw types (for initial parsing) ----------

type rawTeamFile struct {
	Name         string       `yaml:"name"`
	TeamSettings yaml.Node    `yaml:"team_settings"`
	OrgSettings  yaml.Node    `yaml:"org_settings"`
	AgentOptions yaml.Node    `yaml:"agent_options"`
	Controls     rawControls  `yaml:"controls"`
	Policies     []rawPathRef `yaml:"policies"`
	Queries      []rawPathRef `yaml:"queries"`
	// Reports is the renamed-in-fleetdm/fleet#40726 spelling of queries.
	// `fleetctl gitops` accepts both; we treat them as equivalent here so
	// repos that use the modern `reports:` keyword don't show every live
	// Fleet query as REMOVED on the diff.
	Reports  []rawPathRef     `yaml:"reports"`
	Software rawSoftwareBlock `yaml:"software"`
	Labels   []rawPathRef     `yaml:"labels"`
}

type rawPathRef struct {
	Path string `yaml:"path"`
}

type rawSoftwareBlock struct {
	Packages        []rawSoftwareRef    `yaml:"packages"`
	FleetMaintained []rawFleetApp       `yaml:"fleet_maintained_apps"`
	AppStoreApps    []ParsedAppStoreApp `yaml:"app_store_apps"`
}

type rawFleetApp struct {
	Slug              string      `yaml:"slug"`
	SelfService       bool        `yaml:"self_service"`
	Categories        []string    `yaml:"categories"`
	InstallScript     *rawPathRef `yaml:"install_script"`
	UninstallScript   *rawPathRef `yaml:"uninstall_script"`
	PreInstallQuery   *rawPathRef `yaml:"pre_install_query"`
	PostInstallScript *rawPathRef `yaml:"post_install_script"`
}

type rawSoftwareRef struct {
	Path        string   `yaml:"path"`
	SelfService *bool    `yaml:"self_service"`
	Categories  []string `yaml:"categories"`
}

// rawSoftwarePackage captures script path: refs inside a software package YAML file.
type rawSoftwarePackage struct {
	URL               string      `yaml:"url"`
	HashSHA256        string      `yaml:"hash_sha256"`
	SelfService       bool        `yaml:"self_service"`
	Categories        []string    `yaml:"categories"`
	InstallScript     *rawPathRef `yaml:"install_script"`
	UninstallScript   *rawPathRef `yaml:"uninstall_script"`
	PreInstallQuery   *rawPathRef `yaml:"pre_install_query"`
	PostInstallScript *rawPathRef `yaml:"post_install_script"`
}

type rawControls struct {
	Scripts       []rawPathRef `yaml:"scripts"`
	MacOSSettings struct {
		CustomSettings []rawProfileRef `yaml:"custom_settings"`
	} `yaml:"macos_settings"`
	WindowsSettings struct {
		CustomSettings []rawProfileRef `yaml:"custom_settings"`
		// ConfigurationProfiles is the modern key (mirroring
		// apple_settings.configuration_profiles) that fleetctl gitops accepts
		// for Windows profiles. Both spellings are valid; profiles defined here
		// must be parsed or every matching live Fleet profile is reported as
		// REMOVED.
		ConfigurationProfiles []rawProfileRef `yaml:"configuration_profiles"`
	} `yaml:"windows_settings"`
	// AppleSettings is the modern unified Apple-platform block, replacing
	// macos_settings.custom_settings. configuration_profiles entries can be
	// .mobileconfig (macOS/iOS/iPadOS), .json (DDM declarations), or .xml.
	// The platform is inferred from the file path (e.g. lib/ipados/...) and
	// from file extension if no path hint is present.
	AppleSettings struct {
		ConfigurationProfiles []rawProfileRef `yaml:"configuration_profiles"`
	} `yaml:"apple_settings"`
}

type rawProfileRef struct {
	Path             string   `yaml:"path"`
	LabelsIncludeAny []string `yaml:"labels_include_any"`
	LabelsExcludeAny []string `yaml:"labels_exclude_any"`
	LabelsIncludeAll []string `yaml:"labels_include_all"`
}

// ---------- Parser ----------

// MatchesAnyTeam reports whether name case-insensitively matches any filter.
func MatchesAnyTeam(name string, filters []string) bool {
	for _, f := range filters {
		if strings.EqualFold(name, f) {
			return true
		}
	}
	return false
}

// IsNoTeam reports whether a parsed team file describes Fleet's special
// "hosts not assigned to any team" bucket rather than a real team.
//
// Fleet calls this bucket "No team" in the teams/ layout (teams/no-team.yml)
// and "Unassigned" in the fleets/ layout (fleets/unassigned.yml) — fleetctl
// logs its work on that file as "... for unassigned hosts". Either way it
// always exists, is never created, and is NOT returned by GET /teams, so
// callers must not treat its absence there as a new team.
//
// Matched on the team name and, as a fallback for a file whose name: key was
// spelled differently, the source file's base name.
func IsNoTeam(name, sourceFile string) bool {
	for _, n := range []string{"No team", "Unassigned"} {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(sourceFile), filepath.Ext(sourceFile)))
	return base == "no-team" || base == "unassigned"
}

// ParseRepo parses the fleet-gitops repository at the given root directory.
// If teamFilters is non-empty, only matching teams are parsed.
// If defaultFile is non-empty, it is used as the path to default.yml (the
// pre-merged global config). Otherwise, the parser looks for default.yml in
// the repo root directory.
func ParseRepo(root string, teamFilters []string, defaultFile string) (*ParsedRepo, error) {
	repo := &ParsedRepo{}

	teamsDirName := teamdir.Resolve(root)
	teamsDir := filepath.Join(root, teamsDirName)
	entries, err := os.ReadDir(teamsDir)
	if err != nil {
		if os.IsNotExist(err) {
			repo.Errors = append(repo.Errors, ParseError{
				File:    teamsDir,
				Message: fmt.Sprintf("%s/ directory not found. Are you in a fleet-gitops repo?", teamsDirName),
			})
			return repo, nil
		}
		return nil, fmt.Errorf("reading %s directory: %w", teamsDirName, err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}

		teamFile := filepath.Join(teamsDir, name)
		team, errs := parseTeamFile(root, teamFile)
		if len(errs) > 0 {
			repo.Errors = append(repo.Errors, errs...)
		}
		if team == nil {
			continue
		}

		if len(teamFilters) > 0 && !MatchesAnyTeam(team.Name, teamFilters) {
			continue
		}

		repo.Teams = append(repo.Teams, *team)
	}

	// Resolve default.yml: --default flag takes priority, then default.yml in repo root.
	resolvedDefault := defaultFile
	if resolvedDefault == "" {
		resolvedDefault = filepath.Join(root, "default.yml")
	}
	if _, err := os.Stat(resolvedDefault); err == nil {
		parsed, errs := parseDefaultFile(root, resolvedDefault)
		repo.Errors = append(repo.Errors, errs...)
		if parsed != nil {
			repo.Global = parsed.ParsedGlobal
			repo.Labels = parsed.labels
		}
	}

	return repo, nil
}

// parseTeamFile parses a single team YAML file (under fleets/ or teams/) and
// resolves all path: refs.
func parseTeamFile(root, path string) (*ParsedTeam, []ParseError) {
	var errs []ParseError

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []ParseError{{File: path, Message: fmt.Sprintf("could not read: %s", err)}}
	}

	// Check for unknown top-level keys
	var rawMap map[string]yaml.Node
	if err := yaml.Unmarshal(data, &rawMap); err != nil {
		return nil, []ParseError{{File: path, Message: fmt.Sprintf("invalid YAML: %s", err)}}
	}
	for key := range rawMap {
		if !ValidTopLevelKeys[key] {
			errs = append(errs, ParseError{File: path, Message: fmt.Sprintf("unknown top-level key: %q", key)})
		}
	}

	var raw rawTeamFile
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, []ParseError{{File: path, Message: fmt.Sprintf("YAML parse error: %s", err)}}
	}

	if raw.Name == "" {
		errs = append(errs, ParseError{File: path, Message: "missing required 'name' field"})
		return nil, errs
	}

	team := &ParsedTeam{
		Name:       raw.Name,
		SourceFile: path,
	}

	dir := filepath.Dir(path)
	seenSoftwareRefs := make(map[string]bool)

	// Resolve policies
	for _, ref := range raw.Policies {
		policies, parseErrs := resolvePolicyRef(root, dir, ref.Path, path)
		errs = append(errs, parseErrs...)
		team.Policies = append(team.Policies, policies...)
	}

	// Resolve queries. Both `queries:` and `reports:` are accepted — Fleet
	// renamed the key in fleetdm/fleet#40726 but kept the old spelling
	// working, so repos may use either or both during transition.
	for _, ref := range append(append([]rawPathRef(nil), raw.Queries...), raw.Reports...) {
		queries, parseErrs := resolveQueryRef(root, dir, ref.Path, path)
		errs = append(errs, parseErrs...)
		team.Queries = append(team.Queries, queries...)
	}

	// Resolve software packages
	for _, ref := range raw.Software.Packages {
		pkgs, parseErrs := resolveSoftwareRef(root, dir, ref.Path, path)
		errs = append(errs, parseErrs...)

		// Every package resolved from one ref shares the same source file, so
		// the canonical ref is a property of the ref, not the individual
		// package. Compute it once from the resolved file path.
		canonicalRef := ""
		if root != "" {
			for i := range pkgs {
				if pkgs[i].SourceFile == "" {
					continue
				}
				if rel, err := filepath.Rel(root, pkgs[i].SourceFile); err == nil {
					canonicalRef = NormalizeSoftwarePath(rel)
					break
				}
			}
		}
		if canonicalRef == "" {
			canonicalRef = NormalizeSoftwarePath(ref.Path)
		}

		// Guard against the same package file being referenced twice in the
		// same team YAML. A single file may legitimately define multiple
		// packages (list form), so dedup at the ref level rather than per
		// resolved package.
		if canonicalRef != "" {
			if seenSoftwareRefs[canonicalRef] {
				errs = append(errs, ParseError{
					File:    path,
					Message: fmt.Sprintf("duplicate software package reference: %q", canonicalRef),
				})
				continue
			}
			seenSoftwareRefs[canonicalRef] = true
		}

		for i := range pkgs {
			pkgs[i].RefPath = canonicalRef
			// Team-level package entries can override package file settings.
			// Apply explicit self_service override when present.
			if ref.SelfService != nil {
				pkgs[i].SelfService = *ref.SelfService
			}
			if ref.Categories != nil {
				pkgs[i].Categories = ref.Categories
			}
			team.Software.Packages = append(team.Software.Packages, pkgs[i])
		}
	}
	for _, rawFMA := range raw.Software.FleetMaintained {
		fma, fmaErrs := resolveFleetApp(root, dir, rawFMA, path)
		errs = append(errs, fmaErrs...)
		team.Software.FleetMaintained = append(team.Software.FleetMaintained, fma)
	}
	team.Software.AppStoreApps = raw.Software.AppStoreApps

	// Resolve script paths from controls.scripts[].path.
	// Fleet identifies scripts by filename, which is what the API returns.
	for _, ref := range raw.Controls.Scripts {
		resolved := filepath.Join(dir, ref.Path)
		if root != "" {
			if err := safePath(root, resolved); err != nil {
				errs = append(errs, ParseError{File: path, Message: err.Error()})
				continue
			}
		}
		content := ""
		if data, err := os.ReadFile(resolved); err == nil {
			content = strings.TrimSpace(string(data))
		}
		team.Scripts = append(team.Scripts, ParsedScript{
			Name:       filepath.Base(resolved),
			Path:       resolved,
			Content:    content,
			SourceFile: path,
		})
	}

	// Resolve profile paths and extract names from file content.
	// Fleet identifies profiles by the name embedded in the file (e.g.,
	// PayloadDisplayName for .mobileconfig), NOT by the filename.
	for _, ref := range raw.Controls.MacOSSettings.CustomSettings {
		resolved := filepath.Join(dir, ref.Path)
		if root != "" {
			if err := safePath(root, resolved); err != nil {
				errs = append(errs, ParseError{File: path, Message: err.Error()})
				continue
			}
		}
		name := extractProfileName(resolved)
		if name == "" {
			name = profileNameFromFilename(resolved)
		}
		team.Profiles = append(team.Profiles, ParsedProfile{
			Path:       resolved,
			Name:       name,
			Platform:   "darwin",
			SourceFile: path,
		})
	}
	// Windows profiles may be listed under custom_settings (older spelling) or
	// configuration_profiles (modern spelling, mirroring apple_settings).
	// fleetctl gitops accepts both, so parse both.
	for _, ref := range append(
		append([]rawProfileRef(nil), raw.Controls.WindowsSettings.CustomSettings...),
		raw.Controls.WindowsSettings.ConfigurationProfiles...,
	) {
		resolved := filepath.Join(dir, ref.Path)
		if root != "" {
			if err := safePath(root, resolved); err != nil {
				errs = append(errs, ParseError{File: path, Message: err.Error()})
				continue
			}
		}
		name := extractProfileName(resolved)
		if name == "" {
			name = profileNameFromFilename(resolved)
		}
		team.Profiles = append(team.Profiles, ParsedProfile{
			Path:       resolved,
			Name:       name,
			Platform:   "windows",
			SourceFile: path,
		})
	}

	// apple_settings.configuration_profiles is the unified modern block that
	// supersedes macos_settings.custom_settings. Entries may be .mobileconfig
	// (macOS/iOS/iPadOS), .json (DDM declarations), or .xml. Diff identity is
	// by name (PayloadDisplayName or filename for DDM); platform is informational.
	for _, ref := range raw.Controls.AppleSettings.ConfigurationProfiles {
		resolved := filepath.Join(dir, ref.Path)
		if root != "" {
			if err := safePath(root, resolved); err != nil {
				errs = append(errs, ParseError{File: path, Message: err.Error()})
				continue
			}
		}
		name := extractProfileName(resolved)
		if name == "" {
			name = profileNameFromFilename(resolved)
		}
		team.Profiles = append(team.Profiles, ParsedProfile{
			Path:       resolved,
			Name:       name,
			Platform:   inferApplePlatform(ref.Path),
			SourceFile: path,
		})
	}

	return team, errs
}

// inferApplePlatform returns a coarse platform string based on the path
// segment. lib/ipados/* → ipados, lib/ios/* → ios, anything else → darwin
// (covers lib/macos/, lib/all/, lib/unassigned/, etc.). Fleet's diff identity
// is the embedded profile name, not platform — this is purely for display.
func inferApplePlatform(refPath string) string {
	clean := strings.ToLower(filepath.ToSlash(filepath.Clean(refPath)))
	// Match ios/ipados as complete path components so a root-level relative
	// path (e.g. "ios/profile.mobileconfig", with no leading slash) is still
	// classified correctly rather than falling through to darwin.
	for _, component := range strings.Split(clean, "/") {
		switch component {
		case "ipados":
			return "ipados"
		case "ios":
			return "ios"
		}
	}
	return "darwin"
}

// readYAMLRef resolves a path: reference, validates it stays within the repo
// root, reads the file, and returns the raw bytes and resolved path. This is
// the shared core of resolvePolicyRef, resolveQueryRef, and resolveSoftwareRef.
func readYAMLRef(root, baseDir, refPath, parentFile, label string) (data []byte, resolved string, errs []ParseError) {
	if refPath == "" {
		return nil, "", []ParseError{{File: parentFile, Message: fmt.Sprintf("empty %spath: reference", label)}}
	}

	resolved = filepath.Join(baseDir, refPath)
	if root != "" {
		if err := safePath(root, resolved); err != nil {
			return nil, "", []ParseError{{File: parentFile, Message: err.Error()}}
		}
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, "", []ParseError{{
			File:    parentFile,
			Message: fmt.Sprintf("%spath reference %q: %s", label, refPath, err),
		}}
	}
	return data, resolved, nil
}

// resolvePolicyRef reads a policy YAML file and returns parsed policies.
func resolvePolicyRef(root, baseDir, refPath, parentFile string) ([]ParsedPolicy, []ParseError) {
	data, resolved, errs := readYAMLRef(root, baseDir, refPath, parentFile, "")
	if errs != nil {
		return nil, errs
	}

	var items []ParsedPolicy
	if err := yaml.Unmarshal(data, &items); err != nil {
		var item ParsedPolicy
		if err2 := yaml.Unmarshal(data, &item); err2 == nil {
			items = []ParsedPolicy{item}
		} else {
			return nil, []ParseError{{File: resolved, Message: fmt.Sprintf("YAML parse error: %s", err)}}
		}
	}
	for i := range items {
		items[i].SourceFile = resolved
	}
	return items, nil
}

// resolveQueryRef reads a query YAML file and returns parsed queries.
func resolveQueryRef(root, baseDir, refPath, parentFile string) ([]ParsedQuery, []ParseError) {
	data, resolved, errs := readYAMLRef(root, baseDir, refPath, parentFile, "")
	if errs != nil {
		return nil, errs
	}

	var items []ParsedQuery
	if err := yaml.Unmarshal(data, &items); err != nil {
		var item ParsedQuery
		if err2 := yaml.Unmarshal(data, &item); err2 == nil {
			items = []ParsedQuery{item}
		} else {
			return nil, []ParseError{{File: resolved, Message: fmt.Sprintf("YAML parse error: %s", err)}}
		}
	}
	for i := range items {
		items[i].SourceFile = resolved
	}
	return items, nil
}

// resolveSoftwareRef reads a software package YAML file and resolves any
// install_script, uninstall_script, pre_install_query, or post_install_script
// path: references within it. The resolved paths are tracked in SourceFiles
// so the changed-file filter can match script-only MR changes.
func resolveSoftwareRef(root, baseDir, refPath, parentFile string) ([]ParsedSoftwarePackage, []ParseError) {
	data, resolved, errs := readYAMLRef(root, baseDir, refPath, parentFile, "software ")
	if errs != nil {
		return nil, errs
	}

	// A package file may be a single object (url: ...) or a list of them
	// (- url: ...). fleetctl gitops accepts both, so fall back to the list
	// form when object unmarshaling fails (mirrors resolvePolicyRef /
	// resolveQueryRef). Without this the list form is dropped and every
	// package it holds is falsely reported as REMOVED. A multi-item list
	// must yield one package per entry, not just the first.
	var raws []rawSoftwarePackage
	var single rawSoftwarePackage
	if err := yaml.Unmarshal(data, &single); err == nil {
		raws = []rawSoftwarePackage{single}
	} else if err2 := yaml.Unmarshal(data, &raws); err2 != nil {
		return nil, []ParseError{{File: resolved, Message: fmt.Sprintf("YAML parse error: %s", err)}}
	}

	pkgDir := filepath.Dir(resolved)
	pkgs := make([]ParsedSoftwarePackage, 0, len(raws))
	for _, raw := range raws {
		pkg := ParsedSoftwarePackage{
			URL:         raw.URL,
			HashSHA256:  raw.HashSHA256,
			SelfService: raw.SelfService,
			Categories:  raw.Categories,
			SourceFile:  resolved,
		}

		for _, ref := range []*rawPathRef{raw.InstallScript, raw.UninstallScript, raw.PreInstallQuery, raw.PostInstallScript} {
			if ref == nil || ref.Path == "" {
				continue
			}
			scriptPath := filepath.Join(pkgDir, ref.Path)
			if root != "" {
				if err := safePath(root, scriptPath); err != nil {
					errs = append(errs, ParseError{File: resolved, Message: err.Error()})
					continue
				}
			}
			pkg.SourceFiles = append(pkg.SourceFiles, scriptPath)
		}

		pkgs = append(pkgs, pkg)
	}

	return pkgs, errs
}

// resolveFleetApp resolves path: references in a fleet-maintained app entry,
// reading script and query file content. Non-fatal: missing optional files are
// logged as parse errors but the FMA is still returned.
func resolveFleetApp(root, baseDir string, raw rawFleetApp, parentFile string) (ParsedFleetApp, []ParseError) {
	var errs []ParseError
	fma := ParsedFleetApp{
		Slug:        raw.Slug,
		SelfService: raw.SelfService,
		Categories:  raw.Categories,
	}

	readScript := func(ref *rawPathRef, label string) string {
		if ref == nil || ref.Path == "" {
			return ""
		}
		data, resolved, readErrs := readYAMLRef(root, baseDir, ref.Path, parentFile, label+" ")
		if readErrs != nil {
			errs = append(errs, readErrs...)
			return ""
		}
		fma.SourceFiles = append(fma.SourceFiles, resolved)
		return strings.TrimSpace(string(data))
	}

	fma.InstallScript = readScript(raw.InstallScript, "install_script")
	fma.UninstallScript = readScript(raw.UninstallScript, "uninstall_script")
	fma.PostInstallScript = readScript(raw.PostInstallScript, "post_install_script")

	if raw.PreInstallQuery != nil && raw.PreInstallQuery.Path != "" {
		data, resolved, readErrs := readYAMLRef(root, baseDir, raw.PreInstallQuery.Path, parentFile, "pre_install_query ")
		if readErrs != nil {
			errs = append(errs, readErrs...)
		} else {
			fma.SourceFiles = append(fma.SourceFiles, resolved)
			fma.PreInstallQuery = extractQueryFromYAML(data)
		}
	}

	return fma, errs
}

// extractQueryFromYAML extracts the SQL query from a YAML file that may be a
// single object or a list of objects with a "query" field.
func extractQueryFromYAML(data []byte) string {
	type qObj struct {
		Query string `yaml:"query"`
	}
	var single qObj
	if yaml.Unmarshal(data, &single) == nil && single.Query != "" {
		return single.Query
	}
	var list []qObj
	if yaml.Unmarshal(data, &list) == nil && len(list) > 0 && list[0].Query != "" {
		return list[0].Query
	}
	return strings.TrimSpace(string(data))
}

// NormalizeSoftwarePath canonicalizes team YAML software package paths so
// they match Fleet API's software.packages[].referenced_yaml_path format.
// Example: "../software/mac/slack/slack.yml" -> "software/mac/slack/slack.yml"
func NormalizeSoftwarePath(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "./")
	for strings.HasPrefix(p, "../") {
		p = strings.TrimPrefix(p, "../")
	}
	p = strings.TrimPrefix(p, "/")
	return p
}

// parsedDefault is the internal result of parsing default.yml. The labels field
// is unexported because it gets moved to ParsedRepo.Labels by the caller.
type parsedDefault struct {
	*ParsedGlobal
	labels []ParsedLabel
}

// parseDefaultFile parses default.yml (the pre-merged global config) and extracts
// labels, global policies, global queries, org_settings, agent_options, and controls.
func parseDefaultFile(root, path string) (*parsedDefault, []ParseError) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []ParseError{{File: path, Message: fmt.Sprintf("could not read: %s", err)}}
	}

	// Parse as generic map first for org_settings, agent_options, controls
	var rawMap map[string]any
	if err := yaml.Unmarshal(data, &rawMap); err != nil {
		return nil, []ParseError{{File: path, Message: fmt.Sprintf("YAML parse error: %s", err)}}
	}

	// Parse structured fields (labels, policies, queries path refs).
	// `reports:` is the renamed-in-#40726 alias for `queries:`.
	var rawStruct struct {
		Labels   []rawPathRef `yaml:"labels"`
		Policies []rawPathRef `yaml:"policies"`
		Queries  []rawPathRef `yaml:"queries"`
		Reports  []rawPathRef `yaml:"reports"`
	}
	if err := yaml.Unmarshal(data, &rawStruct); err != nil {
		return nil, []ParseError{{File: path, Message: fmt.Sprintf("YAML parse error: %s", err)}}
	}

	global := &ParsedGlobal{SourceFile: path}
	var errs []ParseError
	dir := filepath.Dir(path)

	// Extract org_settings, agent_options, controls as raw maps
	if v, ok := rawMap["org_settings"]; ok {
		if m, ok := v.(map[string]any); ok {
			global.OrgSettings = m
		}
	}
	if v, ok := rawMap["agent_options"]; ok {
		if m, ok := v.(map[string]any); ok {
			global.AgentOptions = m
		}
	}
	if v, ok := rawMap["controls"]; ok {
		if m, ok := v.(map[string]any); ok {
			global.Controls = m
		}
	}

	// Resolve global policies
	for _, ref := range rawStruct.Policies {
		policies, parseErrs := resolvePolicyRef(root, dir, ref.Path, path)
		errs = append(errs, parseErrs...)
		global.Policies = append(global.Policies, policies...)
	}

	// Resolve global queries. Accept both `queries:` and the renamed
	// `reports:` (fleetdm/fleet#40726); see rawTeamFile for context.
	for _, ref := range append(append([]rawPathRef(nil), rawStruct.Queries...), rawStruct.Reports...) {
		queries, parseErrs := resolveQueryRef(root, dir, ref.Path, path)
		errs = append(errs, parseErrs...)
		global.Queries = append(global.Queries, queries...)
	}

	// Resolve labels
	var labels []ParsedLabel
	for _, ref := range rawStruct.Labels {
		if ref.Path == "" {
			continue
		}
		resolved := filepath.Join(dir, ref.Path)
		if root != "" {
			if err := safePath(root, resolved); err != nil {
				errs = append(errs, ParseError{File: path, Message: err.Error()})
				continue
			}
		}

		fileData, err := os.ReadFile(resolved)
		if err != nil {
			errs = append(errs, ParseError{
				File:    path,
				Message: fmt.Sprintf("label path reference %q: %s", ref.Path, err),
			})
			continue
		}

		var items []ParsedLabel
		if err := yaml.Unmarshal(fileData, &items); err != nil {
			errs = append(errs, ParseError{
				File:    resolved,
				Message: fmt.Sprintf("YAML parse error: %s", err),
			})
			continue
		}
		for i := range items {
			items[i].SourceFile = resolved
		}
		labels = append(labels, items...)
	}

	return &parsedDefault{ParsedGlobal: global, labels: labels}, errs
}

// ---------- Profile name extraction ----------

// extractProfileName reads a profile file and extracts the name that Fleet
// will use to identify it. This matches Fleet's own behavior:
//   - .mobileconfig: top-level PayloadDisplayName from the plist
//   - .json (DDM):   PayloadDisplayName from the JSON declaration
//   - .xml (Windows): Name element from the SyncML/CSP XML
//
// Returns empty string if extraction fails (caller should fall back to filename).
// maxProfileSize is the maximum file size we'll read for profile name extraction.
// Profiles are typically under 100 KB; 10 MB is a generous safety limit.
const maxProfileSize = 10 << 20

func extractProfileName(filePath string) string {
	// Fleet uses PayloadDisplayName for .mobileconfig files, but uses the
	// filename (without extension) for .json (DDM) and .xml (Windows) files.
	// See: server/service/apple_mdm.go SameProfileNameUploadErrorMsg
	lower := strings.ToLower(filePath)
	if !strings.HasSuffix(lower, ".mobileconfig") {
		// For .json and .xml, Fleet uses the filename — no content extraction needed.
		return ""
	}

	// Size guard to prevent OOM on oversized files.
	info, err := os.Stat(filePath)
	if err != nil || info.Size() > maxProfileSize {
		return ""
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}

	return extractMobileconfigName(data)
}

// extractMobileconfigName extracts the top-level PayloadDisplayName from a
// .mobileconfig plist. In Apple's profile format, the top-level dict contains
// both a PayloadContent array (with nested payload dicts that have their own
// PayloadDisplayName) and a top-level PayloadDisplayName. Fleet uses the
// top-level one as the profile identity.
//
// The order of PayloadDisplayName relative to PayloadContent is not
// standardized — Apple Configurator emits it before the array, ProfileCreator
// after, hand-edited files vary. So we can't rely on first/last position;
// instead we track <dict> nesting depth and only accept PayloadDisplayName
// at depth 1 (direct child of the root dict; depth 0 is <plist>).
func extractMobileconfigName(data []byte) string {
	s := string(data)
	const needle = "<key>PayloadDisplayName</key>"

	depth := 0 // <dict> nesting depth, starting at 0 (outside <plist>'s root <dict>).
	// dictOpen reports whether a <dict> tag at position i is an opening tag
	// (not self-closing).
	scanTags := func(seg string) {
		i := 0
		for i < len(seg) {
			lt := strings.IndexByte(seg[i:], '<')
			if lt < 0 {
				return
			}
			i += lt
			if strings.HasPrefix(seg[i:], "<dict>") {
				depth++
				i += len("<dict>")
				continue
			}
			if strings.HasPrefix(seg[i:], "<dict/>") {
				// Self-closing — no children; depth unchanged.
				i += len("<dict/>")
				continue
			}
			if strings.HasPrefix(seg[i:], "</dict>") {
				depth--
				i += len("</dict>")
				continue
			}
			// Skip past this tag.
			gt := strings.IndexByte(seg[i:], '>')
			if gt < 0 {
				return
			}
			i += gt + 1
		}
	}

	cursor := 0
	for {
		idx := strings.Index(s[cursor:], needle)
		if idx < 0 {
			break
		}
		absIdx := cursor + idx
		// Update depth based on every <dict>/</dict> between cursor and this match.
		scanTags(s[cursor:absIdx])
		// Skip past the <key>PayloadDisplayName</key> tag itself.
		cursor = absIdx + len(needle)
		// At depth 1, this is the top-level dict's PayloadDisplayName.
		if depth == 1 {
			after := strings.TrimSpace(s[cursor:])
			if strings.HasPrefix(after, "<string>") {
				after = after[len("<string>"):]
				if end := strings.Index(after, "</string>"); end >= 0 {
					// Decode XML entities so a name like "VPN &amp; Wi-Fi"
					// yields "VPN & Wi-Fi", matching the identity Fleet reports.
					return html.UnescapeString(strings.TrimSpace(after[:end]))
				}
			}
		}
	}
	return ""
}

// profileNameFromFilename derives a profile name from the filename by stripping
// the extension. This is the fallback when content extraction fails.
func profileNameFromFilename(filePath string) string {
	name := filepath.Base(filePath)
	for _, ext := range []string{".mobileconfig", ".json", ".xml"} {
		name = strings.TrimSuffix(name, ext)
	}
	return name
}

// ValidatePlatform checks if a platform string contains only valid identifiers.
func ValidatePlatform(platform string) []string {
	if platform == "" {
		return nil
	}
	var invalid []string
	for _, p := range strings.Split(platform, ",") {
		p = strings.TrimSpace(p)
		if p != "" && !ValidPlatforms[p] {
			invalid = append(invalid, p)
		}
	}
	return invalid
}

// ValidateLogging checks if a logging value is valid.
func ValidateLogging(logging string) bool {
	if logging == "" {
		return true // default is fine
	}
	return ValidLogging[logging]
}
