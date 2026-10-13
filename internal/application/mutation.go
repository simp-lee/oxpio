package application

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalconfig "github.com/simp-lee/oxpio/internal/config"
	internaldiag "github.com/simp-lee/oxpio/internal/diag"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	internallink "github.com/simp-lee/oxpio/internal/link"
	internalmodel "github.com/simp-lee/oxpio/internal/model"
	internalpublish "github.com/simp-lee/oxpio/internal/publish"
	"github.com/simp-lee/oxpio/internal/siteplan"
	"github.com/simp-lee/oxpio/internal/workspace"
)

const (
	MutationWrite       = "write"
	MutationEdit        = "edit"
	MutationMove        = "move"
	MutationDelete      = "delete"
	MutationMkdir       = "mkdir"
	MutationRenameDir   = "rename-dir"
	MutationConfigWrite = "config write"
	MutationConfigEdit  = "config edit"
)

const AbsentHash = "absent"

type MutationChange struct {
	Kind       string  `json:"kind"`
	Domain     string  `json:"domain"`
	Path       string  `json:"path"`
	BeforeHash *string `json:"beforeHash"`
	AfterHash  *string `json:"afterHash"`
}

type SourceMutationRequest struct {
	Operation               string
	Path                    string
	Destination             string
	ExpectedHash            string
	DestinationExpectedHash string
	Content                 []byte
	DryRun                  bool
	IncludeUnpublished      bool
	Publish                 *bool
}

type MutationResult struct {
	Operation       string                    `json:"operation"`
	Path            string                    `json:"path"`
	Destination     string                    `json:"destination,omitempty"`
	Publish         *bool                     `json:"publish"`
	Changes         []MutationChange          `json:"changes"`
	Diagnostics     []internaldiag.Diagnostic `json:"-"`
	ImpactedPages   int                       `json:"impactedPages,omitempty"`
	ArtifactSummary map[string]int            `json:"artifacts,omitempty"`
	Next            string                    `json:"next,omitempty"`
}

// MutationError carries the analyzer's stable diagnostics without coupling the
// application package to the Agent wire DTOs.
type MutationError struct {
	Err         error
	Diagnostics []internaldiag.Diagnostic
}

