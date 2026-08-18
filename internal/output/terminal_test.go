package output

import (
	"fmt"
	"strings"
	"testing"

	"github.com/TsekNet/fleet-plan/internal/diff"
)

// ---------- RenderDiffTerminal ----------

func TestRenderDiffTerminal(t *testing.T) {
	tests := []struct {
		name     string
		verbose  bool
		results  []diff.DiffResult
		wantAll  []string
		wantNone []string
	}{
		{
			name:    "empty results shows no changes",
			verbose: false,
			results: nil,
			wantAll: []string{"no changes"},
		},
		{
			name:    "added policies show name only in default mode",
			verbose: false,
			results: []diff.DiffResult{{
				Team: "Workstations",
				Policies: diff.ResourceDiff{
					Added: []diff.ResourceChange{
						{Name: "[macOS] FileVault Enabled", Fields: map[string]diff.FieldDiff{"query": {New: "SELECT 1"}, "platform": {New: "darwin"}}},
						{Name: "[Windows] BitLocker Enabled", Fields: map[string]diff.FieldDiff{"query": {New: "SELECT 1"}, "platform": {New: "windows"}}},
					},
				},
			}},
			wantAll: []string{
				"Team: Workstations",
				"Policies:",
				"[macOS] FileVault Enabled",
				"[Windows] BitLocker Enabled",
				"2 added",
			},
			wantNone: []string{"SELECT 1"},
		},
		{
			name:    "added policies show fields in verbose mode",
			verbose: true,
			results: []diff.DiffResult{{
				Team: "Workstations",
				Policies: diff.ResourceDiff{
					Added: []diff.ResourceChange{
						{Name: "[macOS] FileVault Enabled", Fields: map[string]diff.FieldDiff{
							"query":    {New: "SELECT 1 FROM disk_encryption"},
							"platform": {New: "darwin"},
						}},
					},
				},
			}},
			wantAll: []string{
				"[macOS] FileVault Enabled",
				"platform:",
				"darwin",
				"query:",
				"SELECT 1 FROM disk_encryption",
			},
		},
		{
			name:    "modified queries show field diffs on indented lines",
			verbose: true,
			results: []diff.DiffResult{{
				Team: "Servers",
				Queries: diff.ResourceDiff{
					Modified: []diff.ResourceChange{{
						Name: "Disk Usage",
						Fields: map[string]diff.FieldDiff{
							"interval": {Old: "3600", New: "7200"},
						},
					}},
				},
			}},
			wantAll: []string{
				"Team: Servers", "Queries:", "Disk Usage",
				"interval:", "3600", "7200", "1 modified",
			},
		},
		{
			name:    "modified queries truncated in default mode",
			verbose: false,
			results: []diff.DiffResult{{
				Team: "Servers",
				Queries: diff.ResourceDiff{
					Modified: []diff.ResourceChange{{
						Name: "Disk Usage",
						Fields: map[string]diff.FieldDiff{
							"interval": {Old: "3600", New: "7200"},
						},
					}},
				},
			}},
			wantAll: []string{
				"Disk Usage", "interval:", "3600", "7200",
			},
		},
		{
			name:    "deleted with host count and warning",
			verbose: false,
			results: []diff.DiffResult{{
				Team: "Workstations",
				Policies: diff.ResourceDiff{
					Deleted: []diff.ResourceChange{{
						Name: "[Windows] Legacy AV Check", HostCount: 42,
						Warning: "will delete policy affecting 42 hosts",
					}},
				},
			}},
			wantAll: []string{"[Windows] Legacy AV Check", "42 hosts", "1 deleted", "will delete policy"},
		},
		{
			name:    "info errors not counted as errors",
			verbose: false,
			results: []diff.DiffResult{{
				Team:   "NewTeam",
				Errors: []string{"info: team \"NewTeam\" does not exist in Fleet yet (will be created)"},
			}},
			wantAll:  []string{"NewTeam", "does not exist"},
			wantNone: []string{"1 errors"},
		},
		{
			name:    "real errors counted in summary",
			verbose: false,
			results: []diff.DiffResult{{
				Team: "BadTeam", Errors: []string{"failed to parse team YAML"},
			}},
			wantAll: []string{"1 errors"},
		},
		{
			name:    "missing labels counted in summary",
			verbose: false,
			results: []diff.DiffResult{{
				Team: "Workstations",
				Labels: diff.LabelValidation{
					Missing: []diff.LabelRef{{Name: "Nonexistent Label", ReferencedBy: "policy: Test"}},
				},
			}},
			wantAll: []string{"1 label errors", "NOT FOUND"},
		},
		{
			name:    "multiple resource types in one team",
			verbose: false,
			results: []diff.DiffResult{{
				Team:     "Endpoints",
				Policies: diff.ResourceDiff{Added: []diff.ResourceChange{{Name: "Policy A"}}},
				Queries:  diff.ResourceDiff{Modified: []diff.ResourceChange{{Name: "Query B", Fields: map[string]diff.FieldDiff{"interval": {Old: "60", New: "120"}}}}},
				Software: diff.ResourceDiff{Deleted: []diff.ResourceChange{{Name: "OldApp"}}},
				Profiles: diff.ResourceDiff{Added: []diff.ResourceChange{{Name: "WiFi Profile"}}},
			}},
			wantAll: []string{
				"Policies:", "Queries:", "Software:", "Profiles:",
				"2 added", "1 modified", "1 deleted",
			},
		},
		{
			name:    "multiple teams",
			verbose: false,
			results: []diff.DiffResult{
				{Team: "TeamA", Policies: diff.ResourceDiff{Added: []diff.ResourceChange{{Name: "P1"}}}},
				{Team: "TeamB", Queries: diff.ResourceDiff{Deleted: []diff.ResourceChange{{Name: "Q1"}}}},
			},
			wantAll: []string{"Team: TeamA", "Team: TeamB", "1 added", "1 deleted"},
		},
		{
			name:    "labels shown without unique count footer",
			verbose: false,
			results: []diff.DiffResult{{
				Team: "Workstations",
				Labels: diff.LabelValidation{
					Valid: func() []diff.LabelRef {
						var refs []diff.LabelRef
						for i := 1; i <= 10; i++ {
							refs = append(refs, diff.LabelRef{Name: fmt.Sprintf("label-%d", i), HostCount: uint(i)})
						}
						return refs
					}(),
				},
			}},
			wantAll:  []string{"Labels referenced:", "label-1"},
			wantNone: []string{"unique labels"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := RenderDiffTerminal(tt.results, tt.verbose)
			plain := stripANSI(out)

			for _, want := range tt.wantAll {
				if !strings.Contains(plain, want) {
					t.Errorf("expected %q in output, got:\n%s", want, plain)
				}
			}
			for _, notWant := range tt.wantNone {
				if strings.Contains(plain, notWant) {
					t.Errorf("did not expect %q in output, got:\n%s", notWant, plain)
				}
			}
		})
	}
}

