package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testClient creates a Client pointing at the test server.
// Sets FLEET_PLAN_INSECURE=1 since httptest uses http://.
func testClient(t *testing.T, ts *httptest.Server, token string) *Client {
	t.Helper()
	t.Setenv("FLEET_PLAN_INSECURE", "1")
	c, err := NewClient(ts.URL, token)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// ---------- NewClient ----------

func TestNewClient(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		token    string
		insecure string // FLEET_PLAN_INSECURE env var
		wantErr  bool
		wantURL  string
	}{
		{name: "https URL accepted", url: "https://fleet.example.com", token: "tok", wantURL: "https://fleet.example.com"},
		{name: "trailing slash stripped", url: "https://fleet.example.com/", token: "tok", wantURL: "https://fleet.example.com"},
		{name: "multiple trailing slashes stripped", url: "https://fleet.example.com///", token: "tok", wantURL: "https://fleet.example.com"},
		{name: "http rejected by default", url: "http://insecure.example.com", token: "tok", wantErr: true},
		{name: "http allowed with insecure flag", url: "http://insecure.example.com", token: "tok", insecure: "1", wantURL: "http://insecure.example.com"},
		{name: "HTTP uppercase rejected", url: "HTTP://insecure.example.com", token: "tok", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.insecure != "" {
				t.Setenv("FLEET_PLAN_INSECURE", tt.insecure)
			} else {
				t.Setenv("FLEET_PLAN_INSECURE", "")
			}

			c, err := NewClient(tt.url, tt.token)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.baseURL != tt.wantURL {
				t.Errorf("baseURL: got %q, want %q", c.baseURL, tt.wantURL)
			}
		})
	}
}

// ---------- HTTP error handling ----------

func TestHTTPErrorCodes(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantStatus int
	}{
		{name: "401 unauthorized", statusCode: 401, body: `{"message":"Unauthorized"}`, wantStatus: 401},
		{name: "403 forbidden", statusCode: 403, body: `{"message":"Forbidden"}`, wantStatus: 403},
		{name: "404 not found", statusCode: 404, body: `{"message":"Not found"}`, wantStatus: 404},
		{name: "500 server error", statusCode: 500, body: `{"message":"Internal error"}`, wantStatus: 500},
		{name: "429 rate limited", statusCode: 429, body: `{"message":"Too many requests"}`, wantStatus: 429},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.body))
			}))
			defer ts.Close()

			c := testClient(t, ts, "tok")
			_, err := c.GetTeams(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}

			if httpErr, ok := err.(*HTTPError); ok {
				if httpErr.StatusCode != tt.wantStatus {
					t.Errorf("status: got %d, want %d", httpErr.StatusCode, tt.wantStatus)
				}
			}
		})
	}
}

// ---------- isPermissionError ----------

func TestIsPermissionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "non-HTTP error", err: fmt.Errorf("network down"), want: false},
		{name: "403", err: &HTTPError{StatusCode: 403}, want: true},
		{name: "404", err: &HTTPError{StatusCode: 404}, want: true},
		{name: "500", err: &HTTPError{StatusCode: 500}, want: false},
		{name: "wrapped 403", err: fmt.Errorf("outer: %w", &HTTPError{StatusCode: 403}), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPermissionError(tt.err); got != tt.want {
				t.Errorf("isPermissionError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------- GetTeams ----------

func TestGetTeams(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer testtoken" {
			t.Errorf("auth header: %q", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(teamsResponse{
			Teams: []Team{
				{ID: 1, Name: "Workstations"},
				{ID: 2, Name: "Infrastructure"},
			},
		})
	}))
	defer ts.Close()

	c := testClient(t, ts, "testtoken")
	teams, err := c.GetTeams(context.Background())
	if err != nil {
		t.Fatalf("GetTeams: %v", err)
	}
	if len(teams) != 2 {
		t.Fatalf("expected 2 teams, got %d", len(teams))
	}
	if teams[0].Name != "Workstations" {
		t.Errorf("first team: got %q", teams[0].Name)
	}
}

func TestGetTeamsCapturesRawSettings(t *testing.T) {
	// The settings blocks a team YAML configures live on the team object
	// itself; the client keeps the raw object so they can be diffed.
	const body = `{"teams":[{
		"id": 1,
		"name": "Workstations",
		"host_expiry_settings": {"host_expiry_enabled": true, "host_expiry_window": 30},
		"webhook_settings": {"failing_policies_webhook": {"destination_url": "https://example.com/hook"}},
		"features": {"enable_software_inventory": true}
	}]}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer ts.Close()

	teams, err := testClient(t, ts, "testtoken").GetTeams(context.Background())
	if err != nil {
		t.Fatalf("GetTeams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("got %d teams, want 1", len(teams))
	}

	// The typed fields still decode.
	if teams[0].ID != 1 || teams[0].Name != "Workstations" {
		t.Errorf("typed fields: got id=%d name=%q", teams[0].ID, teams[0].Name)
	}

	hes, ok := teams[0].Settings["host_expiry_settings"].(map[string]any)
	if !ok {
		t.Fatalf("Settings missing host_expiry_settings: %+v", teams[0].Settings)
	}
	if hes["host_expiry_enabled"] != true {
		t.Errorf("host_expiry_enabled: got %v, want true", hes["host_expiry_enabled"])
	}
	if _, ok := teams[0].Settings["features"]; !ok {
		t.Errorf("Settings missing features: %+v", teams[0].Settings)
	}
}

func TestTeamUnmarshalJSONNonObject(t *testing.T) {
	// A team that is not a JSON object should surface as an error rather than
	// panicking or silently producing a half-decoded team.
	var team Team
	if err := json.Unmarshal([]byte(`"not-an-object"`), &team); err == nil {
		t.Fatal("expected an error unmarshalling a non-object team")
	}
}

// ---------- GetPolicies ----------

func TestGetPoliciesPathRouting(t *testing.T) {
	tests := []struct {
		name     string
		teamID   uint
		wantPath string
	}{
		{name: "global policies", teamID: 0, wantPath: "/api/v1/fleet/global/policies"},
		{name: "team 1 policies", teamID: 1, wantPath: "/api/v1/fleet/teams/1/policies"},
		{name: "team 42 policies", teamID: 42, wantPath: "/api/v1/fleet/teams/42/policies"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				json.NewEncoder(w).Encode(policiesResponse{Policies: []Policy{}})
			}))
			defer ts.Close()

			c := testClient(t, ts, "tok")
			_, err := c.GetPolicies(context.Background(), tt.teamID)
			if err != nil {
				t.Fatalf("GetPolicies: %v", err)
			}
			if gotPath != tt.wantPath {
				t.Errorf("path: got %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

func TestGetPoliciesPageParams(t *testing.T) {
	tests := []struct {
		name   string
		teamID uint
	}{
		{name: "global policies send page params", teamID: 0},
		{name: "team policies send page params", teamID: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("page") == "" {
					t.Errorf("expected page param, got none")
				}
				if r.URL.Query().Get("per_page") == "" {
					t.Errorf("expected per_page param, got none")
				}
				json.NewEncoder(w).Encode(policiesResponse{Policies: []Policy{
					{ID: 1, Name: "A Policy"},
				}})
			}))
			defer ts.Close()

			c := testClient(t, ts, "tok")
			policies, err := c.GetPolicies(context.Background(), tt.teamID)
			if err != nil {
				t.Fatalf("GetPolicies: %v", err)
			}
			if len(policies) != 1 {
				t.Fatalf("expected 1 policy, got %d", len(policies))
			}
		})
	}
}

func TestGetPoliciesFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(policiesResponse{
			Policies: []Policy{
				{ID: 10, Name: "Disk Encryption", Platform: "darwin", Critical: true},
			},
		})
	}))
	defer ts.Close()

	c := testClient(t, ts, "tok")
	policies, err := c.GetPolicies(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetPolicies: %v", err)
	}
	if len(policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(policies))
	}
	if policies[0].Name != "Disk Encryption" {
		t.Errorf("policy name: got %q", policies[0].Name)
	}
	if !policies[0].Critical {
		t.Error("policy should be critical")
	}
}

// ---------- GetLabels ----------

func TestGetLabels(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(labelsResponse{
			Labels: []Label{
				{ID: 1, Name: "Managed Devices", HostCount: 24},
				{ID: 2, Name: "macOS 15+", HostCount: 120},
			},
		})
	}))
	defer ts.Close()

	c := testClient(t, ts, "tok")
	labels, err := c.GetLabels(context.Background())
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(labels))
	}
	if labels[0].HostCount != 24 {
		t.Errorf("host count: got %d", labels[0].HostCount)
	}
}

// ---------- GetQueries ----------

func TestGetQueries(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		teamID := r.URL.Query().Get("team_id")
		if teamID != "5" {
			t.Errorf("expected team_id=5, got %q", teamID)
		}
		json.NewEncoder(w).Encode(queriesResponse{
			Queries: []Query{
				{ID: 1, Name: "Disk Usage", Interval: 3600, Platform: "darwin"},
			},
		})
	}))
	defer ts.Close()

	c := testClient(t, ts, "tok")
	queries, err := c.GetQueries(context.Background(), 5)
	if err != nil {
		t.Fatalf("GetQueries: %v", err)
	}
	if len(queries) != 1 {
		t.Fatalf("expected 1 query, got %d", len(queries))
	}
	if queries[0].Interval != 3600 {
		t.Errorf("interval: got %d", queries[0].Interval)
	}
}

// ---------- GetProfiles ----------

func TestGetProfilesTeamID(t *testing.T) {
	tests := []struct {
		name       string
		teamID     uint
		wantTeamID string
	}{
		// teamID 0 is Fleet's "hosts on no team" bucket, so the parameter
		// must be sent explicitly. Omitting it would return every team's
		// profiles instead.
		{name: "no-team profiles", teamID: 0, wantTeamID: "0"},
		{name: "team 5 profiles", teamID: 5, wantTeamID: "5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotTeamID string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotTeamID = r.URL.Query().Get("team_id")
				json.NewEncoder(w).Encode(profilesResponse{Profiles: []Profile{}})
			}))
			defer ts.Close()

			c := testClient(t, ts, "tok")
			_, err := c.GetProfiles(context.Background(), tt.teamID)
			if err != nil {
				t.Fatalf("GetProfiles: %v", err)
			}
			if gotTeamID != tt.wantTeamID {
				t.Errorf("team_id param: got %q, want %q", gotTeamID, tt.wantTeamID)
			}
		})
	}
}

// ---------- GetSoftware pagination ----------

func TestGetSoftwarePagination(t *testing.T) {
	tests := []struct {
		name      string
		pages     int
		perPage   int
		wantTotal int
	}{
		{name: "single page", pages: 1, perPage: 3, wantTotal: 3},
		{name: "two pages", pages: 2, perPage: 2, wantTotal: 4},
		{name: "empty result", pages: 1, perPage: 0, wantTotal: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pageCount := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pageCount++
				var titles []SoftwareTitle
				if tt.perPage > 0 && pageCount <= tt.pages {
					for i := 0; i < tt.perPage; i++ {
						titles = append(titles, SoftwareTitle{ID: uint(pageCount*100 + i), Name: "App"})
					}
				}
				resp := softwareResponse{SoftwareTitles: titles}
				resp.Meta.HasNextResults = pageCount < tt.pages
				json.NewEncoder(w).Encode(resp)
			}))
			defer ts.Close()

			c := testClient(t, ts, "tok")
			titles, err := c.GetSoftware(context.Background(), 1)
			if err != nil {
				t.Fatalf("GetSoftware: %v", err)
			}
			if len(titles) != tt.wantTotal {
				t.Errorf("total: got %d, want %d", len(titles), tt.wantTotal)
			}
		})
	}
}

// ---------- GetFleetMaintainedApps pagination ----------

func TestGetFleetMaintainedAppsPagination(t *testing.T) {
	tests := []struct {
		name      string
		pages     int
		perPage   int
		wantTotal int
	}{
		{name: "single page", pages: 1, perPage: 5, wantTotal: 5},
		{name: "three pages", pages: 3, perPage: 2, wantTotal: 6},
		{name: "empty catalog", pages: 1, perPage: 0, wantTotal: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pageCount := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pageCount++
				var apps []FleetMaintainedApp
				if tt.perPage > 0 && pageCount <= tt.pages {
					for i := 0; i < tt.perPage; i++ {
						apps = append(apps, FleetMaintainedApp{ID: uint(pageCount*100 + i), Slug: "app/darwin"})
					}
				}
				resp := fleetMaintainedAppsResponse{FleetMaintainedApps: apps}
				resp.Meta.HasNextResults = pageCount < tt.pages
				json.NewEncoder(w).Encode(resp)
			}))
			defer ts.Close()

			c := testClient(t, ts, "tok")
			apps, err := c.GetFleetMaintainedApps(context.Background())
			if err != nil {
				t.Fatalf("GetFleetMaintainedApps: %v", err)
			}
			if len(apps) != tt.wantTotal {
				t.Errorf("total: got %d, want %d", len(apps), tt.wantTotal)
			}
		})
	}
}

// ---------- Authorization header ----------

func TestAuthorizationHeader(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{name: "standard token", token: "abc123", want: "Bearer abc123"},
		{name: "empty token", token: "", want: "Bearer"},
		{name: "token with spaces", token: "my token", want: "Bearer my token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuth string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				json.NewEncoder(w).Encode(labelsResponse{Labels: []Label{}})
			}))
			defer ts.Close()

			c := testClient(t, ts, tt.token)
			_, _ = c.GetLabels(context.Background())

			if gotAuth != tt.want {
				t.Errorf("auth header: got %q, want %q", gotAuth, tt.want)
			}
		})
	}
}

// ---------- FetchAll ----------

func TestFetchAll(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/fleet/teams", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(teamsResponse{Teams: []Team{{ID: 1, Name: "Test Team"}}})
	})
	mux.HandleFunc("/api/v1/fleet/labels", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(labelsResponse{Labels: []Label{{ID: 1, Name: "Test Label"}}})
	})
	mux.HandleFunc("/api/v1/fleet/teams/1/policies", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(policiesResponse{Policies: []Policy{{ID: 1, Name: "Test Policy"}}})
	})
	mux.HandleFunc("/api/v1/fleet/queries", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(queriesResponse{})
	})
	mux.HandleFunc("/api/v1/fleet/software/titles", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(softwareResponse{})
	})
	mux.HandleFunc("/api/v1/fleet/software/fleet_maintained_apps", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fleetMaintainedAppsResponse{})
	})
	mux.HandleFunc("/api/v1/fleet/configuration_profiles", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(profilesResponse{})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := testClient(t, ts, "tok")
	state, err := c.FetchAll(context.Background())
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if len(state.Teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(state.Teams))
	}
	if len(state.Teams[0].Policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(state.Teams[0].Policies))
	}
	if len(state.Labels) != 1 {
		t.Fatalf("expected 1 label, got %d", len(state.Labels))
	}
}

// ---------- FetchAll with fetchGlobal ----------

func TestFetchAllWithGlobal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/fleet/teams", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(teamsResponse{Teams: []Team{{ID: 1, Name: "T"}}})
	})
	mux.HandleFunc("/api/v1/fleet/labels", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(labelsResponse{})
	})
	mux.HandleFunc("/api/v1/fleet/software/fleet_maintained_apps", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fleetMaintainedAppsResponse{})
	})
	mux.HandleFunc("/api/v1/fleet/config", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"org_info": map[string]any{"org_name": "Test"}})
	})
	mux.HandleFunc("/api/v1/fleet/global/policies", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(policiesResponse{Policies: []Policy{{ID: 100, Name: "Global Policy"}}})
	})
	mux.HandleFunc("/api/v1/fleet/teams/1/policies", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(policiesResponse{})
	})
	mux.HandleFunc("/api/v1/fleet/queries", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(queriesResponse{Queries: []Query{{ID: 200, Name: "Global Query"}}})
	})
	mux.HandleFunc("/api/v1/fleet/software/titles", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(softwareResponse{})
	})
	mux.HandleFunc("/api/v1/fleet/configuration_profiles", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(profilesResponse{})
	})
	mux.HandleFunc("/api/v1/fleet/scripts", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(scriptsResponse{})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := testClient(t, ts, "tok")
	state, err := c.FetchAll(context.Background(), FetchOptions{Global: true})
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if state.Config == nil {
		t.Error("expected Config to be populated")
	}
	if len(state.GlobalPolicies) != 1 {
		t.Errorf("expected 1 global policy, got %d", len(state.GlobalPolicies))
	}
	if len(state.GlobalQueries) != 1 {
		t.Errorf("expected 1 global query, got %d", len(state.GlobalQueries))
	}
}

// ---------- GetSoftwareTitleDetail ----------

func TestGetSoftwareTitleDetail(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/fleet/software/titles/42" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("team_id") != "5" {
			t.Errorf("expected team_id=5, got %q", r.URL.Query().Get("team_id"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"software_title": map[string]any{
				"id":   42,
				"name": "7-Zip",
				"software_package": map[string]any{
					"install_script":   "choco install 7zip",
					"uninstall_script": "choco uninstall 7zip",
					"platform":         "windows",
					"self_service":     true,
				},
			},
		})
	}))
	defer ts.Close()

	c := testClient(t, ts, "tok")
	detail, err := c.GetSoftwareTitleDetail(context.Background(), 42, 5)
	if err != nil {
		t.Fatalf("GetSoftwareTitleDetail: %v", err)
	}
	if detail.Name != "7-Zip" {
		t.Errorf("name: got %q, want %q", detail.Name, "7-Zip")
	}
	if detail.SoftwarePackage == nil {
		t.Fatal("expected SoftwarePackage to be populated")
	}
	if detail.SoftwarePackage.InstallScript != "choco install 7zip" {
		t.Errorf("install_script: got %q", detail.SoftwarePackage.InstallScript)
	}
}

// ---------- FetchAll permission fallback tests ----------

func TestFetchAllSoftware403(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantNil bool
		wantErr bool
	}{
		{name: "200 returns software", status: 200},
		{name: "403 gracefully returns nil", status: 403, wantNil: true},
		{name: "500 returns error", status: 500, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/fleet/teams", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(teamsResponse{Teams: []Team{{ID: 1, Name: "T"}}})
			})
			mux.HandleFunc("/api/v1/fleet/labels", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(labelsResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/software/fleet_maintained_apps", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(fleetMaintainedAppsResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/teams/1/policies", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(policiesResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/queries", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(queriesResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/software/titles", func(w http.ResponseWriter, r *http.Request) {
				if tt.status != 200 {
					w.WriteHeader(tt.status)
					w.Write([]byte(`{"message":"forbidden"}`))
					return
				}
				json.NewEncoder(w).Encode(softwareResponse{
					SoftwareTitles: []SoftwareTitle{{ID: 1, Name: "Chrome"}},
				})
			})
			mux.HandleFunc("/api/v1/fleet/configuration_profiles", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(profilesResponse{})
			})

			ts := httptest.NewServer(mux)
			defer ts.Close()

			c := testClient(t, ts, "tok")
			state, err := c.FetchAll(context.Background())

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("FetchAll: %v", err)
			}
			if tt.wantNil && state.Teams[0].SoftwareTitles != nil {
				t.Errorf("expected nil SoftwareTitles, got %v", state.Teams[0].SoftwareTitles)
			}
			if !tt.wantNil && len(state.Teams[0].SoftwareTitles) != 1 {
				t.Errorf("expected 1 software title, got %d", len(state.Teams[0].SoftwareTitles))
			}
		})
	}
}

func TestFetchAllProfiles403(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantNil bool
		wantErr bool
	}{
		{name: "200 returns profiles", status: 200},
		{name: "403 gracefully returns nil", status: 403, wantNil: true},
		{name: "500 returns error", status: 500, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/fleet/teams", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(teamsResponse{Teams: []Team{{ID: 1, Name: "T"}}})
			})
			mux.HandleFunc("/api/v1/fleet/labels", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(labelsResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/software/fleet_maintained_apps", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(fleetMaintainedAppsResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/teams/1/policies", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(policiesResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/queries", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(queriesResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/software/titles", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(softwareResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/configuration_profiles", func(w http.ResponseWriter, r *http.Request) {
				if tt.status != 200 {
					w.WriteHeader(tt.status)
					w.Write([]byte(`{"message":"forbidden"}`))
					return
				}
				json.NewEncoder(w).Encode(profilesResponse{
					Profiles: []Profile{{ProfileUUID: "p1", Name: "WiFi"}},
				})
			})

			ts := httptest.NewServer(mux)
			defer ts.Close()

			c := testClient(t, ts, "tok")
			state, err := c.FetchAll(context.Background())

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("FetchAll: %v", err)
			}
			if tt.wantNil && state.Teams[0].Profiles != nil {
				t.Errorf("expected nil Profiles, got %v", state.Teams[0].Profiles)
			}
			if !tt.wantNil && len(state.Teams[0].Profiles) != 1 {
				t.Errorf("expected 1 profile, got %d", len(state.Teams[0].Profiles))
			}
		})
	}
}

// ---------- GetConfig ----------

func TestGetConfig(t *testing.T) {
	tests := []struct {
		name       string
		response   map[string]any
		statusCode int
		wantErr    bool
		wantKey    string
		wantValue  any
	}{
		{
			name: "parses org_info",
			response: map[string]any{
				"org_info":      map[string]any{"org_name": "NVIDIA"},
				"agent_options": map[string]any{"config": map[string]any{"interval": 300}},
			},
			statusCode: 200,
			wantKey:    "org_info",
		},
		{
			name:       "server error returns error",
			statusCode: 500,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/fleet/config" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if tt.statusCode != 200 {
					w.WriteHeader(tt.statusCode)
					w.Write([]byte(`{"message":"error"}`))
					return
				}
				json.NewEncoder(w).Encode(tt.response)
			}))
			defer ts.Close()

			c := testClient(t, ts, "tok")
			cfg, err := c.GetConfig(context.Background())
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetConfig: %v", err)
			}
			if tt.wantKey != "" {
				if _, ok := cfg[tt.wantKey]; !ok {
					t.Errorf("expected key %q in config, got keys: %v", tt.wantKey, keys(cfg))
				}
			}
		})
	}
}

func keys(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// ---------- GetScripts ----------

func TestGetScripts(t *testing.T) {
	tests := []struct {
		name      string
		teamID    uint
		scripts   []Script
		wantCount int
	}{
		{
			name:   "returns scripts for team",
			teamID: 5,
			scripts: []Script{
				{ID: 1, Name: "setup.ps1", TeamID: 5},
				{ID: 2, Name: "cleanup.sh", TeamID: 5},
			},
			wantCount: 2,
		},
		{
			name:      "empty result",
			teamID:    99,
			scripts:   []Script{},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/fleet/scripts" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if tt.teamID > 0 {
					gotTeamID := r.URL.Query().Get("team_id")
					if gotTeamID == "" {
						t.Error("expected team_id query param")
					}
				}
				resp := scriptsResponse{Scripts: tt.scripts}
				resp.Meta.HasNextResults = false
				json.NewEncoder(w).Encode(resp)
			}))
			defer ts.Close()

			c := testClient(t, ts, "tok")
			scripts, err := c.GetScripts(context.Background(), tt.teamID)
			if err != nil {
				t.Fatalf("GetScripts: %v", err)
			}
			if len(scripts) != tt.wantCount {
				t.Errorf("script count: got %d, want %d", len(scripts), tt.wantCount)
			}
			for i, s := range scripts {
				if s.Name != tt.scripts[i].Name {
					t.Errorf("script[%d] name: got %q, want %q", i, s.Name, tt.scripts[i].Name)
				}
			}
		})
	}
}

func TestGetScriptsPagination(t *testing.T) {
	pageCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCount++
		var scripts []Script
		if pageCount <= 2 {
			for i := 0; i < 3; i++ {
				scripts = append(scripts, Script{
					ID:   uint(pageCount*100 + i),
					Name: fmt.Sprintf("script-%d.sh", pageCount*100+i),
				})
			}
		}
		resp := scriptsResponse{Scripts: scripts}
		resp.Meta.HasNextResults = pageCount < 2
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	c := testClient(t, ts, "tok")
	scripts, err := c.GetScripts(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetScripts: %v", err)
	}
	if len(scripts) != 6 {
		t.Errorf("expected 6 scripts across 2 pages, got %d", len(scripts))
	}
}

// ---------- FetchAll scripts fallback ----------

func TestFetchAllScripts403(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantNil bool
		wantErr bool
	}{
		{name: "200 returns scripts", status: 200},
		{name: "403 gracefully returns nil", status: 403, wantNil: true},
		{name: "500 returns error", status: 500, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/fleet/teams", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(teamsResponse{Teams: []Team{{ID: 1, Name: "T"}}})
			})
			mux.HandleFunc("/api/v1/fleet/labels", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(labelsResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/software/fleet_maintained_apps", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(fleetMaintainedAppsResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/teams/1/policies", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(policiesResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/queries", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(queriesResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/software/titles", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(softwareResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/configuration_profiles", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(profilesResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/scripts", func(w http.ResponseWriter, r *http.Request) {
				if tt.status != 200 {
					w.WriteHeader(tt.status)
					w.Write([]byte(`{"message":"forbidden"}`))
					return
				}
				json.NewEncoder(w).Encode(scriptsResponse{
					Scripts: []Script{{ID: 1, Name: "test.ps1", TeamID: 1}},
				})
			})

			ts := httptest.NewServer(mux)
			defer ts.Close()

			c := testClient(t, ts, "tok")
			state, err := c.FetchAll(context.Background())

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("FetchAll: %v", err)
			}
			if tt.wantNil && state.Teams[0].Scripts != nil {
				t.Errorf("expected nil Scripts, got %v", state.Teams[0].Scripts)
			}
			if !tt.wantNil && len(state.Teams[0].Scripts) != 1 {
				t.Errorf("expected 1 script, got %d", len(state.Teams[0].Scripts))
			}
		})
	}
}

func TestFetchAllFleetMaintainedFallback(t *testing.T) {
	tests := []struct {
		name           string
		catalogStatus  int
		wantCatalogLen int
		wantErr        bool
	}{
		{name: "200 returns catalog", catalogStatus: 200, wantCatalogLen: 1},
		{name: "404 gracefully returns empty", catalogStatus: 404, wantCatalogLen: 0},
		{name: "403 gracefully returns empty", catalogStatus: 403, wantCatalogLen: 0},
		{name: "500 returns error", catalogStatus: 500, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/fleet/teams", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(teamsResponse{Teams: []Team{{ID: 1, Name: "T"}}})
			})
			mux.HandleFunc("/api/v1/fleet/labels", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(labelsResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/software/fleet_maintained_apps", func(w http.ResponseWriter, r *http.Request) {
				if tt.catalogStatus != 200 {
					w.WriteHeader(tt.catalogStatus)
					w.Write([]byte(`{"message":"error"}`))
					return
				}
				json.NewEncoder(w).Encode(fleetMaintainedAppsResponse{
					FleetMaintainedApps: []FleetMaintainedApp{{ID: 1, Slug: "slack/darwin"}},
				})
			})
			mux.HandleFunc("/api/v1/fleet/teams/1/policies", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(policiesResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/queries", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(queriesResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/software/titles", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(softwareResponse{})
			})
			mux.HandleFunc("/api/v1/fleet/configuration_profiles", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(profilesResponse{})
			})

			ts := httptest.NewServer(mux)
			defer ts.Close()

			c := testClient(t, ts, "tok")
			state, err := c.FetchAll(context.Background())

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("FetchAll: %v", err)
			}
			if len(state.FleetMaintainedCatalog) != tt.wantCatalogLen {
				t.Errorf("catalog len: got %d, want %d", len(state.FleetMaintainedCatalog), tt.wantCatalogLen)
			}
		})
	}
}

// ---------- no-team bucket ----------

func TestGetNoTeamPolicies(t *testing.T) {
	// /teams/0/policies returns the bucket's own policies plus the global ones
	// it inherits. Only the former belong to a no-team YAML file.
	const body = `{
		"policies": [{"id": 1, "name": "No-team policy", "query": "SELECT 1;"}],
		"inherited_policies": [{"id": 99, "name": "Global policy", "query": "SELECT 2;"}]
	}`

	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, body)
	}))
	defer ts.Close()

	policies, err := testClient(t, ts, "tok").GetNoTeamPolicies(context.Background())
	if err != nil {
		t.Fatalf("GetNoTeamPolicies: %v", err)
	}
	if gotPath != "/api/v1/fleet/teams/0/policies" {
		t.Errorf("path: got %q", gotPath)
	}
	if len(policies) != 1 || policies[0].Name != "No-team policy" {
		t.Fatalf("policies: got %+v, want only the no-team policy", policies)
	}
}

func TestFetchAllNoTeam(t *testing.T) {
	var noTeamParams struct{ profiles, scripts string }
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/fleet/teams", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"teams":[]}`)
	})
	mux.HandleFunc("/api/v1/fleet/labels", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"labels":[]}`)
	})
	mux.HandleFunc("/api/v1/fleet/software/fleet_maintained_apps", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"fleet_maintained_apps":[]}`)
	})
	mux.HandleFunc("/api/v1/fleet/teams/0/policies", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"policies":[{"id":1,"name":"No-team policy"}],"inherited_policies":[{"id":9,"name":"Global"}]}`)
	})
	mux.HandleFunc("/api/v1/fleet/configuration_profiles", func(w http.ResponseWriter, r *http.Request) {
		noTeamParams.profiles = r.URL.Query().Get("team_id")
		fmt.Fprint(w, `{"profiles":[{"profile_uuid":"u1","name":"Conditional access"}]}`)
	})
	mux.HandleFunc("/api/v1/fleet/scripts", func(w http.ResponseWriter, r *http.Request) {
		noTeamParams.scripts = r.URL.Query().Get("team_id")
		fmt.Fprint(w, `{"scripts":[{"id":0,"name":"uninstall.sh"}]}`)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	t.Run("requested", func(t *testing.T) {
		state, err := testClient(t, ts, "tok").FetchAll(context.Background(), FetchOptions{NoTeam: true})
		if err != nil {
			t.Fatalf("FetchAll: %v", err)
		}
		if state.NoTeam == nil {
			t.Fatal("NoTeam: got nil, want the fetched bucket")
		}
		if len(state.NoTeam.Policies) != 1 || state.NoTeam.Policies[0].Name != "No-team policy" {
			t.Errorf("policies: got %+v", state.NoTeam.Policies)
		}
		if len(state.NoTeam.Profiles) != 1 || len(state.NoTeam.Scripts) != 1 {
			t.Errorf("profiles=%d scripts=%d, want 1 each", len(state.NoTeam.Profiles), len(state.NoTeam.Scripts))
		}
		// team_id=0 must be sent explicitly; omitting it returns every team's
		// resources instead of the no-team bucket's.
		if noTeamParams.profiles != "0" || noTeamParams.scripts != "0" {
			t.Errorf("team_id params: profiles=%q scripts=%q, want 0 for both",
				noTeamParams.profiles, noTeamParams.scripts)
		}
	})

	t.Run("not requested", func(t *testing.T) {
		state, err := testClient(t, ts, "tok").FetchAll(context.Background())
		if err != nil {
			t.Fatalf("FetchAll: %v", err)
		}
		if state.NoTeam != nil {
			t.Errorf("NoTeam: got %+v, want nil when not requested", state.NoTeam)
		}
	})
}

func TestFetchAllNoTeamPermissionErrors(t *testing.T) {
	// A gitops-scoped token may be refused on some of these endpoints. That
	// must degrade to "skipped", not fail the whole diff.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/fleet/teams", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"teams":[]}`)
	})
	mux.HandleFunc("/api/v1/fleet/labels", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"labels":[]}`)
	})
	mux.HandleFunc("/api/v1/fleet/software/fleet_maintained_apps", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"fleet_maintained_apps":[]}`)
	})
	mux.HandleFunc("/api/v1/fleet/teams/0/policies", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/api/v1/fleet/configuration_profiles", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/api/v1/fleet/scripts", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	state, err := testClient(t, ts, "tok").FetchAll(context.Background(), FetchOptions{NoTeam: true})
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if state.NoTeam == nil {
		t.Fatal("NoTeam: got nil")
	}
	if !state.NoTeam.PoliciesUnavailable || !state.NoTeam.ProfilesUnavailable || !state.NoTeam.ScriptsUnavailable {
		t.Errorf("unavailable flags: got %+v, want all true", state.NoTeam)
	}
}

