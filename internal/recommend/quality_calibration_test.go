package recommend

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/diag"
	"github.com/simp-lee/oxpio/internal/link"
	internalmarkdown "github.com/simp-lee/oxpio/internal/markdown"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/slug"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"
)

var qualityWikilinkPattern = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

type qualityRankingCorpus struct {
	Semantics  []model.RelatedSemanticDocument
	Index      *model.VaultIndex
	Graph      *model.LinkGraph
	IDByPath   map[string]string
	PathByID   map[string]string
	SourceByID map[string]qualitySource
}

func TestCalibrationJudgments(t *testing.T) {
	manifest, err := loadQualityManifest(qualityAssetRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQualityManifest(qualityAssetRoot, manifest); err != nil {
		t.Fatal(err)
	}
	corpus, err := loadQualityRankingCorpus(manifest, "calibration")
	if err != nil {
		t.Fatal(err)
	}
	unions, err := calibrationTopTenUnions(corpus)
	if err != nil {
		t.Fatal(err)
	}

	calibrationSets := 0
	for _, language := range []string{"zh-hans", "zh-hant", "en", "mixed"} {
		reference, ok := qualityLabelReference(manifest, "calibration", language)
		if !ok {
			continue
		}
		calibrationSets++
		language := language
		t.Run(language, func(t *testing.T) {
			set, err := loadQualityJudgmentSet(reference)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateCalibrationJudgmentCoverage(set, corpus, unions, language); err != nil {
				t.Fatal(err)
			}
		})
	}
	if calibrationSets != 4 {
		t.Fatalf("calibration label sets = %d, want 4", calibrationSets)
	}
	t.Run("complete", func(t *testing.T) {
		totalJudgments := 0
		for _, reference := range manifest.LabelSets {
			if reference.Split != "calibration" {
				continue
			}
			set, err := loadQualityJudgmentSet(reference)
			if err != nil {
				t.Fatal(err)
			}
			totalJudgments += len(set.Sources)
		}
		if totalJudgments != 40 {
			t.Fatalf("complete calibration judgments = %d, want 40", totalJudgments)
		}
	})
}

func loadQualityRankingCorpus(manifest qualityManifest, split string) (qualityRankingCorpus, error) {
	corpus := qualityRankingCorpus{
		Index: &model.VaultIndex{
			Notes:       make(map[string]*model.Note),
			NoteByName:  make(map[string][]*model.Note),
			AliasByName: make(map[string][]*model.Note),
		},
		IDByPath:   make(map[string]string),
		PathByID:   make(map[string]string),
		SourceByID: make(map[string]qualitySource),
	}

	parser := internalmarkdown.NewParser(diag.NewCollector())
	for _, source := range manifest.Sources {
		if source.Split != split {
			continue
		}
		assetPath, err := qualityAssetPath(qualityAssetRoot, source.Path)
		if err != nil {
			return qualityRankingCorpus{}, err
		}
		data, err := os.ReadFile(assetPath)
		if err != nil {
			return qualityRankingCorpus{}, err
		}
		frontmatter, body, err := parseQualityMarkdown(data)
		if err != nil {
			return qualityRankingCorpus{}, fmt.Errorf("parse quality source %s: %w", source.ID, err)
		}
		relPath := strings.TrimPrefix(source.Path, split+"/")
		sourceBytes := []byte(body)
		root := parser.Parser().Parse(text.NewReader(sourceBytes))
		headings, bodyText := internalmarkdown.RelatedSemanticText(root, sourceBytes)
		corpus.Semantics = append(corpus.Semantics, model.RelatedSemanticDocument{
			RelPath:  relPath,
			Title:    source.Title,
			Aliases:  append([]string(nil), frontmatter.Aliases...),
			Headings: headings,
			Body:     bodyText,
		})
		note := &model.Note{
			RelPath: relPath,
			Aliases: append([]string(nil), frontmatter.Aliases...),
			Tags:    append([]string(nil), frontmatter.Tags...),
			Frontmatter: model.Frontmatter{
				Title: source.Title,
			},
			OutLinks: qualitySourceLinks(body),
		}
		corpus.Index.Notes[relPath] = note
		name := strings.TrimSuffix(path.Base(relPath), path.Ext(relPath))
		corpus.Index.NoteByName[slug.Canonicalize(name)] = append(corpus.Index.NoteByName[slug.Canonicalize(name)], note)
		for _, alias := range note.Aliases {
			key := slug.Canonicalize(alias)
			corpus.Index.AliasByName[key] = append(corpus.Index.AliasByName[key], note)
		}
		corpus.IDByPath[relPath] = source.ID
		corpus.PathByID[source.ID] = relPath
		corpus.SourceByID[source.ID] = source
	}
	corpus.Graph = link.BuildSourceGraph(corpus.Index)
	return corpus, nil
}

func calibrationTopTenUnions(corpus qualityRankingCorpus) (map[string]map[string]struct{}, error) {
	unions := make(map[string]map[string]struct{}, len(corpus.Semantics))
	for _, parameters := range qualityParameterGrid() {
		result, err := BuildEngine(corpus.Semantics, corpus.Index, corpus.Graph, parameters)
		if err != nil {
			return nil, err
		}
		for _, document := range result.Documents {
			sourceID := corpus.IDByPath[document.RelPath]
			if unions[sourceID] == nil {
				unions[sourceID] = make(map[string]struct{})
			}
			for _, ranked := range document.Related {
				candidateID := corpus.IDByPath[result.Documents[ranked.DocID].RelPath]
				if candidateID != "" {
					unions[sourceID][candidateID] = struct{}{}
				}
			}
		}
	}
	return unions, nil
}

func qualityParameterGrid() []EngineParameters {
	parameters := make([]EngineParameters, 0, 81)
	for _, maxFeatures := range []int{32, 48, 64} {
		for _, maxDFRatio := range []float64{0.40, 0.50, 0.60} {
			for _, minCosine := range []float64{0.05, 0.08, 0.10} {
				for _, maxSingleRatio := range []float64{0.05, 0.10, 0.15} {
					parameters = append(parameters, EngineParameters{
						Features:    FeatureParameters{MaxFeatures: maxFeatures, MaxDFRatio: maxDFRatio},
						Content:     ContentParameters{MinCosine: minCosine, MaxSingleTermRatio: maxSingleRatio},
						Count:       10,
						WorkerCount: 1,
					})
				}
			}
		}
	}
	return parameters
}

func validateCalibrationJudgmentCoverage(set qualityJudgmentSet, corpus qualityRankingCorpus, unions map[string]map[string]struct{}, language string) error {
	if set.PoolKind != "calibration-grid" || set.GridTupleCount != 81 || set.Split != "calibration" || set.Language != language {
		return fmt.Errorf("invalid calibration judgment header for %s", language)
	}
	judgments := make(map[string]qualitySourceJudgment, len(set.Sources))
	for _, judgment := range set.Sources {
		judgments[judgment.SourceID] = judgment
	}
	wantSources := 0
	for sourceID, source := range corpus.SourceByID {
		if source.Split != "calibration" || source.Language != language {
			continue
		}
		wantSources++
		judgment, ok := judgments[sourceID]
		if !ok {
			return fmt.Errorf("missing calibration judgment for %s", sourceID)
		}
		candidates := make(map[string]qualityCandidateJudgment, len(judgment.Candidates))
		independentlyDiscovered := false
		for _, candidate := range judgment.Candidates {
			candidates[candidate.CandidateID] = candidate
			for _, discovery := range candidate.Discovery {
				if discovery != "production-top10" {
					independentlyDiscovered = true
				}
			}
		}
		for candidateID := range unions[sourceID] {
			candidate, ok := candidates[candidateID]
			if !ok {
				return fmt.Errorf("calibration pool %s omits grid Top-10 candidate %s", sourceID, candidateID)
			}
			if !qualityStringContains(candidate.Discovery, "production-top10") {
				return fmt.Errorf("calibration candidate %s -> %s lacks production discovery", sourceID, candidateID)
			}
		}
		if !independentlyDiscovered {
			return fmt.Errorf("calibration source %s lacks active independent discovery", sourceID)
		}
	}
	if len(set.Sources) != wantSources {
		return fmt.Errorf("calibration %s judgment count = %d, want %d", language, len(set.Sources), wantSources)
	}
	return nil
}

func qualityLabelReference(manifest qualityManifest, split string, language string) (qualityLabelSetReference, bool) {
	for _, reference := range manifest.LabelSets {
		if reference.Split == split && reference.Language == language {
			return reference, true
		}
	}
	return qualityLabelSetReference{}, false
}

func loadQualityJudgmentSet(reference qualityLabelSetReference) (qualityJudgmentSet, error) {
	assetPath, err := qualityAssetPath(qualityAssetRoot, reference.Path)
	if err != nil {
		return qualityJudgmentSet{}, err
	}
	data, err := os.ReadFile(assetPath)
	if err != nil {
		return qualityJudgmentSet{}, err
	}
	var set qualityJudgmentSet
	if err := json.Unmarshal(data, &set); err != nil {
		return qualityJudgmentSet{}, err
	}
	return set, nil
}

type qualityMarkdownFrontmatter struct {
	Tags    []string `yaml:"tags"`
	Aliases []string `yaml:"aliases"`
}

func parseQualityMarkdown(data []byte) (qualityMarkdownFrontmatter, string, error) {
	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		return qualityMarkdownFrontmatter{}, content, nil
	}
	rest := content[len("---\n"):]
	separator := strings.Index(rest, "\n---\n")
	if separator < 0 {
		return qualityMarkdownFrontmatter{}, "", fmt.Errorf("missing frontmatter terminator")
	}
	var frontmatter qualityMarkdownFrontmatter
	if err := yaml.Unmarshal([]byte(rest[:separator]), &frontmatter); err != nil {
		return qualityMarkdownFrontmatter{}, "", err
	}
	return frontmatter, rest[separator+len("\n---\n"):], nil
}

func qualitySourceLinks(body string) []model.LinkRef {
	matches := qualityWikilinkPattern.FindAllStringSubmatch(body, -1)
	links := make([]model.LinkRef, 0, len(matches))
	for _, match := range matches {
		inside := match[1]
		if separator := strings.IndexByte(inside, '|'); separator >= 0 {
			inside = inside[:separator]
		}
		target, fragment, _ := strings.Cut(inside, "#")
		target = strings.TrimSpace(target)
		fragment = strings.TrimSpace(fragment)
		rawTarget := target
		if fragment != "" {
			rawTarget += "#" + fragment
		}
		links = append(links, model.LinkRef{RawTarget: rawTarget, Fragment: fragment})
	}
	return links
}

func qualityStringContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
