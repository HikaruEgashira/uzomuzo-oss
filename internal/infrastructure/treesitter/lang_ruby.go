//go:build cgo

package treesitter

import (
	"strings"

	"github.com/package-url/packageurl-go"
	sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
)

const (
	rubyRequireKeyPrefix  = "require:"
	rubyConstantKeyPrefix = "const:"
)

func registerRubyConfig(a *Analyzer) {
	a.configs[langRuby] = &langConfig{
		language: loadLanguage(tree_sitter_ruby.Language()),
		importQuery: strings.Join([]string{
			`(call method: (identifier) @_method arguments: (argument_list . (string (string_content) @import)) (#eq? @_method "require"))`,
			`(constant) @import`,
		}, "\n"),
		callQuery: strings.Join([]string{
			`(call receiver: (constant) @pkg method: (identifier) @field)`,
			`(scope_resolution scope: (constant) @pkg name: (constant) @field)`,
			`(superclass (constant) @func)`,
		}, "\n"),
	}
}

func (a *Analyzer) handleRubyImport(
	node *sitter.Node,
	src []byte,
	importPath string,
	importToPURL map[string][]string,
	aliasMap map[string][]string,
) {
	if node.Kind() == "string_content" {
		if !isStaticRubyStringContent(node) {
			return
		}
		purls := rubyImportPURLs(strings.ToLower(importPath), importToPURL, rubyRequireKeyPrefix, true)
		if len(purls) > 0 {
			key := blankImportAlias + importPath
			aliasMap[key] = appendUniquePURLs(aliasMap[key], purls)
		}
		return
	}
	if node.Kind() != "constant" || isRubyDefinitionName(node) || isRubyScopeName(node) {
		return
	}

	constant := node.Utf8Text(src)
	key := normalizeRubyConstant(constant)
	purls := rubyImportPURLs(key, importToPURL, rubyConstantKeyPrefix, false)
	if len(purls) > 0 {
		aliasMap[constant] = appendUniquePURLs(aliasMap[constant], purls)
	}
}

func rubyImportPURLs(key string, importToPURL map[string][]string, kindPrefix string, prefix bool) []string {
	var matched []string
	for importKey, purls := range importToPURL {
		importPath, ok := strings.CutPrefix(importKey, kindPrefix)
		if !ok {
			continue
		}
		if importPath != key && (!prefix || !strings.HasPrefix(key, importPath+"/")) {
			continue
		}
		for _, purl := range purls {
			parsed, err := packageurl.FromString(purl)
			if err == nil && parsed.Type == "gem" {
				matched = append(matched, purl)
			}
		}
	}
	return matched
}

func isStaticRubyStringContent(node *sitter.Node) bool {
	stringNode := node.Parent()
	if stringNode == nil || stringNode.Kind() != "string" || stringNode.NamedChildCount() != 1 {
		return false
	}
	return sameRubyNode(node, stringNode.NamedChild(0))
}

func normalizeRubyConstant(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", ""))
}

func isRubyDefinitionName(node *sitter.Node) bool {
	name := node
	parent := node.Parent()
	for parent != nil && parent.Kind() == "scope_resolution" {
		name = parent
		parent = parent.Parent()
	}
	if parent == nil || (parent.Kind() != "class" && parent.Kind() != "module") {
		return false
	}
	return sameRubyNode(name, parent.ChildByFieldName("name"))
}

func isRubyScopeName(node *sitter.Node) bool {
	parent := node.Parent()
	if parent == nil || parent.Kind() != "scope_resolution" {
		return false
	}
	return sameRubyNode(node, parent.ChildByFieldName("name"))
}

func sameRubyNode(left, right *sitter.Node) bool {
	return left != nil && right != nil && left.StartByte() == right.StartByte() && left.EndByte() == right.EndByte()
}
