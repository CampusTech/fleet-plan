// Package output formats DiffResult for display.
// terminal.go renders colored output to the terminal.
package output

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/CampusTech/fleet-plan/internal/diff"
)

// Terminal color palette.
var (
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // additions
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // modifications
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // deletions
	bold   = lipgloss.NewStyle().Bold(true)
	dim    = lipgloss.NewStyle().Faint(true)
)

const (
	maxLineWidth = 80  // target line width for truncation
	maxFields    = 3   // max fields shown in default mode before "... and N more"
	fieldIndent  = "        " // 8 spaces for field lines under resource name
)

// DiffSummary holds counts for summary rendering.
type DiffSummary struct {
	Added    int
	Modified int
	Deleted  int
	Errors   int
	Labels   struct {
		Valid   int
		Missing int
	}
}

// RenderDiffTerminal renders diff results to styled terminal output.
// When verbose=false (default), only shows field names that changed.
// When verbose=true, shows full old→new values for every changed field.
func RenderDiffTerminal(results []diff.DiffResult, verbose bool) string {
	var sb strings.Builder
	summary := DiffSummary{}

	for _, result := range results {
		content := renderTeamDiff(result, &summary, verbose)
		if content != "" {
			var header string
			if result.Team == "(global)" {
				header = bold.Render("Global (default.yml)")
			} else {
				header = bold.Render(fmt.Sprintf("Team: %s", result.Team))
			}
			sb.WriteString(header + "\n" + content)
			sb.WriteString("\n\n")
		}
	}

	// Label validation
	labelContent := renderLabels(results)
	if labelContent != "" {
		sb.WriteString(labelContent)
		sb.WriteString("\n")
	}

	// Count deduplicated missing labels for summary bar
	missingSeen := make(map[string]bool)
	for _, r := range results {
		for _, l := range r.Labels.Missing {
			missingSeen[l.Name] = true
		}
	}
	summary.Labels.Missing = len(missingSeen)

	// Summary
	sb.WriteString(renderSummaryBar(summary))

	return strings.TrimRight(sb.String(), "\n")
}

func renderTeamDiff(result diff.DiffResult, summary *DiffSummary, verbose bool) string {
	var lines []string

	// Config changes (global scope only)
	if len(result.Config) > 0 {
		lines = append(lines, renderConfigChanges(result.Config, summary, verbose))
	}

	if !result.Policies.IsEmpty() {
		lines = append(lines, renderResourceDiff("Policies", result.Policies, summary, verbose))
	}
	if !result.Queries.IsEmpty() {
		lines = append(lines, renderResourceDiff("Queries", result.Queries, summary, verbose))
	}
	if !result.Software.IsEmpty() {
		lines = append(lines, renderResourceDiff("Software", result.Software, summary, verbose))
	}
	if !result.Profiles.IsEmpty() {
		lines = append(lines, renderResourceDiff("Profiles", result.Profiles, summary, verbose))
	}
	if !result.Scripts.IsEmpty() {
		lines = append(lines, renderResourceDiff("Scripts", result.Scripts, summary, verbose))
	}

	for _, e := range result.Errors {
		display := e
		isInfo := strings.HasPrefix(e, "info: ")
		if isInfo {
			display = strings.TrimPrefix(e, "info: ")
		}
		lines = append(lines, yellow.Render("  * ")+display)
		if !isInfo && !strings.Contains(e, "no API diff available") {
			summary.Errors++
		}
	}

	return strings.Join(lines, "\n")
}

func renderConfigChanges(changes []diff.ConfigChange, summary *DiffSummary, verbose bool) string {
	var lines []string
	lines = append(lines, bold.Render("  Config:"))

	for _, c := range changes {
		if c.Old == "" {
			summary.Added++
			lines = append(lines, green.Render("    + ")+fmt.Sprintf("%s.%s", c.Section, c.Key))
			val := fmt.Sprintf("= %q", c.New)
			if !verbose {
				val = truncateToFit(val, maxLineWidth-len(fieldIndent))
			}
			lines = append(lines, fieldIndent+dim.Render(val))
		} else {
			summary.Modified++
			lines = append(lines, yellow.Render("    ~ ")+fmt.Sprintf("%s.%s", c.Section, c.Key))
			if verbose {
				lines = append(lines, fieldIndent+dim.Render(fmt.Sprintf("%q ", c.Old))+yellow.Render("→")+dim.Render(fmt.Sprintf(" %q", c.New)))
			} else {
				old := truncateToFit(fmt.Sprintf("%q", c.Old), 30)
				nw := truncateToFit(fmt.Sprintf("%q", c.New), 30)
				lines = append(lines, fieldIndent+dim.Render(old+" ")+yellow.Render("→")+dim.Render(" "+nw))
			}
		}
	}

	return strings.Join(lines, "\n")
}

