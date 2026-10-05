package recommend

import (
	"fmt"
	"path"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/simp-lee/oxpio/internal/model"
)

// EngineParameters contains internal ranking limits and testable worker control.
type EngineParameters struct {
	Features    FeatureParameters
	Content     ContentParameters
	Count       int
	WorkerCount int
}

// EngineDocument is the compact post-ranking state for one public document.
type EngineDocument struct {
	RelPath           string
	NormalizedRelPath string
	DisplayTitle      string
	Related           []RankedDocument
}

// EngineResult contains only compact documents and rankings; token/DF owners
// are not retained after BuildEngine returns.
type EngineResult struct {
	Documents []EngineDocument
}

const (
	candidateForwardLink uint8 = 1 << iota
	candidateReverseLink
)

type candidateScratch struct {
	dot               []float64
	sharedTerms       []uint16
	sharedTopicTerms  []uint16
	singleTermDF      []int
	singleTermStrong  []bool
	sharedTags        []uint32
	linkFlags         []uint8
	generation        []uint32
	currentGeneration uint32
	touched           []int
}

type pairObserver func(sourceDocID int, candidateDocID int)

// BuildEngine builds and scores the deterministic bounded recommendation index
// without consuming the caller's semantic documents.
func BuildEngine(semantics []model.RelatedSemanticDocument, idx *model.VaultIndex, graph *model.LinkGraph, parameters EngineParameters) (*EngineResult, error) {
	return buildEngine(semantics, idx, graph, parameters, nil)
}

// BuildEngineFromSemanticOwner consumes a build-owned semantic sidecar so its
// source text can be released immediately after field tokenization.
func BuildEngineFromSemanticOwner(owner *[]model.RelatedSemanticDocument, idx *model.VaultIndex, graph *model.LinkGraph, parameters EngineParameters) (*EngineResult, error) {
	return buildOwnedEngine(owner, idx, graph, parameters, nil)
}

func buildEngine(semantics []model.RelatedSemanticDocument, idx *model.VaultIndex, graph *model.LinkGraph, parameters EngineParameters, observer pairObserver) (*EngineResult, error) {
	if len(semantics) < 2 {
		return &EngineResult{}, nil
	}
	if err := validateEngineParameters(parameters); err != nil {
		return nil, err
	}

	features, err := BuildFeatureIndex(semantics, parameters.Features)
	if err != nil {
		return nil, err
	}
	tags := BuildTagSignalIndex(features.Documents, idx)
	return rankFeatureIndex(features, tags, idx, graph, parameters, observer)
}

func buildOwnedEngine(owner *[]model.RelatedSemanticDocument, idx *model.VaultIndex, graph *model.LinkGraph, parameters EngineParameters, observer pairObserver) (*EngineResult, error) {
	if owner == nil || len(*owner) < 2 {
		if owner != nil {
			clear(*owner)
			*owner = nil
		}
		return &EngineResult{}, nil
	}
	if err := validateEngineParameters(parameters); err != nil {
		clear(*owner)
		*owner = nil
		return nil, err
	}

	features, err := buildOwnedFeatureIndex(owner, parameters.Features)
	if err != nil {
		return nil, err
	}
	tags := BuildTagSignalIndex(features.Documents, idx)
	return rankFeatureIndex(features, tags, idx, graph, parameters, observer)
}

func validateEngineParameters(parameters EngineParameters) error {
	if parameters.Count < 1 || parameters.Count > 20 {
		return fmt.Errorf("related count must be between 1 and 20")
	}
	if err := validateFeatureParameters(parameters.Features); err != nil {
		return err
	}
	return validateContentParameters(parameters.Content)
}

