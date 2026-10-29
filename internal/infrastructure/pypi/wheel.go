package pypi

// Wheel-based import name resolution for PyPI packages.
//
// DDD Layer: Infrastructure
// Responsibility: Download the smallest wheel for a PyPI package and extract
// the actual Python import module names from its metadata (top_level.txt,
// RECORD, or directory listing). This is a last-resort fallback used when
// heuristic name guessing produces zero coupling matches.

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/future-architect/uzomuzo-oss/internal/common/ttlcache"
	domain "github.com/future-architect/uzomuzo-oss/internal/domain/analysis"
)

// maxWheelSize is the maximum wheel file size we are willing to download (5 MB).
// Wheels larger than this are skipped to avoid excessive network and memory usage.
const maxWheelSize = 5 << 20

// maxEntrySize caps the decompressed size of a single ZIP entry (1 MB).
// This prevents zip-bomb attacks where a tiny compressed entry expands to
// gigabytes of data.
const maxEntrySize = 1 << 20

// maxJSONResponseSize caps the PyPI JSON API response body (10 MB).
const maxJSONResponseSize = 10 << 20

// getImportCached returns cached import names if the TTL has not expired.
// A (nil, true) result is a valid negative-cache hit (package has no import names).
func (c *Client) getImportCached(name string) ([]string, bool) {
	return c.importCache.Get(name)
}

// setImportCache stores resolved import names in the cache.
// Stores nil slices to represent negative cache results.
func (c *Client) setImportCache(name string, names []string) {
	// Cache the names unconditionally (including nil, for negative caching).
	// ttlcache.Set is a no-op when TTL <= 0, matching old behavior.
	c.importCache.Set(name, names)
}

// wheelURL describes a single distribution file from the PyPI JSON API urls array.
type wheelURL struct {
	Filename    string `json:"filename"`
	PackageType string `json:"packagetype"`
	Size        int64  `json:"size"`
	URL         string `json:"url"`
}

// ResolveImportNames fetches the actual Python import module names for a PyPI
// package by downloading and inspecting the smallest wheel file.
//
// It returns nil, nil when the package name is empty or when no suitable wheel
// can be used to resolve import names. It may return a non-nil error for wheel
// lookup or download failures; callers should treat those as non-fatal fallback
// failures and fall back to heuristic names.
func (c *Client) ResolveImportNames(ctx context.Context, packageName string) ([]string, error) {
	imports, _, err := c.ResolveDistributionMetadata(ctx, packageName)
	return imports, err
}

// ResolveDistributionMetadata fetches Python import names and console scripts from a wheel.
func (c *Client) ResolveDistributionMetadata(ctx context.Context, packageName string) ([]string, []string, error) {
	n := strings.TrimSpace(packageName)
	if n == "" {
		return nil, nil, nil
	}
	lower := strings.ToLower(n)

	// Check cache first.
	if names, ok := c.getImportCached(lower); ok {
		slog.Debug("pypi_wheel: import name cache hit", "package", lower)
		scripts, _ := c.consoleScriptCache.Get(lower)
		return names, scripts, nil
	}

	// Fetch the wheel URL.
	wURL, err := c.selectSmallestWheel(ctx, n)
	if err != nil {
		return nil, nil, fmt.Errorf("pypi wheel URL lookup failed for %s: %w", n, err)
	}
	if wURL == "" {
		slog.Debug("pypi_wheel: no suitable wheel found", "package", n)
		c.setImportCache(lower, nil) // cache negative result
		c.consoleScriptCache.Set(lower, nil)
		return nil, nil, nil
	}

	// Download the wheel.
	wheelData, err := c.downloadWheel(ctx, wURL)
	if err != nil {
		return nil, nil, fmt.Errorf("pypi wheel download failed for %s: %w", n, err)
	}

	imports := extractImportNamesFromWheel(wheelData)
	scripts := extractConsoleScriptsFromWheel(wheelData)
	slog.Debug("pypi_wheel: resolved distribution metadata", "package", n, "imports", imports, "console_scripts", scripts)

	c.setImportCache(lower, imports)
	c.consoleScriptCache.Set(lower, scripts)
	return imports, scripts, nil
}

