package git_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CampusTech/fleet-plan/internal/api"
	"github.com/CampusTech/fleet-plan/internal/diff"
	"github.com/CampusTech/fleet-plan/internal/output"
	"github.com/CampusTech/fleet-plan/internal/parser"
)

// TestIntegrationGitFlag validates that a mock Fleet API + synthetic YAML repo
// produces the expected diff table (matching the MR #1072 validated output).
//
// Expected rows (structural match: Change | Team | Type | Resource):
//
//	ADDED   | Test Alpha | Policy   | [Windows] Fleet Plan Test
//	MODIFIED| Test Alpha | Policy   | [Windows] Cinc Installed
//	REMOVED | Test Alpha | Policy   | [Linux] LCE Package Installed
//	ADDED   | Test Alpha | Query    | Fleet Plan Test Query
//	MODIFIED| Test Alpha | Query    | System Hardware Information
//	REMOVED | Test Alpha | Query    | [Windows] GPU Info
//	ADDED   | Test Alpha | Software | software/windows/fleet-plan-test/fleet-plan-test.yml
//	MODIFIED| Test Alpha | Software | fleet app 7-zip/windows
//	MODIFIED| Test Alpha | Software | fleet app acrobat/windows
//	MODIFIED| Test Alpha | Software | software/linux/cinc-fedora/cinc-fedora.yml
//	REMOVED | Test Alpha | Software | software/windows/dell-assist/dell-assist.yml
//	ADDED   | Test Alpha | Profile  | Fleet Plan Test Profile
//	REMOVED | Test Alpha | Profile  | certs-sap
//	ADDED   | Test Alpha | Script   | fleet-plan-test.ps1
//	MODIFIED| Test Alpha | Script   | enable-fleet-desktop.ps1
//	REMOVED | Test Alpha | Script   | sro_linux_eap_tls.sh
func TestIntegrationGitFlag(t *testing.T) {
	// Build synthetic gitops repo in a temp dir.
	root := t.TempDir()
	writeTestRepo(t, root)

	// Start mock Fleet API.
	ts := mockFleetAPI(t)
	defer ts.Close()

	t.Setenv("FLEET_PLAN_INSECURE", "1")

	client, err := api.NewClient(ts.URL, "test-token")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	repo, err := parser.ParseRepo(root, []string{"Test Alpha"}, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	if len(repo.Teams) == 0 {
		t.Fatal("no teams parsed")
	}

	state, err := client.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}

	results := diff.Diff(state, repo, []string{"Test Alpha"}, nil,
		diff.WithScriptEnricher(client))

	md := output.RenderDiffMarkdown(results, output.MarkdownOptions{
		Heading: "Test Plan",
		Marker:  "test-marker",
	})

	// Validate structural rows.
	type wantRow struct {
		change   string
		team     string
		kind     string
		resource string
	}
	expected := []wantRow{
		{"ADDED", "Test Alpha", "Policy", "[Windows] Fleet Plan Test"},
		{"MODIFIED", "Test Alpha", "Policy", "[Windows] Cinc Installed"},
		{"REMOVED", "Test Alpha", "Policy", "[Linux] LCE Package Installed"},
		{"ADDED", "Test Alpha", "Query", "Fleet Plan Test Query"},
		{"MODIFIED", "Test Alpha", "Query", "System Hardware Information"},
		{"REMOVED", "Test Alpha", "Query", "[Windows] GPU Info"},
		{"ADDED", "Test Alpha", "Software", "software/windows/fleet-plan-test/fleet-plan-test.yml"},
		{"MODIFIED", "Test Alpha", "Software", "fleet app 7-zip/windows"},
		{"MODIFIED", "Test Alpha", "Software", "fleet app acrobat/windows"},
		{"MODIFIED", "Test Alpha", "Software", "software/linux/cinc-fedora/cinc-fedora.yml"},
		{"REMOVED", "Test Alpha", "Software", "software/windows/dell-assist/dell-assist.yml"},
		{"ADDED", "Test Alpha", "Profile", "Fleet Plan Test Profile"},
		{"REMOVED", "Test Alpha", "Profile", "certs-sap"},
		{"ADDED", "Test Alpha", "Script", "fleet-plan-test.ps1"},
		{"MODIFIED", "Test Alpha", "Script", "enable-fleet-desktop.ps1"},
		{"REMOVED", "Test Alpha", "Script", "sro_linux_eap_tls.sh"},
	}

	for _, want := range expected {
		// Look for: | CHANGE | Team | Type | **Resource** |
		needle := fmt.Sprintf("| %s | %s | %s | **%s**", want.change, want.team, want.kind, want.resource)
		if !strings.Contains(md, needle) {
			t.Errorf("missing row: %s", needle)
		}
	}

	// Verify total count.
	if !strings.Contains(md, "5 added") {
		t.Errorf("expected 5 added in summary, got:\n%s", md)
	}
	if !strings.Contains(md, "6 modified") {
		t.Errorf("expected 6 modified in summary, got:\n%s", md)
	}
	if !strings.Contains(md, "5 deleted") {
		t.Errorf("expected 5 deleted in summary, got:\n%s", md)
	}

	if t.Failed() {
		t.Logf("Full markdown output:\n%s", md)
	}
}

