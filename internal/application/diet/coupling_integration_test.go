//go:build cgo

package diet

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/future-architect/uzomuzo-oss/internal/application"
	domain "github.com/future-architect/uzomuzo-oss/internal/domain/analysis"
	domaindiet "github.com/future-architect/uzomuzo-oss/internal/domain/diet"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/depgraph"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/pypi"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/sbomgraph"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/treesitter"
)

// TestBuildImportPaths_AnalyzeCoupling_Integration verifies the full pipeline
// from PURL to buildImportPaths to AnalyzeCoupling, ensuring that the generated
// import paths actually match real Java source code. This catches regressions
// in buildImportPaths that unit tests with hardcoded paths would mask.
func TestBuildImportPaths_AnalyzeCoupling_Integration(t *testing.T) {
	tests := []struct {
		name            string
		purl            string
		sourceFile      string
		sourceCode      string
		wantImportCount int
		wantCallSites   int
		wantUnused      bool
	}{
		{
			name:       "gson via override mapping",
			purl:       "pkg:maven/com.google.code.gson/gson@2.10",
			sourceFile: "Main.java",
			sourceCode: `import com.google.gson.Gson;

public class Main {
    public static void main(String[] args) {
        Gson gson = new Gson();
        String json = gson.toJson("hello");
        String json2 = gson.fromJson("{}", String.class);
    }
}
`,
			wantImportCount: 1,
			// 4 call sites: Gson local var type (1) + new Gson() (1) + gson.toJson (1) + gson.fromJson (1)
			wantCallSites: 4,
			wantUnused:    false,
		},
		{
			name:       "commons-lang3 via groupId heuristic",
			purl:       "pkg:maven/org.apache.commons/commons-lang3@3.14",
			sourceFile: "Main.java",
			sourceCode: `import org.apache.commons.lang3.StringUtils;

public class Main {
    public static void main(String[] args) {
        boolean b = StringUtils.isBlank("");
    }
}
`,
			wantImportCount: 1,
			wantCallSites:   1,
			wantUnused:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			err := os.WriteFile(filepath.Join(dir, tt.sourceFile), []byte(tt.sourceCode), 0644)
			if err != nil {
				t.Fatalf("failed to write source file: %v", err)
			}

			// Use the real buildImportPaths — not hardcoded paths.
			importPaths := buildImportPaths([]string{tt.purl})
			paths := importPaths[tt.purl]
			if len(paths) == 0 {
				t.Fatalf("buildImportPaths(%q) returned no candidate import paths for the current PURL", tt.purl)
			}

			t.Logf("buildImportPaths(%q) = %v", tt.purl, paths)

			analyzer := treesitter.NewAnalyzer()
			t.Cleanup(analyzer.Close)
			result, err := analyzer.AnalyzeCoupling(context.Background(), dir, importPaths)
			if err != nil {
				t.Fatalf("AnalyzeCoupling() error: %v", err)
			}

			ca, ok := result[tt.purl]
			if !ok {
				if tt.wantImportCount > 0 || tt.wantCallSites > 0 {
					t.Fatalf("no coupling result for %s; buildImportPaths generated %v but none matched the source",
						tt.purl, importPaths[tt.purl])
				}
				// No result and we expected zero coupling — pass.
				return
			}

			if ca.ImportFileCount != tt.wantImportCount {
				t.Errorf("ImportFileCount = %d, want %d", ca.ImportFileCount, tt.wantImportCount)
			}
			if ca.CallSiteCount != tt.wantCallSites {
				t.Errorf("CallSiteCount = %d, want %d", ca.CallSiteCount, tt.wantCallSites)
			}
			if ca.IsUnused != tt.wantUnused {
				t.Errorf("IsUnused = %v, want %v", ca.IsUnused, tt.wantUnused)
			}
		})
	}

	// Test unused dependency by including a used PURL alongside the unused one.
	// When no dependency matches any source, AnalyzeCoupling returns nil (coupling
	// unavailable). A used PURL ensures the analyzer returns non-nil results so the
	// unused PURL's absence from the result map is meaningful.
	t.Run("unused dependency alongside used dependency", func(t *testing.T) {
		dir := t.TempDir()
		sourceCode := `import com.google.gson.Gson;

public class Main {
    public static void main(String[] args) {
        Gson gson = new Gson();
    }
}
`
		err := os.WriteFile(filepath.Join(dir, "Main.java"), []byte(sourceCode), 0644)
		if err != nil {
			t.Fatalf("failed to write source file: %v", err)
		}

		usedPURL := "pkg:maven/com.google.code.gson/gson@2.10"
		unusedPURL := "pkg:maven/org.apache.commons/commons-lang3@3.14"

		importPaths := buildImportPaths([]string{usedPURL, unusedPURL})
		t.Logf("importPaths = %v", importPaths)

		analyzer := treesitter.NewAnalyzer()
		t.Cleanup(analyzer.Close)
		result, err := analyzer.AnalyzeCoupling(context.Background(), dir, importPaths)
		if err != nil {
			t.Fatalf("AnalyzeCoupling() error: %v", err)
		}
		if result == nil {
			t.Fatal("AnalyzeCoupling() returned nil; expected non-nil with at least the used dependency")
		}

		// The used dependency must be present.
		if _, ok := result[usedPURL]; !ok {
			t.Errorf("expected coupling result for used PURL %s", usedPURL)
		}

		// The unused dependency must either be absent or marked as unused.
		if ca, ok := result[unusedPURL]; ok {
			if !ca.IsUnused {
				t.Errorf("unused PURL %s: IsUnused = false, want true", unusedPURL)
			}
		}
		// Absent from the result map is also acceptable — means no coupling data collected.
	})
}