func rankFeatureIndex(features *FeatureIndex, tags *TagSignalIndex, idx *model.VaultIndex, graph *model.LinkGraph, parameters EngineParameters, observer pairObserver) (*EngineResult, error) {
	if features == nil || len(features.Documents) < 2 {
		return &EngineResult{}, nil
	}

	documentCount := len(features.Documents)
	limit := parameters.Count
	if limit > documentCount-1 {
		limit = documentCount - 1
	}
	rankingDocuments := make([]rankingDocument, documentCount)
	docIDByPath := make(map[string]int, documentCount)
	for docID, document := range features.Documents {
		title := engineDisplayTitle(idx, document.RelPath)
		rankingDocuments[docID] = rankingDocument{
			normalizedPath: document.NormalizedRelPath,
			displayTitle:   title,
		}
		docIDByPath[document.RelPath] = docID
	}

	ranked := make([][]RankedDocument, documentCount)
	errorsByDoc := make([]error, documentCount)
	workerCount := normalizeEngineWorkerCount(parameters.WorkerCount, documentCount)
	runWorker := func(jobs <-chan int) {
		scratch := newCandidateScratch(documentCount)
		for sourceDocID := range jobs {
			ranked[sourceDocID], errorsByDoc[sourceDocID] = rankSourceDocument(
				features,
				tags,
				graph,
				docIDByPath,
				rankingDocuments,
				sourceDocID,
				limit,
				parameters.Content,
				observer,
				scratch,
			)
		}
	}

	if workerCount == 1 {
		jobs := make(chan int, documentCount)
		for docID := 0; docID < documentCount; docID++ {
			jobs <- docID
		}
		close(jobs)
		runWorker(jobs)
	} else {
		jobs := make(chan int)
		var workers sync.WaitGroup
		for worker := 0; worker < workerCount; worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				runWorker(jobs)
			}()
		}
		for docID := 0; docID < documentCount; docID++ {
			jobs <- docID
		}
		close(jobs)
		workers.Wait()
	}

	for docID, err := range errorsByDoc {
		if err != nil {
			return nil, fmt.Errorf("rank related candidates for %q: %w", features.Documents[docID].RelPath, err)
		}
	}

	result := &EngineResult{Documents: make([]EngineDocument, documentCount)}
	for docID, document := range features.Documents {
		result.Documents[docID] = EngineDocument{
			RelPath:           document.RelPath,
			NormalizedRelPath: document.NormalizedRelPath,
			DisplayTitle:      rankingDocuments[docID].displayTitle,
			Related:           ranked[docID],
		}
	}
	return result, nil
}

func rankSourceDocument(
	features *FeatureIndex,
	tags *TagSignalIndex,
	graph *model.LinkGraph,
	docIDByPath map[string]int,
	documents []rankingDocument,
	sourceDocID int,
	limit int,
	parameters ContentParameters,
	observer pairObserver,
	scratch *candidateScratch,
) ([]RankedDocument, error) {
	scratch.beginSource()
	source := features.Documents[sourceDocID]
	for _, feature := range source.Features {
		if feature.TermID < 0 || feature.TermID >= len(features.Postings) || feature.TermID >= len(features.Terms) {
			continue
		}
		term := features.Terms[feature.TermID]
		termHasTopicShape := TopicShape(term)
		termDF := -1
		if feature.TermID < len(features.CorpusDF) {
			termDF = features.CorpusDF[feature.TermID]
		}
		for _, posting := range features.Postings[feature.TermID] {
			candidateDocID := posting.DocID
			if candidateDocID == sourceDocID || !scratch.touch(candidateDocID) {
				continue
			}
			scratch.dot[candidateDocID] += feature.Weight * posting.Weight
			scratch.sharedTerms[candidateDocID]++
			if termHasTopicShape {
				scratch.sharedTopicTerms[candidateDocID]++
				if scratch.sharedTerms[candidateDocID] == 1 {
					scratch.singleTermDF[candidateDocID] = termDF
					scratch.singleTermStrong[candidateDocID] = topicEvidence(term, feature) &&
						posting.Fields&(fieldTitle|fieldAliases|fieldHeadings) != 0
				}
			}
		}
	}
	if graph != nil {
		for _, candidatePath := range graph.Forward[source.RelPath] {
			if candidateDocID, ok := docIDByPath[candidatePath]; ok && candidateDocID != sourceDocID && scratch.touch(candidateDocID) {
				scratch.linkFlags[candidateDocID] |= candidateForwardLink
			}
		}
		for _, candidatePath := range graph.Backward[source.RelPath] {
			if candidateDocID, ok := docIDByPath[candidatePath]; ok && candidateDocID != sourceDocID && scratch.touch(candidateDocID) {
				scratch.linkFlags[candidateDocID] |= candidateReverseLink
			}
		}
	}
	if tags != nil && sourceDocID < len(tags.DocumentTags) {
		for _, tagID := range tags.DocumentTags[sourceDocID] {
			if tagID < 0 || tagID >= len(tags.Postings) {
				continue
			}
			for _, candidateDocID := range tags.Postings[tagID] {
				if candidateDocID == sourceDocID || !scratch.touch(candidateDocID) {
					continue
				}
				scratch.sharedTags[candidateDocID]++
			}
		}
	}

	sort.Ints(scratch.touched)
	topK := newTopKHeap(limit, documents)
	for _, candidateDocID := range scratch.touched {
		if observer != nil {
			observer(sourceDocID, candidateDocID)
		}
		score, err := scratch.score(features, tags, sourceDocID, candidateDocID, parameters)
		if err != nil {
			return nil, fmt.Errorf("score %q: %w", features.Documents[candidateDocID].RelPath, err)
		}
		if score.Final <= 0 {
			continue
		}
		topK.add(RankedDocument{DocID: candidateDocID, Score: score.Final}, limit)
	}

	sort.Slice(topK.items, func(left int, right int) bool {
		return rankedDocumentIsBetter(topK.items[left], topK.items[right], documents)
	})
	return append([]RankedDocument(nil), topK.items...), nil
}

