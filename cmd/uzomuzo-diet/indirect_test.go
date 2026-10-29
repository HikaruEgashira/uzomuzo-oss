package main

import (
	"context"
	"encoding/json"
	"testing"

	dietapp "github.com/future-architect/uzomuzo-oss/internal/application/diet"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/depgraph"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/depparser/gomod"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/sbomgraph"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/treesitter"
)

func TestMultimoduleIndirectCandidates(t *testing.T) {
	const root = "../../internal/infrastructure/depparser/gomod/testdata/multimodule"
	ctx := context.Background()
	tools, indirect, err := gomod.ReadDependencyScopes(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	bom := sbomgraph.BOMEnvelope{Metadata: &sbomgraph.BOMMetadata{Component: &sbomgraph.Component{BOMRef: "root"}}}
	refs := []string{}
	for _, name := range []string{"example.com/used", "example.com/unused", "example.com/shared", "example.com/tool", "gopkg.in/datadog/dd-trace-go.v1"} {
		bom.Components = append(bom.Components, sbomgraph.Component{BOMRef: name, PURL: "pkg:golang/" + name + "@v1.0.0"})
		refs = append(refs, name)
	}
	bom.Dependencies = []sbomgraph.Dependency{{Ref: "root", DependsOn: refs}}
	data, err := json.Marshal(bom)
	if err != nil {
		t.Fatal(err)
	}
	analyzer := treesitter.NewAnalyzer()
	defer analyzer.Close()
	service := dietapp.NewService(depgraph.NewAnalyzer(), analyzer, nil, nil)
	plan, err := service.Run(ctx, dietapp.DietInput{SBOMData: data, SourceRoot: root, ToolDeps: tools, GoIndirectDeps: indirect})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary.TotalDirect != 4 || plan.Summary.UnusedDirect != 1 {
		t.Fatalf("incorrect multi-module candidates: %+v", plan.Summary)
	}
	for _, entry := range plan.Entries {
		if entry.Coupling.IsUnused && entry.Name != "example.com/unused" {
			t.Fatalf("incorrect unused candidate: %+v", entry)
		}
	}
}
