//go:build cgo

package treesitter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// AnalyzePyPICommandUsage matches distribution entry points to project commands and tool configuration.
func (a *Analyzer) AnalyzePyPICommandUsage(
	ctx context.Context,
	sourceRoot string,
	scripts, importPaths map[string][]string,
) (map[string]struct{}, error) {
	owners := make(map[string][]string)
	for purl, names := range scripts {
		for _, name := range names {
			owners[name] = append(owners[name], purl)
		}
	}
	used := make(map[string]struct{})
	if len(owners) == 0 && len(importPaths) == 0 {
		return used, nil
	}
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
		if err := ctx.Err(); err != nil {
			return err
		}
		relPath, err := filepath.Rel(sourceRoot, path)
		if err != nil || !isCommandEvidenceFile(relPath) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > largeFileBytes {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Debug("failed to read Python command evidence", "path", path, "error", err)
			return nil
		}
		if filepath.Base(path) == "pyproject.toml" {
			for purl := range pyprojectCommandUsage(data, scripts, importPaths) {
				used[purl] = struct{}{}
			}
			return nil
		}
		for _, words := range commandWords(relPath, data) {
			if script := commandScript(words); script != "" {
				for _, purl := range owners[script] {
					used[purl] = struct{}{}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking source tree for Python command usage: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("analyzing Python command usage: %w", err)
	}
	return used, nil
}

func isCommandEvidenceFile(relPath string) bool {
	base := filepath.Base(relPath)
	if base == "Makefile" || base == "Procfile" || strings.HasPrefix(base, "Dockerfile") || filepath.Ext(base) == ".sh" || base == "pyproject.toml" {
		return true
	}
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	return len(parts) == 3 && parts[0] == ".github" && parts[1] == "workflows" &&
		(strings.HasSuffix(parts[2], ".yml") || strings.HasSuffix(parts[2], ".yaml"))
}

func commandWords(relPath string, data []byte) [][]string {
	if strings.HasPrefix(filepath.Base(relPath), "Dockerfile") {
		return dockerCommandWords(data)
	}
	var commands [][]string
	for _, line := range commandLines(relPath, data) {
		commands = append(commands, splitShellCommands(line)...)
	}
	return commands
}

func dockerCommandWords(data []byte) [][]string {
	var commands [][]string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		instruction := fields[0]
		if !strings.EqualFold(instruction, "RUN") && !strings.EqualFold(instruction, "CMD") && !strings.EqualFold(instruction, "ENTRYPOINT") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, instruction))
		if strings.HasPrefix(rest, "[") {
			var args []string
			if json.Unmarshal([]byte(rest), &args) == nil && len(args) > 0 {
				if dockerShellCommand(args) {
					commands = append(commands, splitShellCommands(args[2])...)
				} else {
					commands = append(commands, args)
				}
			}
			continue
		}
		commands = append(commands, splitShellCommands(rest)...)
	}
	return commands
}

func dockerShellCommand(args []string) bool {
	return len(args) >= 3 &&
		(filepath.Base(args[0]) == "sh" || filepath.Base(args[0]) == "bash") && args[1] == "-c"
}

func commandLines(relPath string, data []byte) []string {
	base := filepath.Base(relPath)
	var lines []string
	if base == "Procfile" {
		for _, line := range stripHeredocBodies(strings.Split(string(data), "\n")) {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if _, command, ok := strings.Cut(line, ":"); ok {
				lines = append(lines, command)
			}
		}
		return lines
	}
	if base == "Makefile" {
		for _, line := range stripHeredocBodies(strings.Split(string(data), "\n")) {
			if strings.HasPrefix(line, "\t") {
				lines = append(lines, line)
			}
		}
		return lines
	}
	if filepath.Ext(base) == ".sh" {
		return stripHeredocBodies(strings.Split(string(data), "\n"))
	}
	return workflowRunLines(data)
}

func workflowRunLines(data []byte) []string {
	var lines []string
	var workflow any
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		return nil
	}
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if key == "run" {
					if command, ok := child.(string); ok {
						lines = append(lines, stripHeredocBodies(strings.Split(command, "\n"))...)
						continue
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range value {
				visit(child)
			}
		}
	}
	visit(workflow)
	return lines
}

