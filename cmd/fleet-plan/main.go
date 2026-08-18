// fleet-plan: terraform plan, but for your device fleet.
//
// A strictly read-only CLI that diffs proposed Fleet GitOps YAML against
// current Fleet state. It never mutates Fleet. GET requests only.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/CampusTech/fleet-plan/internal/api"
	"github.com/CampusTech/fleet-plan/internal/config"
	"github.com/CampusTech/fleet-plan/internal/diff"
	"github.com/CampusTech/fleet-plan/internal/git"
	"github.com/CampusTech/fleet-plan/internal/merge"
	"github.com/CampusTech/fleet-plan/internal/output"
	"github.com/CampusTech/fleet-plan/internal/parser"
	"github.com/CampusTech/fleet-plan/internal/teamdir"
)

// Set via -ldflags at build time.
var (
	version   = "dev"
	buildDate = "unknown"
	goVersion = "unknown"
)

// Flags.
var (
	flagURL              string
	flagToken            string
	flagRepo             string
	flagFormat           string
	flagNoColor          bool
	flagVerbose          bool
	flagTeams            []string
	flagHeading          string
	flagDetailedExitCode bool

	// --git mode flags.
	flagGit  bool
	flagBase string
	flagEnv  string
)

// buildRootCmd constructs the root cobra.Command with all subcommands and flags.
// Extracted from main() so tests can call it without os.Exit.
func buildRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "fleet-plan",
		Short: "terraform plan, but for your device fleet",
		Long: `fleet-plan diffs proposed Fleet GitOps YAML against current Fleet state.
Strictly read-only -- GET requests only.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runDiff,
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flagURL, "url", "", "Fleet server URL (or $FLEET_URL)")
	pf.StringVar(&flagToken, "token", "", "API token (or $FLEET_TOKEN)")
	pf.StringVar(&flagRepo, "repo", ".", "path to fleet-gitops repo")
	pf.StringVarP(&flagFormat, "format", "f", "terminal", "output format: terminal, json, markdown")
	pf.BoolVar(&flagNoColor, "no-color", false, "disable color output")
	pf.BoolVarP(&flagVerbose, "verbose", "v", false, "show full old/new values for modified fields")
	pf.StringSliceVar(&flagTeams, "team", nil, "diff only these teams (repeatable, default: all)")
	pf.StringVar(&flagHeading, "heading", "", "## heading for markdown output")
	pf.BoolVar(&flagDetailedExitCode, "detailed-exitcodes", false, "exit 2 when changes detected (0=no changes, 1=error, 2=changes)")

	// --git mode.
	pf.BoolVar(&flagGit, "git", false, "enable CI mode: auto-detect changed files, infer affected teams, post MR/PR comment")
	pf.StringVar(&flagBase, "base", "", "path to base.yml for multi-env config merge (use with --env)")
	pf.StringVar(&flagEnv, "env", "", "path to environment overlay YAML, merged with --base in-memory")

	root.AddCommand(versionCmd())

	return root
}

func runDiff(cmd *cobra.Command, _ []string) error {
	start := time.Now()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	auth, err := config.ResolveAuth(flagURL, flagToken, flagRepo)
	if err != nil {
		return err
	}

	info, err := os.Stat(flagRepo)
	if err != nil {
		return fmt.Errorf("repo path %q does not exist: %w", flagRepo, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repo path %q is not a directory", flagRepo)
	}

	// Resolve the default.yml path: merge base+env if provided, else auto-detect.
	defaultFile, cleanup, err := resolveDefaultFile(flagRepo, flagBase, flagEnv)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	// --git: detect CI context, resolve changed files + affected teams.
	var changedFiles []string
	var ci git.Env
	var includeGlobal bool
	teams := flagTeams

	if flagGit {
		ci = git.Detect()
		resolved, skip := resolveCIScope(ci, flagRepo, flagEnv, &defaultFile, teams)
		if skip {
			return nil
		}
		changedFiles = resolved.ChangedFiles
		includeGlobal = resolved.IncludeGlobal
		if len(resolved.Teams) > 0 && len(teams) == 0 {
			teams = resolved.Teams
		}
	}

	repo, err := parser.ParseRepo(flagRepo, teams, defaultFile)
	if err != nil {
		return fmt.Errorf("parsing repo: %w", err)
	}

	if len(repo.Teams) == 0 && len(repo.Errors) == 0 {
		dirName := teamdir.Resolve(flagRepo)
		if len(teams) > 0 {
			return fmt.Errorf("no teams matching %v found in %s/%s/", teams, flagRepo, dirName)
		}
		return fmt.Errorf("no teams found in %s/%s/\nAre you in a fleet-gitops repo? Try --repo /path/to/repo", flagRepo, dirName)
	}

	// Parse baseline (base branch) for subtraction when in --git mode.
	var baseline *parser.ParsedRepo
	if flagGit && len(changedFiles) > 0 && ci.DiffBaseSHA != "" {
		baseRoot, baseCleanup, err := git.CheckoutBaseline(flagRepo, ci.DiffBaseSHA, changedFiles)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not extract baseline (%v), skipping baseline subtraction\n", err)
		} else {
			defer baseCleanup()
			baseDefaultFile := resolveBaselineDefault(baseRoot, flagRepo, flagBase, flagEnv)
			baseParsed, err := parser.ParseRepo(baseRoot, teams, baseDefaultFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not parse baseline (%v), skipping baseline subtraction\n", err)
			} else {
				baseline = baseParsed
			}
		}
	}

	client, err := api.NewClient(auth.URL, auth.Token)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Fetching Fleet state from %s...\n", auth.URL)

	state, err := client.FetchAll(ctx, repo.Global != nil)
	if err != nil {
		return err
	}

	diffOpts := []diff.DiffOption{diff.WithScriptEnricher(client), diff.WithVerbose(flagVerbose), diff.WithIncludeGlobal(includeGlobal)}
	if baseline != nil {
		diffOpts = append(diffOpts, diff.WithBaseline(baseline))
	}
	results := diff.Diff(state, repo, teams, changedFiles, diffOpts...)
	elapsed := time.Since(start)

	hasChanges := output.HasChanges(results)

	const marker = "fleet-plan-marker"

	switch flagFormat {
	case "json":
		out, err := output.RenderDiffJSON(results)
		if err != nil {
			return err
		}
		fmt.Println(out)
	case "markdown":
		heading := flagHeading
		if heading == "" && flagGit {
			heading = buildHeading(auth.URL)
		}

		mdBody := output.RenderDiffMarkdown(results, output.MarkdownOptions{
			Heading: heading,
			Marker:  marker,
			JobURL:  ci.JobURL(),
		})
		fmt.Println(mdBody)

		if flagGit && ci.Platform != git.PlatformUnknown {
			commentURL, err := ci.PostOrUpdateComment(mdBody, marker)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not post MR comment: %v\n", err)
				fmt.Fprintln(os.Stderr, "The diff is printed above.")
			} else {
				fmt.Fprintf(os.Stderr, "MR comment posted: %s\n", commentURL)
			}
		}
	default:
		fmt.Println(output.RenderDiffTerminal(results, flagVerbose))
	}

	fmt.Fprintf(os.Stderr, "Completed in %s\n", elapsed.Round(time.Millisecond))

	if flagDetailedExitCode && hasChanges {
		// Sentinel rather than os.Exit(2) here: exiting from runDiff skips the
		// deferred cleanup of the temp merged config file.
		return errChangesDetected
	}

	return nil
}

// errChangesDetected signals --detailed-exitcodes exit status 2. It is not a
// failure: main translates it to the exit code after runDiff's defers run.
var errChangesDetected = errors.New("changes detected")

// resolveDefaultFile returns the path to default.yml to pass to ParseRepo.
// If base+env are provided, they are merged into a temp file and a cleanup
// func is returned to delete it. If neither is provided, returns empty string
// (ParseRepo will auto-detect).
func resolveDefaultFile(repo, base, env string) (path string, cleanup func(), err error) {
	if base == "" && env == "" {
		return "", nil, nil
	}
	if base == "" || env == "" {
		return "", nil, fmt.Errorf("--base and --env must be used together")
	}

	// Resolve relative to repo root if not absolute.
	if !filepath.IsAbs(base) {
		base = filepath.Join(repo, base)
	}
	if !filepath.IsAbs(env) {
		env = filepath.Join(repo, env)
	}

	// Place the merged file inside the repo so the parser resolves path:
	// references (./queries/...) relative to the repo root, not /tmp.
	tmp, err := os.CreateTemp(repo, ".fleet-plan-default-*.yml")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp file for merged config: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()

	if err := merge.MergeFiles(base, env, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", nil, fmt.Errorf("merging base+env: %w", err)
	}

	return tmpPath, func() { _ = os.Remove(tmpPath) }, nil
}

// resolveBaselineDefault returns the path to default.yml for baseline parsing.
// When --base and --env are used, it merges the base branch's base.yml with
// the env overlay (from the MR branch, since env overlays rarely change).
// Falls back to the baseline's default.yml if present.
func resolveBaselineDefault(baseRoot, repoRoot, base, env string) string {
	if base != "" && env != "" {
		baseFile := filepath.Join(baseRoot, base)
		if _, err := os.Stat(baseFile); err != nil {
			// base.yml doesn't exist at base ref, skip
			return ""
		}
		// Use the env overlay from the MR branch (repoRoot), not the baseline.
		envFile := env
		if !filepath.IsAbs(envFile) {
			envFile = filepath.Join(repoRoot, envFile)
		}
		if _, err := os.Stat(envFile); err != nil {
			return ""
		}
		// Place the merged file inside the baseline temp dir so the parser
		// resolves path: refs (./queries/...) relative to the baseline root.
		tmpPath := filepath.Join(baseRoot, "default.yml")
		if err := merge.MergeFiles(baseFile, envFile, tmpPath); err != nil {
			return ""
		}
		return tmpPath
	}
	// No base+env: check for plain default.yml in the baseline.
	candidate := filepath.Join(baseRoot, "default.yml")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// buildHeading returns the default CI heading using the Fleet server URL.
func buildHeading(fleetURL string) string {
	display := strings.TrimPrefix(fleetURL, "https://")
	return fmt.Sprintf("Planned changes for [%s](%s)", display, fleetURL)
}

// resolveCIScope detects the CI platform, fetches changed files, and resolves
// affected teams. Returns the scope and whether the caller should skip the diff
// (no fleet-relevant files changed). Updates defaultFile in place if global
// config is affected and no default was explicitly provided.
func resolveCIScope(ci git.Env, repo, envFile string, defaultFile *string, explicitTeams []string) (git.Scope, bool) {
	if ci.Platform == git.PlatformUnknown {
		fmt.Fprintln(os.Stderr, "Warning: --git specified but no CI MR/PR context detected; running full diff")
		return git.Scope{}, false
	}

	files, err := ci.ChangedFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not determine changed files (%v); running full diff\n", err)
		return git.Scope{}, false
	}

	scope := git.ResolveScope(repo, files, envFile)
	if !scope.IncludeGlobal && len(scope.Teams) == 0 {
		fmt.Fprintln(os.Stderr, "No fleet-relevant files changed in this MR, skipping diff.")
		return scope, true
	}

	if scope.IncludeGlobal && *defaultFile == "" {
		candidate := filepath.Join(repo, "default.yml")
		if _, err := os.Stat(candidate); err == nil {
			*defaultFile = candidate
		}
	}

	if len(scope.Teams) > 0 && len(explicitTeams) == 0 {
		for _, t := range scope.Teams {
			fmt.Fprintf(os.Stderr, "Affected team: %s\n", t)
		}
	}

	return scope, false
}

func main() {
	// NotifyContext so an interrupt cancels the in-flight Fleet fetches and
	// unwinds through runDiff's defers, which delete the temp merged config
	// file that --base/--env writes inside the repo.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := buildRootCmd().ExecuteContext(ctx); err != nil {
		if errors.Is(err, errChangesDetected) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
