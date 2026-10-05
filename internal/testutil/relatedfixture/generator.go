package relatedfixture

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/simp-lee/oxpio/internal/model"
)

const Seed int64 = 20260406

const GeneratorVersion = 2

const (
	CaseMixed           = "mixed"
	CaseSparsePosting   = "sparse-posting"
	CaseTermCoverage40  = "term-40%-boundary"
	CaseTermCoverage49  = "term-49%-coverage"
	CaseTagCoverage49   = "tag-49%-coverage"
	CaseRejectedContent = "rejected-content"
)

// Manifest freezes generator inputs independently from generated corpus size.
type Manifest struct {
	Version int            `json:"version"`
	Seed    int64          `json:"seed"`
	Cases   []CaseSpec     `json:"cases"`
	Samples []CorpusDigest `json:"samples"`
	SHA256  string         `json:"sha256"`
}

// CorpusDigest freezes representative generated output for one named size.
type CorpusDigest struct {
	Name   string `json:"name"`
	Count  int    `json:"count"`
	SHA256 string `json:"sha256"`
}

// CaseSpec defines one deterministic performance fixture family.
type CaseSpec struct {
	Name            string `json:"name"`
	MinimumTokens   int    `json:"minimumTokens"`
	Topics          int    `json:"topics"`
	Languages       int    `json:"languages"`
	SharedTerms     int    `json:"sharedTerms"`
	CoveragePercent int    `json:"coveragePercent,omitempty"`
}

// Fixture is an in-memory public corpus ready for recommendation benchmarks.
type Fixture struct {
	Semantics []model.RelatedSemanticDocument
	Index     *model.VaultIndex
	Graph     *model.LinkGraph
}

// FrozenManifest returns the canonical case specification.
func FrozenManifest() Manifest {
	manifest := Manifest{
		Version: GeneratorVersion,
		Seed:    Seed,
		Cases: []CaseSpec{
			{Name: CaseMixed, MinimumTokens: 200, Topics: 5, Languages: 4, SharedTerms: 2},
			{Name: CaseSparsePosting, MinimumTokens: 2000, Topics: 1, Languages: 1, SharedTerms: 2},
			{Name: CaseTermCoverage40, MinimumTokens: 200, Topics: 1, Languages: 1, SharedTerms: 8, CoveragePercent: 40},
			{Name: CaseTermCoverage49, MinimumTokens: 200, Topics: 1, Languages: 1, SharedTerms: 8, CoveragePercent: 49},
			{Name: CaseTagCoverage49, MinimumTokens: 200, Topics: 5, Languages: 4, SharedTerms: 2, CoveragePercent: 49},
			{Name: CaseRejectedContent, MinimumTokens: 200, Topics: 1, Languages: 1, SharedTerms: 1},
		},
		Samples: []CorpusDigest{
			{Name: CaseMixed, Count: 20, SHA256: "1247a2cac179100b17a184a92aff64a77abd6b69932351dd4f175f70e93c1393"},
			{Name: CaseSparsePosting, Count: 10, SHA256: "7754aaa57a10518bddf98c28fd96a76cb476bba11f5581e5af1bfe90f10d9091"},
			{Name: CaseTermCoverage40, Count: 100, SHA256: "f86240ddfc84c423795984a3f235c9d9b279bbaf1121863bad605cb4230d90eb"},
			{Name: CaseTermCoverage49, Count: 100, SHA256: "ffd6db4a125100997f2741a40ff31a7fb1a8167e9bd582b9b70712c45d1bdbb4"},
			{Name: CaseTagCoverage49, Count: 100, SHA256: "0eccbd90262a0014bdb61695d766bfa8ba9174de0f1cdc43e8827b79ba6a764d"},
			{Name: CaseRejectedContent, Count: 100, SHA256: "838f0ef42c48eb5928640f338dc6a60bd5ce39760c3da28b21d3def663fb3040"},
		},
	}
	manifest.SHA256 = ManifestHash(manifest)
	return manifest
}

// ManifestHash hashes canonical generator fields, excluding the hash field.
func ManifestHash(manifest Manifest) string {
	cases := append([]CaseSpec(nil), manifest.Cases...)
	sort.Slice(cases, func(left int, right int) bool { return cases[left].Name < cases[right].Name })
	hasher := sha256.New()
	_, _ = fmt.Fprintf(hasher, "version=%d\nseed=%d\n", manifest.Version, manifest.Seed)
	for _, current := range cases {
		_, _ = fmt.Fprintf(hasher, "%s|%d|%d|%d|%d|%d\n", current.Name, current.MinimumTokens, current.Topics, current.Languages, current.SharedTerms, current.CoveragePercent)
	}
	samples := append([]CorpusDigest(nil), manifest.Samples...)
	sort.Slice(samples, func(left int, right int) bool { return samples[left].Name < samples[right].Name })
	for _, sample := range samples {
		_, _ = fmt.Fprintf(hasher, "sample|%s|%d|%s\n", sample.Name, sample.Count, sample.SHA256)
	}
	return fmt.Sprintf("%x", hasher.Sum(nil))
}

// LoadManifest loads and verifies a fixed performance manifest.
func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.SHA256 != ManifestHash(manifest) {
		return Manifest{}, fmt.Errorf("performance manifest hash mismatch")
	}
	return manifest, nil
}

// CorpusHash hashes generated semantics and tags in stable document order.
func CorpusHash(fixture Fixture) string {
	hasher := sha256.New()
	for _, document := range fixture.Semantics {
		_, _ = fmt.Fprintf(hasher, "%s\x00%s\x00%s\x00", document.RelPath, document.Title, document.Body)
		if fixture.Index != nil {
			if note := fixture.Index.Notes[document.RelPath]; note != nil {
				for _, tag := range note.Tags {
					_, _ = fmt.Fprintf(hasher, "%s\x00", tag)
				}
			}
		}
	}
	return fmt.Sprintf("%x", hasher.Sum(nil))
}

