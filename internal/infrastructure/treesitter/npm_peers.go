//go:build cgo

package treesitter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/package-url/packageurl-go"
	"gopkg.in/yaml.v3"
)

const (
	maxNpmPackageJSONBytes  = 1 << 20
	maxNpmPeerLockfileBytes = 64 << 20
)

type npmPeerMetadata struct {
	Version              string                           `json:"version" yaml:"version"`
	PeerDependencies     map[string]string                `json:"peerDependencies" yaml:"peerDependencies"`
	PeerDependenciesMeta map[string]npmPeerDependencyMeta `json:"peerDependenciesMeta" yaml:"peerDependenciesMeta"`
}

type npmPeerDependencyMeta struct {
	Optional bool `json:"optional" yaml:"optional"`
}

type npmPeerMetadataResolver struct {
	sourceRoot string

	packageLockLoaded   bool
	packageLockValid    bool
	packageLockPackages map[string]npmPeerMetadata

	pnpmLockLoaded bool
	pnpmLockValid  bool
	pnpmPackages   map[string]npmPeerMetadata
	pnpmSnapshots  map[string]npmPeerMetadata
}

// NpmPeerDependencies resolves required peer names for direct npm packages.
// Source metadata wins over lockfiles, which win over the small known-host table.
func (*Analyzer) NpmPeerDependencies(ctx context.Context, sourceRoot string, directDeps []string) (map[string][]string, error) {
	peersByHost := make(map[string][]string)
	metadata := npmPeerMetadataResolver{sourceRoot: sourceRoot}
	for _, purl := range directDeps {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("reading npm peer dependencies: %w", err)
		}
		parsed, err := packageurl.FromString(purl)
		if err != nil || parsed.Type != "npm" {
			continue
		}
		name := npmPURLName(parsed)
		if !validNpmPackageName(name) {
			continue
		}

		peers, found := nodeModulesPeerDependencies(sourceRoot, name, parsed.Version)
		if !found {
			peers, found = metadata.packageLockPeerDependencies(name, parsed.Version)
		}
		if !found {
			peers, found = metadata.pnpmLockPeerDependencies(name, parsed.Version)
		}
		if !found {
			peers, found = staticNpmPeerDependencies(name)
		}
		if found && len(peers) > 0 {
			peersByHost[purl] = peers
		}
	}
	return peersByHost, nil
}

func npmPURLName(parsed packageurl.PackageURL) string {
	if parsed.Namespace == "" {
		return strings.ToLower(parsed.Name)
	}
	return strings.ToLower(parsed.Namespace + "/" + parsed.Name)
}

func validNpmPackageName(name string) bool {
	if name == "" || strings.ContainsAny(name, `\:`) || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func nodeModulesPeerDependencies(sourceRoot, name, version string) ([]string, bool) {
	if data, ok := readNpmSourceFile(sourceRoot, filepath.Join("node_modules", filepath.FromSlash(name), "package.json"), maxNpmPackageJSONBytes); ok {
		var manifest struct {
			Name string `json:"name"`
			npmPeerMetadata
		}
		if json.Unmarshal(data, &manifest) == nil && strings.EqualFold(manifest.Name, name) && npmVersionsMatch(manifest.Version, version) {
			return requiredNpmPeers(manifest.npmPeerMetadata), true
		}
	}
	return nil, false
}

func (r *npmPeerMetadataResolver) packageLockPeerDependencies(name, version string) ([]string, bool) {
	if !r.packageLockLoaded {
		r.packageLockLoaded = true
		if data, ok := readNpmSourceFile(r.sourceRoot, "package-lock.json", maxNpmPeerLockfileBytes); ok {
			var lock struct {
				LockfileVersion int                        `json:"lockfileVersion"`
				Packages        map[string]npmPeerMetadata `json:"packages"`
			}
			if json.Unmarshal(data, &lock) == nil && (lock.LockfileVersion == 2 || lock.LockfileVersion == 3) {
				r.packageLockPackages = lock.Packages
				r.packageLockValid = true
			}
		}
	}
	if !r.packageLockValid {
		return nil, false
	}
	entry, found := r.packageLockPackages[filepath.ToSlash(filepath.Join("node_modules", filepath.FromSlash(name)))]
	if !found || !npmVersionsMatch(entry.Version, version) {
		return nil, false
	}
	return requiredNpmPeers(entry), true
}

func (r *npmPeerMetadataResolver) pnpmLockPeerDependencies(name, version string) ([]string, bool) {
	if !r.pnpmLockLoaded {
		r.pnpmLockLoaded = true
		if data, ok := readNpmSourceFile(r.sourceRoot, "pnpm-lock.yaml", maxNpmPeerLockfileBytes); ok {
			var lock struct {
				Packages  map[string]npmPeerMetadata `yaml:"packages"`
				Snapshots map[string]npmPeerMetadata `yaml:"snapshots"`
			}
			if yaml.Unmarshal(data, &lock) == nil {
				r.pnpmPackages = indexPnpmPeerMetadata(lock.Packages)
				r.pnpmSnapshots = indexPnpmPeerMetadata(lock.Snapshots)
				r.pnpmLockValid = true
			}
		}
	}
	if !r.pnpmLockValid {
		return nil, false
	}
	if entry, found := pnpmPackageMetadata(r.pnpmPackages, name, version); found {
		return requiredNpmPeers(entry), true
	}
	if entry, found := pnpmPackageMetadata(r.pnpmSnapshots, name, version); found {
		return requiredNpmPeers(entry), true
	}
	return nil, false
}

func indexPnpmPeerMetadata(packages map[string]npmPeerMetadata) map[string]npmPeerMetadata {
	index := make(map[string]npmPeerMetadata, len(packages))
	keys := make([]string, 0, len(packages))
	for key := range packages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		packageName, packageVersion, ok := pnpmPackageCoordinates(key)
		if !ok {
			continue
		}
		packageVersion, _, _ = strings.Cut(packageVersion, "_")
		coordinate := npmPeerCoordinate(packageName, packageVersion)
		if _, exists := index[coordinate]; !exists {
			index[coordinate] = packages[key]
		}
	}
	return index
}

