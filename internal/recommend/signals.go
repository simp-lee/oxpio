package recommend

import (
	"sort"

	"github.com/simp-lee/oxpio/internal/model"
)

const (
	oneWayLinkWeight = 0.20
	mutualLinkBonus  = 0.10
	tagSignalWeight  = 0.15
)

// TagSignalIndex stores deterministic normalized tag IDs and postings aligned
// to FeatureIndex document IDs.
type TagSignalIndex struct {
	Tags         []string
	DocumentTags [][]int
	Postings     [][]int
}

// BuildTagSignalIndex builds scoring/candidate tags for feature documents.
func BuildTagSignalIndex(documents []FeatureDocument, idx *model.VaultIndex) *TagSignalIndex {
	documentTags := make([]map[string]struct{}, len(documents))
	documentFrequency := make(map[string]int)
	for docID, document := range documents {
		unique := make(map[string]struct{})
		if idx != nil {
			if note := idx.Notes[document.RelPath]; note != nil {
				for _, rawTag := range note.Tags {
					tag := model.NormalizeTagName(rawTag)
					if tag != "" {
						unique[tag] = struct{}{}
					}
				}
			}
		}
		documentTags[docID] = unique
		for tag := range unique {
			documentFrequency[tag]++
		}
	}

	tags := make([]string, 0, len(documentFrequency))
	for tag, frequency := range documentFrequency {
		if len(documents) >= 20 && float64(frequency)/float64(len(documents)) > 0.50 {
			continue
		}
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	tagIDs := make(map[string]int, len(tags))
	for tagID, tag := range tags {
		tagIDs[tag] = tagID
	}

	index := &TagSignalIndex{
		Tags:         tags,
		DocumentTags: make([][]int, len(documents)),
		Postings:     make([][]int, len(tags)),
	}
	for docID, unique := range documentTags {
		tagList := make([]int, 0, len(unique))
		for tag := range unique {
			if tagID, ok := tagIDs[tag]; ok {
				tagList = append(tagList, tagID)
			}
		}
		sort.Ints(tagList)
		index.DocumentTags[docID] = tagList
		for _, tagID := range tagList {
			index.Postings[tagID] = append(index.Postings[tagID], docID)
		}
	}
	return index
}

// Score returns the fixed Jaccard tag contribution for two document IDs.
func (i *TagSignalIndex) Score(leftDocID int, rightDocID int) float64 {
	if i == nil || leftDocID < 0 || rightDocID < 0 || leftDocID >= len(i.DocumentTags) || rightDocID >= len(i.DocumentTags) {
		return 0
	}
	return tagScore(i.DocumentTags[leftDocID], i.DocumentTags[rightDocID])
}

func tagScore(left []int, right []int) float64 {
	leftIndex, rightIndex := 0, 0
	intersection := 0
	for leftIndex < len(left) && rightIndex < len(right) {
		switch {
		case left[leftIndex] < right[rightIndex]:
			leftIndex++
		case left[leftIndex] > right[rightIndex]:
			rightIndex++
		default:
			intersection++
			leftIndex++
			rightIndex++
		}
	}
	return tagScoreFromIntersection(len(left), len(right), intersection)
}

func tagScoreFromIntersection(leftCount int, rightCount int, intersection int) float64 {
	union := leftCount + rightCount - intersection
	if intersection <= 0 || union <= 0 {
		return 0
	}
	return tagSignalWeight * float64(intersection) / float64(union)
}

// LinkSignal returns direct forward/reverse contributions plus the mutual bonus.
func LinkSignal(graph *model.LinkGraph, sourcePath string, candidatePath string) float64 {
	if graph == nil || sourcePath == "" || candidatePath == "" || sourcePath == candidatePath {
		return 0
	}
	forward := sortedStringContains(graph.Forward[sourcePath], candidatePath)
	reverse := sortedStringContains(graph.Forward[candidatePath], sourcePath)
	return linkScoreFromDirections(forward, reverse)
}

func linkScoreFromDirections(forward bool, reverse bool) float64 {
	score := 0.0
	if forward {
		score += oneWayLinkWeight
	}
	if reverse {
		score += oneWayLinkWeight
	}
	if forward && reverse {
		score += mutualLinkBonus
	}
	return score
}

func sortedStringContains(values []string, want string) bool {
	position := sort.SearchStrings(values, want)
	return position < len(values) && values[position] == want
}
