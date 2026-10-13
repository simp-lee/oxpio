package application

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	internalasset "github.com/simp-lee/oxpio/internal/asset"
	internalconfig "github.com/simp-lee/oxpio/internal/config"
	internaldiag "github.com/simp-lee/oxpio/internal/diag"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	internalmodel "github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/workspace"
)

const (
	UploadArticle       = "article"
	UploadSectionIndex  = "section-index"
	UploadAttachment    = "attachment"
	maxUploadMarkdown   = 1 << 20
	maxUploadAttachment = 64 << 20
)

// UploadMutationRequest describes the deliberately narrow, create-only Agent
// upload operation. Markdown uploads are analyzed through the normal planner;
// attachments are opaque regular-file bytes below the configured attachment root.
type UploadMutationRequest struct {
	Directory          string
	Name               string
	Kind               string
	ExpectedHash       string
	Content            []byte
	IncludeUnpublished bool
	DryRun             bool
}

// MutateUpload creates one new article, section index, or configured
// attachment. It never publishes output or reloads a service.
func MutateUpload(vaultPath, outputPath string, request UploadMutationRequest) (MutationResult, error) {
	boundary, err := mutationBoundary(vaultPath, outputPath)
	if err != nil {
		return MutationResult{}, err
	}
	if request.Kind != UploadArticle && request.Kind != UploadSectionIndex && request.Kind != UploadAttachment {
		return MutationResult{}, fmt.Errorf("--kind must be article, section-index, or attachment")
	}
	if request.ExpectedHash != AbsentHash {
		return MutationResult{}, errors.New("upload requires --if-hash absent")
	}
	if request.Kind == UploadAttachment && request.IncludeUnpublished {
		return MutationResult{}, errors.New("--include-unpublished is only valid for Markdown uploads")
	}
	if request.DryRun {
		if err := workspace.CheckDryRun(boundary.VaultPath); err != nil {
			return MutationResult{}, err
		}
		return mutateUploadLocked(boundary, request)
	}
	lease, err := workspace.Acquire(boundary.VaultPath)
	if err != nil {
		return MutationResult{}, err
	}
	defer func() { _ = lease.Release() }()
	return mutateUploadLocked(boundary, request)
}

