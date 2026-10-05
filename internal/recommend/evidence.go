package recommend

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/simp-lee/oxpio/internal/model"
	"golang.org/x/text/unicode/norm"
)

var (
	topicDatePattern    = regexp.MustCompile(`^\d{4}[-/.]\d{1,2}[-/.]\d{1,2}$`)
	topicVersionPattern = regexp.MustCompile(`(?i)^v(?:ersion)?\p{N}+(?:[._-]\p{N}+)*$`)
	topicUUIDPattern    = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// ContentParameters contains the calibration-controlled content gate limits.
type ContentParameters struct {
	MinCosine          float64
	MaxSingleTermRatio float64
}

// PairScore exposes the independently explainable recommendation signals.
type PairScore struct {
	Content float64
	Link    float64
	Tag     float64
	Final   float64
}

// TopicShape is the centralized lexical topic predicate.
func TopicShape(term string) bool {
	normalized := strings.ToLower(norm.NFKC.String(strings.TrimSpace(term)))
	if normalized == "" || utf8.RuneCountInString(normalized) > 48 {
		return false
	}
	if normalized == "c++" || normalized == "c#" {
		return true
	}
	if isPureNumber(normalized) || topicDatePattern.MatchString(normalized) ||
		topicVersionPattern.MatchString(normalized) || topicUUIDPattern.MatchString(normalized) ||
		isLongHexDigest(normalized) {
		return false
	}

	letterCount, hanCount, digitCount := 0, 0, 0
	for _, current := range normalized {
		switch {
		case unicode.Is(unicode.Han, current):
			hanCount++
		case unicode.IsLetter(current):
			letterCount++
		case unicode.IsNumber(current):
			digitCount++
		}
	}
	letters := letterCount + hanCount
	if letters < 2 {
		return false
	}
	return float64(letters)/float64(letters+digitCount) >= 0.5
}

func topicEvidence(term string, feature Feature) bool {
	if !TopicShape(term) {
		return false
	}
	return feature.Fields&(fieldTitle|fieldAliases|fieldHeadings) != 0
}

// ScorePair combines qualified content, source-only links, and valid tags.
func ScorePair(features *FeatureIndex, tags *TagSignalIndex, graph *model.LinkGraph, sourceDocID int, candidateDocID int, parameters ContentParameters) (PairScore, error) {
	if err := validateContentParameters(parameters); err != nil {
		return PairScore{}, err
	}
	if features == nil || sourceDocID < 0 || candidateDocID < 0 || sourceDocID >= len(features.Documents) || candidateDocID >= len(features.Documents) || sourceDocID == candidateDocID {
		return PairScore{}, nil
	}

	content, err := qualifiedContentScore(features, sourceDocID, candidateDocID, parameters)
	if err != nil {
		return PairScore{}, err
	}
	source := features.Documents[sourceDocID]
	candidate := features.Documents[candidateDocID]
	linkScore := LinkSignal(graph, source.RelPath, candidate.RelPath)
	tagScore := tags.Score(sourceDocID, candidateDocID)
	return PairScore{
		Content: content,
		Link:    linkScore,
		Tag:     tagScore,
		Final:   content + linkScore + tagScore,
	}, nil
}

func qualifiedContentScore(index *FeatureIndex, sourceDocID int, candidateDocID int, parameters ContentParameters) (float64, error) {
	left := index.Documents[sourceDocID].Features
	right := index.Documents[candidateDocID].Features
	cosine, err := SparseCosine(left, right)
	if err != nil {
		return 0, err
	}
	leftIndex, rightIndex := 0, 0
	sharedTerms := 0
	sharedTopicTerms := 0
	singleTermDF := -1
	singleTermStrong := false
	for leftIndex < len(left) && rightIndex < len(right) {
		switch {
		case left[leftIndex].TermID < right[rightIndex].TermID:
			leftIndex++
		case left[leftIndex].TermID > right[rightIndex].TermID:
			rightIndex++
		default:
			sharedTerms++
			termID := left[leftIndex].TermID
			if termID >= 0 && termID < len(index.Terms) && TopicShape(index.Terms[termID]) {
				sharedTopicTerms++
				if termID < len(index.CorpusDF) {
					singleTermDF = index.CorpusDF[termID]
				}
				singleTermStrong = topicEvidence(index.Terms[termID], left[leftIndex]) && topicEvidence(index.Terms[termID], right[rightIndex])
			}
			leftIndex++
			rightIndex++
		}
	}

	return qualifiedContentFromEvidence(
		cosine,
		sharedTerms,
		sharedTopicTerms,
		singleTermDF,
		singleTermStrong,
		len(index.Documents),
		parameters,
	), nil
}

func qualifiedContentFromEvidence(
	cosine float64,
	sharedTerms int,
	sharedTopicTerms int,
	singleTermDF int,
	singleTermStrong bool,
	documentCount int,
	parameters ContentParameters,
) float64 {
	if cosine < parameters.MinCosine || sharedTopicTerms == 0 {
		return 0
	}
	if sharedTerms >= 2 {
		return cosine
	}
	if sharedTerms != 1 || sharedTopicTerms != 1 || !singleTermStrong || singleTermDF < 0 {
		return 0
	}
	if documentCount < 20 {
		if singleTermDF <= 2 {
			return cosine
		}
		return 0
	}
	if float64(singleTermDF)/float64(documentCount) <= parameters.MaxSingleTermRatio {
		return cosine
	}
	return 0
}

func validateContentParameters(parameters ContentParameters) error {
	switch parameters.MinCosine {
	case 0.05, 0.08, 0.10:
	default:
		return fmt.Errorf("minimum content cosine must be one of 0.05, 0.08, or 0.10")
	}
	switch parameters.MaxSingleTermRatio {
	case 0.05, 0.10, 0.15:
	default:
		return fmt.Errorf("maximum single-term DF ratio must be one of 0.05, 0.10, or 0.15")
	}
	return nil
}

func isPureNumber(value string) bool {
	found := false
	for _, current := range value {
		if !unicode.IsNumber(current) {
			return false
		}
		found = true
	}
	return found
}

func isLongHexDigest(value string) bool {
	length := 0
	for _, current := range value {
		switch current {
		case '-', '_', ':', '/', '.':
			continue
		}
		if !isASCIIHex(current) {
			return false
		}
		length++
	}
	return length >= 16
}

func isASCIIHex(current rune) bool {
	return current >= '0' && current <= '9' || current >= 'a' && current <= 'f' || current >= 'A' && current <= 'F'
}
