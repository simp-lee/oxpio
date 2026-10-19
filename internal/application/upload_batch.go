package application

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	internalmodel "github.com/simp-lee/oxpio/internal/model"
	internalpublish "github.com/simp-lee/oxpio/internal/publish"
	"github.com/simp-lee/oxpio/internal/publishpath"
	"github.com/simp-lee/oxpio/internal/workspace"
)

const maxBatchFiles = 10000

// UploadBatchItem is one create-only Markdown source in an import manifest.
// The content is carried in the manifest so the whole set can be analyzed as
// one overlay before any source file is committed.
type UploadBatchItem struct {
	Path    string
	Kind    string
	Content []byte
}

// UploadBatchMutationRequest describes a strict, create-only Markdown import.
// Section directories must already exist; section indexes may be included in
// the manifest and are checked through the same final overlay as articles.
type UploadBatchMutationRequest struct {
	Items              []UploadBatchItem
	IncludeUnpublished bool
	DryRun             bool
}

// MutateUploadBatch imports mutually-referencing Markdown sources as one
// source-only transaction. It never publishes output.
func MutateUploadBatch(vaultPath, outputPath string, request UploadBatchMutationRequest) (MutationResult, error) {
	boundary, err := mutationBoundary(vaultPath, outputPath)
	if err != nil {
		return MutationResult{}, err
	}
	if len(request.Items) == 0 || len(request.Items) > maxBatchFiles {
		return MutationResult{}, fmt.Errorf("import manifest must contain 1..%d files", maxBatchFiles)
	}
	if request.DryRun {
		if err := workspace.CheckDryRun(boundary.VaultPath); err != nil {
			return MutationResult{}, err
		}
		return mutateUploadBatchLocked(boundary, request)
	}
	if err := recoverPendingApplicationJournal(boundary.VaultPath); err != nil {
		return MutationResult{}, err
	}
	lease, err := workspace.Acquire(boundary.VaultPath)
	if err != nil {
		return MutationResult{}, err
	}
	defer func() { _ = lease.Release() }()
	return mutateUploadBatchLocked(boundary, request)
}