func mutateUploadLocked(boundary internalfsutil.VaultOutputBoundary, request UploadMutationRequest) (MutationResult, error) {
	directory, err := normalizeUploadDirectory(request.Directory)
	if err != nil {
		return MutationResult{}, err
	}
	name, err := normalizeUploadName(request.Name)
	if err != nil {
		return MutationResult{}, err
	}
	if request.Kind == UploadArticle || request.Kind == UploadSectionIndex {
		name = normalizeMarkdownUploadName(name)
	}
	if request.Kind == UploadSectionIndex && name != "_index.md" {
		return MutationResult{}, errors.New("section-index uploads must be named _index.md")
	}
	if request.Kind != UploadSectionIndex && name == "_index.md" {
		return MutationResult{}, errors.New("_index.md uploads require --kind section-index")
	}
	target := name
	if directory != "." {
		target = path.Join(directory, name)
	}
	if request.Kind == UploadArticle || request.Kind == UploadSectionIndex {
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			return MutationResult{}, errors.New("markdown uploads require a .md filename")
		}
	} else if err := validateUploadAttachment(name, request.Content); err != nil {
		return MutationResult{}, err
	}
	if request.Kind == UploadArticle || request.Kind == UploadSectionIndex {
		if len(request.Content) > maxUploadMarkdown {
			return MutationResult{}, errors.New("markdown upload exceeds 1 MiB")
		}
		if !utf8.Valid(request.Content) || bytes.IndexByte(request.Content, 0) >= 0 {
			return MutationResult{}, ErrInvalidSource
		}
	} else if len(request.Content) > maxUploadAttachment {
		return MutationResult{}, errors.New("attachment exceeds 64 MiB")
	}
	if request.Kind == UploadAttachment {
		cfg, configErr := internalconfig.LoadForBuildWithOutput(boundary.VaultPath, boundary.OutputPath)
		if configErr != nil {
			return MutationResult{}, configErr
		}
		root := cfg.AttachmentDirectory
		if root == "" || (directory != root && !strings.HasPrefix(directory, root+"/")) {
			return MutationResult{}, errors.New("attachment upload must stay inside attachments.directory")
		}
	}
	if err := validateUploadTarget(boundary, directory, target); err != nil {
		return MutationResult{}, err
	}
	targetAbsolute := filepathJoin(boundary.VaultPath, target)
	if _, statErr := os.Lstat(targetAbsolute); statErr == nil {
		return MutationResult{}, &ConflictError{Path: target, Expected: AbsentHash, Actual: "present"}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return MutationResult{}, statErr
	}
	startFingerprint, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return MutationResult{}, err
	}

	var candidateEntry *internalmodel.SourceCatalogEntry
	var diagnostics []internaldiag.Diagnostic
	var impacted, candidateSources int
	if request.Kind != UploadAttachment {
		if request.Kind == UploadArticle && directory != "." {
			if _, _, _, ancestryErr := internalfsutil.ReadContainedRegularFile(boundary.VaultPath, filepathJoin(boundary.VaultPath, path.Join(directory, "_index.md"))); ancestryErr != nil {
				return MutationResult{}, fmt.Errorf("article upload requires an existing section: %w", ancestryErr)
			}
		}
		if err := validateSectionAncestry(boundary.VaultPath, directory); err != nil {
			return MutationResult{}, err
		}
		overlay := map[string][]byte{target: append([]byte(nil), request.Content...)}
		analysis, analyzeErr := internalanalyze.AnalyzeWithOutputAndConcurrencyAndOverlay(boundary.VaultPath, boundary.OutputPath, 0, overlay, nil)
		if analyzeErr != nil || len(analysis.Diagnostics) != 0 || analysis.Plan == nil {
			if analyzeErr == nil {
				analyzeErr = internalanalyze.Failure(analysis.Diagnostics)
			}
			if analyzeErr == nil {
				analyzeErr = errors.New("strict analysis produced no site plan")
			}
			return MutationResult{}, &MutationError{Err: analyzeErr, Diagnostics: analysis.Diagnostics}
		}
		catalog := sourceCatalogFromAnalysis(analysis.Plan)
		for index := range catalog.Entries {
			if catalog.Entries[index].RelPath == target {
				candidateEntry = &catalog.Entries[index]
				break
			}
		}
		if candidateEntry == nil || (request.Kind == UploadArticle && candidateEntry.Kind != "article") || (request.Kind == UploadSectionIndex && candidateEntry.Kind != "section") {
			return MutationResult{}, &MutationError{Err: errors.New("uploaded Markdown kind does not match the normalized site plan"), Diagnostics: analysis.Diagnostics}
		}
		if !request.IncludeUnpublished && !candidateEntry.EffectivePublish {
			return MutationResult{}, ErrHiddenSource
		}
		diagnostics = redactMutationDiagnostics(analysis.Diagnostics, catalog)
		impacted = len(analysis.Plan.Plan.Pages)
		candidateSources = len(catalog.Entries)
	}

	before := AbsentHash
	after := hashBytes(request.Content)
	publish := (*bool)(nil)
	if candidateEntry != nil {
		value := candidateEntry.Publish
		publish = &value
	}
	result := MutationResult{
		Operation: "upload", Path: target, Publish: publish, Diagnostics: diagnostics,
		ImpactedPages: impacted, ArtifactSummary: map[string]int{"candidateSources": candidateSources},
		Next:    "validate then explicitly publish",
		Changes: []MutationChange{{Kind: "create", Domain: "source", Path: target, BeforeHash: nil, AfterHash: stringPtrHash(after)}},
	}
	if request.DryRun {
		if end, fingerprintErr := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath); fingerprintErr != nil {
			return MutationResult{}, fingerprintErr
		} else if end != startFingerprint {
			return MutationResult{}, errors.New("CONCURRENT_INPUT: Vault inputs changed during dry-run")
		}
		return result, nil
	}
	if err := recheckUploadBeforeCommit(boundary, target, startFingerprint); err != nil {
		return MutationResult{}, err
	}
	journal, err := sourceOnlyJournal(boundary, "upload", target, "", before, request.Content, false, startFingerprint)
	if err != nil {
		return MutationResult{}, err
	}
	if err := workspace.WriteJournal(boundary.VaultPath, journal); err != nil {
		return MutationResult{}, err
	}
	if err := workspace.AtomicCreateFile(targetAbsolute, request.Content, 0o644); err != nil {
		if errors.Is(err, os.ErrExist) {
			return MutationResult{}, &ConflictError{Path: target, Expected: AbsentHash, Actual: "present"}
		}
		return MutationResult{}, err
	}
	if err := workspace.RecordCompleted(boundary.VaultPath, []workspace.CompletedPath{{Path: targetAbsolute, AfterHash: after}}); err != nil {
		result.Diagnostics = append(result.Diagnostics, directoryWarning("source committed; watch suppression marker could not be recorded"))
	}
	journal.Phase = "source-committed"
	if err := workspace.WriteJournal(boundary.VaultPath, journal); err != nil {
		result.Diagnostics = append(result.Diagnostics, directoryWarning("source committed; transaction journal finalization requires recovery"))
		return result, nil
	}
	if err := workspace.ClearJournal(boundary.VaultPath); err != nil {
		result.Diagnostics = append(result.Diagnostics, directoryWarning("source committed; transaction journal cleanup requires recovery"))
	}
	if _, fingerprintErr := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath); fingerprintErr != nil {
		result.Diagnostics = append(result.Diagnostics, directoryWarning("source committed; final input fingerprint could not be checked"))
	}
	return result, nil
}

