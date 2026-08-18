// Package diff computes semantic diffs between current Fleet state (from API)
// and proposed state (from parsed YAML). It produces structured DiffResult
// values showing what will be added, modified, or deleted.
package diff

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/CampusTech/fleet-plan/internal/api"
	"github.com/CampusTech/fleet-plan/internal/parser"
)

// wsRE collapses runs of whitespace (spaces, tabs, newlines) into a single space.
var wsRE = regexp.MustCompile(`\s+`)

// ---------- Result types ----------

// DiffResult holds the diff for a single team (or global scope).
type DiffResult struct {
	Team                  string // "(global)" for default.yml scope
	Policies              ResourceDiff
	Queries               ResourceDiff
	Software              ResourceDiff
	Profiles              ResourceDiff
	Scripts               ResourceDiff
	Labels                LabelValidation
	Config                []ConfigChange // org_settings, agent_options, controls diffs
	Errors                []string
	SkippedConfigSections []string // config sections absent from API (e.g. "agent_options")
}

// ConfigChange represents a change in a top-level config section.
type ConfigChange struct {
	Section string // "org_settings", "agent_options", "controls"
	Key     string // dot-separated path, e.g. "server_settings.server_url"
	Old     string
	New     string
}

// ResourceDiff categorizes changes for one resource type.
type ResourceDiff struct {
	Added    []ResourceChange
	Modified []ResourceChange
	Deleted  []ResourceChange
}

// IsEmpty returns true if there are no changes.
func (d ResourceDiff) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Modified) == 0 && len(d.Deleted) == 0
}

// Total returns the total number of changes.
func (d ResourceDiff) Total() int {
	return len(d.Added) + len(d.Modified) + len(d.Deleted)
}

// ResourceChange describes a single resource change.
type ResourceChange struct {
	Name      string
	Fields    map[string]FieldDiff // field name -> old/new values
	HostCount uint                 // from API: affected hosts
	Warning   string               // e.g., "will delete compliance data"
}

// FieldDiff shows old vs new value for a single field.
type FieldDiff struct {
	Old      string
	New      string
	OldSlice []string // non-nil for slice-typed fields (used by JSON renderer)
	NewSlice []string // non-nil for slice-typed fields (used by JSON renderer)
	IsSlice  bool     // true when OldSlice/NewSlice should be used for JSON output
}

// LabelValidation reports label cross-reference status.
type LabelValidation struct {
	Valid   []LabelRef
	Missing []LabelRef
}

// LabelRef is a label name with metadata.
type LabelRef struct {
	Name         string
	HostCount    uint   // only for valid labels
	ReferencedBy string // which policy/query references it
}

// ---------- Diff engine ----------

// ScriptEnricher fetches script content for inferred fleet-maintained apps.
// Implementations should populate InstallScript, UninstallScript, etc. on each
// TeamFleetApp that has a non-zero TitleID.
type ScriptEnricher interface {
	EnrichFleetAppScripts(ctx context.Context, apps []api.TeamFleetApp)
}

// DiffOption configures optional Diff behavior.
type DiffOption func(*diffOptions)

type diffOptions struct {
	enricher      ScriptEnricher
	baseline      *parser.ParsedRepo
	verbose       bool
	includeGlobal bool
}

// WithScriptEnricher enables script-level diffing for fleet-maintained apps.
func WithScriptEnricher(e ScriptEnricher) DiffOption {
	return func(o *diffOptions) { o.enricher = e }
}

// WithVerbose enables detailed stderr logging of baseline subtraction.
func WithVerbose(v bool) DiffOption {
	return func(o *diffOptions) { o.verbose = v }
}

// WithIncludeGlobal forces global config diffing even when team filters are
// set. Used in --git mode when both global and team files changed.
func WithIncludeGlobal(v bool) DiffOption {
	return func(o *diffOptions) { o.includeGlobal = v }
}

// WithBaseline provides a parsed base-branch repo. When set, Diff subtracts
// changes that already exist between the base branch and Fleet (i.e. changes
// merged to main but not yet deployed) so that only the incremental changes
// introduced by the current MR are reported.
func WithBaseline(b *parser.ParsedRepo) DiffOption {
	return func(o *diffOptions) { o.baseline = b }
}

// vlog writes to stderr when verbose mode is enabled.
func vlog(verbose bool, format string, args ...any) {
	if verbose {
		fmt.Fprintf(os.Stderr, "[verbose] "+format+"\n", args...)
	}
}

// rdSummary returns a one-line summary of a ResourceDiff.
func rdSummary(rd ResourceDiff) string {
	return fmt.Sprintf("+%d ~%d -%d", len(rd.Added), len(rd.Modified), len(rd.Deleted))
}

// noTeamSummary describes what a repo configures for the no-team bucket, as a
// comma-separated list of non-zero resource counts ("2 policies, 2 scripts").
// Returns "" when nothing is configured.
func noTeamSummary(t parser.ParsedTeam) string {
	softwareCount := len(t.Software.Packages) + len(t.Software.FleetMaintained) + len(t.Software.AppStoreApps)
	counts := []struct {
		n    int
		one  string
		many string
	}{
		{len(t.Policies), "policy", "policies"},
		{len(t.Queries), "query", "queries"},
		{len(t.Scripts), "script", "scripts"},
		{len(t.Profiles), "profile", "profiles"},
		{softwareCount, "software item", "software items"},
	}
	var parts []string
	for _, c := range counts {
		switch {
		case c.n == 1:
			parts = append(parts, "1 "+c.one)
		case c.n > 1:
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.many))
		}
	}
	return strings.Join(parts, ", ")
}

