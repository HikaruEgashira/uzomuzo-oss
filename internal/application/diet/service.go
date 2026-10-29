// Package diet orchestrates the 4-phase dependency diet analysis pipeline.
package diet

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/future-architect/uzomuzo-oss/internal/application"
	domain "github.com/future-architect/uzomuzo-oss/internal/domain/analysis"
	domaindiet "github.com/future-architect/uzomuzo-oss/internal/domain/diet"
	"github.com/package-url/packageurl-go"
)

// SourceAnalyzer abstracts static analysis of source code against dependencies.
type SourceAnalyzer interface {
	// AnalyzeCoupling scans the source tree and returns coupling data per PURL.
	// importPaths maps PURL -> []string of import paths for that ecosystem.
	AnalyzeCoupling(ctx context.Context, sourceRoot string, importPaths map[string][]string) (map[string]*domaindiet.CouplingAnalysis, error)
	// PyPIProjectNames returns package names declared by [project].name tables.
	PyPIProjectNames(ctx context.Context, sourceRoot string) ([]string, error)
	// AnalyzePyPICommandUsage matches installed console scripts to executable source configuration.
	AnalyzePyPICommandUsage(ctx context.Context, sourceRoot string, scripts, importPaths map[string][]string) (map[string]struct{}, error)
	// NpmPeerDependencies returns required peer names for direct npm packages in the source tree.
	NpmPeerDependencies(ctx context.Context, sourceRoot string, directDeps []string) (map[string][]string, error)
}

// GraphAnalyzer abstracts dependency graph analysis from an SBOM.
type GraphAnalyzer interface {
	AnalyzeGraph(ctx context.Context, sbomData []byte) (*domaindiet.GraphResult, error)
}

// PyPIImportResolver resolves import names and console scripts from local or wheel metadata.
// This is the fallback used when heuristic import path guessing fails.
type PyPIImportResolver interface {
	// ResolveLocalDistributionMetadata reads import names and console scripts from a local virtualenv.
	ResolveLocalDistributionMetadata(ctx context.Context, sourceRoot, packageName string) (imports, scripts []string, err error)
	// ResolveDistributionMetadata fetches Python import names and console scripts
	// by downloading and inspecting the smallest wheel file.
	// It may return an error if metadata lookup, wheel download, or inspection
	// fails. Callers should treat such errors as non-fatal fallback failures
	// (graceful degradation) and continue with heuristic guesses or an empty
	// result when appropriate.
	ResolveDistributionMetadata(ctx context.Context, packageName string) (imports, scripts []string, err error)
}

// DietInput contains the inputs for a diet analysis run.
type DietInput struct {
	SBOMData          []byte
	SBOMPath          string
	SourceRoot        string // empty = skip source analysis
	Offline           bool
	PrivatePatterns   string
	GoPrivatePatterns string
	// ToolDeps is a set of module paths declared in go.mod tool directives
	// (Go 1.24+). These are dev/CI executables that intentionally have zero
	// source imports and should not be flagged as unused.
	ToolDeps map[string]struct{}
	// GoIndirectDeps contains normalized module names required only indirectly.
	GoIndirectDeps map[string]struct{}
}

// Service orchestrates the 4-phase diet pipeline.
type Service struct {
	graphAnalyzer   GraphAnalyzer
	sourceAnalyzer  SourceAnalyzer     // nil = skip source analysis
	pypiResolver    PyPIImportResolver // nil = skip wheel fallback
	analysisService *application.AnalysisService
}

// NewService creates a new diet service.
func NewService(
	graphAnalyzer GraphAnalyzer,
	sourceAnalyzer SourceAnalyzer,
	pypiResolver PyPIImportResolver,
	analysisService *application.AnalysisService,
) *Service {
	return &Service{
		graphAnalyzer:   graphAnalyzer,
		sourceAnalyzer:  sourceAnalyzer,
		pypiResolver:    pypiResolver,
		analysisService: analysisService,
	}
}

