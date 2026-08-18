package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CampusTech/fleet-plan/internal/testutil"
)

// TestParseTestdataRepo is the primary integration test: parse the shared
// testdata/ fixture and verify the full structure.
func TestParseTestdataRepo(t *testing.T) {
	root := testutil.TestdataRoot(t)

	repo, err := ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}

	if len(repo.Errors) > 0 {
		for _, e := range repo.Errors {
			t.Logf("parse error: %s", e)
		}
		t.Fatalf("expected zero parse errors, got %d", len(repo.Errors))
	}

	// Two teams: Workstations, Servers
	if len(repo.Teams) != 2 {
		t.Fatalf("expected 2 teams, got %d", len(repo.Teams))
	}

	// Find Workstations team
	var ws *ParsedTeam
	for i := range repo.Teams {
		if repo.Teams[i].Name == "Workstations" {
			ws = &repo.Teams[i]
			break
		}
	}
	if ws == nil {
		t.Fatal("Workstations team not found")
	}

	// Workstations: 4 policies (filevault, defender, ssh, firewall)
	if len(ws.Policies) != 4 {
		t.Fatalf("Workstations: expected 4 policies, got %d", len(ws.Policies))
	}

	// Verify FileVault policy
	var fv *ParsedPolicy
	for i := range ws.Policies {
		if strings.Contains(ws.Policies[i].Name, "FileVault") {
			fv = &ws.Policies[i]
			break
		}
	}
	if fv == nil {
		t.Fatal("FileVault policy not found")
	}
	if fv.Platform != "darwin" {
		t.Errorf("FileVault platform: got %q, want darwin", fv.Platform)
	}
	if !fv.Critical {
		t.Error("FileVault should be critical")
	}
	if len(fv.LabelsIncludeAny) != 1 || fv.LabelsIncludeAny[0] != "macOS 14+" {
		t.Errorf("FileVault labels_include_any: got %v", fv.LabelsIncludeAny)
	}

	// Verify Firewall policy (has empty resolution — intentional for rules testing)
	var fw *ParsedPolicy
	for i := range ws.Policies {
		if strings.Contains(ws.Policies[i].Name, "Firewall") {
			fw = &ws.Policies[i]
			break
		}
	}
	if fw == nil {
		t.Fatal("Firewall policy not found")
	}
	if fw.Resolution != "" {
		t.Errorf("Firewall resolution should be empty, got %q", fw.Resolution)
	}

	// Workstations: 3 queries (disk-usage, uptime, os-version)
	if len(ws.Queries) != 3 {
		t.Fatalf("Workstations: expected 3 queries, got %d", len(ws.Queries))
	}

	// Workstations: 1 profile (fleet_orbit-allowfulldiskaccess from renamed-fulldisk.mobileconfig)
	if len(ws.Profiles) != 1 {
		t.Fatalf("Workstations: expected 1 profile, got %d", len(ws.Profiles))
	}
	if ws.Profiles[0].Name != "fleet_orbit-allowfulldiskaccess" {
		t.Errorf("Workstations profile name: got %q, want fleet_orbit-allowfulldiskaccess", ws.Profiles[0].Name)
	}

	// Workstations: 2 software packages (slack, example-app)
	if len(ws.Software.Packages) != 2 {
		t.Fatalf("Workstations: expected 2 packages, got %d", len(ws.Software.Packages))
	}

	// Verify self_service override: example-app.yml has self_service: false,
	// but the team YAML overrides it to true.
	var exApp *ParsedSoftwarePackage
	for i := range ws.Software.Packages {
		if strings.Contains(ws.Software.Packages[i].RefPath, "example-app") {
			exApp = &ws.Software.Packages[i]
			break
		}
	}
	if exApp == nil {
		t.Fatal("example-app package not found")
	}
	if !exApp.SelfService {
		t.Error("example-app should have self_service overridden to true by team YAML")
	}

	// Find Servers team
	var srv *ParsedTeam
	for i := range repo.Teams {
		if repo.Teams[i].Name == "Servers" {
			srv = &repo.Teams[i]
			break
		}
	}
	if srv == nil {
		t.Fatal("Servers team not found")
	}
	if len(srv.Policies) != 1 {
		t.Fatalf("Servers: expected 1 policy, got %d", len(srv.Policies))
	}
	if len(srv.Queries) != 2 {
		t.Fatalf("Servers: expected 2 queries, got %d", len(srv.Queries))
	}

	// Labels from default.yml
	if len(repo.Labels) != 3 {
		t.Fatalf("expected 3 labels, got %d", len(repo.Labels))
	}
	labelNames := make(map[string]bool)
	for _, l := range repo.Labels {
		labelNames[l.Name] = true
	}
	if !labelNames["macOS 14+"] || !labelNames["Windows 11"] || !labelNames["Ubuntu 24.04"] {
		t.Errorf("expected labels 'macOS 14+', 'Windows 11', and 'Ubuntu 24.04', got %v", labelNames)
	}

	// --- Global config from default.yml ---
	if repo.Global == nil {
		t.Fatal("expected Global to be parsed from default.yml")
	}

	// org_settings
	if repo.Global.OrgSettings == nil {
		t.Fatal("expected OrgSettings to be non-nil")
	}
	orgInfo, ok := repo.Global.OrgSettings["org_info"].(map[string]any)
	if !ok {
		t.Fatal("expected org_info in OrgSettings")
	}
	if orgInfo["org_name"] != "Test Corp" {
		t.Errorf("org_name: got %q, want %q", orgInfo["org_name"], "Test Corp")
	}

	// agent_options
	if repo.Global.AgentOptions == nil {
		t.Fatal("expected AgentOptions to be non-nil")
	}
	agentConfig, ok := repo.Global.AgentOptions["config"].(map[string]any)
	if !ok {
		t.Fatal("expected config in AgentOptions")
	}
	opts, ok := agentConfig["options"].(map[string]any)
	if !ok {
		t.Fatal("expected options in agent_options.config")
	}
	if opts["distributed_interval"] != 10 {
		t.Errorf("distributed_interval: got %v, want 10", opts["distributed_interval"])
	}

	// controls
	if repo.Global.Controls == nil {
		t.Fatal("expected Controls to be non-nil")
	}
	if repo.Global.Controls["enable_disk_encryption"] != true {
		t.Errorf("enable_disk_encryption: got %v, want true", repo.Global.Controls["enable_disk_encryption"])
	}

	// Global policies
	if len(repo.Global.Policies) != 1 {
		t.Fatalf("expected 1 global policy, got %d", len(repo.Global.Policies))
	}
	if !strings.Contains(repo.Global.Policies[0].Name, "Osquery Health Check") {
		t.Errorf("global policy name: got %q", repo.Global.Policies[0].Name)
	}

	// Global queries
	if len(repo.Global.Queries) != 1 {
		t.Fatalf("expected 1 global query, got %d", len(repo.Global.Queries))
	}
	if repo.Global.Queries[0].Name != "Global System Info" {
		t.Errorf("global query name: got %q", repo.Global.Queries[0].Name)
	}
}

