// Package git detects the CI platform, fetches MR/PR changed files,
// and posts or updates a comment with the fleet-plan diff output.
package git

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Platform identifies the CI provider.
type Platform int

// Recognized CI providers. PlatformUnknown means fleet-plan is not running in
// a merge request or pull request context.
const (
	PlatformUnknown Platform = iota
	PlatformGitLab
	PlatformGitHub
)

// Env holds all CI environment variables resolved at startup.
type Env struct {
	Platform Platform

	// GitLab
	GitLabAPIURL    string
	GitLabProjectID string
	GitLabMRIID     string
	GitLabJobURL    string
	GitLabMRURL     string
	GitLabToken     string

	// GitHub
	GitHubAPIURL    string
	GitHubRepo      string
	GitHubPRNumber  string
	GitHubServerURL string
	GitHubToken     string

	// Git
	DiffBaseSHA  string
	TargetBranch string
}

var (
	validNumeric = regexp.MustCompile(`^[0-9]+$`)
	validGHRepo  = regexp.MustCompile(`^[a-zA-Z0-9_.\-]+/[a-zA-Z0-9_.\-]+$`)
	validSHA     = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	validBranch  = regexp.MustCompile(`^[a-zA-Z0-9/_.\-]+$`)
)

// maxResponseBody is the upper bound on API response size (10 MiB).
const maxResponseBody = 10 * 1024 * 1024

// Detect reads CI environment variables and returns a populated Env.
// Returns PlatformUnknown if not running in a recognized CI MR/PR context.
func Detect() Env {
	var e Env

	// GitLab: CI_MERGE_REQUEST_IID is only set in merge request pipelines.
	if iid := os.Getenv("CI_MERGE_REQUEST_IID"); iid != "" {
		e.Platform = PlatformGitLab
		e.GitLabAPIURL = os.Getenv("CI_API_V4_URL")
		e.GitLabProjectID = os.Getenv("CI_PROJECT_ID")
		e.GitLabMRIID = iid
		e.GitLabJobURL = os.Getenv("CI_JOB_URL")
		e.GitLabMRURL = os.Getenv("CI_PROJECT_URL") + "/-/merge_requests/" + iid
		e.GitLabToken = os.Getenv("FLEET_PLAN_BOT")
		e.DiffBaseSHA = os.Getenv("CI_MERGE_REQUEST_DIFF_BASE_SHA")
		e.TargetBranch = os.Getenv("CI_MERGE_REQUEST_TARGET_BRANCH_NAME")
		return e
	}

	// GitHub: GITHUB_EVENT_NAME is set for all GitHub Actions runs.
	if event := os.Getenv("GITHUB_EVENT_NAME"); event == "pull_request" || event == "pull_request_target" {
		e.Platform = PlatformGitHub
		e.GitHubAPIURL = os.Getenv("GITHUB_API_URL")
		if e.GitHubAPIURL == "" {
			e.GitHubAPIURL = "https://api.github.com"
		}
		e.GitHubRepo = os.Getenv("GITHUB_REPOSITORY")

		// PR number resolution order: PR_NUMBER is the explicit override
		// (set by workflow dispatch or reusable workflows), GITHUB_PR_NUMBER
		// is a common convention in composite actions, and
		// parsePRNumberFromEvent is the final fallback reading the event
		// payload written by GitHub Actions at $GITHUB_EVENT_PATH.
		e.GitHubPRNumber = os.Getenv("PR_NUMBER")
		if e.GitHubPRNumber == "" {
			e.GitHubPRNumber = os.Getenv("GITHUB_PR_NUMBER")
		}
		if e.GitHubPRNumber == "" {
			e.GitHubPRNumber = parsePRNumberFromEvent(os.Getenv("GITHUB_EVENT_PATH"))
		}
		e.GitHubServerURL = os.Getenv("GITHUB_SERVER_URL")
		e.GitHubToken = os.Getenv("GITHUB_TOKEN")
		e.DiffBaseSHA = os.Getenv("GITHUB_BASE_SHA")
		e.TargetBranch = os.Getenv("GITHUB_BASE_REF")
		return e
	}

	return e
}