func (e *MutationError) Error() string {
	if e == nil || e.Err == nil {
		return "mutation failed"
	}
	return e.Err.Error()
}
func (e *MutationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
func (e *MutationError) AnalysisDiagnostics() []internaldiag.Diagnostic {
	if e == nil {
		return nil
	}
	return append([]internaldiag.Diagnostic(nil), e.Diagnostics...)
}

type ConflictError struct {
	Path     string
	Expected string
	Actual   string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("CONFLICT: source %q expected %s, found %s", e.Path, e.Expected, e.Actual)
}

func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

var ErrConflict = errors.New("CONFLICT")

// MutateSource is the source-only Agent mutation service. It shares the
// analyzer, path boundary, workspace lock and fingerprint rules with formal
// publishing, but intentionally never calls a publisher or reload service.
func MutateSource(vaultPath, outputPath string, request SourceMutationRequest) (MutationResult, error) {
	boundary, err := mutationBoundary(vaultPath, outputPath)
	if err != nil {
		return MutationResult{}, err
	}
	if request.Operation == "" {
		return MutationResult{}, errors.New("mutation operation is required")
	}
	if request.DryRun {
		if err := workspace.CheckDryRun(boundary.VaultPath); err != nil {
			return MutationResult{}, err
		}
		return mutateSourceLocked(boundary, request, false)
	}
	lease, err := workspace.Acquire(boundary.VaultPath)
	if err != nil {
		return MutationResult{}, err
	}
	defer func() { _ = lease.Release() }()
	return mutateSourceLocked(boundary, request, true)
}

func mutationBoundary(vaultPath, outputPath string) (internalfsutil.VaultOutputBoundary, error) {
	vault, err := internalfsutil.ResolveVaultPath(vaultPath)
	if err != nil {
		return internalfsutil.VaultOutputBoundary{}, err
	}
	if strings.TrimSpace(outputPath) == "" {
		outputPath = filepath.Join(vault, "public")
	}
	return internalfsutil.ResolveVaultOutput(vault, outputPath)
}

func mutateSourceLocked(boundary internalfsutil.VaultOutputBoundary, request SourceMutationRequest, persistent bool) (MutationResult, error) {
	if request.Operation != MutationWrite && request.Operation != MutationEdit && request.Operation != MutationMove && request.Operation != MutationDelete {
		return MutationResult{}, fmt.Errorf("unsupported source mutation %q", request.Operation)
	}
	pathValue, err := NormalizeSourcePath(request.Path)
	if err != nil {
		return MutationResult{}, err
	}
	if request.ExpectedHash == "" {
		return MutationResult{}, errors.New("--if-hash is required")
	}
	if err := validateExpectedHash(request.ExpectedHash, true); err != nil {
		return MutationResult{}, err
	}
	if request.Operation == MutationMove {
		request.Destination, err = NormalizeSourcePath(request.Destination)
		if err != nil {
			return MutationResult{}, err
		}
		if request.DestinationExpectedHash == "" {
			return MutationResult{}, errors.New("--dest-if-hash is required")
		}
		if request.DestinationExpectedHash != AbsentHash {
			return MutationResult{}, errors.New("--dest-if-hash must be absent")
		}
		if pathValue == request.Destination {
			return MutationResult{}, errors.New("source and destination paths are identical")
		}
	}
	if request.Operation == MutationDelete && request.Publish != nil {
		return MutationResult{}, errors.New("delete does not accept publish state flags")
	}
	if request.Publish != nil && request.Operation == MutationMove {
		return MutationResult{}, errors.New("move does not accept publish state flags")
	}
	if request.Publish != nil && request.Operation != MutationWrite && request.Operation != MutationEdit {
		return MutationResult{}, errors.New("publish state flags are only valid for write and edit")
	}

	startFingerprint, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return MutationResult{}, err
	}
	current, exists, err := readMutationFile(boundary.VaultPath, pathValue)
	if err != nil {
		return MutationResult{}, err
	}
	beforeHash := AbsentHash
	if exists {
		beforeHash = hashBytes(current)
	}
	if beforeHash != request.ExpectedHash {
		return MutationResult{}, &ConflictError{Path: pathValue, Expected: request.ExpectedHash, Actual: beforeHash}
	}

	snapshot, err := OpenSnapshot(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return MutationResult{}, err
	}
	entry, entryErr := snapshot.Entry(pathValue, request.IncludeUnpublished)
	if request.Operation == MutationWrite || request.Operation == MutationEdit {
		if exists {
			if entryErr != nil {
				return MutationResult{}, entryErr
			}
			if entry.Kind != "article" && entry.Kind != "section" {
				return MutationResult{}, fmt.Errorf("source %q is not a Markdown source", pathValue)
			}
		} else {
			if request.Operation == MutationEdit {
				return MutationResult{}, &ConflictError{Path: pathValue, Expected: request.ExpectedHash, Actual: AbsentHash}
			}
			if strings.EqualFold(path.Base(pathValue), "_index.md") {
				return MutationResult{}, errors.New("write cannot create a section index; use upload --kind section-index")
			}
			if err := validateSourceParent(boundary.VaultPath, pathValue); err != nil {
				return MutationResult{}, err
			}
		}
	}
	if request.Operation == MutationMove || request.Operation == MutationDelete {
		if entryErr != nil {
			return MutationResult{}, entryErr
		}
		if entry.Kind != "article" {
			return MutationResult{}, fmt.Errorf("only ordinary article sources can be moved or deleted")
		}
		if request.Operation == MutationMove {
			if err := validateMoveDestination(boundary.VaultPath, snapshot, entry, request.Destination); err != nil {
				return MutationResult{}, err
			}
			if _, destinationExists, err := readMutationFile(boundary.VaultPath, request.Destination); err != nil {
				return MutationResult{}, err
			} else if destinationExists {
				return MutationResult{}, &ConflictError{Path: request.Destination, Expected: AbsentHash, Actual: "present"}
			}
		}
		if err := checkSourceReferences(snapshot, pathValue); err != nil {
			return MutationResult{}, err
		}
	}
	if exists && !entry.EffectivePublish && !request.IncludeUnpublished {
		return MutationResult{}, ErrHiddenSource
	}

	candidate := append([]byte(nil), current...)
	if request.Operation == MutationWrite {
		candidate = append([]byte(nil), request.Content...)
	}
	if request.Operation == MutationEdit {
		candidate, err = applyPatchBytes(current, request.Content)
		if err != nil {
			return MutationResult{}, err
		}
		if request.Publish != nil && entryErr == nil {
			if patchedPublish, present := sourcePublishScalar(candidate); present && patchedPublish != entry.Publish {
				return MutationResult{}, errors.New("publish override conflicts with an edit to the publish scalar")
			}
		}
	}
	if request.Operation == MutationWrite || request.Operation == MutationEdit {
		if !utf8.Valid(candidate) || bytes.IndexByte(candidate, 0) >= 0 {
			return MutationResult{}, ErrInvalidSource
		}
		candidate, err = applyPublishOverride(current, candidate, request.Publish)
		if err != nil {
			return MutationResult{}, err
		}
		if request.Operation == MutationWrite && !exists && request.Content == nil {
			return MutationResult{}, errors.New("source content is required")
		}
	}

	overlay := map[string][]byte{}
	deleted := map[string]bool{}
	candidatePath := pathValue
	switch request.Operation {
	case MutationWrite, MutationEdit:
		overlay[pathValue] = candidate
	case MutationMove:
		overlay[request.Destination] = append([]byte(nil), current...)
		deleted[pathValue] = true
		candidatePath = request.Destination
	case MutationDelete:
		deleted[pathValue] = true
	}
	analysis, analyzeErr := internalanalyze.AnalyzeWithOutputAndConcurrencyAndOverlay(boundary.VaultPath, boundary.OutputPath, 0, overlay, deleted)
	if analyzeErr != nil || hasAnalysisErrors(analysis.Diagnostics) || analysis.Plan == nil {
		diagnostics := analysis.Diagnostics
		if analysis.Plan != nil {
			diagnostics = redactMutationDiagnostics(diagnostics, sourceCatalogFromAnalysis(analysis.Plan))
		}
		if analyzeErr == nil {
			analyzeErr = internalanalyze.Failure(diagnostics)
		}
		return MutationResult{}, &MutationError{Err: analyzeErr, Diagnostics: diagnostics}
	}
	catalog := sourceCatalogFromAnalysis(analysis.Plan)
	safeDiagnostics := redactMutationDiagnostics(analysis.Diagnostics, catalog)
	var candidateEntry *internalmodel.SourceCatalogEntry
	for index := range catalog.Entries {
		if catalog.Entries[index].RelPath == candidatePath {
			candidateEntry = &catalog.Entries[index]
			break
		}
	}
	if request.Operation != MutationDelete && candidateEntry == nil {
		return MutationResult{}, &MutationError{Err: errors.New("candidate source is not present in the normalized catalog"), Diagnostics: analysis.Diagnostics}
	}
	if entryErr == nil && candidateEntry != nil && request.Publish == nil && candidateEntry.Publish != entry.Publish {
		return MutationResult{}, errors.New("changing publish requires --publish or --unpublish")
	}
	if request.Publish != nil && candidateEntry != nil && candidateEntry.Publish != *request.Publish {
		return MutationResult{}, errors.New("publish state override did not produce the requested state")
	}
	if !request.IncludeUnpublished && candidateEntry != nil && !candidateEntry.EffectivePublish {
		return MutationResult{}, ErrHiddenSource
	}

	endFingerprint, err := workspace.FingerprintWithOverlay(boundary.VaultPath, boundary.OutputPath, overlay, deleted)
	if err != nil {
		return MutationResult{}, err
	}
	currentFingerprint, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return MutationResult{}, err
	}
	if currentFingerprint != startFingerprint {
		return MutationResult{}, fmt.Errorf("CONCURRENT_INPUT: Vault inputs changed during mutation")
	}
	result := MutationResult{Operation: request.Operation, Path: pathValue, Destination: request.Destination, Next: "validate then explicitly publish", Diagnostics: safeDiagnostics, ImpactedPages: len(analysis.Plan.Plan.Pages), ArtifactSummary: map[string]int{"candidateSources": len(catalog.Entries)}}
	if candidateEntry != nil {
		value := candidateEntry.Publish
		result.Publish = &value
	}
	result.Changes = sourceMutationChanges(request.Operation, pathValue, request.Destination, beforeHash, candidate, exists)
	if request.DryRun {
		return result, nil
	}
	if err := recheckSourceMutationBeforeCommit(boundary, request, pathValue, beforeHash, startFingerprint); err != nil {
		return MutationResult{}, err
	}

	journal, err := sourceOnlyJournal(boundary, request.Operation, pathValue, request.Destination, beforeHash, candidate, exists, endFingerprint)
	if err != nil {
		return MutationResult{}, err
	}
	if err := workspace.WriteJournal(boundary.VaultPath, journal); err != nil {
		return MutationResult{}, err
	}
	if err := applySourceMutation(boundary.VaultPath, request.Operation, pathValue, request.Destination, candidate); err != nil {
		return MutationResult{}, err
	}
	if err := workspace.RecordCompleted(boundary.VaultPath, sourceCompletedPaths(boundary.VaultPath, request.Operation, pathValue, request.Destination, candidate)); err != nil {
		result.Diagnostics = append(result.Diagnostics, internaldiag.Diagnostic{Severity: internaldiag.SeverityWarning, Kind: internaldiag.KindOutputCleanup, Message: "source committed; watch suppression marker could not be recorded"})
	}
	journal.Phase = "source-committed"
	if request.Operation == MutationDelete {
		journal.SourceAfterHash = AbsentHash
		journal.AfterHash = AbsentHash
	}
	if err := workspace.WriteJournal(boundary.VaultPath, journal); err != nil {
		result.Diagnostics = append(result.Diagnostics, internaldiag.Diagnostic{Severity: internaldiag.SeverityWarning, Kind: internaldiag.KindOutputCleanup, Message: "source committed; transaction journal finalization requires recovery"})
		return result, nil
	}
	if err := workspace.ClearJournal(boundary.VaultPath); err != nil {
		result.Diagnostics = append(result.Diagnostics, internaldiag.Diagnostic{Severity: internaldiag.SeverityWarning, Kind: internaldiag.KindOutputCleanup, Message: "source committed; transaction journal cleanup requires recovery"})
	}
	if after, afterErr := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath); afterErr != nil {
		result.Diagnostics = append(result.Diagnostics, internaldiag.Diagnostic{Severity: internaldiag.SeverityWarning, Kind: internaldiag.KindOutputCleanup, Message: "source committed; final input fingerprint could not be checked"})
	} else if after != endFingerprint {
		result.Diagnostics = append(result.Diagnostics, internaldiag.Diagnostic{Severity: internaldiag.SeverityWarning, Kind: internaldiag.KindOutputCleanup, Message: "source committed while another input changed"})
	}
	return result, nil
}