func TestRenderDiffTerminalCategories(t *testing.T) {
	// Added fields only show in verbose mode; modified fields always show.
	results := []diff.DiffResult{{
		Team: "T",
		Software: diff.ResourceDiff{
			Added: []diff.ResourceChange{{
				Name: "fleet app chrome/mac",
				Fields: map[string]diff.FieldDiff{
					"slug":         {New: "chrome/mac"},
					"self_service": {New: "true"},
					"categories":   {New: "[Browsers]", IsSlice: true, NewSlice: []string{"Browsers"}},
				},
			}},
			Modified: []diff.ResourceChange{{
				Name: "software/slack.yml",
				Fields: map[string]diff.FieldDiff{
					"categories": {Old: "[Communication]", New: "[Communication, Productivity]", IsSlice: true},
				},
			}},
		},
	}}

	out := RenderDiffTerminal(results, true)
	plain := stripANSI(out)

	for _, want := range []string{"categories:", "[Browsers]", "[Communication]", "[Communication, Productivity]"} {
		if !strings.Contains(plain, want) {
			t.Errorf("expected %q in verbose output, got:\n%s", want, plain)
		}
	}

	// In default mode, modified categories should still show (may be truncated)
	outDefault := RenderDiffTerminal(results, false)
	plainDefault := stripANSI(outDefault)
	for _, want := range []string{"categories:", "[Communication]", "Productivity]"} {
		if !strings.Contains(plainDefault, want) {
			t.Errorf("expected %q in default output, got:\n%s", want, plainDefault)
		}
	}
}

// ---------- renderFieldLines ----------