func TestGetNoTeamPoliciesPagination(t *testing.T) {
	// 250 results means "there may be more"; the client keeps paging until a
	// short page comes back.
	var pages []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		n := 250
		if page != "0" {
			n = 2
		}
		policies := make([]Policy, n)
		for i := range policies {
			policies[i] = Policy{ID: uint(i + 1), Name: fmt.Sprintf("p%s-%d", page, i)}
		}
		_ = json.NewEncoder(w).Encode(policiesResponse{Policies: policies})
	}))
	defer ts.Close()

	policies, err := testClient(t, ts, "tok").GetNoTeamPolicies(context.Background())
	if err != nil {
		t.Fatalf("GetNoTeamPolicies: %v", err)
	}
	if len(policies) != 252 {
		t.Errorf("got %d policies, want 252", len(policies))
	}
	if strings.Join(pages, ",") != "0,1" {
		t.Errorf("pages requested: got %v, want [0 1]", pages)
	}
}

func TestFetchAllNoTeamFatalErrors(t *testing.T) {
	// A 403/404 degrades to "unavailable", but any other failure is a real
	// problem and must not be reported as an empty bucket.
	tests := []struct {
		name     string
		failPath string
	}{
		{"policies", "/api/v1/fleet/teams/0/policies"},
		{"profiles", "/api/v1/fleet/configuration_profiles"},
		{"scripts", "/api/v1/fleet/scripts"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/fleet/teams", func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"teams":[]}`)
			})
			mux.HandleFunc("/api/v1/fleet/labels", func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"labels":[]}`)
			})
			mux.HandleFunc("/api/v1/fleet/software/fleet_maintained_apps", func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"fleet_maintained_apps":[]}`)
			})
			// Every no-team endpoint succeeds except the one under test,
			// which returns a server error rather than a 403/404.
			ok := map[string]string{
				"/api/v1/fleet/teams/0/policies":       `{"policies":[]}`,
				"/api/v1/fleet/configuration_profiles": `{"profiles":[]}`,
				"/api/v1/fleet/scripts":                `{"scripts":[]}`,
			}
			for path, body := range ok {
				if path == tt.failPath {
					mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
						w.WriteHeader(http.StatusInternalServerError)
					})
					continue
				}
				mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
					fmt.Fprint(w, body)
				})
			}

			ts := httptest.NewServer(mux)
			defer ts.Close()

			if _, err := testClient(t, ts, "tok").FetchAll(context.Background(), FetchOptions{NoTeam: true}); err == nil {
				t.Fatalf("expected FetchAll to fail when %s returns 500", tt.name)
			}
		})
	}
}

