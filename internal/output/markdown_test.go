package output

import (
	"strings"
	"testing"

	"github.com/TsekNet/fleet-plan/internal/diff"
)

func assertOutputContains(t *testing.T, out string, substrings []string) {
	t.Helper()
	for _, s := range substrings {
		if !strings.Contains(out, s) {
			t.Errorf("expected %q in output, got:\n%s", s, out)
		}
	}
}

func assertOutputExcludes(t *testing.T, out string, substrings []string) {
	t.Helper()
	for _, s := range substrings {
		if strings.Contains(out, s) {
			t.Errorf("did not expect %q in output, got:\n%s", s, out)
		}
	}
}

func TestRenderDiffMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		results  []diff.DiffResult
		opts     MarkdownOptions
		wantAll  []string
		wantNone []string
	}{
		{
			name:     "empty results shows no changes",
			results:  nil,
			wantAll:  []string{"## fleet-plan", "No changes detected"},
			wantNone: []string{"Change | Team"},
		},
		{
			name: "added policy in table",
			results: []diff.DiffResult{{
				Team: "Workstations",
				Policies: diff.ResourceDiff{
					Added: []diff.ResourceChange{{Name: "[macOS] FileVault"}},
				},
			}},
			wantAll: []string{
				"| Change | Team | Type | Resource | Details |",
				"| ADDED | Workstations | Policy | **[macOS] FileVault** |",
				"**1 added**",
			},
			wantNone: []string{"### Workstations", "**Policies:**"},
		},
		{
			name: "added with host count",
			results: []diff.DiffResult{{
				Team: "Servers",
				Policies: diff.ResourceDiff{
					Added: []diff.ResourceChange{{Name: "Test", HostCount: 50}},
				},
			}},
			wantAll: []string{"~50 hosts"},
		},
		{
			name: "modified query with fields uses table and arrow",
			results: []diff.DiffResult{{
				Team: "Endpoints",
				Queries: diff.ResourceDiff{
					Modified: []diff.ResourceChange{{
						Name:   "Disk Usage",
						Fields: map[string]diff.FieldDiff{"interval": {Old: "3600", New: "7200"}},
					}},
				},
			}},
			wantAll: []string{
				"| MODIFIED | Endpoints | Query | **Disk Usage** |",
				"`interval`",
				"`3600` → `7200`",
				"**1 modified**",
			},
			wantNone: []string{"### Endpoints", "**Queries:**"},
		},
		{
			name: "multiple fields joined with br",
			results: []diff.DiffResult{{
				Team: "T",
				Policies: diff.ResourceDiff{
					Modified: []diff.ResourceChange{{
						Name: "P",
						Fields: map[string]diff.FieldDiff{
							"critical":    {Old: "false", New: "true"},
							"description": {Old: "old desc", New: "new desc"},
						},
					}},
				},
			}},
			wantAll:  []string{"<ul>", "<li>`critical`", "<li>`description`", "</ul>"},
			wantNone: []string{"<br>"},
		},
		{
			name: "backticks in field values use double backtick spans",
			results: []diff.DiffResult{{
				Team: "T",
				Queries: diff.ResourceDiff{
					Modified: []diff.ResourceChange{{
						Name:   "Q",
						Fields: map[string]diff.FieldDiff{"query": {Old: "SELECT `col` FROM t", New: "SELECT `col` FROM t2"}},
					}},
				},
			}},
			wantAll:  []string{"`` SELECT `col` FROM t ``", "`` SELECT `col` FROM t2 ``"},
			wantNone: []string{"\\`"},
		},
		{
			name: "empty field values render as italic empty",
			results: []diff.DiffResult{{
				Team: "T",
				Software: diff.ResourceDiff{
					Modified: []diff.ResourceChange{{
						Name:   "S",
						Fields: map[string]diff.FieldDiff{"hash": {Old: "abc123", New: ""}},
					}},
				},
			}},
			wantAll: []string{"`hash`: `abc123` → _(empty)_"},
		},
		{
			name: "deleted with warning",
			results: []diff.DiffResult{{
				Team: "Workstations",
				Software: diff.ResourceDiff{
					Deleted: []diff.ResourceChange{{Name: "OldApp", Warning: "in use by 10 hosts"}},
				},
			}},
			wantAll: []string{
				"| REMOVED | Workstations | Software | **OldApp** |",
				"⚠️ in use by 10 hosts",
				"**1 deleted**",
			},
		},
		{
			name: "errors rendered in table",
			results: []diff.DiffResult{{
				Team:   "BadTeam",
				Errors: []string{"parse error in team YAML"},
			}},
			wantAll:  []string{"⚠️", "BadTeam", "parse error in team YAML"},
			wantNone: []string{"**WARNING**"},
		},
		{
			name: "labels as separate table with host counts",
			results: []diff.DiffResult{{
				Team: "Workstations",
				Policies: diff.ResourceDiff{
					Added: []diff.ResourceChange{{Name: "P"}},
				},
				Labels: diff.LabelValidation{
					Valid:   []diff.LabelRef{{Name: "x86-64", HostCount: 100}},
					Missing: []diff.LabelRef{{Name: "Ghost Label", ReferencedBy: "policy: Test"}},
				},
			}},
			wantAll: []string{
				"| Affected Labels | Hosts |",
				"| `x86-64` | 100 |",
				"`Ghost Label` **NOT FOUND**",
			},
			wantNone: []string{"### Labels", "🏷️", "🚫"},
		},
		{
			name: "labels without host counts omit Hosts column",
			results: []diff.DiffResult{{
				Team: "T",
				Policies: diff.ResourceDiff{
					Added: []diff.ResourceChange{{Name: "P"}},
				},
				Labels: diff.LabelValidation{
					Valid: []diff.LabelRef{{Name: "label-a"}, {Name: "label-b"}},
				},
			}},
			wantAll:  []string{"| Affected Labels |", "| `label-a` |", "| `label-b` |"},
			wantNone: []string{"Hosts"},
		},
		{
			name: "multiple teams aggregated summary",
			results: []diff.DiffResult{
				{
					Team:     "TeamA",
					Policies: diff.ResourceDiff{Added: []diff.ResourceChange{{Name: "P1"}, {Name: "P2"}}},
				},
				{
					Team:    "TeamB",
					Queries: diff.ResourceDiff{Deleted: []diff.ResourceChange{{Name: "Q1"}}},
				},
			},
			wantAll: []string{
				"| ADDED | TeamA |",
				"| REMOVED | TeamB |",
				"**2 added, 1 deleted**",
			},
		},
		{
			name: "all resource types in single table",
			results: []diff.DiffResult{{
				Team:     "Full",
				Policies: diff.ResourceDiff{Added: []diff.ResourceChange{{Name: "P"}}},
				Queries:  diff.ResourceDiff{Modified: []diff.ResourceChange{{Name: "Q", Fields: map[string]diff.FieldDiff{"f": {Old: "a", New: "b"}}}}},
				Software: diff.ResourceDiff{Deleted: []diff.ResourceChange{{Name: "S"}}},
				Profiles: diff.ResourceDiff{Added: []diff.ResourceChange{{Name: "Prof"}}},
			}},
			wantAll: []string{
				"| ADDED | Full | Policy | **P** |",
				"| MODIFIED | Full | Query | **Q** |",
				"| REMOVED | Full | Software | **S** |",
				"| ADDED | Full | Profile | **Prof** |",
			},
			wantNone: []string{"**Policies:**", "**Queries:**", "### Full"},
		},
		{
			name: "config changes in table",
			results: []diff.DiffResult{{
				Team: "(global)",
				Config: []diff.ConfigChange{
					{Section: "org_settings", Key: "enable_host_users", Old: "true", New: "false"},
					{Section: "org_settings", Key: "new_key", New: "value"},
				},
			}},
			wantAll: []string{
				"| MODIFIED | Global | Config | **org_settings.enable_host_users** |",
				"`true` → `false`",
				"| ADDED | Global | Config | **org_settings.new_key** |",
			},
		},
		{
			name:    "ci-heading as heading",
			results: []diff.DiffResult{{
				Team:     "T",
				Policies: diff.ResourceDiff{Added: []diff.ResourceChange{{Name: "P"}}},
			}},
			opts:    MarkdownOptions{Heading: "Planned changes for fleet.example.com"},
			wantAll: []string{"## Planned changes for fleet.example.com", "**1 added**"},
		},
		{
			name:    "ci-marker appended",
			results: []diff.DiffResult{{
				Team:     "T",
				Policies: diff.ResourceDiff{Added: []diff.ResourceChange{{Name: "P"}}},
			}},
			opts:    MarkdownOptions{Marker: "fleet-plan-marker"},
			wantAll: []string{"<!-- fleet-plan-marker -->"},
		},
		{
			name:    "ci-marker on no changes",
			results: nil,
			opts:    MarkdownOptions{Marker: "fleet-plan-marker"},
			wantAll: []string{"No changes detected", "<!-- fleet-plan-marker -->"},
		},
		{
			name:    "ci-heading on no changes",
			results: nil,
			opts:    MarkdownOptions{Heading: "Planned changes for fleet.example.com"},
			wantAll: []string{"## Planned changes for fleet.example.com", "No changes detected"},
		},
		{
			name: "host count formatting with commas",
			results: []diff.DiffResult{{
				Team: "T",
				Policies: diff.ResourceDiff{
					Added: []diff.ResourceChange{{Name: "P"}},
				},
				Labels: diff.LabelValidation{
					Valid: []diff.LabelRef{{Name: "big-label", HostCount: 26782}},
				},
			}},
			wantAll: []string{"26,782"},
		},
		{
			name: "pipes and newlines escaped in table cells",
			results: []diff.DiffResult{{
				Team: "T",
				Software: diff.ResourceDiff{
					Modified: []diff.ResourceChange{{
						Name: "fleet app cursor/windows",
						Fields: map[string]diff.FieldDiff{
							"install_script": {
								Old: "$exit_code = Invoke-Binary $env:INSTALLER_PATH\nexit $exit_code",
								New: "try { $result = Invoke-AsLoggedInUser | Out-Null }\nexit 0",
							},
						},
					}},
				},
			}},
			wantAll: []string{`\|`, "Invoke-AsLoggedInUser"},
		},
		{
			name: "long field values truncated",
			results: []diff.DiffResult{{
				Team: "T",
				Queries: diff.ResourceDiff{
					Modified: []diff.ResourceChange{{
						Name: "Q",
						Fields: map[string]diff.FieldDiff{
							"query": {
								Old: "SELECT 1 FROM programs WHERE name = 'Google Chrome' AND version_compare(version, '135.0.7049.85') >= 0;",
								New: "SELECT 1 FROM programs WHERE name = 'Google Chrome' AND version_compare(version, '135.0.7049.95') >= 0;",
							},
						},
					}},
				},
			}},
			wantAll:  []string{"..."},
			wantNone: []string{"SELECT 1 FROM programs"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := RenderDiffMarkdown(tt.results, tt.opts)
			assertOutputContains(t, out, tt.wantAll)
			assertOutputExcludes(t, out, tt.wantNone)
		})
	}
}

