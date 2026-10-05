package build

import (
	"fmt"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	"github.com/simp-lee/oxpio/internal/diag"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/siteplan"
)

// BuildResult is the public build summary returned to the CLI.
type BuildResult struct {
	OutputPath  string
	Index       *model.VaultIndex
	Graph       *model.LinkGraph
	Assets      map[string]*model.Asset
	Diagnostics []diag.Diagnostic
	// Catalog is the normalized source mapping used by edit mode. Consumers
	// must treat its entries as read-only.
	Catalog      *model.SourceCatalog
	NotePages    int
	TagPages     int
	WarningCount int
	ErrorCount   int
	// OutputCleanupError reports backup cleanup after a successful commit. It
	// is publication status, not a quality failure.
	OutputCleanupError error
}

// Options controls the strict section-based build entry point.
type Options struct {
	Strict            bool
	DiagnosticsWriter io.Writer
	// TrackOutputTransactionPath is called when the publisher creates a
	// temporary output transaction path.
	TrackOutputTransactionPath func(path string)
	// Concurrency bounds independent Markdown indexing and recommendation
	// workers. A non-positive value uses the production default.
	Concurrency int
	// SourceOverlay and SourceDeleted are immutable candidate Markdown inputs
	// used by edit transactions. They are never written by the builder.
	SourceOverlay map[string][]byte
	SourceDeleted map[string]bool
	// SourceOutputPath is the formal output boundary to exclude from source
	// discovery when it differs from the temporary publication path.
	SourceOutputPath string
}

// BuildWithOptions analyzes the vault once and publishes the resulting
// canonical section plan. Normal builds continue after warnings; strict builds
// reject both warnings and errors before opening a staging publisher.
func BuildWithOptions(vaultPath, outputPath string, options Options) (*BuildResult, error) {
	sourceOutputPath := outputPath
	if options.SourceOutputPath != "" {
		sourceOutputPath = options.SourceOutputPath
	}
	analysis, analyzeErr := internalanalyze.AnalyzeWithOutputAndConcurrencyAndOverlay(vaultPath, sourceOutputPath, options.Concurrency, options.SourceOverlay, options.SourceDeleted)
	if writeErr := internalanalyze.WriteDiagnostics(options.DiagnosticsWriter, analysis.Diagnostics); writeErr != nil {
		return nil, fmt.Errorf("write diagnostics: %w", writeErr)
	}
	result := buildResultFromAnalysis(analysis.Diagnostics)
	result.Catalog = sourceCatalog(analysis.Plan)
	if analyzeErr != nil || result.ErrorCount > 0 || options.Strict && result.WarningCount > 0 {
		if analyzeErr != nil {
			return result, analyzeErr
		}
		return result, internalanalyze.Failure(analysis.Diagnostics)
	}
	strictResult, buildErr := buildStrictSiteWithTransactionTracking(analysis.Plan, vaultPath, outputPath, options.DiagnosticsWriter, options.TrackOutputTransactionPath, options.Concurrency)
	if strictResult == nil {
		strictResult = &BuildResult{}
	}
	strictResult.Diagnostics = append(result.Diagnostics, strictResult.Diagnostics...)
	if strictResult.Catalog == nil {
		strictResult.Catalog = result.Catalog
	}
	strictResult.ErrorCount += result.ErrorCount
	strictResult.WarningCount += result.WarningCount
	return strictResult, buildErr
}

func sourceCatalog(planned *siteplan.Result) *model.SourceCatalog {
	if planned == nil || planned.Plan == nil {
		return nil
	}
	catalog := &model.SourceCatalog{}
	if parsed, err := url.Parse(planned.Plan.Config.BaseURL); err == nil && parsed.EscapedPath() != "" {
		catalog.BasePath = parsed.EscapedPath()
	}
	if catalog.BasePath == "" {
		catalog.BasePath = "/"
	}
	sections := make(map[string]*model.Section, len(planned.Plan.Sections))
	for _, section := range planned.Plan.Sections {
		if section != nil {
			sections[section.SourcePath] = section
		}
	}
	articles := make(map[string]*model.Note, len(planned.Plan.Articles))
	for _, article := range planned.Plan.Articles {
		if article != nil {
			articles[article.RelPath] = article
		}
	}
	entries := make([]model.SourceCatalogEntry, 0, len(planned.Sources.Sources))
	for _, source := range planned.Sources.Sources {
		if source.Section != nil {
			entry := model.SourceCatalogEntry{
				RelPath: source.RelPath, Title: source.Section.Frontmatter.Title, Kind: "section",
				Publish: source.Publish, SectionPath: source.Section.SectionPath,
			}
			if section := sections[source.RelPath]; section != nil {
				entry.Route, entry.EffectivePublish, entry.VersionID = section.Route, section.EffectivePublish, section.VersionID
			}
			entries = append(entries, entry)
			continue
		}
		if source.Article == nil {
			continue
		}
		entry := model.SourceCatalogEntry{
			RelPath: source.RelPath, Title: source.Article.Frontmatter.Title, Kind: "article",
			Type: source.Article.Frontmatter.Type, Publish: source.Publish,
			SectionPath: path.Dir(source.RelPath),
		}
		if article := articles[source.RelPath]; article != nil {
			entry.Route, entry.EffectivePublish, entry.SectionPath, entry.VersionID = article.Route, true, article.SectionPath, article.VersionID
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return strings.Compare(entries[i].RelPath, entries[j].RelPath) < 0 })
	catalog.Entries = entries
	return catalog
}

func buildResultFromAnalysis(diagnostics []diag.Diagnostic) *BuildResult {
	result := &BuildResult{Diagnostics: append([]diag.Diagnostic(nil), diagnostics...)}
	for _, diagnostic := range diagnostics {
		switch diagnostic.Severity {
		case diag.SeverityError:
			result.ErrorCount++
		case diag.SeverityWarning:
			result.WarningCount++
		}
	}
	return result
}