// rdNames returns names of changes for debugging.
func rdNames(rd ResourceDiff) string {
	var names []string
	for _, c := range rd.Added {
		names = append(names, "+"+c.Name)
	}
	for _, c := range rd.Modified {
		names = append(names, "~"+c.Name)
	}
	for _, c := range rd.Deleted {
		names = append(names, "-"+c.Name)
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

// Diff computes the diff between current Fleet state and proposed YAML for all
// teams. If teamFilters is non-empty, only matching teams are diffed.
// If changedFiles is non-empty, only resources whose SourceFile matches are
// included in the output (MR-scoped filtering).
// Global config from default.yml is diffed when proposed.Global is non-nil.
func Diff(current *api.FleetState, proposed *parser.ParsedRepo, teamFilters []string, changedFiles []string, opts ...DiffOption) []DiffResult {
	var cfg diffOptions
	for _, o := range opts {
		o(&cfg)
	}
	var results []DiffResult

	// Build label lookup from API
	labelMap := make(map[string]api.Label)
	for _, l := range current.Labels {
		labelMap[l.Name] = l
	}

	vlog(cfg.verbose, "baseline=%v, baseline.Global=%v, teamFilters=%v, changedFiles=%v",
		cfg.baseline != nil, cfg.baseline != nil && cfg.baseline.Global != nil, teamFilters, changedFiles)

	// --- Global config diff (default.yml) ---
	if proposed.Global != nil && (len(teamFilters) == 0 || cfg.includeGlobal) {
		vlog(cfg.verbose, "(global) proposed: %d policies, %d queries", len(proposed.Global.Policies), len(proposed.Global.Queries))
		vlog(cfg.verbose, "(global) fleet: %d policies, %d queries", len(current.GlobalPolicies), len(current.GlobalQueries))
		globalResult := DiffResult{Team: "(global)"}

		if current.Config != nil {
			globalResult.Config, globalResult.SkippedConfigSections = diffConfig(current.Config, proposed.Global)
		}

		// Diff global policies
		globalResult.Policies = diffPolicies(current.GlobalPolicies, proposed.Global.Policies)

		// Diff global queries
		globalResult.Queries = diffQueries(current.GlobalQueries, proposed.Global.Queries)

		vlog(cfg.verbose, "(global) MR diff: policies=%s queries=%s config=%d",
			rdSummary(globalResult.Policies), rdSummary(globalResult.Queries), len(globalResult.Config))

		// Subtract baseline for global scope
		if cfg.baseline != nil && cfg.baseline.Global != nil {
			vlog(cfg.verbose, "(global) baseline: %d policies, %d queries",
				len(cfg.baseline.Global.Policies), len(cfg.baseline.Global.Queries))
			var baseConfig []ConfigChange
			if current.Config != nil {
				baseConfig, _ = diffConfig(current.Config, cfg.baseline.Global)
			}
			basePolicies := diffPolicies(current.GlobalPolicies, cfg.baseline.Global.Policies)
			baseQueries := diffQueries(current.GlobalQueries, cfg.baseline.Global.Queries)

			vlog(cfg.verbose, "(global) baseline diff: policies=%s queries=%s config=%d",
				rdSummary(basePolicies), rdSummary(baseQueries), len(baseConfig))

			globalResult.Config = subtractConfigChanges(globalResult.Config, baseConfig)
			globalResult.Policies = subtractResourceDiff(globalResult.Policies, basePolicies)
			globalResult.Queries = subtractResourceDiff(globalResult.Queries, baseQueries)

			vlog(cfg.verbose, "(global) after subtraction: policies=%s queries=%s config=%d",
				rdSummary(globalResult.Policies), rdSummary(globalResult.Queries), len(globalResult.Config))
		}

		results = append(results, globalResult)
	}

	// --- Per-team diffs ---
	// Index current teams by name
	currentTeams := make(map[string]api.Team)
	for _, t := range current.Teams {
		currentTeams[t.Name] = t
	}

	for _, proposedTeam := range proposed.Teams {
		if len(teamFilters) > 0 && !parser.MatchesAnyTeam(proposedTeam.Name, teamFilters) {
			continue
		}

		result := DiffResult{Team: proposedTeam.Name}

		currentTeam, exists := currentTeams[proposedTeam.Name]
		if !exists {
			// Fleet's "hosts on no team" bucket -- "No team" in the teams/ layout,
			// "Unassigned" in the fleets/ layout -- always exists but isn't
			// returned by the /teams API endpoint. Skip the "will be created"
			// warning for it, and don't list its resources as additions.
			if parser.IsNoTeam(proposedTeam.Name, proposedTeam.SourceFile) {
				// Can't deep-diff against API state since it's not in the teams
				// list. Just report what the repo configures for it, so nothing
				// silently disappears from the plan.
				if summary := noTeamSummary(proposedTeam); summary != "" {
					result.Errors = append(result.Errors,
						fmt.Sprintf("%s configured (no API diff available for hosts on no team)", summary))
				}
			} else {
				// Genuinely new team
				for _, p := range proposedTeam.Policies {
					result.Policies.Added = append(result.Policies.Added, ResourceChange{Name: p.Name})
				}
				for _, q := range proposedTeam.Queries {
					result.Queries.Added = append(result.Queries.Added, ResourceChange{Name: q.Name})
				}
				result.Errors = append(result.Errors, fmt.Sprintf("info: team %q does not exist in Fleet yet (will be created)", proposedTeam.Name))
			}
		} else {
			vlog(cfg.verbose, "[%s] proposed: %d policies, %d queries", proposedTeam.Name,
				len(proposedTeam.Policies), len(proposedTeam.Queries))
			vlog(cfg.verbose, "[%s] fleet: %d policies, %d queries", proposedTeam.Name,
				len(currentTeam.Policies), len(currentTeam.Queries))

			result.Policies = diffPolicies(currentTeam.Policies, proposedTeam.Policies)
			result.Queries = diffQueries(currentTeam.Queries, proposedTeam.Queries)

			// enrichedSoftware holds the API software state with fleet-maintained
			// app scripts populated. Hoisted here so the baseline subtraction
			// can reuse the same enriched state.
			enrichedSoftware := currentTeam.Software

			if currentTeam.SoftwareUnavailable {
				result.Errors = append(result.Errors, "software diff skipped: API token lacks permission to read software titles")
			} else {
				// Fleet's /teams API may return fleet_maintained_apps: null, or
				// return a partial list (e.g., only macOS FMAs while Windows FMAs
				// are merged into packages). Infer from software titles + catalog
				// and merge with any API-provided entries to get the full picture.
				if len(proposedTeam.Software.FleetMaintained) > 0 {
					inferred := inferFleetMaintainedApps(currentTeam, current.FleetMaintainedCatalog, proposedTeam.Software.Packages)
					if cfg.enricher != nil && len(inferred) > 0 {
						cfg.enricher.EnrichFleetAppScripts(context.Background(), inferred)
					}
					enrichedSoftware.FleetMaintained = mergeFleetApps(currentTeam.Software.FleetMaintained, inferred)
				}

				result.Software = diffSoftware(enrichedSoftware, proposedTeam.Software)
			}

			if currentTeam.ProfilesUnavailable {
				result.Errors = append(result.Errors, "profiles diff skipped: API token lacks permission to read profiles")
			} else {
				var profileWarnings []string
				result.Profiles, profileWarnings = diffProfiles(currentTeam.Profiles, proposedTeam.Profiles, changedFiles)
				result.Errors = append(result.Errors, profileWarnings...)
			}

			if currentTeam.ScriptsUnavailable {
				result.Errors = append(result.Errors, "scripts diff skipped: API token lacks permission to read scripts")
			} else {
				result.Scripts = diffScripts(currentTeam.Scripts, proposedTeam.Scripts)
			}

			vlog(cfg.verbose, "[%s] MR diff: policies=%s queries=%s software=%s scripts=%s",
				proposedTeam.Name, rdSummary(result.Policies), rdSummary(result.Queries),
				rdSummary(result.Software), rdSummary(result.Scripts))
			if cfg.verbose {
				vlog(true, "[%s] MR queries: %s", proposedTeam.Name, rdNames(result.Queries))
			}

			// Subtract baseline: remove changes that already exist between the
			// base branch and Fleet (merged but not yet deployed).
			if cfg.baseline != nil {
				if baseTeam, ok := findBaselineTeam(cfg.baseline, proposedTeam.Name); ok {
					vlog(cfg.verbose, "[%s] baseline team found: %d policies, %d queries",
						proposedTeam.Name, len(baseTeam.Policies), len(baseTeam.Queries))
					baseDiff := DiffResult{}
					baseDiff.Policies = diffPolicies(currentTeam.Policies, baseTeam.Policies)
					baseDiff.Queries = diffQueries(currentTeam.Queries, baseTeam.Queries)
					if !currentTeam.SoftwareUnavailable {
						baseDiff.Software = diffSoftware(enrichedSoftware, baseTeam.Software)
					}
					if !currentTeam.ProfilesUnavailable {
						baseDiff.Profiles, _ = diffProfiles(currentTeam.Profiles, baseTeam.Profiles, nil)
					}
					if !currentTeam.ScriptsUnavailable {
						baseDiff.Scripts = diffScripts(currentTeam.Scripts, baseTeam.Scripts)
					}
					vlog(cfg.verbose, "[%s] baseline diff: policies=%s queries=%s software=%s",
						proposedTeam.Name, rdSummary(baseDiff.Policies),
						rdSummary(baseDiff.Queries), rdSummary(baseDiff.Software))
					if cfg.verbose {
						vlog(true, "[%s] baseline queries: %s", proposedTeam.Name, rdNames(baseDiff.Queries))
					}
					result.Policies = subtractResourceDiff(result.Policies, baseDiff.Policies)
					result.Queries = subtractResourceDiff(result.Queries, baseDiff.Queries)
					result.Software = subtractResourceDiff(result.Software, baseDiff.Software)
					result.Profiles = subtractResourceDiff(result.Profiles, baseDiff.Profiles)
					result.Scripts = subtractResourceDiff(result.Scripts, baseDiff.Scripts)
					vlog(cfg.verbose, "[%s] after subtraction: policies=%s queries=%s software=%s",
						proposedTeam.Name, rdSummary(result.Policies),
						rdSummary(result.Queries), rdSummary(result.Software))
				} else {
					vlog(cfg.verbose, "[%s] no baseline team found", proposedTeam.Name)
				}
			}
		}

		if len(changedFiles) > 0 {
			vlog(cfg.verbose, "[%s] before changedFiles filter: policies=%s queries=%s software=%s",
				proposedTeam.Name, rdSummary(result.Policies), rdSummary(result.Queries),
				rdSummary(result.Software))
			sourceNames := buildSourceMap(proposedTeam)
			result.Policies = filterResourceDiff(result.Policies, sourceNames, changedFiles)
			result.Queries = filterResourceDiff(result.Queries, sourceNames, changedFiles)
			result.Software = filterResourceDiff(result.Software, sourceNames, changedFiles)
			result.Profiles = filterResourceDiff(result.Profiles, sourceNames, changedFiles)
			result.Scripts = filterResourceDiff(result.Scripts, sourceNames, changedFiles)
			vlog(cfg.verbose, "[%s] after changedFiles filter: policies=%s queries=%s software=%s",
				proposedTeam.Name, rdSummary(result.Policies), rdSummary(result.Queries),
				rdSummary(result.Software))
		}

		result.Labels = validateLabels(proposedTeam, labelMap, changedNames(result.Policies))
		results = append(results, result)
	}

	return results
}

func buildSourceMap(team parser.ParsedTeam) map[string][]string {
	m := make(map[string][]string)
	add := func(name, src string) {
		if name != "" && src != "" {
			m[name] = append(m[name], src)
		}
	}
	for _, p := range team.Policies {
		add(p.Name, p.SourceFile)
		add(p.Name, team.SourceFile)
	}
	for _, q := range team.Queries {
		add(q.Name, q.SourceFile)
		add(q.Name, team.SourceFile)
	}
	for _, p := range team.Software.Packages {
		key := parser.NormalizeSoftwarePath(p.RefPath)
		if key == "" {
			key = inferSoftwarePathFromSource(p.SourceFile)
		}
		if key == "" {
			key = parser.NormalizeSoftwarePath(p.URL)
		}
		if key != "" {
			add(parser.NormalizeSoftwarePath(key), p.SourceFile)
			add(parser.NormalizeSoftwarePath(key), team.SourceFile)
			for _, sf := range p.SourceFiles {
				add(parser.NormalizeSoftwarePath(key), sf)
			}
		}
	}
	for _, f := range team.Software.FleetMaintained {
		slug := parser.NormalizeSoftwarePath(f.Slug)
		if slug == "" {
			continue
		}
		name := "fleet app " + slug
		add(name, team.SourceFile)
		for _, sf := range f.SourceFiles {
			add(name, sf)
		}
	}
	for _, p := range team.Profiles {
		add(p.Name, p.SourceFile)
		add(p.Name, team.SourceFile)
		add(p.Name, p.Path)
	}
	for _, s := range team.Scripts {
		add(s.Name, s.SourceFile)
		add(s.Name, team.SourceFile)
	}
	return m
}

func filterResourceDiff(rd ResourceDiff, sourceNames map[string][]string, changedFiles []string) ResourceDiff {
	match := func(name string) bool {
		srcs, ok := sourceNames[name]
		if !ok {
			return true
		}
		for _, src := range srcs {
			for _, cf := range changedFiles {
				if src == cf || strings.HasSuffix(src, "/"+cf) {
					return true
				}
			}
		}
		return false
	}
	return ResourceDiff{
		Added:    filterChanges(rd.Added, match),
		Modified: filterChanges(rd.Modified, match),
		Deleted:  filterChanges(rd.Deleted, match),
	}
}

func filterChanges(changes []ResourceChange, keep func(string) bool) []ResourceChange {
	var out []ResourceChange
	for _, c := range changes {
		if keep(c.Name) {
			out = append(out, c)
		}
	}
	return out
}

// ---------- Baseline subtraction ----------

// findBaselineTeam looks up a team by name in the baseline parsed repo.
func findBaselineTeam(baseline *parser.ParsedRepo, name string) (parser.ParsedTeam, bool) {
	for _, t := range baseline.Teams {
		if strings.EqualFold(t.Name, name) {
			return t, true
		}
	}
	return parser.ParsedTeam{}, false
}

// subtractResourceDiff removes changes from "total" that also appear in
// "baseline". A change is considered the same if it has the same Name and
// change type (added/modified/deleted).
//
// For modified resources, if the resource appears in both diffs but with
// different field changes, it is kept (the MR introduced additional changes
// beyond what the baseline already had).
func subtractResourceDiff(total, baseline ResourceDiff) ResourceDiff {
	return ResourceDiff{
		Added:    subtractChanges(total.Added, baseline.Added),
		Modified: subtractModified(total.Modified, baseline.Modified),
		Deleted:  subtractChanges(total.Deleted, baseline.Deleted),
	}
}

// subtractChanges removes entries from "total" whose Name matches an entry in
// "baseline". Used for Added and Deleted lists where name-match is sufficient.
func subtractChanges(total, baseline []ResourceChange) []ResourceChange {
	if len(baseline) == 0 {
		return total
	}
	baseNames := make(map[string]bool, len(baseline))
	for _, b := range baseline {
		baseNames[b.Name] = true
	}
	var out []ResourceChange
	for _, c := range total {
		if !baseNames[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

// subtractModified removes entries from "total" that have the exact same field
// diffs in "baseline". If a resource is modified in both but with different
// fields or values, it is kept (the MR changed it further).
func subtractModified(total, baseline []ResourceChange) []ResourceChange {
	if len(baseline) == 0 {
		return total
	}
	baseFields := make(map[string]map[string]FieldDiff, len(baseline))
	for _, b := range baseline {
		baseFields[b.Name] = b.Fields
	}
	var out []ResourceChange
	for _, c := range total {
		bf, exists := baseFields[c.Name]
		if !exists || !sameFieldDiffs(c.Fields, bf) {
			out = append(out, c)
		}
	}
	return out
}

// sameFieldDiffs returns true if two field diff maps are identical.
func sameFieldDiffs(a, b map[string]FieldDiff) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || av.Old != bv.Old || av.New != bv.New {
			return false
		}
	}
	return true
}

// subtractConfigChanges removes ConfigChange entries from "total" that also
// appear in "baseline" with the same Section, Key, Old, and New values.
func subtractConfigChanges(total, baseline []ConfigChange) []ConfigChange {
	if len(baseline) == 0 {
		return total
	}
	type configKey struct{ Section, Key, Old, New string }
	baseSet := make(map[configKey]bool, len(baseline))
	for _, b := range baseline {
		baseSet[configKey{b.Section, b.Key, b.Old, b.New}] = true
	}
	var out []ConfigChange
	for _, c := range total {
		if !baseSet[configKey{c.Section, c.Key, c.Old, c.New}] {
			out = append(out, c)
		}
	}
	return out
}

// ---------- Per-resource diffing ----------

func diffPolicies(current []api.Policy, proposed []parser.ParsedPolicy) ResourceDiff {
	var diff ResourceDiff

	currentMap := make(map[string]api.Policy)
	for _, p := range current {
		currentMap[p.Name] = p
	}

	proposedNames := make(map[string]bool)
	for _, p := range proposed {
		proposedNames[p.Name] = true
		cur, exists := currentMap[p.Name]
		if !exists {
			fields := map[string]FieldDiff{
				"query":    {New: normalizeWS(p.Query)},
				"platform": {New: p.Platform},
				"critical": {New: fmt.Sprint(p.Critical)},
			}
			if p.Description != "" {
				fields["description"] = FieldDiff{New: normalizeWS(p.Description)}
			}
			if p.Resolution != "" {
				fields["resolution"] = FieldDiff{New: normalizeWS(p.Resolution)}
			}
			diff.Added = append(diff.Added, ResourceChange{Name: p.Name, Fields: fields})
			continue
		}

		fields := make(map[string]FieldDiff)
		if normalizeWS(cur.Query) != normalizeWS(p.Query) {
			fields["query"] = FieldDiff{Old: normalizeWS(cur.Query), New: normalizeWS(p.Query)}
		}
		if normalizeWS(cur.Description) != normalizeWS(p.Description) {
			fields["description"] = FieldDiff{Old: normalizeWS(cur.Description), New: normalizeWS(p.Description)}
		}
		if normalizeWS(cur.Resolution) != normalizeWS(p.Resolution) {
			fields["resolution"] = FieldDiff{Old: normalizeWS(cur.Resolution), New: normalizeWS(p.Resolution)}
		}
		if cur.Platform != p.Platform {
			fields["platform"] = FieldDiff{Old: cur.Platform, New: p.Platform}
		}
		if cur.Critical != p.Critical {
			fields["critical"] = FieldDiff{Old: fmt.Sprint(cur.Critical), New: fmt.Sprint(p.Critical)}
		}

		if len(fields) > 0 {
			diff.Modified = append(diff.Modified, ResourceChange{
				Name:      p.Name,
				Fields:    fields,
				HostCount: cur.PassingHostCount + cur.FailingHostCount,
			})
		}
	}

	// Anything in current but not proposed will be deleted
	for _, cur := range current {
		if !proposedNames[cur.Name] {
			hostCount := cur.PassingHostCount + cur.FailingHostCount
			warning := ""
			if hostCount > 0 {
				warning = fmt.Sprintf("will delete policy affecting %d hosts", hostCount)
			}
			diff.Deleted = append(diff.Deleted, ResourceChange{
				Name:      cur.Name,
				HostCount: hostCount,
				Warning:   warning,
			})
		}
	}

	return diff
}

func diffQueries(current []api.Query, proposed []parser.ParsedQuery) ResourceDiff {
	var diff ResourceDiff

	currentMap := make(map[string]api.Query)
	for _, q := range current {
		currentMap[q.Name] = q
	}

	proposedNames := make(map[string]bool)
	for _, q := range proposed {
		proposedNames[q.Name] = true
		cur, exists := currentMap[q.Name]
		if !exists {
			fields := map[string]FieldDiff{
				"query":    {New: normalizeWS(q.Query)},
				"interval": {New: fmt.Sprint(q.Interval)},
				"platform": {New: q.Platform},
			}
			if q.Logging != "" {
				fields["logging"] = FieldDiff{New: q.Logging}
			}
			diff.Added = append(diff.Added, ResourceChange{Name: q.Name, Fields: fields})
			continue
		}

		fields := make(map[string]FieldDiff)
		if normalizeWS(cur.Query) != normalizeWS(q.Query) {
			fields["query"] = FieldDiff{Old: normalizeWS(cur.Query), New: normalizeWS(q.Query)}
		}
		if cur.Interval != q.Interval {
			fields["interval"] = FieldDiff{
				Old: fmt.Sprint(cur.Interval),
				New: fmt.Sprint(q.Interval),
			}
		}
		if cur.Platform != q.Platform {
			fields["platform"] = FieldDiff{Old: cur.Platform, New: q.Platform}
		}
		if cur.Logging != q.Logging && q.Logging != "" {
			fields["logging"] = FieldDiff{Old: cur.Logging, New: q.Logging}
		}

		if len(fields) > 0 {
			diff.Modified = append(diff.Modified, ResourceChange{
				Name:   q.Name,
				Fields: fields,
			})
		}
	}

	for _, cur := range current {
		if !proposedNames[cur.Name] {
			diff.Deleted = append(diff.Deleted, ResourceChange{Name: cur.Name})
		}
	}

	return diff
}

// categoriesEqual compares two category slices as sets (order-independent).
func categoriesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, v := range a {
		set[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := set[v]; !ok {
			return false
		}
	}
	return true
}

// sortedCategories returns a sorted copy of the slice.
func sortedCategories(cats []string) []string {
	cp := make([]string, len(cats))
	copy(cp, cats)
	sort.Strings(cp)
	return cp
}

// formatCategories returns a sorted display string like "[Browsers, Communication]".
func formatCategories(cats []string) string {
	if len(cats) == 0 {
		return "[]"
	}
	sorted := sortedCategories(cats)
	return "[" + strings.Join(sorted, ", ") + "]"
}

// disambiguateSoftwareKeys returns a collision-free diff key for each package.
// base[i] is a package's primary identity: its referenced YAML file path,
// which is how Fleet identifies an installer. Most files define exactly one
// package, so the base path is used verbatim — this keeps a URL change on that
// package a "modify" rather than a replace. When several packages share a base
// path (a list-form file with multiple entries), each colliding key is
// suffixed with the package URL so no sibling silently overwrites another in
// the diff map. Empty base keys pass through unchanged; callers skip them.
func disambiguateSoftwareKeys(base, urls []string) []string {
	counts := make(map[string]int, len(base))
	for _, b := range base {
		if b != "" {
			counts[b]++
		}
	}
	keys := make([]string, len(base))
	for i, b := range base {
		switch {
		case b == "":
			keys[i] = ""
		case counts[b] > 1:
			keys[i] = b + "#" + parser.NormalizeSoftwarePath(urls[i])
		default:
			keys[i] = b
		}
	}
	return keys
}

func diffSoftware(current api.TeamSoftware, proposed parser.ParsedSoftware) ResourceDiff {
	var rd ResourceDiff

	// -------- Packages (keyed by referenced_yaml_path) --------
	// The display name is always the base path; the map key may additionally
	// carry a URL discriminator when one file defines multiple packages.
	currentBase := make([]string, len(current.Packages))
	currentURLs := make([]string, len(current.Packages))
	for i, p := range current.Packages {
		b := parser.NormalizeSoftwarePath(p.ReferencedYAMLPath)
		if b == "" {
			b = parser.NormalizeSoftwarePath(p.URL)
		}
		currentBase[i] = b
		currentURLs[i] = p.URL
	}
	currentKeys := disambiguateSoftwareKeys(currentBase, currentURLs)
	currentPkgs := make(map[string]api.TeamSoftwarePackage, len(current.Packages))
	currentNames := make(map[string]string, len(current.Packages))
	for i, p := range current.Packages {
		if currentKeys[i] != "" {
			currentPkgs[currentKeys[i]] = p
			currentNames[currentKeys[i]] = currentBase[i]
		}
	}

	proposedBase := make([]string, len(proposed.Packages))
	proposedURLs := make([]string, len(proposed.Packages))
	for i, p := range proposed.Packages {
		b := parser.NormalizeSoftwarePath(p.RefPath)
		if b == "" {
			b = parser.NormalizeSoftwarePath(inferSoftwarePathFromSource(p.SourceFile))
		}
		if b == "" {
			b = parser.NormalizeSoftwarePath(p.URL)
		}
		proposedBase[i] = b
		proposedURLs[i] = p.URL
	}
	proposedKeys := disambiguateSoftwareKeys(proposedBase, proposedURLs)
	proposedPkgs := make(map[string]parser.ParsedSoftwarePackage, len(proposed.Packages))
	proposedNames := make(map[string]string, len(proposed.Packages))
	for i, p := range proposed.Packages {
		if proposedKeys[i] != "" {
			proposedPkgs[proposedKeys[i]] = p
			proposedNames[proposedKeys[i]] = proposedBase[i]
		}
	}

	for key, p := range proposedPkgs {
		cur, exists := currentPkgs[key]
		if !exists {
			fields := map[string]FieldDiff{
				"url":          {New: p.URL},
				"self_service": {New: fmt.Sprint(p.SelfService)},
			}
			if p.HashSHA256 != "" {
				fields["hash_sha256"] = FieldDiff{New: p.HashSHA256}
			}
			if len(p.Categories) > 0 {
				fields["categories"] = FieldDiff{
					New: formatCategories(p.Categories), IsSlice: true,
					NewSlice: sortedCategories(p.Categories),
				}
			}
			rd.Added = append(rd.Added, ResourceChange{Name: proposedNames[key], Fields: fields})
			continue
		}
		fields := make(map[string]FieldDiff)
		if cur.URL != p.URL {
			fields["url"] = FieldDiff{Old: cur.URL, New: p.URL}
		}
		if cur.HashSHA256 != p.HashSHA256 {
			fields["hash_sha256"] = FieldDiff{Old: cur.HashSHA256, New: p.HashSHA256}
		}
		if cur.SelfService != p.SelfService {
			fields["self_service"] = FieldDiff{Old: fmt.Sprint(cur.SelfService), New: fmt.Sprint(p.SelfService)}
		}
		if !categoriesEqual(cur.Categories, p.Categories) {
			fields["categories"] = FieldDiff{
				Old: formatCategories(cur.Categories), New: formatCategories(p.Categories),
				IsSlice: true, OldSlice: sortedCategories(cur.Categories), NewSlice: sortedCategories(p.Categories),
			}
		}
		if len(fields) > 0 {
			rd.Modified = append(rd.Modified, ResourceChange{
				Name:   proposedNames[key],
				Fields: fields,
			})
		}
	}
	for key := range currentPkgs {
		if _, exists := proposedPkgs[key]; !exists {
			rd.Deleted = append(rd.Deleted, ResourceChange{Name: currentNames[key]})
		}
	}

	// -------- Fleet-maintained apps (keyed by slug) --------
	currentFleet := make(map[string]api.TeamFleetApp)
	for _, a := range current.FleetMaintained {
		slug := parser.NormalizeSoftwarePath(a.Slug)
		if slug != "" {
			currentFleet[slug] = a
		}
	}
	proposedFleet := make(map[string]parser.ParsedFleetApp)
	for _, a := range proposed.FleetMaintained {
		slug := parser.NormalizeSoftwarePath(a.Slug)
		if slug != "" {
			proposedFleet[slug] = a
		}
	}

	for slug, a := range proposedFleet {
		cur, exists := currentFleet[slug]
		if !exists {
			addedFields := map[string]FieldDiff{
				"slug":         {New: a.Slug},
				"self_service": {New: fmt.Sprint(a.SelfService)},
			}
			if len(a.Categories) > 0 {
				addedFields["categories"] = FieldDiff{
					New: formatCategories(a.Categories), IsSlice: true,
					NewSlice: sortedCategories(a.Categories),
				}
			}
			rd.Added = append(rd.Added, ResourceChange{
				Name:   "fleet app " + slug,
				Fields: addedFields,
			})
			continue
		}
		fields := make(map[string]FieldDiff)
		if cur.SelfService != a.SelfService {
			fields["self_service"] = FieldDiff{
				Old: fmt.Sprint(cur.SelfService),
				New: fmt.Sprint(a.SelfService),
			}
		}
		if !categoriesEqual(cur.Categories, a.Categories) {
			fields["categories"] = FieldDiff{
				Old: formatCategories(cur.Categories), New: formatCategories(a.Categories),
				IsSlice: true, OldSlice: sortedCategories(cur.Categories), NewSlice: sortedCategories(a.Categories),
			}
		}
		for _, sc := range []struct {
			name   string
			curVal string
			newVal string
		}{
			{"install_script", cur.InstallScript, a.InstallScript},
			{"uninstall_script", cur.UninstallScript, a.UninstallScript},
			{"pre_install_query", cur.PreInstallQuery, a.PreInstallQuery},
			{"post_install_script", cur.PostInstallScript, a.PostInstallScript},
		} {
			if sc.curVal != "" && sc.newVal != "" &&
				normalizeScript(sc.curVal) != normalizeScript(sc.newVal) {
				fields[sc.name] = FieldDiff{
					New: scriptDiffSummary(normalizeScript(sc.curVal), normalizeScript(sc.newVal)),
				}
			}
		}
		if len(fields) > 0 {
			rd.Modified = append(rd.Modified, ResourceChange{
				Name:   "fleet app " + slug,
				Fields: fields,
			})
		}
	}
	for slug := range currentFleet {
		if _, exists := proposedFleet[slug]; !exists {
			rd.Deleted = append(rd.Deleted, ResourceChange{Name: "fleet app " + slug})
		}
	}

	// -------- App Store apps (keyed by app_store_id) --------
	currentApps := make(map[string]api.TeamAppStoreApp)
	for _, a := range current.AppStoreApps {
		id := strings.TrimSpace(a.AppStoreID)
		if id != "" {
			currentApps[id] = a
		}
	}
	proposedApps := make(map[string]parser.ParsedAppStoreApp)
	for _, a := range proposed.AppStoreApps {
		id := strings.TrimSpace(a.AppStoreID)
		if id != "" {
			proposedApps[id] = a
		}
	}

	for id, a := range proposedApps {
		cur, exists := currentApps[id]
		if !exists {
			addedFields := map[string]FieldDiff{
				"app_store_id": {New: a.AppStoreID},
				"self_service": {New: fmt.Sprint(a.SelfService)},
			}
			if len(a.Categories) > 0 {
				addedFields["categories"] = FieldDiff{
					New: formatCategories(a.Categories), IsSlice: true,
					NewSlice: sortedCategories(a.Categories),
				}
			}
			rd.Added = append(rd.Added, ResourceChange{
				Name:   "app store app " + id,
				Fields: addedFields,
			})
			continue
		}
		fields := make(map[string]FieldDiff)
		if cur.SelfService != a.SelfService {
			fields["self_service"] = FieldDiff{
				Old: fmt.Sprint(cur.SelfService),
				New: fmt.Sprint(a.SelfService),
			}
		}
		if !categoriesEqual(cur.Categories, a.Categories) {
			fields["categories"] = FieldDiff{
				Old: formatCategories(cur.Categories), New: formatCategories(a.Categories),
				IsSlice: true, OldSlice: sortedCategories(cur.Categories), NewSlice: sortedCategories(a.Categories),
			}
		}
		if len(fields) > 0 {
			rd.Modified = append(rd.Modified, ResourceChange{
				Name:   "app store app " + id,
				Fields: fields,
			})
		}
	}
	for id := range currentApps {
		if _, exists := proposedApps[id]; !exists {
			rd.Deleted = append(rd.Deleted, ResourceChange{Name: "app store app " + id})
		}
	}

	sortResourceChanges(&rd)
	return rd
}

func inferSoftwarePathFromSource(source string) string {
	source = strings.ReplaceAll(source, "\\", "/")
	if idx := strings.Index(source, "/software/"); idx >= 0 {
		return strings.TrimPrefix(source[idx+1:], "/")
	}
	return ""
}

func sortResourceChanges(rd *ResourceDiff) {
	byName := func(a, b ResourceChange) bool { return a.Name < b.Name }
	sort.Slice(rd.Added, func(i, j int) bool { return byName(rd.Added[i], rd.Added[j]) })
	sort.Slice(rd.Modified, func(i, j int) bool { return byName(rd.Modified[i], rd.Modified[j]) })
	sort.Slice(rd.Deleted, func(i, j int) bool { return byName(rd.Deleted[i], rd.Deleted[j]) })
}

// mergeFleetApps combines API-provided FMAs with inferred ones. API entries
// take precedence for slugs that appear in both lists.
func mergeFleetApps(apiApps, inferred []api.TeamFleetApp) []api.TeamFleetApp {
	seen := make(map[string]bool, len(apiApps))
	merged := make([]api.TeamFleetApp, 0, len(apiApps)+len(inferred))
	for _, a := range apiApps {
		slug := parser.NormalizeSoftwarePath(a.Slug)
		seen[slug] = true
		merged = append(merged, a)
	}
	for _, a := range inferred {
		slug := parser.NormalizeSoftwarePath(a.Slug)
		if !seen[slug] {
			merged = append(merged, a)
		}
	}
	return merged
}

func inferFleetMaintainedApps(team api.Team, catalog []api.FleetMaintainedApp, proposedPackages []parser.ParsedSoftwarePackage) []api.TeamFleetApp {
	if len(team.SoftwareTitles) == 0 || len(catalog) == 0 {
		return nil
	}

	// Build exclusion set from YAML-defined custom packages. We cannot use
	// team.Software.Packages because the API merges fleet-maintained apps
	// into that list when fleet_maintained_apps is null. The proposed YAML
	// packages are the authoritative set of custom (non-FMA) software.
	proposedPkgURLs := make(map[string]bool)
	for _, p := range proposedPackages {
		u := parser.NormalizeSoftwarePath(p.URL)
		if u != "" {
			proposedPkgURLs[u] = true
		}
	}

	catalogByAppID := make(map[uint]api.FleetMaintainedApp)
	catalogByTitleID := make(map[uint]api.FleetMaintainedApp)
	catalogByNamePlatform := make(map[string][]api.FleetMaintainedApp)
	catalogByPlatform := make(map[string][]api.FleetMaintainedApp)
	for _, app := range catalog {
		if app.ID != 0 {
			catalogByAppID[app.ID] = app
		}
		if app.SoftwareTitleID != 0 {
			catalogByTitleID[app.SoftwareTitleID] = app
		}
		key := fleetCatalogKey(app.Name, app.Platform)
		if key != "" {
			catalogByNamePlatform[key] = append(catalogByNamePlatform[key], app)
		}
		plat := normalizeFleetPlatform(app.Platform)
		if plat != "" {
			catalogByPlatform[plat] = append(catalogByPlatform[plat], app)
		}
	}

	inferred := make(map[string]api.TeamFleetApp)
	for _, title := range team.SoftwareTitles {
		if title.AppStoreApp != nil {
			continue
		}
		if title.SoftwarePackage == nil {
			continue
		}

		packageURL := parser.NormalizeSoftwarePath(title.SoftwarePackage.PackageURL)
		if packageURL != "" && proposedPkgURLs[packageURL] {
			continue
		}

		record := func(slug string) {
			inferred[slug] = api.TeamFleetApp{
				Slug:        slug,
				SelfService: title.SoftwarePackage.SelfService,
				TitleID:     title.ID,
				TeamID:      team.ID,
			}
		}

		// Strategy 1: match by fleet_maintained_app_id (exact, from API).
		if title.SoftwarePackage.FleetMaintainedAppID != nil {
			if app, ok := catalogByAppID[*title.SoftwarePackage.FleetMaintainedAppID]; ok {
				slug := parser.NormalizeSoftwarePath(app.Slug)
				if slug != "" {
					record(slug)
					continue
				}
			}
		}

		// Strategy 2: match by SoftwareTitleID -> catalog SoftwareTitleID.
		if app, ok := catalogByTitleID[title.ID]; ok {
			slug := parser.NormalizeSoftwarePath(app.Slug)
			if slug != "" {
				record(slug)
				continue
			}
		}

		// Strategy 3: match by name|platform (requires exactly 1 catalog hit).
		// Windows titles often include arch suffixes (e.g., "Notepad++ (64-bit x64)")
		// that the catalog omits, so try the raw name first, then stripped.
		// The catalog uses short marketing names (e.g., "OBS", "Zoom") while the
		// OS reports full product names (e.g., "OBS Studio", "Zoom Workplace"),
		// so fall back to prefix matching when exact/stripped matching fails.
		key := fleetCatalogKey(title.Name, title.SoftwarePackage.Platform)
		matches := catalogByNamePlatform[key]
		if len(matches) != 1 {
			stripped := stripArchSuffix(title.Name)
			if stripped != strings.TrimSpace(strings.ToLower(title.Name)) {
				key = fleetCatalogKey(stripped, title.SoftwarePackage.Platform)
				matches = catalogByNamePlatform[key]
			}
		}
		if len(matches) != 1 {
			matches = catalogPrefixMatch(title.Name, title.SoftwarePackage.Platform, catalogByPlatform)
		}
		if len(matches) != 1 {
			continue
		}

		slug := parser.NormalizeSoftwarePath(matches[0].Slug)
		if slug == "" {
			continue
		}
		record(slug)
	}

	if len(inferred) == 0 {
		return nil
	}
	out := make([]api.TeamFleetApp, 0, len(inferred))
	for _, app := range inferred {
		out = append(out, app)
	}
	return out
}

// catalogPrefixMatch finds catalog entries whose name is a prefix of the
// title name on the same platform. Returns matches only when exactly one
// catalog entry qualifies (ambiguous results are discarded). This handles
// cases where the catalog uses short marketing names ("OBS", "Zoom") while
// the OS reports full product names ("OBS Studio", "Zoom Workplace").
func catalogPrefixMatch(titleName, titlePlatform string, byPlatform map[string][]api.FleetMaintainedApp) []api.FleetMaintainedApp {
	plat := normalizeFleetPlatform(titlePlatform)
	candidates := byPlatform[plat]
	if len(candidates) == 0 {
		return nil
	}
	norm := strings.TrimSpace(strings.ToLower(stripArchSuffix(titleName)))
	if norm == "" {
		return nil
	}
	var hits []api.FleetMaintainedApp
	for _, app := range candidates {
		catName := strings.TrimSpace(strings.ToLower(app.Name))
		if catName != "" && strings.HasPrefix(norm, catName) {
			hits = append(hits, app)
		}
	}
	return hits
}

func fleetCatalogKey(name, platform string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	platform = normalizeFleetPlatform(platform)
	if name == "" || platform == "" {
		return ""
	}
	return name + "|" + platform
}

var archSuffixRe = regexp.MustCompile(`(?i)\s*\((?:x64|x86|64-bit(?:\s+x64)?|32-bit|arm64|amd64)\)\s*$`)

func stripArchSuffix(name string) string {
	return strings.TrimSpace(archSuffixRe.ReplaceAllString(name, ""))
}

func normalizeFleetPlatform(platform string) string {
	p := strings.TrimSpace(strings.ToLower(platform))
	switch p {
	case "macos":
		return "darwin"
	default:
		return p
	}
}

func diffProfiles(current []api.Profile, proposed []parser.ParsedProfile, changedFiles []string) (ResourceDiff, []string) {
	var diff ResourceDiff
	var warnings []string

	currentMap := make(map[string]api.Profile)
	for _, p := range current {
		currentMap[p.Name] = p
	}

	// Build a set of changed file paths for fast lookup.
	changedSet := make(map[string]bool, len(changedFiles))
	for _, cf := range changedFiles {
		changedSet[cf] = true
	}

	// Profile identity is determined by the name embedded in the file content
	// (e.g., PayloadDisplayName for .mobileconfig), which is what Fleet uses.
	// The parser extracts this into ParsedProfile.Name during parsing.
	proposedNames := make(map[string]bool)
	for _, p := range proposed {
		name := p.Name
		if proposedNames[name] {
			warnings = append(warnings, fmt.Sprintf("duplicate profile name %q derived from %q (conflicts with another profile)", name, p.Path))
		}
		proposedNames[name] = true
		if _, exists := currentMap[name]; !exists {
			fields := map[string]FieldDiff{
				"platform": {New: p.Platform},
			}
			if p.Path != "" {
				fields["path"] = FieldDiff{New: p.Path}
			}
			diff.Added = append(diff.Added, ResourceChange{
				Name:   name,
				Fields: fields,
			})
		} else if changedSet[p.Path] {
			// The Fleet API does not return profile content, so we cannot
			// compare payloads. When the profile XML file appears in the
			// git changed-files list, treat it as modified.
			diff.Modified = append(diff.Modified, ResourceChange{
				Name: name,
				Fields: map[string]FieldDiff{
					"path": {New: p.Path},
				},
			})
		}
	}

	for _, cur := range current {
		if !proposedNames[cur.Name] {
			diff.Deleted = append(diff.Deleted, ResourceChange{
				Name: cur.Name,
			})
		}
	}

	return diff, warnings
}

// diffScripts compares current scripts (from API) against proposed scripts
// (from YAML). Scripts are matched by filename. Content is compared when both
// sides have non-empty content.
func diffScripts(current []api.Script, proposed []parser.ParsedScript) ResourceDiff {
	var diff ResourceDiff

	currentMap := make(map[string]api.Script)
	for _, s := range current {
		currentMap[s.Name] = s
	}

	proposedNames := make(map[string]bool)
	for _, s := range proposed {
		proposedNames[s.Name] = true
		cur, exists := currentMap[s.Name]
		if !exists {
			diff.Added = append(diff.Added, ResourceChange{
				Name:   s.Name,
				Fields: map[string]FieldDiff{"path": {New: s.Path}},
			})
			continue
		}
		// Compare content when both sides are available.
		// Normalize line endings (\r\n → \n) and trim before comparing.
		if cur.Content != "" && s.Content != "" &&
			normalizeScript(cur.Content) != normalizeScript(s.Content) {
			diff.Modified = append(diff.Modified, ResourceChange{
				Name:    s.Name,
				Warning: scriptDiffSummary(normalizeScript(cur.Content), normalizeScript(s.Content)),
			})
		}
	}

	for _, cur := range current {
		if !proposedNames[cur.Name] {
			diff.Deleted = append(diff.Deleted, ResourceChange{Name: cur.Name})
		}
	}

	return diff
}

// ---------- Label validation ----------

// changedNames collects all resource names from a ResourceDiff (added + modified + deleted).
func changedNames(d ResourceDiff) map[string]bool {
	names := make(map[string]bool, d.Total())
	for _, c := range d.Added {
		names[c.Name] = true
	}
	for _, c := range d.Modified {
		names[c.Name] = true
	}
	for _, c := range d.Deleted {
		names[c.Name] = true
	}
	return names
}

// validateLabels checks labels referenced by changed policies only.
// If no policies changed, the label section is empty.
func validateLabels(team parser.ParsedTeam, labelMap map[string]api.Label, changed map[string]bool) LabelValidation {
	var validation LabelValidation
	seen := make(map[string]bool)

	checkLabel := func(name, referencedBy string) {
		if seen[name] {
			return
		}
		seen[name] = true
		if l, ok := labelMap[name]; ok {
			validation.Valid = append(validation.Valid, LabelRef{
				Name:         name,
				HostCount:    l.HostCount,
				ReferencedBy: referencedBy,
			})
		} else {
			validation.Missing = append(validation.Missing, LabelRef{
				Name:         name,
				ReferencedBy: referencedBy,
			})
		}
	}

	for _, p := range team.Policies {
		if !changed[p.Name] {
			continue
		}
		for _, name := range p.LabelsIncludeAny {
			checkLabel(name, p.Name)
		}
		for _, name := range p.LabelsExcludeAny {
			checkLabel(name, p.Name)
		}
	}

	return validation
}

// ---------- Global config diffing ----------

// diffConfig compares the current Fleet config (from API) against proposed
// global config sections from default.yml. Returns a list of config changes.
// Skips values containing "$" (env var placeholders that Fleet substitutes).
func diffConfig(apiConfig map[string]any, proposed *parser.ParsedGlobal) ([]ConfigChange, []string) {
	var changes []ConfigChange
	var skipped []string

	sections := map[string]map[string]any{
		"org_settings":  proposed.OrgSettings,
		"agent_options": proposed.AgentOptions,
		"controls":      proposed.Controls,
	}

	for section, proposedMap := range sections {
		if proposedMap == nil {
			continue
		}

		var apiSection map[string]any
		switch section {
		case "org_settings":
			apiSection = apiConfig
		case "agent_options":
			if v, ok := apiConfig["agent_options"]; ok {
				if m, ok := v.(map[string]any); ok {
					apiSection = m
				}
			}
		case "controls":
			if v, ok := apiConfig["mdm"]; ok {
				if m, ok := v.(map[string]any); ok {
					apiSection = m
				}
			}
		}

		if apiSection == nil {
			skipped = append(skipped, section)
			continue
		}

		// Recursive key-by-key comparison.
		// Only report a diff when the API actually has a value for this key.
		// If getNestedValue returns "" the API doesn't expose the field (e.g.
		// agent_options sub-keys, sso_settings) and we can't determine whether
		// the proposed value differs from what Fleet already has.
		flattenMap(proposedMap, "", func(key, proposedVal string) {
			if containsEnvVar(proposedVal) {
				return
			}
			if proposedVal == "<nil>" || proposedVal == "" {
				return
			}
			apiVal := getNestedValue(apiSection, key)
			if apiVal == "<nil>" || apiVal == "" {
				return
			}
			compareAPI, compareProposed := apiVal, proposedVal
			if looksLikeJSON(apiVal) && looksLikeJSON(proposedVal) {
				compareAPI = normalizeJSON(apiVal)
				compareProposed = normalizeJSON(proposedVal)
			}
			if compareAPI != compareProposed {
				changes = append(changes, ConfigChange{
					Section: section,
					Key:     key,
					Old:     apiVal,
					New:     proposedVal,
				})
			}
		})
	}

	return changes, skipped
}

// containsEnvVar returns true if the string contains a $ (env var placeholder).
func containsEnvVar(s string) bool {
	return strings.Contains(s, "$")
}

func looksLikeJSON(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) > 0 && (s[0] == '{' || s[0] == '[')
}

// normalizeJSON unmarshals a JSON string, recursively removes keys with empty
// string values (""), empty arrays ([]), and null values, then re-marshals with
// sorted keys. Returns the original string on error or if input is not JSON.
// Used to compare semantically equivalent JSON where API includes empty keys
// and YAML omits them.
func normalizeJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || (len(s) > 0 && s[0] != '{' && s[0] != '[') {
		return s
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	cleaned := removeEmptyJSONValues(v)
	b, err := json.Marshal(cleaned)
	if err != nil {
		return s
	}
	return string(b)
}

func removeEmptyJSONValues(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any)
		for k, val := range x {
			if val == nil {
				continue
			}
			if s, ok := val.(string); ok && s == "" {
				continue
			}
			if arr, ok := val.([]any); ok && len(arr) == 0 {
				continue
			}
			out[k] = removeEmptyJSONValues(val)
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, elem := range x {
			if elem == nil {
				continue
			}
			if s, ok := elem.(string); ok && s == "" {
				continue
			}
			if arr, ok := elem.([]any); ok && len(arr) == 0 {
				continue
			}
			out = append(out, removeEmptyJSONValues(elem))
		}
		return out
	default:
		return v
	}
}

// flattenMap recursively flattens a nested map into dot-separated key paths.
// Calls fn(key, value) for each leaf value. Slices are serialized to JSON
// for stable, order-independent comparison.
func flattenMap(m map[string]any, prefix string, fn func(key, val string)) {
	for k, v := range m {
		fullKey := k
		if prefix != "" {
			fullKey = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			flattenMap(val, fullKey, fn)
		case []any:
			b, err := json.Marshal(val)
			if err != nil {
				fn(fullKey, fmt.Sprint(val))
			} else {
				fn(fullKey, string(b))
			}
		default:
			fn(fullKey, fmt.Sprint(v))
		}
	}
}

// getNestedValue retrieves a value from a nested map using a dot-separated key.
// Slices are JSON-serialized to match flattenMap's output format.
func getNestedValue(m map[string]any, key string) string {
	parts := strings.Split(key, ".")
	current := m
	for i, part := range parts {
		v, ok := current[part]
		if !ok {
			return ""
		}
		if i == len(parts)-1 {
			if slice, ok := v.([]any); ok {
				b, err := json.Marshal(slice)
				if err != nil {
					return fmt.Sprint(v)
				}
				return string(b)
			}
			return fmt.Sprint(v)
		}
		if next, ok := v.(map[string]any); ok {
			current = next
		} else {
			return ""
		}
	}
	return ""
}

// ---------- Helpers ----------

// scriptDiffSummary returns a human-readable summary of what changed between
// two normalized script contents. Uses a compact "+added/-deleted" format,
// omitting zero counts. For single-line changes it includes the line number
// (e.g. "+5", "-3", "~2").
func scriptDiffSummary(old, new string) string {
	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(new, "\n")

	// Bag-of-lines: count lines present in new but not old (inserted)
	// and lines present in old but not new (deleted).
	oldSet := make(map[string]int)
	for _, l := range oldLines {
		oldSet[l]++
	}
	newSet := make(map[string]int)
	for _, l := range newLines {
		newSet[l]++
	}

	inserted := 0
	for l, count := range newSet {
		if diff := count - oldSet[l]; diff > 0 {
			inserted += diff
		}
	}
	deleted := 0
	for l, count := range oldSet {
		if diff := count - newSet[l]; diff > 0 {
			deleted += diff
		}
	}

	if inserted+deleted == 1 {
		// Single change: find its line number.
		maxLen := len(oldLines)
		if len(newLines) > maxLen {
			maxLen = len(newLines)
		}
		for i := 0; i < maxLen; i++ {
			var o, n string
			if i < len(oldLines) {
				o = oldLines[i]
			}
			if i < len(newLines) {
				n = newLines[i]
			}
			if o != n {
				ln := i + 1
				if o == "" {
					return fmt.Sprintf("+%d", ln)
				}
				if n == "" {
					return fmt.Sprintf("-%d", ln)
				}
				return fmt.Sprintf("~%d", ln)
			}
		}
	}

	// Compact format, omitting zero counts.
	var parts []string
	if inserted > 0 {
		parts = append(parts, fmt.Sprintf("+%d", inserted))
	}
	if deleted > 0 {
		parts = append(parts, fmt.Sprintf("-%d", deleted))
	}
	return strings.Join(parts, "/")
}

// normalizeScript normalizes a script for comparison: trims whitespace and
// converts \r\n to \n so line-ending differences don't cause false positives.
func normalizeScript(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

// normalizeWS collapses all whitespace runs into a single space and trims.
// Used for comparison only — the raw values are stored in FieldDiff so the
// renderer can show full content in verbose mode.
func normalizeWS(s string) string {
	return strings.TrimSpace(wsRE.ReplaceAllString(s, " "))
}