func hasAnalysisErrors(diagnostics []internaldiag.Diagnostic) bool {
	for _, item := range diagnostics {
		if item.Severity == internaldiag.SeverityError {
			return true
		}
	}
	return false
}

func redactMutationDiagnostics(diagnostics []internaldiag.Diagnostic, catalog *internalmodel.SourceCatalog) []internaldiag.Diagnostic {
	if len(diagnostics) == 0 {
		return nil
	}
	prefixes := make([]string, 0)
	if catalog != nil {
		for _, entry := range catalog.Entries {
			if entry.EffectivePublish {
				continue
			}
			if entry.RelPath != "" {
				prefixes = append(prefixes, filepath.ToSlash(entry.RelPath))
			}
			if entry.SectionPath != "" && entry.SectionPath != "." {
				prefixes = append(prefixes, filepath.ToSlash(entry.SectionPath))
			}
		}
	}
	isHidden := func(value string) bool {
		value = strings.Trim(filepath.ToSlash(value), "/")
		for _, prefix := range prefixes {
			prefix = strings.Trim(prefix, "/")
			if value == prefix || strings.HasPrefix(value, prefix+"/") {
				return true
			}
		}
		return false
	}
	result := append([]internaldiag.Diagnostic(nil), diagnostics...)
	for index := range result {
		hidden := isHidden(result[index].Target) || isHidden(result[index].Location.Path)
		if string(result[index].Kind) == "unpublished_wikilink" {
			hidden = true
		}
		if hidden {
			result[index].Location = internaldiag.Location{}
			result[index].Target = ""
			result[index].Message = "target is not available in the selected read scope"
		}
	}
	return result
}