// Generate builds exactly one deterministic fixture case.
func Generate(name string, documentCount int) (Fixture, error) {
	if documentCount < 0 {
		return Fixture{}, fmt.Errorf("document count must not be negative")
	}
	spec, ok := caseSpecification(name)
	if !ok {
		return Fixture{}, fmt.Errorf("unknown related fixture case %q", name)
	}
	fixture := Fixture{
		Semantics: newRelatedSemanticFixtureOwner(documentCount),
		Index:     &model.VaultIndex{Notes: make(map[string]*model.Note, documentCount)},
		Graph: &model.LinkGraph{
			Forward:  make(map[string][]string, documentCount),
			Backward: make(map[string][]string, documentCount),
		},
	}
	coverage := documentCount * spec.CoveragePercent / 100
	for docID := 0; docID < documentCount; docID++ {
		relPath := fmt.Sprintf("notes/%05d.md", docID)
		body, tags, err := generateDocument(spec, docID, coverage)
		if err != nil {
			return Fixture{}, err
		}
		fixture.Semantics[docID] = model.RelatedSemanticDocument{
			RelPath: relPath,
			Title:   fmt.Sprintf("Document %05d", docID),
			Body:    body,
		}
		fixture.Index.Notes[relPath] = &model.Note{
			RelPath: relPath,
			Slug:    fmt.Sprintf("notes/%05d", docID),
			Tags:    tags,
			Frontmatter: model.Frontmatter{
				Title: fmt.Sprintf("Document %05d", docID),
			},
		}
		fixture.Graph.Forward[relPath] = []string{}
		fixture.Graph.Backward[relPath] = []string{}
	}
	return fixture, nil
}

//go:noinline
func newRelatedSemanticFixtureOwner(count int) []model.RelatedSemanticDocument {
	return make([]model.RelatedSemanticDocument, count)
}

func generateDocument(spec CaseSpec, docID int, coverage int) (string, []string, error) {
	switch spec.Name {
	case CaseMixed:
		return mixedBody(docID, spec.MinimumTokens), mixedTags(docID), nil
	case CaseSparsePosting:
		return sparseBody(docID, spec.MinimumTokens), nil, nil
	case CaseTermCoverage40, CaseTermCoverage49:
		return termCoverageBody(docID, coverage, spec.MinimumTokens), nil, nil
	case CaseTagCoverage49:
		tags := mixedTags(docID)
		if docID < coverage {
			tags = append(tags, "coverage-a", "coverage-b", "coverage-c", "coverage-d")
		}
		return mixedBody(docID, spec.MinimumTokens), tags, nil
	case CaseRejectedContent:
		pairTerm := fmt.Sprintf("rejectedtopic%d%04d", Seed%1000, docID/2)
		return repeatedTokens([]string{pairTerm}, spec.MinimumTokens), nil, nil
	default:
		return "", nil, fmt.Errorf("unknown related fixture case %q", spec.Name)
	}
}

func caseSpecification(name string) (CaseSpec, bool) {
	for _, spec := range FrozenManifest().Cases {
		if spec.Name == name {
			return spec, true
		}
	}
	return CaseSpec{}, false
}

func mixedBody(docID int, tokenCount int) string {
	topic := docID % 5
	language := docID % 4
	shared := [4][5][2]string{
		{{"数据库", "一致性"}, {"可靠性", "告警"}, {"知识", "导航"}, {"发酵", "面包"}, {"城市", "步行"}},
		{{"資料庫", "一致性"}, {"可靠性", "告警"}, {"知識", "導航"}, {"發酵", "麵包"}, {"城市", "步行"}},
		{{"database", "consistency"}, {"reliability", "alerts"}, {"knowledge", "navigation"}, {"fermentation", "bread"}, {"urban", "walking"}},
		{{"database", "一致性"}, {"reliability", "告警"}, {"knowledge", "導航"}, {"fermentation", "麵包"}, {"urban", "步行"}},
	}
	unique := fmt.Sprintf("unique%d%05d", Seed%1000, docID)
	return repeatedTokens([]string{shared[language][topic][0], shared[language][topic][1], unique}, tokenCount)
}

func sparseBody(docID int, tokenCount int) string {
	var builder strings.Builder
	builder.Grow(tokenCount * 18)
	pairID := docID / 2
	_, _ = fmt.Fprintf(&builder, "sparsetopic%d%05da sparsetopic%d%05db ", Seed%1000, pairID, Seed%1000, pairID)
	for tokenID := 2; tokenID < tokenCount; tokenID++ {
		_, _ = fmt.Fprintf(&builder, "u%d%05dx%04d ", Seed%1000, docID, tokenID)
	}
	return builder.String()
}

func termCoverageBody(docID int, coverage int, tokenCount int) string {
	tokens := []string{fmt.Sprintf("unique%d%05d", Seed%1000, docID)}
	if docID < coverage {
		for index := 0; index < 8; index++ {
			tokens = append(tokens, fmt.Sprintf("coverage-term-%d", index))
		}
	}
	return repeatedTokens(tokens, tokenCount)
}

func repeatedTokens(tokens []string, count int) string {
	if len(tokens) == 0 || count <= 0 {
		return ""
	}
	var builder strings.Builder
	for index := 0; index < count; index++ {
		builder.WriteString(tokens[index%len(tokens)])
		builder.WriteByte(' ')
	}
	return builder.String()
}

func mixedTags(docID int) []string {
	return []string{fmt.Sprintf("topic-%d", docID%5), fmt.Sprintf("language-%d", docID%4)}
}