func TestRenderDiffMarkdownCategories(t *testing.T) {
	tests := []struct {
		name    string
		results []diff.DiffResult
		wantAll []string
	}{
		{
			name: "added software with categories in Details not shown (added only shows host count)",
			results: []diff.DiffResult{{
				Team: "T",
				Software: diff.ResourceDiff{
					Added: []diff.ResourceChange{{
						Name: "fleet app chrome/mac",
						Fields: map[string]diff.FieldDiff{
							"slug":       {New: "chrome/mac"},
							"categories": {New: "[Browsers]", IsSlice: true, NewSlice: []string{"Browsers"}},
						},
					}},
				},
			}},
			wantAll: []string{"ADDED", "fleet app chrome/mac"},
		},
		{
			name: "modified software with categories change",
			results: []diff.DiffResult{{
				Team: "T",
				Software: diff.ResourceDiff{
					Modified: []diff.ResourceChange{{
						Name: "software/slack.yml",
						Fields: map[string]diff.FieldDiff{
							"categories": {
								Old: "[Communication]", New: "[Communication, Productivity]",
								IsSlice: true,
							},
						},
					}},
				},
			}},
			wantAll: []string{"MODIFIED", "software/slack.yml", "`categories`", "[Communication]", "[Communication, Productivity]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := RenderDiffMarkdown(tt.results, MarkdownOptions{})
			assertOutputContains(t, out, tt.wantAll)
		})
	}
}