func sourceCatalogFromAnalysis(planned *siteplan.Result) *internalmodel.SourceCatalog {
	return internalbuild.SourceCatalogForPlan(planned)
}

func sourceMutationChanges(operation, source, destination, before string, candidate []byte, existed bool) []MutationChange {
	after := hashBytes(candidate)
	beforeValue, afterValue := stringPtrHash(before), stringPtrHash(after)
	switch operation {
	case MutationMove:
		return []MutationChange{{Kind: "delete", Domain: "source", Path: source, BeforeHash: beforeValue, AfterHash: nil}, {Kind: "create", Domain: "source", Path: destination, BeforeHash: nil, AfterHash: afterValue}}
	case MutationDelete:
		return []MutationChange{{Kind: "delete", Domain: "source", Path: source, BeforeHash: beforeValue, AfterHash: nil}}
	case MutationWrite, MutationEdit:
		kind := "update"
		if !existed {
			kind = "create"
		}
		return []MutationChange{{Kind: kind, Domain: "source", Path: source, BeforeHash: beforeValue, AfterHash: afterValue}}
	default:
		return nil
	}
}

func sourceOnlyJournal(boundary internalfsutil.VaultOutputBoundary, operation, source, destination, before string, candidate []byte, existed bool, fingerprint string) (workspace.Journal, error) {
	outputHash := internalpublish.DirectoryHash(boundary.OutputPath)
	identity := internalpublish.OutputIdentity(boundary.OutputPath)
	if outputHash == "unavailable" || identity == "" {
		return workspace.Journal{}, errors.New("cannot capture output identity for source mutation")
	}
	after := AbsentHash
	if operation != MutationDelete {
		after = hashBytes(candidate)
	}
	sourcePath := filepath.Join(boundary.VaultPath, filepath.FromSlash(source))
	beforePath := sourcePath
	if operation == MutationMove {
		sourcePath = filepath.Join(boundary.VaultPath, filepath.FromSlash(destination))
	}
	temporary := filepath.Join(filepath.Dir(boundary.OutputPath), ".oxpio-source-only-"+fingerprint[:minInt(16, len(fingerprint))])
	return workspace.Journal{Version: workspace.JournalVersion, CommitKind: "source-only", Phase: "prepared", InputFingerprint: fingerprint, TargetPath: boundary.OutputPath, SourcePath: sourcePath, SourceBeforePath: beforePath, OutputPath: boundary.OutputPath, TemporaryPath: temporary, BeforeHash: outputHash, OutputBeforeHash: outputHash, OutputIdentity: identity, SourceBeforeHash: before, SourceAfterHash: after, AfterHash: after}, nil
}

func recheckSourceMutationBeforeCommit(boundary internalfsutil.VaultOutputBoundary, request SourceMutationRequest, source, beforeHash, startFingerprint string) error {
	current, exists, err := readMutationFile(boundary.VaultPath, source)
	if err != nil {
		return err
	}
	actual := AbsentHash
	if exists {
		actual = hashBytes(current)
	}
	if actual != beforeHash {
		return &ConflictError{Path: source, Expected: beforeHash, Actual: actual}
	}
	if request.Operation == MutationMove {
		if _, exists, err := readMutationFile(boundary.VaultPath, request.Destination); err != nil {
			return err
		} else if exists {
			return &ConflictError{Path: request.Destination, Expected: AbsentHash, Actual: "present"}
		}
	}
	currentFingerprint, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return err
	}
	if currentFingerprint != startFingerprint {
		return fmt.Errorf("CONCURRENT_INPUT: Vault inputs changed during mutation")
	}
	return nil
}

func sourceCompletedPaths(vault, operation, source, destination string, candidate []byte) []workspace.CompletedPath {
	candidateHash := hashBytes(candidate)
	sourcePath := filepath.Join(vault, filepath.FromSlash(source))
	switch operation {
	case MutationMove:
		return []workspace.CompletedPath{{Path: sourcePath, AfterHash: AbsentHash}, {Path: filepath.Join(vault, filepath.FromSlash(destination)), AfterHash: candidateHash}}
	case MutationDelete:
		return []workspace.CompletedPath{{Path: sourcePath, AfterHash: AbsentHash}}
	default:
		return []workspace.CompletedPath{{Path: sourcePath, AfterHash: candidateHash}}
	}
}