type purlLookupRecorder struct {
	purls []string
}

func (r *purlLookupRecorder) AnalyzeFromPURLs(_ context.Context, purls []string) (map[string]*domain.Analysis, error) {
	r.purls = append([]string(nil), purls...)
	return map[string]*domain.Analysis{}, nil
}

func (r *purlLookupRecorder) AnalyzeFromGitHubURLs(_ context.Context, _ []string) (map[string]*domain.Analysis, error) {
	return map[string]*domain.Analysis{}, nil
}

type sourceAnalyzerRecorder struct {
	analyzer    *treesitter.Analyzer
	importPaths map[string][]string
}

func (r *sourceAnalyzerRecorder) AnalyzeCoupling(ctx context.Context, sourceRoot string, importPaths map[string][]string) (map[string]*domaindiet.CouplingAnalysis, error) {
	r.importPaths = importPaths
	return r.analyzer.AnalyzeCoupling(ctx, sourceRoot, importPaths)
}

func (r *sourceAnalyzerRecorder) PyPIProjectNames(ctx context.Context, sourceRoot string) ([]string, error) {
	return r.analyzer.PyPIProjectNames(ctx, sourceRoot)
}

func (r *sourceAnalyzerRecorder) AnalyzePyPICommandUsage(ctx context.Context, sourceRoot string, scripts, importPaths map[string][]string) (map[string]struct{}, error) {
	return r.analyzer.AnalyzePyPICommandUsage(ctx, sourceRoot, scripts, importPaths)
}

func (r *sourceAnalyzerRecorder) NpmPeerDependencies(ctx context.Context, sourceRoot string, directDeps []string) (map[string][]string, error) {
	return r.analyzer.NpmPeerDependencies(ctx, sourceRoot, directDeps)
}

