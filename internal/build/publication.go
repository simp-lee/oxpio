package build

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	internalpublish "github.com/simp-lee/oxpio/internal/publish"
)

const (
	managedOutputMarkerFilename = internalpublish.ManagedMarkerFilename
	managedOutputMarkerContents = internalpublish.ManagedMarkerContents
	customCSSOutputPath         = "assets/custom.css"
)

type strictOutputRegistry struct {
	claims       map[string]string
	previousRoot string
	previous     map[string]strictCacheOutput
	records      []strictCacheOutput
	dependencies []strictCacheDependency
}

func newStrictOutputRegistry(previousRoot string, manifest *strictCacheManifest) *strictOutputRegistry {
	previous := make(map[string]strictCacheOutput)
	if manifest != nil {
		for _, entry := range manifest.Outputs {
			if entry.Route != "" {
				previous[entry.Route] = entry
			}
		}
	}
	return &strictOutputRegistry{claims: make(map[string]string), previousRoot: previousRoot, previous: previous}
}

func (registry *strictOutputRegistry) dependency(owner, source string, value any) error {
	if registry == nil {
		return fmt.Errorf("output registry is required")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal cache dependency %q: %w", source, err)
	}
	hash := sha256.Sum256(data)
	registry.dependencies = append(registry.dependencies, strictCacheDependency{
		Owner: owner, Source: source, InputSignature: fmt.Sprintf("%x", hash),
	})
	return nil
}

func (registry *strictOutputRegistry) dependencyBytes(owner, source string, data []byte) error {
	if registry == nil {
		return fmt.Errorf("output registry is required")
	}
	hash := sha256.Sum256(data)
	registry.dependencies = append(registry.dependencies, strictCacheDependency{
		Owner: owner, Source: source, InputSignature: fmt.Sprintf("%x", hash),
	})
	return nil
}

func (registry *strictOutputRegistry) write(outputRoot, relPath, owner string, content []byte) error {
	if registry == nil {
		return fmt.Errorf("output registry is required")
	}
	cleaned, _, err := resolveOutputWritePath(outputRoot, relPath)
	if err != nil {
		return err
	}
	if existing, ok := registry.claims[cleaned]; ok {
		return fmt.Errorf("output path %q claimed by %q and written again by %q", cleaned, existing, owner)
	}
	registry.claims[cleaned] = owner
	writeContent := content
	hash := sha256.Sum256(content)
	if previous, ok := registry.previous[cleaned]; ok && previous.OutputHash == fmt.Sprintf("%x", hash) && registry.previousRoot != "" {
		_, previousPath, pathErr := resolveOutputWritePath(registry.previousRoot, cleaned)
		if pathErr != nil {
			return pathErr
		}
		previousInfo, statErr := os.Lstat(previousPath)
		if statErr == nil && previousInfo.Mode().IsRegular() {
			if previousContent, readErr := os.ReadFile(previousPath); readErr == nil {
				previousHash := sha256.Sum256(previousContent)
				if fmt.Sprintf("%x", previousHash) == previous.OutputHash {
					if linkErr := linkCachedOutput(previousPath, outputRoot, cleaned); linkErr == nil {
						registry.records = append(registry.records, strictCacheOutput{Owner: owner, Route: cleaned, OutputHash: fmt.Sprintf("%x", hash)})
						return nil
					}
					writeContent = previousContent
				}
			}
		}
	}
	if err := writeOutputFile(outputRoot, cleaned, writeContent); err != nil {
		return err
	}
	registry.records = append(registry.records, strictCacheOutput{Owner: owner, Route: cleaned, OutputHash: fmt.Sprintf("%x", hash)})
	return nil
}

func linkCachedOutput(previousPath, outputRoot, relPath string) error {
	if strings.TrimSpace(previousPath) == "" || strings.TrimSpace(outputRoot) == "" {
		return os.ErrNotExist
	}
	info, err := os.Lstat(previousPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("cached output source %q must be a regular file", previousPath)
	}
	cleanRelPath, destination, err := resolveOutputWritePath(outputRoot, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.Link(previousPath, destination); err != nil {
		return fmt.Errorf("link cached output %q: %w", cleanRelPath, err)
	}
	return nil
}

func (registry *strictOutputRegistry) claim(relPath, owner string) error {
	if registry == nil {
		return fmt.Errorf("output registry is required")
	}
	cleaned := strings.Trim(strings.ReplaceAll(relPath, `\`, "/"), "/")
	if cleaned == "" || cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("invalid output path %q", relPath)
	}
	if existing, ok := registry.claims[cleaned]; ok && existing != owner {
		if !strings.HasPrefix(existing, "asset:") || !strings.HasPrefix(owner, "asset:") {
			return fmt.Errorf("output path %q claimed by %q and %q", cleaned, existing, owner)
		}
		return nil
	}
	registry.claims[cleaned] = owner
	return nil
}

func writeOutputFile(outputRoot, relPath string, content []byte) error {
	cleanRelPath, absPath, err := resolveOutputWritePath(outputRoot, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("create output directory for %q: %w", cleanRelPath, err)
	}
	if err := os.WriteFile(absPath, content, 0o644); err != nil {
		return fmt.Errorf("write output file %q: %w", cleanRelPath, err)
	}
	return nil
}

func resolveOutputWritePath(outputRoot, relPath string) (string, string, error) {
	return internalfsutil.ResolveOutputURLPath(outputRoot, relPath)
}
