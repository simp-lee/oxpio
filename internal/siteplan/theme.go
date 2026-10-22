package siteplan

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/simp-lee/oxpio/internal/asset"
	internalconfig "github.com/simp-lee/oxpio/internal/config"
	"github.com/simp-lee/oxpio/internal/diag"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/model"
	internaltheme "github.com/simp-lee/oxpio/internal/theme"
	"github.com/simp-lee/oxpio/internal/vault"
	"github.com/tdewolff/parse/v2"
	parsejs "github.com/tdewolff/parse/v2/js"
)

// ValidateThemeSource validates a theme with the same content-addressed asset
// planner used by a strict site plan, without scanning or writing a vault.
func ValidateThemeSource(vaultRoot string, source internaltheme.Source) error {
	if err := internaltheme.Validate(source); err != nil {
		return err
	}
	plan := &model.SitePlan{VaultPath: vaultRoot, ThemeFiles: source.Files, ThemeAssets: map[string]*model.PlannedAsset{}, VaultCSSAssets: map[string]*model.PlannedAsset{}, CSSInputFiles: map[string]struct{}{}}
	inputs := make(map[string][]byte)
	assetSources := make(map[string]string)
	for rel, data := range source.Files {
		if !strings.HasPrefix(rel, "assets/") || strings.TrimPrefix(rel, "assets/") == "" {
			continue
		}
		name := strings.TrimPrefix(rel, "assets/")
		physical := rel
		if !source.Embedded {
			physical = filepath.ToSlash(filepath.Join(internalconfig.ThemeRootRelPath, source.ID, rel))
		}
		inputs[physical] = data
		assetSources[name] = physical
	}
	collector := diag.NewCollector()
	planThemeAssets(plan, vault.ScanResult{}, vault.ScanResult{}, nil, inputs, assetSources, "", collector)
	if collector.HasErrors() {
		items := collector.Diagnostics()
		if len(items) > 0 {
			return fmt.Errorf("theme asset validation: %s", items[0].Message)
		}
		return fmt.Errorf("theme asset validation failed")
	}
	return internaltheme.ValidateWithAssets(source, plan.ThemeAssetURLs, "https://theme.invalid/")
}

// Theme inputs are admitted explicitly, not via Markdown's resource discovery.
// Keep their exact physical identities (including NFC/NFD distinctions), and
// allocate their emitted bytes through the shared content-addressed planner.
func planThemeAssets(plan *model.SitePlan, scan, fullScan vault.ScanResult, index *model.VaultIndex, inputs map[string][]byte, assetSources map[string]string, cssSource string, collector *diag.Collector) {
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
				return rewriteVaultCSSURL(plan, scan, fullScan, index, source, raw, resolveVault)
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
			return nil, fmt.Errorf("cyclic theme asset dependency at %q", source)
		}
		visiting[source] = true
		defer delete(visiting, source)
		data := inputs[source]
		if strings.EqualFold(path.Ext(source), ".css") {
			var err error
			data, err = asset.RewriteCSSURLs(data, func(raw string) (string, error) {
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "data:image/") {
					return raw, nil
				}
				u, err := url.Parse(raw)
				if err != nil {
					return "", fmt.Errorf("theme CSS URL %q: %w", raw, err)
				}
				if u.IsAbs() || u.Host != "" || strings.HasPrefix(raw, "//") {
					return "", fmt.Errorf("theme CSS external URL %q is not allowed", raw)
				}
				if u.Path == "" {
					return raw, nil
				}
				if strings.HasPrefix(raw, "/") {
					return "", fmt.Errorf("theme CSS URL %q must reference a theme asset", raw)
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
		} else if strings.EqualFold(path.Ext(source), ".js") {
			var err error
			data, err = rewriteThemeJavaScriptImports(data, source, assetSources, resolve)
			if err != nil {
				return nil, err
			}
		} else if strings.EqualFold(path.Ext(source), ".svg") {
			assetName := ""
			for name, candidate := range assetSources {
				if candidate == source {
					assetName = name
					break
				}
			}
			var err error
			data, err = rewriteThemeSVGReferences(data, assetName, assetSources, resolve)
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
			plan.ThemeAssetURLs["assets/"+name] = planned.DstPath
		}
	}
	if cssSource != "" {
		if planned := plan.ThemeAssets[cssSource]; planned != nil {
			plan.Config.ThemeCSS = planned.DstPath
		}
	} else if source := assetSources["style.css"]; source != "" {
		if planned := plan.ThemeAssets[source]; planned != nil {
			plan.Config.ThemeCSS = planned.DstPath
		}
	}
}

