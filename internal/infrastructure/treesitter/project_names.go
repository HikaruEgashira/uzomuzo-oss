//go:build cgo

package treesitter

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// PyPIProjectNames returns names declared by [project].name tables under sourceRoot.
func (a *Analyzer) PyPIProjectNames(ctx context.Context, sourceRoot string) ([]string, error) {
	var names []string
	err := filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if skipDirs[entry.Name()] || isPythonEnvironmentDir(entry.Name()) {
				return filepath.SkipDir
			}
			if path != sourceRoot && isBuildOutputDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.Name() != "pyproject.toml" {
			return nil
		}

		info, err := entry.Info()
		if err != nil || info.Size() > largeFileBytes {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Debug("failed to read pyproject.toml", "path", path, "error", err)
			return nil
		}
		if name, ok := extractPyPIProjectName(data); ok {
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking source tree for Python project names: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("reading Python project names: %w", err)
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

func extractPyPIProjectName(data []byte) (string, bool) {
	if bytes.ContainsAny(data, "{}") || bytes.Contains(data, []byte(`"""`)) || bytes.Contains(data, []byte(`'''`)) {
		// Compound TOML values can make nested keys look like top-level keys.
		return "", false
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), int(largeFileBytes))
	section := ""
	for scanner.Scan() {
		line := strings.TrimSpace(stripTOMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				section = ""
				continue
			}
			section = strings.TrimSpace(line[1:end])
			continue
		}
		if section != "project" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "name" {
			continue
		}
		return parseTOMLString(strings.TrimSpace(value))
	}
	return "", false
}

func parseTOMLString(value string) (string, bool) {
	if len(value) < 2 || strings.HasPrefix(value, `"""`) || strings.HasPrefix(value, `'''`) {
		return "", false
	}
	if value[0] == '"' {
		parsed, err := strconv.Unquote(value)
		return parsed, err == nil
	}
	if value[0] != '\'' || value[len(value)-1] != '\'' {
		return "", false
	}
	parsed := value[1 : len(value)-1]
	if strings.ContainsRune(parsed, '\'') {
		return "", false
	}
	return parsed, true
}

func stripTOMLComment(line string) string {
	var quote byte
	escaped := false
	for i := 0; i < len(line); i++ {
		char := line[i]
		if quote == '"' {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == quote {
				quote = 0
			}
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			}
			continue
		}
		if char == '"' || char == '\'' {
			quote = char
			continue
		}
		if char == '#' {
			return line[:i]
		}
	}
	return line
}

func isPythonEnvironmentDir(name string) bool {
	switch name {
	case ".venv", "venv", ".tox":
		return true
	default:
		return false
	}
}
