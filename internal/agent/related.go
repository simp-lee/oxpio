package agent

import (
	"fmt"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
)

// RecommendationSignals is the public, stable signal summary for a candidate.
type RecommendationSignals struct {
	Content float64 `json:"content"`
	Link    float64 `json:"link"`
	Tag     float64 `json:"tag"`
}

// RelatedCandidate is the Agent representation of a page recommendation.
type RelatedCandidate struct {
	Path    string                `json:"path"`
	Route   string                `json:"route"`
	Title   string                `json:"title"`
	Version string                `json:"version"`
	Score   float64               `json:"score"`
	Reasons []string              `json:"reasons"`
	Signals RecommendationSignals `json:"signals"`
}

type RelatedResult struct {
	Source     Source             `json:"source"`
	Partition  string             `json:"partition"`
	Candidates []RelatedCandidate `json:"candidates"`
}

type RelatedError struct {
	Code     string
	Category string
	Message  string
	Exit     int
	Cause    error
}

func (e RelatedError) Error() string { return e.Message }
func (e RelatedError) Unwrap() error { return e.Cause }
func (e RelatedError) ExitCode() int { return e.Exit }
func (e RelatedError) Diagnostic() Diagnostic {
	return Diagnostic{Code: e.Code, Severity: "error", Category: e.Category, Message: e.Message}
}

func invalidRelatedSourceError() error {
	return RelatedError{
		Code: "INVALID_ARGUMENT", Category: "parameter",
		Message: "related source must be an ordinary article", Exit: 2,
	}
}

// Related returns the production recommendation list for one public ordinary
// article. It never broadens the public query scope and does not use search.
func Related(snapshot internalapplication.Snapshot, relPath string) (RelatedResult, error) {
	entry, err := snapshot.Entry(relPath, false)
	if err != nil {
		return RelatedResult{}, err
	}
	if entry.Kind != "article" {
		return RelatedResult{}, invalidRelatedSourceError()
	}
	planned := snapshot.Analysis.Plan
	if planned == nil || planned.Index == nil {
		return RelatedResult{}, fmt.Errorf("normalized recommendation index is unavailable")
	}
	note := planned.Index.Notes[entry.RelPath]
	if note == nil || note.Frontmatter.Type != "doc" && note.Frontmatter.Type != "post" && note.Frontmatter.Type != "page" {
		return RelatedResult{}, invalidRelatedSourceError()
	}
	recommendations, err := internalapplication.RelatedRecommendations(snapshot)
	if err != nil {
		return RelatedResult{}, err
	}
	partition := note.VersionID
	if partition == "" {
		partition = "unversioned"
	}
	result := RelatedResult{Source: sourceDTO(entry), Partition: partition, Candidates: []RelatedCandidate{}}
	for _, recommendation := range recommendations[entry.RelPath] {
		target := planned.Index.Notes[recommendation.RelPath]
		if target == nil {
			continue
		}
		result.Candidates = append(result.Candidates, RelatedCandidate{
			Path: target.RelPath, Route: target.Route, Title: target.Frontmatter.Title, Version: target.VersionID,
			Score: recommendation.Score, Reasons: append([]string(nil), recommendation.Reasons...),
			Signals: RecommendationSignals{Content: recommendation.Signals.Content, Link: recommendation.Signals.Link, Tag: recommendation.Signals.Tag},
		})
	}
	return result, nil
}
