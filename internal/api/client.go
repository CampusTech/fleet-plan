// Package api provides a read-only Fleet REST API client. It only performs GET
// requests and enforces this at the type level -- there are no exported methods
// that perform writes. This is intentional and permanent.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

// Client is a read-only Fleet REST API client.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient creates a new read-only Fleet API client.
// Returns an error if the URL uses plain HTTP (token would be sent in cleartext).
func NewClient(baseURL, token string) (*Client, error) {
	baseURL = strings.TrimRight(baseURL, "/")

	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Fleet server URL %q: must include scheme and host", baseURL)
	}

	insecure := os.Getenv("FLEET_PLAN_INSECURE") == "1"
	if strings.ToLower(parsed.Scheme) == "http" {
		if !insecure {
			return nil, fmt.Errorf("refusing to send API token over plain HTTP (%s)\nUse https:// or set FLEET_PLAN_INSECURE=1 to override", baseURL)
		}
		fmt.Fprintf(os.Stderr, "warning: FLEET_PLAN_INSECURE=1, sending API token over plain HTTP\n")
	}

	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// get performs a GET request and decodes JSON into dest.
func (c *Client) get(ctx context.Context, path string, query url.Values, dest any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &HTTPError{
			StatusCode: resp.StatusCode,
			URL:        u,
			Body:       string(body),
		}
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("decoding response from %s: %w", path, err)
	}

	return nil
}

// HTTPError represents a non-200 HTTP response.
type HTTPError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *HTTPError) Error() string {
	body := strings.ReplaceAll(e.Body, "\n", " ")
	body = strings.ReplaceAll(body, "\r", " ")
	return fmt.Sprintf("HTTP %d from %s: %s", e.StatusCode, e.URL, body)
}

// isPermissionError returns true if err is an HTTP 403 or 404, which indicates
// the API token lacks access to this endpoint (e.g. gitops role restrictions).
func isPermissionError(err error) bool {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.StatusCode == http.StatusForbidden || httpErr.StatusCode == http.StatusNotFound
}

// ---------- Response types ----------

// FleetState holds the complete current state fetched from the Fleet API.
type FleetState struct {
	Teams                  []Team
	Labels                 []Label
	FleetMaintainedCatalog []FleetMaintainedApp
	Config                 map[string]any // from GET /api/v1/fleet/config
	GlobalPolicies         []Policy       // from GET /api/v1/fleet/global/policies (teamID=0)
	GlobalQueries          []Query        // from GET /api/v1/fleet/queries (teamID=0)
}

// Team represents a Fleet team with its associated resources.
// Policies/queries/profiles are fetched via separate endpoints.
// Managed software definitions come directly from /teams[].software.
type Team struct {
	ID                    uint         `json:"id"`
	Name                  string       `json:"name"`
	Software              TeamSoftware `json:"software"`
	Policies              []Policy
	Queries               []Query
	Profiles              []Profile // populated by GetProfiles
	Scripts               []Script  `json:"-"` // populated by GetScripts
	SoftwareTitles        []SoftwareTitle
	SoftwareUnavailable   bool // true when GetSoftware returned 403/404 (token lacks permission)
	ProfilesUnavailable   bool // true when GetProfiles returned 403/404 (token lacks permission)
	ScriptsUnavailable    bool // true when GetScripts returned 403/404 (token lacks permission)
}

// TeamSoftware mirrors /api/v1/fleet/teams[].software for managed software
// definitions configured in Fleet/GitOps.
type TeamSoftware struct {
	Packages        []TeamSoftwarePackage `json:"packages"`
	FleetMaintained []TeamFleetApp        `json:"fleet_maintained_apps"`
	AppStoreApps    []TeamAppStoreApp     `json:"app_store_apps"`
}

type TeamSoftwarePackage struct {
	URL                string   `json:"url"`
	HashSHA256         string   `json:"hash_sha256"`
	SelfService        bool     `json:"self_service"`
	Categories         []string `json:"categories"`
	ReferencedYAMLPath string   `json:"referenced_yaml_path"`
}

