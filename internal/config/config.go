// Package config resolves fleet-plan authentication.
// Priority: flags > env vars > config file (~/.config/fleet-plan.json or <repo>/.config/fleet-plan.json).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// EnvURL overrides the Fleet server URL.
	EnvURL = "FLEET_URL"
	// EnvToken overrides the API token.
	EnvToken = "FLEET_TOKEN"

	// ConfigRelPath is the config file path relative to a root directory.
	ConfigRelPath = ".config/fleet-plan.json"
)

// ResolvedAuth contains the final URL and token after priority resolution.
type ResolvedAuth struct {
	URL   string
	Token string
}

// configFile supports both flat and contexts-based JSON formats:
//
//	Flat:     {"url":"...","token":"..."}
//	Contexts: {"contexts":{"dev":{...},"prod":{...}},"default_context":"dev"}
type configFile struct {
	URL            string                       `json:"url"`
	Token          string                       `json:"token"`
	Contexts       map[string]configFileContext `json:"contexts"`
	DefaultContext string                       `json:"default_context"`
}

type configFileContext struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// resolve returns the effective URL and token, handling both flat and contexts formats.
func (c configFile) resolve() (string, string) {
	if c.URL != "" || c.Token != "" {
		return c.URL, c.Token
	}
	if c.DefaultContext != "" {
		if ctx, ok := c.Contexts[c.DefaultContext]; ok {
			return ctx.URL, ctx.Token
		}
	}
	return "", ""
}

// loadConfigFile reads .config/fleet-plan.json from the given directory.
// Returns zero values if the file doesn't exist or can't be parsed.
func loadConfigFile(root string) configFile {
	path := filepath.Join(root, ConfigRelPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return configFile{}
	}
	if info, err := os.Stat(path); err == nil {
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			fmt.Fprintf(os.Stderr, "warning: %s is readable by others (mode %04o), consider chmod 600\n", path, mode)
		}
	}
	var cfg configFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return configFile{}
	}
	return cfg
}

// ResolveAuth resolves auth with priority: flags > env vars > config file.
// Config file is searched in: 1) repoRoot/.config/ 2) $HOME/.config/
func ResolveAuth(flagURL, flagToken string, repoRoot ...string) (*ResolvedAuth, error) {
	url := flagURL
	token := flagToken

	// Env vars fill in gaps
	if url == "" {
		url = os.Getenv(EnvURL)
	}
	if token == "" {
		token = os.Getenv(EnvToken)
	}

	// Config file fills remaining gaps (repo root first, then home dir)
	if url == "" || token == "" {
		cfgURL, cfgToken := findConfigAuth(repoRoot...)
		if url == "" {
			url = cfgURL
		}
		if token == "" {
			token = cfgToken
		}
	}

	if url == "" {
		return nil, fmt.Errorf("missing Fleet server URL (--url or $%s)", EnvURL)
	}
	if token == "" {
		return nil, fmt.Errorf("API token required (--token or $%s)", EnvToken)
	}

	return &ResolvedAuth{URL: url, Token: token}, nil
}

// findConfigAuth checks repo root then $HOME for a config file.
func findConfigAuth(repoRoot ...string) (string, string) {
	// Try repo root first
	if len(repoRoot) > 0 && repoRoot[0] != "" {
		cfg := loadConfigFile(repoRoot[0])
		if u, t := cfg.resolve(); u != "" || t != "" {
			return u, t
		}
	}
	// Fall back to $HOME
	if home, err := os.UserHomeDir(); err == nil {
		cfg := loadConfigFile(home)
		return cfg.resolve()
	}
	return "", ""
}