func mutateUploadBatchLocked(boundary internalfsutil.VaultOutputBoundary, request UploadBatchMutationRequest) (MutationResult, error) {
	overlay := make(map[string][]byte, len(request.Items))
	items := make([]UploadBatchItem, 0, len(request.Items))
	seen := make(map[string]struct{}, len(request.Items))
	for _, item := range request.Items {
		target, err := NormalizeSourcePath(item.Path)
		if err != nil {
			return MutationResult{}, fmt.Errorf("import path %q: %w", item.Path, err)
		}
		if target != item.Path {
			return MutationResult{}, fmt.Errorf("import path %q must be normalized", item.Path)
		}
		if publishpath.IsReservedPath(target) {
			return MutationResult{}, fmt.Errorf("import path %q is reserved", target)
		}
		if item.Kind != UploadArticle && item.Kind != UploadSectionIndex {
			return MutationResult{}, errors.New("import manifests support only article and section-index Markdown files")
		}
		name := path.Base(target)
		if item.Kind == UploadSectionIndex && name != "_index.md" {
			return MutationResult{}, fmt.Errorf("section-index import %q must be named _index.md", target)
		}
		if item.Kind == UploadArticle && name == "_index.md" {
			return MutationResult{}, fmt.Errorf("_index.md import %q requires kind section-index", target)
		}
		if len(item.Content) > maxUploadMarkdown || !utf8.Valid(item.Content) || strings.IndexByte(string(item.Content), 0) >= 0 {
			return MutationResult{}, fmt.Errorf("markdown import %q is invalid or exceeds 1 MiB", target)
		}
		if _, exists := seen[target]; exists {
			return MutationResult{}, fmt.Errorf("import manifest contains duplicate path %q", target)
		}
		seen[target] = struct{}{}
		if _, statErr := os.Lstat(filepathJoin(boundary.VaultPath, target)); statErr == nil {
			return MutationResult{}, &ConflictError{Path: target, Expected: AbsentHash, Actual: "present"}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return MutationResult{}, statErr
		}
		directory := path.Dir(target)
		if err := validateUploadTarget(boundary, directory, target); err != nil {
			return MutationResult{}, err
		}
		items = append(items, UploadBatchItem{Path: target, Kind: item.Kind, Content: append([]byte(nil), item.Content...)})
		overlay[target] = append([]byte(nil), item.Content...)
	}
	for _, item := range items {
		if err := validateBatchSectionAncestry(boundary.VaultPath, path.Dir(item.Path), overlay); err != nil {
			return MutationResult{}, err
		}
	}
	startFingerprint, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return MutationResult{}, err
	}
	analysis, analyzeErr := internalanalyze.AnalyzeWithOutputAndConcurrencyAndOverlay(boundary.VaultPath, boundary.OutputPath, 0, overlay, nil)
	if analyzeErr != nil || len(analysis.Diagnostics) != 0 || analysis.Plan == nil || analysis.Plan.Plan == nil {
		diagnostics := analysis.Diagnostics
		if !request.IncludeUnpublished {
			diagnostics = redactUnavailableMutationDiagnostics(boundary.VaultPath, diagnostics)
		}
		if analyzeErr == nil {
			analyzeErr = internalanalyze.Failure(diagnostics)
		}
		if analyzeErr == nil {
			analyzeErr = errors.New("strict batch analysis produced no site plan")
		}
		return MutationResult{}, &MutationError{Err: analyzeErr, Diagnostics: diagnostics}
	}
	catalog := sourceCatalogFromAnalysis(analysis.Plan)
	for _, item := range items {
		entry := batchCatalogEntry(catalog, item.Path)
		if entry == nil || (item.Kind == UploadArticle && entry.Kind != "article") || (item.Kind == UploadSectionIndex && entry.Kind != "section") {
			return MutationResult{}, &MutationError{Err: errors.New("imported Markdown kind does not match the normalized site plan"), Diagnostics: analysis.Diagnostics}
		}
		if !request.IncludeUnpublished && !entry.EffectivePublish {
			return MutationResult{}, ErrHiddenSource
		}
	}
	changes := make([]MutationChange, 0, len(items))
	sourceChanges := make([]workspace.SourceChange, 0, len(items))
	completed := make([]workspace.CompletedPath, 0, len(items))
	for _, item := range items {
		after := hashBytes(item.Content)
		changes = append(changes, MutationChange{Kind: "create", Domain: "source", Path: item.Path, BeforeHash: nil, AfterHash: stringPtrHash(after)})
		absolute := filepathJoin(boundary.VaultPath, item.Path)
		sourceChanges = append(sourceChanges, workspace.SourceChange{Path: absolute, BeforeHash: AbsentHash, AfterHash: after})
		completed = append(completed, workspace.CompletedPath{Path: absolute, AfterHash: after})
	}
	result := MutationResult{Operation: "import", Path: "batch", Diagnostics: redactMutationDiagnostics(analysis.Diagnostics, catalog), ImpactedPages: len(analysis.Plan.Plan.Pages), ArtifactSummary: map[string]int{"importedSources": len(items)}, Next: "validate then explicitly publish", Changes: changes}
	if request.DryRun {
		end, fingerprintErr := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
		if fingerprintErr != nil {
			return MutationResult{}, fingerprintErr
		}
		if end != startFingerprint {
			return MutationResult{}, errors.New("CONCURRENT_INPUT: Vault inputs changed during dry-run")
		}
		return result, nil
	}
	end, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return MutationResult{}, err
	}
	if end != startFingerprint {
		return MutationResult{}, errors.New("CONCURRENT_INPUT: Vault inputs changed during import")
	}
	expectedFingerprint, err := workspace.FingerprintWithOverlay(boundary.VaultPath, boundary.OutputPath, overlay, nil)
	if err != nil {
		return MutationResult{}, err
	}
	outputHash := internalpublish.DirectoryHash(boundary.OutputPath)
	identity := internalpublish.OutputIdentity(boundary.OutputPath)
	if outputHash == "unavailable" || identity == "" {
		return MutationResult{}, errors.New("cannot capture output identity for batch import")
	}
	journal := workspace.Journal{Version: workspace.JournalVersion, CommitKind: "source-batch", Phase: "prepared", InputFingerprint: startFingerprint, TargetPath: boundary.OutputPath, OutputPath: boundary.OutputPath, OutputBeforeHash: outputHash, OutputIdentity: identity, AfterFingerprint: expectedFingerprint, SourceChanges: sourceChanges}
	if err := workspace.WriteJournal(boundary.VaultPath, journal); err != nil {
		return MutationResult{}, err
	}
	if err := workspace.RecordCompleted(boundary.VaultPath, completed); err != nil {
		cleanupErr := workspace.RemoveCompleted(boundary.VaultPath, completed)
		journalErr := workspace.ClearJournal(boundary.VaultPath)
		return MutationResult{}, errors.Join(fmt.Errorf("watch suppression marker could not be recorded: %w", err), cleanupErr, journalErr)
	}
	for _, item := range items {
		if err := workspace.AtomicCreateFile(filepathJoin(boundary.VaultPath, item.Path), item.Content, 0o644); err != nil {
			return MutationResult{}, fmt.Errorf("%w: batch import recovery required: %v", workspace.ErrRecovery, err)
		}
	}
	for _, change := range sourceChanges {
		got, hashErr := hashContainedBatchFile(boundary.VaultPath, change.Path)
		if hashErr != nil || got != change.AfterHash {
			return MutationResult{}, fmt.Errorf("%w: batch import postcondition changed", workspace.ErrRecovery)
		}
	}
	finalFingerprint, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil || finalFingerprint != expectedFingerprint {
		return MutationResult{}, fmt.Errorf("%w: batch import input changed during commit", workspace.ErrRecovery)
	}
	journal.Phase = "source-committed"
	if err := workspace.WriteJournal(boundary.VaultPath, journal); err != nil {
		return result, errors.Join(fmt.Errorf("%w: batch import recovery journal could not be finalized", workspace.ErrRecovery), err)
	}
	if err := workspace.ClearJournal(boundary.VaultPath); err != nil {
		return result, errors.Join(fmt.Errorf("%w: batch import recovery journal could not be cleared", workspace.ErrRecovery), err)
	}
	return result, nil
}