func rewriteThemeSVGReferences(data []byte, assetName string, assetSources map[string]string, resolve func(string) (*model.PlannedAsset, error)) ([]byte, error) {
	var rewritten, safe bytes.Buffer
	rewrittenEncoder := xml.NewEncoder(&rewritten)
	safeEncoder := xml.NewEncoder(&safe)
	decoder := xml.NewDecoder(bytes.NewReader(data))
	didRewrite := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode theme SVG: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if ok {
			safeStart := start
			safeStart.Attr = append([]xml.Attr(nil), start.Attr...)
			for i := range start.Attr {
				attribute := &start.Attr[i]
				if !strings.EqualFold(attribute.Name.Local, "href") && !strings.EqualFold(attribute.Name.Local, "src") {
					continue
				}
				value := strings.TrimSpace(attribute.Value)
				if value == "" || strings.HasPrefix(value, "#") {
					continue
				}
				parsed, parseErr := url.Parse(value)
				if parseErr != nil || parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\\\r\n\x00") {
					return nil, fmt.Errorf("theme SVG reference %q is not a local asset", value)
				}
				resourcePath := parsed.Path
				if resourcePath == "" || path.Clean(resourcePath) != resourcePath || strings.HasPrefix(resourcePath, "/") || strings.HasPrefix(resourcePath, "../") || resourcePath == ".." {
					return nil, fmt.Errorf("theme SVG reference %q is not a normalized asset path", value)
				}
				name := path.Join(path.Dir(assetName), resourcePath)
				target := assetSources[name]
				if target == "" {
					return nil, fmt.Errorf("theme SVG asset %q was not found", value)
				}
				dependency, resolveErr := resolve(target)
				if resolveErr != nil {
					return nil, resolveErr
				}
				suffix := value[len(resourcePath):]
				attribute.Value = path.Base(dependency.DstPath) + suffix
				safeStart.Attr[i].Value = "#theme-asset"
				didRewrite = true
			}
			if err := rewrittenEncoder.EncodeToken(start); err != nil {
				return nil, err
			}
			if err := safeEncoder.EncodeToken(safeStart); err != nil {
				return nil, err
			}
			continue
		}
		if err := rewrittenEncoder.EncodeToken(token); err != nil {
			return nil, err
		}
		if err := safeEncoder.EncodeToken(token); err != nil {
			return nil, err
		}
	}
	if err := rewrittenEncoder.Flush(); err != nil {
		return nil, err
	}
	if err := safeEncoder.Flush(); err != nil {
		return nil, err
	}
	if didRewrite {
		if err := asset.ValidateLocalSVG(safe.Bytes()); err != nil {
			return nil, fmt.Errorf("theme SVG: %w", err)
		}
		return rewritten.Bytes(), nil
	}
	if err := asset.ValidateLocalSVG(data); err != nil {
		return nil, fmt.Errorf("theme SVG: %w", err)
	}
	return data, nil
}

func rewriteThemeJavaScriptImports(data []byte, source string, assetSources map[string]string, resolve func(string) (*model.PlannedAsset, error)) ([]byte, error) {
	specifiers, err := themeJavaScriptImportSpecifiers(data)
	if err != nil {
		return nil, err
	}
	assetName := ""
	for name, candidate := range assetSources {
		if candidate == source {
			assetName = name
			break
		}
	}
	if assetName == "" || len(specifiers) == 0 {
		return data, nil
	}
	var rewritten strings.Builder
	last := 0
	for _, specifier := range specifiers {
		raw := specifier.raw
		parsed, err := url.Parse(raw)
		if err != nil || parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/") || strings.ContainsAny(raw, "\\\\\r\n\x00") {
			return nil, fmt.Errorf("theme JavaScript import %q is not a local asset", raw)
		}
		resourcePath := raw
		suffix := ""
		if split := strings.IndexAny(raw, "?#"); split >= 0 {
			resourcePath, suffix = raw[:split], raw[split:]
		}
		resourcePath = strings.TrimPrefix(resourcePath, "./")
		if resourcePath == "" || path.Clean(resourcePath) != resourcePath || strings.HasPrefix(resourcePath, "../") || resourcePath == ".." {
			return nil, fmt.Errorf("theme JavaScript import %q is not a normalized asset path", raw)
		}
		name := path.Join(path.Dir(assetName), resourcePath)
		target := assetSources[name]
		if target == "" {
			return nil, fmt.Errorf("theme JavaScript asset %q was not found", raw)
		}
		dependency, err := resolve(target)
		if err != nil {
			return nil, err
		}
		rewritten.Write(data[last:specifier.start])
		rewritten.WriteByte(data[specifier.start])
		rewritten.WriteString(path.Base(dependency.DstPath))
		rewritten.WriteString(suffix)
		rewritten.WriteByte(data[specifier.end-1])
		last = specifier.end
	}
	rewritten.Write(data[last:])
	return []byte(rewritten.String()), nil
}

