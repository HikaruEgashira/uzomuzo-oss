package diet

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/future-architect/uzomuzo-oss/internal/application"
	domaindiet "github.com/future-architect/uzomuzo-oss/internal/domain/diet"
)

type recordingResolver struct{ names []string }

func (r *recordingResolver) ResolveImportNames(_ context.Context, name string) ([]string, error) {
	r.names = append(r.names, name)
	return nil, nil
}

func TestPrivateDependenciesStayLocal(t *testing.T) {
	const privateGo = "pkg:golang/github.com/acme/secret@v1.0.0"
	const privatePy = "pkg:pypi/acme-private@1.0"
	const privateNPM = "pkg:npm/%40acme/private@1.0"
	const publicPy = "pkg:pypi/requests@2.0"
	deps := []string{privateGo, privatePy, privateNPM, publicPy}
	for _, offline := range []bool{false, true} {
		t.Run(map[bool]string{false: "online", true: "offline"}[offline], func(t *testing.T) {
			health := &captureHealthSource{}
			resolver := &recordingResolver{}
			source := &stubSourceAnalyzer{}
			svc := NewService(&stubGraphAnalyzer{result: &domaindiet.GraphResult{DirectDeps: deps}}, source, resolver, application.NewAnalysisService(health))
			plan, err := svc.Run(context.Background(), DietInput{SourceRoot: t.TempDir(), Offline: offline, GoPrivatePatterns: "github.com/Acme/*", PrivatePatterns: "acme-*, @acme/*"})
			if err != nil {
				t.Fatal(err)
			}
			wantHealth := []string{publicPy}
			wantResolver := []string{"requests"}
			if offline {
				wantHealth = nil
				wantResolver = nil
			}
			if offline && health.calls != 0 {
				t.Fatal("offline health call")
			}
			if !reflect.DeepEqual(health.purls, wantHealth) || !reflect.DeepEqual(resolver.names, wantResolver) {
				t.Fatalf("health=%v resolver=%v", health.purls, resolver.names)
			}
			if len(plan.Entries) != 4 || len(source.imports) != 4 {
				t.Fatalf("private dependencies were removed from local analysis")
			}
			for _, entry := range plan.Entries {
				want := "Private"
				if entry.PURL == publicPy {
					want = ""
					if offline {
						want = "Unknown"
					}
				}
				if entry.Health.MaintenanceStatus != want || entry.Health.HealthRisk != 0.5 {
					t.Fatalf("unexpected health: %+v", entry.Health)
				}
			}
		})
	}
}

func TestInvalidPrivacyPatternStopsBeforeAnalysis(t *testing.T) {
	for _, input := range []DietInput{{PrivatePatterns: "secret["}, {GoPrivatePatterns: "secret["}} {
		_, err := NewService(nil, nil, nil, nil).Run(context.Background(), input)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("expected a redacted pattern error, got %v", err)
		}
	}
}

func TestPrivateOnlyBatchHasNoHealthRequest(t *testing.T) {
	health := &captureHealthSource{}
	resolver := &recordingResolver{}
	const purl = "pkg:pypi/acme@1.0"
	svc := NewService(&stubGraphAnalyzer{result: &domaindiet.GraphResult{DirectDeps: []string{purl}}}, &stubSourceAnalyzer{}, resolver, application.NewAnalysisService(health))
	_, err := svc.Run(context.Background(), DietInput{SourceRoot: t.TempDir(), PrivatePatterns: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if health.calls != 0 || resolver.names != nil {
		t.Fatalf("private-only batch reached external analysis")
	}
}
