package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAuth(t *testing.T) {
	tests := []struct {
		name      string
		flagURL   string
		flagToken string
		envURL    string
		envToken  string
		wantURL   string
		wantToken string
		wantErr   bool
	}{
		{
			name:      "flags take priority",
			flagURL:   "https://flag.example.com",
			flagToken: "flagtoken",
			wantURL:   "https://flag.example.com",
			wantToken: "flagtoken",
		},
		{
			name:      "env vars fill gaps",
			envURL:    "https://env.example.com",
			envToken:  "envtoken",
			wantURL:   "https://env.example.com",
			wantToken: "envtoken",
		},
		{
			name:      "flag URL + env token",
			flagURL:   "https://flag.example.com",
			envToken:  "envtoken",
			wantURL:   "https://flag.example.com",
			wantToken: "envtoken",
		},
		{
			name:      "env URL + flag token",
			envURL:    "https://env.example.com",
			flagToken: "flagtoken",
			wantURL:   "https://env.example.com",
			wantToken: "flagtoken",
		},
		{
			name:    "missing both errors",
			wantErr: true,
		},
		{
			name:    "missing token errors",
			flagURL: "https://flag.example.com",
			wantErr: true,
		},
		{
			name:      "missing URL errors",
			flagToken: "flagtoken",
			wantErr:   true,
		},
		{
			name:      "flags override env vars",
			flagURL:   "https://flag.example.com",
			flagToken: "flagtoken",
			envURL:    "https://env.example.com",
			envToken:  "envtoken",
			wantURL:   "https://flag.example.com",
			wantToken: "flagtoken",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvURL, tt.envURL)
			t.Setenv(EnvToken, tt.envToken)
			t.Setenv("HOME", t.TempDir()) // prevent $HOME config file from interfering

			auth, err := ResolveAuth(tt.flagURL, tt.flagToken)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if auth.URL != tt.wantURL {
				t.Errorf("URL: got %q, want %q", auth.URL, tt.wantURL)
			}
			if auth.Token != tt.wantToken {
				t.Errorf("Token: got %q, want %q", auth.Token, tt.wantToken)
			}
		})
	}
}

func TestResolveAuthConfigFile(t *testing.T) {
	tests := []struct {
		name      string
		json      string
		envURL    string
		envToken  string
		wantURL   string
		wantToken string
		wantErr   bool
	}{
		{
			name:      "flat format fills both gaps",
			json:      `{"url":"https://file.example.com","token":"filetoken"}`,
			wantURL:   "https://file.example.com",
			wantToken: "filetoken",
		},
		{
			name:      "contexts format with default_context",
			json:      `{"contexts":{"dev":{"url":"https://dev.example.com","token":"devtoken"},"prod":{"url":"https://prod.example.com","token":"prodtoken"}},"default_context":"dev"}`,
			wantURL:   "https://dev.example.com",
			wantToken: "devtoken",
		},
		{
			name:      "env vars override config file",
			json:      `{"url":"https://file.example.com","token":"filetoken"}`,
			envURL:    "https://env.example.com",
			envToken:  "envtoken",
			wantURL:   "https://env.example.com",
			wantToken: "envtoken",
		},
		{
			name:      "config file fills token gap only",
			json:      `{"url":"https://file.example.com","token":"filetoken"}`,
			envURL:    "https://env.example.com",
			wantURL:   "https://env.example.com",
			wantToken: "filetoken",
		},
		{
			name:    "invalid json ignored",
			json:    `{not valid json`,
			wantErr: true,
		},
		{
			name:    "empty config file",
			json:    `{}`,
			wantErr: true,
		},
		{
			name:    "contexts format with missing default_context",
			json:    `{"contexts":{"dev":{"url":"https://dev.example.com","token":"devtoken"}},"default_context":"nonexistent"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvURL, tt.envURL)
			t.Setenv(EnvToken, tt.envToken)
			t.Setenv("HOME", t.TempDir()) // prevent $HOME config file from interfering

			root := t.TempDir()
			configDir := filepath.Join(root, ".config")
			os.MkdirAll(configDir, 0o755)
			os.WriteFile(filepath.Join(configDir, "fleet-plan.json"), []byte(tt.json), 0o644)

			auth, err := ResolveAuth("", "", root)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if auth.URL != tt.wantURL {
				t.Errorf("URL: got %q, want %q", auth.URL, tt.wantURL)
			}
			if auth.Token != tt.wantToken {
				t.Errorf("Token: got %q, want %q", auth.Token, tt.wantToken)
			}
		})
	}
}

func TestLoadConfigFileMissing(t *testing.T) {
	cfg := loadConfigFile(t.TempDir())
	u, tok := cfg.resolve()
	if u != "" || tok != "" {
		t.Errorf("expected empty config for missing file, got url=%q token=%q", u, tok)
	}
}

func TestConfigFileResolve(t *testing.T) {
	tests := []struct {
		name      string
		cfg       configFile
		wantURL   string
		wantToken string
	}{
		{
			name:      "flat format",
			cfg:       configFile{URL: "https://fleet.example.com", Token: "tok"},
			wantURL:   "https://fleet.example.com",
			wantToken: "tok",
		},
		{
			name: "contexts format uses default_context",
			cfg: configFile{
				Contexts: map[string]configFileContext{
					"dev":  {URL: "https://dev.example.com", Token: "devtok"},
					"prod": {URL: "https://prod.example.com", Token: "prodtok"},
				},
				DefaultContext: "dev",
			},
			wantURL:   "https://dev.example.com",
			wantToken: "devtok",
		},
		{
			name: "contexts format selects prod",
			cfg: configFile{
				Contexts: map[string]configFileContext{
					"dev":  {URL: "https://dev.example.com", Token: "devtok"},
					"prod": {URL: "https://prod.example.com", Token: "prodtok"},
				},
				DefaultContext: "prod",
			},
			wantURL:   "https://prod.example.com",
			wantToken: "prodtok",
		},
		{
			name: "flat takes priority over contexts",
			cfg: configFile{
				URL:   "https://fleet.example.com",
				Token: "flattok",
				Contexts: map[string]configFileContext{
					"dev": {URL: "https://dev.example.com", Token: "devtok"},
				},
				DefaultContext: "dev",
			},
			wantURL:   "https://fleet.example.com",
			wantToken: "flattok",
		},
		{
			name: "missing default context returns empty",
			cfg: configFile{
				Contexts: map[string]configFileContext{
					"dev": {URL: "https://dev.example.com", Token: "devtok"},
				},
				DefaultContext: "nonexistent",
			},
		},
		{
			name: "empty config",
			cfg:  configFile{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, tok := tt.cfg.resolve()
			if u != tt.wantURL {
				t.Errorf("URL: got %q, want %q", u, tt.wantURL)
			}
			if tok != tt.wantToken {
				t.Errorf("Token: got %q, want %q", tok, tt.wantToken)
			}
		})
	}
}
