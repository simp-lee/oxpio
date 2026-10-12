package model

// RecommendationSignals contains the production recommendation contributions.
// The values are also exposed to themes and the Agent adapter so both surfaces
// describe the same ranked result.
type RecommendationSignals struct {
	Content float64
	Link    float64
	Tag     float64
}

// RelatedRecommendation is one ranked candidate in a recommendation result.
// RelPath is the canonical Vault-relative source identity; presentation data
// is resolved by the page or Agent adapter from the same site plan.
type RelatedRecommendation struct {
	RelPath string
	Score   float64
	Reasons []string
	Signals RecommendationSignals
}