func stripHeredocBodies(lines []string) []string {
	var kept []string
	var delimiters []string
	for _, line := range lines {
		if len(delimiters) > 0 {
			if strings.TrimSpace(line) == delimiters[0] {
				delimiters = delimiters[1:]
			}
			continue
		}
		kept = append(kept, line)
		delimiters = heredocDelimiters(line)
	}
	return kept
}

func heredocDelimiters(line string) []string {
	var delimiters []string
	var quote byte
	escaped := false
	for i := 0; i < len(line); i++ {
		b := line[i]
		if escaped {
			escaped = false
			continue
		}
		if b == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if b == quote {
				quote = 0
			}
			continue
		}
		if b == '\'' || b == '"' {
			quote = b
			continue
		}
		if b == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			break
		}
		if b != '<' || i+1 >= len(line) || line[i+1] != '<' || i+2 < len(line) && line[i+2] == '<' {
			continue
		}
		i += 2
		if i < len(line) && line[i] == '-' {
			i++
		}
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			break
		}
		var delimiter strings.Builder
		if line[i] == '\'' || line[i] == '"' {
			quote = line[i]
			i++
			for i < len(line) && line[i] != quote {
				delimiter.WriteByte(line[i])
				i++
			}
			if i < len(line) {
				i++
			}
			quote = 0
		} else {
			for i < len(line) && !strings.ContainsRune(" \t\r;|&()<>\n", rune(line[i])) {
				delimiter.WriteByte(line[i])
				i++
			}
		}
		if delimiter.Len() > 0 {
			delimiters = append(delimiters, delimiter.String())
		}
		i--
	}
	return delimiters
}

func pyprojectCommandUsage(data []byte, scripts, importPaths map[string][]string) map[string]struct{} {
	used := make(map[string]struct{})
	var project map[string]any
	if err := toml.Unmarshal(data, &project); err != nil {
		return used
	}
	tool, _ := project["tool"].(map[string]any)
	for toolName := range tool {
		for purl, names := range scripts {
			for _, name := range names {
				if strings.EqualFold(toolName, name) {
					used[purl] = struct{}{}
				}
			}
		}
	}
	projectTable, _ := project["project"].(map[string]any)
	projectScripts, _ := projectTable["scripts"].(map[string]any)
	for _, rawTarget := range projectScripts {
		target, ok := rawTarget.(string)
		if !ok {
			continue
		}
		module, _, _ := strings.Cut(target, ":")
		for purl, names := range importPaths {
			for _, name := range names {
				if module == name || strings.HasPrefix(module, name+".") {
					used[purl] = struct{}{}
				}
			}
		}
	}
	return used
}

func splitShellCommands(line string) [][]string {
	var commands [][]string
	var words []string
	var word strings.Builder
	var quote rune
	escaped := false
	flushWord := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	flushCommand := func() {
		flushWord()
		if len(words) > 0 {
			commands = append(commands, words)
			words = nil
		}
	}
	for _, r := range line {
		if escaped {
			word.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == '#' && (word.Len() == 0 || len(words) > 0) {
			break
		}
		if r == ' ' || r == '\t' || r == '\r' {
			flushWord()
			continue
		}
		if strings.ContainsRune(";|&()", r) {
			flushCommand()
			continue
		}
		word.WriteRune(r)
	}
	flushCommand()
	return commands
}

func commandScript(words []string) string {
	for i := 0; i < len(words); i++ {
		word := strings.TrimPrefix(words[i], "@")
		if i == 0 {
			word = strings.TrimPrefix(word, "-")
		}
		if strings.HasPrefix(word, "-") {
			continue
		}
		if word == "" || isShellAssignment(word) {
			continue
		}
		base := filepath.Base(word)
		switch base {
		case "env", "sudo", "command", "exec", "time", "nice":
			continue
		case "uv", "poetry", "pipenv", "pdm", "hatch", "rye", "pipx":
			for j := i + 1; j < len(words); j++ {
				if words[j] == "run" || words[j] == "exec" {
					i = j
					if i+1 < len(words) && words[i+1] == "--" {
						i++
					}
					break
				}
			}
			continue
		case "uvx":
			if i+1 < len(words) {
				return strings.TrimSuffix(filepath.Base(words[i+1]), ".exe")
			}
			return ""
		}
		if strings.HasPrefix(base, "python") && i+2 < len(words) && words[i+1] == "-m" {
			return words[i+2]
		}
		return strings.TrimSuffix(base, ".exe")
	}
	return ""
}

func isShellAssignment(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