func TestRun_PyPIConsoleScriptUsage(t *testing.T) {
	root := t.TempDir()
	const purl = "pkg:pypi/private-cli@1.0.0"
	distInfo := filepath.Join(root, ".venv", "lib", "python3.12", "site-packages", "private_cli-1.0.0.dist-info")
	if err := os.MkdirAll(distInfo, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"METADATA":         "Metadata-Version: 2.1\nName: private-cli\nVersion: 1.0.0\n",
		"entry_points.txt": "[console_scripts]\nlocal-cli = private_cli.cli:main\n",
	} {
		if err := os.WriteFile(filepath.Join(distInfo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("serve:\n\tuv run local-cli --port 8000\n# local-cli is only a comment here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := NewService(
		&stubGraphAnalyzer{result: &domaindiet.GraphResult{DirectDeps: []string{purl}}},
		treesitter.NewAnalyzer(), pypi.NewClient(), nil,
	)
	plan, err := svc.Run(context.Background(), DietInput{SourceRoot: root, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(plan.Entries))
	}
	entry := plan.Entries[0]
	if entry.Scope != domaindiet.ScopeTool || entry.Coupling.Unanalyzed || entry.Coupling.IsUnused {
		t.Fatalf("console-script dependency = %+v, want used tool", entry)
	}
}

func TestRun_PythonSelfDependenciesAreFilteredBeforeHealthLookup(t *testing.T) {
	dir := t.TempDir()
	pyproject := `[project]
name = "Local_Project"
description = """
name = "shadow-project"
"""
urls = { Homepage = "https://example.invalid" }
`
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pyproject), 0o644); err != nil {
		t.Fatal(err)
	}

	const (
		rootPURL        = "pkg:pypi/Root_Project@0.1.0"
		selfPURL        = "pkg:pypi/root-project@0.2.0"
		projectPURL     = "pkg:pypi/local.project@1.2.3"
		shadowPURL      = "pkg:pypi/shadow-project@0.1.0"
		requestsPURL    = "pkg:pypi/requests@2.31.0"
		npmSameNamePURL = "pkg:npm/local-project@1.2.3"
	)
	bom := sbomgraph.BOMEnvelope{
		Metadata: &sbomgraph.BOMMetadata{Component: &sbomgraph.Component{
			BOMRef: "root", PURL: rootPURL,
		}},
		Components: []sbomgraph.Component{
			{BOMRef: "self", PURL: selfPURL},
			{BOMRef: "project", PURL: projectPURL},
			{BOMRef: "shadow", PURL: shadowPURL},
			{BOMRef: "requests", PURL: requestsPURL},
			{BOMRef: "npm-same-name", PURL: npmSameNamePURL},
		},
		Dependencies: []sbomgraph.Dependency{
			{Ref: "root", DependsOn: []string{"self", "project", "shadow", "requests", "npm-same-name"}},
			{Ref: "self"},
			{Ref: "project"},
			{Ref: "shadow"},
			{Ref: "requests"},
			{Ref: "npm-same-name"},
		},
	}
	sbomData, err := json.Marshal(bom)
	if err != nil {
		t.Fatal(err)
	}

	lookup := &purlLookupRecorder{}
	analysisService := application.NewAnalysisService(lookup)
	graphAnalyzer := depgraph.NewAnalyzer()
	sourceAnalyzer := &sourceAnalyzerRecorder{analyzer: treesitter.NewAnalyzer()}
	t.Cleanup(sourceAnalyzer.analyzer.Close)
	service := NewService(graphAnalyzer, sourceAnalyzer, nil, analysisService)
	plan, err := service.Run(context.Background(), DietInput{
		SBOMData: sbomData, SourceRoot: dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	entries := make(map[string]struct{}, len(plan.Entries))
	for _, entry := range plan.Entries {
		entries[entry.PURL] = struct{}{}
	}
	for _, purl := range []string{selfPURL, projectPURL} {
		if _, ok := entries[purl]; ok {
			t.Errorf("self dependency %s remains in diet entries", purl)
		}
	}
	for _, purl := range []string{shadowPURL, requestsPURL, npmSameNamePURL} {
		if _, ok := entries[purl]; !ok {
			t.Errorf("non-Python-self dependency %s was filtered", purl)
		}
	}
	for _, purl := range []string{selfPURL, projectPURL} {
		if _, ok := sourceAnalyzer.importPaths[purl]; ok {
			t.Errorf("self dependency %s reached source analysis", purl)
		}
	}
	for _, purl := range []string{shadowPURL, requestsPURL, npmSameNamePURL} {
		if _, ok := sourceAnalyzer.importPaths[purl]; !ok {
			t.Errorf("non-Python-self dependency %s was filtered before source analysis", purl)
		}
	}

	slices.Sort(lookup.purls)
	wantLookups := []string{npmSameNamePURL, requestsPURL, shadowPURL}
	slices.Sort(wantLookups)
	if !slices.Equal(lookup.purls, wantLookups) {
		t.Errorf("health lookup PURLs = %v, want %v", lookup.purls, wantLookups)
	}
}

func TestBuildImportPaths_AnalyzeRubyRailsApp(t *testing.T) {
	dir := t.TempDir()
	source := `require "rails/all"
Bundler.require(*Rails.groups)
Rails.application
FactoryBot.create(:user)
`
	if err := os.WriteFile(filepath.Join(dir, "application.rb"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	purls := []string{"pkg:gem/rails@8.0.0", "pkg:gem/factory_bot_rails@6.4.2"}
	importPaths := buildImportPaths(purls)
	analyzer := treesitter.NewAnalyzer()
	t.Cleanup(analyzer.Close)
	result, err := analyzer.AnalyzeCoupling(context.Background(), dir, importPaths)
	if err != nil {
		t.Fatal(err)
	}
	for _, purl := range purls {
		coupling, ok := result[purl]
		if !ok {
			t.Errorf("no coupling result for %s; import paths: %v", purl, importPaths[purl])
			continue
		}
		if coupling.IsUnused || coupling.ImportFileCount != 1 {
			t.Errorf("%s coupling = %+v, want one used file", purl, coupling)
		}
	}
}

func TestBuildImportPaths_RubyRequireDoesNotUseConstantAliases(t *testing.T) {
	tests := []struct {
		name   string
		purl   string
		source string
	}{
		{
			name:   "constant-only alias is not a require path",
			purl:   "pkg:gem/factory_bot_rails@6.4.2",
			source: `require "factorybot"`,
		},
		{
			name:   "interpolated require is not a static path",
			purl:   "pkg:gem/foo@1.0.0",
			source: `require "foo#{suffix}"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "app.rb"), []byte(tt.source), 0o644); err != nil {
				t.Fatal(err)
			}

			analyzer := treesitter.NewAnalyzer()
			t.Cleanup(analyzer.Close)
			result, err := analyzer.AnalyzeCoupling(context.Background(), dir, buildImportPaths([]string{tt.purl}))
			if err != nil {
				t.Fatal(err)
			}
			if result[tt.purl] != nil {
				t.Errorf("%s got coupling from non-literal or non-require path: %+v", tt.purl, result[tt.purl])
			}
		})
	}
}