func TestRenderFieldLines(t *testing.T) {
	tests := []struct {
		name     string
		fields   map[string]diff.FieldDiff
		verbose  bool
		showOld  bool
		wantAll  []string
		wantNone []string
	}{
		{
			name:   "nil fields returns nil",
			fields: nil,
		},
		{
			name:    "modified field verbose shows old and new",
			fields:  map[string]diff.FieldDiff{"interval": {Old: "3600", New: "7200"}},
			verbose: true, showOld: true,
			wantAll: []string{"interval:", "3600", "7200"},
		},
		{
			name:    "modified field default truncates long values",
			fields:  map[string]diff.FieldDiff{"query": {Old: strings.Repeat("A", 100), New: strings.Repeat("B", 100)}},
			verbose: false, showOld: true,
			wantAll:  []string{"query:", "..."},
			wantNone: []string{strings.Repeat("A", 100)},
		},
		{
			name:    "added field verbose shows new value",
			fields:  map[string]diff.FieldDiff{"platform": {New: "darwin"}},
			verbose: true, showOld: false,
			wantAll: []string{"platform:", "darwin"},
		},
		{
			name: "caps at 3 fields in default mode",
			fields: map[string]diff.FieldDiff{
				"a_field": {Old: "1", New: "2"},
				"b_field": {Old: "3", New: "4"},
				"c_field": {Old: "5", New: "6"},
				"d_field": {Old: "7", New: "8"},
				"e_field": {Old: "9", New: "10"},
			},
			verbose: false, showOld: true,
			wantAll:  []string{"a_field:", "b_field:", "c_field:", "... and 2 more fields"},
			wantNone: []string{"d_field:", "e_field:"},
		},
		{
			name: "shows all fields in verbose mode",
			fields: map[string]diff.FieldDiff{
				"a_field": {Old: "1", New: "2"},
				"b_field": {Old: "3", New: "4"},
				"c_field": {Old: "5", New: "6"},
				"d_field": {Old: "7", New: "8"},
				"e_field": {Old: "9", New: "10"},
			},
			verbose: true, showOld: true,
			wantAll: []string{"a_field:", "b_field:", "c_field:", "d_field:", "e_field:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := renderFieldLines(tt.fields, tt.verbose, tt.showOld)
			if tt.fields == nil {
				if lines != nil {
					t.Fatalf("expected nil for nil fields, got %v", lines)
				}
				return
			}
			joined := stripANSI(strings.Join(lines, "\n"))
			for _, want := range tt.wantAll {
				if !strings.Contains(joined, want) {
					t.Errorf("expected %q in output, got:\n%s", want, joined)
				}
			}
			for _, notWant := range tt.wantNone {
				if strings.Contains(joined, notWant) {
					t.Errorf("did not expect %q in output, got:\n%s", notWant, joined)
				}
			}
		})
	}
}

// ---------- renderChangeList ----------

func TestRenderChangeList(t *testing.T) {
	tests := []struct {
		name       string
		items      []diff.ResourceChange
		changeType string
		verbose    bool
		wantAll    []string
		wantNone   []string
	}{
		{name: "empty items returns nil", items: nil, changeType: "added"},
		{name: "added items show + prefix", items: []diff.ResourceChange{{Name: "TestItem"}}, changeType: "added", wantAll: []string{"+", "TestItem"}},
		{name: "deleted items show - prefix", items: []diff.ResourceChange{{Name: "OldItem"}}, changeType: "deleted", wantAll: []string{"-", "OldItem"}},
		{
			name:       "modified items show ~ prefix and field lines",
			items:      []diff.ResourceChange{{Name: "ChangedItem", Fields: map[string]diff.FieldDiff{"key": {Old: "a", New: "b"}}}},
			changeType: "modified", verbose: true,
			wantAll: []string{"~", "ChangedItem", "key:", "\"a\"", "\"b\""},
		},
		{
			name:       "modified items default mode shows truncated fields",
			items:      []diff.ResourceChange{{Name: "ChangedItem", Fields: map[string]diff.FieldDiff{"key": {Old: "a", New: "b"}}}},
			changeType: "modified", verbose: false,
			wantAll: []string{"~", "ChangedItem", "key:"},
		},
		{name: "deleted with host count", items: []diff.ResourceChange{{Name: "CriticalPolicy", HostCount: 500}}, changeType: "deleted", wantAll: []string{"CriticalPolicy", "500 hosts"}},
		{name: "deleted with warning", items: []diff.ResourceChange{{Name: "DangerPolicy", Warning: "affects production hosts"}}, changeType: "deleted", wantAll: []string{"DangerPolicy", "affects production hosts"}},
		{
			name:       "added verbose shows fields",
			items:      []diff.ResourceChange{{Name: "NewPolicy", Fields: map[string]diff.FieldDiff{"query": {New: "SELECT 1"}, "platform": {New: "darwin"}}}},
			changeType: "added", verbose: true,
			wantAll: []string{"+", "NewPolicy", "platform:", "darwin", "query:", "SELECT 1"},
		},
		{
			name:       "added default hides fields",
			items:      []diff.ResourceChange{{Name: "NewPolicy", Fields: map[string]diff.FieldDiff{"query": {New: "SELECT 1"}}}},
			changeType: "added", verbose: false,
			wantAll:  []string{"+", "NewPolicy"},
			wantNone: []string{"SELECT 1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := renderChangeList(tt.items, tt.changeType, green, tt.verbose)
			if tt.items == nil {
				if lines != nil {
					t.Fatalf("expected nil for empty items, got %v", lines)
				}
				return
			}
			joined := stripANSI(strings.Join(lines, "\n"))
			for _, want := range tt.wantAll {
				if !strings.Contains(joined, want) {
					t.Errorf("expected %q in output, got:\n%s", want, joined)
				}
			}
			for _, notWant := range tt.wantNone {
				if strings.Contains(joined, notWant) {
					t.Errorf("did not expect %q in output, got:\n%s", notWant, joined)
				}
			}
		})
	}
}