func applySourceMutation(vault, operation, source, destination string, candidate []byte) error {
	sourcePath := filepath.Join(vault, filepath.FromSlash(source))
	switch operation {
	case MutationWrite, MutationEdit:
		return workspace.AtomicWriteFile(sourcePath, candidate, 0o644)
	case MutationMove:
		destinationPath := filepath.Join(vault, filepath.FromSlash(destination))
		if err := internalfsutil.RenameNoReplace(sourcePath, destinationPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				return &ConflictError{Path: destination, Expected: AbsentHash, Actual: "present"}
			}
			return err
		}
		return nil
	case MutationDelete:
		return os.Remove(sourcePath)
	default:
		return fmt.Errorf("unsupported source mutation %q", operation)
	}
}

func readMutationFile(vault, rel string) ([]byte, bool, error) {
	absolute := filepath.Join(vault, filepath.FromSlash(rel))
	_, data, _, err := internalfsutil.ReadContainedRegularFile(vault, absolute)
	if err == nil {
		return data, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, err
}

func validateSourceParent(vault, rel string) error {
	parent := path.Dir(rel)
	if parent == "." {
		return nil
	}
	_, _, err := internalfsutil.InspectContainedDirectory(vault, filepath.FromSlash(parent))
	if err != nil {
		return fmt.Errorf("parent directory %q is not available: %w", parent, err)
	}
	return nil
}

func validateMoveDestination(vault string, snapshot Snapshot, source internalmodel.SourceCatalogEntry, destination string) error {
	if strings.EqualFold(path.Base(destination), "_index.md") {
		return errors.New("cannot move an article to a section source")
	}
	if err := validateSourceParent(vault, destination); err != nil {
		return err
	}
	parent := path.Dir(destination)
	for _, entry := range snapshot.Catalog.Entries {
		if entry.Kind == "section" && entry.SectionPath == parent && entry.EffectivePublish {
			if source.VersionID == entry.VersionID {
				return nil
			}
		}
	}
	return fmt.Errorf("destination must be inside an existing section in the same version")
}

func checkSourceReferences(snapshot Snapshot, rel string) error {
	if snapshot.Analysis.Plan == nil || snapshot.Analysis.Plan.Index == nil {
		return nil
	}
	graph := internallink.BuildSourceGraph(sourceReferenceIndex(snapshot.Analysis.Plan.Index))
	if len(graph.Backward[rel]) != 0 {
		return &MutationError{
			Err: errors.New("source reference conflict"),
			Diagnostics: []internaldiag.Diagnostic{{
				Severity: internaldiag.SeverityError,
				Kind:     internaldiag.KindReferenceConflict,
				Message:  fmt.Sprintf("source %q is still referenced; repair links before move or delete", rel),
			}},
		}
	}
	return nil
}

func sourceReferenceIndex(index *internalmodel.VaultIndex) *internalmodel.VaultIndex {
	if index == nil {
		return nil
	}
	combined := &internalmodel.VaultIndex{
		AttachmentFolderPath: index.AttachmentFolderPath,
		Notes:                make(map[string]*internalmodel.Note, len(index.Notes)+len(index.Unpublished.Notes)),
		Sections:             index.Sections,
		SectionsBySource:     index.SectionsBySource,
		SectionsByRoute:      index.SectionsByRoute,
		NoteBySlug:           index.NoteBySlug,
		NoteByName:           make(map[string][]*internalmodel.Note, len(index.NoteByName)),
		AliasByName:          make(map[string][]*internalmodel.Note, len(index.AliasByName)),
		Tags:                 index.Tags,
		Assets:               index.Assets,
		AssetLookup:          index.AssetLookup,
		Resources:            index.Resources,
		ResourceBaseNames:    index.ResourceBaseNames,
		ResourceVersions:     index.ResourceVersions,
	}
	for relPath, note := range index.Notes {
		combined.Notes[relPath] = note
	}
	for relPath, note := range index.Unpublished.Notes {
		if _, exists := combined.Notes[relPath]; !exists {
			combined.Notes[relPath] = note
		}
	}
	for name, notes := range index.NoteByName {
		combined.NoteByName[name] = append([]*internalmodel.Note(nil), notes...)
	}
	for name, notes := range index.Unpublished.NoteByName {
		combined.NoteByName[name] = append(combined.NoteByName[name], notes...)
	}
	for name, notes := range index.AliasByName {
		combined.AliasByName[name] = append([]*internalmodel.Note(nil), notes...)
	}
	for name, notes := range index.Unpublished.AliasByName {
		combined.AliasByName[name] = append(combined.AliasByName[name], notes...)
	}
	for _, section := range index.Sections {
		if section == nil || strings.TrimSpace(section.SourcePath) == "" {
			continue
		}
		if _, exists := combined.Notes[section.SourcePath]; exists {
			continue
		}
		combined.Notes[section.SourcePath] = &internalmodel.Note{
			RelPath:       section.SourcePath,
			Route:         section.Route,
			VersionID:     section.VersionID,
			VersionRoutes: section.VersionRoutes,
			SectionPath:   section.RelPath,
			OutLinks:      append([]internalmodel.LinkRef(nil), section.OutLinks...),
			Embeds:        append([]internalmodel.EmbedRef(nil), section.Embeds...),
		}
	}
	return combined
}

func validateExpectedHash(value string, allowAbsent bool) error {
	if allowAbsent && value == AbsentHash {
		return nil
	}
	if len(value) != sha256.Size*2 {
		return errors.New("--if-hash must be a lowercase SHA-256 hash or absent")
	}
	if value != strings.ToLower(value) {
		return errors.New("--if-hash must be a lowercase SHA-256 hash or absent")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return errors.New("--if-hash must be a lowercase SHA-256 hash or absent")
	}
	return nil
}

func hashBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func stringPtrHash(value string) *string {
	if value == AbsentHash || value == "" {
		return nil
	}
	return &value
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var publishScalarPattern = regexp.MustCompile(`(?m)^([ \t]*publish[ \t]*:[ \t]*)(true|false)([ \t]*(?:#.*)?(?:\r?\n|$))`)

func sourcePublishScalar(source []byte) (bool, bool) {
	frontmatterEnd := sourceFrontmatterEnd(source)
	if frontmatterEnd < 0 {
		return false, false
	}
	match := publishScalarPattern.FindStringSubmatchIndex(string(source[:frontmatterEnd]))
	if match == nil {
		return false, false
	}
	value := string(source[match[4]:match[5]])
	return value == "true", true
}

func applyPublishOverride(original, candidate []byte, wanted *bool) ([]byte, error) {
	if wanted == nil {
		return candidate, nil
	}
	frontmatterEnd := sourceFrontmatterEnd(candidate)
	if frontmatterEnd < 0 {
		return nil, errors.New("publish override requires a YAML frontmatter document")
	}
	matches := publishScalarPattern.FindAllStringSubmatchIndex(string(candidate[:frontmatterEnd]), -1)
	if len(matches) != 1 {
		return nil, errors.New("publish override requires exactly one publish boolean scalar")
	}
	match := matches[0]
	valueStart, valueEnd := match[4], match[5]
	if valueStart < 0 || valueEnd < 0 {
		return nil, errors.New("publish override could not locate the publish scalar")
	}
	replacement := []byte("false")
	if *wanted {
		replacement = []byte("true")
	}
	result := make([]byte, 0, len(candidate)+len(replacement)-(valueEnd-valueStart))
	result = append(result, candidate[:valueStart]...)
	result = append(result, replacement...)
	result = append(result, candidate[valueEnd:]...)
	return result, nil
}

func sourceFrontmatterEnd(source []byte) int {
	if !bytes.HasPrefix(source, []byte("---\n")) && !bytes.HasPrefix(source, []byte("---\r\n")) {
		return -1
	}
	start := bytes.Index(source, []byte("\n---"))
	if start < 0 {
		return -1
	}
	end := start + len("\n---")
	for end < len(source) && source[end] != '\n' {
		end++
	}
	if end < len(source) {
		end++
	}
	return end
}

// applyPatchBytes applies Pi-style byte patches. Content is the strict JSON
// PatchRequest bytes, not source text.
func applyPatchBytes(source, request []byte) ([]byte, error) {
	patches, err := decodePatchRequest(request)
	if err != nil {
		return nil, err
	}
	type match struct {
		start, end  int
		replacement string
	}
	matches := make([]match, 0, len(patches))
	for _, patch := range patches {
		if *patch.OldText == "" {
			return nil, errors.New("edit oldText must be non-empty")
		}
		if !utf8.ValidString(*patch.OldText) || !utf8.ValidString(*patch.NewText) {
			return nil, errors.New("edit text must be valid UTF-8")
		}
		count := bytes.Count(source, []byte(*patch.OldText))
		if count != 1 {
			return nil, fmt.Errorf("edit oldText must match exactly once; found %d matches", count)
		}
		start := bytes.Index(source, []byte(*patch.OldText))
		matches = append(matches, match{start: start, end: start + len(*patch.OldText), replacement: *patch.NewText})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })
	for i := 1; i < len(matches); i++ {
		if matches[i].start < matches[i-1].end {
			return nil, errors.New("edit patches must not overlap or nest")
		}
	}
	result := append([]byte(nil), source...)
	for i := len(matches) - 1; i >= 0; i-- {
		result = append(result[:matches[i].start], append([]byte(matches[i].replacement), result[matches[i].end:]...)...)
	}
	return result, nil
}

type patchItem struct {
	OldText *string `json:"oldText"`
	NewText *string `json:"newText"`
}
type patchRequest struct {
	Edits *[]patchItem `json:"edits"`
}

func decodePatchRequest(data []byte) ([]patchItem, error) {
	if len(data) == 0 {
		return nil, errors.New("edits JSON is empty")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	var request patchRequest
	decoder := jsonNewDecoder(data)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("invalid edits JSON: %w", err)
	}
	if request.Edits == nil {
		return nil, errors.New("invalid edits JSON: edits must be an array")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("invalid edits JSON: trailing data")
		}
		return nil, fmt.Errorf("invalid edits JSON: %w", err)
	}
	patches := *request.Edits
	for _, patch := range patches {
		if patch.OldText == nil || patch.NewText == nil {
			return nil, errors.New("invalid edits JSON: oldText and newText must be strings")
		}
	}
	return patches, nil
}