// TestProfileNameExtraction verifies that profile names are extracted from file
// content (PayloadDisplayName) rather than derived from the filename.
func TestProfileNameExtraction(t *testing.T) {
	root := testutil.TestdataRoot(t)

	repo, err := ParseRepo(root, []string{"Workstations"}, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}

	ws := &repo.Teams[0]
	if len(ws.Profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(ws.Profiles))
	}

	p := ws.Profiles[0]
	// The filename is "renamed-fulldisk.mobileconfig" but the PayloadDisplayName
	// inside the file is "fleet_orbit-allowfulldiskaccess". The parser should
	// use the PayloadDisplayName, not the filename.
	if p.Name != "fleet_orbit-allowfulldiskaccess" {
		t.Errorf("profile name: got %q, want %q", p.Name, "fleet_orbit-allowfulldiskaccess")
	}
	if p.Platform != "darwin" {
		t.Errorf("profile platform: got %q, want darwin", p.Platform)
	}
}

// TestExtractProfileNameFallback verifies that when a file can't be read or
// doesn't contain a recognizable name, we fall back to the filename.
func TestExtractProfileNameFallback(t *testing.T) {
	// Non-existent file should return empty string
	name := extractProfileName("/nonexistent/file.mobileconfig")
	if name != "" {
		t.Errorf("expected empty string for non-existent file, got %q", name)
	}

	// profileNameFromFilename should strip extensions
	tests := []struct {
		input, want string
	}{
		{"/path/to/my-profile.mobileconfig", "my-profile"},
		{"/path/to/declaration.json", "declaration"},
		{"/path/to/windows-policy.xml", "windows-policy"},
		{"simple.mobileconfig", "simple"},
	}
	for _, tt := range tests {
		got := profileNameFromFilename(tt.input)
		if got != tt.want {
			t.Errorf("profileNameFromFilename(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestExtractMobileconfigName verifies PayloadDisplayName extraction from plist XML.
// Fleet uses the top-level PayloadDisplayName (the one at the root dict, depth 1)
// as the profile identity, not the nested ones inside PayloadContent. Position
// within the file is not meaningful — different profile generators emit the
// top-level PayloadDisplayName before or after PayloadContent.
func TestExtractMobileconfigName(t *testing.T) {
	// Top-level PayloadDisplayName after PayloadContent array — should return
	// the top-level one (last occurrence), not the inner one.
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>PayloadDisplayName</key>
			<string>Inner Profile</string>
		</dict>
	</array>
	<key>PayloadDisplayName</key>
	<string>Top Level Name</string>
</dict>
</plist>`
	name := extractMobileconfigName([]byte(plist))
	if name != "Top Level Name" {
		t.Errorf("expected %q, got %q", "Top Level Name", name)
	}

	// Real-world pattern: inner PayloadDisplayName is human-friendly ("Google Chrome"),
	// top-level PayloadDisplayName matches the filename ("google_chrome-config").
	// Fleet uses the top-level one.
	realWorld := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>PayloadDisplayName</key>
			<string>Google Chrome</string>
			<key>PayloadType</key>
			<string>com.google.Chrome</string>
		</dict>
	</array>
	<key>PayloadDisplayName</key>
	<string>google_chrome-config</string>
	<key>PayloadIdentifier</key>
	<string>com.fleetdm.fleet.mdm.custom.google_chrome-config</string>
</dict>
</plist>`
	name = extractMobileconfigName([]byte(realWorld))
	if name != "google_chrome-config" {
		t.Errorf("expected %q, got %q", "google_chrome-config", name)
	}

	// No PayloadContent — single PayloadDisplayName
	simple := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>PayloadDisplayName</key>
	<string>Simple Profile</string>
</dict>
</plist>`
	name = extractMobileconfigName([]byte(simple))
	if name != "Simple Profile" {
		t.Errorf("expected %q, got %q", "Simple Profile", name)
	}

	// Top-level PayloadDisplayName BEFORE PayloadContent — the order Apple
	// Configurator emits and what most hand-edited profiles look like.
	// Earlier extractor used the last occurrence and would have returned
	// the deepest nested name; now we require depth=1.
	topFirst := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>PayloadDisplayName</key>
	<string>Zoom Room Activation Code — University of Pennsylvania (Room2)</string>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>PayloadDisplayName</key>
			<string>Zoom Room Activation Code</string>
		</dict>
		<dict>
			<key>PayloadDisplayName</key>
			<string>Zoom Room Activation Code (Mac)</string>
		</dict>
	</array>
</dict>
</plist>`
	name = extractMobileconfigName([]byte(topFirst))
	if name != "Zoom Room Activation Code — University of Pennsylvania (Room2)" {
		t.Errorf("top-first ordering: expected the outer PayloadDisplayName, got %q", name)
	}

	// No PayloadDisplayName at all
	empty := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>PayloadType</key>
	<string>Configuration</string>
</dict>
</plist>`
	name = extractMobileconfigName([]byte(empty))
	if name != "" {
		t.Errorf("expected empty string, got %q", name)
	}

	// XML entities in the top-level PayloadDisplayName must be decoded so the
	// name matches the value Fleet reports (identity would mismatch otherwise).
	entities := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>PayloadDisplayName</key>
	<string>VPN &amp; Wi-Fi &lt;Corp&gt;</string>
	<key>PayloadType</key>
	<string>Configuration</string>
</dict>
</plist>`
	name = extractMobileconfigName([]byte(entities))
	if name != "VPN & Wi-Fi <Corp>" {
		t.Errorf("expected decoded entities, got %q", name)
	}
}

func TestInferApplePlatform(t *testing.T) {
	tests := []struct {
		refPath string
		want    string
	}{
		{"lib/macos/profiles/foo.mobileconfig", "darwin"},
		{"lib/ios/profiles/foo.mobileconfig", "ios"},
		{"lib/ipados/profiles/foo.mobileconfig", "ipados"},
		// Root-level relative paths without a leading slash.
		{"ios/profile.mobileconfig", "ios"},
		{"ipados/profile.mobileconfig", "ipados"},
		// Case-insensitive.
		{"lib/iOS/profile.mobileconfig", "ios"},
		// Platform token only counts as a whole path component.
		{"lib/macos/ios-helper.mobileconfig", "darwin"},
		{"profiles/foo.mobileconfig", "darwin"},
	}
	for _, tt := range tests {
		if got := inferApplePlatform(tt.refPath); got != tt.want {
			t.Errorf("inferApplePlatform(%q) = %q, want %q", tt.refPath, got, tt.want)
		}
	}
}

// TestExtractProfileNameUsesFilenameForNonMobileconfig verifies that .json and
// .xml profiles use the filename (not content) as the identity, matching Fleet's
// behavior (see SameProfileNameUploadErrorMsg in Fleet source).
func TestExtractProfileNameUsesFilenameForNonMobileconfig(t *testing.T) {
	// Create a .json file with a PayloadDisplayName that differs from filename
	dir := t.TempDir()
	jsonFile := filepath.Join(dir, "my-declaration.json")
	os.WriteFile(jsonFile, []byte(`{"PayloadDisplayName": "Different Name"}`), 0o644)

	// extractProfileName should return "" for .json (caller falls back to filename)
	name := extractProfileName(jsonFile)
	if name != "" {
		t.Errorf("expected empty string for .json file, got %q", name)
	}

	// Same for .xml
	xmlFile := filepath.Join(dir, "my-policy.xml")
	os.WriteFile(xmlFile, []byte(`<Policy><Name>Different</Name></Policy>`), 0o644)
	name = extractProfileName(xmlFile)
	if name != "" {
		t.Errorf("expected empty string for .xml file, got %q", name)
	}
}

// TestExtractProfileNameSizeGuard verifies that oversized files are rejected.
func TestExtractProfileNameSizeGuard(t *testing.T) {
	dir := t.TempDir()
	bigFile := filepath.Join(dir, "huge.mobileconfig")
	// Create a file just over the limit (write 11 MB of zeros)
	f, _ := os.Create(bigFile)
	f.Write(make([]byte, 11<<20))
	f.Close()

	name := extractProfileName(bigFile)
	if name != "" {
		t.Errorf("expected empty string for oversized file, got %q", name)
	}
}

// TestParseTestdataTeamFilter verifies that team filtering works against testdata.
func TestParseTestdataTeamFilter(t *testing.T) {
	root := testutil.TestdataRoot(t)

	repo, err := ParseRepo(root, []string{"Workstations"}, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}

	if len(repo.Teams) != 1 {
		t.Fatalf("expected 1 team (filtered), got %d", len(repo.Teams))
	}
	if repo.Teams[0].Name != "Workstations" {
		t.Errorf("team name: got %q", repo.Teams[0].Name)
	}
}

// TestParseRepoErrors consolidates error-case tests into a single table-driven test.
func TestParseRepoErrors(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(root string) // create files under root
		wantErrCount int               // minimum expected errors (0 = just check > 0)
		wantErrMsg   string            // substring to find in at least one error message
	}{
		{
			name:       "broken path reference",
			wantErrMsg: "nonexistent.yml",
			setup: func(root string) {
				teamsDir := filepath.Join(root, "teams")
				os.MkdirAll(teamsDir, 0o755)
				os.WriteFile(filepath.Join(teamsDir, "broken.yml"), []byte(`name: Broken
policies:
  - path: ../policies/nonexistent.yml
queries: []
agent_options: {}
controls: {}
software: {}
team_settings: {}
`), 0o644)
			},
		},
		{
			name:       "missing teams directory",
			wantErrMsg: "directory not found",
			setup:      func(root string) {}, // empty dir, no fleets/ or teams/
		},
		{
			name:       "missing name field",
			wantErrMsg: "name",
			setup: func(root string) {
				teamsDir := filepath.Join(root, "teams")
				os.MkdirAll(teamsDir, 0o755)
				os.WriteFile(filepath.Join(teamsDir, "bad.yml"), []byte(`policies: []
queries: []
`), 0o644)
			},
		},
		{
			name:       "unknown top-level key",
			wantErrMsg: `unknown top-level key: "bogus_key"`,
			setup: func(root string) {
				teamsDir := filepath.Join(root, "teams")
				os.MkdirAll(teamsDir, 0o755)
				os.WriteFile(filepath.Join(teamsDir, "test.yml"), []byte(`name: Test
bogus_key: true
policies: []
queries: []
`), 0o644)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(root)

			repo, err := ParseRepo(root, nil, "")
			if err != nil {
				t.Fatalf("ParseRepo: %v", err)
			}

			if len(repo.Errors) == 0 {
				t.Fatal("expected parse errors, got none")
			}

			if tt.wantErrMsg != "" {
				found := false
				for _, e := range repo.Errors {
					if strings.Contains(e.Message, tt.wantErrMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error containing %q, got: %v", tt.wantErrMsg, repo.Errors)
				}
			}
		})
	}
}

func TestValidatePlatform(t *testing.T) {
	tests := []struct {
		input   string
		invalid []string
	}{
		{"darwin", nil},
		{"windows", nil},
		{"linux", nil},
		{"chrome", nil},
		{"darwin,windows", nil},
		{"darwin,windows,linux", nil},
		{"", nil},
		{"macos", []string{"macos"}},
		{"darwin,macos", []string{"macos"}},
		{"osx", []string{"osx"}},
	}

	for _, tt := range tests {
		invalid := ValidatePlatform(tt.input)
		if len(invalid) != len(tt.invalid) {
			t.Errorf("ValidatePlatform(%q): got %v, want %v", tt.input, invalid, tt.invalid)
		}
	}
}

func TestValidateLogging(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"snapshot", true},
		{"differential", true},
		{"differential_ignore_removals", true},
		{"", true},
		{"invalid", false},
	}

	for _, tt := range tests {
		if ValidateLogging(tt.input) != tt.valid {
			t.Errorf("ValidateLogging(%q): got %v, want %v", tt.input, !tt.valid, tt.valid)
		}
	}
}

// TestParseSoftwarePackageScriptSourceFiles verifies that install_script,
// uninstall_script, and other path: refs inside a software package YAML are
// resolved and tracked in SourceFiles. This enables the changed-file filter
// to match MRs that only modify scripts (no YAML changes).
func TestParseSoftwarePackageScriptSourceFiles(t *testing.T) {
	root := testutil.TestdataRoot(t)

	repo, err := ParseRepo(root, []string{"Workstations"}, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	if len(repo.Errors) > 0 {
		for _, e := range repo.Errors {
			t.Logf("parse error: %s", e)
		}
		t.Fatalf("expected zero parse errors, got %d", len(repo.Errors))
	}

	ws := &repo.Teams[0]
	var exApp *ParsedSoftwarePackage
	for i := range ws.Software.Packages {
		if strings.Contains(ws.Software.Packages[i].RefPath, "example-app") {
			exApp = &ws.Software.Packages[i]
			break
		}
	}
	if exApp == nil {
		t.Fatal("example-app package not found")
	}

	if len(exApp.SourceFiles) != 2 {
		t.Fatalf("expected 2 SourceFiles (install.ps1, uninstall.ps1), got %d: %v", len(exApp.SourceFiles), exApp.SourceFiles)
	}

	hasInstall, hasUninstall := false, false
	for _, sf := range exApp.SourceFiles {
		if strings.HasSuffix(sf, "install.ps1") && !strings.Contains(sf, "uninstall") {
			hasInstall = true
		}
		if strings.HasSuffix(sf, "uninstall.ps1") {
			hasUninstall = true
		}
	}
	if !hasInstall {
		t.Errorf("expected install.ps1 in SourceFiles, got %v", exApp.SourceFiles)
	}
	if !hasUninstall {
		t.Errorf("expected uninstall.ps1 in SourceFiles, got %v", exApp.SourceFiles)
	}
}

// TestParseRepoDuplicateSoftwareRefs verifies duplicate software ref detection.
func TestParseRepoDuplicateSoftwareRefs(t *testing.T) {
	root := t.TempDir()

	teamsDir := filepath.Join(root, "teams")
	softwareDir := filepath.Join(root, "software", "windows", "dup-app")
	os.MkdirAll(teamsDir, 0o755)
	os.MkdirAll(softwareDir, 0o755)

	pkgYAML := `url: https://downloads.example.com/dup-app.msi
self_service: false
`
	os.WriteFile(filepath.Join(softwareDir, "dup-app.yml"), []byte(pkgYAML), 0o644)

	teamYAML := `name: Workstations
policies: []
queries: []
agent_options: {}
controls: {}
software:
  packages:
    - path: ../software/windows/dup-app/dup-app.yml
      self_service: true
    - path: ../software/windows/dup-app/dup-app.yml
      self_service: false
team_settings: {}
`
	os.WriteFile(filepath.Join(teamsDir, "workstations.yml"), []byte(teamYAML), 0o644)

	repo, err := ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	if len(repo.Teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(repo.Teams))
	}

	if len(repo.Teams[0].Software.Packages) != 1 {
		t.Fatalf("expected 1 package after duplicate filtering, got %d", len(repo.Teams[0].Software.Packages))
	}

	foundDupErr := false
	for _, e := range repo.Errors {
		if strings.Contains(e.Message, "duplicate software package reference") {
			foundDupErr = true
			break
		}
	}
	if !foundDupErr {
		t.Fatalf("expected duplicate software package error, got: %+v", repo.Errors)
	}
}

// TestParseRepoFleetsDir verifies the new canonical directory name (fleets/)
// produced by Fleet's `fleetctl new` since the teams->fleets migration.
func TestParseRepoFleetsDir(t *testing.T) {
	root := t.TempDir()
	fleetsDir := filepath.Join(root, "fleets")
	if err := os.MkdirAll(fleetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	teamYAML := `name: Workstations
policies: []
queries: []
software:
  packages: []
team_settings: {}
`
	if err := os.WriteFile(filepath.Join(fleetsDir, "workstations.yml"), []byte(teamYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	repo, err := ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	if len(repo.Teams) != 1 {
		t.Fatalf("expected 1 team from fleets/, got %d (errors: %v)", len(repo.Teams), repo.Errors)
	}
	if repo.Teams[0].Name != "Workstations" {
		t.Errorf("team name = %q, want %q", repo.Teams[0].Name, "Workstations")
	}
}

// TestParseRepoFleetsTakesPriorityOverTeams verifies fleets/ wins when both
// directories exist (defensive behavior for repos mid-migration).
func TestParseRepoFleetsTakesPriorityOverTeams(t *testing.T) {
	root := t.TempDir()
	fleetsDir := filepath.Join(root, "fleets")
	teamsDir := filepath.Join(root, "teams")
	if err := os.MkdirAll(fleetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(teamsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(fleetsDir, "ws.yml", "name: FromFleets\npolicies: []\nqueries: []\nsoftware:\n  packages: []\nteam_settings: {}\n")
	write(teamsDir, "ws.yml", "name: FromTeams\npolicies: []\nqueries: []\nsoftware:\n  packages: []\nteam_settings: {}\n")

	repo, err := ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	if len(repo.Teams) != 1 || repo.Teams[0].Name != "FromFleets" {
		t.Fatalf("expected single team named FromFleets, got %d teams: %+v", len(repo.Teams), repo.Teams)
	}
}

// TestParseRepoAppleSettings exercises controls.apple_settings.configuration_profiles,
// the modern unified block that replaced macos_settings.custom_settings. Each
// referenced file should produce a ParsedProfile with platform inferred from
// the path (macos/ → darwin, ipados/ → ipados, ios/ → ios).
func TestParseRepoAppleSettings(t *testing.T) {
	root := t.TempDir()
	fleetsDir := filepath.Join(root, "fleets")
	macosDir := filepath.Join(root, "lib", "macos", "profiles")
	ipadosDir := filepath.Join(root, "lib", "ipados", "profiles")
	iosDir := filepath.Join(root, "lib", "ios", "profiles")
	for _, d := range []string{fleetsDir, macosDir, ipadosDir, iosDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// .mobileconfig with a PayloadDisplayName so extractProfileName succeeds.
	mobileconfig := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>PayloadDisplayName</key>
  <string>Mac Energy Saver</string>
  <key>PayloadType</key>
  <string>Configuration</string>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(macosDir, "energy.mobileconfig"), []byte(mobileconfig), 0o644); err != nil {
		t.Fatal(err)
	}
	// DDM .json — name comes from filename.
	if err := os.WriteFile(filepath.Join(ipadosDir, "ipad-force-os.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iosDir, "ios-restrictions.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	teamYAML := `name: Mixed Apple
controls:
  apple_settings:
    configuration_profiles:
      - path: ../lib/macos/profiles/energy.mobileconfig
        labels_include_any: [mac-fleet]
      - path: ../lib/ipados/profiles/ipad-force-os.json
        labels_include_any: [ipad-fleet]
      - path: ../lib/ios/profiles/ios-restrictions.json
team_settings: {}
`
	if err := os.WriteFile(filepath.Join(fleetsDir, "mixed.yml"), []byte(teamYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	repo, err := ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	if len(repo.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", repo.Errors)
	}
	if len(repo.Teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(repo.Teams))
	}
	got := map[string]string{}
	for _, p := range repo.Teams[0].Profiles {
		got[p.Name] = p.Platform
	}
	want := map[string]string{
		"Mac Energy Saver": "darwin",
		"ipad-force-os":    "ipados",
		"ios-restrictions": "ios",
	}
	for name, plat := range want {
		if got[name] != plat {
			t.Errorf("profile %q: platform = %q, want %q (full map: %v)", name, got[name], plat, got)
		}
	}
}

// TestParseRepoAcceptsSettingsKey covers the top-level `settings:` block,
// which current Fleet GitOps yaml includes but fleet-plan originally
// rejected as unknown. It is parsed opaquely (no field-level diff yet).
func TestParseRepoAcceptsSettingsKey(t *testing.T) {
	root := t.TempDir()
	fleetsDir := filepath.Join(root, "fleets")
	if err := os.MkdirAll(fleetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	teamYAML := `name: T1
team_settings: {}
settings:
  host_expiry_settings:
    host_expiry_enabled: false
`
	if err := os.WriteFile(filepath.Join(fleetsDir, "t1.yml"), []byte(teamYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	for _, e := range repo.Errors {
		if strings.Contains(e.Message, "unknown top-level key") {
			t.Errorf("unexpected unknown-key error: %v", e)
		}
	}
}

// TestParseRepoReportsAliasesQueries verifies that `reports:` (the renamed
// `queries:` from fleetdm/fleet#40726) is parsed as path refs and merged
// into the team's query list. Without this, queries defined under `reports:`
// don't appear on the proposed side and Fleet's live queries get reported
// as REMOVED.
func TestParseRepoReportsAliasesQueries(t *testing.T) {
	root := t.TempDir()
	fleetsDir := filepath.Join(root, "fleets")
	queriesDir := filepath.Join(root, "lib", "queries")
	for _, d := range []string{fleetsDir, queriesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A query file in the standard Fleet GitOps list-of-queries format.
	queryYAML := `- name: My Report Query
  query: SELECT 1;
  platform: darwin
`
	if err := os.WriteFile(filepath.Join(queriesDir, "rpt.yml"), []byte(queryYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	teamYAML := `name: T1
team_settings: {}
reports:
  - path: ../lib/queries/rpt.yml
`
	if err := os.WriteFile(filepath.Join(fleetsDir, "t1.yml"), []byte(teamYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	if len(repo.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", repo.Errors)
	}
	if len(repo.Teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(repo.Teams))
	}
	if len(repo.Teams[0].Queries) != 1 || repo.Teams[0].Queries[0].Name != "My Report Query" {
		t.Errorf("expected 1 query named %q from reports:, got %+v", "My Report Query", repo.Teams[0].Queries)
	}
}

// TestParseRepoWindowsConfigurationProfiles verifies that
// controls.windows_settings.configuration_profiles is parsed. This is the
// modern key (mirroring apple_settings.configuration_profiles) that fleetctl
// gitops accepts alongside the older windows_settings.custom_settings. Without
// it, Windows profiles defined under configuration_profiles never appear on the
// proposed side and every matching live Fleet profile is reported as REMOVED.
func TestParseRepoWindowsConfigurationProfiles(t *testing.T) {
	root := t.TempDir()
	fleetsDir := filepath.Join(root, "fleets")
	winDir := filepath.Join(root, "lib", "windows", "configuration-profiles")
	for _, d := range []string{fleetsDir, winDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Windows .xml profile — Fleet identifies it by filename, not content.
	if err := os.WriteFile(filepath.Join(winDir, "campus-wifi-8021x.xml"), []byte(`<Replace></Replace>`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Label-scoped entry to mirror the real-world pilot config.
	teamYAML := `name: T1
team_settings: {}
controls:
  windows_settings:
    configuration_profiles:
      - path: ../lib/windows/configuration-profiles/campus-wifi-8021x.xml
        labels_include_any:
          - test-pilots
`
	if err := os.WriteFile(filepath.Join(fleetsDir, "t1.yml"), []byte(teamYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	if len(repo.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", repo.Errors)
	}
	if len(repo.Teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(repo.Teams))
	}
	var got []string
	for _, p := range repo.Teams[0].Profiles {
		got = append(got, p.Name+"|"+p.Platform)
	}
	want := "campus-wifi-8021x|windows"
	found := false
	for _, g := range got {
		if g == want {
			found = true
		}
	}
	if !found {
		t.Errorf("windows_settings.configuration_profiles not parsed: want %q in %v", want, got)
	}
}

// TestParseSoftwarePackageListForm verifies that a package YAML file written as
// a single-element list (- url: ...) parses identically to the single-object
// form (url: ...). fleetctl gitops accepts both, but resolveSoftwareRef
// originally only unmarshaled the object form; the list form failed to parse,
// dropping the package from the proposed side and reporting it as REMOVED.
func TestParseSoftwarePackageListForm(t *testing.T) {
	root := t.TempDir()
	teamsDir := filepath.Join(root, "teams")
	softwareDir := filepath.Join(root, "software", "windows", "list-app")
	if err := os.MkdirAll(teamsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(softwareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// List form: a one-element YAML sequence rather than a top-level map.
	pkgYAML := `- url: https://downloads.example.com/list-app.exe
  display_name: List App
  self_service: true
`
	if err := os.WriteFile(filepath.Join(softwareDir, "list-app.yml"), []byte(pkgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	teamYAML := `name: Workstations
team_settings: {}
software:
  packages:
    - path: ../software/windows/list-app/list-app.yml
      labels_include_any:
        - test-pilots
`
	if err := os.WriteFile(filepath.Join(teamsDir, "workstations.yml"), []byte(teamYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	if len(repo.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", repo.Errors)
	}
	if len(repo.Teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(repo.Teams))
	}
	pkgs := repo.Teams[0].Software.Packages
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package from list-form file, got %d: %+v", len(pkgs), pkgs)
	}
	if pkgs[0].URL != "https://downloads.example.com/list-app.exe" {
		t.Errorf("package URL = %q, want list-app.exe URL", pkgs[0].URL)
	}
	if !pkgs[0].SelfService {
		t.Errorf("self_service from list-form package not parsed (want true)")
	}
}

// TestParseSoftwarePackageMultiItemListForm verifies that a package YAML file
// written as a multi-element list yields one package per entry. Keeping only
// the first entry (list[0]) would silently drop later packages and report them
// as REMOVED.
func TestParseSoftwarePackageMultiItemListForm(t *testing.T) {
	root := t.TempDir()
	teamsDir := filepath.Join(root, "teams")
	softwareDir := filepath.Join(root, "software", "windows", "multi-app")
	if err := os.MkdirAll(teamsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(softwareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two-element YAML sequence in a single package file.
	pkgYAML := `- url: https://downloads.example.com/app-one.exe
  display_name: App One
  self_service: true
- url: https://downloads.example.com/app-two.exe
  display_name: App Two
`
	if err := os.WriteFile(filepath.Join(softwareDir, "multi-app.yml"), []byte(pkgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	teamYAML := `name: Workstations
team_settings: {}
software:
  packages:
    - path: ../software/windows/multi-app/multi-app.yml
`
	if err := os.WriteFile(filepath.Join(teamsDir, "workstations.yml"), []byte(teamYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := ParseRepo(root, nil, "")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	if len(repo.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", repo.Errors)
	}
	if len(repo.Teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(repo.Teams))
	}
	pkgs := repo.Teams[0].Software.Packages
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages from multi-item list file, got %d: %+v", len(pkgs), pkgs)
	}
	gotURLs := map[string]bool{}
	for _, p := range pkgs {
		gotURLs[p.URL] = true
	}
	for _, want := range []string{
		"https://downloads.example.com/app-one.exe",
		"https://downloads.example.com/app-two.exe",
	} {
		if !gotURLs[want] {
			t.Errorf("missing package URL %q; got %v", want, gotURLs)
		}
	}
}

func TestIsNoTeam(t *testing.T) {
	tests := []struct {
		name       string
		teamName   string
		sourceFile string
		want       bool
	}{
		{"teams layout name", "No team", "teams/no-team.yml", true},
		{"teams layout name, lowercase", "no team", "teams/no-team.yml", true},
		{"fleets layout name", "Unassigned", "fleets/unassigned.yml", true},
		{"filename fallback", "hosts with no team", "fleets/unassigned.yml", true},
		{"filename fallback, teams layout", "whatever", "teams/no-team.yml", true},
		{"real team", "💻 Workstations", "fleets/workstations.yml", false},
		{"team merely mentioning unassigned", "Unassigned Laptops", "fleets/spares.yml", false},
		{"no source file", "Engineering", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNoTeam(tt.teamName, tt.sourceFile); got != tt.want {
				t.Errorf("IsNoTeam(%q, %q) = %v, want %v", tt.teamName, tt.sourceFile, got, tt.want)
			}
		})
	}
}