// ---------- truncateToFit ----------

func TestTruncateToFit(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{name: "short string unchanged", input: "hello", maxLen: 10, want: "hello"},
		{name: "exact length unchanged", input: "hello", maxLen: 5, want: "hello"},
		{name: "truncated with ellipsis", input: "hello world", maxLen: 8, want: "hello..."},
		{name: "very small maxLen floors at 4", input: "hello", maxLen: 2, want: "h..."},
		{name: "empty string", input: "", maxLen: 10, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateToFit(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateToFit(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

// ---------- diffContext ----------

func TestDiffContext(t *testing.T) {
	tests := []struct {
		name            string
		old, new        string
		maxLen          int
		wantOld, wantNew string
	}{
		{
			name: "short strings returned as-is",
			old: "hello", new: "world", maxLen: 20,
			wantOld: "hello", wantNew: "world",
		},
		{
			name:   "long strings with diff at end",
			old:    "SELECT 1 FROM apps WHERE version >= '15.2'",
			new:    "SELECT 1 FROM apps WHERE version >= '15.3'",
			maxLen: 20,
		},
		{
			name:   "long strings with diff in middle",
			old:    "aaaaaaaaaa DIFFERENT_OLD bbbbbbbbbbb",
			new:    "aaaaaaaaaa DIFFERENT_NEW bbbbbbbbbbb",
			maxLen: 20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOld, gotNew := diffContext(tt.old, tt.new, tt.maxLen)

			if tt.wantOld != "" && gotOld != tt.wantOld {
				t.Errorf("old: got %q, want %q", gotOld, tt.wantOld)
			}
			if tt.wantNew != "" && gotNew != tt.wantNew {
				t.Errorf("new: got %q, want %q", gotNew, tt.wantNew)
			}

			// For long-string cases, just verify the snippets contain the differing part
			if tt.wantOld == "" {
				if gotOld == tt.old {
					t.Errorf("expected old to be truncated, got full string")
				}
				if gotNew == tt.new {
					t.Errorf("expected new to be truncated, got full string")
				}
				if gotOld == gotNew {
					t.Errorf("snippets should differ: old=%q new=%q", gotOld, gotNew)
				}
			}
		})
	}
}

// ---------- renderLabels ----------

func TestRenderLabelsShowsAll(t *testing.T) {
	var refs []diff.LabelRef
	for i := 1; i <= 12; i++ {
		refs = append(refs, diff.LabelRef{Name: fmt.Sprintf("label-%02d", i), HostCount: uint(i)})
	}

	out := renderLabels([]diff.DiffResult{{
		Team:   "Workstations",
		Labels: diff.LabelValidation{Valid: refs},
	}})

	plain := stripANSI(out)
	if !strings.Contains(plain, "Labels referenced:") {
		t.Fatalf("expected labels header, got:\n%s", plain)
	}
	if strings.Contains(plain, "unique labels") {
		t.Fatalf("should not contain unique labels footer, got:\n%s", plain)
	}
	if !strings.Contains(plain, "\"label-12\"") || !strings.Contains(plain, "\"label-01\"") {
		t.Fatalf("expected all labels in output, got:\n%s", plain)
	}
}

// ---------- stripANSI ----------

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{name: "no ansi", input: "hello", want: "hello"},
		{name: "bold", input: "\033[1mhello\033[0m", want: "hello"},
		{name: "color", input: "\033[32mgreen\033[0m text", want: "green text"},
		{name: "empty", input: "", want: ""},
		{name: "nested", input: "\033[1m\033[32mbold green\033[0m\033[0m", want: "bold green"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripANSI(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------- renderSummaryBar ----------

func TestRenderSummaryBar(t *testing.T) {
	tests := []struct {
		name    string
		summary DiffSummary
		wantAll []string
	}{
		{name: "no changes", summary: DiffSummary{}, wantAll: []string{"no changes"}},
		{name: "all change types", summary: DiffSummary{Added: 3, Modified: 2, Deleted: 1}, wantAll: []string{"3 added", "2 modified", "1 deleted"}},
		{
			name: "label errors",
			summary: func() DiffSummary {
				s := DiffSummary{}
				s.Labels.Missing = 2
				return s
			}(),
			wantAll: []string{"2 label errors"},
		},
		{name: "errors count", summary: DiffSummary{Errors: 1}, wantAll: []string{"1 errors"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderSummaryBar(tt.summary)
			plain := stripANSI(out)
			for _, want := range tt.wantAll {
				if !strings.Contains(plain, want) {
					t.Errorf("expected %q in output, got:\n%s", want, plain)
				}
			}
		})
	}
}