type TeamFleetApp struct {
	Slug              string   `json:"slug"`
	SelfService       bool     `json:"self_service"`
	Categories        []string `json:"categories"`
	TitleID           uint     `json:"-"` // software title ID, for fetching detail
	TeamID            uint     `json:"-"`
	InstallScript     string   `json:"-"` // populated from title detail endpoint
	UninstallScript   string   `json:"-"`
	PreInstallQuery   string   `json:"-"`
	PostInstallScript string   `json:"-"`
}

type TeamAppStoreApp struct {
	AppStoreID  string   `json:"app_store_id"`
	SelfService bool     `json:"self_service"`
	Categories  []string `json:"categories"`
}

// FleetMaintainedApp is an entry from Fleet's maintained-app catalog.
type FleetMaintainedApp struct {
	ID              uint   `json:"id"`
	SoftwareTitleID uint   `json:"software_title_id"`
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	Platform        string `json:"platform"`
}

// Policy represents a Fleet policy as returned by the API.
type Policy struct {
	ID               uint     `json:"id"`
	Name             string   `json:"name"`
	Query            string   `json:"query"`
	Description      string   `json:"description"`
	Resolution       string   `json:"resolution"`
	Platform         string   `json:"platform"`
	Critical         bool     `json:"critical"`
	PassingHostCount uint     `json:"passing_host_count"`
	FailingHostCount uint     `json:"failing_host_count"`
	LabelsIncludeAny []string `json:"-"` // normalized from API response
	LabelsExcludeAny []string `json:"-"`
}

// Query represents a Fleet query.
type Query struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Query    string `json:"query"`
	Interval uint   `json:"interval"`
	Platform string `json:"platform"`
	Logging  string `json:"logging"`
}

// SoftwareTitle represents a software title in Fleet.
type SoftwareTitle struct {
	ID              uint                      `json:"id"`
	Name            string                    `json:"name"`
	Source          string                    `json:"source"`
	HostCount       uint                      `json:"hosts_count"`
	AppStoreApp     *SoftwareTitleAppStore    `json:"app_store_app"`
	SoftwarePackage *SoftwareTitlePackageMeta `json:"software_package"`
}

// SoftwareTitleAppStore contains app-store-specific metadata for a title.
type SoftwareTitleAppStore struct {
	AppStoreID  string `json:"app_store_id"`
	SelfService bool   `json:"self_service"`
	Platform    string `json:"platform"`
}

// SoftwareTitlePackageMeta contains package metadata for a title.
type SoftwareTitlePackageMeta struct {
	Name                 string `json:"name"`
	PackageURL           string `json:"package_url"`
	SelfService          bool   `json:"self_service"`
	Platform             string `json:"platform"`
	FleetMaintainedAppID *uint  `json:"fleet_maintained_app_id"`
}

// SoftwareTitleDetail is the full response from GET /api/v1/fleet/software/titles/:id.
// It includes script content not available in the list endpoint.
type SoftwareTitleDetail struct {
	ID              uint                        `json:"id"`
	Name            string                      `json:"name"`
	SoftwarePackage *SoftwareTitleDetailPackage  `json:"software_package"`
}

// SoftwareTitleDetailPackage contains the full package metadata including scripts.
type SoftwareTitleDetailPackage struct {
	InstallScript     string `json:"install_script"`
	UninstallScript   string `json:"uninstall_script"`
	PreInstallQuery   string `json:"pre_install_query"`
	PostInstallScript string `json:"post_install_script"`
	SelfService       bool   `json:"self_service"`
	Platform          string `json:"platform"`
	FleetMaintainedAppID *uint `json:"fleet_maintained_app_id"`
}

// Label represents a Fleet label.
type Label struct {
	ID        uint   `json:"id"`
	Name      string `json:"name"`
	Query     string `json:"query"`
	Platform  string `json:"platform"`
	HostCount uint   `json:"host_count"`
}

// Profile represents an MDM configuration profile.
type Profile struct {
	ProfileUUID string `json:"profile_uuid"`
	Name        string `json:"name"`
	Platform    string `json:"platform"`
}

