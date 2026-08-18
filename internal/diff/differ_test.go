package diff

import (
	"context"
	"strings"
	"testing"

	"github.com/CampusTech/fleet-plan/internal/api"
	"github.com/CampusTech/fleet-plan/internal/parser"
	"github.com/CampusTech/fleet-plan/internal/testutil"
)

// TestDiffTestdataAgainstMockAPI parses the shared testdata/ fixture and diffs
// it against a simulated current Fleet state. This is the integration test that
// exercises parser → differ together.
//
// Mock API state for Workstations:
//   - Policies: FileVault (different query → modified), Legacy AV (not in YAML → deleted, 100 hosts)
//   - Queries: Disk Usage (same), old "Network Interfaces" (not in YAML → deleted)
//   - Software: slack (old URL → modified), old-agent (not in YAML → deleted)
//
// Mock API state for Servers:
//   - Queries: Uptime (interval changed → modified)
//   - No policies (SSH Root Login is new → added)
//
// Labels: macOS 14+ and Windows 11 exist. Ubuntu 24.04 does NOT → missing label error.
func TestDiffTestdataAgainstMockAPI(t *testing.T) {
	root := testutil.TestdataRoot(t)

	proposed, err := parser.ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	if len(proposed.Errors) > 0 {
		t.Fatalf("unexpected parse errors: %v", proposed.Errors)
	}

	current := &api.FleetState{
		Teams: []api.Team{
			{
				ID:   1,
				Name: "Workstations",
				// Live settings: host expiry off and the failing-policies
				// webhook disabled, both of which the fixture turns on.
				Settings: map[string]any{
					"host_expiry_settings": map[string]any{
						"host_expiry_enabled": false,
						"host_expiry_window":  float64(0),
					},
					"webhook_settings": map[string]any{
						"failing_policies_webhook": map[string]any{
							"enable_failing_policies_webhook": false,
							"host_batch_size":                 float64(0),
						},
					},
					"features": map[string]any{"enable_software_inventory": true},
				},
				Policies: []api.Policy{
					// FileVault exists but with a different (simpler) query → modified
					{Name: "[macOS] FileVault Enabled", Query: "SELECT 1 FROM disk_encryption WHERE encrypted = 1;", Platform: "darwin", Critical: true},
					// Legacy AV not in YAML → deleted, has hosts
					{Name: "[Windows] Legacy AV Check", Query: "SELECT 1;", Platform: "windows", PassingHostCount: 100},
				},
				Queries: []api.Query{
					{Name: "Disk Usage", Query: "SELECT path, type, blocks_size, blocks, blocks_free, blocks_available FROM mounts WHERE type != 'tmpfs';", Interval: 3600, Platform: "darwin,windows,linux"},
					// Network Interfaces not in YAML → deleted
					{Name: "Network Interfaces", Query: "SELECT * FROM interface_addresses;", Interval: 3600, Platform: "darwin,windows,linux"},
				},
				Software: api.TeamSoftware{
					Packages: []api.TeamSoftwarePackage{
						// Slack: old URL → modified
						{ReferencedYAMLPath: "software/mac/slack/slack.yml", URL: "https://downloads.slack-edge.com/desktop-releases/macos/4.41.47/Slack-4.41.47-macOS.dmg", HashSHA256: "abc123def456789"},
						// old-agent: not in YAML → deleted
						{ReferencedYAMLPath: "software/mac/old-agent/old-agent.yml", URL: "https://example.com/old-agent.pkg"},
					},
					FleetMaintained: []api.TeamFleetApp{
						{Slug: "cursor/windows", SelfService: true},
					},
				},
				// Profile uses PayloadDisplayName "fleet_orbit-allowfulldiskaccess"
				// which matches the content of the .mobileconfig file (not the filename).
				Profiles: []api.Profile{
					{Name: "fleet_orbit-allowfulldiskaccess"},
				},
				Scripts: []api.Script{
					{ID: 1, Name: "enable-desktop.ps1", TeamID: 1},
					{ID: 2, Name: "old-script.sh", TeamID: 1},
				},
			},
			{
				ID:   2,
				Name: "Servers",
				Queries: []api.Query{
					// Uptime exists but with different interval → modified
					{Name: "System Uptime", Query: "SELECT total_seconds, days, hours FROM uptime;", Interval: 3600, Platform: "darwin,windows,linux"},
				},
			},
		},
		Labels: []api.Label{
			{ID: 1, Name: "macOS 14+", HostCount: 8512},
			{ID: 2, Name: "Windows 11", HostCount: 3200},
			// Ubuntu 24.04 NOT present → missing label error
		},
	}

	// --- Test all teams (unfiltered) ---
	allResults := Diff(current, proposed, nil, nil)

	// Should have 3 results: (global), Workstations, Servers
	// The (global) result comes from default.yml parsing.
	if len(allResults) != 3 {
		t.Fatalf("expected 3 results, got %d", len(allResults))
	}

	// Verify global result exists and is first
	if allResults[0].Team != "(global)" {
		t.Errorf("first result should be (global), got %q", allResults[0].Team)
	}

	// --- Workstations ---
	ws := findTeam(t, allResults, "Workstations")

	// Policies: Defender, SSH, Firewall are new (3 added)
	if len(ws.Policies.Added) != 3 {
		t.Errorf("Workstations: expected 3 added policies, got %d: %v", len(ws.Policies.Added), ws.Policies.Added)
	}
	// FileVault modified (query changed)
	if len(ws.Policies.Modified) != 1 {
		t.Errorf("Workstations: expected 1 modified policy, got %d", len(ws.Policies.Modified))
	} else if ws.Policies.Modified[0].Name != "[macOS] FileVault Enabled" {
		t.Errorf("Workstations: modified policy name: got %q", ws.Policies.Modified[0].Name)
	}
	// Legacy AV deleted
	if len(ws.Policies.Deleted) != 1 {
		t.Fatalf("Workstations: expected 1 deleted policy, got %d", len(ws.Policies.Deleted))
	}
	if ws.Policies.Deleted[0].Name != "[Windows] Legacy AV Check" {
		t.Errorf("deleted policy: got %q", ws.Policies.Deleted[0].Name)
	}
	if ws.Policies.Deleted[0].HostCount != 100 {
		t.Errorf("deleted policy host count: got %d", ws.Policies.Deleted[0].HostCount)
	}
	if ws.Policies.Deleted[0].Warning == "" {
		t.Error("expected warning for deletion with hosts")
	}

	// Queries: Uptime + OS Version are new (2 added), Disk Usage unchanged, Network Interfaces deleted
	if len(ws.Queries.Added) != 2 {
		t.Errorf("Workstations: expected 2 added queries, got %d", len(ws.Queries.Added))
	}
	if len(ws.Queries.Deleted) != 1 {
		t.Errorf("Workstations: expected 1 deleted query, got %d", len(ws.Queries.Deleted))
	} else if ws.Queries.Deleted[0].Name != "Network Interfaces" {
		t.Errorf("Workstations: deleted query name: got %q", ws.Queries.Deleted[0].Name)
	}

	// Software: slack modified (URL + hash + categories changed), cursor modified (categories added),
	// example-app added, app store app 803453959 added, old-agent deleted
	if len(ws.Software.Modified) != 2 {
		t.Errorf("Workstations: expected 2 modified software, got %d", len(ws.Software.Modified))
	}
	if len(ws.Software.Added) != 2 {
		t.Errorf("Workstations: expected 2 added software, got %d", len(ws.Software.Added))
	}
	if len(ws.Software.Deleted) != 1 {
		t.Errorf("Workstations: expected 1 deleted software, got %d", len(ws.Software.Deleted))
	}

	// Verify categories appear in slack's modified fields
	for _, mod := range ws.Software.Modified {
		if strings.Contains(mod.Name, "slack") {
			if _, ok := mod.Fields["categories"]; !ok {
				t.Error("expected categories field in modified slack software")
			}
		}
	}

	// Profiles: fleet_orbit-allowfulldiskaccess matches by PayloadDisplayName → no diff
	if !ws.Profiles.IsEmpty() {
		t.Errorf("Workstations: expected no profile changes (name matches PayloadDisplayName), got added=%d modified=%d deleted=%d",
			len(ws.Profiles.Added), len(ws.Profiles.Modified), len(ws.Profiles.Deleted))
	}

	// Scripts: disk-cleanup.sh is new (added), enable-desktop.ps1 exists (no change), old-script.sh deleted
	if len(ws.Scripts.Added) != 1 {
		t.Errorf("Workstations: expected 1 added script, got %d", len(ws.Scripts.Added))
	} else if ws.Scripts.Added[0].Name != "disk-cleanup.sh" {
		t.Errorf("Workstations: added script name: got %q", ws.Scripts.Added[0].Name)
	}
	if len(ws.Scripts.Deleted) != 1 {
		t.Errorf("Workstations: expected 1 deleted script, got %d", len(ws.Scripts.Deleted))
	} else if ws.Scripts.Deleted[0].Name != "old-script.sh" {
		t.Errorf("Workstations: deleted script name: got %q", ws.Scripts.Deleted[0].Name)
	}
	if len(ws.Scripts.Modified) != 0 {
		t.Errorf("Workstations: expected 0 modified scripts, got %d", len(ws.Scripts.Modified))
	}

	// Labels: macOS 14+ valid, Ubuntu 24.04 missing
	if len(ws.Labels.Valid) == 0 {
		t.Error("expected at least 1 valid label reference")
	}
	if len(ws.Labels.Missing) != 1 {
		t.Errorf("expected 1 missing label, got %d", len(ws.Labels.Missing))
	} else if ws.Labels.Missing[0].Name != "Ubuntu 24.04" {
		t.Errorf("missing label: got %q", ws.Labels.Missing[0].Name)
	}

	// --- Servers ---
	srv := findTeam(t, allResults, "Servers")

	// SSH Root Login is new → added
	if len(srv.Policies.Added) != 1 {
		t.Errorf("Servers: expected 1 added policy, got %d", len(srv.Policies.Added))
	}
	// SSH Root Login references Ubuntu 24.04 which is not in mock API → missing
	if len(srv.Labels.Missing) != 1 {
		t.Errorf("Servers: expected 1 missing label, got %d", len(srv.Labels.Missing))
	} else if srv.Labels.Missing[0].Name != "Ubuntu 24.04" {
		t.Errorf("Servers: missing label: got %q", srv.Labels.Missing[0].Name)
	}
	// OS Version is new → added, Uptime modified (interval 3600→86400)
	if len(srv.Queries.Added) != 1 {
		t.Errorf("Servers: expected 1 added query, got %d", len(srv.Queries.Added))
	}
	if len(srv.Queries.Modified) != 1 {
		t.Errorf("Servers: expected 1 modified query, got %d", len(srv.Queries.Modified))
	} else {
		if _, ok := srv.Queries.Modified[0].Fields["interval"]; !ok {
			t.Error("expected interval field diff for Servers Uptime")
		}
	}
}

// TestDiffTestdataWorkstationsOnly verifies filtered diff for a single team.
func TestDiffTestdataWorkstationsOnly(t *testing.T) {
	root := testutil.TestdataRoot(t)

	proposed, err := parser.ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}

	current := &api.FleetState{
		Teams: []api.Team{
			{
				ID:   1,
				Name: "Workstations",
				Policies: []api.Policy{
					{Name: "[macOS] FileVault Enabled", Query: "SELECT 1 FROM disk_encryption WHERE encrypted = 1;", Platform: "darwin", Critical: true},
				},
			},
		},
		Labels: []api.Label{
			{ID: 1, Name: "macOS 14+", HostCount: 8512},
		},
	}

	results := Diff(current, proposed, []string{"Workstations"}, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Team != "Workstations" {
		t.Errorf("team: got %q", results[0].Team)
	}
}

