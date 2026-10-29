//go:build cgo

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzer_RubyGemCoupling(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		imports map[string][]string
		want    map[string]int
		absent  []string
	}{
		{
			name:   "require is a side-effect import",
			source: `require "mysql2"`,
			imports: map[string][]string{
				"pkg:gem/mysql2@0.5.6": {rubyRequireKeyPrefix + "mysql2", rubyConstantKeyPrefix + "mysql2"},
			},
			want: map[string]int{"pkg:gem/mysql2@0.5.6": 1},
		},
		{
			name: "Bundler.require constants",
			source: `Bundler.require(*Rails.groups)
FactoryBot.create(:user)
FactoryBot::SyntaxRunner
`,
			imports: map[string][]string{
				"pkg:gem/factory_bot_rails@6.4.2": {
					rubyRequireKeyPrefix + "factory_bot_rails",
					rubyConstantKeyPrefix + "factorybotrails",
					rubyConstantKeyPrefix + "factorybot",
				},
			},
			want: map[string]int{"pkg:gem/factory_bot_rails@6.4.2": 2},
		},
		{
			name:   "scope resolution ignores the name-side constant",
			source: `FactoryBot::SyntaxRunner`,
			imports: map[string][]string{
				"pkg:gem/factory_bot_rails@6.4.2": {
					rubyRequireKeyPrefix + "factory_bot_rails",
					rubyConstantKeyPrefix + "factorybotrails",
					rubyConstantKeyPrefix + "factorybot",
				},
				"pkg:gem/syntax_runner@1.0.0": {
					rubyRequireKeyPrefix + "syntax_runner",
					rubyConstantKeyPrefix + "syntaxrunner",
				},
			},
			want:   map[string]int{"pkg:gem/factory_bot_rails@6.4.2": 1},
			absent: []string{"pkg:gem/syntax_runner@1.0.0"},
		},
		{
			name: "class and module names are not package use",
			source: `class Config
end
module Config
end
class Foo::Bar
end
module Example
  class Config
  end
end
`,
			imports: map[string][]string{
				"pkg:gem/config@5.5.0": {
					rubyRequireKeyPrefix + "config",
					rubyConstantKeyPrefix + "config",
				},
				"pkg:gem/foo@1.0.0": {
					rubyRequireKeyPrefix + "foo",
					rubyConstantKeyPrefix + "foo",
				},
				"pkg:gem/bar@1.0.0": {
					rubyRequireKeyPrefix + "bar",
					rubyConstantKeyPrefix + "bar",
				},
				"pkg:gem/example@1.0.0": {
					rubyRequireKeyPrefix + "example",
					rubyConstantKeyPrefix + "example",
				},
			},
			absent: []string{"pkg:gem/config@5.5.0", "pkg:gem/foo@1.0.0", "pkg:gem/bar@1.0.0", "pkg:gem/example@1.0.0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "app.rb"), []byte(tt.source), 0o644); err != nil {
				t.Fatal(err)
			}

			analyzer := NewAnalyzer()
			t.Cleanup(analyzer.Close)
			got, err := analyzer.AnalyzeCoupling(context.Background(), dir, tt.imports)
			if err != nil {
				t.Fatal(err)
			}
			for purl, wantCalls := range tt.want {
				analysis, ok := got[purl]
				if !ok {
					t.Errorf("missing coupling for %s", purl)
					continue
				}
				if analysis.IsUnused {
					t.Errorf("%s marked unused", purl)
				}
				if analysis.ImportFileCount != 1 {
					t.Errorf("%s import files = %d, want 1", purl, analysis.ImportFileCount)
				}
				if analysis.CallSiteCount != wantCalls {
					t.Errorf("%s call sites = %d, want %d", purl, analysis.CallSiteCount, wantCalls)
				}
			}
			for _, purl := range tt.absent {
				if _, ok := got[purl]; ok {
					t.Errorf("%s got coupling from a definition or scope-name constant", purl)
				}
			}
		})
	}
}

func TestAnalyzer_RubyFileDiscovery(t *testing.T) {
	files := []string{"app.rb", "tasks.rake", "library.gemspec", "config.ru", "Gemfile", "Rakefile"}
	dir := t.TempDir()
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`FactoryBot.create(:user)`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	analyzer := NewAnalyzer()
	t.Cleanup(analyzer.Close)
	got, err := analyzer.AnalyzeCoupling(context.Background(), dir, map[string][]string{
		"pkg:gem/factory_bot_rails@6.4.2": {
			rubyRequireKeyPrefix + "factory_bot_rails",
			rubyConstantKeyPrefix + "factorybotrails",
			rubyConstantKeyPrefix + "factorybot",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	analysis := got["pkg:gem/factory_bot_rails@6.4.2"]
	if analysis == nil {
		t.Fatal("missing factory_bot_rails coupling")
	}
	if analysis.ImportFileCount != len(files) || analysis.CallSiteCount != len(files) || analysis.IsUnused {
		t.Errorf("Ruby file coupling = %+v, want %d files and calls and not unused", *analysis, len(files))
	}
}
