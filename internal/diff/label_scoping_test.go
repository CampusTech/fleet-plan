package diff

import (
	"testing"

	"github.com/CampusTech/fleet-plan/internal/api"
	"github.com/CampusTech/fleet-plan/internal/parser"
)

// Shape of fleet-gitops PR 135: a new global label, and labels_exclude_any
// added to an existing policy and an existing profile. fleet-plan reported
// "No resource changes" for all three.

func TestDiffPolicyLabelScoping(t *testing.T) {
	tests := []struct {
		name      string
		cur       api.Policy
		prop      parser.ParsedPolicy
		wantField string // "" means no change expected
	}{
		{
			name:      "exclude added",
			cur:       api.Policy{Name: "P", Query: "SELECT 1;"},
			prop:      parser.ParsedPolicy{Name: "P", Query: "SELECT 1;", LabelsExcludeAny: []string{"exempt"}},
			wantField: "labels_exclude_any",
		},
		{
			name:      "include removed",
			cur:       api.Policy{Name: "P", Query: "SELECT 1;", LabelsIncludeAny: []string{"pilots"}},
			prop:      parser.ParsedPolicy{Name: "P", Query: "SELECT 1;"},
			wantField: "labels_include_any",
		},
		{
			name: "same labels, different order",
			cur:  api.Policy{Name: "P", Query: "SELECT 1;", LabelsExcludeAny: []string{"a", "b"}},
			prop: parser.ParsedPolicy{Name: "P", Query: "SELECT 1;", LabelsExcludeAny: []string{"b", "a"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rd := diffPolicies([]api.Policy{tt.cur}, []parser.ParsedPolicy{tt.prop})
			if tt.wantField == "" {
				if !rd.IsEmpty() {
					t.Fatalf("expected no change, got %+v", rd)
				}
				return
			}
			if len(rd.Modified) != 1 {
				t.Fatalf("expected 1 modified policy, got %+v", rd)
			}
			if _, ok := rd.Modified[0].Fields[tt.wantField]; !ok {
				t.Errorf("expected field %q, got %v", tt.wantField, rd.Modified[0].Fields)
			}
		})
	}
}

func TestDiffProfileLabelScoping(t *testing.T) {
	tests := []struct {
		name      string
		cur       api.Profile
		prop      parser.ParsedProfile
		wantField string
	}{
		{
			name:      "exclude added",
			cur:       api.Profile{Name: "DOR", Platform: "darwin"},
			prop:      parser.ParsedProfile{Name: "DOR", Platform: "darwin", Path: "/r/lib/dor.mobileconfig", LabelsExcludeAny: []string{"exempt"}},
			wantField: "labels_exclude_any",
		},
		{
			name:      "include_all changed",
			cur:       api.Profile{Name: "DOR", Platform: "darwin", LabelsIncludeAll: []string{"a"}},
			prop:      parser.ParsedProfile{Name: "DOR", Platform: "darwin", Path: "/r/lib/dor.mobileconfig", LabelsIncludeAll: []string{"b"}},
			wantField: "labels_include_all",
		},
		{
			name: "unchanged",
			cur:  api.Profile{Name: "DOR", Platform: "darwin", LabelsIncludeAny: []string{"x"}},
			prop: parser.ParsedProfile{Name: "DOR", Platform: "darwin", Path: "/r/lib/dor.mobileconfig", LabelsIncludeAny: []string{"x"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rd, _ := diffProfiles([]api.Profile{tt.cur}, []parser.ParsedProfile{tt.prop}, nil, nil)
			if tt.wantField == "" {
				if !rd.IsEmpty() {
					t.Fatalf("expected no change, got %+v", rd)
				}
				return
			}
			if len(rd.Modified) != 1 {
				t.Fatalf("expected 1 modified profile, got %+v", rd)
			}
			if _, ok := rd.Modified[0].Fields[tt.wantField]; !ok {
				t.Errorf("expected field %q, got %v", tt.wantField, rd.Modified[0].Fields)
			}
		})
	}
}

func TestDiffGlobalLabelDefinitions(t *testing.T) {
	current := &api.FleetState{
		Config: map[string]any{},
		Labels: []api.Label{
			{ID: 1, Name: "kept", Description: "same", LabelMembershipType: "manual"},
			{ID: 2, Name: "edited", Description: "old", Query: "SELECT 1;", LabelMembershipType: "dynamic"},
			{ID: 3, Name: "gone", LabelMembershipType: "manual"},
			{ID: 4, Name: "macOS", LabelMembershipType: "dynamic", LabelType: "builtin"},
		},
	}
	proposed := &parser.ParsedRepo{
		Global: &parser.ParsedGlobal{},
		Labels: []parser.ParsedLabel{
			{Name: "kept", Description: "same", LabelMembershipType: "manual"},
			{Name: "edited", Description: "new", Query: "SELECT 1;", LabelMembershipType: "dynamic"},
			{Name: "ddm-os-reminder-exempt", Description: "d", LabelMembershipType: "manual"},
		},
	}

	global := findTeam(t, Diff(current, proposed, nil, nil), "(global)")
	lc := global.LabelChanges

	if len(lc.Added) != 1 || lc.Added[0].Name != "ddm-os-reminder-exempt" {
		t.Errorf("added: want [ddm-os-reminder-exempt], got %+v", lc.Added)
	}
	if len(lc.Modified) != 1 || lc.Modified[0].Name != "edited" {
		t.Fatalf("modified: want [edited], got %+v", lc.Modified)
	}
	if _, ok := lc.Modified[0].Fields["description"]; !ok {
		t.Errorf("edited: want description field, got %v", lc.Modified[0].Fields)
	}
	// Built-in labels are never in the repo and must not show as deleted.
	if len(lc.Deleted) != 1 || lc.Deleted[0].Name != "gone" {
		t.Errorf("deleted: want [gone], got %+v", lc.Deleted)
	}
}

// A profile whose content and label scoping both change must keep the content
// summary: renderers only show Warning when Fields is empty.
func TestDiffProfileContentAndLabelChange(t *testing.T) {
	cur := api.Profile{Name: "DOR", Content: `<plist><dict><key>a</key><string>1</string></dict></plist>`}
	prop := parser.ParsedProfile{
		Name: "DOR", Path: "/r/dor.mobileconfig", LabelsExcludeAny: []string{"exempt"},
		Content: `<plist><dict><key>a</key><string>2</string></dict></plist>`,
	}
	rd, _ := diffProfiles([]api.Profile{cur}, []parser.ParsedProfile{prop}, nil, nil)
	if len(rd.Modified) != 1 {
		t.Fatalf("expected 1 modified profile, got %+v", rd)
	}
	f := rd.Modified[0].Fields
	if _, ok := f["labels_exclude_any"]; !ok {
		t.Errorf("missing labels_exclude_any: %v", f)
	}
	if c, ok := f["content"]; !ok || c.New == "" {
		t.Errorf("content summary lost: %v", f)
	}
}

// Removing the last label from default.yml must still report the deletion
// when the base branch managed labels.
func TestDiffGlobalLabelsLastRemoved(t *testing.T) {
	current := &api.FleetState{
		Config: map[string]any{},
		Labels: []api.Label{{ID: 1, Name: "exempt", LabelMembershipType: "manual", HostCount: 1}},
	}
	proposed := &parser.ParsedRepo{Global: &parser.ParsedGlobal{}}
	baseline := &parser.ParsedRepo{
		Global: &parser.ParsedGlobal{},
		Labels: []parser.ParsedLabel{{Name: "exempt", LabelMembershipType: "manual"}},
	}

	global := findTeam(t, Diff(current, proposed, nil, nil, WithBaseline(baseline)), "(global)")
	if d := global.LabelChanges.Deleted; len(d) != 1 || d[0].Name != "exempt" {
		t.Errorf("deleted: want [exempt], got %+v", d)
	}
}
