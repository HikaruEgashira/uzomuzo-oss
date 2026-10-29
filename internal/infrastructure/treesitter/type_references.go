//go:build cgo

package treesitter

import (
	"encoding/json"
	"encoding/xml"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

func referenceTypeName(comment string) string {
	if !strings.HasPrefix(comment, "///") {
		return ""
	}
	var reference struct {
		XMLName xml.Name
		Types   string `xml:"types,attr"`
	}
	if xml.Unmarshal([]byte(strings.TrimPrefix(comment, "///")), &reference) != nil || reference.XMLName.Local != "reference" || reference.XMLName.Space != "" {
		return ""
	}
	return reference.Types
}

func recordTypeReference(name, file string, imports map[string][]string, accum map[string]*accumulator) {
	name = strings.TrimPrefix(name, "@types/")
	if strings.HasPrefix(name, "@") {
		name = strings.ReplaceAll(strings.TrimPrefix(name, "@"), "/", "__")
	}
	for _, purl := range imports["@types/"+name] {
		acc := accum[purl]
		if acc == nil {
			acc = &accumulator{importFiles: map[string]bool{}, symbols: map[string]bool{}}
			accum[purl] = acc
		}
		acc.importFiles[file] = true
	}
}

func configTypeNames(parser *sitter.Parser, data []byte) []string {
	const prefix = "const config = ("
	src := append([]byte(prefix), data...)
	src = append(src, []byte(");")...)
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return nil
	}
	// Reuse syntax nodes to remove JSONC trivia without altering strings.
	nodes := []*sitter.Node{root}
	for len(nodes) > 0 {
		node := nodes[len(nodes)-1]
		nodes = nodes[:len(nodes)-1]
		if node.Kind() == "comment" {
			for i := node.StartByte(); i < node.EndByte(); i++ {
				src[i] = ' '
			}
			continue
		}
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child.Kind() == "," && (node.Kind() == "object" || node.Kind() == "array") {
				next := i + 1
				for next < node.ChildCount() && node.Child(next).Kind() == "comment" {
					next++
				}
				if next < node.ChildCount() && (node.Child(next).Kind() == "}" || node.Child(next).Kind() == "]") {
					src[child.StartByte()] = ' '
				}
			}
			nodes = append(nodes, child)
		}
	}
	var config struct {
		CompilerOptions struct {
			Types *[]string `json:"types"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal(src[len(prefix):len(src)-2], &config) != nil {
		return nil
	}
	// shortcut: inherited type lists are not resolved; follow extends when inherited configuration must be exact.
	if config.CompilerOptions.Types == nil {
		return []string{"node", "jest", "mocha", "jasmine", "bun"}
	}
	return *config.CompilerOptions.Types
}

func typeDeclarationImport(value string) string {
	if strings.HasPrefix(value, "node:") {
		return "@types/node"
	}
	parts := strings.Split(value, "/")
	if strings.HasPrefix(value, "@") && len(parts) > 1 {
		if parts[0] == "@types" {
			return parts[0] + "/" + parts[1]
		}
		return "@types/" + strings.TrimPrefix(parts[0], "@") + "__" + parts[1]
	}
	return "@types/" + parts[0]
}
