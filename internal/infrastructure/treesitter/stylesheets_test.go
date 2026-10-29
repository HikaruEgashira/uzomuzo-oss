//go:build cgo

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStylesheetPackageUsage(t *testing.T) {
	for _, tt := range []struct {
		name, source, pkg string
		used              bool
	}{
		{"main.css", `@import "tailwindcss";`, "tailwindcss", true},
		{"main.pcss", `@import url("kit/css");`, "kit", true},
		{"main.postcss", `@plugin "kit";`, "kit", true},
		{"main.scss", `@use "~kit/theme";`, "kit", true},
		{"main.sass", `@forward "kit/theme"`, "kit", true},
		{"main.less", `@import (reference) "~kit";`, "kit", true},
		{"source-traversal.css", `@source "../../node_modules/kit/../../local";`, "kit", false},
		{"source-normalized.css", `@source "../../node_modules/other/../kit";`, "kit", true},
		{"source.css", `@source "../../node_modules/@scope/kit/src";`, "@scope/kit", true},
		{"config.css", `@config "kit/config";`, "kit", true},
		{"relative.css", `@import "./kit/css";`, "kit", false},
		{"remote.css", `@import "https://example.com/kit";`, "kit", false},
		{"remote-source.css", `@source "https://example.com/node_modules/kit";`, "kit", false},
		{"open-comment.css", `/* @import "kit";`, "kit", false},
		{"comment.css", `/* @import "kit"; */`, "kit", false},
		{"string.css", `p { content: '@import "kit";'; }`, "kit", false},
		{"line.scss", "// @use \"kit\";\n", "kit", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tt.name), []byte(tt.source), 0600); err != nil {
				t.Fatal(err)
			}
			a := NewAnalyzer()
			t.Cleanup(a.Close)
			purl := "pkg:npm/" + tt.pkg + "@1.0.0"
			got, err := a.AnalyzeCoupling(context.Background(), dir, map[string][]string{purl: {tt.pkg}})
			if err != nil {
				t.Fatal(err)
			}
			result := got[purl]
			if !tt.used {
				if result != nil {
					t.Fatalf("unexpected usage: %+v", result)
				}
				return
			}
			if result == nil || result.IsUnused || !result.HasBlankImport || result.ImportFileCount != 1 {
				t.Fatalf("missing stylesheet usage: %+v", result)
			}
		})
	}
}

// FuzzStylesheetDirectives exercises malformed comments, strings, and paths.
func FuzzStylesheetDirectives(f *testing.F) {
	for _, seed := range []string{`@import "kit";`, `@source "../../node_modules/kit";`, `/* @import "kit"`, `@import url("kit/css");`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > largeFileBytes {
			t.Skip()
		}
		accum := map[string]*accumulator{}
		analyzeStylesheet(data, "main.css", map[string][]string{"kit": {"pkg:npm/kit@1.0.0"}}, accum)
		for _, a := range accum {
			if !a.hasBlankImport || len(a.importFiles) != 1 {
				t.Fatal("invalid stylesheet coupling")
			}
		}
	})
}
