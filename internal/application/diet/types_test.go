package diet

import (
	"reflect"
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
			entries := (&Service{}).buildEntries(&domaindiet.GraphResult{DirectDeps: []string{react, types}}, tt.coupling, nil, 0, nil, nil, nil)
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
	entries := (&Service{}).buildEntries(&domaindiet.GraphResult{DirectDeps: []string{"pkg:npm/%40types/unpaired@1"}}, map[string]*domaindiet.CouplingAnalysis{}, nil, 0, nil, nil, nil)
	if !entries[0].Coupling.Unanalyzed || entries[0].Coupling.IsUnused || entries[0].Scores.Difficulty != domaindiet.DifficultyUnknown {
		t.Fatalf("unpaired types=%+v", entries[0])
	}
}

func TestScopedTypesInheritUsedVersion(t *testing.T) {
	const types = "pkg:npm/%40types/scope__name@1"
	const unused = "pkg:npm/%40scope/name@1"
	const used = "pkg:npm/%40scope/name@2"
	entries := (&Service{}).buildEntries(&domaindiet.GraphResult{DirectDeps: []string{types, unused, used}}, map[string]*domaindiet.CouplingAnalysis{used: {ImportFileCount: 1}}, nil, 0, nil, nil, nil)
	if entries[0].Coupling.IsUnused || entries[0].Coupling.Unanalyzed || entries[0].Coupling.ImportFileCount != 1 {
		t.Fatalf("scoped usage=%+v", entries[0].Coupling)
	}
}

func TestTypesExplicitUseKeepsDeclarationsForUnusedRuntime(t *testing.T) {
	const types = "pkg:npm/%40types/react@1"
	entries := (&Service{}).buildEntries(&domaindiet.GraphResult{DirectDeps: []string{types, "pkg:npm/react@1"}}, map[string]*domaindiet.CouplingAnalysis{types: {ImportFileCount: 1}}, nil, 0, nil, nil, nil)
	if entries[0].Coupling.IsUnused || entries[0].Coupling.ImportFileCount != 1 {
		t.Fatalf("type-only usage=%+v", entries[0].Coupling)
	}
}

func TestUsedNpmPeerHostsRequiresUsedDirectHost(t *testing.T) {
	const (
		next       = "pkg:npm/next@14.0.0"
		material   = "pkg:npm/%40mui/material@5.15.0"
		reactDOM   = "pkg:npm/react-dom@18.0.0"
		emotion    = "pkg:npm/%40emotion/react@11.0.0"
		transitive = "pkg:npm/transitive-host@1.0.0"
	)
	got := usedNpmPeerHosts(
		[]string{next, material, reactDOM, emotion},
		map[string]*domaindiet.CouplingAnalysis{
			next:     {ImportFileCount: 1, CallSiteCount: 2},
			material: {IsUnused: true},
		},
		map[string][]string{
			next:       {"react-dom"},
			material:   {"@emotion/react"},
			transitive: {"react-dom"},
		},
	)
	want := map[string]string{reactDOM: next}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usedNpmPeerHosts() = %#v, want %#v", got, want)
	}
}

func TestPeerScopeInheritsHostEffortAndLeavesOptionalPeerUnused(t *testing.T) {
	const (
		next         = "pkg:npm/next@14.0.0"
		reactDOM     = "pkg:npm/react-dom@18.0.0"
		optionalPeer = "pkg:npm/optional-adapter@1.0.0"
	)
	hostCoupling := &domaindiet.CouplingAnalysis{ImportFileCount: 3, CallSiteCount: 8, APIBreadth: 5}
	entries := (&Service{}).buildEntries(
		&domaindiet.GraphResult{DirectDeps: []string{next, reactDOM, optionalPeer}},
		map[string]*domaindiet.CouplingAnalysis{
			next:         hostCoupling,
			reactDOM:     {IsUnused: true},
			optionalPeer: {IsUnused: true},
		},
		nil, 0, nil, nil,
		map[string][]string{next: {"react-dom"}},
	)
	if entries[0].Scope != "" || !reflect.DeepEqual(entries[0].Coupling, *hostCoupling) {
		t.Fatalf("host entry = %+v", entries[0])
	}
	peer := entries[1]
	if peer.Scope != domaindiet.ScopePeer || peer.PeerRequiredBy != "next" || peer.Coupling.Unanalyzed || peer.Coupling.IsUnused {
		t.Fatalf("peer entry = %+v", peer)
	}
	if peer.Scores.CouplingEffort != entries[0].Scores.CouplingEffort || peer.Scores.Difficulty != entries[0].Scores.Difficulty {
		t.Fatalf("peer effort=%+v, host effort=%+v", peer.Scores, entries[0].Scores)
	}
	if peer.Coupling.ImportFileCount != 0 || peer.Coupling.CallSiteCount != 0 {
		t.Fatalf("peer should not claim direct imports: %+v", peer.Coupling)
	}
	optional := entries[2]
	if optional.Scope != "" || !optional.Coupling.IsUnused {
		t.Fatalf("optional peer should remain unused: %+v", optional)
	}
}

func TestPeerScopePreservesDirectPeerUsage(t *testing.T) {
	const (
		next  = "pkg:npm/next@14.0.0"
		react = "pkg:npm/react@18.0.0"
	)
	reactCoupling := domaindiet.CouplingAnalysis{ImportFileCount: 1, CallSiteCount: 2, APIBreadth: 1}
	entries := (&Service{}).buildEntries(
		&domaindiet.GraphResult{DirectDeps: []string{next, react}},
		map[string]*domaindiet.CouplingAnalysis{
			next:  {ImportFileCount: 2, CallSiteCount: 4, APIBreadth: 3},
			react: {ImportFileCount: 1, CallSiteCount: 2, APIBreadth: 1},
		},
		nil, 0, nil, nil,
		map[string][]string{next: {"react"}},
	)
	if entries[1].Scope == domaindiet.ScopePeer || entries[1].PeerRequiredBy != "" || !reflect.DeepEqual(entries[1].Coupling, reactCoupling) {
		t.Fatalf("directly used peer lost its own coupling: %+v", entries[1])
	}
}