// Run executes the full 4-phase diet pipeline.
func (s *Service) Run(ctx context.Context, input DietInput) (*domaindiet.DietPlan, error) {
	patterns, err := validatePrivatePatterns(input.PrivatePatterns)
	if err != nil {
		return nil, err
	}
	goPatterns, err := validatePrivatePatterns(input.GoPrivatePatterns)
	if err != nil {
		return nil, err
	}
	// Phase 1: Graph analysis
	slog.Info("Phase 1: Analyzing dependency graph from SBOM")
	graphResult, err := s.graphAnalyzer.AnalyzeGraph(ctx, input.SBOMData)
	if err != nil {
		return nil, fmt.Errorf("graph analysis failed: %w", err)
	}
	var projectNames []string
	if s.sourceAnalyzer != nil && input.SourceRoot != "" {
		projectNames, err = s.sourceAnalyzer.PyPIProjectNames(ctx, input.SourceRoot)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("reading Python project names: %w", err)
			}
			slog.Warn("failed to read Python project names; continuing without workspace filtering", "error", err)
		}
	}
	filteredDirectDeps, selfDeps := filterPyPIProjectDeps(graphResult.DirectDeps, graphResult.RootPURL, projectNames)
	graphResult.DirectDeps = filteredDirectDeps
	pythonProjectDepsFiltered := len(selfDeps)
	for _, purl := range selfDeps {
		delete(graphResult.Metrics, purl)
	}
	// Filter workspace-local packages (npm monorepo internals) before analysis.
	preFilterCount := len(graphResult.DirectDeps)
	graphResult.DirectDeps = filterWorkspaceDeps(graphResult.DirectDeps)
	workspaceFiltered := preFilterCount - len(graphResult.DirectDeps)
	goIndirectFiltered := 0
	kept := graphResult.DirectDeps[:0]
	for _, purl := range graphResult.DirectDeps {
		name, ecosystem, _ := parsePURLParts(purl)
		if _, excluded := input.GoIndirectDeps[strings.ToLower(name)]; ecosystem == "golang" && excluded {
			goIndirectFiltered++
			delete(graphResult.Metrics, purl)
			continue
		}
		kept = append(kept, purl)
	}
	graphResult.DirectDeps = kept
	var npmPeerDeps map[string][]string
	if s.sourceAnalyzer != nil && input.SourceRoot != "" {
		npmPeerDeps, err = s.sourceAnalyzer.NpmPeerDependencies(ctx, input.SourceRoot, graphResult.DirectDeps)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("reading npm peer dependencies: %w", err)
			}
			slog.Warn("failed to read npm peer dependencies; continuing without peer scope", "error", err)
		}
	}
	private := make(map[string]bool)
	public := make([]string, 0, len(graphResult.DirectDeps))
	for _, purl := range graphResult.DirectDeps {
		name, ecosystem, _ := parsePURLParts(purl)
		private[purl] = matchesPrivate(patterns, name) || (ecosystem == "golang" && matchesPrivate(goPatterns, name))
		if !private[purl] {
			public = append(public, purl)
		}
	}

	slog.Info("Phase 1 complete",
		"direct", len(graphResult.DirectDeps),
		"totalTransitive", graphResult.TotalTransitive,
		"workspaceDepsFiltered", workspaceFiltered,
		"pythonProjectDepsFiltered", pythonProjectDepsFiltered,
		"go_indirect_filtered", goIndirectFiltered,
	)

	// Phase 2 & 3: run concurrently (both only depend on graphResult from Phase 1)
	var couplingResults map[string]*domaindiet.CouplingAnalysis
	var healthResults map[string]*domain.Analysis
	var cliDeps map[string]struct{}
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		// Phase 2: Static analysis (optional)
		if s.sourceAnalyzer != nil && input.SourceRoot != "" {
			slog.Info("Phase 2: Analyzing source code coupling", "source", input.SourceRoot)
			importPaths := buildImportPaths(graphResult.DirectDeps)
			var couplingErr error
			couplingResults, couplingErr = s.sourceAnalyzer.AnalyzeCoupling(ctx, input.SourceRoot, importPaths)
			if couplingErr != nil {
				slog.Warn("Phase 2 failed, continuing without coupling data", "error", couplingErr)
				couplingResults = nil
			} else if len(couplingResults) == 0 {
				slog.Warn("Phase 2: no imports matched any dependency — verify --source points to the correct directory", "source", input.SourceRoot)
			} else {
				slog.Info("Phase 2 complete", "analyzed", len(couplingResults))
			}

			// Phase 2.5: Local/wheel metadata fallback for unmatched PyPI packages.
			// Gate on couplingErr (not couplingResults) because AnalyzeCoupling
			// returns (nil, nil) when no imports matched — that nil map is exactly
			// the scenario the wheel fallback should recover from.
			if couplingErr == nil {
				retryPaths, unresolved, detectedCLIDeps := s.resolveUnmatchedPyPI(
					ctx, input.SourceRoot, graphResult.DirectDeps, public, couplingResults, input.Offline,
				)
				cliDeps = detectedCLIDeps
				if len(retryPaths) > 0 {
					slog.Info("Phase 2.5: Retrying coupling with resolved import names", "count", len(retryPaths))

					// Build combined importPaths (original heuristic + metadata-resolved)
					// so AnalyzeCoupling sees the full dependency set and collision
					// attribution stays consistent with Phase 2.
					combinedImportPaths := make(map[string][]string, len(importPaths))
					for purl, paths := range importPaths {
						copiedPaths := make([]string, len(paths))
						copy(copiedPaths, paths)
						combinedImportPaths[purl] = copiedPaths
					}
					for purl, paths := range retryPaths {
						seen := make(map[string]struct{}, len(combinedImportPaths[purl])+len(paths))
						mergedPaths := make([]string, 0, len(combinedImportPaths[purl])+len(paths))
						for _, p := range combinedImportPaths[purl] {
							if _, ok := seen[p]; ok {
								continue
							}
							seen[p] = struct{}{}
							mergedPaths = append(mergedPaths, p)
						}
						for _, p := range paths {
							if _, ok := seen[p]; ok {
								continue
							}
							seen[p] = struct{}{}
							mergedPaths = append(mergedPaths, p)
						}
						combinedImportPaths[purl] = mergedPaths
					}

					retryCoupling, retryErr := s.sourceAnalyzer.AnalyzeCoupling(ctx, input.SourceRoot, combinedImportPaths)
					if retryErr != nil {
						slog.Warn("Phase 2.5 failed, continuing with heuristic results", "error", retryErr)
						for purl := range retryPaths {
							unresolved[purl] = struct{}{}
						}
					} else {
						couplingResults = retryCoupling
						slog.Info("Phase 2.5 complete", "resolved", len(retryCoupling))
					}
				}
				couplingResults = markPyPIUnanalyzed(couplingResults, unresolved)
			}
		} else {
			slog.Info("Phase 2: Skipped (no source root provided)")
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		// Phase 3: Health signals
		if input.Offline {
			slog.Info("Phase 3: Skipped (offline)")
			return
		}
		slog.Info("Phase 3: Fetching health signals", "count", len(public))
		healthResults = make(map[string]*domain.Analysis)
		if s.analysisService != nil && len(public) > 0 {
			var healthErr error
			healthResults, healthErr = s.analysisService.ProcessBatchPURLs(ctx, public)
			if healthErr != nil {
				slog.Warn("Health signal fetch failed, continuing without health data", "error", healthErr)
				healthResults = make(map[string]*domain.Analysis)
			}
		}
		slog.Info("Phase 3 complete", "fetched", len(healthResults))
	}()

	wg.Wait()
	failedMaliciousChecks := 0
	for _, a := range healthResults {
		if a != nil && a.MaliciousState != nil && a.MaliciousState.Status == domain.MaliciousStatusLookupFailed {
			failedMaliciousChecks++
		}
	}
	if failedMaliciousChecks > 0 {
		slog.Warn("malicious check incomplete: OSV lookup failed for dependencies", "count", failedMaliciousChecks)
	}

	// Phase 4: Scoring and prioritization
	slog.Info("Phase 4: Computing scores and ranking")
	maxExclusive := graphResult.MaxExclusiveTransitiveCount()
	entries := s.buildEntries(graphResult, couplingResults, healthResults, maxExclusive, input.ToolDeps, cliDeps, npmPeerDeps)
	for i := range entries {
		if private[entries[i].PURL] {
			entries[i].Health.MaintenanceStatus = "Private"
		} else if input.Offline {
			entries[i].Health.MaintenanceStatus = "Unknown"
		}
	}
	domaindiet.RankEntries(entries)
	summary := domaindiet.ComputeSummary(entries, graphResult.TotalTransitive)

	plan := &domaindiet.DietPlan{
		Entries:    entries,
		Summary:    summary,
		SBOMPath:   input.SBOMPath,
		SourceRoot: input.SourceRoot,
		AnalyzedAt: time.Now(),
	}

	slog.Info("Diet analysis complete",
		"entries", len(entries),
		"easyWins", summary.EasyWins,
		"unusedDirect", summary.UnusedDirect,
	)

	return plan, nil
}