func TestMdDiffContext(t *testing.T) {
	tests := []struct {
		name       string
		old, new   string
		maxLen     int
		wantOldSub string
		wantNewSub string
		wantEllip  bool
	}{
		{
			name: "short values unchanged",
			old: "3600", new: "7200", maxLen: 60,
			wantOldSub: "3600", wantNewSub: "7200",
		},
		{
			name:       "long values truncated with ellipsis",
			old:        "SELECT 1 FROM programs WHERE name = 'Google Chrome' AND version_compare(version, '135.0.7049.85') >= 0;",
			new:        "SELECT 1 FROM programs WHERE name = 'Google Chrome' AND version_compare(version, '135.0.7049.95') >= 0;",
			maxLen:     60,
			wantOldSub: "...",
			wantNewSub: "...",
			wantEllip:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOld, gotNew := mdDiffContext(tt.old, tt.new, tt.maxLen)
			if !strings.Contains(gotOld, tt.wantOldSub) {
				t.Errorf("old: expected substring %q in %q", tt.wantOldSub, gotOld)
			}
			if !strings.Contains(gotNew, tt.wantNewSub) {
				t.Errorf("new: expected substring %q in %q", tt.wantNewSub, gotNew)
			}
			if tt.wantEllip {
				if len(gotOld) > tt.maxLen+6 || len(gotNew) > tt.maxLen+6 {
					t.Errorf("truncated values too long: old=%d new=%d (max %d)", len(gotOld), len(gotNew), tt.maxLen)
				}
			}
		})
	}
}