// ---------- profile content ----------

func TestGetProfileContent(t *testing.T) {
	const body = `<?xml version="1.0"?><plist version="1.0"><dict/></plist>`

	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		fmt.Fprint(w, body)
	}))
	defer ts.Close()

	content, err := testClient(t, ts, "tok").GetProfileContent(context.Background(), "uuid-1")
	if err != nil {
		t.Fatalf("GetProfileContent: %v", err)
	}
	if content != body {
		t.Errorf("content: got %q", content)
	}
	if gotPath != "/api/v1/fleet/configuration_profiles/uuid-1" {
		t.Errorf("path: got %q", gotPath)
	}
	if gotQuery != "alt=media" {
		t.Errorf("query: got %q, want alt=media", gotQuery)
	}
}

func TestGetProfileContentErrors(t *testing.T) {
	t.Run("empty uuid", func(t *testing.T) {
		if _, err := testClient(t, httptest.NewServer(http.NotFoundHandler()), "tok").
			GetProfileContent(context.Background(), ""); err == nil {
			t.Error("expected an error for an empty UUID")
		}
	})

	t.Run("http error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer ts.Close()

		_, err := testClient(t, ts, "tok").GetProfileContent(context.Background(), "u")
		if err == nil {
			t.Fatal("expected an error")
		}
		if !isPermissionError(err) {
			t.Errorf("error should be recognized as a permission error: %v", err)
		}
	})
}