// The JSON parser is kept in a separate file to make raw stdin handling
// auditable and to reject duplicate keys rather than accepting last-key-wins.
func jsonNewDecoder(data []byte) *json.Decoder { return json.NewDecoder(bytes.NewReader(data)) }

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case json.Delim:
			switch value {
			case '{':
				seen := map[string]struct{}{}
				for decoder.More() {
					keyToken, keyErr := decoder.Token()
					if keyErr != nil {
						return keyErr
					}
					key, ok := keyToken.(string)
					if !ok {
						return errors.New("invalid JSON object key")
					}
					if _, exists := seen[key]; exists {
						return fmt.Errorf("duplicate JSON key %q", key)
					}
					seen[key] = struct{}{}
					if err := walk(); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
				return err
			case '[':
				for decoder.More() {
					if err := walk(); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
				return err
			}
		}
		return nil
	}
	if err := walk(); err != nil {
		return fmt.Errorf("invalid edits JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("invalid edits JSON: trailing data")
		}
		return fmt.Errorf("invalid edits JSON: %w", err)
	}
	return nil
}

// ConfigSummary is deliberately redacted: passwordHash and absolute paths
// never cross the application/Agent boundary.
type ConfigSummary struct {
	Configured       bool   `json:"configured"`
	Hash             string `json:"hash"`
	Title            string `json:"title,omitempty"`
	BaseURL          string `json:"baseURL,omitempty"`
	Language         string `json:"language,omitempty"`
	Theme            string `json:"theme,omitempty"`
	Navigation       int    `json:"navigation"`
	Versions         int    `json:"versions"`
	Attachments      string `json:"attachments,omitempty"`
	EditorConfigured bool   `json:"editorConfigured"`
}