// Script represents a Fleet script assigned to a team.
type Script struct {
	ID      uint   `json:"id"`
	Name    string `json:"name"`
	TeamID  uint   `json:"team_id"`
	Content string `json:"-"` // populated by GetScriptContent
}

// ---------- API response wrappers ----------

type teamsResponse struct {
	Teams []Team `json:"teams"`
}

type policiesResponse struct {
	Policies []Policy `json:"policies"`
}

type queriesResponse struct {
	Queries []Query `json:"queries"`
}

type softwareResponse struct {
	SoftwareTitles []SoftwareTitle `json:"software_titles"`
	Meta           struct {
		HasNextResults bool `json:"has_next_results"`
	} `json:"meta"`
}

type labelsResponse struct {
	Labels []Label `json:"labels"`
}

type profilesResponse struct {
	Profiles []Profile `json:"profiles"`
	Meta     struct {
		HasNextResults bool `json:"has_next_results"`
	} `json:"meta"`
}

type scriptsResponse struct {
	Scripts []Script `json:"scripts"`
	Meta    struct {
		HasNextResults bool `json:"has_next_results"`
	} `json:"meta"`
}

type fleetMaintainedAppsResponse struct {
	FleetMaintainedApps []FleetMaintainedApp `json:"fleet_maintained_apps"`
	Meta                struct {
		HasNextResults bool `json:"has_next_results"`
	} `json:"meta"`
}

// ---------- Fetch methods ----------

// GetConfig fetches the Fleet server configuration (org_settings, agent_options, etc.).
func (c *Client) GetConfig(ctx context.Context) (map[string]any, error) {
	var result map[string]any
	if err := c.get(ctx, "/api/v1/fleet/config", nil, &result); err != nil {
		return nil, fmt.Errorf("fetching config: %w", err)
	}
	return result, nil
}

// GetTeams fetches all teams with pagination.
func (c *Client) GetTeams(ctx context.Context) ([]Team, error) {
	var all []Team
	page := 0
	for {
		q := url.Values{
			"per_page": {"250"},
			"page":     {strconv.Itoa(page)},
		}
		var resp teamsResponse
		if err := c.get(ctx, "/api/v1/fleet/teams", q, &resp); err != nil {
			return nil, fmt.Errorf("fetching teams: %w", err)
		}
		all = append(all, resp.Teams...)
		if len(resp.Teams) < 250 {
			break
		}
		page++
		if page > 100 { // safety: max 25k teams
			break
		}
	}
	return all, nil
}

// GetPolicies fetches policies for a team (0 = global) with pagination.
func (c *Client) GetPolicies(ctx context.Context, teamID uint) ([]Policy, error) {
	apiPath := "/api/v1/fleet/global/policies"
	if teamID > 0 {
		apiPath = fmt.Sprintf("/api/v1/fleet/teams/%d/policies", teamID)
	}
	var all []Policy
	page := 0
	for {
		q := url.Values{
			"per_page": {"250"},
			"page":     {strconv.Itoa(page)},
		}
		var resp policiesResponse
		if err := c.get(ctx, apiPath, q, &resp); err != nil {
			return nil, fmt.Errorf("fetching policies (team %d): %w", teamID, err)
		}
		all = append(all, resp.Policies...)
		if len(resp.Policies) < 250 {
			break
		}
		page++
		if page > 100 { // safety: max 25k policies
			break
		}
	}
	return all, nil
}

// GetQueries fetches queries, optionally filtered by team, with pagination.
func (c *Client) GetQueries(ctx context.Context, teamID uint) ([]Query, error) {
	var all []Query
	page := 0
	for {
		q := url.Values{
			"per_page": {"250"},
			"page":     {strconv.Itoa(page)},
		}
		if teamID > 0 {
			q.Set("team_id", strconv.FormatUint(uint64(teamID), 10))
		}
		var resp queriesResponse
		if err := c.get(ctx, "/api/v1/fleet/queries", q, &resp); err != nil {
			return nil, fmt.Errorf("fetching queries (team %d): %w", teamID, err)
		}
		all = append(all, resp.Queries...)
		if len(resp.Queries) < 250 {
			break
		}
		page++
		if page > 100 { // safety: max 25k queries
			break
		}
	}
	return all, nil
}