func TestFormatHostCount(t *testing.T) {
	tests := []struct {
		n    uint
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{26782, "26,782"},
		{1000000, "1,000,000"},
	}
	for _, tt := range tests {
		if got := formatHostCount(tt.n); got != tt.want {
			t.Errorf("formatHostCount(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestHasChanges(t *testing.T) {
	tests := []struct {
		name    string
		results []diff.DiffResult
		want    bool
	}{
		{name: "nil", results: nil, want: false},
		{name: "empty team", results: []diff.DiffResult{{Team: "T"}}, want: false},
		{name: "added policy", results: []diff.DiffResult{{
			Team:     "T",
			Policies: diff.ResourceDiff{Added: []diff.ResourceChange{{Name: "P"}}},
		}}, want: true},
		{name: "config change", results: []diff.DiffResult{{
			Team:   "(global)",
			Config: []diff.ConfigChange{{Section: "org_settings", Key: "k", New: "v"}},
		}}, want: true},
		{name: "errors only", results: []diff.DiffResult{{
			Team:   "T",
			Errors: []string{"something"},
		}}, want: true},
		{name: "missing labels", results: []diff.DiffResult{{
			Team:   "T",
			Labels: diff.LabelValidation{Missing: []diff.LabelRef{{Name: "x"}}},
		}}, want: true},
		{name: "valid labels only is not a change", results: []diff.DiffResult{{
			Team:   "T",
			Labels: diff.LabelValidation{Valid: []diff.LabelRef{{Name: "x", HostCount: 5}}},
		}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasChanges(tt.results); got != tt.want {
				t.Errorf("HasChanges() = %v, want %v", got, tt.want)
			}
		})
	}
}