func batchCatalogEntry(catalog *internalmodel.SourceCatalog, path string) *internalmodel.SourceCatalogEntry {
	if catalog == nil {
		return nil
	}
	for index := range catalog.Entries {
		if catalog.Entries[index].RelPath == path {
			return &catalog.Entries[index]
		}
	}
	return nil
}

func validateBatchSectionAncestry(vault, sectionPath string, overlay map[string][]byte) error {
	check := func(target string) bool {
		if _, ok := overlay[target]; ok {
			return true
		}
		_, _, _, err := internalfsutil.ReadContainedRegularFile(vault, filepathJoin(vault, target))
		return err == nil
	}
	if !check("_index.md") {
		return errors.New("section ancestry must include the root _index.md")
	}
	current := path.Clean(sectionPath)
	for current != "." && current != "" {
		index := path.Join(current, "_index.md")
		if !check(index) {
			return fmt.Errorf("section ancestry is missing %q", index)
		}
		current = path.Dir(current)
	}
	return nil
}

func hashContainedBatchFile(vault, name string) (string, error) {
	_, file, _, err := internalfsutil.OpenContainedRegularFile(vault, name)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxUploadMarkdown+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxUploadMarkdown {
		return "", errors.New("batch source exceeds 1 MiB")
	}
	return hashBytes(data), nil
}
