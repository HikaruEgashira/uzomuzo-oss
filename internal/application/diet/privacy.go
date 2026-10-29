package diet

import (
	"fmt"
	"path"
	"strings"

	"golang.org/x/mod/module"
)

func validatePrivatePatterns(value string) (string, error) {
	patterns := make([]string, 0)
	for _, pattern := range strings.Split(value, ",") {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return "", fmt.Errorf("invalid private dependency pattern")
		}
		patterns = append(patterns, pattern)
	}
	return strings.Join(patterns, ","), nil
}

func matchesPrivate(patterns, name string) bool {
	return module.MatchPrefixPatterns(patterns, strings.ToLower(name))
}
