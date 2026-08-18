//go:build ignore

// vhs-demo produces representative fleet-plan output for the VHS demo recording.
// Uses testdata fixtures and mock API state. Run via: go run ./assets/vhs-demo.go --team Workstations
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/CampusTech/fleet-plan/internal/api"
	"github.com/CampusTech/fleet-plan/internal/diff"
	"github.com/CampusTech/fleet-plan/internal/output"
	"github.com/CampusTech/fleet-plan/internal/parser"
)

func testdataRoot() string {
	_, f, _, ok := runtime.Caller(0)
	if !ok {
		log.Fatal("could not determine source file path")
	}
	dir := filepath.Dir(f)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			log.Fatal("could not find repo root (no go.mod)")
		}
		dir = parent
	}
}

func main() {
	lipgloss.SetColorProfile(termenv.ANSI256)

	flag.String("repo", ".", "ignored")
	team := flag.String("team", "Workstations", "team to diff: Workstations or Servers")
	flag.Parse()

	if *team == "" {
		*team = "Workstations"
	}

	root := testdataRoot()
	noDefault := filepath.Join(root, "_demo_no_default.yml")

	var teamFilter []string
	if *team != "" {
		teamFilter = []string{*team}
	}
	proposed, err := parser.ParseRepo(root, teamFilter, noDefault)
	if err != nil {
		log.Fatalf("ParseRepo: %v", err)
	}
	for ti := range proposed.Teams {
		for pi := range proposed.Teams[ti].Policies {
			proposed.Teams[ti].Policies[pi].LabelsIncludeAny = nil
			proposed.Teams[ti].Policies[pi].LabelsExcludeAny = nil
		}
	}

	fmt.Fprintf(os.Stderr, "Fetching Fleet state from https://fleet.example.com...\n")
	time.Sleep(1200 * time.Millisecond)

	// Mock: Servers exists, Workstations does not.
	current := &api.FleetState{
		Teams: []api.Team{{
			ID:   2,
			Name: "Servers",
			Queries: []api.Query{
				{Name: "System Uptime", Query: "SELECT total_seconds, days, hours FROM uptime;", Interval: 3600, Platform: "darwin,windows,linux"},
			},
		}},
	}

	results := diff.Diff(current, proposed, teamFilter, nil)
	fmt.Println(output.RenderDiffTerminal(results, false))
	fmt.Fprintf(os.Stderr, "Completed in 1.247s\n")
}
