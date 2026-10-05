package siteplan

import (
	"fmt"
	"io"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/simp-lee/oxpio/internal/asset"
	"github.com/simp-lee/oxpio/internal/diag"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/markdown"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/render"
	"github.com/simp-lee/oxpio/internal/resourcepath"
	"github.com/simp-lee/oxpio/internal/vault"
	xhtml "golang.org/x/net/html"
)

func validateThemeSlotAssets(plan *model.SitePlan, index *model.VaultIndex, plannedOutputs map[string]struct{}, collector *diag.Collector) {
	if strings.TrimSpace(plan.Config.ThemeSlots) == "" {
		return
	}
	publicOutputs, err := themeSlotPublicOutputPaths(plan.Config.BaseURL, plannedOutputs)
	if err != nil {
		record(collector, diag.KindMetadata, filepath.Join(plan.Config.ThemeDir, "slots.html"), "theme slot resource plan: %v", err)
		return
	}
	check := func(route, title string, note *model.Note, source string) {
		slots, err := render.RenderStrictThemeSlots(plan, route, title, note, source)
		if err == nil {
			err = validateExpandedThemeSlotResources(plan.Config.BaseURL, route, slots, publicOutputs)
		}
		if err != nil {
			record(collector, diag.KindMetadata, filepath.Join(plan.Config.ThemeDir, "slots.html"), "theme slots for %q: %v", route, err)
		}
	}
	for _, section := range plan.Sections {
		if section != nil && section.Route != "" {
			check(section.Route, section.Title, nil, section.SourcePath)
		}
	}
	for _, article := range plan.Articles {
		if article != nil && article.Route != "" {
			check(article.Route, article.Frontmatter.Title, article, article.RelPath)
		}
	}
	if index != nil {
		for _, tag := range sortedStrictTags(index.Tags) {
			check("/"+encodePath(tag.Slug)+"/", "Tag: "+tag.Name, nil, "")
		}
	}
	if plan.Timeline != nil {
		for _, page := range plan.Timeline.Pages {
			check(page.Route, "Recent articles", nil, "")
		}
	}
	check("/404.html", "Not found", nil, "")
}

func themeSlotPublicOutputPaths(baseURL string, outputs map[string]struct{}) (map[string]struct{}, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	public := make(map[string]struct{}, len(outputs)+1)
	for output := range outputs {
		reference, err := url.Parse(strings.TrimPrefix(output, "/"))
		if err != nil {
			return nil, fmt.Errorf("planned output %q: %w", output, err)
		}
		public[base.ResolveReference(reference).Path] = struct{}{}
	}
	// style.css is emitted by the strict builder rather than the asset planner.
	public[base.ResolveReference(&url.URL{Path: "style.css"}).Path] = struct{}{}
	return public, nil
}

func validateExpandedThemeSlotResources(baseURL, route string, slots map[string]string, plannedOutputs map[string]struct{}) error {
	base, err := url.Parse(baseURL)
	if err != nil {
		return err
	}
	routeReference, err := url.Parse(strings.TrimPrefix(route, "/"))
	if err != nil {
		return err
	}
	page := base.ResolveReference(routeReference)
	names := make([]string, 0, len(slots))
	for name := range slots {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		validateResource := func(raw string) error {
			if !resourcepath.IsLocalTarget(raw) {
				return nil
			}
			reference, err := url.Parse(strings.TrimSpace(raw))
			if err == nil {
				resolved := page.ResolveReference(reference)
				if resolved.Scheme == page.Scheme && resolved.Host == page.Host {
					if _, ok := plannedOutputs[resolved.Path]; ok {
						return nil
					}
				}
			}
			return fmt.Errorf("local resource %q does not point to a planned output; use themeAssetURL for theme resources", raw)
		}
		_, err := markdown.RewriteRawHTMLResources([]byte(slots[name]), func(raw string) (string, error) {
			return raw, validateResource(raw)
		})
		if err == nil {
			err = validateThemeSlotAttachmentLinks(slots[name], validateResource)
		}
		if err != nil {
			return fmt.Errorf("theme slot %q: %w", name, err)
		}
	}
	return nil
}

func validateThemeSlotAttachmentLinks(output string, validate func(string) error) error {
	z := xhtml.NewTokenizer(strings.NewReader(output))
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			if err := z.Err(); err != nil && err != io.EOF {
				return err
			}
			return nil
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			token := z.Token()
			if token.Data == "noscript" {
				z.NextIsNotRawText()
			}
			if token.Data != "a" && token.Data != "area" {
				continue
			}
			href, download := "", false
			for _, attribute := range token.Attr {
				switch strings.ToLower(attribute.Key) {
				case "href":
					href = attribute.Val
				case "download":
					download = true
				}
			}
			if href != "" && (download || themeSlotLooksLikeAttachment(href)) {
				if err := validate(href); err != nil {
					return err
				}
			}
		}
	}
}