// gitLabReady returns an error if the GitLab API credentials are incomplete or
// if GitLabMRIID fails validation.
func (e Env) gitLabReady() error {
	if e.GitLabAPIURL == "" || e.GitLabProjectID == "" || e.GitLabMRIID == "" || e.GitLabToken == "" {
		return fmt.Errorf("missing GitLab API env vars (FLEET_PLAN_BOT not set?)")
	}
	if !validNumeric.MatchString(e.GitLabMRIID) {
		return fmt.Errorf("CI_MERGE_REQUEST_IID %q is not a valid numeric ID", e.GitLabMRIID)
	}
	return nil
}

// gitHubReady returns an error if the GitHub API credentials are incomplete or
// if GitHubPRNumber/GitHubRepo fail validation.
func (e Env) gitHubReady() error {
	if e.GitHubAPIURL == "" || e.GitHubRepo == "" || e.GitHubPRNumber == "" || e.GitHubToken == "" {
		return fmt.Errorf("missing GitHub API env vars (GITHUB_TOKEN not set?)")
	}
	if !validNumeric.MatchString(e.GitHubPRNumber) {
		return fmt.Errorf("GitHub PR number %q is not a valid numeric ID", e.GitHubPRNumber)
	}
	if !validGHRepo.MatchString(e.GitHubRepo) {
		return fmt.Errorf("GITHUB_REPOSITORY %q does not match owner/repo format", e.GitHubRepo)
	}
	return nil
}

// JobURL returns the CI job URL for embedding in comments.
func (e Env) JobURL() string {
	switch e.Platform {
	case PlatformGitLab:
		return e.GitLabJobURL
	case PlatformGitHub:
		if e.GitHubServerURL != "" && e.GitHubRepo != "" {
			runID := os.Getenv("GITHUB_RUN_ID")
			if runID != "" {
				return fmt.Sprintf("%s/%s/actions/runs/%s", e.GitHubServerURL, e.GitHubRepo, runID)
			}
		}
	}
	return ""
}

// ChangedFiles returns the list of files changed in the MR/PR.
// Priority: MR/PR API > git diff > empty (triggers full diff).
func (e Env) ChangedFiles() ([]string, error) {
	switch e.Platform {
	case PlatformGitLab:
		files, err := e.gitLabChangedFiles()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: GitLab MR API unavailable (%v), falling back to git diff\n", err)
		} else {
			// API succeeded: return the result even if empty (no changed files).
			return files, nil
		}
	case PlatformGitHub:
		files, err := e.gitHubChangedFiles()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: GitHub PR API unavailable (%v), falling back to git diff\n", err)
		} else {
			// API succeeded: return the result even if empty (no changed files).
			return files, nil
		}
	}

	return e.gitDiffChangedFiles()
}