// GetSoftware fetches managed (available_for_install) software titles for a team.
// Uses available_for_install=true to exclude detected-only titles (OS packages,
// browser extensions, etc.) and only return software deployed via Fleet/GitOps.
// Paginates to collect all results.
func (c *Client) GetSoftware(ctx context.Context, teamID uint) ([]SoftwareTitle, error) {
	var all []SoftwareTitle
	page := 0
	for {
		q := url.Values{
			"per_page":              {"250"},
			"page":                  {strconv.Itoa(page)},
			"available_for_install": {"true"},
		}
		if teamID > 0 {
			q.Set("team_id", strconv.FormatUint(uint64(teamID), 10))
		}
		var resp softwareResponse
		if err := c.get(ctx, "/api/v1/fleet/software/titles", q, &resp); err != nil {
			return nil, fmt.Errorf("fetching software (team %d): %w", teamID, err)
		}
		all = append(all, resp.SoftwareTitles...)
		if !resp.Meta.HasNextResults || len(resp.SoftwareTitles) == 0 {
			break // last page
		}
		page++
		if page > 400 { // safety: max 100k titles
			break
		}
	}
	return all, nil
}

// GetSoftwareTitleDetail fetches the full detail for a single software title,
// including script content not available in the list endpoint.
func (c *Client) GetSoftwareTitleDetail(ctx context.Context, titleID, teamID uint) (*SoftwareTitleDetail, error) {
	apiPath := fmt.Sprintf("/api/v1/fleet/software/titles/%d", titleID)
	q := url.Values{}
	if teamID > 0 {
		q.Set("team_id", strconv.FormatUint(uint64(teamID), 10))
	}
	var resp struct {
		SoftwareTitle SoftwareTitleDetail `json:"software_title"`
	}
	if err := c.get(ctx, apiPath, q, &resp); err != nil {
		return nil, fmt.Errorf("fetching software title %d: %w", titleID, err)
	}
	return &resp.SoftwareTitle, nil
}

// EnrichFleetAppScripts fetches title details for FMAs that have a TitleID set
// and populates their script content. Errors are non-fatal (scripts stay empty).
func (c *Client) EnrichFleetAppScripts(ctx context.Context, apps []TeamFleetApp) {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(5)
	for i := range apps {
		if apps[i].TitleID == 0 {
			continue
		}
		idx := i
		g.Go(func() error {
			detail, err := c.GetSoftwareTitleDetail(gctx, apps[idx].TitleID, apps[idx].TeamID)
			if err != nil || detail.SoftwarePackage == nil {
				return nil
			}
			apps[idx].InstallScript = strings.TrimSpace(detail.SoftwarePackage.InstallScript)
			apps[idx].UninstallScript = strings.TrimSpace(detail.SoftwarePackage.UninstallScript)
			apps[idx].PreInstallQuery = strings.TrimSpace(detail.SoftwarePackage.PreInstallQuery)
			apps[idx].PostInstallScript = strings.TrimSpace(detail.SoftwarePackage.PostInstallScript)
			return nil
		})
	}
	g.Wait()
}

// GetFleetMaintainedApps fetches Fleet's maintained-app catalog.
func (c *Client) GetFleetMaintainedApps(ctx context.Context) ([]FleetMaintainedApp, error) {
	var all []FleetMaintainedApp
	page := 0
	for {
		q := url.Values{
			"per_page": {"250"},
			"page":     {strconv.Itoa(page)},
		}
		var resp fleetMaintainedAppsResponse
		if err := c.get(ctx, "/api/v1/fleet/software/fleet_maintained_apps", q, &resp); err != nil {
			return nil, fmt.Errorf("fetching fleet maintained apps: %w", err)
		}
		all = append(all, resp.FleetMaintainedApps...)
		if !resp.Meta.HasNextResults || len(resp.FleetMaintainedApps) == 0 {
			break
		}
		page++
		if page > 400 { // safety: max 100k catalog entries
			break
		}
	}
	return all, nil
}

