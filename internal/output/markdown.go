package output

import (
	"fmt"
	"sort"
	"strings"

	"github.com/CampusTech/fleet-plan/internal/diff"
)

// MarkdownOptions controls optional CI-oriented additions to markdown output.
type MarkdownOptions struct {
	Heading string // ## heading text (e.g. "Planned changes for fleet.example.com")
	Marker  string // HTML comment appended for idempotent MR note updates
	JobURL  string // CI pipeline/job URL embedded before the marker
}

// HasChanges returns true if any DiffResult contains additions, modifications,
// deletions, config changes, errors, or missing labels.
func HasChanges(results []diff.DiffResult) bool {
	for _, r := range results {
		if !r.Policies.IsEmpty() || !r.Queries.IsEmpty() ||
			!r.Software.IsEmpty() || !r.Profiles.IsEmpty() ||
			!r.Scripts.IsEmpty() ||
			len(r.Config) > 0 || len(r.Errors) > 0 || len(r.Labels.Missing) > 0 {
			return true
		}
	}
	return false
}

func writeMarker(sb *strings.Builder, opts MarkdownOptions) {
	if opts.JobURL != "" {
		sb.WriteString(fmt.Sprintf("[View pipeline job](%s)\n", opts.JobURL))
	}
	if opts.Marker != "" {
		sb.WriteString(fmt.Sprintf("\n<!-- %s -->\n", opts.Marker))
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

const mdMaxFieldLen = 60

var permissionErrors = map[string]string{
	"software diff skipped: API token lacks permission to read software titles": "software",
	"profiles diff skipped: API token lacks permission to read profiles":        "profiles",
}

// RenderDiffMarkdown renders diff results as a single-table markdown comment.
func RenderDiffMarkdown(results []diff.DiffResult, opts MarkdownOptions) string {
	var sb strings.Builder

	heading := "fleet-plan"
	if opts.Heading != "" {
		heading = opts.Heading
	}
	sb.WriteString("## " + heading + "\n\n")

	if !HasChanges(results) {
		sb.WriteString("No changes detected. Your branch matches the current Fleet state.\n")
		writeMarker(&sb, opts)
		return sb.String()
	}

	type row struct {
		change, team, kind, resource, details string
	}
	var rows []row
	var errRows []string
	totalAdded, totalModified, totalDeleted := 0, 0, 0

	for _, result := range results {
		team := result.Team
		if team == "(global)" {
			team = "Global"
		}

		for _, c := range result.Config {
			if c.Old == "" {
				rows = append(rows, row{"ADDED", team, "Config", c.Section + "." + c.Key, mdCodeSpan(c.New)})
				totalAdded++
			} else {
				rows = append(rows, row{"MODIFIED", team, "Config", c.Section + "." + c.Key, fmt.Sprintf("%s → %s", mdCodeSpan(c.Old), mdCodeSpan(c.New))})
				totalModified++
			}
		}

		types := []struct {
			name string
			rd   diff.ResourceDiff
		}{
			{"Policy", result.Policies},
			{"Query", result.Queries},
			{"Software", result.Software},
			{"Profile", result.Profiles},
		{"Script", result.Scripts},
		}

		for _, rt := range types {
			for _, c := range rt.rd.Added {
				det := ""
				if c.HostCount > 0 {
					det = fmt.Sprintf("~%d hosts", c.HostCount)
				}
				rows = append(rows, row{"ADDED", team, rt.name, c.Name, det})
				totalAdded++
			}
			for _, c := range rt.rd.Modified {
				det := mdFieldDetails(c.Fields)
				if det == "" && c.Warning != "" {
					det = c.Warning
				}
				rows = append(rows, row{"MODIFIED", team, rt.name, c.Name, det})
				totalModified++
			}
			for _, c := range rt.rd.Deleted {
				det := ""
				if c.Warning != "" {
					det = "⚠️ " + c.Warning
				}
				rows = append(rows, row{"REMOVED", team, rt.name, c.Name, det})
				totalDeleted++
			}
		}

		for _, e := range result.Errors {
			if _, ok := permissionErrors[e]; ok {
				continue
			}
			errRows = append(errRows, fmt.Sprintf("| ⚠️ | %s | | | %s |", team, e))
		}
	}

	sb.WriteString("| Change | Team | Type | Resource | Details |\n")
	sb.WriteString("|---|---|---|---|---|\n")
	for _, r := range rows {
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | **%s** | %s |\n",
			r.change, r.team, r.kind, mdEscapeTableCell(r.resource), r.details))
	}
	for _, e := range errRows {
		sb.WriteString(e + "\n")
	}
	sb.WriteString("\n")

	labelContent := renderLabelsTable(results)
	if labelContent != "" {
		sb.WriteString(labelContent)
		sb.WriteString("\n")
	}

	sb.WriteString("---\n")
	sb.WriteString(mdSummaryLine(totalAdded, totalModified, totalDeleted))
	sb.WriteString("\n")

	if warning := buildPermissionWarning(results); warning != "" {
		sb.WriteString(fmt.Sprintf("\n⚠️ %s\n", warning))
	}

	sb.WriteString("\n> **NOTE:** Unexpected changes? Rebase, or confirm that changes have been deployed to Fleet.\n")

	writeMarker(&sb, opts)

	return sb.String()
}