// TestDiffPolicyScenarios consolidates policy add/modify/delete/no-change tests.
func TestDiffPolicyScenarios(t *testing.T) {
	tests := []struct {
		name         string
		current      []api.Policy
		proposed     []parser.ParsedPolicy
		wantAdded    int
		wantModified int
		wantDeleted  int
		checkName    string // name to verify in the first result of the expected bucket
		checkFields  []string
		checkWarning bool
	}{
		{
			name:      "new policy added",
			current:   []api.Policy{{Name: "Existing", Platform: "darwin"}},
			proposed:  []parser.ParsedPolicy{{Name: "Existing", Platform: "darwin"}, {Name: "New Policy", Platform: "windows"}},
			wantAdded: 1, checkName: "New Policy",
		},
		{
			name:         "policy modified (query + critical)",
			current:      []api.Policy{{Name: "Disk Encryption", Query: "SELECT 1 FROM old;", Platform: "darwin", Critical: false}},
			proposed:     []parser.ParsedPolicy{{Name: "Disk Encryption", Query: "SELECT 1 FROM new;", Platform: "darwin", Critical: true}},
			wantModified: 1, checkName: "Disk Encryption", checkFields: []string{"query", "critical"},
		},
		{
			name:        "policy deleted with hosts",
			current:     []api.Policy{{Name: "Keep", Platform: "darwin"}, {Name: "Delete This", Platform: "darwin", PassingHostCount: 50}},
			proposed:    []parser.ParsedPolicy{{Name: "Keep", Platform: "darwin"}},
			wantDeleted: 1, checkName: "Delete This", checkWarning: true,
		},
		{
			name:     "no changes",
			current:  []api.Policy{{Name: "P1", Query: "SELECT 1;", Platform: "darwin"}},
			proposed: []parser.ParsedPolicy{{Name: "P1", Query: "SELECT 1;", Platform: "darwin"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := &api.FleetState{Teams: []api.Team{{ID: 1, Name: "T", Policies: tt.current}}}
			proposed := &parser.ParsedRepo{Teams: []parser.ParsedTeam{{Name: "T", Policies: tt.proposed}}}

			results := Diff(current, proposed, nil, nil)
			r := results[0]

			if len(r.Policies.Added) != tt.wantAdded {
				t.Errorf("added: got %d, want %d", len(r.Policies.Added), tt.wantAdded)
			}
			if len(r.Policies.Modified) != tt.wantModified {
				t.Errorf("modified: got %d, want %d", len(r.Policies.Modified), tt.wantModified)
			}
			if len(r.Policies.Deleted) != tt.wantDeleted {
				t.Errorf("deleted: got %d, want %d", len(r.Policies.Deleted), tt.wantDeleted)
			}

			if tt.checkName != "" {
				var found *ResourceChange
				for _, bucket := range [][]ResourceChange{r.Policies.Added, r.Policies.Modified, r.Policies.Deleted} {
					for i := range bucket {
						if bucket[i].Name == tt.checkName {
							found = &bucket[i]
						}
					}
				}
				if found == nil {
					t.Fatalf("expected resource %q not found", tt.checkName)
				}
				for _, f := range tt.checkFields {
					if _, ok := found.Fields[f]; !ok {
						t.Errorf("expected field diff %q", f)
					}
				}
				if tt.checkWarning && found.Warning == "" {
					t.Error("expected warning on deleted resource")
				}
			}
		})
	}
}

func TestDiffNewTeam(t *testing.T) {
	current := &api.FleetState{Teams: []api.Team{}, Labels: []api.Label{}}
	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{{Name: "New Team", Policies: []parser.ParsedPolicy{{Name: "Test Policy"}}}},
	}

	results := Diff(current, proposed, nil, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Team != "New Team" {
		t.Errorf("team name: got %q", r.Team)
	}
	if len(r.Policies.Added) != 1 {
		t.Errorf("expected 1 added policy, got %d", len(r.Policies.Added))
	}

	foundInfo := false
	for _, e := range r.Errors {
		if strings.Contains(e, "does not exist in Fleet yet") {
			foundInfo = true
			if !strings.HasPrefix(e, "info:") {
				t.Errorf("new team message should be prefixed with 'info:', got %q", e)
			}
		}
	}
	if !foundInfo {
		t.Error("expected info message about new team")
	}
}

// The no-team bucket is absent from GET /teams by design, in both the teams/
// layout ("No team") and the fleets/ layout ("Unassigned"). Neither may be
// reported as a team that will be created, and neither may have its resources
// listed as additions -- but the plan must still say what's configured for it.
func TestDiffNoTeamIsNotANewTeam(t *testing.T) {
	tests := []struct {
		name        string
		team        parser.ParsedTeam
		wantSummary string
	}{
		{
			name: "teams layout",
			team: parser.ParsedTeam{
				Name:       "No team",
				SourceFile: "teams/no-team.yml",
				Policies:   []parser.ParsedPolicy{{Name: "P1"}},
			},
			wantSummary: "1 policy",
		},
		{
			name: "fleets layout",
			team: parser.ParsedTeam{
				Name:       "Unassigned",
				SourceFile: "fleets/unassigned.yml",
				Policies:   []parser.ParsedPolicy{{Name: "P1"}, {Name: "P2"}},
				Scripts:    []parser.ParsedScript{{Name: "a.sh"}, {Name: "b.ps1"}},
			},
			wantSummary: "2 policies, 2 scripts",
		},
		{
			name: "fleets layout, unrecognized name key",
			team: parser.ParsedTeam{
				Name:       "hosts with no team",
				SourceFile: "fleets/unassigned.yml",
				Queries:    []parser.ParsedQuery{{Name: "Q1"}},
			},
			wantSummary: "1 query",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := &api.FleetState{Teams: []api.Team{}, Labels: []api.Label{}}
			proposed := &parser.ParsedRepo{Teams: []parser.ParsedTeam{tt.team}}

			results := Diff(current, proposed, nil, nil)
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			r := results[0]

			if len(r.Policies.Added) != 0 || len(r.Queries.Added) != 0 {
				t.Errorf("no-team resources listed as additions: %d policies, %d queries",
					len(r.Policies.Added), len(r.Queries.Added))
			}

			var summary string
			for _, e := range r.Errors {
				if strings.Contains(e, "does not exist in Fleet yet") {
					t.Errorf("no-team reported as a new team: %q", e)
				}
				if strings.Contains(e, "no API diff available") {
					summary = e
				}
			}
			if !strings.HasPrefix(summary, tt.wantSummary+" configured") {
				t.Errorf("summary: got %q, want prefix %q", summary, tt.wantSummary+" configured")
			}
		})
	}
}

func TestDiffTeamFilter(t *testing.T) {
	current := &api.FleetState{
		Teams: []api.Team{{ID: 1, Name: "Alpha"}, {ID: 2, Name: "Beta"}},
	}
	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{Name: "Alpha", Policies: []parser.ParsedPolicy{{Name: "P1"}}},
			{Name: "Beta", Policies: []parser.ParsedPolicy{{Name: "P2"}}},
		},
	}

	results := Diff(current, proposed, []string{"Alpha"}, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result with filter, got %d", len(results))
	}
	if results[0].Team != "Alpha" {
		t.Errorf("team: got %q", results[0].Team)
	}
}

func TestDiffLabelValidation(t *testing.T) {
	tests := []struct {
		name        string
		apiLabels   []api.Label
		yamlLabels  []string
		wantValid   int
		wantMissing int
	}{
		{
			name:        "valid and missing labels on new policy",
			apiLabels:   []api.Label{{Name: "Managed Devices", HostCount: 24}},
			yamlLabels:  []string{"Managed Devices", "Missing Scope Label"},
			wantValid:   1,
			wantMissing: 1,
		},
		{
			name:       "all labels valid on new policy",
			apiLabels:  []api.Label{{Name: "A"}, {Name: "B"}},
			yamlLabels: []string{"A", "B"},
			wantValid:  2,
		},
		{
			name:        "all labels missing on new policy",
			apiLabels:   []api.Label{},
			yamlLabels:  []string{"Ghost"},
			wantMissing: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Policy "P" is new (not in current) so it appears in the diff,
			// which means its labels get validated.
			current := &api.FleetState{
				Teams:  []api.Team{{ID: 1, Name: "T", Policies: []api.Policy{}}},
				Labels: tt.apiLabels,
			}
			proposed := &parser.ParsedRepo{
				Teams: []parser.ParsedTeam{{
					Name:     "T",
					Policies: []parser.ParsedPolicy{{Name: "P", LabelsIncludeAny: tt.yamlLabels}},
				}},
			}

			results := Diff(current, proposed, nil, nil)
			r := results[0]

			if len(r.Labels.Valid) != tt.wantValid {
				t.Errorf("valid labels: got %d, want %d", len(r.Labels.Valid), tt.wantValid)
			}
			if len(r.Labels.Missing) != tt.wantMissing {
				t.Errorf("missing labels: got %d, want %d", len(r.Labels.Missing), tt.wantMissing)
			}
		})
	}
}

func TestDiffLabelValidationSkipsUnchangedPolicies(t *testing.T) {
	// Policy exists in both current and proposed with identical config.
	// Its labels should NOT appear in the label validation output.
	current := &api.FleetState{
		Teams: []api.Team{{
			ID:       1,
			Name:     "T",
			Policies: []api.Policy{{Name: "Unchanged", Query: "SELECT 1;", Platform: "darwin"}},
		}},
		Labels: []api.Label{{Name: "Some Label", HostCount: 10}},
	}
	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{{
			Name: "T",
			Policies: []parser.ParsedPolicy{{
				Name:             "Unchanged",
				Query:            "SELECT 1;",
				Platform:         "darwin",
				LabelsIncludeAny: []string{"Some Label"},
			}},
		}},
	}

	results := Diff(current, proposed, nil, nil)
	r := results[0]

	if len(r.Labels.Valid) != 0 {
		t.Errorf("unchanged policy labels should not appear: got %d valid", len(r.Labels.Valid))
	}
	if len(r.Labels.Missing) != 0 {
		t.Errorf("unchanged policy labels should not appear: got %d missing", len(r.Labels.Missing))
	}
}

func TestDiffQueryChanges(t *testing.T) {
	tests := []struct {
		name         string
		current      []api.Query
		proposed     []parser.ParsedQuery
		wantAdded    int
		wantModified int
		wantDeleted  int
		checkField   string
	}{
		{
			name:         "interval changed + new query",
			current:      []api.Query{{Name: "Inventory", Query: "SELECT * FROM disk_info;", Interval: 3600, Platform: "darwin"}},
			proposed:     []parser.ParsedQuery{{Name: "Inventory", Query: "SELECT * FROM disk_info;", Interval: 7200, Platform: "darwin"}, {Name: "New", Query: "SELECT 1;", Interval: 300, Platform: "windows"}},
			wantAdded:    1,
			wantModified: 1,
			checkField:   "interval",
		},
		{
			name:        "query deleted",
			current:     []api.Query{{Name: "Old", Query: "SELECT 1;", Interval: 3600}},
			proposed:    []parser.ParsedQuery{},
			wantDeleted: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := &api.FleetState{Teams: []api.Team{{ID: 1, Name: "T", Queries: tt.current}}}
			proposed := &parser.ParsedRepo{Teams: []parser.ParsedTeam{{Name: "T", Queries: tt.proposed}}}

			results := Diff(current, proposed, nil, nil)
			r := results[0]

			if len(r.Queries.Added) != tt.wantAdded {
				t.Errorf("added: got %d, want %d", len(r.Queries.Added), tt.wantAdded)
			}
			if len(r.Queries.Modified) != tt.wantModified {
				t.Errorf("modified: got %d, want %d", len(r.Queries.Modified), tt.wantModified)
			}
			if len(r.Queries.Deleted) != tt.wantDeleted {
				t.Errorf("deleted: got %d, want %d", len(r.Queries.Deleted), tt.wantDeleted)
			}
			if tt.checkField != "" && len(r.Queries.Modified) > 0 {
				if _, ok := r.Queries.Modified[0].Fields[tt.checkField]; !ok {
					t.Errorf("expected %q field diff", tt.checkField)
				}
			}
		})
	}
}

func TestResourceDiffTotal(t *testing.T) {
	rd := ResourceDiff{
		Added:    []ResourceChange{{Name: "a"}, {Name: "b"}},
		Modified: []ResourceChange{{Name: "c"}},
		Deleted:  []ResourceChange{{Name: "d"}},
	}
	if rd.Total() != 4 {
		t.Errorf("total: got %d, want 4", rd.Total())
	}
}

func TestResourceDiffIsEmpty(t *testing.T) {
	rd := ResourceDiff{}
	if !rd.IsEmpty() {
		t.Error("empty diff should report IsEmpty")
	}

	rd.Added = append(rd.Added, ResourceChange{Name: "a"})
	if rd.IsEmpty() {
		t.Error("non-empty diff should not report IsEmpty")
	}
}