func TestEnrichProfileContents(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// One profile is readable, the other is refused.
		if strings.HasSuffix(r.URL.Path, "denied") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprint(w, "<plist version=\"1.0\"><dict/></plist>")
	}))
	defer ts.Close()

	profiles := []Profile{
		{ProfileUUID: "ok"},
		{ProfileUUID: "denied"},
		{ProfileUUID: ""}, // skipped entirely
	}
	testClient(t, ts, "tok").EnrichProfileContents(context.Background(), profiles)

	if profiles[0].Content == "" {
		t.Error("readable profile: content not populated")
	}
	// A failed download is non-fatal and leaves Content empty, so the diff can
	// fall back to name-only matching.
	if profiles[1].Content != "" {
		t.Errorf("denied profile: got content %q, want empty", profiles[1].Content)
	}
	if profiles[2].Content != "" {
		t.Errorf("uuid-less profile: got content %q, want empty", profiles[2].Content)
	}
}

func TestGetProfileContentTransportErrors(t *testing.T) {
	t.Run("unbuildable request URL", func(t *testing.T) {
		// A control character in the base URL cannot be turned into a request.
		c := &Client{baseURL: "https://example.com/\x7f", token: "tok", httpClient: &http.Client{}}
		if _, err := c.GetProfileContent(context.Background(), "u"); err == nil {
			t.Error("expected an error building the request")
		}
	})

	t.Run("connection failure", func(t *testing.T) {
		ts := httptest.NewServer(http.NotFoundHandler())
		url := ts.URL
		ts.Close() // nothing is listening any more

		t.Setenv("FLEET_PLAN_INSECURE", "1")
		c, err := NewClient(url, "tok")
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if _, err := c.GetProfileContent(context.Background(), "u"); err == nil {
			t.Error("expected a transport error")
		}
	})

	t.Run("truncated response body", func(t *testing.T) {
		// Hijack the connection so the headers promise 512 bytes, then hang up
		// after 7. The client must fail while reading rather than hand back a
		// half profile that would diff as a pile of removed keys.
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			conn, buf, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 512\r\n\r\n<plist>")
			_ = buf.Flush()
		}))
		defer ts.Close()

		if _, err := testClient(t, ts, "tok").GetProfileContent(context.Background(), "u"); err == nil {
			t.Error("expected an error reading the truncated body")
		}
	})
}

func TestGetProfileContentOversized(t *testing.T) {
	// An oversized profile must be an error rather than silently truncated
	// content, which would diff as a pile of removed keys.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxProfileContentSize+10))
	}))
	defer ts.Close()

	_, err := testClient(t, ts, "tok").GetProfileContent(context.Background(), "u")
	if err == nil {
		t.Fatal("expected an error for an oversized profile")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should say the profile is too large: %v", err)
	}
}