func (s *Service) buildEntries(
	graph *domaindiet.GraphResult,
	coupling map[string]*domaindiet.CouplingAnalysis,
	health map[string]*domain.Analysis,
	maxExclusive int,
	toolDeps map[string]struct{},
	cliDeps map[string]struct{},
	npmPeerDeps map[string][]string,
) []domaindiet.DietEntry {
	peerHosts := usedNpmPeerHosts(graph.DirectDeps, coupling, npmPeerDeps)
	npm := make(map[string][]string)
	for _, purl := range graph.DirectDeps {
		name, ecosystem, _ := parsePURLParts(purl)
		if ecosystem == "npm" {
			npm[strings.ToLower(name)] = append(npm[strings.ToLower(name)], purl)
		}
	}
	entries := make([]domaindiet.DietEntry, 0, len(graph.DirectDeps))
	for _, purl := range graph.DirectDeps {
		entry := domaindiet.DietEntry{
			PURL:     purl,
			Relation: domaindiet.RelationDirect,
		}

		entry.Name, entry.Ecosystem, entry.Version = parsePURLParts(purl)

		// Check if this dependency is a Go tool directive dep.
		if toolDeps != nil {
			if _, ok := toolDeps[entry.Name]; ok {
				entry.Scope = domaindiet.ScopeTool
			}
		}

		// Check if this dependency is a Maven runtime dep (reflection-loaded).
		if entry.Scope == "" && entry.Ecosystem == "maven" {
			if _, ok := mavenRuntimeDeps[strings.ToLower(entry.Name)]; ok {
				entry.Scope = domaindiet.ScopeRuntime
			}
		}
		if entry.Scope == "" && entry.Ecosystem == "pypi" && isPyPIRuntimeDep(entry.Name) {
			entry.Scope = domaindiet.ScopeRuntime
		}
		if entry.Scope == "" {
			if _, ok := cliDeps[purl]; ok {
				entry.Scope = domaindiet.ScopeTool
			}
		}

		if m, ok := graph.Metrics[purl]; ok {
			entry.Graph = *m
		}

		entry.Coupling.Unanalyzed = true
		switch entry.Ecosystem {
		case "golang", "pypi", "npm", "maven":
			if coupling != nil {
				if c := coupling[purl]; c != nil {
					entry.Coupling = *c
				} else {
					entry.Coupling = domaindiet.CouplingAnalysis{IsUnused: true}
				}
			}
		}

		if entry.Ecosystem == "npm" && strings.HasPrefix(entry.Name, "@types/") {
			entry.Scope = domaindiet.ScopeTypes
			runtime := strings.TrimPrefix(entry.Name, "@types/")
			if scope, name, ok := strings.Cut(runtime, "__"); ok {
				runtime = "@" + scope + "/" + name
			}
			if own := coupling[entry.PURL]; own != nil && !own.Unanalyzed && !own.IsUnused {
				entry.Coupling = *own
			} else if purls := npm[strings.ToLower(runtime)]; len(purls) > 0 {
				entry.Coupling = inheritedTypeCoupling(purls, coupling)
			} else if coupling[entry.PURL] == nil {
				entry.Coupling = domaindiet.CouplingAnalysis{Unanalyzed: true}
			}
		}
		var inheritedPeerCoupling *domaindiet.CouplingAnalysis
		if hostPURL := peerHosts[purl]; hostPURL != "" {
			peerHasDirectUsage := !entry.Coupling.Unanalyzed && !entry.Coupling.IsUnused
			if host := coupling[hostPURL]; !peerHasDirectUsage && host != nil && !host.Unanalyzed && !host.IsUnused {
				entry.Scope = domaindiet.ScopePeer
				entry.PeerRequiredBy, _, _ = parsePURLParts(hostPURL)
				inheritedPeerCoupling = host
				entry.Coupling = domaindiet.CouplingAnalysis{}
			}
		}

		// Non-static-import scopes (tool directives, runtime/reflection deps)
		// intentionally have zero source imports. Override IsUnused so they are
		// not flagged as trivial removals, and provide a minimal synthetic
		// coupling signal so downstream scoring does not treat them as zero-effort.
		if entry.Scope == domaindiet.ScopeTool || entry.Scope == domaindiet.ScopeRuntime {
			entry.Coupling.Unanalyzed = false
			if entry.Coupling.IsUnused {
				entry.Coupling.IsUnused = false
			}
			if entry.Coupling.ImportFileCount == 0 &&
				entry.Coupling.CallSiteCount == 0 &&
				entry.Coupling.APIBreadth == 0 {
				entry.Coupling.APIBreadth = 1
			}
		}

		if a, ok := health[purl]; ok && a != nil {
			entry.Health = computeHealthSignals(a)
		} else {
			entry.Health = domaindiet.HealthSignals{HealthRisk: 0.5} // unknown = moderate
		}

		scoringCoupling := entry.Coupling
		if inheritedPeerCoupling != nil {
			scoringCoupling = *inheritedPeerCoupling
		}
		entry.Scores = domaindiet.ComputeImpactScore(entry.Graph, scoringCoupling, entry.Health, maxExclusive)

		if name := graph.DisplayNames[purl]; name != "" {
			entry.Name = name
		}
		entries = append(entries, entry)
	}
	return entries
}