type themeJavaScriptToken struct {
	kind       parsejs.TokenType
	start, end int
}

type themeJavaScriptSpecifier struct {
	start, end int
	raw        string
}

func themeJavaScriptImportSpecifiers(data []byte) ([]themeJavaScriptSpecifier, error) {
	if !bytes.Contains(data, []byte("import")) && !bytes.Contains(data, []byte("export")) {
		return nil, nil
	}
	tokens, err := lexThemeJavaScript(data)
	if err != nil {
		return nil, err
	}
	specifiers := make([]themeJavaScriptSpecifier, 0)
	for index, token := range tokens {
		if token.kind != parsejs.ImportToken && token.kind != parsejs.ExportToken {
			continue
		}
		if token.kind == parsejs.ImportToken && index > 0 && (tokens[index-1].kind == parsejs.DotToken || tokens[index-1].kind == parsejs.OptChainToken) {
			continue
		}
		if token.kind == parsejs.ImportToken && index+1 < len(tokens) {
			switch tokens[index+1].kind {
			case parsejs.DotToken:
				// import.meta is not an asset import.
				continue
			case parsejs.OpenParenToken:
				if index+2 >= len(tokens) || !isThemeJavaScriptStringToken(tokens[index+2].kind) {
					return nil, errors.New("theme JavaScript import must use a statically analyzable string literal")
				}
				specifier, literalErr := themeJavaScriptSpecifierFromToken(data, tokens[index+2])
				if literalErr != nil {
					return nil, literalErr
				}
				if index+3 >= len(tokens) || (tokens[index+3].kind != parsejs.CloseParenToken && tokens[index+3].kind != parsejs.CommaToken) {
					return nil, errors.New("theme JavaScript import must use a statically analyzable string literal")
				}
				specifiers = append(specifiers, specifier)
				continue
			}
		}
		start := index + 1
		if token.kind == parsejs.ImportToken && start < len(tokens) && isThemeJavaScriptStringToken(tokens[start].kind) {
			specifier, literalErr := themeJavaScriptSpecifierFromToken(data, tokens[start])
			if literalErr != nil {
				return nil, literalErr
			}
			specifiers = append(specifiers, specifier)
			continue
		}
		for current := start; current+1 < len(tokens); current++ {
			if tokens[current].kind == parsejs.SemicolonToken {
				break
			}
			if tokens[current].kind != parsejs.FromToken || !isThemeJavaScriptStringToken(tokens[current+1].kind) {
				continue
			}
			specifier, literalErr := themeJavaScriptSpecifierFromToken(data, tokens[current+1])
			if literalErr != nil {
				return nil, literalErr
			}
			specifiers = append(specifiers, specifier)
			break
		}
	}
	return specifiers, nil
}

func lexThemeJavaScript(data []byte) ([]themeJavaScriptToken, error) {
	input := parse.NewInputBytes(data)
	defer input.Restore()
	lexer := parsejs.NewLexer(input)
	tokens := make([]themeJavaScriptToken, 0)
	for {
		start := input.Offset()
		token, _ := lexer.Next()
		end := input.Offset()
		switch token {
		case parsejs.WhitespaceToken, parsejs.LineTerminatorToken, parsejs.CommentToken, parsejs.CommentLineTerminatorToken:
			continue
		case parsejs.ErrorToken:
			if lexer.Err() == io.EOF {
				return tokens, nil
			}
			return nil, fmt.Errorf("theme JavaScript import cannot be analyzed: %w", lexer.Err())
		default:
			tokens = append(tokens, themeJavaScriptToken{kind: token, start: start, end: end})
		}
	}
}

func isThemeJavaScriptStringToken(token parsejs.TokenType) bool {
	return token == parsejs.StringToken || token == parsejs.TemplateToken
}

func themeJavaScriptSpecifierFromToken(data []byte, token themeJavaScriptToken) (themeJavaScriptSpecifier, error) {
	literal := data[token.start:token.end]
	if len(literal) < 2 || (literal[0] != '\'' && literal[0] != '"' && literal[0] != '`') || literal[len(literal)-1] != literal[0] {
		return themeJavaScriptSpecifier{}, errors.New("theme JavaScript import must use a statically analyzable string literal")
	}
	raw := string(literal[1 : len(literal)-1])
	if token.kind == parsejs.TemplateToken && strings.ContainsAny(raw, "\\\\\r\n") {
		return themeJavaScriptSpecifier{}, errors.New("theme JavaScript import must use a statically analyzable string literal")
	}
	return themeJavaScriptSpecifier{start: token.start, end: token.end, raw: raw}, nil
}