// ResolveLocalImportNames reads import names from a virtualenv under sourceRoot.
func (c *Client) ResolveLocalImportNames(ctx context.Context, sourceRoot, packageName string) ([]string, error) {
	imports, _, err := c.ResolveLocalDistributionMetadata(ctx, sourceRoot, packageName)
	return imports, err
}

// ResolveLocalDistributionMetadata reads import names and console scripts from a local virtualenv.
func (c *Client) ResolveLocalDistributionMetadata(ctx context.Context, sourceRoot, packageName string) ([]string, []string, error) {
	wanted := domain.NormalizePyPIName(strings.TrimSpace(packageName))
	if wanted == "" {
		return nil, nil, nil
	}
	var names, scripts []string
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() && isInsideVirtualEnvSitePackages(sourceRoot, path) && !strings.HasSuffix(entry.Name(), ".dist-info") {
			return filepath.SkipDir
		}
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".dist-info") {
			if entry.IsDir() && isLocalScanSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isVirtualEnvDistInfo(sourceRoot, path) {
			return filepath.SkipDir
		}
		imports, entryPoints, err := readDistInfoMetadata(path, wanted)
		if err != nil {
			slog.Debug("pypi_local: failed to read distribution metadata", "path", path, "error", err)
		} else {
			names = append(names, imports...)
			scripts = append(scripts, entryPoints...)
		}
		return filepath.SkipDir
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walking source tree for Python distribution metadata: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading local Python distribution metadata: %w", err)
	}
	sort.Strings(names)
	sort.Strings(scripts)
	return slices.Compact(names), slices.Compact(scripts), nil
}

func isLocalScanSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "__pycache__":
		return true
	default:
		return false
	}
}

func isVirtualEnvDistInfo(sourceRoot, distInfoPath string) bool {
	return strings.HasSuffix(filepath.Base(distInfoPath), ".dist-info") && isInsideVirtualEnvSitePackages(sourceRoot, distInfoPath)
}

func isInsideVirtualEnvSitePackages(sourceRoot, candidatePath string) bool {
	relative, err := filepath.Rel(sourceRoot, candidatePath)
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	virtualEnv := filepath.Base(sourceRoot) == ".venv" || filepath.Base(sourceRoot) == "venv" || filepath.Base(sourceRoot) == "env" || filepath.Base(sourceRoot) == ".tox"
	for i, part := range parts {
		switch part {
		case ".venv", "venv", "env", ".tox":
			virtualEnv = true
		case "site-packages":
			return virtualEnv && i < len(parts)-1
		}
	}
	return false
}

func readDistInfoMetadata(distInfoPath, wanted string) ([]string, []string, error) {
	metadata, err := readLocalMetadataFile(filepath.Join(distInfoPath, "METADATA"))
	if err != nil || !metadataMatchesPackage(metadata, wanted) {
		return nil, nil, err
	}
	var imports []string
	if topLevel, err := readLocalMetadataFile(filepath.Join(distInfoPath, "top_level.txt")); err == nil {
		imports = parseTopLevelTxtData(topLevel)
	}
	if len(imports) == 0 {
		if record, err := readLocalMetadataFile(filepath.Join(distInfoPath, "RECORD")); err == nil {
			imports = parseRECORDData(record)
		}
	}
	var scripts []string
	if entryPoints, err := readLocalMetadataFile(filepath.Join(distInfoPath, "entry_points.txt")); err == nil {
		scripts = parseConsoleScriptsData(entryPoints)
	}
	return imports, scripts, nil
}

func readLocalMetadataFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxEntrySize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxEntrySize {
		return nil, fmt.Errorf("metadata file exceeds %d bytes", maxEntrySize)
	}
	return data, nil
}