// ---------- test repo builder ----------

func writeTestRepo(t *testing.T, root string) {
	t.Helper()

	// teams/alpha.yml
	mkdirAndWrite(t, root, "teams/alpha.yml", `name: Test Alpha
team_settings:
  secrets:
    - secret: "$ENROLL_SECRET"
policies:
  - path: ../policies/win-fleet-plan-test.yml
  - path: ../policies/win-cinc-installed.yml
queries:
  - path: ../queries/fleet-plan-test-query.yml
  - path: ../queries/system-hardware.yml
controls:
  scripts:
    - path: ../scripts/fleet-plan-test.ps1
    - path: ../scripts/enable-fleet-desktop.ps1
  macos_settings:
    custom_settings:
      - path: ../profiles/fleet-plan-test.mobileconfig
software:
  packages:
    - path: ../software/windows/fleet-plan-test/fleet-plan-test.yml
    - path: ../software/linux/cinc-fedora/cinc-fedora.yml
  fleet_maintained_apps:
    - slug: 7-zip/windows
      uninstall_script:
        path: ../software/windows/7-zip/uninstall-new.ps1
    - slug: acrobat/windows
      install_script:
        path: ../software/windows/acrobat/install-new.ps1
`)

	// Policies (proposed)
	mkdirAndWrite(t, root, "policies/win-fleet-plan-test.yml", `name: "[Windows] Fleet Plan Test"
query: "SELECT 1;"
platform: windows
`)
	mkdirAndWrite(t, root, "policies/win-cinc-installed.yml", `name: "[Windows] Cinc Installed"
query: "SELECT 1 FROM programs WHERE name = 'Cinc' AND version >= '18.7.6.1'; /* fleet-plan-test */"
platform: windows
`)

	// Queries (proposed)
	mkdirAndWrite(t, root, "queries/fleet-plan-test-query.yml", `name: Fleet Plan Test Query
query: "SELECT * FROM system_info;"
interval: 3600
platform: ""
logging: snapshot
`)
	mkdirAndWrite(t, root, "queries/system-hardware.yml", `name: System Hardware Information
query: "SELECT * FROM system_info;"
interval: 43200
platform: ""
logging: snapshot
`)

	// Scripts (proposed)
	mkdirAndWrite(t, root, "scripts/fleet-plan-test.ps1", `Write-Host "fleet-plan test"`)
	mkdirAndWrite(t, root, "scripts/enable-fleet-desktop.ps1", `# Enable Fleet Desktop
Set-Location $env:TEMP
# Added line 1
# Added line 2
Write-Host "Enabling Fleet Desktop"`)

	// Profiles (proposed): one new profile. API will have one old profile to be deleted.
	mkdirAndWrite(t, root, "profiles/fleet-plan-test.mobileconfig", `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadDisplayName</key>
	<string>Fleet Plan Test Profile</string>
	<key>PayloadIdentifier</key>
	<string>com.example.fleet-plan-test</string>
	<key>PayloadType</key>
	<string>Configuration</string>
	<key>PayloadUUID</key>
	<string>11111111-1111-1111-1111-111111111111</string>
	<key>PayloadVersion</key>
	<integer>1</integer>
</dict>
</plist>`)

	// Software packages (proposed)
	mkdirAndWrite(t, root, "software/windows/fleet-plan-test/fleet-plan-test.yml", `url: https://example.com/fleet-plan-test.exe
hash_sha256: aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff6666aabb7788ccdd9900
`)
	mkdirAndWrite(t, root, "software/linux/cinc-fedora/cinc-fedora.yml", `url: https://example.com/cinc-fedora.rpm
hash_sha256: 0000000000000000000000000000000000000000000000000000000000000000
`)

	// FMA scripts
	mkdirAndWrite(t, root, "software/windows/7-zip/uninstall-new.ps1", `# Uninstall 7-Zip
# Modified line`)
	mkdirAndWrite(t, root, "software/windows/acrobat/install-new.ps1", `# Install Acrobat
# Modified line`)
}

