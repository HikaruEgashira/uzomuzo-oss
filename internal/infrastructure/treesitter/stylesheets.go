//go:build cgo

package treesitter

import (
	"regexp"
	"strings"
)

// Consume comments and standalone strings before looking for directives.
var stylesheetTokens = regexp.MustCompile(`(?s)/\*.*?(?:\*/|\z)|//[^\n]*|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|@(import|use|forward|plugin|source|config)\s+(?:\([^)]*\)\s*)?(?:url\(\s*)?(?:"([^"\n]*)"|'([^'\n]*)')`)

func isStylesheet(ext string) bool {
	switch ext {
	case ".css", ".pcss", ".postcss", ".scss", ".sass", ".less":
		return true
	default:
		return false
	}
}

func analyzeStylesheet(src []byte, file string, imports map[string][]string, accum map[string]*accumulator) {
	for _, match := range stylesheetTokens.FindAllSubmatch(src, -1) {
		if len(match[1]) == 0 {
			continue
		}
		spec := string(match[2])
		if spec == "" {
			spec = string(match[3])
		}
		spec = strings.TrimPrefix(spec, "~")
		if strings.HasPrefix(spec, "//") || strings.ContainsAny(spec, ":\\#{}$") {
			continue
		}
		if string(match[1]) == "source" {
			if i := strings.LastIndex(spec, "node_modules/"); i >= 0 && (i == 0 || spec[i-1] == '/') {
				spec = spec[i+len("node_modules/"):]
			} else {
				continue
			}
		}
		if spec == "" || strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") || strings.ContainsAny(spec, ":\\#{}$") {
			continue
		}
		lower := strings.ToLower(spec)
		best := ""
		for candidate := range imports {
			if (lower == candidate || strings.HasPrefix(lower, candidate+"/")) && len(candidate) > len(best) {
				best = candidate
			}
		}
		for _, purl := range imports[best] {
			if !strings.HasPrefix(purl, "pkg:npm/") {
				continue
			}
			acc := accum[purl]
			if acc == nil {
				acc = &accumulator{importFiles: map[string]bool{}, symbols: map[string]bool{}}
				accum[purl] = acc
			}
			acc.importFiles[file] = true
			acc.hasBlankImport = true
		}
	}
}
