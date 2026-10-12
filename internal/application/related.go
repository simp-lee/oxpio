package application

import (
	"errors"

	internalbuild "github.com/simp-lee/oxpio/internal/build"
	"github.com/simp-lee/oxpio/internal/model"
)

// RelatedRecommendations delegates the independent query to the same
// production recommendation pipeline used while rendering article pages.
func RelatedRecommendations(snapshot Snapshot) (map[string][]model.RelatedRecommendation, error) {
	if snapshot.Analysis.Plan == nil {
		return nil, errors.New("normalized recommendation plan is unavailable")
	}
	return internalbuild.RelatedRecommendations(snapshot.Analysis.Plan, 0)
}