func usedNpmPeerHosts(
	directDeps []string,
	coupling map[string]*domaindiet.CouplingAnalysis,
	peerDeps map[string][]string,
) map[string]string {
	directByName := make(map[string][]string)
	directPURLs := make(map[string]struct{}, len(directDeps))
	for _, purl := range directDeps {
		name, ecosystem, _ := parsePURLParts(purl)
		if ecosystem != "npm" {
			continue
		}
		directByName[strings.ToLower(name)] = append(directByName[strings.ToLower(name)], purl)
		directPURLs[purl] = struct{}{}
	}
	owners := make(map[string][]string)
	for hostPURL, peers := range peerDeps {
		if _, direct := directPURLs[hostPURL]; !direct {
			continue
		}
		hostCoupling := coupling[hostPURL]
		if hostCoupling == nil || hostCoupling.Unanalyzed || hostCoupling.IsUnused {
			continue
		}
		_, ecosystem, _ := parsePURLParts(hostPURL)
		if ecosystem != "npm" {
			continue
		}
		for _, peerName := range peers {
			for _, peerPURL := range directByName[strings.ToLower(peerName)] {
				if peerPURL != hostPURL {
					owners[peerPURL] = append(owners[peerPURL], hostPURL)
				}
			}
		}
	}
	peerHosts := make(map[string]string, len(owners))
	for peerPURL, hosts := range owners {
		sort.Strings(hosts)
		peerHosts[peerPURL] = hosts[0]
	}
	return peerHosts
}

