package output

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/TsekNet/fleet-plan/internal/diff"
)

func TestRenderDiffJSON(t *testing.T) {
	tests := []struct {
		name    string
		results []diff.DiffResult
		check   func(t *testing.T, output JSONDiffOutput)
	}{
		{
			name:    "empty results",
			results: nil,
			check: func(t *testing.T, output JSONDiffOutput) {
				if len(output.Teams) != 0 {
					t.Errorf("expected 0 teams, got %d", len(output.Teams))
				}
			},
		},
		{
			name: "single team with added policy",
			results: []diff.DiffResult{{
				Team: "Workstations",
				Policies: diff.ResourceDiff{
					Added: []diff.ResourceChange{{Name: "[macOS] FileVault"}},
				},
			}},
			check: func(t *testing.T, output JSONDiffOutput) {
				if len(output.Teams) != 1 {
					t.Fatalf("expected 1 team, got %d", len(output.Teams))
				}
				team := output.Teams[0]
				if team.Team != "Workstations" {
					t.Errorf("team name: got %q", team.Team)
				}
				if len(team.Policies.Added) != 1 {
					t.Fatalf("expected 1 added policy, got %d", len(team.Policies.Added))
				}
				if team.Policies.Added[0].Name != "[macOS] FileVault" {
					t.Errorf("policy name: got %q", team.Policies.Added[0].Name)
				}
			},
		},
		{
			name: "modified with fields",
			results: []diff.DiffResult{{
				Team: "Servers",
				Queries: diff.ResourceDiff{
					Modified: []diff.ResourceChange{{
						Name:   "Disk Usage",
						Fields: map[string]diff.FieldDiff{"interval": {Old: "3600", New: "7200"}},
					}},
				},
			}},
			check: func(t *testing.T, output JSONDiffOutput) {
				mod := output.Teams[0].Queries.Modified
				if len(mod) != 1 {
					t.Fatalf("expected 1 modified, got %d", len(mod))
				}
				f, ok := mod[0].Fields["interval"]
				if !ok {
					t.Fatal("expected 'interval' field")
				}
				if f.Old != "3600" || f.New != "7200" {
					t.Errorf("field values: old=%q new=%q", f.Old, f.New)
				}
			},
		},
		{
			name: "deleted with warning and host count",
			results: []diff.DiffResult{{
				Team: "Workstations",
				Policies: diff.ResourceDiff{
					Deleted: []diff.ResourceChange{{
						Name:      "Old Policy",
						HostCount: 42,
						Warning:   "affects 42 hosts",
					}},
				},
			}},
			check: func(t *testing.T, output JSONDiffOutput) {
				del := output.Teams[0].Policies.Deleted
				if len(del) != 1 {
					t.Fatalf("expected 1 deleted, got %d", len(del))
				}
				if del[0].HostCount != 42 {
					t.Errorf("host count: got %d", del[0].HostCount)
				}
				if del[0].Warning != "affects 42 hosts" {
					t.Errorf("warning: got %q", del[0].Warning)
				}
			},
		},
		{
			name: "labels valid and missing",
			results: []diff.DiffResult{{
				Team: "Endpoints",
				Labels: diff.LabelValidation{
					Valid:   []diff.LabelRef{{Name: "x86-64", HostCount: 100}},
					Missing: []diff.LabelRef{{Name: "Ghost", ReferencedBy: "policy: Test"}},
				},
			}},
			check: func(t *testing.T, output JSONDiffOutput) {
				labels := output.Teams[0].Labels
				if len(labels.Valid) != 1 {
					t.Fatalf("expected 1 valid label, got %d", len(labels.Valid))
				}
				if labels.Valid[0].HostCount != 100 {
					t.Errorf("host count: got %d", labels.Valid[0].HostCount)
				}
				if len(labels.Missing) != 1 {
					t.Fatalf("expected 1 missing label, got %d", len(labels.Missing))
				}
				if labels.Missing[0].ReferencedBy != "policy: Test" {
					t.Errorf("referenced by: got %q", labels.Missing[0].ReferencedBy)
				}
			},
		},
		{
			name: "errors array never null",
			results: []diff.DiffResult{{
				Team: "Clean",
			}},
			check: func(t *testing.T, output JSONDiffOutput) {
				if output.Teams[0].Errors == nil {
					t.Error("errors should be empty array, not null")
				}
			},
		},
		{
			name: "errors preserved",
			results: []diff.DiffResult{{
				Team:   "BadTeam",
				Errors: []string{"parse error", "missing field"},
			}},
			check: func(t *testing.T, output JSONDiffOutput) {
				if len(output.Teams[0].Errors) != 2 {
					t.Fatalf("expected 2 errors, got %d", len(output.Teams[0].Errors))
				}
			},
		},
		{
			name: "all resource types present",
			results: []diff.DiffResult{{
				Team:     "Full",
				Policies: diff.ResourceDiff{Added: []diff.ResourceChange{{Name: "P"}}},
				Queries:  diff.ResourceDiff{Modified: []diff.ResourceChange{{Name: "Q"}}},
				Software: diff.ResourceDiff{Deleted: []diff.ResourceChange{{Name: "S"}}},
				Profiles: diff.ResourceDiff{Added: []diff.ResourceChange{{Name: "Prof"}}},
			}},
			check: func(t *testing.T, output JSONDiffOutput) {
				team := output.Teams[0]
				if len(team.Policies.Added) != 1 {
					t.Error("expected 1 added policy")
				}
				if len(team.Queries.Modified) != 1 {
					t.Error("expected 1 modified query")
				}
				if len(team.Software.Deleted) != 1 {
					t.Error("expected 1 deleted software")
				}
				if len(team.Profiles.Added) != 1 {
					t.Error("expected 1 added profile")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := RenderDiffJSON(tt.results)
			if err != nil {
				t.Fatalf("RenderDiffJSON: %v", err)
			}

			var output JSONDiffOutput
			if err := json.Unmarshal([]byte(raw), &output); err != nil {
				t.Fatalf("invalid JSON: %v\n%s", err, raw)
			}

			tt.check(t, output)
		})
	}
}

func TestRenderDiffJSONIsValidJSON(t *testing.T) {
	results := []diff.DiffResult{{
		Team: "Team with \"quotes\" and <brackets>",
		Policies: diff.ResourceDiff{
			Added: []diff.ResourceChange{{Name: "Policy with\nnewline"}},
		},
		Errors: []string{"error with \"quotes\""},
	}}

	raw, err := RenderDiffJSON(results)
	if err != nil {
		t.Fatalf("RenderDiffJSON: %v", err)
	}

	if !json.Valid([]byte(raw)) {
		t.Fatalf("output is not valid JSON:\n%s", raw)
	}
}

func TestRenderDiffJSONCategories(t *testing.T) {
	tests := []struct {
		name  string
		field diff.FieldDiff
		check func(t *testing.T, raw string)
	}{
		{
			name: "added categories renders as null old and array new",
			field: diff.FieldDiff{
				New: "[Communication]", IsSlice: true,
				NewSlice: []string{"Communication"},
			},
			check: func(t *testing.T, raw string) {
				if !strings.Contains(raw, `"old": null`) {
					t.Errorf("expected null old, got:\n%s", raw)
				}
				if !strings.Contains(raw, `"Communication"`) {
					t.Errorf("expected Communication in new, got:\n%s", raw)
				}
			},
		},
		{
			name: "modified categories renders arrays for both",
			field: diff.FieldDiff{
				Old: "[Communication]", New: "[Communication, Productivity]", IsSlice: true,
				OldSlice: []string{"Communication"}, NewSlice: []string{"Communication", "Productivity"},
			},
			check: func(t *testing.T, raw string) {
				// Parse as generic JSON to check structure
				var output JSONDiffOutput
				if err := json.Unmarshal([]byte(raw), &output); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
				f := output.Teams[0].Software.Added[0].Fields["categories"]
				// Old should be an array
				if f.Old == nil {
					t.Error("expected non-nil old")
				}
				if f.New == nil {
					t.Error("expected non-nil new")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := []diff.DiffResult{{
				Team: "T",
				Software: diff.ResourceDiff{
					Added: []diff.ResourceChange{{
						Name:   "app",
						Fields: map[string]diff.FieldDiff{"categories": tt.field},
					}},
				},
			}}
			raw, err := RenderDiffJSON(results)
			if err != nil {
				t.Fatalf("RenderDiffJSON: %v", err)
			}
			if !json.Valid([]byte(raw)) {
				t.Fatalf("invalid JSON:\n%s", raw)
			}
			tt.check(t, raw)
		})
	}
}

func TestRenderDiffJSONStringFieldsUnchanged(t *testing.T) {
	// Verify that regular string fields still render as strings, not arrays.
	results := []diff.DiffResult{{
		Team: "T",
		Queries: diff.ResourceDiff{
			Modified: []diff.ResourceChange{{
				Name:   "Q",
				Fields: map[string]diff.FieldDiff{"interval": {Old: "3600", New: "7200"}},
			}},
		},
	}}
	raw, err := RenderDiffJSON(results)
	if err != nil {
		t.Fatalf("RenderDiffJSON: %v", err)
	}
	var output JSONDiffOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	f := output.Teams[0].Queries.Modified[0].Fields["interval"]
	// String fields should unmarshal as strings via any
	oldStr, ok := f.Old.(string)
	if !ok {
		t.Errorf("expected string old, got %T", f.Old)
	}
	if oldStr != "3600" {
		t.Errorf("old = %q, want 3600", oldStr)
	}
}

func TestRenderDiffJSONSpecialCharsRoundtrip(t *testing.T) {
	results := []diff.DiffResult{{
		Team: "Team with \"quotes\" and <brackets>",
		Policies: diff.ResourceDiff{
			Added: []diff.ResourceChange{{Name: "Policy with\nnewline"}},
		},
		Errors: []string{"error with \"quotes\""},
	}}

	raw, err := RenderDiffJSON(results)
	if err != nil {
		t.Fatalf("RenderDiffJSON: %v", err)
	}

	if !strings.Contains(raw, "quotes") {
		t.Error("expected quotes in output")
	}
}