func normalizeUploadDirectory(value string) (string, error) {
	normalized, err := NormalizeDirectoryPath(value)
	if err != nil {
		return "", err
	}
	if normalized != "." {
		for _, segment := range strings.Split(normalized, "/") {
			if segment == "" || segment == "." || segment == ".." || strings.EqualFold(segment, ".git") || strings.EqualFold(segment, "node_modules") {
				return "", errors.New("upload directory contains a reserved segment")
			}
		}
	}
	return normalized, nil
}

func normalizeUploadName(value string) (string, error) {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\\`) || strings.ContainsRune(value, 0) || path.Clean(value) != value || !internalfsutil.IsPortableSitePath(value) {
		return "", errors.New("upload name must be one safe path segment")
	}
	return value, nil
}

func validateUploadTarget(boundary internalfsutil.VaultOutputBoundary, directory, target string) error {
	if directory == "." {
		info, statErr := os.Lstat(boundary.VaultPath)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("upload directory must be an existing ordinary directory")
		}
	} else if _, info, err := internalfsutil.InspectContainedDirectory(boundary.VaultPath, directory); err != nil || info == nil || !info.IsDir() {
		if err != nil {
			return fmt.Errorf("upload directory must be an existing ordinary directory: %w", err)
		}
		return errors.New("upload directory must be an existing ordinary directory")
	}
	absolute := filepathJoin(boundary.VaultPath, target)
	if internalfsutil.PathWithinRootAfterSymlinks(boundary.OutputPath, absolute) {
		return errors.New("upload target overlaps the formal output")
	}
	return nil
}

func validateSectionAncestry(vault, sectionPath string) error {
	if _, _, _, err := internalfsutil.ReadContainedRegularFile(vault, filepathJoin(vault, "_index.md")); err != nil {
		return errors.New("section ancestry must include the root _index.md")
	}
	current := path.Dir(sectionPath)
	for current != "." && current != "" {
		if _, _, _, err := internalfsutil.ReadContainedRegularFile(vault, filepathJoin(vault, path.Join(current, "_index.md"))); err != nil {
			return fmt.Errorf("section ancestry is missing %q", path.Join(current, "_index.md"))
		}
		current = path.Dir(current)
	}
	return nil
}

func recheckUploadBeforeCommit(boundary internalfsutil.VaultOutputBoundary, target, startFingerprint string) error {
	absolute := filepathJoin(boundary.VaultPath, target)
	if _, err := os.Lstat(absolute); err == nil {
		return &ConflictError{Path: target, Expected: AbsentHash, Actual: "present"}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	current, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return err
	}
	if current != startFingerprint {
		return errors.New("CONCURRENT_INPUT: Vault inputs changed during upload")
	}
	return nil
}

func validateUploadAttachment(name string, data []byte) error {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".svg":
		return internalasset.ValidateUploadImage(name, data)
	}
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".md", ".markdown", ".html", ".htm", ".css", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".sh", ".bash", ".zsh", ".fish", ".bat", ".cmd", ".ps1", ".exe", ".com", ".dll", ".so", ".dylib":
		return errors.New("attachment type is not permitted")
	}
	if bytes.HasPrefix(data, []byte("#!")) || bytes.HasPrefix(data, []byte("\x7fELF")) || bytes.HasPrefix(data, []byte("MZ")) {
		return errors.New("executable attachments are not permitted")
	}
	return nil
}

func normalizeMarkdownUploadName(name string) string {
	ext := path.Ext(name)
	if strings.EqualFold(ext, ".md") {
		return strings.TrimSuffix(name, ext) + ".md"
	}
	return name
}

// filepathJoin keeps the upload implementation's paths rooted at the already
// resolved vault while preserving the public POSIX path contract.
func filepathJoin(root, rel string) string {
	if rel == "." || rel == "" {
		return root
	}
	return strings.ReplaceAll(root+"/"+rel, "/", string(os.PathSeparator))
}