type ConfigMutationRequest struct {
	Operation    string
	ExpectedHash string
	Content      []byte
	DryRun       bool
}

func ReadConfig(vaultPath string) (ConfigSummary, error) {
	vault, err := internalfsutil.ResolveVaultPath(vaultPath)
	if err != nil {
		return ConfigSummary{}, err
	}
	data, err := readConfigBytes(vault)
	if err != nil {
		return ConfigSummary{}, err
	}
	cfg, err := internalconfig.ValidateConfigBytes(data)
	if err != nil {
		return ConfigSummary{}, err
	}
	return configSummary(cfg, hashBytes(data)), nil
}

func MutateConfig(vaultPath string, request ConfigMutationRequest) (MutationResult, error) {
	vault, err := internalfsutil.ResolveVaultPath(vaultPath)
	if err != nil {
		return MutationResult{}, err
	}
	if request.Operation != MutationConfigWrite && request.Operation != MutationConfigEdit {
		return MutationResult{}, fmt.Errorf("unsupported config mutation %q", request.Operation)
	}
	if request.ExpectedHash == "" {
		return MutationResult{}, errors.New("--if-hash is required")
	}
	if err := validateExpectedHash(request.ExpectedHash, true); err != nil {
		return MutationResult{}, err
	}
	if request.DryRun {
		if err := workspace.CheckDryRun(vault); err != nil {
			return MutationResult{}, err
		}
		return mutateConfigLocked(vault, request, false)
	}
	lease, err := workspace.Acquire(vault)
	if err != nil {
		return MutationResult{}, err
	}
	defer func() { _ = lease.Release() }()
	return mutateConfigLocked(vault, request, true)
}