func newCandidateScratch(documentCount int) *candidateScratch {
	return &candidateScratch{
		dot:              make([]float64, documentCount),
		sharedTerms:      make([]uint16, documentCount),
		sharedTopicTerms: make([]uint16, documentCount),
		singleTermDF:     make([]int, documentCount),
		singleTermStrong: make([]bool, documentCount),
		sharedTags:       make([]uint32, documentCount),
		linkFlags:        make([]uint8, documentCount),
		generation:       make([]uint32, documentCount),
		touched:          make([]int, 0, documentCount),
	}
}

func (s *candidateScratch) beginSource() {
	s.touched = s.touched[:0]
	s.currentGeneration++
	if s.currentGeneration == 0 {
		clear(s.generation)
		s.currentGeneration = 1
	}
}

func (s *candidateScratch) touch(docID int) bool {
	if s == nil || docID < 0 || docID >= len(s.generation) {
		return false
	}
	if s.generation[docID] == s.currentGeneration {
		return true
	}
	s.generation[docID] = s.currentGeneration
	s.dot[docID] = 0
	s.sharedTerms[docID] = 0
	s.sharedTopicTerms[docID] = 0
	s.singleTermDF[docID] = -1
	s.singleTermStrong[docID] = false
	s.sharedTags[docID] = 0
	s.linkFlags[docID] = 0
	s.touched = append(s.touched, docID)
	return true
}

func (s *candidateScratch) score(features *FeatureIndex, tags *TagSignalIndex, sourceDocID int, candidateDocID int, parameters ContentParameters) (PairScore, error) {
	cosine, err := boundedCosine(s.dot[candidateDocID])
	if err != nil {
		return PairScore{}, err
	}
	content := qualifiedContentFromEvidence(
		cosine,
		int(s.sharedTerms[candidateDocID]),
		int(s.sharedTopicTerms[candidateDocID]),
		s.singleTermDF[candidateDocID],
		s.singleTermStrong[candidateDocID],
		len(features.Documents),
		parameters,
	)
	flags := s.linkFlags[candidateDocID]
	linkScore := linkScoreFromDirections(flags&candidateForwardLink != 0, flags&candidateReverseLink != 0)
	tagScore := 0.0
	if tags != nil && sourceDocID < len(tags.DocumentTags) && candidateDocID < len(tags.DocumentTags) {
		tagScore = tagScoreFromIntersection(
			len(tags.DocumentTags[sourceDocID]),
			len(tags.DocumentTags[candidateDocID]),
			int(s.sharedTags[candidateDocID]),
		)
	}
	return PairScore{
		Content: content,
		Link:    linkScore,
		Tag:     tagScore,
		Final:   content + linkScore + tagScore,
	}, nil
}

func normalizeEngineWorkerCount(requested int, documentCount int) int {
	if documentCount <= 0 {
		return 0
	}
	maxWorkers := runtime.GOMAXPROCS(0)
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	if maxWorkers > 8 {
		maxWorkers = 8
	}
	if requested <= 0 || requested > maxWorkers {
		requested = maxWorkers
	}
	if requested < 1 {
		requested = 1
	}
	if requested > documentCount {
		requested = documentCount
	}
	return requested
}

func engineDisplayTitle(idx *model.VaultIndex, relPath string) string {
	if idx != nil {
		if note := idx.Notes[relPath]; note != nil {
			if title := strings.TrimSpace(note.Frontmatter.Title); title != "" {
				return title
			}
		}
	}
	base := path.Base(strings.ReplaceAll(relPath, `\`, "/"))
	if base == "." || base == "" || base == "/" {
		return ""
	}
	return strings.TrimSuffix(base, path.Ext(base))
}
