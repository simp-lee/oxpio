package render

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/simp-lee/oxpio/internal/branding"
	"github.com/tdewolff/minify/v2"
	mincss "github.com/tdewolff/minify/v2/css"
)

const (
	katexCSSOutputPath  = "assets/oxpio-runtime/katex.min.css"
	katexJSOutputPath   = "assets/oxpio-runtime/katex.min.js"
	katexAutoOutputPath = "assets/oxpio-runtime/auto-render.min.js"
	mermaidJSOutputPath = "assets/oxpio-runtime/mermaid.min.js"
	logoOutputPath      = "assets/oxpio/logo.svg"
)

type embeddedOutputAsset struct {
	name       string
	outputPath string
}

var runtimeTemplateAssets = func() []embeddedOutputAsset {
	assets := []embeddedOutputAsset{
		{name: "vendor/katex/katex.min.css", outputPath: katexCSSOutputPath},
		{name: "vendor/katex/katex.min.js", outputPath: katexJSOutputPath},
		{name: "vendor/katex/contrib/auto-render.min.js", outputPath: katexAutoOutputPath},
		{name: "vendor/mermaid/mermaid.min.js", outputPath: mermaidJSOutputPath},
	}
	fonts, _ := fs.Glob(embeddedSiteFS, "vendor/katex/fonts/*")
	for _, name := range fonts {
		assets = append(assets, embeddedOutputAsset{name: name, outputPath: path.Join("assets/oxpio-runtime/fonts", path.Base(name))})
	}
	return assets
}()

type sharedRuntimeFile struct {
	outputPath string
	data       []byte
}

// RuntimeAsset is one fixed offline runtime file ready for owner-registry publication.
type RuntimeAsset struct {
	OutputPath string
	Data       []byte
}

var loadSharedRuntimeFile = sync.OnceValues(func() (sharedRuntimeFile, error) {
	data, err := readEmbeddedAsset("runtime.js")
	if err != nil {
		return sharedRuntimeFile{}, err
	}
	hash := sha256.Sum256(data)
	return sharedRuntimeFile{outputPath: fmt.Sprintf("assets/oxpio/runtime.%x.js", hash), data: data}, nil
})

// StyleCSSData returns the fixed built-in stylesheet for owner-registry publication.
func StyleCSSData() ([]byte, error) {
	data, err := readEmbeddedAsset("style.css")
	if err != nil {
		return nil, fmt.Errorf("read style.css: %w", err)
	}
	minifier := minify.New()
	minifier.AddFunc("text/css", mincss.Minify)
	compact, err := minifier.Bytes("text/css", data)
	if err != nil {
		return nil, fmt.Errorf("minify style.css: %w", err)
	}
	return compact, nil
}

// RuntimeAssetData returns all fixed offline runtime files for owner-registry publication.
func RuntimeAssetData() ([]RuntimeAsset, error) {
	result := make([]RuntimeAsset, 0, len(runtimeTemplateAssets)+2)
	for _, asset := range runtimeTemplateAssets {
		data, err := readEmbeddedAsset(asset.name)
		if err != nil {
			return nil, fmt.Errorf("read runtime asset %s: %w", asset.name, err)
		}
		result = append(result, RuntimeAsset{OutputPath: asset.outputPath, Data: append([]byte(nil), data...)})
	}
	runtimeFile, err := loadSharedRuntimeFile()
	if err != nil {
		return nil, fmt.Errorf("read shared runtime: %w", err)
	}
	result = append(result, RuntimeAsset{OutputPath: runtimeFile.outputPath, Data: append([]byte(nil), runtimeFile.data...)})
	result = append(result, RuntimeAsset{OutputPath: logoOutputPath, Data: branding.LogoSVG()})
	return result, nil
}

// LogoOutputPath returns the fixed output path for the built-in OXPIO logo.
func LogoOutputPath() string { return logoOutputPath }

// SharedRuntimeOutputPath returns the content-addressed shared runtime path.
func SharedRuntimeOutputPath() (string, error) {
	runtimeFile, err := loadSharedRuntimeFile()
	if err != nil {
		return "", err
	}
	return runtimeFile.outputPath, nil
}

func readEmbeddedAsset(name string) ([]byte, error) {
	assetPath := name
	if !strings.HasPrefix(assetPath, "vendor/") {
		assetPath = embeddedSiteAssetPath(name)
	}
	data, err := embeddedSiteFS.ReadFile(assetPath)
	if err != nil {
		return nil, fmt.Errorf("read embedded asset %q: %w", name, err)
	}
	return data, nil
}

// EmbeddedRuntimeAssetNames returns the fixed runtime inventory.
func EmbeddedRuntimeAssetNames() []string {
	result := make([]string, 0, len(runtimeTemplateAssets)+3)
	for _, asset := range runtimeTemplateAssets {
		result = append(result, asset.name)
	}
	result = append(result, "runtime.js", "style.css", "logo.svg")
	return result
}
