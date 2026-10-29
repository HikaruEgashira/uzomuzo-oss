//go:build cgo

package treesitter

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/pelletier/go-toml/v2"
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
	var manifest map[string]any
	if err := toml.Unmarshal(data, &manifest); err != nil {
		return "", false
	}
	project, ok := manifest["project"].(map[string]any)
	if !ok {
		return "", false
	}
	name, ok := project["name"].(string)
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

func isPythonEnvironmentDir(name string) bool {
	switch name {
	case ".venv", "venv", ".tox":
		return true
	default:
		return false
	}
}
