//go:build cgo

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExtractPyPIProjectName(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
		ok   bool
	}{
		{
			name: "project table after tool metadata",
			src:  "[tool.poetry]\nname = \"tool-name\"\n[project]\nname = \"app_name\" # comment\n",
			want: "app_name",
			ok:   true,
		},
		{
			name: "single quoted project name",
			src:  "[project]\nname = 'app-name'\n",
			want: "app-name",
			ok:   true,
		},
		{
			name: "hash inside a quoted name is not a comment",
			src:  "[project]\nname = \"app#name\"\n",
			want: "app#name",
			ok:   true,
		},
		{
			name: "multiline description with key-like text",
			src:  "[project]\ndescription = \"\"\"\nname = \"shadow\"\n\"\"\"\nname = \"real\"\n",
			want: "real",
			ok:   true,
		},
		{
			name: "inline table does not hide project name",
			src:  "[project]\nurls = { Homepage = \"https://example.invalid\", Documentation = \"https://docs.example.invalid\" }\nname = \"real\"\n",
			want: "real",
			ok:   true,
		},
		{
			name: "quoted project key",
			src:  "[project]\n\"name\" = \"quoted-project\"\n",
			want: "quoted-project",
			ok:   true,
		},
		{
			name: "dotted project key",
			src:  "project.name = \"dotted-project\"\n",
			want: "dotted-project",
			ok:   true,
		},
		{
			name: "invalid TOML is ignored",
			src:  "[project]\nname = \"first\"\nname = \"duplicate\"\n",
		},
		{
			name: "tool table without project table",
			src:  "[tool.poetry]\nname = \"not-the-project\"\n",
		},
		{
			name: "optional dependency name does not replace project name",
			src:  "[project]\nversion = \"1.0\"\n[project.optional-dependencies]\nname = \"not-the-project\"\n",
		},
		{
			name: "unquoted value is ignored",
			src:  "[project]\nname = app_name\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractPyPIProjectName([]byte(tt.src))
			if got != tt.want || ok != tt.ok {
				t.Errorf("extractPyPIProjectName() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestAnalyzer_PyPIProjectNames(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"pyproject.toml": `[project]
name = "Root_Project"
description = """
name = "not-the-project"
"""
urls = { Homepage = "https://example.invalid" }
`,
		"packages/api/pyproject.toml":                               "[project]\nname = 'Local.Project'\n",
		"tools/pyproject.toml":                                      "[tool.poetry]\nname = \"not-a-project-table\"\n",
		"broken/pyproject.toml":                                     "[project]\nname = \"not-a-project\"\nname = \"duplicate\"\n",
		".venv/lib/python/site-packages/third-party/pyproject.toml": "[project]\nname = \"third-party\"\n",
	}
	for path, contents := range files {
		fullPath := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	analyzer := NewAnalyzer()
	t.Cleanup(analyzer.Close)
	got, err := analyzer.PyPIProjectNames(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Local.Project", "Root_Project"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PyPIProjectNames() = %v, want %v", got, want)
	}
}