func renderResourceDiff(name string, rd diff.ResourceDiff, summary *DiffSummary, verbose bool) string {
	var lines []string
	lines = append(lines, bold.Render("  "+name+":"))

	summary.Added += len(rd.Added)
	summary.Modified += len(rd.Modified)
	summary.Deleted += len(rd.Deleted)

	lines = append(lines, renderChangeList(rd.Added, "added", green, verbose)...)
	lines = append(lines, renderChangeList(rd.Modified, "modified", yellow, verbose)...)
	lines = append(lines, renderChangeList(rd.Deleted, "deleted", red, verbose)...)

	return strings.Join(lines, "\n")
}

// renderChangeList renders resource changes with per-field indented lines.
//
// Modified items: name on first line, each changed field indented below.
// Added items: name only (default) or name + proposed fields (verbose).
// Deleted items: name + host count + warning.
//
// Default mode truncates values to fit 80-char lines and caps at 3 fields.
// Verbose mode shows all fields with full values.
func renderChangeList(items []diff.ResourceChange, changeType string, color lipgloss.Style, verbose bool) []string {
	if len(items) == 0 {
		return nil
	}

	prefix := map[string]string{"added": "    + ", "modified": "    ~ ", "deleted": "    - "}[changeType]

	var lines []string
	for _, c := range items {
		switch changeType {
		case "added":
			line := color.Render(prefix + c.Name)
			if c.HostCount > 0 {
				line += dim.Render(fmt.Sprintf(" (~%d hosts)", c.HostCount))
			}
			lines = append(lines, line)
			if verbose {
				lines = append(lines, renderFieldLines(c.Fields, verbose, false)...)
			}

		case "modified":
			line := color.Render(prefix + c.Name)
			if len(c.Fields) == 0 && c.Warning != "" {
				line += dim.Render(" (" + c.Warning + ")")
			}
			lines = append(lines, line)
			lines = append(lines, renderFieldLines(c.Fields, verbose, true)...)

		case "deleted":
			line := color.Render(prefix + c.Name)
			if c.HostCount > 0 {
				line += dim.Render(fmt.Sprintf(" (~%d hosts)", c.HostCount))
			}
			lines = append(lines, line)
			if c.Warning != "" {
				lines = append(lines, "      "+red.Render("! "+c.Warning))
			}
		}
	}

	return lines
}

// renderFieldLines renders field diffs as indented lines under a resource name.
// showOld controls whether old→new format is used (true for modified) or just new value (false for added).
func renderFieldLines(fields map[string]diff.FieldDiff, verbose bool, showOld bool) []string {
	if len(fields) == 0 {
		return nil
	}

	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)

	limit := len(names)
	if !verbose && limit > maxFields {
		limit = maxFields
	}

	var lines []string
	for i := 0; i < limit; i++ {
		name := names[i]
		fd := fields[name]

		if showOld {
			if verbose {
				lines = append(lines, fieldIndent+dim.Render(name+": ")+
					dim.Render(fmt.Sprintf("%q ", fd.Old))+yellow.Render("→")+
					dim.Render(fmt.Sprintf(" %q", fd.New)))
			} else {
				avail := maxLineWidth - len(fieldIndent) - len(name) - len(": ") - len(" → ")
				half := avail / 2
				if half < 8 {
					half = 8
				}
				oldSnip, newSnip := diffContext(fd.Old, fd.New, half)
				lines = append(lines, fieldIndent+dim.Render(name+": ")+
					dim.Render(fmt.Sprintf("%q", oldSnip)+" ")+yellow.Render("→")+dim.Render(" "+fmt.Sprintf("%q", newSnip)))
			}
		} else {
			if verbose {
				lines = append(lines, fieldIndent+dim.Render(name+": "+fmt.Sprintf("%q", fd.New)))
			} else {
				avail := maxLineWidth - len(fieldIndent) - len(name) - len(": ")
				val := truncateToFit(fmt.Sprintf("%q", fd.New), avail)
				lines = append(lines, fieldIndent+dim.Render(name+": "+val))
			}
		}
	}

	if !verbose && len(names) > maxFields {
		remaining := len(names) - maxFields
		lines = append(lines, fieldIndent+dim.Render(fmt.Sprintf("... and %d more fields", remaining)))
	}

	return lines
}