func mutateConfigLocked(vault string, request ConfigMutationRequest, persistent bool) (MutationResult, error) {
	startFingerprint, err := workspace.Fingerprint(vault, filepath.Join(vault, "public"))
	if err != nil {
		return MutationResult{}, err
	}
	before, exists, err := readConfigBytesState(vault)
	if err != nil {
		return MutationResult{}, err
	}
	beforeHash := AbsentHash
	if exists {
		beforeHash = hashBytes(before)
	}
	if beforeHash != request.ExpectedHash {
		return MutationResult{}, &ConflictError{Path: internalconfig.Filename, Expected: request.ExpectedHash, Actual: beforeHash}
	}
	candidate := append([]byte(nil), request.Content...)
	if request.Operation == MutationConfigEdit {
		candidate, err = applyPatchBytes(before, request.Content)
		if err != nil {
			return MutationResult{}, err
		}
	}
	if !utf8.Valid(candidate) || bytes.IndexByte(candidate, 0) >= 0 {
		return MutationResult{}, errors.New("config must be UTF-8 without NUL bytes")
	}
	candidateConfig, err := internalconfig.ValidateConfigBytes(candidate)
	if err != nil {
		return MutationResult{}, err
	}
	if request.Operation == MutationConfigWrite && exists {
		beforeConfig, beforeErr := internalconfig.ValidateConfigBytes(before)
		if beforeErr != nil {
			return MutationResult{}, beforeErr
		}
		if beforeConfig.Editor != nil && (candidateConfig.Editor == nil || strings.TrimSpace(candidateConfig.Editor.PasswordHash) == "") {
			return MutationResult{}, errors.New("config write must include editor.passwordHash when replacing configured editor credentials")
		}
	}
	result := MutationResult{Operation: request.Operation, Path: internalconfig.Filename, Next: "validate then explicitly publish", Changes: []MutationChange{{Kind: "update", Domain: "config", Path: internalconfig.Filename, BeforeHash: stringPtrHash(beforeHash), AfterHash: stringPtrHash(hashBytes(candidate))}}}
	if !exists {
		result.Changes[0].Kind = "create"
	}
	currentFingerprint, err := workspace.Fingerprint(vault, filepath.Join(vault, "public"))
	if err != nil {
		return MutationResult{}, err
	}
	if currentFingerprint != startFingerprint {
		return MutationResult{}, fmt.Errorf("CONCURRENT_INPUT: Vault inputs changed during mutation")
	}
	candidateFingerprint, err := workspace.FingerprintWithOverlay(vault, filepath.Join(vault, "public"), map[string][]byte{internalconfig.Filename: candidate}, nil)
	if err != nil {
		return MutationResult{}, err
	}
	if request.DryRun {
		return result, nil
	}
	currentAfterRead, currentExists, err := readConfigBytesState(vault)
	if err != nil {
		return MutationResult{}, err
	}
	currentHash := AbsentHash
	if currentExists {
		currentHash = hashBytes(currentAfterRead)
	}
	if currentHash != beforeHash {
		return MutationResult{}, &ConflictError{Path: internalconfig.Filename, Expected: beforeHash, Actual: currentHash}
	}
	currentFingerprint, err = workspace.Fingerprint(vault, filepath.Join(vault, "public"))
	if err != nil {
		return MutationResult{}, err
	}
	if currentFingerprint != startFingerprint {
		return MutationResult{}, fmt.Errorf("CONCURRENT_INPUT: Vault inputs changed during mutation")
	}
	fingerprint := startFingerprint
	journal := workspace.Journal{Version: workspace.JournalVersion, CommitKind: "source-only", Phase: "prepared", InputFingerprint: fingerprint, TargetPath: filepath.Join(vault, "public"), SourcePath: filepath.Join(vault, internalconfig.Filename), SourceBeforePath: filepath.Join(vault, internalconfig.Filename), OutputPath: filepath.Join(vault, "public"), TemporaryPath: filepath.Join(vault, ".oxpio", ".oxpio-config-only-"+fingerprint[:minInt(16, len(fingerprint))]), SourceBeforeHash: beforeHash, SourceAfterHash: hashBytes(candidate), AfterHash: hashBytes(candidate), OutputBeforeHash: internalpublish.DirectoryHash(filepath.Join(vault, "public")), OutputIdentity: internalpublish.OutputIdentity(filepath.Join(vault, "public"))}
	if err := workspace.WriteJournal(vault, journal); err != nil {
		return MutationResult{}, err
	}
	if err := workspace.AtomicWriteFile(filepath.Join(vault, internalconfig.Filename), candidate, 0o644); err != nil {
		return MutationResult{}, err
	}
	if err := workspace.RecordCompleted(vault, []workspace.CompletedPath{{Path: filepath.Join(vault, internalconfig.Filename), AfterHash: hashBytes(candidate)}}); err != nil {
		result.Diagnostics = append(result.Diagnostics, internaldiag.Diagnostic{Severity: internaldiag.SeverityWarning, Kind: internaldiag.KindOutputCleanup, Message: "config committed; watch suppression marker could not be recorded"})
	}
	journal.Phase = "source-committed"
	if err := workspace.WriteJournal(vault, journal); err != nil {
		result.Diagnostics = append(result.Diagnostics, internaldiag.Diagnostic{Severity: internaldiag.SeverityWarning, Kind: internaldiag.KindOutputCleanup, Message: "config committed; transaction journal finalization requires recovery"})
		return result, nil
	}
	if err := workspace.ClearJournal(vault); err != nil {
		result.Diagnostics = append(result.Diagnostics, internaldiag.Diagnostic{Severity: internaldiag.SeverityWarning, Kind: internaldiag.KindOutputCleanup, Message: "config committed; transaction journal cleanup requires recovery"})
	}
	if after, afterErr := workspace.Fingerprint(vault, filepath.Join(vault, "public")); afterErr != nil {
		result.Diagnostics = append(result.Diagnostics, internaldiag.Diagnostic{Severity: internaldiag.SeverityWarning, Kind: internaldiag.KindOutputCleanup, Message: "config committed; final input fingerprint could not be checked"})
	} else if after != candidateFingerprint {
		result.Diagnostics = append(result.Diagnostics, internaldiag.Diagnostic{Severity: internaldiag.SeverityWarning, Kind: internaldiag.KindOutputCleanup, Message: "config committed while another input changed"})
	}
	return result, nil
}

func readConfigBytes(vault string) ([]byte, error) {
	data, exists, err := readConfigBytesState(vault)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("config %q was not found", internalconfig.Filename)
	}
	return data, nil
}
func readConfigBytesState(vault string) ([]byte, bool, error) {
	_, data, _, err := internalfsutil.ReadContainedRegularFile(vault, filepath.Join(vault, internalconfig.Filename))
	if err == nil {
		return data, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, err
}
func configSummary(cfg internalmodel.SiteConfig, hash string) ConfigSummary {
	versions := 0
	if cfg.Versions != nil {
		versions = len(cfg.Versions.Entries)
	}
	editor := cfg.Editor != nil
	return ConfigSummary{Configured: true, Hash: hash, Title: cfg.Title, BaseURL: cfg.BaseURL, Language: cfg.Language, Theme: cfg.Theme, Navigation: len(cfg.Navigation), Versions: versions, Attachments: cfg.AttachmentDirectory, EditorConfigured: editor}
}