func TestDiffSoftwarePackageAddedDeleted(t *testing.T) {
	current := &api.FleetState{
		Teams: []api.Team{
			{
				ID:   1,
				Name: "Workstations",
				Software: api.TeamSoftware{
					Packages: []api.TeamSoftwarePackage{
						{ReferencedYAMLPath: "software/mac/old/old.yml", URL: "https://example.com/old.pkg"},
						{ReferencedYAMLPath: "software/mac/keep/keep.yml", URL: "https://example.com/keep.pkg"},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "Workstations",
				Software: parser.ParsedSoftware{
					Packages: []parser.ParsedSoftwarePackage{
						{RefPath: "software/mac/keep/keep.yml", URL: "https://example.com/keep.pkg"},
						{RefPath: "software/mac/new/new.yml", URL: "https://example.com/new.pkg"},
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil)
	r := results[0]

	if len(r.Software.Added) != 1 {
		t.Fatalf("expected 1 added package, got %d", len(r.Software.Added))
	}
	if r.Software.Added[0].Name != "software/mac/new/new.yml" {
		t.Fatalf("unexpected added package: %q", r.Software.Added[0].Name)
	}
	if len(r.Software.Deleted) != 1 {
		t.Fatalf("expected 1 deleted package, got %d", len(r.Software.Deleted))
	}
	if r.Software.Deleted[0].Name != "software/mac/old/old.yml" {
		t.Fatalf("unexpected deleted package: %q", r.Software.Deleted[0].Name)
	}
}

func TestDiffSoftwarePackageModified(t *testing.T) {
	current := &api.FleetState{
		Teams: []api.Team{
			{
				ID:   1,
				Name: "Workstations",
				Software: api.TeamSoftware{
					Packages: []api.TeamSoftwarePackage{
						{
							ReferencedYAMLPath: "software/mac/slack/slack.yml",
							URL:                "https://example.com/slack-1.pkg",
							HashSHA256:         "abc123",
							SelfService:        false,
						},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "Workstations",
				Software: parser.ParsedSoftware{
					Packages: []parser.ParsedSoftwarePackage{
						{
							RefPath:     "software/mac/slack/slack.yml",
							URL:         "https://example.com/slack-2.pkg",
							HashSHA256:  "def456",
							SelfService: true,
						},
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil)
	r := results[0]

	if len(r.Software.Modified) != 1 {
		t.Fatalf("expected 1 modified package, got %d", len(r.Software.Modified))
	}
	mod := r.Software.Modified[0]
	if mod.Name != "software/mac/slack/slack.yml" {
		t.Fatalf("unexpected modified package name: %q", mod.Name)
	}
	if _, ok := mod.Fields["url"]; !ok {
		t.Fatal("expected url field diff")
	}
	if _, ok := mod.Fields["hash_sha256"]; !ok {
		t.Fatal("expected hash_sha256 field diff")
	}
	if _, ok := mod.Fields["self_service"]; !ok {
		t.Fatal("expected self_service field diff")
	}
}

// TestDiffSoftwareMultiPackageSharedFile verifies that when a single list-form
// package file yields multiple packages (all sharing one referenced YAML path),
// the diff surfaces each package instead of collapsing them by the shared path
// key. Both sides carry two packages under the same path; the URL discriminator
// keeps them distinct so an unchanged pair produces no spurious add/delete, and
// a single changed package is reported on its own.
func TestDiffSoftwareMultiPackageSharedFile(t *testing.T) {
	const sharedPath = "software/windows/multi/multi.yml"

	current := &api.FleetState{
		Teams: []api.Team{
			{
				ID:   1,
				Name: "Workstations",
				Software: api.TeamSoftware{
					Packages: []api.TeamSoftwarePackage{
						{ReferencedYAMLPath: sharedPath, URL: "https://example.com/app-one.exe", SelfService: false},
						{ReferencedYAMLPath: sharedPath, URL: "https://example.com/app-two.exe"},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "Workstations",
				Software: parser.ParsedSoftware{
					Packages: []parser.ParsedSoftwarePackage{
						// app-one flips self_service (modified); app-two unchanged.
						{RefPath: sharedPath, URL: "https://example.com/app-one.exe", SelfService: true},
						{RefPath: sharedPath, URL: "https://example.com/app-two.exe"},
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil)
	r := results[0]

	if len(r.Software.Added) != 0 {
		t.Fatalf("expected 0 added packages, got %d: %+v", len(r.Software.Added), r.Software.Added)
	}
	if len(r.Software.Deleted) != 0 {
		t.Fatalf("expected 0 deleted packages, got %d: %+v", len(r.Software.Deleted), r.Software.Deleted)
	}
	if len(r.Software.Modified) != 1 {
		t.Fatalf("expected 1 modified package (app-one self_service), got %d: %+v", len(r.Software.Modified), r.Software.Modified)
	}
	if _, ok := r.Software.Modified[0].Fields["self_service"]; !ok {
		t.Fatalf("expected self_service field diff on the changed package, got %+v", r.Software.Modified[0].Fields)
	}
}

func TestDiffSoftwareFleetAndAppStore(t *testing.T) {
	current := &api.FleetState{
		Teams: []api.Team{
			{
				ID:   1,
				Name: "Workstations",
				Software: api.TeamSoftware{
					FleetMaintained: []api.TeamFleetApp{
						{Slug: "slack/darwin", SelfService: false},
						{Slug: "zoom/darwin", SelfService: true},
					},
					AppStoreApps: []api.TeamAppStoreApp{
						{AppStoreID: "111", SelfService: true},
						{AppStoreID: "222", SelfService: false},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "Workstations",
				Software: parser.ParsedSoftware{
					FleetMaintained: []parser.ParsedFleetApp{
						{Slug: "slack/darwin", SelfService: true},  // modified
						{Slug: "notion/darwin", SelfService: true}, // added
					},
					AppStoreApps: []parser.ParsedAppStoreApp{
						{AppStoreID: "111", SelfService: true},  // unchanged
						{AppStoreID: "333", SelfService: false}, // added
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil)
	r := results[0]

	// Expect at least one add/delete/modify across fleet + app store
	if r.Software.IsEmpty() {
		t.Fatal("expected software changes, got empty diff")
	}
}

func TestDiffFleetMaintainedAppsNullAPIShowsAdded(t *testing.T) {
	// When the API returns fleet_maintained_apps: null and inference can't
	// reconstruct the state (no catalog/titles), the proposed apps should
	// appear as "added" — this is the honest diff.
	current := &api.FleetState{
		Teams: []api.Team{
			{
				ID:   1,
				Name: "Workstations",
				Software: api.TeamSoftware{
					AppStoreApps: []api.TeamAppStoreApp{
						{AppStoreID: "111", SelfService: true},
					},
					FleetMaintained: nil, // API returned null
				},
			},
		},
		// No catalog and no software titles -> inference returns nil
		FleetMaintainedCatalog: nil,
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "Workstations",
				Software: parser.ParsedSoftware{
					FleetMaintained: []parser.ParsedFleetApp{
						{Slug: "zoom/darwin", SelfService: true},
					},
					AppStoreApps: []parser.ParsedAppStoreApp{
						{AppStoreID: "111", SelfService: true},
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]

	// With no inference data, the fleet app should show as "added".
	if len(r.Software.Added) != 1 {
		t.Fatalf("expected 1 added fleet app, got %d added", len(r.Software.Added))
	}
	if r.Software.Added[0].Name != "fleet app zoom/darwin" {
		t.Fatalf("unexpected added name: %q", r.Software.Added[0].Name)
	}

	// No info/skip messages should be emitted.
	for _, e := range r.Errors {
		if strings.Contains(e, "fleet_maintained") {
			t.Fatalf("unexpected fleet_maintained error message: %q", e)
		}
	}
}

func TestDiffFleetMaintainedAppsInferenceFromTitles(t *testing.T) {
	current := &api.FleetState{
		FleetMaintainedCatalog: []api.FleetMaintainedApp{
			{
				Slug:     "google-chrome/darwin",
				Name:     "Google Chrome",
				Platform: "darwin",
			},
		},
		Teams: []api.Team{
			{
				ID:   1,
				Name: "Workstations",
				Software: api.TeamSoftware{
					// Custom package URL should be excluded from maintained-app inference.
					Packages: []api.TeamSoftwarePackage{
						{URL: "https://downloads.example.com/custom-agent.pkg"},
					},
					FleetMaintained: nil, // API currently returns null here
				},
				SoftwareTitles: []api.SoftwareTitle{
					{
						Name:   "Google Chrome",
						Source: "apps",
						SoftwarePackage: &api.SoftwareTitlePackageMeta{
							PackageURL:  "https://fleet-maintained.example/google-chrome.pkg",
							Platform:    "darwin",
							SelfService: true,
						},
					},
					{
						Name:   "Custom Agent",
						Source: "apps",
						SoftwarePackage: &api.SoftwareTitlePackageMeta{
							PackageURL:  "https://downloads.example.com/custom-agent.pkg",
							Platform:    "darwin",
							SelfService: true,
						},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "Workstations",
				Software: parser.ParsedSoftware{
					FleetMaintained: []parser.ParsedFleetApp{
						{Slug: "google-chrome/darwin", SelfService: false},
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]

	// Inferred current self_service=true, proposed=false -> modified.
	if len(r.Software.Modified) != 1 {
		t.Fatalf("expected 1 modified fleet app from inference, got %d", len(r.Software.Modified))
	}
	if r.Software.Modified[0].Name != "fleet app google-chrome/darwin" {
		t.Fatalf("unexpected modified name: %q", r.Software.Modified[0].Name)
	}
	if _, ok := r.Software.Modified[0].Fields["self_service"]; !ok {
		t.Fatal("expected self_service diff for inferred fleet app")
	}

	// No info/skip messages should be emitted — inference is silent.
	for _, e := range r.Errors {
		if strings.Contains(e, "fleet_maintained") {
			t.Fatalf("unexpected fleet_maintained message: %q", e)
		}
	}
}

func TestDiffFleetMaintainedAppsInferenceByAppID(t *testing.T) {
	fmaID := uint(42)
	current := &api.FleetState{
		FleetMaintainedCatalog: []api.FleetMaintainedApp{
			{
				ID:       fmaID,
				Slug:     "7-zip/windows",
				Name:     "7-Zip",
				Platform: "windows",
			},
		},
		Teams: []api.Team{
			{
				ID:   1,
				Name: "Workstations",
				Software: api.TeamSoftware{
					FleetMaintained: nil,
				},
				SoftwareTitles: []api.SoftwareTitle{
					{
						ID:     99,
						Name:   "7-Zip (x64)",
						Source: "programs", // Windows MSI/EXE source, NOT "apps"
						SoftwarePackage: &api.SoftwareTitlePackageMeta{
							PackageURL:           "https://fleet-maintained.example/7-zip.msi",
							Platform:             "windows",
							SelfService:          true,
							FleetMaintainedAppID: &fmaID,
						},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "Workstations",
				Software: parser.ParsedSoftware{
					FleetMaintained: []parser.ParsedFleetApp{
						{Slug: "7-zip/windows", SelfService: true},
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]

	if len(r.Software.Added) != 0 {
		t.Errorf("expected 0 added fleet apps (inferred via fleet_maintained_app_id), got %d: %v",
			len(r.Software.Added), r.Software.Added)
	}
	if len(r.Software.Modified) != 0 {
		t.Errorf("expected 0 modified fleet apps, got %d", len(r.Software.Modified))
	}
}

// TestDiffFleetMaintainedAppsInferenceWithMergedPackages verifies that
// inference works when the API returns fleet_maintained_apps: null and merges
// all software (including fleet-maintained) into team.Software.Packages.
func TestDiffFleetMaintainedAppsInferenceWithMergedPackages(t *testing.T) {
	current := &api.FleetState{
		FleetMaintainedCatalog: []api.FleetMaintainedApp{
			{ID: 10, Slug: "cursor/windows", Name: "Cursor", Platform: "windows"},
			{ID: 11, Slug: "notepad-plus-plus/windows", Name: "Notepad++", Platform: "windows"},
		},
		Teams: []api.Team{
			{
				ID:   6,
				Name: "NVDI",
				Software: api.TeamSoftware{
					FleetMaintained: nil, // API returns null
					Packages: []api.TeamSoftwarePackage{
						{URL: "https://downloads.cursor.com/CursorSetup-x64-2.3.21.exe"},
						{URL: "https://github.com/notepad-plus-plus/notepad-plus-plus/releases/download/v8.9.2/npp.8.9.2.Installer.x64.exe"},
						{URL: "https://example.com/custom-tool.exe"},
					},
				},
				SoftwareTitles: []api.SoftwareTitle{
					{
						ID: 570582, Name: "Cursor", Source: "programs",
						SoftwarePackage: &api.SoftwareTitlePackageMeta{
							PackageURL: "https://downloads.cursor.com/CursorSetup-x64-2.3.21.exe",
							Platform:   "windows", SelfService: true,
						},
					},
					{
						ID: 2254239, Name: "Notepad++", Source: "programs",
						SoftwarePackage: &api.SoftwareTitlePackageMeta{
							PackageURL: "https://github.com/notepad-plus-plus/notepad-plus-plus/releases/download/v8.9.2/npp.8.9.2.Installer.x64.exe",
							Platform:   "windows", SelfService: true,
						},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "NVDI",
				Software: parser.ParsedSoftware{
					FleetMaintained: []parser.ParsedFleetApp{
						{Slug: "cursor/windows", SelfService: true},
						{Slug: "notepad-plus-plus/windows", SelfService: true},
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]

	if len(r.Software.Added) != 0 {
		var slugs []string
		for _, a := range r.Software.Added {
			slugs = append(slugs, a.Name)
		}
		t.Errorf("expected 0 added fleet apps (URLs in Packages should not block inference), got %d: %v",
			len(r.Software.Added), slugs)
	}
}

// TestDiffFleetMaintainedAppsSkipsProposedCustomPackages verifies that titles
// whose PackageURL matches a proposed custom package are not inferred as
// fleet-maintained apps (prevents false REMOVED diffs for custom packages
// that happen to share a name with a catalog entry).
func TestDiffFleetMaintainedAppsSkipsProposedCustomPackages(t *testing.T) {
	current := &api.FleetState{
		FleetMaintainedCatalog: []api.FleetMaintainedApp{
			{ID: 30, Slug: "gimp/windows", Name: "GIMP", Platform: "windows"},
		},
		Teams: []api.Team{
			{
				ID: 6, Name: "NVDI",
				Software: api.TeamSoftware{FleetMaintained: nil},
				SoftwareTitles: []api.SoftwareTitle{
					{
						ID: 99, Name: "GIMP", Source: "programs",
						SoftwarePackage: &api.SoftwareTitlePackageMeta{
							PackageURL: "https://download.gimp.org/gimp/v3.0/windows/gimp-3.0.4-setup.exe",
							Platform:   "windows", SelfService: true,
						},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "NVDI",
				Software: parser.ParsedSoftware{
					Packages: []parser.ParsedSoftwarePackage{
						{URL: "https://download.gimp.org/gimp/v3.0/windows/gimp-3.0.4-setup.exe"},
					},
					FleetMaintained: []parser.ParsedFleetApp{
						{Slug: "some-other-app/windows", SelfService: true},
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	for _, d := range r.Software.Deleted {
		if d.Name == "fleet app gimp/windows" || d.Name == "gimp/windows" {
			t.Errorf("gimp/windows should NOT appear as deleted (it is a custom package, not FMA)")
		}
	}
}

// TestDiffFleetMaintainedAppsInferenceArchSuffix verifies that inference
// matches titles whose OS-reported name includes an architecture suffix
// (e.g., "Notepad++ (64-bit x64)") against catalog entries without it.
func TestDiffFleetMaintainedAppsInferenceArchSuffix(t *testing.T) {
	current := &api.FleetState{
		FleetMaintainedCatalog: []api.FleetMaintainedApp{
			{ID: 20, Slug: "notepad-plus-plus/windows", Name: "Notepad++", Platform: "windows"},
		},
		Teams: []api.Team{
			{
				ID: 6, Name: "NVDI",
				Software: api.TeamSoftware{FleetMaintained: nil},
				SoftwareTitles: []api.SoftwareTitle{
					{
						ID: 15977, Name: "Notepad++ (64-bit x64)", Source: "programs",
						SoftwarePackage: &api.SoftwareTitlePackageMeta{
							PackageURL: "https://github.com/notepad-plus-plus/notepad-plus-plus/releases/download/v8.9.2/npp.8.9.2.Installer.x64.exe",
							Platform:   "windows", SelfService: true,
						},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "NVDI",
				Software: parser.ParsedSoftware{
					FleetMaintained: []parser.ParsedFleetApp{
						{Slug: "notepad-plus-plus/windows", SelfService: true},
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if len(r.Software.Added) != 0 {
		var slugs []string
		for _, a := range r.Software.Added {
			slugs = append(slugs, a.Name)
		}
		t.Errorf("expected 0 added (arch suffix should be stripped for matching), got %d: %v",
			len(r.Software.Added), slugs)
	}
}

// TestDiffFleetMaintainedAppsInferencePrefixMatch verifies that inference
// matches titles whose OS-reported name is longer than the catalog's short
// marketing name (e.g., "OBS Studio" -> catalog "OBS", "Zoom Workplace" ->
// catalog "Zoom") via prefix matching.
func TestDiffFleetMaintainedAppsInferencePrefixMatch(t *testing.T) {
	current := &api.FleetState{
		FleetMaintainedCatalog: []api.FleetMaintainedApp{
			{ID: 50, Slug: "obs/windows", Name: "OBS", Platform: "windows"},
			{ID: 51, Slug: "zoom/windows", Name: "Zoom", Platform: "windows"},
		},
		Teams: []api.Team{
			{
				ID: 6, Name: "NVDI",
				Software: api.TeamSoftware{FleetMaintained: nil},
				SoftwareTitles: []api.SoftwareTitle{
					{
						ID: 16700, Name: "OBS Studio", Source: "programs",
						SoftwarePackage: &api.SoftwareTitlePackageMeta{
							PackageURL: "https://github.com/obsproject/obs-studio/releases/download/32.0.4/OBS-Studio-32.0.4-Windows-x64-Installer.exe",
							Platform:   "windows", SelfService: true,
						},
					},
					{
						ID: 16731, Name: "Zoom Workplace (X64)", Source: "programs",
						SoftwarePackage: &api.SoftwareTitlePackageMeta{
							PackageURL: "https://zoom.us/client/6.7.5.30439/ZoomInstallerFull.msi?archType=x64",
							Platform:   "windows", SelfService: true,
						},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "NVDI",
				Software: parser.ParsedSoftware{
					FleetMaintained: []parser.ParsedFleetApp{
						{Slug: "obs/windows", SelfService: true},
						{Slug: "zoom/windows", SelfService: true},
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if len(r.Software.Added) != 0 {
		var slugs []string
		for _, a := range r.Software.Added {
			slugs = append(slugs, a.Name)
		}
		t.Errorf("expected 0 added (prefix matching should resolve OBS/Zoom), got %d: %v",
			len(r.Software.Added), slugs)
	}
}

// TestDiffFleetMaintainedAppsScriptChange verifies that when an FMA's install
// script content changes, the diff reports it as modified.
func TestDiffFleetMaintainedAppsScriptChange(t *testing.T) {
	current := &api.FleetState{
		FleetMaintainedCatalog: []api.FleetMaintainedApp{
			{ID: 10, Slug: "cursor/windows", Name: "Cursor", Platform: "windows"},
		},
		Teams: []api.Team{
			{
				ID: 6, Name: "NVDI",
				Software: api.TeamSoftware{
					FleetMaintained: []api.TeamFleetApp{
						{
							Slug:          "cursor/windows",
							SelfService:   true,
							InstallScript: "old-install-script",
						},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "NVDI",
				Software: parser.ParsedSoftware{
					FleetMaintained: []parser.ParsedFleetApp{
						{
							Slug:          "cursor/windows",
							SelfService:   true,
							InstallScript: "new-install-script",
						},
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if len(r.Software.Modified) != 1 {
		t.Fatalf("expected 1 modified FMA, got %d (added=%d)", len(r.Software.Modified), len(r.Software.Added))
	}
	if r.Software.Modified[0].Name != "fleet app cursor/windows" {
		t.Errorf("unexpected modified name: %q", r.Software.Modified[0].Name)
	}
	if _, ok := r.Software.Modified[0].Fields["install_script"]; !ok {
		t.Error("expected install_script field diff")
	}
}

// TestDiffProfilesMatchByContentName verifies that profiles are matched by
// the name extracted from file content (e.g., PayloadDisplayName), not by
// filename. This is the exact bug that caused false add/delete diffs when
// a .mobileconfig file was renamed but its PayloadDisplayName stayed the same.
func TestDiffProfilesMatchByContentName(t *testing.T) {
	// API has a profile named "fleet_orbit-allowfulldiskaccess"
	current := []api.Profile{
		{Name: "fleet_orbit-allowfulldiskaccess"},
		{Name: "wifi-corporate"},
	}

	// YAML has a file named "renamed-fulldisk.mobileconfig" but its
	// PayloadDisplayName is "fleet_orbit-allowfulldiskaccess" (matches API).
	// Also has "wifi-corporate" that matches.
	proposed := []parser.ParsedProfile{
		{Path: "/repo/profiles/mac/renamed-fulldisk.mobileconfig", Name: "fleet_orbit-allowfulldiskaccess", Platform: "darwin"},
		{Path: "/repo/profiles/mac/wifi-corporate.mobileconfig", Name: "wifi-corporate", Platform: "darwin"},
	}

	diff, warnings := diffProfiles(current, proposed, nil, nil)
	if len(warnings) > 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if !diff.IsEmpty() {
		t.Errorf("expected no profile changes (names match by content), got added=%d modified=%d deleted=%d",
			len(diff.Added), len(diff.Modified), len(diff.Deleted))
	}
}

// TestDiffProfilesDetectsRealChanges verifies that genuine profile additions
// and deletions are still detected correctly.
func TestDiffProfilesDetectsRealChanges(t *testing.T) {
	current := []api.Profile{
		{Name: "old-profile"},
		{Name: "unchanged-profile"},
	}

	proposed := []parser.ParsedProfile{
		{Path: "/repo/profiles/mac/unchanged-profile.mobileconfig", Name: "unchanged-profile", Platform: "darwin"},
		{Path: "/repo/profiles/mac/new-profile.mobileconfig", Name: "new-profile", Platform: "darwin"},
	}

	diff, _ := diffProfiles(current, proposed, nil, nil)
	if len(diff.Added) != 1 || diff.Added[0].Name != "new-profile" {
		t.Errorf("expected 1 added profile 'new-profile', got %v", diff.Added)
	}
	if len(diff.Deleted) != 1 || diff.Deleted[0].Name != "old-profile" {
		t.Errorf("expected 1 deleted profile 'old-profile', got %v", diff.Deleted)
	}
}

// --- Global config diff tests ---

func TestDiffGlobalConfig(t *testing.T) {
	tests := []struct {
		name             string
		apiConfig        map[string]any
		proposedOrg      map[string]any
		wantChanges      int
		wantKey          string // key to find in changes (empty = skip check)
		wantKeyAbsent    string // key that must NOT appear in changes
		wantOld, wantNew string
	}{
		{
			name:      "key absent from API is skipped",
			apiConfig: map[string]any{"org_info": map[string]any{"org_name": "Acme Corp"}},
			proposedOrg: map[string]any{"org_info": map[string]any{
				"org_name": "Acme Corp", "org_logo_url": "https://example.com/logo.png",
			}},
			wantChanges: 0, wantKeyAbsent: "org_info.org_logo_url",
		},
		{
			name:      "value modified",
			apiConfig: map[string]any{"server_settings": map[string]any{"server_url": "https://fleet.old.com", "live_query_disabled": false}},
			proposedOrg: map[string]any{"server_settings": map[string]any{
				"server_url": "https://fleet.new.com", "live_query_disabled": false,
			}},
			wantChanges: 1, wantKey: "server_settings.server_url",
		},
		{
			name:      "env var placeholder skipped",
			apiConfig: map[string]any{"integrations": map[string]any{"jira": map[string]any{"api_token": "actual-token"}}},
			proposedOrg: map[string]any{"integrations": map[string]any{"jira": map[string]any{
				"api_token": "$JIRA_API_TOKEN",
			}}},
			wantChanges: 0, wantKeyAbsent: "integrations.jira.api_token",
		},
		{
			name:        "no changes when identical",
			apiConfig:   map[string]any{"org_info": map[string]any{"org_name": "Same Corp"}},
			proposedOrg: map[string]any{"org_info": map[string]any{"org_name": "Same Corp"}},
			wantChanges: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := &api.FleetState{Config: tt.apiConfig}
			proposed := &parser.ParsedRepo{
				Global: &parser.ParsedGlobal{OrgSettings: tt.proposedOrg},
				Teams:  []parser.ParsedTeam{},
			}

			results := Diff(current, proposed, nil, nil)
			global := findTeam(t, results, "(global)")

			if len(global.Config) != tt.wantChanges {
				t.Fatalf("config changes: got %d, want %d: %+v", len(global.Config), tt.wantChanges, global.Config)
			}
			if tt.wantKey != "" {
				found := false
				for _, c := range global.Config {
					if c.Key == tt.wantKey {
						found = true
						if tt.wantOld != "" && c.Old != tt.wantOld {
							t.Errorf("old: got %q, want %q", c.Old, tt.wantOld)
						}
						if tt.wantNew != "" && c.New != tt.wantNew {
							t.Errorf("new: got %q, want %q", c.New, tt.wantNew)
						}
					}
				}
				if !found {
					t.Errorf("expected key %q in config changes", tt.wantKey)
				}
			}
			if tt.wantKeyAbsent != "" {
				for _, c := range global.Config {
					if c.Key == tt.wantKeyAbsent {
						t.Errorf("key %q should not appear in config changes", tt.wantKeyAbsent)
					}
				}
			}
		})
	}
}

func TestDiffGlobalPoliciesAndQueries(t *testing.T) {
	current := &api.FleetState{
		Config:         map[string]any{},
		GlobalPolicies: []api.Policy{{Name: "Existing", Query: "SELECT 1;", Platform: "darwin"}},
		GlobalQueries:  []api.Query{{Name: "Existing Q", Query: "SELECT hostname FROM system_info;", Interval: 3600, Platform: "darwin"}},
	}
	proposed := &parser.ParsedRepo{
		Global: &parser.ParsedGlobal{
			Policies: []parser.ParsedPolicy{
				{Name: "Existing", Query: "SELECT 1;", Platform: "darwin"},
				{Name: "New Global Policy", Query: "SELECT 1 FROM os_version;", Platform: "linux"},
			},
			Queries: []parser.ParsedQuery{
				{Name: "Existing Q", Query: "SELECT hostname FROM system_info;", Interval: 7200, Platform: "darwin"},
			},
		},
		Teams: []parser.ParsedTeam{},
	}

	results := Diff(current, proposed, nil, nil)
	global := findTeam(t, results, "(global)")

	if len(global.Policies.Added) != 1 {
		t.Errorf("expected 1 added global policy, got %d", len(global.Policies.Added))
	}
	if len(global.Queries.Modified) != 1 {
		t.Errorf("expected 1 modified global query, got %d", len(global.Queries.Modified))
	}
}

func TestDiffGlobalSkippedWithTeamFilter(t *testing.T) {
	current := &api.FleetState{
		Config: map[string]any{"org_info": map[string]any{"org_name": "Old"}},
		Teams:  []api.Team{{ID: 1, Name: "Alpha"}},
	}
	proposed := &parser.ParsedRepo{
		Global: &parser.ParsedGlobal{OrgSettings: map[string]any{"org_info": map[string]any{"org_name": "New"}}},
		Teams:  []parser.ParsedTeam{{Name: "Alpha"}},
	}

	results := Diff(current, proposed, []string{"Alpha"}, nil)
	for _, r := range results {
		if r.Team == "(global)" {
			t.Error("global result should be skipped when team filter is set")
		}
	}
}

func TestFlattenMap(t *testing.T) {
	m := map[string]any{
		"a": "1",
		"b": map[string]any{
			"c": "2",
			"d": map[string]any{
				"e": "3",
			},
		},
	}

	got := make(map[string]string)
	flattenMap(m, "", func(key, val string) {
		got[key] = val
	})

	expected := map[string]string{
		"a":     "1",
		"b.c":   "2",
		"b.d.e": "3",
	}

	for k, v := range expected {
		if got[k] != v {
			t.Errorf("flattenMap: key %q = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(expected) {
		t.Errorf("flattenMap: got %d keys, want %d", len(got), len(expected))
	}
}

func TestContainsEnvVar(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"$FLEET_SECRET_WIFI", true},
		{"$SSO_METADATA", true},
		{"https://example.com", false},
		{"plain text", false},
		{"value with $VAR inside", true},
		{"", false},
	}

	for _, tt := range tests {
		if got := containsEnvVar(tt.input); got != tt.want {
			t.Errorf("containsEnvVar(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeWS(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"  hello  ", "hello"},
		{"hello\nworld", "hello world"},
		{"hello\n  world\n\ttab", "hello world tab"},
		{"SELECT 1\n   FROM table\n   WHERE x = 1;", "SELECT 1 FROM table WHERE x = 1;"},
		{"", ""},
		{"  \n\t  ", ""},
	}
	for _, tc := range cases {
		got := normalizeWS(tc.in)
		if got != tc.want {
			t.Errorf("normalizeWS(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripArchSuffix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Notepad++ (64-bit x64)", "Notepad++"},
		{"Zoom Workplace (X64)", "Zoom Workplace"},
		{"7-Zip (x64)", "7-Zip"},
		{"Something (arm64)", "Something"},
		{"App (32-bit)", "App"},
		{"No Suffix", "No Suffix"},
		{"Parens (not arch)", "Parens (not arch)"},
		{"", ""},
	}
	for _, tc := range cases {
		got := stripArchSuffix(tc.in)
		if got != tc.want {
			t.Errorf("stripArchSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDiffChangedFileFilterIncludesScriptSourceFiles verifies that when only a
// script file changes (no YAML changes), the changed-file filter still includes
// the parent software package in the diff output. Regression test for #11.
func TestDiffChangedFileFilterIncludesScriptSourceFiles(t *testing.T) {
	current := &api.FleetState{
		Teams: []api.Team{
			{
				ID:   1,
				Name: "Workstations",
				Software: api.TeamSoftware{
					Packages: []api.TeamSoftwarePackage{
						{
							ReferencedYAMLPath: "software/mac/printers-hq/printers-hq.yml",
							URL:                "https://example.com/printers-hq-1.0.pkg",
						},
						{
							ReferencedYAMLPath: "software/mac/slack/slack.yml",
							URL:                "https://example.com/slack-old.dmg",
						},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "Workstations",
				Software: parser.ParsedSoftware{
					Packages: []parser.ParsedSoftwarePackage{
						{
							RefPath:    "software/mac/printers-hq/printers-hq.yml",
							URL:        "https://example.com/printers-hq-2.0.pkg",
							SourceFile: "/repo/software/mac/printers-hq/printers-hq.yml",
							SourceFiles: []string{
								"/repo/software/mac/printers-hq/printers-hq-install.sh",
								"/repo/software/mac/printers-hq/printers-hq-uninstall.sh",
							},
						},
						{
							RefPath:    "software/mac/slack/slack.yml",
							URL:        "https://example.com/slack-new.dmg",
							SourceFile: "/repo/software/mac/slack/slack.yml",
						},
					},
				},
			},
		},
	}

	// Only the install script changed, no YAML changes.
	changedFiles := []string{
		"software/mac/printers-hq/printers-hq-install.sh",
	}

	results := Diff(current, proposed, nil, changedFiles)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]

	// printers-hq should appear (its install script is in changedFiles).
	if len(r.Software.Modified) != 1 {
		t.Fatalf("expected 1 modified package (printers-hq via script match), got %d modified, %d added, %d deleted",
			len(r.Software.Modified), len(r.Software.Added), len(r.Software.Deleted))
	}
	if !strings.Contains(r.Software.Modified[0].Name, "printers-hq") {
		t.Errorf("expected printers-hq in modified, got %q", r.Software.Modified[0].Name)
	}

	// slack should be filtered out (its YAML is not in changedFiles).
	for _, m := range r.Software.Modified {
		if strings.Contains(m.Name, "slack") {
			t.Errorf("slack should be filtered out, but found in modified: %q", m.Name)
		}
	}
}

// TestDiffChangedFileFilterYAMLStillWorks verifies that the changed-file filter
// continues to work for YAML-only changes (no regression from script tracking).
func TestDiffChangedFileFilterYAMLStillWorks(t *testing.T) {
	current := &api.FleetState{
		Teams: []api.Team{
			{
				ID:   1,
				Name: "T",
				Software: api.TeamSoftware{
					Packages: []api.TeamSoftwarePackage{
						{ReferencedYAMLPath: "software/mac/app/app.yml", URL: "https://example.com/old.pkg"},
					},
				},
			},
		},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name: "T",
				Software: parser.ParsedSoftware{
					Packages: []parser.ParsedSoftwarePackage{
						{
							RefPath:     "software/mac/app/app.yml",
							URL:         "https://example.com/new.pkg",
							SourceFile:  "/repo/software/mac/app/app.yml",
							SourceFiles: []string{"/repo/software/mac/app/install.sh"},
						},
					},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, []string{"software/mac/app/app.yml"})
	r := results[0]
	if len(r.Software.Modified) != 1 {
		t.Fatalf("expected 1 modified (YAML match), got %d", len(r.Software.Modified))
	}
}

// TestDiffScriptScenarios verifies add, delete, and no-change for scripts.
func TestDiffScriptScenarios(t *testing.T) {
	current := &api.FleetState{
		Teams: []api.Team{{
			ID:   1,
			Name: "T",
			Scripts: []api.Script{
				{ID: 1, Name: "existing.ps1"},
				{ID: 2, Name: "to-delete.sh"},
			},
		}},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{{
			Name: "T",
			Scripts: []parser.ParsedScript{
				{Name: "existing.ps1", Path: "/repo/scripts/existing.ps1", SourceFile: "/repo/teams/t.yml"},
				{Name: "new-script.py", Path: "/repo/scripts/new-script.py", SourceFile: "/repo/teams/t.yml"},
			},
		}},
	}

	results := Diff(current, proposed, nil, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]

	if len(r.Scripts.Added) != 1 {
		t.Fatalf("expected 1 added script, got %d", len(r.Scripts.Added))
	}
	if r.Scripts.Added[0].Name != "new-script.py" {
		t.Errorf("added script name: got %q", r.Scripts.Added[0].Name)
	}

	if len(r.Scripts.Deleted) != 1 {
		t.Fatalf("expected 1 deleted script, got %d", len(r.Scripts.Deleted))
	}
	if r.Scripts.Deleted[0].Name != "to-delete.sh" {
		t.Errorf("deleted script name: got %q", r.Scripts.Deleted[0].Name)
	}

	if len(r.Scripts.Modified) != 0 {
		t.Errorf("expected 0 modified scripts, got %d", len(r.Scripts.Modified))
	}
}

// TestDiffScriptContentModified verifies that script content changes are detected.
func TestDiffScriptContentModified(t *testing.T) {
	current := &api.FleetState{
		Teams: []api.Team{{
			ID:   1,
			Name: "T",
			Scripts: []api.Script{
				{ID: 1, Name: "helper.ps1", Content: "Write-Host 'old version'"},
				{ID: 2, Name: "unchanged.sh", Content: "echo hello"},
			},
		}},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{{
			Name: "T",
			Scripts: []parser.ParsedScript{
				{Name: "helper.ps1", Content: "Write-Host 'new version'", SourceFile: "/repo/teams/t.yml"},
				{Name: "unchanged.sh", Content: "echo hello", SourceFile: "/repo/teams/t.yml"},
			},
		}},
	}

	results := Diff(current, proposed, nil, nil)
	r := results[0]

	if len(r.Scripts.Modified) != 1 {
		t.Fatalf("expected 1 modified script, got %d", len(r.Scripts.Modified))
	}
	if r.Scripts.Modified[0].Name != "helper.ps1" {
		t.Errorf("modified script name: got %q", r.Scripts.Modified[0].Name)
	}
	if len(r.Scripts.Added) != 0 || len(r.Scripts.Deleted) != 0 {
		t.Errorf("expected no adds/deletes, got added=%d deleted=%d", len(r.Scripts.Added), len(r.Scripts.Deleted))
	}
}

// TestDiffScriptChangedFileFilter verifies that the changed-file filter
// includes scripts whose SourceFile matches a changed file.
func TestDiffScriptChangedFileFilter(t *testing.T) {
	current := &api.FleetState{
		Teams: []api.Team{{
			ID:   1,
			Name: "T",
		}},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{{
			Name: "T",
			Scripts: []parser.ParsedScript{
				{Name: "included.ps1", Path: "/repo/scripts/included.ps1", SourceFile: "/repo/teams/t.yml"},
				{Name: "excluded.sh", Path: "/repo/scripts/excluded.sh", SourceFile: "/repo/teams/other.yml"},
			},
		}},
	}

	results := Diff(current, proposed, nil, []string{"teams/t.yml"})
	r := results[0]

	// included.ps1 should appear (its SourceFile matches the changed file)
	if len(r.Scripts.Added) != 1 {
		t.Fatalf("expected 1 added script (filtered), got %d", len(r.Scripts.Added))
	}
	if r.Scripts.Added[0].Name != "included.ps1" {
		t.Errorf("added script name: got %q, want included.ps1", r.Scripts.Added[0].Name)
	}
}

// ---------- Baseline subtraction tests ----------

func TestSubtractResourceDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		total    ResourceDiff
		baseline ResourceDiff
		want     ResourceDiff
	}{
		{
			name:     "empty baseline changes nothing",
			total:    ResourceDiff{Added: []ResourceChange{{Name: "A"}}},
			baseline: ResourceDiff{},
			want:     ResourceDiff{Added: []ResourceChange{{Name: "A"}}},
		},
		{
			name:     "subtract matching delete",
			total:    ResourceDiff{Deleted: []ResourceChange{{Name: "old-policy"}, {Name: "mr-policy"}}},
			baseline: ResourceDiff{Deleted: []ResourceChange{{Name: "old-policy"}}},
			want:     ResourceDiff{Deleted: []ResourceChange{{Name: "mr-policy"}}},
		},
		{
			name:     "subtract matching add",
			total:    ResourceDiff{Added: []ResourceChange{{Name: "base-add"}, {Name: "mr-add"}}},
			baseline: ResourceDiff{Added: []ResourceChange{{Name: "base-add"}}},
			want:     ResourceDiff{Added: []ResourceChange{{Name: "mr-add"}}},
		},
		{
			name: "subtract identical modify",
			total: ResourceDiff{Modified: []ResourceChange{
				{Name: "same-mod", Fields: map[string]FieldDiff{"query": {Old: "a", New: "b"}}},
				{Name: "mr-mod", Fields: map[string]FieldDiff{"query": {Old: "x", New: "y"}}},
			}},
			baseline: ResourceDiff{Modified: []ResourceChange{
				{Name: "same-mod", Fields: map[string]FieldDiff{"query": {Old: "a", New: "b"}}},
			}},
			want: ResourceDiff{Modified: []ResourceChange{
				{Name: "mr-mod", Fields: map[string]FieldDiff{"query": {Old: "x", New: "y"}}},
			}},
		},
		{
			name: "keep modify with different fields",
			total: ResourceDiff{Modified: []ResourceChange{
				{Name: "evolved", Fields: map[string]FieldDiff{"query": {Old: "a", New: "c"}}},
			}},
			baseline: ResourceDiff{Modified: []ResourceChange{
				{Name: "evolved", Fields: map[string]FieldDiff{"query": {Old: "a", New: "b"}}},
			}},
			want: ResourceDiff{Modified: []ResourceChange{
				{Name: "evolved", Fields: map[string]FieldDiff{"query": {Old: "a", New: "c"}}},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := subtractResourceDiff(tt.total, tt.baseline)
			assertResourceDiffEqual(t, tt.want, got)
		})
	}
}

func TestDiffWithBaselineSubtraction(t *testing.T) {
	t.Parallel()

	// Simulate: base branch already removed "old-policy" and modified "shared-query".
	// MR adds "new-query" and removes "mr-removed-policy".
	// Only the MR's changes should appear.

	current := &api.FleetState{
		Teams: []api.Team{
			{
				ID:   1,
				Name: "TestTeam",
				Policies: []api.Policy{
					{Name: "old-policy", Query: "SELECT 1;", Platform: "linux"},
					{Name: "mr-removed-policy", Query: "SELECT 2;", Platform: "windows"},
				},
				Queries: []api.Query{
					{Name: "shared-query", Query: "SELECT old;", Interval: 3600},
					{Name: "existing-query", Query: "SELECT x;", Interval: 300},
				},
			},
		},
	}

	// MR branch: old-policy gone (already removed in base), mr-removed-policy gone (MR removes it),
	// shared-query modified to "SELECT new;" (already modified in base to same value),
	// new-query added (MR adds it).
	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name:       "TestTeam",
				SourceFile: "teams/test.yml",
				Queries: []parser.ParsedQuery{
					{Name: "shared-query", Query: "SELECT new;", Interval: 3600},
					{Name: "existing-query", Query: "SELECT x;", Interval: 300},
					{Name: "new-query", Query: "SELECT fresh;", Interval: 600, SourceFile: "queries/new.yml"},
				},
			},
		},
	}

	// Base branch: old-policy already removed, shared-query already modified,
	// but mr-removed-policy still present.
	baseline := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{
			{
				Name:       "TestTeam",
				SourceFile: "teams/test.yml",
				Policies: []parser.ParsedPolicy{
					{Name: "mr-removed-policy", Query: "SELECT 2;", Platform: "windows"},
				},
				Queries: []parser.ParsedQuery{
					{Name: "shared-query", Query: "SELECT new;", Interval: 3600},
					{Name: "existing-query", Query: "SELECT x;", Interval: 300},
				},
			},
		},
	}

	results := Diff(current, proposed, nil, nil, WithBaseline(baseline))
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]

	// old-policy deletion should be subtracted (already in base).
	if len(r.Policies.Deleted) != 1 {
		t.Fatalf("expected 1 deleted policy (mr-removed-policy), got %d: %+v", len(r.Policies.Deleted), r.Policies.Deleted)
	}
	if r.Policies.Deleted[0].Name != "mr-removed-policy" {
		t.Errorf("deleted policy name: got %q, want mr-removed-policy", r.Policies.Deleted[0].Name)
	}

	// shared-query modification should be subtracted (same diff in base).
	if len(r.Queries.Modified) != 0 {
		t.Errorf("expected 0 modified queries (subtracted), got %d: %+v", len(r.Queries.Modified), r.Queries.Modified)
	}

	// new-query addition should remain (not in base).
	if len(r.Queries.Added) != 1 {
		t.Fatalf("expected 1 added query (new-query), got %d", len(r.Queries.Added))
	}
	if r.Queries.Added[0].Name != "new-query" {
		t.Errorf("added query name: got %q, want new-query", r.Queries.Added[0].Name)
	}
}

func assertResourceDiffEqual(t *testing.T, want, got ResourceDiff) {
	t.Helper()
	assertChangesEqual(t, "Added", want.Added, got.Added)
	assertChangesEqual(t, "Modified", want.Modified, got.Modified)
	assertChangesEqual(t, "Deleted", want.Deleted, got.Deleted)
}

func assertChangesEqual(t *testing.T, label string, want, got []ResourceChange) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s: want %d changes, got %d", label, len(want), len(got))
		return
	}
	for i := range want {
		if want[i].Name != got[i].Name {
			t.Errorf("%s[%d].Name: want %q, got %q", label, i, want[i].Name, got[i].Name)
		}
	}
}

// TestDiffChangedFileFilterIncludesProfilePath verifies that modifying a profile
// XML file (not the team YAML) is detected by the changed-file filter.
func TestDiffChangedFileFilterIncludesProfilePath(t *testing.T) {
	t.Parallel()

	current := &api.FleetState{
		Teams: []api.Team{{
			ID:   1,
			Name: "T",
			Profiles: []api.Profile{
				{ProfileUUID: "uuid-1", Name: "experience_windows", Platform: "windows"},
				{ProfileUUID: "uuid-2", Name: "newsandinterests_windows", Platform: "windows"},
			},
		}},
	}

	proposed := &parser.ParsedRepo{
		Teams: []parser.ParsedTeam{{
			Name: "T",
			Profiles: []parser.ParsedProfile{
				{
					Name:       "experience_windows",
					Path:       "profiles/windows/experience_windows.xml",
					Platform:   "windows",
					SourceFile: "/repo/teams/t.yml",
				},
				{
					Name:       "newsandinterests_windows",
					Path:       "profiles/windows/newsandinterests_windows.xml",
					Platform:   "windows",
					SourceFile: "/repo/teams/t.yml",
				},
			},
		}},
	}

	// Only the profile XML changed, not the team YAML.
	changedFiles := []string{
		"profiles/windows/experience_windows.xml",
	}

	results := Diff(current, proposed, nil, changedFiles)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]

	// experience_windows should appear (its XML path is in changedFiles).
	if len(r.Profiles.Modified) != 1 {
		t.Fatalf("expected 1 modified profile (XML path match), got %d modified, %d added, %d deleted",
			len(r.Profiles.Modified), len(r.Profiles.Added), len(r.Profiles.Deleted))
	}
	if r.Profiles.Modified[0].Name != "experience_windows" {
		t.Errorf("expected experience_windows in modified, got %q", r.Profiles.Modified[0].Name)
	}

	// newsandinterests_windows should be filtered out (its XML is not in changedFiles).
	for _, m := range r.Profiles.Modified {
		if m.Name == "newsandinterests_windows" {
			t.Errorf("newsandinterests_windows should be filtered out, but found in modified")
		}
	}
}

func TestDiffSoftwareCategories(t *testing.T) {
	tests := []struct {
		name         string
		current      api.TeamSoftware
		proposed     parser.ParsedSoftware
		wantAdded    int
		wantModified int
		checkName    string
		checkHasCat  bool // expect "categories" field in result
	}{
		{
			name: "FMA added with categories",
			proposed: parser.ParsedSoftware{
				FleetMaintained: []parser.ParsedFleetApp{
					{Slug: "chrome/mac", SelfService: true, Categories: []string{"Browsers"}},
				},
			},
			wantAdded: 1, checkName: "fleet app chrome/mac", checkHasCat: true,
		},
		{
			name: "FMA added without categories",
			proposed: parser.ParsedSoftware{
				FleetMaintained: []parser.ParsedFleetApp{
					{Slug: "chrome/mac", SelfService: true},
				},
			},
			wantAdded: 1, checkName: "fleet app chrome/mac", checkHasCat: false,
		},
		{
			name: "package modified set to different set",
			current: api.TeamSoftware{
				Packages: []api.TeamSoftwarePackage{
					{ReferencedYAMLPath: "software/app.yml", URL: "https://x.com/a.pkg", Categories: []string{"Communication"}},
				},
			},
			proposed: parser.ParsedSoftware{
				Packages: []parser.ParsedSoftwarePackage{
					{RefPath: "software/app.yml", URL: "https://x.com/a.pkg", Categories: []string{"Productivity"}},
				},
			},
			wantModified: 1, checkName: "software/app.yml", checkHasCat: true,
		},
		{
			name: "package modified set to empty",
			current: api.TeamSoftware{
				Packages: []api.TeamSoftwarePackage{
					{ReferencedYAMLPath: "software/app.yml", URL: "https://x.com/a.pkg", Categories: []string{"Communication"}},
				},
			},
			proposed: parser.ParsedSoftware{
				Packages: []parser.ParsedSoftwarePackage{
					{RefPath: "software/app.yml", URL: "https://x.com/a.pkg"},
				},
			},
			wantModified: 1, checkName: "software/app.yml", checkHasCat: true,
		},
		{
			name: "package modified empty to set",
			current: api.TeamSoftware{
				Packages: []api.TeamSoftwarePackage{
					{ReferencedYAMLPath: "software/app.yml", URL: "https://x.com/a.pkg"},
				},
			},
			proposed: parser.ParsedSoftware{
				Packages: []parser.ParsedSoftwarePackage{
					{RefPath: "software/app.yml", URL: "https://x.com/a.pkg", Categories: []string{"Communication"}},
				},
			},
			wantModified: 1, checkName: "software/app.yml", checkHasCat: true,
		},
		{
			name: "package modified reorder only no change",
			current: api.TeamSoftware{
				Packages: []api.TeamSoftwarePackage{
					{ReferencedYAMLPath: "software/app.yml", URL: "https://x.com/a.pkg", Categories: []string{"B", "A"}},
				},
			},
			proposed: parser.ParsedSoftware{
				Packages: []parser.ParsedSoftwarePackage{
					{RefPath: "software/app.yml", URL: "https://x.com/a.pkg", Categories: []string{"A", "B"}},
				},
			},
			wantModified: 0,
		},
		{
			name: "app store app added with categories",
			proposed: parser.ParsedSoftware{
				AppStoreApps: []parser.ParsedAppStoreApp{
					{AppStoreID: "123456", SelfService: true, Categories: []string{"Productivity"}},
				},
			},
			wantAdded: 1, checkName: "app store app 123456", checkHasCat: true,
		},
		{
			name: "app store app modified categories",
			current: api.TeamSoftware{
				AppStoreApps: []api.TeamAppStoreApp{
					{AppStoreID: "123456", SelfService: true, Categories: []string{"Communication"}},
				},
			},
			proposed: parser.ParsedSoftware{
				AppStoreApps: []parser.ParsedAppStoreApp{
					{AppStoreID: "123456", SelfService: true, Categories: []string{"Communication", "Productivity"}},
				},
			},
			wantModified: 1, checkName: "app store app 123456", checkHasCat: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := &api.FleetState{
				Teams: []api.Team{{ID: 1, Name: "T", Software: tt.current}},
			}
			proposed := &parser.ParsedRepo{
				Teams: []parser.ParsedTeam{{Name: "T", Software: tt.proposed}},
			}

			results := Diff(current, proposed, nil, nil)
			r := results[0]

			if len(r.Software.Added) != tt.wantAdded {
				t.Errorf("added: got %d, want %d", len(r.Software.Added), tt.wantAdded)
			}
			if len(r.Software.Modified) != tt.wantModified {
				t.Errorf("modified: got %d, want %d", len(r.Software.Modified), tt.wantModified)
			}

			if tt.checkName != "" {
				var found *ResourceChange
				for i := range r.Software.Added {
					if r.Software.Added[i].Name == tt.checkName {
						found = &r.Software.Added[i]
					}
				}
				for i := range r.Software.Modified {
					if r.Software.Modified[i].Name == tt.checkName {
						found = &r.Software.Modified[i]
					}
				}
				if found == nil {
					t.Fatalf("expected resource %q not found", tt.checkName)
				}
				_, hasCat := found.Fields["categories"]
				if tt.checkHasCat && !hasCat {
					t.Error("expected categories field in diff")
				}
				if !tt.checkHasCat && hasCat {
					t.Error("did not expect categories field in diff")
				}
				if hasCat {
					fd := found.Fields["categories"]
					if !fd.IsSlice {
						t.Error("categories FieldDiff should have IsSlice=true")
					}
				}
			}
		})
	}
}

func TestCategoriesEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"both nil", nil, nil, true},
		{"both empty", []string{}, []string{}, true},
		{"same order", []string{"A", "B"}, []string{"A", "B"}, true},
		{"different order", []string{"B", "A"}, []string{"A", "B"}, true},
		{"different sets", []string{"A"}, []string{"B"}, false},
		{"different lengths", []string{"A", "B"}, []string{"A"}, false},
		{"nil vs empty", nil, []string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := categoriesEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("categoriesEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestFormatCategories(t *testing.T) {
	tests := []struct {
		input []string
		want  string
	}{
		{nil, "[]"},
		{[]string{}, "[]"},
		{[]string{"Communication"}, "[Communication]"},
		{[]string{"Productivity", "Browsers"}, "[Browsers, Productivity]"},
	}
	for _, tt := range tests {
		if got := formatCategories(tt.input); got != tt.want {
			t.Errorf("formatCategories(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// findTeam locates a DiffResult by team name, failing the test if not found.
func findTeam(t *testing.T, results []DiffResult, name string) *DiffResult {
	t.Helper()
	for i := range results {
		if results[i].Team == name {
			return &results[i]
		}
	}
	t.Fatalf("team %q not found in results", name)
	return nil
}

func TestSubtractConfigChanges(t *testing.T) {
	change := func(section, key, old, new string) ConfigChange {
		return ConfigChange{Section: section, Key: key, Old: old, New: new}
	}

	tests := []struct {
		name     string
		total    []ConfigChange
		baseline []ConfigChange
		want     []ConfigChange
	}{
		{
			name:  "empty baseline passes everything through",
			total: []ConfigChange{change("org_settings", "server_settings.server_url", "a", "b")},
			want:  []ConfigChange{change("org_settings", "server_settings.server_url", "a", "b")},
		},
		{
			name:     "identical change is subtracted",
			total:    []ConfigChange{change("org_settings", "k", "a", "b")},
			baseline: []ConfigChange{change("org_settings", "k", "a", "b")},
			want:     nil,
		},
		{
			name: "only the pre-existing change is subtracted",
			total: []ConfigChange{
				change("org_settings", "k1", "a", "b"),
				change("agent_options", "k2", "c", "d"),
			},
			baseline: []ConfigChange{change("org_settings", "k1", "a", "b")},
			want:     []ConfigChange{change("agent_options", "k2", "c", "d")},
		},
		{
			name:     "same key with a different new value is kept",
			total:    []ConfigChange{change("org_settings", "k", "a", "b")},
			baseline: []ConfigChange{change("org_settings", "k", "a", "different")},
			want:     []ConfigChange{change("org_settings", "k", "a", "b")},
		},
		{
			name:     "same key in a different section is kept",
			total:    []ConfigChange{change("controls", "k", "a", "b")},
			baseline: []ConfigChange{change("org_settings", "k", "a", "b")},
			want:     []ConfigChange{change("controls", "k", "a", "b")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := subtractConfigChanges(tt.total, tt.baseline)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d changes %+v, want %d %+v", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("change %d: got %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDiffTeamSettings(t *testing.T) {
	// A trimmed-down version of what GET /teams returns for a team.
	liveTeam := func() map[string]any {
		return map[string]any{
			"host_expiry_settings": map[string]any{
				"host_expiry_enabled": false,
				"host_expiry_window":  0,
			},
			"webhook_settings": map[string]any{
				"failing_policies_webhook": map[string]any{
					"enable_failing_policies_webhook": false,
					"destination_url":                 "https://old.example.com/hook",
					"host_batch_size":                 0,
				},
			},
			"features": map[string]any{"enable_software_inventory": true},
			"integrations": map[string]any{
				"google_calendar": map[string]any{"enable_calendar_events": false},
				"allow_list":      []any{"a", "b"},
			},
		}
	}

	tests := []struct {
		name        string
		proposed    map[string]any
		wantChanges []ConfigChange
		wantSkipped []string
	}{
		{
			name:     "no settings block",
			proposed: nil,
		},
		{
			name: "matching values produce no diff",
			proposed: map[string]any{
				"features": map[string]any{"enable_software_inventory": true},
			},
		},
		{
			name: "changed nested value",
			proposed: map[string]any{
				"host_expiry_settings": map[string]any{
					"host_expiry_enabled": true,
					"host_expiry_window":  30,
				},
			},
			wantChanges: []ConfigChange{
				{Section: "settings", Key: "host_expiry_settings.host_expiry_enabled", Old: "false", New: "true"},
				{Section: "settings", Key: "host_expiry_settings.host_expiry_window", Old: "0", New: "30"},
			},
		},
		{
			name: "deeply nested webhook value",
			proposed: map[string]any{
				"webhook_settings": map[string]any{
					"failing_policies_webhook": map[string]any{
						"destination_url": "https://new.example.com/hook",
					},
				},
			},
			wantChanges: []ConfigChange{
				{
					Section: "settings",
					Key:     "webhook_settings.failing_policies_webhook.destination_url",
					Old:     "https://old.example.com/hook",
					New:     "https://new.example.com/hook",
				},
			},
		},
		{
			// Enroll secrets are credentials; they must never reach the diff,
			// which lands in CI logs and MR comments.
			name: "secrets are never diffed",
			proposed: map[string]any{
				"secrets": []any{map[string]any{"secret": "literal-not-a-placeholder"}},
			},
		},
		{
			// Fleet substitutes $VARS server-side, so the YAML value and the
			// live value never match and comparing them is noise.
			name: "env var placeholders are skipped",
			proposed: map[string]any{
				"webhook_settings": map[string]any{
					"failing_policies_webhook": map[string]any{
						"destination_url": "$WEBHOOK_URL",
					},
				},
			},
		},
		{
			// The API does not report a value for this key, so "" cannot be
			// told apart from "Fleet has no opinion" -- reporting it would be
			// a guess.
			name: "key absent from the API section is not reported",
			proposed: map[string]any{
				"features": map[string]any{"enable_future_thing": true},
			},
		},
		{
			// List values are compared as serialized JSON, element order
			// included -- the same rule the global config diff uses.
			name: "reordered JSON list is reported as a change",
			proposed: map[string]any{
				"integrations": map[string]any{
					"allow_list": []any{"b", "a"},
				},
			},
			wantChanges: []ConfigChange{
				{
					Section: "settings",
					Key:     "integrations.allow_list",
					Old:     `["a","b"]`,
					New:     `["b","a"]`,
				},
			},
		},
		{
			name: "differing JSON lists are a change",
			proposed: map[string]any{
				"integrations": map[string]any{
					"allow_list": []any{"a", "c"},
				},
			},
			wantChanges: []ConfigChange{
				{
					Section: "settings",
					Key:     "integrations.allow_list",
					Old:     `["a","b"]`,
					New:     `["a","c"]`,
				},
			},
		},
		{
			name: "unknown settings sub-key is reported as skipped",
			proposed: map[string]any{
				"future_settings": map[string]any{"some_key": "value"},
			},
			wantSkipped: []string{"settings.future_settings"},
		},
		{
			name: "sub-key absent from the API is reported as skipped",
			proposed: map[string]any{
				"mdm": map[string]any{"enable_disk_encryption": true},
			},
			wantSkipped: []string{"settings.mdm"},
		},
		{
			name: "non-map sub-key is reported as skipped",
			proposed: map[string]any{
				"features": "not-a-map",
			},
			wantSkipped: []string{"settings.features"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changes, skipped := diffTeamSettings(liveTeam(), tt.proposed)

			if len(changes) != len(tt.wantChanges) {
				t.Fatalf("changes: got %d %+v, want %d %+v",
					len(changes), changes, len(tt.wantChanges), tt.wantChanges)
			}
			for i := range changes {
				if changes[i] != tt.wantChanges[i] {
					t.Errorf("change %d:\n got %+v\nwant %+v", i, changes[i], tt.wantChanges[i])
				}
			}
			if strings.Join(skipped, ",") != strings.Join(tt.wantSkipped, ",") {
				t.Errorf("skipped: got %v, want %v", skipped, tt.wantSkipped)
			}
		})
	}
}

func TestDiffTeamSettingsMissingAPISettings(t *testing.T) {
	// A team object with no settings at all (older Fleet, or a token that
	// cannot see them) must not report every proposed key as a change.
	changes, skipped := diffTeamSettings(nil, map[string]any{
		"features": map[string]any{"enable_software_inventory": false},
	})
	if len(changes) != 0 {
		t.Errorf("changes: got %+v, want none", changes)
	}
	if len(skipped) != 1 || skipped[0] != "settings.features" {
		t.Errorf("skipped: got %v, want [settings.features]", skipped)
	}
}

func TestDiffTeamSettingsOrderIsStable(t *testing.T) {
	current := map[string]any{
		"features":             map[string]any{"a": "1", "b": "2", "c": "3"},
		"host_expiry_settings": map[string]any{"host_expiry_window": 0},
	}
	proposed := map[string]any{
		"features":             map[string]any{"a": "x", "b": "y", "c": "z"},
		"host_expiry_settings": map[string]any{"host_expiry_window": 30},
	}

	// flattenMap walks maps in random order, so run repeatedly.
	var first []string
	for i := 0; i < 20; i++ {
		changes, _ := diffTeamSettings(current, proposed)
		var keys []string
		for _, c := range changes {
			keys = append(keys, c.Key)
		}
		if i == 0 {
			first = keys
			continue
		}
		if strings.Join(keys, ",") != strings.Join(first, ",") {
			t.Fatalf("unstable order: got %v, first run %v", keys, first)
		}
	}
	want := "features.a,features.b,features.c,host_expiry_settings.host_expiry_window"
	if strings.Join(first, ",") != want {
		t.Errorf("keys: got %v, want %s", first, want)
	}
}

// TestDiffTestdataTeamSettings checks that the `settings:` block in the shared
// fixture is diffed field by field against the team's live settings.
func TestDiffTestdataTeamSettings(t *testing.T) {
	root := testutil.TestdataRoot(t)

	proposed, err := parser.ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}

	current := &api.FleetState{
		Teams: []api.Team{{
			ID:   1,
			Name: "Workstations",
			Settings: map[string]any{
				"host_expiry_settings": map[string]any{
					"host_expiry_enabled": false,
					"host_expiry_window":  float64(0),
				},
				"webhook_settings": map[string]any{
					"failing_policies_webhook": map[string]any{
						"enable_failing_policies_webhook": false,
						"host_batch_size":                 float64(0),
					},
				},
				"features": map[string]any{"enable_software_inventory": true},
			},
		}},
	}

	results := Diff(current, proposed, []string{"Workstations"}, nil)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	got := make(map[string]string, len(results[0].Config))
	for _, c := range results[0].Config {
		if c.Section != "settings" {
			t.Errorf("section: got %q, want settings", c.Section)
		}
		got[c.Key] = c.Old + " → " + c.New
	}

	want := map[string]string{
		"host_expiry_settings.host_expiry_enabled":                                  "false → true",
		"host_expiry_settings.host_expiry_window":                                   "0 → 30",
		"webhook_settings.failing_policies_webhook.enable_failing_policies_webhook": "false → true",
		"webhook_settings.failing_policies_webhook.host_batch_size":                 "0 → 100",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	// features matches on both sides, and secrets must never appear.
	for k := range got {
		if strings.HasPrefix(k, "features") || strings.HasPrefix(k, "secrets") {
			t.Errorf("unexpected change reported for %q", k)
		}
	}
}

// When the no-team bucket has been fetched (team_id=0), it is diffed like any
// other team rather than being summarized.
func TestDiffNoTeamDeepDiff(t *testing.T) {
	current := &api.FleetState{
		Teams:  []api.Team{},
		Labels: []api.Label{},
		NoTeam: &api.NoTeam{
			Policies: []api.Policy{
				// Same name, different query → modified.
				{Name: "RingCentral uninstalled", Query: "SELECT 1;", Platform: "darwin"},
				// Not in the YAML → deleted.
				{Name: "Retired policy", Query: "SELECT 2;", PassingHostCount: 5},
			},
			Profiles: []api.Profile{{Name: "Conditional access", Platform: "darwin"}},
			Scripts: []api.Script{
				{ID: 1, Name: "uninstall-ringcentral.sh", Content: "echo one\n"},
				{ID: 2, Name: "gone.ps1", Content: "Write-Host removed\n"},
			},
		},
	}

	proposed := &parser.ParsedRepo{Teams: []parser.ParsedTeam{{
		Name:       "Unassigned",
		SourceFile: "fleets/unassigned.yml",
		Policies: []parser.ParsedPolicy{
			{Name: "RingCentral uninstalled", Query: "SELECT 42;", Platform: "darwin"},
			{Name: "Brand new policy", Query: "SELECT 3;"},
		},
		Profiles: []parser.ParsedProfile{
			{Name: "Conditional access", Platform: "darwin", Path: "lib/profiles/ca.mobileconfig"},
			{Name: "Newly added profile", Platform: "darwin", Path: "lib/profiles/new.mobileconfig"},
		},
		Scripts: []parser.ParsedScript{
			{Name: "uninstall-ringcentral.sh", Content: "echo one\necho two\n"},
		},
	}}}

	results := Diff(current, proposed, nil, nil)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]

	checks := []struct {
		what string
		got  ResourceDiff
		a    int
		m    int
		d    int
	}{
		{"policies", r.Policies, 1, 1, 1},
		{"profiles", r.Profiles, 1, 0, 0},
		{"scripts", r.Scripts, 0, 1, 1},
	}
	for _, c := range checks {
		if len(c.got.Added) != c.a || len(c.got.Modified) != c.m || len(c.got.Deleted) != c.d {
			t.Errorf("%s: got +%d ~%d -%d, want +%d ~%d -%d", c.what,
				len(c.got.Added), len(c.got.Modified), len(c.got.Deleted), c.a, c.m, c.d)
		}
	}

	// The summary fallback must not appear once a real diff is available.
	for _, e := range r.Errors {
		if strings.Contains(e, "no API diff available") {
			t.Errorf("summary fallback still reported: %q", e)
		}
		if strings.Contains(e, "does not exist in Fleet yet") {
			t.Errorf("no-team reported as a new team: %q", e)
		}
	}
}

func TestDiffNoTeamUnavailableResources(t *testing.T) {
	current := &api.FleetState{
		Teams:  []api.Team{},
		Labels: []api.Label{},
		NoTeam: &api.NoTeam{
			PoliciesUnavailable: true,
			ProfilesUnavailable: true,
			ScriptsUnavailable:  true,
		},
	}
	proposed := &parser.ParsedRepo{Teams: []parser.ParsedTeam{{
		Name:       "No team",
		SourceFile: "teams/no-team.yml",
		Policies:   []parser.ParsedPolicy{{Name: "P"}},
	}}}

	r := Diff(current, proposed, nil, nil)[0]

	// Nothing may be reported as an addition when the API side is unreadable:
	// that would claim Fleet has none of it, which is not known.
	if !r.Policies.IsEmpty() || !r.Profiles.IsEmpty() || !r.Scripts.IsEmpty() {
		t.Errorf("expected empty diffs, got policies=%+v profiles=%+v scripts=%+v",
			r.Policies, r.Profiles, r.Scripts)
	}
	for _, want := range []string{"policies diff skipped", "profiles diff skipped", "scripts diff skipped"} {
		found := false
		for _, e := range r.Errors {
			if strings.Contains(e, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %q in %v", want, r.Errors)
		}
	}
}

func TestDiffNoTeamSoftwareIsReportedAsSkipped(t *testing.T) {
	current := &api.FleetState{Teams: []api.Team{}, Labels: []api.Label{}, NoTeam: &api.NoTeam{}}
	proposed := &parser.ParsedRepo{Teams: []parser.ParsedTeam{{
		Name:       "Unassigned",
		SourceFile: "fleets/unassigned.yml",
		Software: parser.ParsedSoftware{
			Packages:        []parser.ParsedSoftwarePackage{{URL: "https://example.com/a.pkg"}},
			FleetMaintained: []parser.ParsedFleetApp{{Slug: "zoom/darwin"}},
		},
	}}}

	r := Diff(current, proposed, nil, nil)[0]

	if !r.Software.IsEmpty() {
		t.Errorf("software: got %+v, want empty (nothing to compare against)", r.Software)
	}
	found := false
	for _, e := range r.Errors {
		if strings.Contains(e, "software diff skipped: 2 software items configured") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing software skip note in %v", r.Errors)
	}
}

// A no-team change that is already merged to the base branch but not yet
// deployed must not be reported again on every later MR.
func TestDiffNoTeamBaselineSubtraction(t *testing.T) {
	current := &api.FleetState{
		Teams:  []api.Team{},
		Labels: []api.Label{},
		NoTeam: &api.NoTeam{
			Policies: []api.Policy{{Name: "Existing", Query: "SELECT 1;"}},
			Scripts:  []api.Script{{ID: 1, Name: "keep.sh", Content: "echo one\n"}},
		},
	}

	// Already on the base branch: the added policy and the edited script.
	baseline := &parser.ParsedRepo{Teams: []parser.ParsedTeam{{
		Name:       "No team",
		SourceFile: "teams/no-team.yml",
		Policies: []parser.ParsedPolicy{
			{Name: "Existing", Query: "SELECT 1;"},
			{Name: "Merged not deployed", Query: "SELECT 2;"},
		},
		Scripts: []parser.ParsedScript{{Name: "keep.sh", Content: "echo one\necho two\n"}},
	}}}

	// The MR adds one more policy on top of the base branch's state. The
	// branch spells the bucket differently, which must not defeat matching.
	proposed := &parser.ParsedRepo{Teams: []parser.ParsedTeam{{
		Name:       "Unassigned",
		SourceFile: "fleets/unassigned.yml",
		Policies: []parser.ParsedPolicy{
			{Name: "Existing", Query: "SELECT 1;"},
			{Name: "Merged not deployed", Query: "SELECT 2;"},
			{Name: "New in this MR", Query: "SELECT 3;"},
		},
		Scripts: []parser.ParsedScript{{Name: "keep.sh", Content: "echo one\necho two\n"}},
	}}}

	r := Diff(current, proposed, nil, nil, WithBaseline(baseline))[0]

	if len(r.Policies.Added) != 1 || r.Policies.Added[0].Name != "New in this MR" {
		t.Errorf("policies added: got %+v, want only the MR's own addition", r.Policies.Added)
	}
	if !r.Scripts.IsEmpty() {
		t.Errorf("scripts: got %+v, want empty (the edit is already on the base branch)", r.Scripts)
	}
}

func TestDiffNoTeamQueriesAreReportedAsSkipped(t *testing.T) {
	current := &api.FleetState{Teams: []api.Team{}, Labels: []api.Label{}, NoTeam: &api.NoTeam{}}
	proposed := &parser.ParsedRepo{Teams: []parser.ParsedTeam{{
		Name:       "No team",
		SourceFile: "teams/no-team.yml",
		Queries:    []parser.ParsedQuery{{Name: "Q1"}, {Name: "Q2"}},
	}}}

	r := Diff(current, proposed, nil, nil)[0]

	if !r.Queries.IsEmpty() {
		t.Errorf("queries: got %+v, want empty (Fleet has no no-team query scope)", r.Queries)
	}
	found := false
	for _, e := range r.Errors {
		if strings.Contains(e, "queries diff skipped: 2 queries configured") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing query skip note in %v", r.Errors)
	}
}

// fakeProfileEnricher serves canned profile content and counts downloads, so
// tests can assert the checksum pre-filter actually avoids fetching.
type fakeProfileEnricher struct {
	content map[string]string // profile UUID → stored content
	calls   int               // number of batches requested
	fetched int               // number of profiles across all batches
}

func (f *fakeProfileEnricher) EnrichProfileContents(_ context.Context, profiles []api.Profile) {
	f.calls++
	f.fetched += len(profiles)
	for i := range profiles {
		if c, ok := f.content[profiles[i].ProfileUUID]; ok {
			profiles[i].Content = c
		}
	}
}

func TestDiffProfilesContent(t *testing.T) {
	const stored = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
  <key>PayloadDisplayName</key><string>Wi-Fi</string>
  <key>PayloadContent</key><array><dict>
    <key>SSID_STR</key><string>Campus</string>
    <key>Password</key><string>expanded-secret</string>
  </dict></array>
</dict></plist>`

	tests := []struct {
		name        string
		local       string
		checksum    string // as reported by the API for the stored profile
		wantWarning string
		wantCalls   int
	}{
		{
			name:      "identical content produces no diff and no download",
			local:     stored,
			checksum:  profileChecksum([]byte(stored)),
			wantCalls: 0,
		},
		{
			name: "changed value names the key",
			local: strings.Replace(stored,
				"<key>SSID_STR</key><string>Campus</string>",
				"<key>SSID_STR</key><string>Campus-Guest</string>", 1),
			wantWarning: "1 key changed: PayloadContent[0].SSID_STR",
			wantCalls:   1,
		},
		{
			name: "added key is marked with +",
			local: strings.Replace(stored,
				"<key>SSID_STR</key><string>Campus</string>",
				"<key>SSID_STR</key><string>Campus</string><key>AutoJoin</key><true/>", 1),
			wantWarning: "1 key changed: +PayloadContent[0].AutoJoin",
			wantCalls:   1,
		},
		{
			// Fleet expands $VARS when storing a profile, so the bytes always
			// differ. That must not be reported as a change every single run.
			name: "variable substitution is not a change",
			local: strings.Replace(stored,
				"<string>expanded-secret</string>",
				"<string>$FLEET_GLOBAL_ENROLL_SECRET</string>", 1),
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enricher := &fakeProfileEnricher{content: map[string]string{"uuid-1": stored}}
			current := []api.Profile{{ProfileUUID: "uuid-1", Name: "Wi-Fi", Platform: "darwin", Checksum: tt.checksum}}
			proposed := []parser.ParsedProfile{{
				Name:     "Wi-Fi",
				Platform: "darwin",
				Path:     "lib/macos/profiles/wifi.mobileconfig",
				Content:  tt.local,
			}}

			diff, warnings := diffProfiles(current, proposed, nil, enricher)
			if len(warnings) != 0 {
				t.Errorf("warnings: got %v", warnings)
			}
			if enricher.calls != tt.wantCalls {
				t.Errorf("downloads: got %d, want %d", enricher.calls, tt.wantCalls)
			}

			if tt.wantWarning == "" {
				if !diff.IsEmpty() {
					t.Fatalf("expected no changes, got %+v", diff)
				}
				return
			}
			if len(diff.Modified) != 1 {
				t.Fatalf("got %d modified, want 1 (%+v)", len(diff.Modified), diff)
			}
			if diff.Modified[0].Warning != tt.wantWarning {
				t.Errorf("warning: got %q, want %q", diff.Modified[0].Warning, tt.wantWarning)
			}
			// No payload value may appear in the output.
			if strings.Contains(diff.Modified[0].Warning, "secret") ||
				strings.Contains(diff.Modified[0].Warning, "Campus") {
				t.Errorf("warning leaked a value: %q", diff.Modified[0].Warning)
			}
		})
	}
}

func TestDiffProfilesFallsBackToChangedFiles(t *testing.T) {
	current := []api.Profile{{ProfileUUID: "u", Name: "Windows Thing", Platform: "windows"}}
	proposed := []parser.ParsedProfile{{
		Name:     "Windows Thing",
		Platform: "windows",
		Path:     "lib/windows/profiles/thing.xml",
		// SyncML, which is not a property list: keys cannot be flattened.
		Content: "<Replace><Item><Target><LocURI>./Device/Foo</LocURI></Target></Item></Replace>",
	}}
	enricher := &fakeProfileEnricher{content: map[string]string{
		"u": "<Replace><Item><Target><LocURI>./Device/Bar</LocURI></Target></Item></Replace>",
	}}

	t.Run("file changed in the MR", func(t *testing.T) {
		diff, _ := diffProfiles(current, proposed, []string{"lib/windows/profiles/thing.xml"}, enricher)
		if len(diff.Modified) != 1 {
			t.Fatalf("got %+v, want 1 modified from the changed-file signal", diff)
		}
		if diff.Modified[0].Warning != "" {
			t.Errorf("warning: got %q, want empty (no keys could be named)", diff.Modified[0].Warning)
		}
	})

	t.Run("file not changed in the MR", func(t *testing.T) {
		diff, _ := diffProfiles(current, proposed, []string{"other.yml"}, enricher)
		if !diff.IsEmpty() {
			t.Errorf("got %+v, want no changes", diff)
		}
	})
}

func TestDiffProfilesWithoutEnricher(t *testing.T) {
	// Without an enricher, behavior is unchanged from before content diffing:
	// name matching plus the changed-file heuristic.
	current := []api.Profile{{ProfileUUID: "u", Name: "P", Checksum: "irrelevant"}}
	proposed := []parser.ParsedProfile{{Name: "P", Path: "p.mobileconfig", Content: "<plist><dict/></plist>"}}

	diff, _ := diffProfiles(current, proposed, []string{"p.mobileconfig"}, nil)
	if len(diff.Modified) != 1 {
		t.Fatalf("got %+v, want 1 modified", diff)
	}
	if got := diff.Modified[0].Fields["path"].New; got != "p.mobileconfig" {
		t.Errorf("path field: got %q", got)
	}
}

func TestWithProfileEnricher(t *testing.T) {
	var o diffOptions
	enricher := &fakeProfileEnricher{}
	WithProfileEnricher(enricher)(&o)
	if o.profileEnricher != enricher {
		t.Error("WithProfileEnricher did not set the enricher")
	}
}

func TestProfileContentChangeInconclusive(t *testing.T) {
	const validPlist = `<plist version="1.0"><dict><key>A</key><string>1</string></dict></plist>`

	tests := []struct {
		name     string
		cur      api.Profile
		proposed parser.ParsedProfile
	}{
		{
			name:     "local file could not be read",
			cur:      api.Profile{ProfileUUID: "u", Content: validPlist},
			proposed: parser.ParsedProfile{Content: ""},
		},
		{
			// The download failed or was never attempted, so Content is empty.
			name:     "stored content unavailable",
			cur:      api.Profile{ProfileUUID: "u"},
			proposed: parser.ParsedProfile{Content: validPlist},
		},
		{
			name:     "stored content is not parseable",
			cur:      api.Profile{ProfileUUID: "u", Content: "not a profile"},
			proposed: parser.ParsedProfile{Content: validPlist},
		},
		{
			name:     "local content is not parseable",
			cur:      api.Profile{ProfileUUID: "u", Content: validPlist},
			proposed: parser.ParsedProfile{Content: "not a profile"},
		},
		{
			// A valid plist this grammar cannot flatten yields no keys, which
			// must not read as "nothing changed".
			name:     "no payload keys could be extracted",
			cur:      api.Profile{ProfileUUID: "u", Content: `<plist version="1.0"><dict/></plist>`},
			proposed: parser.ParsedProfile{Content: `<plist version="1.0"><dict/></plist>`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			change, conclusive := profileContentChange(tt.cur, tt.proposed)
			if conclusive {
				t.Errorf("conclusive: got true, want false (change=%+v)", change)
			}
			if change != nil {
				t.Errorf("change: got %+v, want nil", change)
			}
		})
	}
}

// Content is fetched in one batch, and only for profiles whose checksum does
// not already prove them unchanged. One round trip per profile would serialize
// the downloads and defeat the client's concurrency limit.
func TestDiffProfilesFetchesInOneBatch(t *testing.T) {
	const stored = `<plist version="1.0"><dict><key>A</key><string>1</string></dict></plist>`
	changed := strings.Replace(stored, "<string>1</string>", "<string>2</string>", 1)

	current := []api.Profile{
		{ProfileUUID: "u1", Name: "P1", Checksum: profileChecksum([]byte(stored))}, // unchanged
		{ProfileUUID: "u2", Name: "P2", Checksum: "stale"},
		{ProfileUUID: "u3", Name: "P3", Checksum: "stale"},
	}
	proposed := []parser.ParsedProfile{
		{Name: "P1", Content: stored},
		{Name: "P2", Content: changed},
		{Name: "P3", Content: changed},
	}
	enricher := &fakeProfileEnricher{content: map[string]string{"u1": stored, "u2": stored, "u3": stored}}

	diff, _ := diffProfiles(current, proposed, nil, enricher)

	if len(diff.Modified) != 2 {
		t.Errorf("modified: got %d, want 2 (%+v)", len(diff.Modified), diff.Modified)
	}
	if enricher.calls != 1 {
		t.Errorf("enrichment batches: got %d, want 1", enricher.calls)
	}
	// P1's checksum matched, so its content must not have been requested.
	if enricher.fetched != 2 {
		t.Errorf("profiles fetched: got %d, want 2", enricher.fetched)
	}
}

// The baseline pass runs over the same profiles; it must reuse the content
// fetched by the first pass rather than downloading everything again.
func TestDiffProfilesReusesFetchedContentAcrossPasses(t *testing.T) {
	const stored = `<plist version="1.0"><dict><key>A</key><string>1</string></dict></plist>`
	changed := strings.Replace(stored, "<string>1</string>", "<string>2</string>", 1)

	current := []api.Profile{{ProfileUUID: "u1", Name: "P1", Checksum: "stale"}}
	proposed := []parser.ParsedProfile{{Name: "P1", Content: changed}}
	baseline := []parser.ParsedProfile{{Name: "P1", Content: changed}}
	enricher := &fakeProfileEnricher{content: map[string]string{"u1": stored}}

	if _, _ = diffProfiles(current, proposed, nil, enricher); enricher.fetched != 1 {
		t.Fatalf("first pass fetched %d profiles, want 1", enricher.fetched)
	}
	if _, _ = diffProfiles(current, baseline, nil, enricher); enricher.fetched != 1 {
		t.Errorf("second pass re-fetched: total %d, want 1", enricher.fetched)
	}
}

// The no-team bucket goes through the same profile path as any team, so its
// profiles get content-level diffs too rather than name-only matching.
func TestDiffNoTeamProfileContent(t *testing.T) {
	const stored = `<plist version="1.0"><dict>
  <key>PayloadDisplayName</key><string>Conditional access</string>
  <key>PayloadOrganization</key><string>Campus</string>
</dict></plist>`
	changed := strings.Replace(stored, "<string>Campus</string>", "<string>Campus IT</string>", 1)

	current := &api.FleetState{
		Teams:  []api.Team{},
		Labels: []api.Label{},
		NoTeam: &api.NoTeam{
			Profiles: []api.Profile{{ProfileUUID: "u1", Name: "Conditional access", Checksum: "stale"}},
		},
	}
	proposed := &parser.ParsedRepo{Teams: []parser.ParsedTeam{{
		Name:       "Unassigned",
		SourceFile: "fleets/unassigned.yml",
		Profiles: []parser.ParsedProfile{{
			Name:    "Conditional access",
			Path:    "lib/unassigned/profiles/ca.mobileconfig",
			Content: changed,
		}},
	}}}
	enricher := &fakeProfileEnricher{content: map[string]string{"u1": stored}}

	r := Diff(current, proposed, nil, nil, WithProfileEnricher(enricher))[0]

	if len(r.Profiles.Modified) != 1 {
		t.Fatalf("modified: got %+v, want 1", r.Profiles)
	}
	if got := r.Profiles.Modified[0].Warning; got != "1 key changed: PayloadOrganization" {
		t.Errorf("warning: got %q", got)
	}
}
