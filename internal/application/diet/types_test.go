package diet

import (
	"testing"

	domaindiet "github.com/future-architect/uzomuzo-oss/internal/domain/diet"
)

func TestTypesInheritRuntimeUsage(t *testing.T) {
	const react = "pkg:npm/react@18.0.0"
	const types = "pkg:npm/%40types/react@18.0.0"
	for _, tt := range []struct {
		name            string
		coupling        map[string]*domaindiet.CouplingAnalysis
		unused, unknown bool
	}{
		{"used", map[string]*domaindiet.CouplingAnalysis{react: {ImportFileCount: 2, CallSiteCount: 3}}, false, false},
		{"unused", map[string]*domaindiet.CouplingAnalysis{}, true, false},
		{"unavailable", nil, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entries := (&Service{}).buildEntries(&domaindiet.GraphResult{DirectDeps: []string{react, types}}, tt.coupling, nil, 0, nil, nil)
			got := entries[1]
			if got.Scope != domaindiet.ScopeTypes {
				t.Fatalf("scope=%q", got.Scope)
			}
			if got.Coupling.IsUnused != tt.unused || got.Coupling.Unanalyzed != tt.unknown {
				t.Fatalf("coupling=%+v", got.Coupling)
			}
			if !tt.unused && !tt.unknown && got.Coupling.CallSiteCount != 3 {
				t.Fatalf("usage not inherited: %+v", got.Coupling)
			}
		})
	}
}

func TestTypesWithoutRuntimeStayUnknown(t *testing.T) {
	entries := (&Service{}).buildEntries(&domaindiet.GraphResult{DirectDeps: []string{"pkg:npm/%40types/unpaired@1"}}, map[string]*domaindiet.CouplingAnalysis{}, nil, 0, nil, nil)
	if !entries[0].Coupling.Unanalyzed || entries[0].Coupling.IsUnused || entries[0].Scores.Difficulty != domaindiet.DifficultyUnknown {
		t.Fatalf("unpaired types=%+v", entries[0])
	}
}

func TestScopedTypesInheritUsedVersion(t *testing.T) {
	const types = "pkg:npm/%40types/scope__name@1"
	const unused = "pkg:npm/%40scope/name@1"
	const used = "pkg:npm/%40scope/name@2"
	entries := (&Service{}).buildEntries(&domaindiet.GraphResult{DirectDeps: []string{types, unused, used}}, map[string]*domaindiet.CouplingAnalysis{used: {ImportFileCount: 1}}, nil, 0, nil, nil)
	if entries[0].Coupling.IsUnused || entries[0].Coupling.Unanalyzed || entries[0].Coupling.ImportFileCount != 1 {
		t.Fatalf("scoped usage=%+v", entries[0].Coupling)
	}
}

func TestTypesExplicitUseKeepsDeclarationsForUnusedRuntime(t *testing.T) {
	const types = "pkg:npm/%40types/react@1"
	entries := (&Service{}).buildEntries(&domaindiet.GraphResult{DirectDeps: []string{types, "pkg:npm/react@1"}}, map[string]*domaindiet.CouplingAnalysis{types: {ImportFileCount: 1}}, nil, 0, nil, nil)
	if entries[0].Coupling.IsUnused || entries[0].Coupling.ImportFileCount != 1 {
		t.Fatalf("type-only usage=%+v", entries[0].Coupling)
	}
}
