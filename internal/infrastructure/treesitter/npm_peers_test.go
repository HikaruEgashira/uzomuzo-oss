//go:build cgo

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNpmPeerDependencies_NodeModulesMetadata(t *testing.T) {
	root := t.TempDir()
	writeNpmPeerFixture(t, root, "node_modules/next/package.json", `{
  "name": "next",
  "version": "14.0.0",
  "peerDependencies": {"react": "^18", "react-dom": "^18", "optional-adapter": "*"},
  "peerDependenciesMeta": {"optional-adapter": {"optional": true}}
}`)

	got, err := NewAnalyzer().NpmPeerDependencies(context.Background(), root, []string{"pkg:npm/next@14.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"pkg:npm/next@14.0.0": {"react", "react-dom"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NpmPeerDependencies() = %#v, want %#v", got, want)
	}
}

func TestNpmPeerDependencies_PackageLockV2AndV3(t *testing.T) {
	for _, version := range []int{2, 3} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			root := t.TempDir()
			writeNpmPeerFixture(t, root, "package-lock.json", `{
  "lockfileVersion": `+string(rune('0'+version))+`,
  "packages": {
    "node_modules/next": {
      "version": "14.0.0",
      "peerDependencies": {"react": "^18", "react-dom": "^18", "optional-peer": "*"},
      "peerDependenciesMeta": {"optional-peer": {"optional": true}}
    }
  }
}`)

			got, err := NewAnalyzer().NpmPeerDependencies(context.Background(), root, []string{"pkg:npm/next@14.0.0"})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string][]string{"pkg:npm/next@14.0.0": {"react", "react-dom"}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("NpmPeerDependencies() = %#v, want %#v", got, want)
			}
		})
	}
}

func TestNpmPeerDependencies_PnpmLock(t *testing.T) {
	root := t.TempDir()
	writeNpmPeerFixture(t, root, "pnpm-lock.yaml", `lockfileVersion: '9.0'
packages:
  '@mui/material@5.15.0':
    peerDependencies:
      '@emotion/react': ^11.0.0
      '@emotion/styled': ^11.0.0
    peerDependenciesMeta:
      '@emotion/styled':
        optional: true
`)

	got, err := NewAnalyzer().NpmPeerDependencies(context.Background(), root, []string{"pkg:npm/%40mui/material@5.15.0"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"pkg:npm/%40mui/material@5.15.0": {"@emotion/react"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NpmPeerDependencies() = %#v, want %#v", got, want)
	}
}

func TestNpmPeerDependencies_PnpmLegacyPackageKey(t *testing.T) {
	root := t.TempDir()
	writeNpmPeerFixture(t, root, "pnpm-lock.yaml", `lockfileVersion: '6.0'
packages:
  /next/14.0.0_react@18.2.0:
    peerDependencies:
      react: ^18.0.0
      react-dom: ^18.0.0
`)

	got, err := NewAnalyzer().NpmPeerDependencies(context.Background(), root, []string{"pkg:npm/next@14.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"pkg:npm/next@14.0.0": {"react", "react-dom"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NpmPeerDependencies() = %#v, want %#v", got, want)
	}
}

func TestNpmPeerDependencies_OptionalPeerDoesNotFallThroughToStaticTable(t *testing.T) {
	root := t.TempDir()
	writeNpmPeerFixture(t, root, "node_modules/next/package.json", `{
  "name": "next", "version": "14.0.0",
  "peerDependencies": {"react-dom": "^18"},
  "peerDependenciesMeta": {"react-dom": {"optional": true}}
}`)

	got, err := NewAnalyzer().NpmPeerDependencies(context.Background(), root, []string{"pkg:npm/next@14.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("optional peer fell through to static fallback: %#v", got)
	}
}

func TestNpmPeerDependencies_StaticFallback(t *testing.T) {
	root := t.TempDir()
	got, err := NewAnalyzer().NpmPeerDependencies(context.Background(), root, []string{
		"pkg:npm/next@14.0.0",
		"pkg:npm/%40mui/material@5.15.0",
		"pkg:npm/react-redux@9.0.0",
		"pkg:npm/%40testing-library/react@15.0.0",
		"pkg:npm/unknown-host@1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"pkg:npm/next@14.0.0":                     {"react", "react-dom"},
		"pkg:npm/%40mui/material@5.15.0":          {"@emotion/react", "@emotion/styled"},
		"pkg:npm/react-redux@9.0.0":               {"react", "redux"},
		"pkg:npm/%40testing-library/react@15.0.0": {"react", "react-dom"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NpmPeerDependencies() = %#v, want %#v", got, want)
	}
}

func TestNpmPeerDependencies_LocalMetadataOverridesFallbacks(t *testing.T) {
	root := t.TempDir()
	writeNpmPeerFixture(t, root, "node_modules/next/package.json", `{
  "name": "next", "version": "14.0.0",
  "peerDependencies": {"react": "^18"}
}`)
	writeNpmPeerFixture(t, root, "package-lock.json", `{
  "lockfileVersion": 3,
  "packages": {"node_modules/next": {
    "version": "14.0.0", "peerDependencies": {"react": "^18", "react-dom": "^18"}
  }}
}`)

	got, err := NewAnalyzer().NpmPeerDependencies(context.Background(), root, []string{"pkg:npm/next@14.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"pkg:npm/next@14.0.0": {"react"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NpmPeerDependencies() = %#v, want %#v", got, want)
	}
}

func TestNpmPeerDependencies_VersionMismatchFallsBackToLockfile(t *testing.T) {
	root := t.TempDir()
	writeNpmPeerFixture(t, root, "node_modules/next/package.json", `{
  "name": "next", "version": "15.0.0",
  "peerDependencies": {"react": "^19"}
}`)
	writeNpmPeerFixture(t, root, "package-lock.json", `{
  "lockfileVersion": 3,
  "packages": {"node_modules/next": {
    "version": "14.0.0", "peerDependencies": {"react": "^18"}
  }}
}`)

	got, err := NewAnalyzer().NpmPeerDependencies(context.Background(), root, []string{"pkg:npm/next@14.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"pkg:npm/next@14.0.0": {"react"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NpmPeerDependencies() = %#v, want %#v", got, want)
	}
}

func writeNpmPeerFixture(t *testing.T, root, relativePath, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