func computeHealthSignals(a *domain.Analysis) domaindiet.HealthSignals {
	h := domaindiet.HealthSignals{
		OverallScore: a.OverallScore,
	}

	// Map EOL state to health signals.
	// domain.EOLEndOfLife is the terminal state; we use FinalMaintenanceStatus()
	// to get the refined label (EOL-Confirmed vs EOL-Effective).
	switch a.EOL.State {
	case domain.EOLEndOfLife:
		h.IsEOL = true
		ms := a.FinalMaintenanceStatus()
		h.MaintenanceStatus = ms.String()
		h.HealthRisk = 0.9
		if ms == domain.LabelEOLEffective {
			h.HealthRisk = 0.85
		}
	case domain.EOLScheduled:
		h.MaintenanceStatus = domain.LabelEOLScheduled.String()
		h.HealthRisk = 0.7
	default:
		ms := a.FinalMaintenanceStatus()
		h.MaintenanceStatus = ms.String()
		h.HealthRisk = 0.2
		// Elevate risk for non-active statuses
		switch ms {
		case domain.LabelStalled:
			h.IsStalled = true
			h.HealthRisk = 0.6
		case domain.LabelLegacySafe:
			h.HealthRisk = 0.4
		case domain.LabelReviewNeeded:
			h.HealthRisk = 0.5
		}
	}

	// Check repo state for archived/stalled
	if a.RepoState != nil {
		if a.RepoState.IsArchived {
			h.MaintenanceStatus = domaindiet.MaintenanceStatusArchived
			h.HealthRisk = math.Max(h.HealthRisk, 0.85)
		}
		if a.RepoState.DaysSinceLastCommit > 365 {
			h.IsStalled = true
			h.HealthRisk = math.Max(h.HealthRisk, 0.6)
		}
	}
	if a.Malicious() {
		h.HealthRisk = 1
	}

	// Vulnerability info from the latest version detail
	if a.ReleaseInfo != nil {
		if vd := a.ReleaseInfo.LatestVersionDetail(); vd != nil {
			for _, adv := range vd.Advisories {
				h.VulnerabilityCount++
				if adv.CVSS3Score > h.MaxCVSSScore {
					h.MaxCVSSScore = adv.CVSS3Score
				}
			}
			if h.VulnerabilityCount > 0 {
				h.HasVulnerabilities = true
				h.HealthRisk = math.Min(h.HealthRisk+h.MaxCVSSScore/10.0*0.2, 1.0)
			}
		}
	}

	// Low Scorecard score increases risk
	if a.OverallScore > 0 {
		h.HealthRisk = math.Min(h.HealthRisk+(1.0-a.OverallScore/10.0)*0.1, 1.0)
	}

	return h
}

