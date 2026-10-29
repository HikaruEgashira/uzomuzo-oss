package gomod

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/future-architect/uzomuzo-oss/internal/domain/depparser"
)

// ReadDependencyScopes reads Go tool and indirect-only module names beneath sourceRoot.
// Names are lowercased to match normalized SBOM PURLs; direct usage in any module wins.
func ReadDependencyScopes(ctx context.Context, sourceRoot string) (tools, indirect map[string]struct{}, err error) {
	if sourceRoot == "" {
		return nil, nil, nil
	}
	tools, indirect = map[string]struct{}{}, map[string]struct{}{}
	direct := map[string]struct{}{}
	root, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve source root: %w", err)
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "vendor" || entry.Name() == "testdata" || entry.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "go.mod" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		deps, err := (&Parser{}).Parse(ctx, data)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		for _, dep := range deps {
			name := strings.ToLower(dep.Name)
			if dep.Scope == "tool" {
				tools[name] = struct{}{}
			}
			if dep.Relation == depparser.RelationDirect || dep.Scope == "tool" {
				direct[name] = struct{}{}
			} else if dep.Relation == depparser.RelationTransitive {
				indirect[name] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read Go dependency scopes: %w", err)
	}
	for name := range direct {
		delete(indirect, name)
	}
	return tools, indirect, nil
}