func pnpmPackageMetadata(index map[string]npmPeerMetadata, name, version string) (npmPeerMetadata, bool) {
	metadata, found := index[npmPeerCoordinate(name, version)]
	return metadata, found
}

func npmPeerCoordinate(name, version string) string {
	return strings.ToLower(name) + "@" + strings.TrimPrefix(version, "v")
}

func pnpmPackageCoordinates(key string) (name, version string, ok bool) {
	key = strings.TrimPrefix(key, "/")
	if suffix, _, found := strings.Cut(key, "("); found {
		key = suffix
	}
	if slash := strings.LastIndexByte(key, '/'); slash > 0 {
		versionCandidate := key[slash+1:]
		versionWithoutPeerContext, _, _ := strings.Cut(versionCandidate, "_")
		if strings.HasPrefix(versionWithoutPeerContext, "v") || (len(versionWithoutPeerContext) > 0 && versionWithoutPeerContext[0] >= '0' && versionWithoutPeerContext[0] <= '9') {
			return key[:slash], versionWithoutPeerContext, true
		}
	}
	if at := strings.LastIndexByte(key, '@'); at > 0 {
		if underscore := strings.LastIndexByte(key[:at], '_'); underscore > 0 {
			previousAt := strings.LastIndexByte(key[:underscore], '@')
			if previousAt > 0 {
				key = key[:underscore]
			}
		}
	}
	if at := strings.LastIndexByte(key, '@'); at > 0 {
		return key[:at], key[at+1:], true
	}
	if slash := strings.LastIndexByte(key, '/'); slash > 0 {
		return key[:slash], key[slash+1:], true
	}
	return "", "", false
}

func npmVersionsMatch(metadataVersion, purlVersion string) bool {
	if purlVersion == "" {
		return true
	}
	return metadataVersion != "" && strings.TrimPrefix(metadataVersion, "v") == strings.TrimPrefix(purlVersion, "v")
}

func requiredNpmPeers(metadata npmPeerMetadata) []string {
	peers := make([]string, 0, len(metadata.PeerDependencies))
	for name := range metadata.PeerDependencies {
		if npmPeerOptional(metadata.PeerDependenciesMeta, name) {
			continue
		}
		peers = append(peers, strings.ToLower(name))
	}
	sort.Strings(peers)
	return peers
}

func npmPeerOptional(optionalByName map[string]npmPeerDependencyMeta, name string) bool {
	for optionalName, metadata := range optionalByName {
		if strings.EqualFold(optionalName, name) {
			return metadata.Optional
		}
	}
	return false
}

func readNpmSourceFile(sourceRoot, relativePath string, maxBytes int64) ([]byte, bool) {
	root, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return nil, false
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, relativePath))
	if err != nil {
		return nil, false
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

func staticNpmPeerDependencies(name string) ([]string, bool) {
	peers, found := staticNpmPeerDeps[strings.ToLower(name)]
	if !found {
		return nil, false
	}
	return append([]string(nil), peers...), true
}

// staticNpmPeerDeps is used only when local package metadata and lockfiles lack a host entry.
var staticNpmPeerDeps = map[string][]string{
	"next":                   {"react", "react-dom"},
	"@mui/material":          {"@emotion/react", "@emotion/styled"},
	"react-redux":            {"react", "redux"},
	"@testing-library/react": {"react", "react-dom"},
}