// parsePURLParts extracts name, ecosystem, version from a PURL string.
func parsePURLParts(purlStr string) (name, ecosystem, version string) {
	parsed, err := packageurl.FromString(purlStr)
	if err != nil {
		return purlStr, "", ""
	}
	n := parsed.Name
	if parsed.Namespace != "" {
		n = parsed.Namespace + "/" + parsed.Name
	}
	return n, parsed.Type, parsed.Version
}

// resolveUnmatchedPyPI resolves metadata for PyPI PURLs without source matches.
func (s *Service) resolveUnmatchedPyPI(
	ctx context.Context,
	sourceRoot string,
	directDeps []string,
	publicDeps []string,
	couplingResults map[string]*domaindiet.CouplingAnalysis,
	offline bool,
) (map[string][]string, map[string]struct{}, map[string]struct{}) {
	retryPaths := make(map[string][]string)
	commandScripts := make(map[string][]string)
	commandImports := make(map[string][]string)
	public := make(map[string]struct{}, len(publicDeps))
	for _, purl := range publicDeps {
		public[purl] = struct{}{}
	}
	unresolved := make(map[string]struct{})
	cliDeps := make(map[string]struct{})
	for _, purl := range directDeps {
		parsed, err := packageurl.FromString(purl)
		if err != nil || parsed.Type != "pypi" {
			continue
		}
		if c := couplingResults[purl]; c != nil && !c.Unanalyzed {
			continue
		}
		if s.pypiResolver == nil {
			unresolved[purl] = struct{}{}
			continue
		}
		names, scripts, resolveErr := s.pypiResolver.ResolveLocalDistributionMetadata(ctx, sourceRoot, parsed.Name)
		if resolveErr != nil {
			slog.Debug("pypi_local: import metadata lookup failed", "package", parsed.Name, "error", resolveErr)
		}
		if len(names) == 0 && !offline {
			if _, isPublic := public[purl]; !isPublic {
				if len(scripts) == 0 {
					unresolved[purl] = struct{}{}
					continue
				}
			} else {
				wheelNames, wheelScripts, wheelErr := s.pypiResolver.ResolveDistributionMetadata(ctx, parsed.Name)
				if wheelErr != nil {
					slog.Debug("pypi_wheel: resolve failed, skipping", "package", parsed.Name, "error", wheelErr)
				} else {
					if len(names) == 0 {
						names = wheelNames
					}
					if len(scripts) == 0 {
						scripts = wheelScripts
					}
				}
			}
		}
		if len(names) > 0 {
			retryPaths[purl] = names
			commandImports[purl] = names
		}
		if len(scripts) > 0 {
			commandScripts[purl] = scripts
		}
		if len(names) == 0 && len(scripts) == 0 {
			unresolved[purl] = struct{}{}
		}
	}
	if (len(commandScripts) > 0 || len(commandImports) > 0) && s.sourceAnalyzer != nil {
		used, err := s.sourceAnalyzer.AnalyzePyPICommandUsage(ctx, sourceRoot, commandScripts, commandImports)
		if err != nil {
			slog.Warn("PyPI console-script analysis failed", "error", err)
			for purl := range commandScripts {
				unresolved[purl] = struct{}{}
			}
			for purl := range commandImports {
				unresolved[purl] = struct{}{}
			}
		} else {
			for purl := range used {
				if _, ok := commandScripts[purl]; !ok {
					if _, ok := commandImports[purl]; !ok {
						continue
					}
				}
				cliDeps[purl] = struct{}{}
				delete(unresolved, purl)
			}
			for purl := range commandScripts {
				if len(commandImports[purl]) == 0 {
					if _, found := cliDeps[purl]; !found {
						unresolved[purl] = struct{}{}
					}
				}
			}
		}
	} else if len(commandScripts) > 0 || len(commandImports) > 0 {
		for purl := range commandScripts {
			unresolved[purl] = struct{}{}
		}
		for purl := range commandImports {
			unresolved[purl] = struct{}{}
		}
	}
	return retryPaths, unresolved, cliDeps
}

func markPyPIUnanalyzed(
	couplingResults map[string]*domaindiet.CouplingAnalysis,
	unresolved map[string]struct{},
) map[string]*domaindiet.CouplingAnalysis {
	if len(unresolved) == 0 {
		return couplingResults
	}
	if couplingResults == nil {
		couplingResults = make(map[string]*domaindiet.CouplingAnalysis, len(unresolved))
	}
	for purl := range unresolved {
		if couplingResults[purl] == nil || couplingResults[purl].Unanalyzed {
			couplingResults[purl] = &domaindiet.CouplingAnalysis{Unanalyzed: true}
		}
	}
	return couplingResults
}