// GetLabels fetches all labels with pagination.
func (c *Client) GetLabels(ctx context.Context) ([]Label, error) {
	var all []Label
	page := 0
	for {
		q := url.Values{
			"per_page":            {"250"},
			"page":                {strconv.Itoa(page)},
			"include_host_counts": {"true"},
		}
		var resp labelsResponse
		if err := c.get(ctx, "/api/v1/fleet/labels", q, &resp); err != nil {
			return nil, fmt.Errorf("fetching labels: %w", err)
		}
		all = append(all, resp.Labels...)
		if len(resp.Labels) < 250 {
			break
		}
		page++
		if page > 100 { // safety: max 25k labels
			break
		}
	}
	return all, nil
}

// GetProfiles fetches MDM profiles for a team with pagination.
func (c *Client) GetProfiles(ctx context.Context, teamID uint) ([]Profile, error) {
	var all []Profile
	page := 0
	for {
		q := url.Values{
			"per_page": {"250"},
			"page":     {strconv.Itoa(page)},
		}
		if teamID > 0 {
			q.Set("team_id", strconv.FormatUint(uint64(teamID), 10))
		}
		var resp profilesResponse
		if err := c.get(ctx, "/api/v1/fleet/configuration_profiles", q, &resp); err != nil {
			return nil, fmt.Errorf("fetching profiles (team %d): %w", teamID, err)
		}
		all = append(all, resp.Profiles...)
		if !resp.Meta.HasNextResults || len(resp.Profiles) == 0 {
			break
		}
		page++
		if page > 100 { // safety: max 25k profiles
			break
		}
	}
	return all, nil
}

// GetScripts fetches scripts for a team with pagination.
func (c *Client) GetScripts(ctx context.Context, teamID uint) ([]Script, error) {
	var all []Script
	page := 0
	for {
		q := url.Values{
			"per_page": {"250"},
			"page":     {strconv.Itoa(page)},
		}
		if teamID > 0 {
			q.Set("team_id", strconv.FormatUint(uint64(teamID), 10))
		}
		var resp scriptsResponse
		if err := c.get(ctx, "/api/v1/fleet/scripts", q, &resp); err != nil {
			return nil, fmt.Errorf("fetching scripts (team %d): %w", teamID, err)
		}
		all = append(all, resp.Scripts...)
		if !resp.Meta.HasNextResults || len(resp.Scripts) == 0 {
			break
		}
		page++
		if page > 100 { // safety: max 25k scripts
			break
		}
	}
	return all, nil
}

// EnrichScriptContents fetches the content for each script by ID using the
// download endpoint (?alt=media) and populates the Content field.
// Errors are non-fatal (content stays empty).
func (c *Client) EnrichScriptContents(ctx context.Context, scripts []Script) {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(5)
	for i := range scripts {
		if scripts[i].ID == 0 {
			continue
		}
		idx := i
		g.Go(func() error {
			content, err := c.getScriptContent(gctx, scripts[idx].ID)
			if err != nil {
				return nil // non-fatal
			}
			scripts[idx].Content = strings.TrimSpace(content)
			return nil
		})
	}
	g.Wait()
}