func mdCodeSpan(s string) string {
	if s == "" {
		return "_(empty)_"
	}
	s = mdEscapeTableCell(s)
	if !strings.Contains(s, "`") {
		return "`" + s + "`"
	}
	return "`` " + s + " ``"
}

// mdEscapeTableCell escapes characters that break markdown table cells.
func mdEscapeTableCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}

func mdFieldDetails(fields map[string]diff.FieldDiff) string {
	if len(fields) == 0 {
		return ""
	}
	names := sortedKeys(fields)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		fd := fields[name]
		if fd.Old == "" && fd.New != "" {
			// Summary-only field (e.g., script diff with +N/-N format)
			parts = append(parts, fmt.Sprintf("`%s`: %s", name, mdCodeSpan(fd.New)))
		} else {
			old, new := mdDiffContext(fd.Old, fd.New, mdMaxFieldLen)
			parts = append(parts, fmt.Sprintf("`%s`: %s → %s", name, mdCodeSpan(old), mdCodeSpan(new)))
		}
	}
	if len(parts) == 1 {
		return parts[0]
	}
	var sb strings.Builder
	sb.WriteString("<ul>")
	for _, p := range parts {
		sb.WriteString("<li>")
		sb.WriteString(p)
		sb.WriteString("</li>")
	}
	sb.WriteString("</ul>")
	return sb.String()
}

// mdDiffContext truncates long old/new values, showing context around the diff.
func mdDiffContext(old, new string, maxLen int) (string, string) {
	if maxLen < 8 {
		maxLen = 8
	}
	if len(old) <= maxLen && len(new) <= maxLen {
		return old, new
	}

	minLen := len(old)
	if len(new) < minLen {
		minLen = len(new)
	}
	diffAt := 0
	for diffAt < minLen && old[diffAt] == new[diffAt] {
		diffAt++
	}

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
		prefix, suffix := "", ""
		if start > 0 {
			prefix = "..."
		}
		if end < len(s) {
			suffix = "..."
		}
		avail := maxLen - len(prefix) - len(suffix)
		if avail < 4 {
			avail = 4
		}
		if len(chunk) > avail {
			chunk = chunk[:avail]
		}
		return prefix + chunk + suffix
	}

	return extract(old), extract(new)
}

func mdSummaryLine(added, modified, deleted int) string {
	var parts []string
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", added))
	}
	if modified > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", modified))
	}
	if deleted > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", deleted))
	}
	if len(parts) == 0 {
		return "**No resource changes**"
	}
	return "**" + strings.Join(parts, ", ") + "**"
}

func buildPermissionWarning(results []diff.DiffResult) string {
	unavailable := make(map[string]bool)

	for _, r := range results {
		for _, e := range r.Errors {
			if resource, ok := permissionErrors[e]; ok {
				unavailable[resource] = true
			}
		}
		for _, s := range r.SkippedConfigSections {
			unavailable[s] = true
		}
	}

	hasLabels, hasLabelCounts := false, false
	for _, r := range results {
		for _, l := range r.Labels.Valid {
			hasLabels = true
			if l.HostCount > 0 {
				hasLabelCounts = true
			}
		}
	}
	if hasLabels && !hasLabelCounts {
		unavailable["label host counts"] = true
	}

	if len(unavailable) == 0 {
		return ""
	}

	resources := sortedKeys(unavailable)
	return fmt.Sprintf("Token lacks read access to: %s. Full-access token required for a complete diff.", strings.Join(resources, ", "))
}

func renderLabelsTable(results []diff.DiffResult) string {
	var validLabels []diff.LabelRef
	validSeen := make(map[string]bool)
	missingSeen := make(map[string]diff.LabelRef)
	anyNonZero := false

	for _, result := range results {
		for _, l := range result.Labels.Valid {
			if !validSeen[l.Name] {
				validSeen[l.Name] = true
				validLabels = append(validLabels, l)
				if l.HostCount > 0 {
					anyNonZero = true
				}
			}
		}
		for _, l := range result.Labels.Missing {
			if _, ok := missingSeen[l.Name]; !ok {
				missingSeen[l.Name] = l
			}
		}
	}

	if len(validLabels) == 0 && len(missingSeen) == 0 {
		return ""
	}

	var sb strings.Builder
	if anyNonZero {
		sb.WriteString("| Affected Labels | Hosts |\n")
		sb.WriteString("|---|--:|\n")
	} else {
		sb.WriteString("| Affected Labels |\n")
		sb.WriteString("|---|\n")
	}

	for _, l := range validLabels {
		if anyNonZero {
			sb.WriteString(fmt.Sprintf("| `%s` | %s |\n", l.Name, formatHostCount(l.HostCount)))
		} else {
			sb.WriteString(fmt.Sprintf("| `%s` |\n", l.Name))
		}
	}

	missingNames := sortedKeys(missingSeen)
	for _, name := range missingNames {
		l := missingSeen[name]
		if anyNonZero {
			sb.WriteString(fmt.Sprintf("| `%s` **NOT FOUND** (ref: %s) | — |\n", l.Name, l.ReferencedBy))
		} else {
			sb.WriteString(fmt.Sprintf("| `%s` **NOT FOUND** (ref: %s) |\n", l.Name, l.ReferencedBy))
		}
	}

	return sb.String()
}

func formatHostCount(n uint) string {
	if n == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}
