//go:build cgo

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// FuzzAnalyzeJavaScriptRequire feeds arbitrary JavaScript source through the
// coupling analysis, including the require() binding walk, to check that
// malformed, truncated, or deeply nested input does not panic or hang.
func FuzzAnalyzeJavaScriptRequire(f *testing.F) {
	seeds := []string{
		`const ipc = require("pkg").default; ipc.serve(() => {});`,
		`const post = require("pkg").post; post({});`,
		`const sub = require("pkg").api.v2; sub.run();`,
		`var X = window.X || require("pkg").default; X.on("tap");`,
		`const x = ready && require("pkg").api; x.run();`,
		`const x = require("pkg").flag && local; x();`,
		`const x = c ? require("pkg").a : b; x.go();`,
		`deps.ipc = require("pkg").default; deps.ipc.serve();`,
		`module.exports = require("pkg").helpers;`,
		`const x = (deps.x = require("pkg").y); x();`,
		`const p = (((require("pkg").api))); p.call();`,
		`const x = a || (b || (c || (d || (e || require("pkg").y)))); x();`,
		`const x = require("pkg").`,
		`const x = require("pkg"`,
		`require(`,
		`const { a } = require("pkg").b; a();`,
		`const x = require("pkg")["y"]; x();`,
		`const x = require(` + "`pkg`" + `).y;`,
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	analyzer := NewAnalyzer()
	f.Cleanup(analyzer.Close)
	importPaths := map[string][]string{"pkg:npm/pkg@1.0.0": {"pkg"}}

	f.Fuzz(func(t *testing.T, src string) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _ = analyzer.AnalyzeCoupling(context.Background(), dir, importPaths) // only panics and hangs matter here
	})
}

func FuzzExtractPyPIProjectName(f *testing.F) {
	for _, seed := range []string{
		"[project]\nname = \"sample-project\"\n",
		"[project]\ndescription = \"\"\"\nname = \"hidden\"\n\"\"\"\nname = \"sample-project\"\n",
		"[project]\nurls = { Home = \"https://example.invalid\" }\nname = \"sample-project\"\n",
		"project.name = \"sample-project\"\n",
		"[project]\nname = \"first\"\nname = \"duplicate\"\n",
		"[tool.poetry]\nname = \"ignored\"\n",
		"[project]\nname = 'sample_project' # comment\n",
		"",
		"[project",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = extractPyPIProjectName(data)
	})
}

// FuzzAnalyzeRubyRequire checks that arbitrary Ruby source does not panic in
// import extraction or the class/module name filters.
func FuzzAnalyzeRubyRequire(f *testing.F) {
	seeds := []string{
		`require "mysql2"`,
		`require "foo#{suffix}"`,
		`require "omniauth/google/oauth2"`,
		`Bundler.require(*Rails.groups)`,
		`FactoryBot.create(:user)`,
		`FactoryBot::SyntaxRunner`,
		`class Config; end`,
		`class Foo::Config; end`,
		`module Example; class Config; end; end`,
		"class",
		"",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	analyzer := NewAnalyzer()
	f.Cleanup(analyzer.Close)
	importPaths := map[string][]string{
		"pkg:gem/mysql2@0.5.6": {
			rubyRequireKeyPrefix + "mysql2",
			rubyConstantKeyPrefix + "mysql2",
		},
		"pkg:gem/factory_bot_rails@6.4.2": {
			rubyRequireKeyPrefix + "factory_bot_rails",
			rubyConstantKeyPrefix + "factorybotrails",
			rubyConstantKeyPrefix + "factorybot",
		},
		"pkg:gem/omniauth-google-oauth2@1.8.0": {
			rubyRequireKeyPrefix + "omniauth-google-oauth2",
			rubyRequireKeyPrefix + "omniauth/google/oauth2",
			rubyConstantKeyPrefix + "omniauthgoogleoauth2",
		},
		"pkg:gem/config@5.5.0": {
			rubyRequireKeyPrefix + "config",
			rubyConstantKeyPrefix + "config",
		},
		"pkg:gem/foo@1.0.0": {
			rubyRequireKeyPrefix + "foo",
			rubyConstantKeyPrefix + "foo",
		},
	}

	f.Fuzz(func(t *testing.T, src string) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "app.rb"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _ = analyzer.AnalyzeCoupling(context.Background(), dir, importPaths)
	})
}
