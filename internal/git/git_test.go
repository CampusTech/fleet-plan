package git

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// insecureServer starts an HTTP test server and allows plain-HTTP requests for
// the duration of the test. doRequest refuses non-HTTPS URLs otherwise.
func insecureServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	t.Setenv("FLEET_PLAN_INSECURE", "1")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// ---------- Detect ----------

func TestDetect(t *testing.T) {
	// Both platforms read from the process environment, so each case clears
	// the variables it does not set.
	all := []string{
		"CI_MERGE_REQUEST_IID", "CI_API_V4_URL", "CI_PROJECT_ID", "CI_JOB_URL",
		"CI_PROJECT_URL", "FLEET_PLAN_BOT", "CI_MERGE_REQUEST_DIFF_BASE_SHA",
		"CI_MERGE_REQUEST_TARGET_BRANCH_NAME", "GITHUB_EVENT_NAME",
		"GITHUB_API_URL", "GITHUB_REPOSITORY", "PR_NUMBER", "GITHUB_PR_NUMBER",
		"GITHUB_EVENT_PATH", "GITHUB_SERVER_URL", "GITHUB_TOKEN",
		"GITHUB_BASE_SHA", "GITHUB_BASE_REF",
	}

	tests := []struct {
		name  string
		env   map[string]string
		check func(t *testing.T, e Env)
	}{
		{
			name: "no CI context",
			env:  nil,
			check: func(t *testing.T, e Env) {
				if e.Platform != PlatformUnknown {
					t.Errorf("Platform: got %v, want PlatformUnknown", e.Platform)
				}
			},
		},
		{
			name: "gitlab merge request",
			env: map[string]string{
				"CI_MERGE_REQUEST_IID":                "42",
				"CI_API_V4_URL":                       "https://gitlab.example.com/api/v4",
				"CI_PROJECT_ID":                       "17",
				"CI_PROJECT_URL":                      "https://gitlab.example.com/group/repo",
				"FLEET_PLAN_BOT":                      "glpat-token",
				"CI_MERGE_REQUEST_DIFF_BASE_SHA":      strings.Repeat("a", 40),
				"CI_MERGE_REQUEST_TARGET_BRANCH_NAME": "main",
			},
			check: func(t *testing.T, e Env) {
				if e.Platform != PlatformGitLab {
					t.Fatalf("Platform: got %v, want PlatformGitLab", e.Platform)
				}
				if e.GitLabMRIID != "42" || e.GitLabProjectID != "17" {
					t.Errorf("MR identity: got iid=%q project=%q", e.GitLabMRIID, e.GitLabProjectID)
				}
				want := "https://gitlab.example.com/group/repo/-/merge_requests/42"
				if e.GitLabMRURL != want {
					t.Errorf("GitLabMRURL: got %q, want %q", e.GitLabMRURL, want)
				}
				if e.TargetBranch != "main" {
					t.Errorf("TargetBranch: got %q", e.TargetBranch)
				}
			},
		},
		{
			name: "github pull request with explicit PR number",
			env: map[string]string{
				"GITHUB_EVENT_NAME": "pull_request",
				"GITHUB_REPOSITORY": "CampusTech/fleet-plan",
				"PR_NUMBER":         "7",
				"GITHUB_TOKEN":      "ghs-token",
				"GITHUB_BASE_REF":   "main",
			},
			check: func(t *testing.T, e Env) {
				if e.Platform != PlatformGitHub {
					t.Fatalf("Platform: got %v, want PlatformGitHub", e.Platform)
				}
				if e.GitHubPRNumber != "7" {
					t.Errorf("GitHubPRNumber: got %q, want 7", e.GitHubPRNumber)
				}
				// API URL defaults when GITHUB_API_URL is unset.
				if e.GitHubAPIURL != "https://api.github.com" {
					t.Errorf("GitHubAPIURL: got %q", e.GitHubAPIURL)
				}
			},
		},
		{
			name: "github pull_request_target falls back to GITHUB_PR_NUMBER",
			env: map[string]string{
				"GITHUB_EVENT_NAME": "pull_request_target",
				"GITHUB_REPOSITORY": "CampusTech/fleet-plan",
				"GITHUB_PR_NUMBER":  "9",
				"GITHUB_API_URL":    "https://ghe.example.com/api/v3",
			},
			check: func(t *testing.T, e Env) {
				if e.GitHubPRNumber != "9" {
					t.Errorf("GitHubPRNumber: got %q, want 9", e.GitHubPRNumber)
				}
				if e.GitHubAPIURL != "https://ghe.example.com/api/v3" {
					t.Errorf("GitHubAPIURL: got %q", e.GitHubAPIURL)
				}
			},
		},
		{
			name: "push event is not a PR context",
			env:  map[string]string{"GITHUB_EVENT_NAME": "push"},
			check: func(t *testing.T, e Env) {
				if e.Platform != PlatformUnknown {
					t.Errorf("Platform: got %v, want PlatformUnknown", e.Platform)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range all {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			tt.check(t, Detect())
		})
	}
}

func TestDetectGitHubReadsPRNumberFromEventPayload(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "event.json")
	if err := os.WriteFile(payload, []byte(`{"pull_request":{"number":123}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GITHUB_EVENT_NAME", "pull_request")
	t.Setenv("PR_NUMBER", "")
	t.Setenv("GITHUB_PR_NUMBER", "")
	t.Setenv("GITHUB_EVENT_PATH", payload)

	if got := Detect().GitHubPRNumber; got != "123" {
		t.Errorf("GitHubPRNumber: got %q, want 123", got)
	}
}

func TestParsePRNumberFromEvent(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{"pull_request.number", `{"pull_request":{"number":5}}`, "5"},
		{"top-level number", `{"number":6}`, "6"},
		{"pull_request wins", `{"number":6,"pull_request":{"number":5}}`, "5"},
		{"zero is not a PR", `{"number":0}`, ""},
		{"malformed JSON", `{`, ""},
		{"no number", `{"action":"opened"}`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "event.json")
			if err := os.WriteFile(path, []byte(tt.payload), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := parsePRNumberFromEvent(path); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}

	if got := parsePRNumberFromEvent(""); got != "" {
		t.Errorf("empty path: got %q, want empty", got)
	}
	if got := parsePRNumberFromEvent(filepath.Join(t.TempDir(), "missing.json")); got != "" {
		t.Errorf("missing file: got %q, want empty", got)
	}
}

// ---------- readiness checks ----------

func TestReadyChecks(t *testing.T) {
	tests := []struct {
		name    string
		env     Env
		wantErr string
	}{
		{
			name:    "gitlab missing token",
			env:     Env{GitLabAPIURL: "https://x/api/v4", GitLabProjectID: "1", GitLabMRIID: "2"},
			wantErr: "missing GitLab API env vars",
		},
		{
			name:    "gitlab non-numeric MR iid",
			env:     Env{GitLabAPIURL: "https://x/api/v4", GitLabProjectID: "1", GitLabMRIID: "2; rm -rf /", GitLabToken: "t"},
			wantErr: "not a valid numeric ID",
		},
		{
			name: "gitlab complete",
			env:  Env{GitLabAPIURL: "https://x/api/v4", GitLabProjectID: "1", GitLabMRIID: "2", GitLabToken: "t"},
		},
		{
			name:    "github missing token",
			env:     Env{GitHubAPIURL: "https://api.github.com", GitHubRepo: "o/r", GitHubPRNumber: "1"},
			wantErr: "missing GitHub API env vars",
		},
		{
			name:    "github malformed repo",
			env:     Env{GitHubAPIURL: "https://api.github.com", GitHubRepo: "not-a-repo", GitHubPRNumber: "1", GitHubToken: "t"},
			wantErr: "GITHUB_REPOSITORY",
		},
		{
			name:    "github non-numeric PR number",
			env:     Env{GitHubAPIURL: "https://api.github.com", GitHubRepo: "o/r", GitHubPRNumber: "abc", GitHubToken: "t"},
			wantErr: "not a valid numeric",
		},
		{
			name: "github complete",
			env:  Env{GitHubAPIURL: "https://api.github.com", GitHubRepo: "o/r", GitHubPRNumber: "1", GitHubToken: "t"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.env.GitLabAPIURL != "" {
				err = tt.env.gitLabReady()
			} else {
				err = tt.env.gitHubReady()
			}
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

// ---------- JobURL ----------

func TestJobURL(t *testing.T) {
	t.Run("gitlab", func(t *testing.T) {
		e := Env{Platform: PlatformGitLab, GitLabJobURL: "https://gitlab/-/jobs/1"}
		if got := e.JobURL(); got != "https://gitlab/-/jobs/1" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("github with run id", func(t *testing.T) {
		t.Setenv("GITHUB_RUN_ID", "555")
		e := Env{Platform: PlatformGitHub, GitHubServerURL: "https://github.com", GitHubRepo: "o/r"}
		want := "https://github.com/o/r/actions/runs/555"
		if got := e.JobURL(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("github without run id", func(t *testing.T) {
		t.Setenv("GITHUB_RUN_ID", "")
		e := Env{Platform: PlatformGitHub, GitHubServerURL: "https://github.com", GitHubRepo: "o/r"}
		if got := e.JobURL(); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("unknown platform", func(t *testing.T) {
		if got := (Env{}).JobURL(); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// ---------- doRequest ----------

func TestDoRequestRefusesNonHTTPS(t *testing.T) {
	t.Setenv("FLEET_PLAN_INSECURE", "")
	_, err := doRequest("GET", "http://gitlab.example.com/api/v4/projects", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("expected non-HTTPS refusal, got %v", err)
	}
}

func TestDoRequestSendsHeadersAndReturnsBody(t *testing.T) {
	var gotToken, gotMethod string
	srv := insecureServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		gotMethod = r.Method
		fmt.Fprint(w, `{"ok":true}`)
	})

	body, err := doRequest("POST", srv.URL, strings.NewReader("x=1"), map[string]string{"PRIVATE-TOKEN": "secret"})
	if err != nil {
		t.Fatalf("doRequest: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body: got %q", body)
	}
	if gotToken != "secret" {
		t.Errorf("PRIVATE-TOKEN header: got %q", gotToken)
	}
	if gotMethod != "POST" {
		t.Errorf("method: got %q", gotMethod)
	}
}

func TestDoRequestErrorStatus(t *testing.T) {
	srv := insecureServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "insufficient scope")
	})

	_, err := doRequest("GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should mention status: %v", err)
	}
}

// ---------- findThenRoute ----------

func TestFindThenRoute(t *testing.T) {
	tests := []struct {
		name       string
		list       string
		wantID     string
		wantMethod string
		wantSuffix string
	}{
		{
			name:       "no existing comment posts new",
			list:       `[{"id":1,"body":"unrelated"}]`,
			wantID:     "",
			wantMethod: "POST",
			wantSuffix: "/comments",
		},
		{
			name:       "existing marker routes to update",
			list:       `[{"id":1,"body":"unrelated"},{"id":77,"body":"diff\n<!-- fleet-plan-marker -->"}]`,
			wantID:     "77",
			wantMethod: "PATCH",
			wantSuffix: "/comments/77",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := insecureServer(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, tt.list)
			})
			base := srv.URL + "/comments"

			id, method, reqURL, err := findThenRoute(base, base+"?per_page=100", nil, "fleet-plan-marker", "PATCH")
			if err != nil {
				t.Fatalf("findThenRoute: %v", err)
			}
			if id != tt.wantID {
				t.Errorf("id: got %q, want %q", id, tt.wantID)
			}
			if method != tt.wantMethod {
				t.Errorf("method: got %q, want %q", method, tt.wantMethod)
			}
			if !strings.HasSuffix(reqURL, tt.wantSuffix) {
				t.Errorf("url %q does not end with %q", reqURL, tt.wantSuffix)
			}
		})
	}

	t.Run("malformed list JSON", func(t *testing.T) {
		srv := insecureServer(t, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"not":"an array"}`)
		})
		if _, _, _, err := findThenRoute(srv.URL, srv.URL, nil, "m", "PATCH"); err == nil {
			t.Fatal("expected error for non-array list response")
		}
	})
}

