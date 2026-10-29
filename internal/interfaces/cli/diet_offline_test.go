package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	domaindiet "github.com/future-architect/uzomuzo-oss/internal/domain/diet"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/depgraph"
	"github.com/future-architect/uzomuzo-oss/internal/infrastructure/pypi"
)

type denyDietTransport struct{ calls atomic.Int32 }

func (r *denyDietTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return nil, fmt.Errorf("unexpected offline HTTP request")
}

func TestRunDietOfflineMakesNoRequests(t *testing.T) {
	transport := &denyDietTransport{}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	root := t.TempDir()
	path := filepath.Join(root, "bom.json")
	bom := `{"metadata":{"component":{"bom-ref":"root"}},"components":[{"bom-ref":"dep","purl":"pkg:pypi/beautifulsoup4@4.12.3"}],"dependencies":[{"ref":"root","dependsOn":["dep"]}]}`
	if err := os.WriteFile(path, []byte(bom), 0600); err != nil {
		t.Fatal(err)
	}
	source := emptyDietSource{}
	if err := RunDiet(context.Background(), nil, DietOptions{SBOMPath: path, SourceRoot: root, Format: "json", Offline: true}, depgraph.NewAnalyzer(), source, pypi.NewClient()); err != nil {
		t.Fatal(err)
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("offline HTTP requests=%d", got)
	}
}

type emptyDietSource struct{}

func (emptyDietSource) AnalyzeCoupling(context.Context, string, map[string][]string) (map[string]*domaindiet.CouplingAnalysis, error) {
	return nil, nil
}

func (emptyDietSource) PyPIProjectNames(context.Context, string) ([]string, error) {
	return nil, nil
}

func (emptyDietSource) AnalyzePyPICommandUsage(context.Context, string, map[string][]string, map[string][]string) (map[string]struct{}, error) {
	return nil, nil
}

func (emptyDietSource) NpmPeerDependencies(context.Context, string, []string) (map[string][]string, error) {
	return nil, nil
}
