package diff

import (
	"context"
	"strings"
	"testing"

	"github.com/CampusTech/fleet-plan/internal/api"
	"github.com/CampusTech/fleet-plan/internal/parser"
)

// failingEnricher marks every app's title detail as unreadable.
type failingEnricher struct {
	forbidden bool
	err       string
}

func (f failingEnricher) EnrichFleetAppScripts(_ context.Context, apps []api.TeamFleetApp) {
	for i := range apps {
		apps[i].DetailUnavailable = true
		apps[i].DetailForbidden = f.forbidden
		apps[i].DetailError = f.err
	}
}

// The categories warning appeared on every Workstations plan, blamed token
// permissions even for an admin token, and fired when the YAML set no
// categories at all.
func TestDiffFleetMaintainedDetailWarning(t *testing.T) {
	tests := []struct {
		name       string
		categories []string
		enricher   failingEnricher
		want       string // substring; "" means no warning
		notWant    string
	}{
		{
			name:     "yaml sets no categories",
			enricher: failingEnricher{forbidden: true, err: "403"},
		},
		{
			name:       "forbidden",
			categories: []string{"Browsers"},
			enricher:   failingEnricher{forbidden: true, err: "fetching software title 308: HTTP 403"},
			want:       "lacks permission",
		},
		{
			name:       "other failure keeps its cause",
			categories: []string{"Browsers"},
			enricher:   failingEnricher{err: "fetching software title 308: HTTP 500"},
			want:       "HTTP 500",
			notWant:    "permission",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := &api.FleetState{
				FleetMaintainedCatalog: []api.FleetMaintainedApp{
					{Slug: "google-chrome/darwin", Name: "Google Chrome", Platform: "darwin", SoftwareTitleID: 308},
				},
				Teams: []api.Team{{
					ID: 6, Name: "Workstations",
					SoftwareTitles: []api.SoftwareTitle{
						{ID: 308, Name: "Google Chrome", Source: "apps", SoftwarePackage: &api.SoftwareTitlePackageMeta{Platform: "darwin"}},
					},
				}},
			}
			proposed := &parser.ParsedRepo{Teams: []parser.ParsedTeam{{
				Name: "Workstations",
				Software: parser.ParsedSoftware{FleetMaintained: []parser.ParsedFleetApp{
					{Slug: "google-chrome/darwin", Categories: tt.categories},
				}},
			}}}

			r := Diff(current, proposed, nil, nil, WithScriptEnricher(tt.enricher))[0]
			var warn string
			for _, e := range r.Errors {
				if strings.Contains(e, "categories") {
					warn = e
				}
			}
			if tt.want == "" {
				if warn != "" {
					t.Fatalf("expected no warning, got %q", warn)
				}
				return
			}
			if !strings.Contains(warn, tt.want) {
				t.Errorf("warning %q should contain %q", warn, tt.want)
			}
			if tt.notWant != "" && strings.Contains(warn, tt.notWant) {
				t.Errorf("warning %q should not contain %q", warn, tt.notWant)
			}
		})
	}
}