// ---------- ChangedFiles ----------

func TestGitLabChangedFiles(t *testing.T) {
	var gotPath string
	srv := insecureServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"changes":[
			{"new_path":"teams/workstations.yml","old_path":"teams/workstations.yml"},
			{"new_path":"","old_path":"teams/deleted.yml"},
			{"new_path":"","old_path":""}
		]}`)
	})

	e := Env{Platform: PlatformGitLab, GitLabAPIURL: srv.URL, GitLabProjectID: "17", GitLabMRIID: "42", GitLabToken: "t"}
	files, err := e.gitLabChangedFiles()
	if err != nil {
		t.Fatalf("gitLabChangedFiles: %v", err)
	}
	want := []string{"teams/workstations.yml", "teams/deleted.yml"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Errorf("files: got %v, want %v", files, want)
	}
	if !strings.Contains(gotPath, "/projects/17/merge_requests/42/changes") {
		t.Errorf("request path: got %q", gotPath)
	}
}

func TestGitHubChangedFiles(t *testing.T) {
	var gotAuth string
	srv := insecureServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `[{"filename":"teams/workstations.yml"},{"filename":""},{"filename":"default.yml"}]`)
	})

	e := Env{Platform: PlatformGitHub, GitHubAPIURL: srv.URL, GitHubRepo: "CampusTech/fleet-plan", GitHubPRNumber: "7", GitHubToken: "tok"}
	files, err := e.gitHubChangedFiles()
	if err != nil {
		t.Fatalf("gitHubChangedFiles: %v", err)
	}
	want := []string{"teams/workstations.yml", "default.yml"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Errorf("files: got %v, want %v", files, want)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization: got %q", gotAuth)
	}
}

func TestChangedFilesAPIErrorFallsBackToGitDiff(t *testing.T) {
	srv := insecureServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	// No DiffBaseSHA and no TargetBranch, so the git diff fallback also fails.
	// That combination is what proves the fallback was attempted.
	e := Env{Platform: PlatformGitHub, GitHubAPIURL: srv.URL, GitHubRepo: "o/r", GitHubPRNumber: "1", GitHubToken: "t"}
	if _, err := e.ChangedFiles(); err == nil {
		t.Fatal("expected git diff fallback error")
	} else if !strings.Contains(err.Error(), "no base SHA or target branch") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestChangedFilesUnknownPlatformUsesGitDiff(t *testing.T) {
	if _, err := (Env{}).ChangedFiles(); err == nil {
		t.Fatal("expected error with no base SHA or target branch")
	}
}

func TestGitDiffChangedFilesRejectsUnsafeRefs(t *testing.T) {
	tests := []struct {
		name string
		env  Env
	}{
		{"path traversal in branch", Env{TargetBranch: "../../etc"}},
		{"non-hex base SHA", Env{DiffBaseSHA: "not-a-sha"}},
		{"branch with shell metacharacters", Env{TargetBranch: "main; rm -rf /"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.env.gitDiffChangedFiles(); err == nil {
				t.Fatal("expected refusal, got nil error")
			}
		})
	}
}

// ---------- PostOrUpdateComment ----------

func TestPostOrUpdateCommentUnknownPlatform(t *testing.T) {
	if _, err := (Env{}).PostOrUpdateComment("body", "marker"); err == nil {
		t.Fatal("expected error for unknown platform")
	}
}

func TestGitLabPostOrUpdate(t *testing.T) {
	tests := []struct {
		name       string
		list       string
		wantMethod string
		wantURL    string
	}{
		{
			name:       "creates a new note",
			list:       `[]`,
			wantMethod: "POST",
			wantURL:    "https://gitlab.example.com/group/repo/-/merge_requests/42#note_501",
		},
		{
			name:       "updates the existing note",
			list:       `[{"id":501,"body":"old diff\n<!-- fleet-plan-marker -->"}]`,
			wantMethod: "PUT",
			wantURL:    "https://gitlab.example.com/group/repo/-/merge_requests/42#note_501",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var writeMethod, writeBody, writeCT string
			srv := insecureServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					fmt.Fprint(w, tt.list)
					return
				}
				writeMethod = r.Method
				writeCT = r.Header.Get("Content-Type")
				buf := make([]byte, r.ContentLength)
				_, _ = r.Body.Read(buf)
				writeBody = string(buf)
				fmt.Fprint(w, `{"id":501}`)
			})

			e := Env{
				Platform:        PlatformGitLab,
				GitLabAPIURL:    srv.URL,
				GitLabProjectID: "17",
				GitLabMRIID:     "42",
				GitLabMRURL:     "https://gitlab.example.com/group/repo/-/merge_requests/42",
				GitLabToken:     "t",
			}
			got, err := e.PostOrUpdateComment("the diff\n<!-- fleet-plan-marker -->", "fleet-plan-marker")
			if err != nil {
				t.Fatalf("PostOrUpdateComment: %v", err)
			}
			if got != tt.wantURL {
				t.Errorf("comment URL: got %q, want %q", got, tt.wantURL)
			}
			if writeMethod != tt.wantMethod {
				t.Errorf("write method: got %q, want %q", writeMethod, tt.wantMethod)
			}
			if writeCT != "application/x-www-form-urlencoded" {
				t.Errorf("Content-Type: got %q", writeCT)
			}
			if !strings.Contains(writeBody, "body=") {
				t.Errorf("form body missing body field: %q", writeBody)
			}
		})
	}
}

func TestGitLabPostOrUpdateRejectsBadResponse(t *testing.T) {
	tests := []struct {
		name  string
		write string
	}{
		{"missing id", `{}`},
		{"malformed JSON", `not json`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := insecureServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					fmt.Fprint(w, `[]`)
					return
				}
				fmt.Fprint(w, tt.write)
			})

			e := Env{Platform: PlatformGitLab, GitLabAPIURL: srv.URL, GitLabProjectID: "1", GitLabMRIID: "2", GitLabToken: "t"}
			if _, err := e.PostOrUpdateComment("body", "marker"); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestGitLabPostOrUpdateSkipsWhenNotReady(t *testing.T) {
	e := Env{Platform: PlatformGitLab, GitLabAPIURL: "https://x/api/v4", GitLabProjectID: "1", GitLabMRIID: "2"}
	_, err := e.PostOrUpdateComment("body", "marker")
	if err == nil || !strings.Contains(err.Error(), "skipping MR note") {
		t.Fatalf("expected skip error, got %v", err)
	}
}

func TestGitHubPostOrUpdate(t *testing.T) {
	tests := []struct {
		name       string
		list       string
		wantMethod string
		wantPath   string
	}{
		{
			name:       "creates a new comment",
			list:       `[]`,
			wantMethod: "POST",
			wantPath:   "/repos/CampusTech/fleet-plan/issues/7/comments",
		},
		{
			name:       "updates the existing comment",
			list:       `[{"id":88,"body":"old\n<!-- fleet-plan-marker -->"}]`,
			wantMethod: "PATCH",
			wantPath:   "/repos/CampusTech/fleet-plan/issues/comments/88",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var writeMethod, writePath, writeCT string
			var payload map[string]string
			srv := insecureServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					fmt.Fprint(w, tt.list)
					return
				}
				writeMethod, writePath = r.Method, r.URL.Path
				writeCT = r.Header.Get("Content-Type")
				_ = json.NewDecoder(r.Body).Decode(&payload)
				fmt.Fprint(w, `{"html_url":"https://github.com/CampusTech/fleet-plan/pull/7#issuecomment-88"}`)
			})

			e := Env{
				Platform:       PlatformGitHub,
				GitHubAPIURL:   srv.URL,
				GitHubRepo:     "CampusTech/fleet-plan",
				GitHubPRNumber: "7",
				GitHubToken:    "tok",
			}
			got, err := e.PostOrUpdateComment("the diff\n<!-- fleet-plan-marker -->", "fleet-plan-marker")
			if err != nil {
				t.Fatalf("PostOrUpdateComment: %v", err)
			}
			if got != "https://github.com/CampusTech/fleet-plan/pull/7#issuecomment-88" {
				t.Errorf("comment URL: got %q", got)
			}
			if writeMethod != tt.wantMethod {
				t.Errorf("write method: got %q, want %q", writeMethod, tt.wantMethod)
			}
			if writePath != tt.wantPath {
				t.Errorf("write path: got %q, want %q", writePath, tt.wantPath)
			}
			if writeCT != "application/json" {
				t.Errorf("Content-Type: got %q", writeCT)
			}
			if !strings.Contains(payload["body"], "the diff") {
				t.Errorf("payload body: got %q", payload["body"])
			}
		})
	}
}

func TestGitHubPostOrUpdateMissingHTMLURL(t *testing.T) {
	srv := insecureServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `{}`)
	})

	e := Env{Platform: PlatformGitHub, GitHubAPIURL: srv.URL, GitHubRepo: "o/r", GitHubPRNumber: "1", GitHubToken: "t"}
	if _, err := e.PostOrUpdateComment("body", "marker"); err == nil ||
		!strings.Contains(err.Error(), "missing html_url") {
		t.Fatalf("expected missing html_url error, got %v", err)
	}
}

func TestGitHubPostOrUpdateSkipsWhenNotReady(t *testing.T) {
	e := Env{Platform: PlatformGitHub, GitHubAPIURL: "https://api.github.com", GitHubRepo: "o/r", GitHubPRNumber: "1"}
	_, err := e.PostOrUpdateComment("body", "marker")
	if err == nil || !strings.Contains(err.Error(), "skipping PR comment") {
		t.Fatalf("expected skip error, got %v", err)
	}
}

func TestGitHubHeaders(t *testing.T) {
	h := githubHeaders("tok")
	if h["Authorization"] != "Bearer tok" {
		t.Errorf("Authorization: got %q", h["Authorization"])
	}
	if h["Accept"] != "application/vnd.github+json" {
		t.Errorf("Accept: got %q", h["Accept"])
	}
}
