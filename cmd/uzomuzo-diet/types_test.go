package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	dietapp "github.com/future-architect/uzomuzo-oss/internal/application/diet"
	domaindiet "github.com/future-architect/uzomuzo-oss/internal/domain/diet"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/depgraph"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/sbomgraph"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/treesitter"
)

func TestDefinitelyTypedUsage(t *testing.T) {
	root := t.TempDir()
	for name, source := range map[string]string{"tsconfig.json": `{}`, "app.tsx": `import React from "react"; React.createElement("div");`} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bom := sbomgraph.BOMEnvelope{Metadata: &sbomgraph.BOMMetadata{Component: &sbomgraph.Component{BOMRef: "root"}}}
	refs := []string{}
	for _, name := range []string{"react", "react-modal", "%40types/react", "%40types/react-modal", "%40types/node", "%40types/unpaired"} {
		purl := "pkg:npm/" + name + "@1.0.0"
		bom.Components = append(bom.Components, sbomgraph.Component{BOMRef: name, PURL: purl})
		refs = append(refs, name)
	}
	bom.Dependencies = []sbomgraph.Dependency{{Ref: "root", DependsOn: refs}}
	data, err := json.Marshal(bom)
	if err != nil {
		t.Fatal(err)
	}
	analyzer := treesitter.NewAnalyzer()
	defer analyzer.Close()
	plan, err := dietapp.NewService(depgraph.NewAnalyzer(), analyzer, nil, nil).Run(context.Background(), dietapp.DietInput{SBOMData: data, SourceRoot: root, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range plan.Entries {
		switch entry.Name {
		case "@types/react", "@types/node":
			if entry.Coupling.IsUnused || entry.Coupling.Unanalyzed || entry.Scope != domaindiet.ScopeTypes {
				t.Fatalf("used types=%+v", entry)
			}
		case "@types/react-modal":
			if !entry.Coupling.IsUnused {
				t.Fatalf("unused runtime's types=%+v", entry)
			}
		case "@types/unpaired":
			if !entry.Coupling.Unanalyzed || entry.Coupling.IsUnused {
				t.Fatalf("unpaired types=%+v", entry)
			}
		}
	}
	if plan.Summary.UnusedDirect != 2 {
		t.Fatalf("unused count=%d", plan.Summary.UnusedDirect)
	}
}