func metadataMatchesPackage(data []byte, wanted string) bool {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(key, "Name") {
			return domain.NormalizePyPIName(strings.TrimSpace(value)) == wanted
		}
	}
	return false
}

// selectSmallestWheel fetches the PyPI JSON API and returns the URL of the
// smallest bdist_wheel distribution that fits within maxWheelSize.
func (c *Client) selectSmallestWheel(ctx context.Context, packageName string) (string, error) {
	apiURL := fmt.Sprintf("%s/pypi/%s/json", c.resolvedBaseURL(), url.PathEscape(packageName))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", pypiUserAgent)

	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return "", fmt.Errorf("http failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		slog.Debug("pypi_wheel: package not found on PyPI", "package", packageName)
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http status %d", resp.StatusCode)
	}

	var raw struct {
		URLs []wheelURL `json:"urls"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONResponseSize)).Decode(&raw); err != nil {
		return "", fmt.Errorf("decode failed: %w", err)
	}

	// Filter to wheels only, respecting size limit.
	var candidates []wheelURL
	for _, u := range raw.URLs {
		if u.PackageType != "bdist_wheel" {
			continue
		}
		if u.Size <= 0 || u.Size > maxWheelSize {
			continue
		}
		if u.URL == "" {
			continue
		}
		candidates = append(candidates, u)
	}
	if len(candidates) == 0 {
		return "", nil
	}

	// Pick the smallest.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Size < candidates[j].Size
	})
	return candidates[0].URL, nil
}

// downloadWheel fetches the wheel file content, capped at maxWheelSize.
func (c *Client) downloadWheel(ctx context.Context, dlURL string) ([]byte, error) {
	parsed, err := url.Parse(dlURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid wheel URL: %s", dlURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", pypiUserAgent)

	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("http failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	// Read with a safety cap to prevent memory exhaustion.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxWheelSize+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(data)) > maxWheelSize {
		return nil, fmt.Errorf("wheel exceeds %d bytes", maxWheelSize)
	}
	return data, nil
}

// extractImportNamesFromWheel parses a wheel ZIP and extracts Python import
// module names. Resolution priority:
//  1. top_level.txt in .dist-info
//  2. RECORD in .dist-info
//  3. Directory listing (__init__.py at top level)
func extractImportNamesFromWheel(data []byte) []string {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		slog.Debug("pypi_wheel: invalid zip", "error", err)
		return nil
	}

	// 1. Try top_level.txt
	if names := parseTopLevelTxt(r); len(names) > 0 {
		return names
	}

	// 2. Try RECORD
	if names := parseRECORD(r); len(names) > 0 {
		return names
	}

	// 3. Fallback: directories containing __init__.py at depth 1
	return parseInitPyDirs(r)
}

// parseTopLevelTxt looks for a top_level.txt file inside any .dist-info
// directory and extracts one module name per line.
func extractConsoleScriptsFromWheel(data []byte) []string {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	return parseConsoleScripts(r)
}

func parseConsoleScripts(r *zip.Reader) []string {
	for _, f := range r.File {
		dir, base := path.Split(f.Name)
		if base != "entry_points.txt" || !strings.HasSuffix(dir, ".dist-info/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxEntrySize+1))
		_ = rc.Close()
		if err != nil || int64(len(data)) > maxEntrySize {
			continue
		}
		return parseConsoleScriptsData(data)
	}
	return nil
}

func parseConsoleScriptsData(data []byte) []string {
	var scripts []string
	seen := make(map[string]struct{})
	consoleScripts := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			consoleScripts = strings.EqualFold(strings.TrimSpace(line[1:len(line)-1]), "console_scripts")
			continue
		}
		if !consoleScripts {
			continue
		}
		name, _, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || !isConsoleScriptName(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		scripts = append(scripts, name)
	}
	sort.Strings(scripts)
	return scripts
}

func isConsoleScriptName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("_.-", r) {
			continue
		}
		return false
	}
	return true
}

func parseTopLevelTxt(r *zip.Reader) []string {
	for _, f := range r.File {
		dir, base := path.Split(f.Name)
		if base != "top_level.txt" {
			continue
		}
		if !strings.HasSuffix(dir, ".dist-info/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxEntrySize+1))
		_ = rc.Close()
		if err != nil {
			continue
		}
		if int64(len(data)) > maxEntrySize {
			continue
		}
		return parseTopLevelTxtData(data)
	}
	return nil
}

func parseTopLevelTxtData(data []byte) []string {
	var names []string
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(data), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || !isPyIdentifierSafe(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

// parseRECORD extracts top-level package directories from a RECORD file
// inside .dist-info. Each line is "path,hash,size". We look for entries
// whose first path component contains __init__.py at depth 1 only.
func parseRECORD(r *zip.Reader) []string {
	for _, f := range r.File {
		dir, base := path.Split(f.Name)
		if base != "RECORD" {
			continue
		}
		if !strings.HasSuffix(dir, ".dist-info/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxEntrySize+1))
		_ = rc.Close()
		if err != nil {
			continue
		}
		if int64(len(data)) > maxEntrySize {
			continue
		}

		return parseRECORDData(data)
	}
	return nil
}

func parseRECORDData(data []byte) []string {
	pkgDirs := make(map[string]struct{})
	csvReader := csv.NewReader(bytes.NewReader(data))
	csvReader.FieldsPerRecord = -1 // variable field count
	for {
		record, csvErr := csvReader.Read()
		if csvErr != nil {
			break
		}
		if len(record) == 0 {
			continue
		}
		recPath := strings.TrimSpace(record[0])
		if recPath == "" {
			continue
		}

		parts := strings.SplitN(recPath, "/", 2)
		if len(parts) < 2 {
			if strings.HasSuffix(recPath, ".py") {
				stem := strings.TrimSuffix(recPath, ".py")
				if isPyIdentifierSafe(stem) && !strings.HasPrefix(stem, "_") {
					pkgDirs[stem] = struct{}{}
				}
			}
			continue
		}
		topDir := parts[0]
		rest := parts[1]
		if strings.HasSuffix(topDir, ".dist-info") || strings.HasSuffix(topDir, ".data") {
			continue
		}
		if rest == "__init__.py" && isPyIdentifierSafe(topDir) {
			pkgDirs[topDir] = struct{}{}
		}
	}

	if len(pkgDirs) == 0 {
		return nil
	}
	names := make([]string, 0, len(pkgDirs))
	for dir := range pkgDirs {
		names = append(names, dir)
	}
	sort.Strings(names)
	return names
}

// parseInitPyDirs scans the ZIP for directories at depth 1 that contain
// __init__.py (the classic Python package indicator).
func parseInitPyDirs(r *zip.Reader) []string {
	dirs := make(map[string]struct{})
	for _, f := range r.File {
		parts := strings.SplitN(f.Name, "/", 3)
		if len(parts) < 2 {
			continue
		}
		topDir := parts[0]
		rest := parts[1]

		// Skip metadata directories
		if strings.HasSuffix(topDir, ".dist-info") || strings.HasSuffix(topDir, ".data") {
			continue
		}
		if strings.HasPrefix(topDir, "_") {
			continue
		}
		if rest == "__init__.py" {
			if isPyIdentifierSafe(topDir) {
				dirs[topDir] = struct{}{}
			}
		}
	}
	if len(dirs) == 0 {
		return nil
	}
	names := make([]string, 0, len(dirs))
	for d := range dirs {
		names = append(names, d)
	}
	sort.Strings(names)
	return names
}

// isPyIdentifierSafe reports whether s is a valid Python identifier segment.
// Duplicated from application/diet/service.go to avoid cross-layer import.
func isPyIdentifierSafe(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// importCacheFields groups the fields added to Client for wheel import name
// caching.  The zero value is usable; caching is disabled until SetTTL is
// called on the embedded cache.
type importCacheFields struct {
	importCache        ttlcache.Cache[[]string]
	consoleScriptCache ttlcache.Cache[[]string]
}