func themeSlotLooksLikeAttachment(raw string) bool {
	if !resourcepath.IsLocalTarget(raw) {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	targetPath := ""
	if err == nil {
		targetPath = parsed.Path
	} else {
		targetPath = raw
		if index := strings.IndexAny(targetPath, "?#"); index >= 0 {
			targetPath = targetPath[:index]
		}
	}
	extension := strings.ToLower(path.Ext(targetPath))
	return extension != "" && extension != ".html" && extension != ".htm" && extension != ".md"
}

// Theme inputs are admitted explicitly, not via Markdown's resource discovery.
// Keep their exact physical identities (including NFC/NFD distinctions), and
// allocate their emitted bytes through the shared content-addressed planner.
func planThemeAssets(plan *model.SitePlan, scan vault.ScanResult, index *model.VaultIndex, inputs map[string][]byte, assetSources map[string]string, cssSource string, collector *diag.Collector) {
	plan.ThemeAssets = make(map[string]*model.PlannedAsset, len(inputs))
	plan.ThemeAssetURLs = make(map[string]string, len(assetSources))
	plan.Config.ThemeCSS = ""
	cssDirs := make(map[string]string, len(assetSources))
	for name, source := range assetSources {
		cssDirs[source] = path.Dir(name)
	}
	// Previously theme.css and the contents of assets/ shared one directory.
	cssDirs[cssSource] = "."

	vaultVisiting := make(map[string]bool)
	var resolveVault func(string) (*model.PlannedAsset, error)
	resolveVault = func(source string) (*model.PlannedAsset, error) {
		if planned := plan.VaultCSSAssets[source]; planned != nil {
			return planned, nil
		}
		if planned := plan.ThemeAssets[source]; planned != nil {
			return planned, nil
		}
		if vaultVisiting[source] {
			return nil, fmt.Errorf("cyclic CSS dependency at %q", source)
		}
		vaultVisiting[source] = true
		defer delete(vaultVisiting, source)
		plan.CSSInputFiles[source] = struct{}{}
		_, data, _, err := internalfsutil.ReadContainedRegularFile(plan.VaultPath, source)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(path.Ext(source), ".css") {
			data, err = asset.RewriteCSSURLs(data, func(raw string) (string, error) {
				return rewriteVaultCSSURL(plan, scan, index, source, raw, resolveVault)
			})
			if err != nil {
				return nil, err
			}
		}
		planned := asset.PlanData(source, data)
		plan.ThemeAssets[source] = planned
		return planned, nil
	}

	visiting := make(map[string]bool)
	var resolve func(string) (*model.PlannedAsset, error)
	resolve = func(source string) (*model.PlannedAsset, error) {
		if planned := plan.ThemeAssets[source]; planned != nil {
			return planned, nil
		}
		if visiting[source] {
			return nil, fmt.Errorf("cyclic theme CSS dependency at %q", source)
		}
		visiting[source] = true
		defer delete(visiting, source)
		data := inputs[source]
		if strings.EqualFold(path.Ext(source), ".css") {
			var err error
			data, err = asset.RewriteCSSURLs(data, func(raw string) (string, error) {
				u, err := url.Parse(raw)
				if err != nil {
					return "", fmt.Errorf("theme CSS URL %q: %w", raw, err)
				}
				if u.IsAbs() || u.Host != "" || strings.HasPrefix(raw, "//") || u.Path == "" {
					return raw, nil
				}
				if strings.HasPrefix(raw, "/") {
					return rewriteVaultCSSURL(plan, scan, index, "", raw, resolveVault)
				}
				name := path.Join(cssDirs[source], u.Path)
				target := assetSources[name]
				if target == "" && name == "theme.css" {
					target = cssSource
				}
				if target == "" {
					return "", fmt.Errorf("theme CSS asset %q was not found", raw)
				}
				dependency, err := resolve(target)
				if err != nil {
					return "", err
				}
				suffix := ""
				if i := strings.IndexAny(raw, "?#"); i >= 0 {
					suffix = raw[i:]
				}
				return path.Base(dependency.DstPath) + suffix, nil
			})
			if err != nil {
				return nil, err
			}
		}
		planned := asset.PlanData(source, data)
		plan.ThemeAssets[source] = planned
		return planned, nil
	}
	ordered := make([]string, 0, len(inputs))
	for source := range inputs {
		ordered = append(ordered, source)
	}
	sort.Strings(ordered)
	for _, source := range ordered {
		if _, err := resolve(source); err != nil {
			record(collector, diag.KindMetadata, source, "theme asset: %v", err)
		}
	}
	for name, source := range assetSources {
		if planned := plan.ThemeAssets[source]; planned != nil {
			plan.ThemeAssetURLs[name] = planned.DstPath
		}
	}
	if planned := plan.ThemeAssets[cssSource]; planned != nil {
		plan.Config.ThemeCSS = planned.DstPath
	}
}
