package gomod

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadDependencyScopesMultimodule(t *testing.T) {
	tools, indirect, err := ReadDependencyScopes(context.Background(), "testdata/multimodule")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tools, map[string]struct{}{"example.com/tool": {}}) ||
		!reflect.DeepEqual(indirect, map[string]struct{}{"gopkg.in/datadog/dd-trace-go.v1": {}}) {
		t.Fatalf("tools=%v indirect=%v", tools, indirect)
	}
}

func TestReadDependencyScopesSkippedDirectoriesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"vendor", "testdata", "node_modules", ".hidden"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("not a go.mod"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "go.mod"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"linked": outside, "go.mod": filepath.Join(outside, "go.mod")} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	_, indirect, err := ReadDependencyScopes(context.Background(), root)
	if err != nil || len(indirect) != 0 {
		t.Fatalf("indirect=%v err=%v", indirect, err)
	}
}

func TestReadDependencyScopesRejectsPartialScan(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	tools, indirect, err := ReadDependencyScopes(context.Background(), root)
	if err == nil || tools != nil || indirect != nil {
		t.Fatalf("partial scan accepted: tools=%v indirect=%v err=%v", tools, indirect, err)
	}
}
