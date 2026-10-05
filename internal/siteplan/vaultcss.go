package siteplan

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/simp-lee/oxpio/internal/asset"
	"github.com/simp-lee/oxpio/internal/diag"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/resourcepath"
	"github.com/simp-lee/oxpio/internal/vault"
)

const customCSSDestination = "assets/custom.css"

// planVaultCSS rewrites the optional vault stylesheet and every local CSS
// resource already discovered in content. Recursive imports and URL targets are
// admitted through the same emitted-byte asset plan.
func planVaultCSS(plan *model.SitePlan, scan vault.ScanResult, index *model.VaultIndex, collector *diag.Collector) {
	if plan == nil {
		return
	}
	plan.VaultCSSAssets = make(map[string]*model.PlannedAsset)
	plan.CSSInputFiles = make(map[string]struct{})

	rootSource := ""
	if strings.TrimSpace(plan.Config.CustomCSS) != "" {
		relative, err := filepath.Rel(plan.VaultPath, plan.Config.CustomCSS)
		if err != nil {
			recordCustomCSSError(collector, plan.Config.CustomCSS, "", "resolve custom CSS path: %v", err)
			return
		}
		rootSource = filepath.ToSlash(relative)
		if rootSource == "." || rootSource == ".." || strings.HasPrefix(rootSource, "../") {
			recordCustomCSSError(collector, plan.Config.CustomCSS, "", "custom CSS must be inside the vault")
			return
		}
		plan.CSSInputFiles[rootSource] = struct{}{}
	}

	resolved := make(map[string]*model.PlannedAsset)
	visiting := make(map[string]bool)
	var resolve func(string) (*model.PlannedAsset, error)
	resolve = func(source string) (*model.PlannedAsset, error) {
		plan.CSSInputFiles[source] = struct{}{}
		if planned := resolved[source]; planned != nil {
			return planned, nil
		}
		if visiting[source] {
			return nil, fmt.Errorf("cyclic CSS dependency at %q", source)
		}
		visiting[source] = true
		defer delete(visiting, source)

		_, data, _, err := internalfsutil.ReadContainedRegularFile(plan.VaultPath, source)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(path.Ext(source), ".css") {
			data, err = asset.RewriteCSSURLs(data, func(raw string) (string, error) {
				return rewriteVaultCSSURL(plan, scan, index, source, raw, resolve)
			})
			if err != nil {
				return nil, err
			}
		}

		if source == rootSource {
			planned := &model.PlannedAsset{Asset: model.Asset{SrcPath: source, DstPath: customCSSDestination}, Data: data}
			resolved[source] = planned
			return planned, nil
		}
		planned := asset.PlanData(source, data)
		resolved[source] = planned
		plan.VaultCSSAssets[source] = planned
		return planned, nil
	}

	if rootSource != "" {
		root, err := resolve(rootSource)
		if err != nil {
			recordCustomCSSError(collector, rootSource, "", "custom CSS: %v", err)
		} else {
			plan.CustomCSSData = root.Data
			if index != nil && index.Assets[rootSource] != nil {
				plan.VaultCSSAssets[rootSource] = asset.PlanData(rootSource, root.Data)
			}
		}
	}

	roots := make([]string, 0)
	if index != nil {
		for source := range index.Assets {
			if source != rootSource && strings.EqualFold(path.Ext(source), ".css") {
				roots = append(roots, source)
			}
		}
	}
	sort.Strings(roots)
	for _, source := range roots {
		if _, err := resolve(source); err != nil {
			recordCustomCSSError(collector, source, "", "CSS asset: %v", err)
		}
	}
}

func rewriteVaultCSSURL(plan *model.SitePlan, scan vault.ScanResult, index *model.VaultIndex, source, raw string, resolve func(string) (*model.PlannedAsset, error)) (string, error) {
	if !resourcepath.IsLocalTarget(raw) {
		return raw, nil
	}
	targetPath := raw
	if position := strings.IndexAny(targetPath, "?#"); position >= 0 {
		targetPath = targetPath[:position]
	}
	if targetPath == "" {
		return raw, nil
	}
	if decoded, err := url.PathUnescape(targetPath); err == nil {
		targetPath = decoded
	}
	targetPath = strings.ReplaceAll(targetPath, `\`, "/")
	candidate := path.Clean(path.Join(path.Dir(source), targetPath))
	if rootRelative, ok := strings.CutPrefix(targetPath, "/"); ok {
		candidate = path.Clean(rootRelative)
	}
	if candidate == "." || candidate == ".." || strings.HasPrefix(candidate, "../") {
		return "", fmt.Errorf("CSS asset %q escapes the vault", raw)
	}
	lookup := scan.LookupResourcePath(candidate)
	if len(lookup.Ambiguous) > 0 {
		for _, candidate := range lookup.Ambiguous {
			plan.CSSInputFiles[candidate] = struct{}{}
		}
		return "", fmt.Errorf("CSS asset %q matched multiple vault resources after canonical path normalization (%s)", raw, strings.Join(lookup.Ambiguous, ", "))
	}
	if lookup.Path == "" {
		return "", fmt.Errorf("CSS asset %q was not found", raw)
	}
	plan.CSSInputFiles[lookup.Path] = struct{}{}
	sourceVersion, targetVersion := "", ""
	if index != nil {
		sourceVersion = index.ResourceVersions[source]
		targetVersion = index.ResourceVersions[lookup.Path]
	}
	if targetVersion != "" && targetVersion != sourceVersion {
		return "", fmt.Errorf("CSS asset %q belongs to version %q and cannot be used from version %q", raw, targetVersion, sourceVersion)
	}
	dependency, err := resolve(lookup.Path)
	if err != nil {
		return "", err
	}
	suffix := ""
	if position := strings.IndexAny(raw, "?#"); position >= 0 {
		suffix = raw[position:]
	}
	return path.Base(dependency.DstPath) + suffix, nil
}

func recordCustomCSSError(collector *diag.Collector, source, target, format string, args ...any) {
	if collector == nil {
		return
	}
	collector.Add(diag.Diagnostic{
		Severity: diag.SeverityError,
		Kind:     diag.KindUnresolvedAsset,
		Location: diag.Location{Path: source},
		Field:    "css",
		Target:   target,
		Message:  fmt.Sprintf(format, args...),
	})
}
