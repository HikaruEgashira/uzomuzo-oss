//go:build cgo

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTypeConfigAndReferences(t *testing.T) {
	for _, tt := range []struct {
		name, file, source string
		node, custom       bool
	}{
		{"ambient", "tsconfig.json", `{}`, true, false},
		{"jsonc", "tsconfig.build.json", "// heading\n{\"url\":\"https://example.com/a//b\",\"compilerOptions\":{\"types\":[\"custom\", /* trailing */],},}", false, true},
		{"empty", "tsconfig.json", `{"compilerOptions":{"types":[]}}`, false, false},
		{"invalid", "tsconfig.json", `{"compilerOptions":`, false, false},
		{"not-json", "tsconfig.json", `{...factory()}`, false, false},
		{"reference", "types.d.ts", "/// <reference types=\"custom\" />\nexport {};", false, true},
		{"late-reference", "types.ts", "export {};\n/// <reference types=\"custom\" />", false, false},
		{"string", "types.ts", "const text = '/// <reference types=\"custom\" />';", false, false},
		{"block-comment", "types.ts", "/*\n/// <reference types=\"custom\" />\n*/", false, false},
		{"vendor-config", filepath.Join("node_modules", "tsconfig.json"), `{}`, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, tt.file)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.source), 0600); err != nil {
				t.Fatal(err)
			}
			a := NewAnalyzer()
			defer a.Close()
			got, err := a.AnalyzeCoupling(context.Background(), root, map[string][]string{"node": {"@types/node"}, "custom": {"@types/custom"}})
			if err != nil {
				t.Fatal(err)
			}
			if (got["node"] != nil && !got["node"].Unanalyzed) != tt.node || (got["custom"] != nil && !got["custom"].Unanalyzed) != tt.custom {
				t.Fatalf("node=%+v custom=%+v", got["node"], got["custom"])
			}
			for _, c := range got {
				if c.IsUnused || (!c.Unanalyzed && c.ImportFileCount != 1) {
					t.Fatalf("type reference=%+v", c)
				}
			}
		})
	}
}

func TestTypeConfigSymlinkIsSkipped(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "tsconfig.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "tsconfig.json")); err != nil {
		t.Fatal(err)
	}
	a := NewAnalyzer()
	defer a.Close()
	result, err := a.AnalyzeCoupling(context.Background(), root, map[string][]string{"node": {"@types/node"}})
	if err != nil || result["node"] != nil {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

func TestTypeMetadataDoesNotInventRuntimeAnalysis(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "tsconfig.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	a := NewAnalyzer()
	defer a.Close()
	result, err := a.AnalyzeCoupling(context.Background(), root, map[string][]string{"node": {"@types/node"}, "react": {"react"}})
	if err != nil {
		t.Fatal(err)
	}
	if result["react"] == nil || !result["react"].Unanalyzed || result["react"].IsUnused {
		t.Fatalf("runtime analysis=%+v", result["react"])
	}
}

func TestTypeOnlyImportsAttributeDeclarationsOnly(t *testing.T) {
	for _, source := range []string{`import type { Foo } from "@scope/name/subpath";`, `export type { Foo } from "@scope/name";`, `import { type Foo } from "@scope/name";`, `export { type Foo } from "@scope/name";`} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "app.ts"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		a := NewAnalyzer()
		result, err := a.AnalyzeCoupling(context.Background(), root, map[string][]string{"runtime": {"@scope/name"}, "types": {"@types/scope__name"}})
		a.Close()
		if err != nil {
			t.Fatal(err)
		}
		if result["types"] == nil || result["types"].IsUnused || result["types"].ImportFileCount != 1 {
			t.Fatalf("declaration usage=%+v", result["types"])
		}
		if result["runtime"] != nil && !result["runtime"].Unanalyzed {
			t.Fatalf("invented runtime coupling=%+v", result["runtime"])
		}
	}
}

func TestMixedTypeAndRuntimeImportsKeepRuntimeUsage(t *testing.T) {
	for _, code := range []string{`import { type Foo, bar } from "pkg";`, `import value, { type Foo } from "pkg";`, `export { type Foo, bar } from "pkg";`} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "app.ts"), []byte(code), 0600); err != nil {
			t.Fatal(err)
		}
		a := NewAnalyzer()
		result, err := a.AnalyzeCoupling(context.Background(), root, map[string][]string{"runtime": {"pkg"}, "types": {"@types/pkg"}})
		a.Close()
		if err != nil {
			t.Fatal(err)
		}
		if result["runtime"] == nil || result["runtime"].Unanalyzed || result["runtime"].IsUnused {
			t.Fatalf("missing mixed runtime coupling: %+v", result["runtime"])
		}
	}
}
