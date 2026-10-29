//go:build cgo

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzePyPICommandUsage(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Makefile", "serve:\n\tuv run uvicorn app:app\n# hypercorn app:app\n")
	write("Dockerfile.api", "CMD [\"gunicorn\", \"app:app\"]\nCMD [\"uv\", \"run\", \"custom-server\"]\n# ENTRYPOINT [\"daphne\"]\n")
	write("Procfile", "# worker: procfile-server\nweb: hypercorn app:app\n")
	write("scripts/deploy.sh", "# daphne app:app\necho 'daphne app:app'\ncat <<EOF\nheredoc-server --flag\nEOF\n")
	write(".github/workflows/deploy.yml", "jobs:\n  deploy:\n    steps:\n      - run: |\n          pytest -q\n          # daphne app:app\n      - run: 'quoted-server --flag'\n")
	write("pyproject.toml", "[tool.ruff]\nline-length = 100\n[tool.black]\nline-length = 100\n[project.scripts]\nmy-app = 'project_api.cli:main'\n")
	write("README.md", "Run `daphne app:app` when deploying.\n")

	commands := map[string][]string{
		"pkg:pypi/uvicorn@1":         {"uvicorn"},
		"pkg:pypi/gunicorn@1":        {"gunicorn"},
		"pkg:pypi/hypercorn@1":       {"hypercorn"},
		"pkg:pypi/daphne@1":          {"daphne"},
		"pkg:pypi/pytest@1":          {"pytest"},
		"pkg:pypi/ruff@1":            {"ruff"},
		"pkg:pypi/black@1":           {"black"},
		"pkg:pypi/project-api@1":     nil,
		"pkg:pypi/custom-server@1":   {"custom-server"},
		"pkg:pypi/quoted-server@1":   {"quoted-server"},
		"pkg:pypi/procfile-server@1": {"procfile-server"},
		"pkg:pypi/heredoc-server@1":  {"heredoc-server"},
	}
	imports := map[string][]string{
		"pkg:pypi/uvicorn@1":     {"uvicorn"},
		"pkg:pypi/project-api@1": {"project_api"},
	}
	got, err := NewAnalyzer().AnalyzePyPICommandUsage(context.Background(), root, commands, imports)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{}{
		"pkg:pypi/uvicorn@1":       {},
		"pkg:pypi/gunicorn@1":      {},
		"pkg:pypi/hypercorn@1":     {},
		"pkg:pypi/pytest@1":        {},
		"pkg:pypi/ruff@1":          {},
		"pkg:pypi/black@1":         {},
		"pkg:pypi/project-api@1":   {},
		"pkg:pypi/custom-server@1": {},
		"pkg:pypi/quoted-server@1": {},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AnalyzePyPICommandUsage() = %v, want %v", got, want)
	}
}

func FuzzSplitShellCommands(f *testing.F) {
	f.Add("uv run uvicorn app:app # server")
	f.Add("echo 'a;b' && python -m uvicorn")
	f.Add("echo mentions uvicorn; ruff check .")
	f.Fuzz(func(t *testing.T, line string) {
		for _, words := range splitShellCommands(line) {
			for _, word := range words {
				if word == "" {
					t.Fatal("shell tokenizer returned an empty token")
				}
			}
			_ = commandScript(words)
		}
	})
}

func FuzzPyprojectCommandUsage(f *testing.F) {
	f.Add([]byte("[tool.ruff]\nline-length = 100\n"))
	f.Add([]byte("[project.scripts]\napp = 'uvicorn.main:main'\n"))
	f.Add([]byte("[tool.bad\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		pyprojectCommandUsage(data,
			map[string][]string{"pkg:pypi/uvicorn@1": {"uvicorn"}},
			map[string][]string{"pkg:pypi/uvicorn@1": {"uvicorn"}},
		)
	})
}

func FuzzHeredocDelimiters(f *testing.F) {
	f.Add("cat <<EOF\ncustom-server --flag\nEOF\n")
	f.Add("cat <<'END TEXT'\nplain body\nEND TEXT\n")
	f.Add("echo \"<<NOT_A_HEREDOC\" # <<COMMENT\n")
	f.Fuzz(func(t *testing.T, input string) {
		for _, delimiter := range heredocDelimiters(input) {
			if delimiter == "" {
				t.Fatal("empty heredoc delimiter")
			}
		}
		stripHeredocBodies(strings.Split(input, "\n"))
	})
}

func FuzzWorkflowRunLines(f *testing.F) {
	f.Add([]byte("steps:\n  - run: 'custom-server --flag'\n"))
	f.Add([]byte("steps:\n  - run: |\n      cat <<EOF\n      body\n      EOF\n"))
	f.Add([]byte("[broken\n"))
	f.Fuzz(func(t *testing.T, input []byte) {
		workflowRunLines(input)
	})
}