// truncateToFit shortens a string to maxLen, appending "..." if truncated.
func truncateToFit(s string, maxLen int) string {
	if maxLen < 4 {
		maxLen = 4
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// diffContext extracts a short window around the first point where old and new
// diverge. Returns (oldSnippet, newSnippet) each at most maxLen characters,
// centered on the first difference. If the strings are short enough already,
// returns them unchanged.
func diffContext(old, new string, maxLen int) (string, string) {
	if maxLen < 8 {
		maxLen = 8
	}
	if len(old) <= maxLen && len(new) <= maxLen {
		return old, new
	}

	// Find first differing byte
	minLen := len(old)
	if len(new) < minLen {
		minLen = len(new)
	}
	diffAt := 0
	for diffAt < minLen && old[diffAt] == new[diffAt] {
		diffAt++
	}

	// Window: start a bit before the diff point so there's context
	contextBefore := maxLen / 4
	if contextBefore < 4 {
		contextBefore = 4
	}
	start := diffAt - contextBefore
	if start < 0 {
		start = 0
	}

	extract := func(s string) string {
		if len(s) <= maxLen {
			return s
		}
		end := start + maxLen
		if end > len(s) {
			end = len(s)
		}
		chunk := s[start:end]
		prefix := ""
		suffix := ""
		if start > 0 {
			prefix = "..."
		}
		if end < len(s) {
			suffix = "..."
		}
		// Trim chunk to fit with ellipses
		avail := maxLen - len(prefix) - len(suffix)
		if avail < 4 {
			avail = 4
		}
		if len(chunk) > avail {
			chunk = chunk[:avail]
			if end < len(s) {
				suffix = "..."
			}
		}
		return prefix + chunk + suffix
	}

	return extract(old), extract(new)
}

func renderLabels(results []diff.DiffResult) string {
	type labelInfo struct {
		name      string
		hostCount uint
	}
	type missingInfo struct {
		name         string
		referencedBy string
	}

	validSeen := make(map[string]labelInfo)
	missingSeen := make(map[string]missingInfo)

	for _, result := range results {
		for _, l := range result.Labels.Valid {
			if _, ok := validSeen[l.Name]; !ok {
				validSeen[l.Name] = labelInfo{name: l.Name, hostCount: l.HostCount}
			}
		}
		for _, l := range result.Labels.Missing {
			if _, ok := missingSeen[l.Name]; !ok {
				missingSeen[l.Name] = missingInfo{name: l.Name, referencedBy: l.ReferencedBy}
			}
		}
	}

	if len(validSeen) == 0 && len(missingSeen) == 0 {
		return ""
	}

	// Sort valid labels by host count (descending)
	validList := make([]labelInfo, 0, len(validSeen))
	for _, info := range validSeen {
		validList = append(validList, info)
	}
	sort.Slice(validList, func(i, j int) bool {
		return validList[i].hostCount > validList[j].hostCount
	})

	// Sort missing labels alphabetically
	missingList := make([]missingInfo, 0, len(missingSeen))
	for _, info := range missingSeen {
		missingList = append(missingList, info)
	}
	sort.Slice(missingList, func(i, j int) bool {
		return missingList[i].name < missingList[j].name
	})

	var sb strings.Builder
	sb.WriteString(bold.Render("Labels referenced:") + "\n")

	// Missing labels first (errors are more important)
	for _, info := range missingList {
		line := fmt.Sprintf("  - %q (NOT FOUND) referenced by %s", info.name, info.referencedBy)
		sb.WriteString(red.Render(line) + "\n")
	}

	// Valid labels (informational, not a change)
	for _, info := range validList {
		line := fmt.Sprintf("  * %q (%d hosts)", info.name, info.hostCount)
		sb.WriteString(dim.Render(line) + "\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

func renderSummaryBar(summary DiffSummary) string {
	parts := []string{}
	if summary.Added > 0 {
		parts = append(parts, green.Render(fmt.Sprintf("%d added", summary.Added)))
	}
	if summary.Modified > 0 {
		parts = append(parts, yellow.Render(fmt.Sprintf("%d modified", summary.Modified)))
	}
	if summary.Deleted > 0 {
		parts = append(parts, red.Render(fmt.Sprintf("%d deleted", summary.Deleted)))
	}
	if summary.Labels.Missing > 0 {
		parts = append(parts, red.Render(fmt.Sprintf("%d label errors", summary.Labels.Missing)))
	}
	if summary.Errors > 0 {
		parts = append(parts, red.Render(fmt.Sprintf("%d errors", summary.Errors)))
	}

	line := "Summary: "
	if len(parts) == 0 {
		line += dim.Render("no changes")
	} else {
		line += strings.Join(parts, ", ")
	}

	var sb strings.Builder
	sb.WriteString(dim.Render(strings.Repeat("-", 80)) + "\n")
	sb.WriteString(line + "\n")
	sb.WriteString(dim.Render("NOTE: Unexpected changes? Rebase, or confirm that changes have been deployed to Fleet."))

	return sb.String()
}

// stripANSI removes ANSI escape sequences for length measurement.
func stripANSI(s string) string {
	var out strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\033' {
			inEsc = true
			continue
		}
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
