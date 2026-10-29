package diet

import (
	"context"
	"encoding/json"
	"testing"

	domaindiet "github.com/future-architect/uzomuzo-oss/internal/domain/diet"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/depgraph"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/sbomgraph"
)

func TestGoDisplayNameFromSBOM(t *testing.T) {
	const purl = "pkg:golang/github.com/masterminds/goutils@v1.0.0"
	for _, tt := range []struct{ name, group, component, want string }{
		{"full", "", "github.com/Masterminds/goutils", "github.com/Masterminds/goutils"},
		{"group", "github.com/Masterminds", "goutils", "github.com/Masterminds/goutils"},
		{"missing", "", "", "github.com/masterminds/goutils"},
		{"mismatch", "", "github.com/another/module", "github.com/masterminds/goutils"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bom := sbomgraph.BOMEnvelope{
				Metadata:     &sbomgraph.BOMMetadata{Component: &sbomgraph.Component{BOMRef: "root"}},
				Components:   []sbomgraph.Component{{Components: []sbomgraph.Component{{BOMRef: "dep", PURL: purl, Group: tt.group, Name: tt.component}}}},
				Dependencies: []sbomgraph.Dependency{{Ref: "root", DependsOn: []string{"dep"}}},
			}
			data, err := json.Marshal(bom)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := NewService(depgraph.NewAnalyzer(), nil, nil, nil).Run(context.Background(), DietInput{SBOMData: data, ToolDeps: map[string]struct{}{"github.com/masterminds/goutils": {}}})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			var decoded domaindiet.DietPlan
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded.Entries) != 1 {
				t.Fatalf("entries=%v", decoded.Entries)
			}
			entry := decoded.Entries[0]
			if entry.Name != tt.want || entry.PURL != purl || entry.Scope != domaindiet.ScopeTool {
				t.Fatalf("entry=%+v, want name=%q with unchanged identity and tool scope", entry, tt.want)
			}
		})
	}
}