func (e Env) gitLabChangedFiles() ([]string, error) {
	if err := e.gitLabReady(); err != nil {
		return nil, err
	}
	// NOTE: pagination is not implemented; only the first 100 changed files are returned.
	apiURL := fmt.Sprintf("%s/projects/%s/merge_requests/%s/changes?per_page=100",
		e.GitLabAPIURL, url.PathEscape(e.GitLabProjectID), url.PathEscape(e.GitLabMRIID))

	body, err := doRequest("GET", apiURL, nil, map[string]string{"PRIVATE-TOKEN": e.GitLabToken})
	if err != nil {
		return nil, fmt.Errorf("GitLab: %w", err)
	}

	var result struct {
		Changes []struct {
			NewPath string `json:"new_path"`
			OldPath string `json:"old_path"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	var files []string
	for _, c := range result.Changes {
		p := c.NewPath
		if p == "" {
			p = c.OldPath
		}
		if p != "" {
			files = append(files, p)
		}
	}
	return files, nil
}

func (e Env) gitHubChangedFiles() ([]string, error) {
	if err := e.gitHubReady(); err != nil {
		return nil, err
	}
	// NOTE: pagination is not implemented; only the first 100 changed files are returned.
	apiURL := fmt.Sprintf("%s/repos/%s/pulls/%s/files?per_page=100", e.GitHubAPIURL, e.GitHubRepo, e.GitHubPRNumber)

	body, err := doRequest("GET", apiURL, nil, githubHeaders(e.GitHubToken))
	if err != nil {
		return nil, fmt.Errorf("GitHub: %w", err)
	}

	var result []struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	var files []string
	for _, f := range result {
		if f.Filename != "" {
			files = append(files, f.Filename)
		}
	}
	return files, nil
}

func (e Env) gitDiffChangedFiles() ([]string, error) {
	// Try to fetch the target branch if needed.
	if e.TargetBranch != "" && validBranch.MatchString(e.TargetBranch) && !strings.Contains(e.TargetBranch, "..") {
		_ = exec.Command("git", "fetch", "origin", "--depth=200", "--", e.TargetBranch).Run()
	}

	var ref string
	if e.DiffBaseSHA != "" && validSHA.MatchString(e.DiffBaseSHA) {
		// Verify the commit is available.
		if err := exec.Command("git", "cat-file", "-e", e.DiffBaseSHA+"^{commit}").Run(); err == nil {
			ref = e.DiffBaseSHA + "...HEAD"
		}
	}
	if ref == "" && e.TargetBranch != "" && validBranch.MatchString(e.TargetBranch) && !strings.Contains(e.TargetBranch, "..") {
		ref = "origin/" + e.TargetBranch + "...HEAD"
	}
	if ref == "" {
		return nil, fmt.Errorf("no base SHA or target branch available for git diff")
	}

	out, err := exec.Command("git", "diff", "--name-only", "--", ref).Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// PostOrUpdateComment posts or idempotently updates an MR/PR comment containing marker.
// If a comment with marker already exists it is replaced; otherwise a new one is created.
// Returns the comment URL on success.
func (e Env) PostOrUpdateComment(body, marker string) (string, error) {
	switch e.Platform {
	case PlatformGitLab:
		return e.gitLabPostOrUpdate(body, marker)
	case PlatformGitHub:
		return e.gitHubPostOrUpdate(body, marker)
	}
	return "", fmt.Errorf("unknown CI platform")
}

func (e Env) gitLabPostOrUpdate(body, marker string) (string, error) {
	if err := e.gitLabReady(); err != nil {
		return "", fmt.Errorf("skipping MR note: %w", err)
	}

	headers := map[string]string{"PRIVATE-TOKEN": e.GitLabToken}
	notesURL := fmt.Sprintf("%s/projects/%s/merge_requests/%s/notes",
		e.GitLabAPIURL, url.PathEscape(e.GitLabProjectID), url.PathEscape(e.GitLabMRIID))

	listURL := notesURL + "?per_page=100&sort=desc&order_by=updated_at"
	_, method, reqURL, err := findThenRoute(notesURL, listURL, headers, marker, "PUT")
	if err != nil {
		return "", fmt.Errorf("listing MR notes: %w", err)
	}

	params := url.Values{}
	params.Set("body", body)
	encoded := params.Encode()

	writeHeaders := map[string]string{
		"PRIVATE-TOKEN": e.GitLabToken,
		"Content-Type":  "application/x-www-form-urlencoded",
	}
	respBody, err := doRequest(method, reqURL, strings.NewReader(encoded), writeHeaders)
	if err != nil {
		return "", err
	}

	var note struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(respBody, &note); err != nil {
		return "", fmt.Errorf("parsing MR note response: %w", err)
	}
	if note.ID <= 0 {
		return "", fmt.Errorf("MR note response missing valid id")
	}
	commentURL := fmt.Sprintf("%s#note_%d", e.GitLabMRURL, note.ID)
	return commentURL, nil
}

func (e Env) gitHubPostOrUpdate(body, marker string) (string, error) {
	if err := e.gitHubReady(); err != nil {
		return "", fmt.Errorf("skipping PR comment: %w", err)
	}

	headers := githubHeaders(e.GitHubToken)
	commentsURL := fmt.Sprintf("%s/repos/%s/issues/%s/comments",
		e.GitHubAPIURL, e.GitHubRepo, e.GitHubPRNumber)

	listURL := commentsURL + "?per_page=100"
	commentID, method, reqURL, err := findThenRoute(commentsURL, listURL, headers, marker, "PATCH")
	if err != nil {
		return "", fmt.Errorf("listing PR comments: %w", err)
	}

	if commentID != "" {
		reqURL = fmt.Sprintf("%s/repos/%s/issues/comments/%s", e.GitHubAPIURL, e.GitHubRepo, commentID)
	}

	payload, _ := json.Marshal(map[string]string{"body": body})

	writeHeaders := githubHeaders(e.GitHubToken)
	writeHeaders["Content-Type"] = "application/json"
	respBody, err := doRequest(method, reqURL, strings.NewReader(string(payload)), writeHeaders)
	if err != nil {
		return "", err
	}

	var comment struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &comment); err != nil {
		return "", fmt.Errorf("parsing PR comment response: %w", err)
	}
	if comment.HTMLURL == "" {
		return "", fmt.Errorf("PR comment response missing html_url")
	}
	return comment.HTMLURL, nil
}

// --- shared HTTP helpers ---

// doRequest performs an HTTP request with the given method, body, and headers.
// Response body is limited to maxResponseBody bytes.
// Returns the response body or an error if the status code is >= 300.
func doRequest(method, reqURL string, body io.Reader, headers map[string]string) ([]byte, error) {
	if os.Getenv("FLEET_PLAN_INSECURE") != "1" && !strings.HasPrefix(strings.ToLower(reqURL), "https://") {
		return nil, fmt.Errorf("refusing API request to non-HTTPS URL: %s", reqURL)
	}
	req, err := http.NewRequest(method, reqURL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API %s %d: %s", method, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// findThenRoute searches for an existing comment with the given marker and
// returns (commentID, httpMethod, targetURL, error). When a matching comment
// is found, the method is updateMethod and the URL includes the comment ID;
// otherwise the method is POST and the URL is the base commentsURL.
//
// NOTE: only the first page of 100 comments is searched. Pagination is not
// implemented; MRs/PRs with more than 100 comments may create a duplicate.
func findThenRoute(baseURL, listURL string, headers map[string]string, marker, updateMethod string) (string, string, string, error) {
	body, err := doRequest("GET", listURL, nil, headers)
	if err != nil {
		return "", "", "", err
	}

	var items []struct {
		ID   int    `json:"id"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return "", "", "", err
	}

	needle := fmt.Sprintf("<!-- %s -->", marker)
	for _, item := range items {
		if strings.Contains(item.Body, needle) {
			id := fmt.Sprintf("%d", item.ID)
			return id, updateMethod, baseURL + "/" + id, nil
		}
	}
	return "", "POST", baseURL, nil
}

// githubHeaders returns the standard headers for GitHub API requests.
func githubHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/vnd.github+json",
	}
}

// parsePRNumberFromEvent reads the GitHub event JSON file and extracts the PR number.
func parsePRNumberFromEvent(eventPath string) string {
	if eventPath == "" {
		return ""
	}
	data, err := os.ReadFile(eventPath)
	if err != nil {
		return ""
	}
	var event struct {
		PullRequest struct {
			Number json.Number `json:"number"`
		} `json:"pull_request"`
		Number json.Number `json:"number"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return ""
	}
	if n := event.PullRequest.Number.String(); n != "" && n != "0" {
		return n
	}
	if n := event.Number.String(); n != "" && n != "0" {
		return n
	}
	return ""
}