// getScriptContent downloads script content via GET /api/v1/fleet/scripts/:id?alt=media.
func (c *Client) getScriptContent(ctx context.Context, scriptID uint) (string, error) {
	u := fmt.Sprintf("%s/api/v1/fleet/scripts/%d?alt=media", c.baseURL, scriptID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d fetching script %d", resp.StatusCode, scriptID)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// FetchAll concurrently fetches the complete Fleet state. Uses errgroup for
// parallel requests. If fetchGlobal is true, also fetches global config,
// policies, and queries (for default.yml diffing).
func (c *Client) FetchAll(ctx context.Context, fetchGlobal ...bool) (*FleetState, error) {
	state := &FleetState{}
	wantGlobal := len(fetchGlobal) > 0 && fetchGlobal[0]

	teams, err := c.GetTeams(ctx)
	if err != nil {
		return nil, err
	}

	labels, err := c.GetLabels(ctx)
	if err != nil {
		return nil, err
	}
	state.Labels = labels

	fleetMaintainedCatalog, err := c.GetFleetMaintainedApps(ctx)
	if err != nil {
		if !isPermissionError(err) {
			return nil, err
		}
		fleetMaintainedCatalog = nil
	}
	state.FleetMaintainedCatalog = fleetMaintainedCatalog

	// Fetch per-team resources concurrently
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(5)

	// Local variables for global results; assigned to state after g.Wait().
	var (
		globalConfig   map[string]any
		globalPolicies []Policy
		globalQueries  []Query
	)

	// Fetch global config/policies/queries in parallel with team resources
	if wantGlobal {
		g.Go(func() error {
			cfg, err := c.GetConfig(gctx)
			if err != nil {
				return err
			}
			globalConfig = cfg
			return nil
		})
		g.Go(func() error {
			policies, err := c.GetPolicies(gctx, 0)
			if err != nil {
				return err
			}
			globalPolicies = policies
			return nil
		})
		g.Go(func() error {
			queries, err := c.GetQueries(gctx, 0)
			if err != nil {
				return err
			}
			globalQueries = queries
			return nil
		})
	}

	// teamPartials holds per-goroutine results indexed by team slot.
	// Each field is written by exactly one goroutine, so there is no data race.
	type teamPartial struct {
		policies            []Policy
		queries             []Query
		profiles            []Profile
		profilesUnavailable bool
		softwareTitles      []SoftwareTitle
		softwareUnavailable bool
		scripts             []Script
		scriptsUnavailable  bool
	}
	teamPartials := make([]teamPartial, len(teams))

	teamResults := make([]Team, len(teams))
	for i, t := range teams {
		teamResults[i] = t
		idx := i
		teamID := t.ID

		g.Go(func() error {
			policies, err := c.GetPolicies(gctx, teamID)
			if err != nil {
				return err
			}
			teamPartials[idx].policies = policies
			return nil
		})

		g.Go(func() error {
			queries, err := c.GetQueries(gctx, teamID)
			if err != nil {
				return err
			}
			teamPartials[idx].queries = queries
			return nil
		})

		g.Go(func() error {
			profiles, err := c.GetProfiles(gctx, teamID)
			if err != nil {
				if !isPermissionError(err) {
					return err
				}
				teamPartials[idx].profilesUnavailable = true
				profiles = nil
			}
			teamPartials[idx].profiles = profiles
			return nil
		})

		g.Go(func() error {
			softwareTitles, err := c.GetSoftware(gctx, teamID)
			if err != nil {
				if !isPermissionError(err) {
					return err
				}
				teamPartials[idx].softwareUnavailable = true
				softwareTitles = nil
			}
			teamPartials[idx].softwareTitles = softwareTitles
			return nil
		})

		g.Go(func() error {
			scripts, err := c.GetScripts(gctx, teamID)
			if err != nil {
				if !isPermissionError(err) {
					return err
				}
				teamPartials[idx].scriptsUnavailable = true
				scripts = nil
			}
			teamPartials[idx].scripts = scripts
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Assign global results after all goroutines have completed.
	if wantGlobal {
		state.Config = globalConfig
		state.GlobalPolicies = globalPolicies
		state.GlobalQueries = globalQueries
	}

	// Assign per-team partial results.
	for i := range teamResults {
		p := &teamPartials[i]
		teamResults[i].Policies = p.policies
		teamResults[i].Queries = p.queries
		teamResults[i].Profiles = p.profiles
		teamResults[i].ProfilesUnavailable = p.profilesUnavailable
		teamResults[i].SoftwareTitles = p.softwareTitles
		teamResults[i].SoftwareUnavailable = p.softwareUnavailable
		teamResults[i].Scripts = p.scripts
		teamResults[i].ScriptsUnavailable = p.scriptsUnavailable
	}

	// Enrich script contents (second pass, needs script IDs from first pass)
	for i := range teamResults {
		if !teamResults[i].ScriptsUnavailable && len(teamResults[i].Scripts) > 0 {
			c.EnrichScriptContents(ctx, teamResults[i].Scripts)
		}
	}

	state.Teams = teamResults
	return state, nil
}