func mkdirAndWrite(t *testing.T, root, relPath, content string) {
	t.Helper()
	full := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------- mock Fleet API ----------

func mockFleetAPI(t *testing.T) *httptest.Server {
	t.Helper()
	const teamID = 42

	mux := http.NewServeMux()

	// Teams
	mux.HandleFunc("/api/v1/fleet/teams", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"teams": []map[string]any{
				{
					"id":   teamID,
					"name": "Test Alpha",
					"software": map[string]any{
						"packages": []map[string]any{
							{
								"url":                  "https://example.com/cinc-fedora.rpm",
								"hash_sha256":          "026a755000000000000000000000000000000000000000000000000000000000",
								"self_service":         false,
								"referenced_yaml_path": "software/linux/cinc-fedora/cinc-fedora.yml",
							},
							{
								"url":                  "https://example.com/dell-assist.exe",
								"hash_sha256":          "bbbb",
								"self_service":         false,
								"referenced_yaml_path": "software/windows/dell-assist/dell-assist.yml",
							},
						},
						"fleet_maintained_apps": nil,
						"app_store_apps":        []any{},
					},
				},
			},
		})
	})

	// Labels
	mux.HandleFunc("/api/v1/fleet/labels", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"labels": []any{}})
	})

	// Fleet-maintained catalog
	mux.HandleFunc("/api/v1/fleet/software/fleet_maintained_apps", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"fleet_maintained_apps": []map[string]any{
				{"id": 100, "slug": "7-zip/windows", "name": "7-Zip", "platform": "windows", "software_title_id": 1001},
				{"id": 101, "slug": "acrobat/windows", "name": "Adobe Acrobat Reader", "platform": "windows", "software_title_id": 1002},
			},
			"meta": map[string]any{"has_next_results": false},
		})
	})

	// Policies for team
	mux.HandleFunc(fmt.Sprintf("/api/v1/fleet/teams/%d/policies", teamID), func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"policies": []map[string]any{
				{
					"id":       1,
					"name":     "[Windows] Cinc Installed",
					"query":    "SELECT 1 FROM programs WHERE name = 'Cinc' AND version >= '18.7.6.1';",
					"platform": "windows",
				},
				{
					"id":       2,
					"name":     "[Linux] LCE Package Installed",
					"query":    "SELECT 1 FROM deb_packages WHERE name = 'lce-eap-tls';",
					"platform": "linux",
				},
			},
		})
	})

	// Queries for team
	mux.HandleFunc("/api/v1/fleet/queries", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"queries": []map[string]any{
				{
					"id":       10,
					"name":     "System Hardware Information",
					"query":    "SELECT * FROM system_info;",
					"interval": 86400,
					"platform": "",
					"logging":  "snapshot",
				},
				{
					"id":       11,
					"name":     "[Windows] GPU Info",
					"query":    "SELECT * FROM video_info;",
					"interval": 3600,
					"platform": "windows",
					"logging":  "snapshot",
				},
			},
		})
	})

	// Software titles for team (available_for_install)
	mux.HandleFunc("/api/v1/fleet/software/titles", func(w http.ResponseWriter, r *http.Request) {
		fmaID100 := uint(100)
		fmaID101 := uint(101)
		json.NewEncoder(w).Encode(map[string]any{
			"software_titles": []api.SoftwareTitle{
				{
					ID: 1001, Name: "7-Zip", Source: "fleet_maintained",
					SoftwarePackage: &api.SoftwareTitlePackageMeta{
						Name: "7-Zip", Platform: "windows",
						FleetMaintainedAppID: &fmaID100,
					},
				},
				{
					ID: 1002, Name: "Adobe Acrobat Reader", Source: "fleet_maintained",
					SoftwarePackage: &api.SoftwareTitlePackageMeta{
						Name: "Adobe Acrobat Reader", Platform: "windows",
						FleetMaintainedAppID: &fmaID101,
					},
				},
			},
			"meta": map[string]any{"has_next_results": false},
		})
	})

	// Software title detail (for FMA script enrichment)
	mux.HandleFunc("/api/v1/fleet/software/titles/1001", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"software_title": map[string]any{
				"id":   1001,
				"name": "7-Zip",
				"software_package": map[string]any{
					"install_script":   "# Default 7-Zip install",
					"uninstall_script": "# Uninstall 7-Zip",
					"platform":         "windows",
				},
			},
		})
	})
	mux.HandleFunc("/api/v1/fleet/software/titles/1002", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"software_title": map[string]any{
				"id":   1002,
				"name": "Adobe Acrobat Reader",
				"software_package": map[string]any{
					"install_script":   "# Install Acrobat",
					"uninstall_script": "# Default Acrobat uninstall",
					"platform":         "windows",
				},
			},
		})
	})

	// Profiles for team
	mux.HandleFunc("/api/v1/fleet/configuration_profiles", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				{"profile_uuid": "p1", "name": "certs-sap", "platform": "darwin"},
			},
		})
	})

	// Scripts for team
	mux.HandleFunc("/api/v1/fleet/scripts", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"scripts": []map[string]any{
				{"id": 201, "name": "enable-fleet-desktop.ps1", "team_id": teamID},
				{"id": 202, "name": "sro_linux_eap_tls.sh", "team_id": teamID},
			},
			"meta": map[string]any{"has_next_results": false},
		})
	})

	// Script content download
	mux.HandleFunc("/api/v1/fleet/scripts/201", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") == "media" {
			fmt.Fprint(w, "# Enable Fleet Desktop\nSet-Location $env:TEMP\nWrite-Host \"Enabling Fleet Desktop\"")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 201, "name": "enable-fleet-desktop.ps1"})
	})
	mux.HandleFunc("/api/v1/fleet/scripts/202", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") == "media" {
			fmt.Fprint(w, "#!/bin/bash\necho 'SRO EAP TLS setup'")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 202, "name": "sro_linux_eap_tls.sh"})
	})

	return httptest.NewServer(mux)
}
